import '../mock-vscode';
import { strict as assert } from 'node:assert';
import { describe, it, beforeEach, afterEach } from 'mocha';
import {
	resetMockState,
	mockWebviewPanels,
	mockCalls,
	MockSecretStorage,
	MockMemento,
	type MockWebviewPanel,
} from '../mock-vscode';
import { AddServerPanel } from '../../webview/add-server-panel';
import { CredentialStore } from '../../credential-store';
import type { IGatewayClient } from '../../extension';

interface TrackedCall { method: string; args: unknown[] }

function createTrackingClient(opts: { fail?: boolean; failMessage?: string } = {}) {
	const calls: TrackedCall[] = [];
	const client = {
		calls,
		listServers: async () => [],
		getHealth: async () => ({ status: 'ok', servers: 0, running: 0 }),
		getServer: async () => ({}),
		addServer: async (name: string, config: unknown) => {
			calls.push({ method: 'addServer', args: [name, config] });
			if (opts.fail) { throw new Error(opts.failMessage ?? 'mock failure'); }
			return { status: 'ok' };
		},
		removeServer: async () => ({}),
		patchServer: async () => ({}),
		restartServer: async () => ({}),
		resetCircuit: async () => ({}),
		callTool: async () => ({ content: null }),
		listTools: async () => [],
	};
	return client as IGatewayClient & { calls: TrackedCall[] };
}

function createMockContext() {
	return {
		secrets: new MockSecretStorage(),
		globalState: new MockMemento(),
		subscriptions: [] as Array<{ dispose(): void }>,
		extensionUri: { scheme: 'file', path: '/test', with: () => ({}), toString: () => 'file:///test' },
	};
}

function latestPanel(): MockWebviewPanel {
	assert.ok(mockWebviewPanels.length > 0, 'expected a webview panel to be created');
	return mockWebviewPanels[mockWebviewPanels.length - 1];
}

async function openPanel(
	client: IGatewayClient,
	credStore: CredentialStore,
	onCreated: () => void = () => {},
): Promise<MockWebviewPanel> {
	const ctx = createMockContext();
	await AddServerPanel.createOrShow(ctx.extensionUri as any, client, credStore, onCreated);
	return latestPanel();
}

function simulateSubmit(panel: MockWebviewPanel, payload: unknown): void {
	panel.webview._simulateMessage({ type: 'submit', payload });
}

async function flush(): Promise<void> {
	await new Promise((r) => setImmediate(r));
	await new Promise((r) => setImmediate(r));
}

describe('AddServerPanel', () => {
	let ctx: ReturnType<typeof createMockContext>;
	let credStore: CredentialStore;

	beforeEach(() => {
		resetMockState();
		AddServerPanel._reset();
		ctx = createMockContext();
		credStore = new CredentialStore(ctx as any);
	});

	afterEach(() => {
		AddServerPanel._reset();
	});

	describe('panel lifecycle', () => {
		it('creates a webview panel with CSP nonce and scripts enabled', async () => {
			const client = createTrackingClient();
			const panel = await openPanel(client, credStore);
			assert.equal(panel.viewType, 'mcpAddServer');
			assert.equal(panel.title, 'Add MCP Server');
			assert.ok(panel.webview.html.includes('<title>Add Server</title>'));
			assert.ok(panel.webview.html.includes('Content-Security-Policy'));
			assert.ok(!panel.webview.html.includes("'unsafe-inline'"));
		});

		it('is a singleton — second createOrShow reveals the existing panel', async () => {
			const client = createTrackingClient();
			const ctx1 = createMockContext();
			const ctx2 = createMockContext();
			await AddServerPanel.createOrShow(ctx1.extensionUri as any, client, credStore, () => {});
			const firstCount = mockWebviewPanels.length;
			await AddServerPanel.createOrShow(ctx2.extensionUri as any, client, credStore, () => {});
			assert.equal(mockWebviewPanels.length, firstCount, 'second call must not create a new panel');
		});

		it('cancel message disposes the panel', async () => {
			const client = createTrackingClient();
			const panel = await openPanel(client, credStore);
			panel.webview._simulateMessage({ type: 'cancel' });
			await flush();
			assert.ok(panel.disposed);
		});

		it('ignores malformed messages without crashing', async () => {
			const client = createTrackingClient();
			const panel = await openPanel(client, credStore);
			panel.webview._simulateMessage(null);
			panel.webview._simulateMessage('string');
			panel.webview._simulateMessage({ type: 'unknown' });
			await flush();
			assert.equal(client.calls.length, 0);
			assert.ok(!panel.disposed);
		});
	});

	describe('transport auto-detect', () => {
		it('http:// target creates HTTP server config', async () => {
			const client = createTrackingClient();
			let created = false;
			const panel = await openPanel(client, credStore, () => { created = true; });
			simulateSubmit(panel, {
				name: 'http-srv',
				target: 'http://localhost:3000/mcp',
				env: [],
				headers: [],
			});
			await flush();
			assert.equal(client.calls.length, 1);
			assert.equal(client.calls[0].method, 'addServer');
			assert.deepEqual(client.calls[0].args, ['http-srv', { url: 'http://localhost:3000/mcp' }]);
			assert.ok(created);
			assert.ok(panel.disposed, 'panel disposes on success');
		});

		it('https:// target is treated as http transport', async () => {
			const client = createTrackingClient();
			const panel = await openPanel(client, credStore);
			simulateSubmit(panel, {
				name: 'https-srv',
				target: 'https://api.example.com/v1/mcp',
				env: [],
				headers: [],
			});
			await flush();
			assert.deepEqual(client.calls[0].args, [
				'https-srv',
				{ url: 'https://api.example.com/v1/mcp' },
			]);
			assert.ok(panel.disposed);
		});

		it('absolute path target creates stdio server config', async () => {
			const client = createTrackingClient();
			const panel = await openPanel(client, credStore);
			const command = process.platform === 'win32' ? 'C:\\bin\\server.exe' : '/usr/bin/server';
			simulateSubmit(panel, {
				name: 'stdio-srv',
				target: command,
				env: [],
				headers: [],
			});
			await flush();
			assert.equal(client.calls.length, 1);
			assert.deepEqual(client.calls[0].args, ['stdio-srv', { command }]);
		});
	});

	describe('env and headers', () => {
		it('env entries are stored and indexed as credentials', async () => {
			const client = createTrackingClient();
			const panel = await openPanel(client, credStore);
			simulateSubmit(panel, {
				name: 'env-srv',
				target: '/usr/bin/srv',
				env: ['API_KEY=secret', 'DEBUG=1'],
				headers: [],
			});
			await flush();
			assert.equal(client.calls.length, 1);
			const [, config] = client.calls[0].args as [string, Record<string, unknown>];
			assert.deepEqual(config.env, ['API_KEY=secret', 'DEBUG=1']);

			const creds = await credStore.getServerCredentials('env-srv');
			assert.equal(creds.env.API_KEY, 'secret');
			assert.equal(creds.env.DEBUG, '1');
		});

		it('header entries are merged into a map and indexed', async () => {
			const client = createTrackingClient();
			const panel = await openPanel(client, credStore);
			simulateSubmit(panel, {
				name: 'hdr-srv',
				target: 'http://localhost:3000/mcp',
				env: [],
				headers: ['Authorization: Bearer tok', 'X-Custom: val'],
			});
			await flush();
			const [, config] = client.calls[0].args as [string, Record<string, unknown>];
			assert.deepEqual(config.headers, {
				Authorization: 'Bearer tok',
				'X-Custom': 'val',
			});

			const creds = await credStore.getServerCredentials('hdr-srv');
			assert.equal(creds.headers.Authorization, 'Bearer tok');
			assert.equal(creds.headers['X-Custom'], 'val');
		});

		it('partial credential failure still creates the server and warns', async () => {
			const client = createTrackingClient();
			// Override storeEnvVar to fail for one key.
			const realStore = credStore.storeEnvVar.bind(credStore);
			let firstCall = true;
			(credStore as any).storeEnvVar = async (server: string, key: string, value: string) => {
				if (firstCall) { firstCall = false; throw new Error('keychain locked'); }
				return realStore(server, key, value);
			};
			const panel = await openPanel(client, credStore);
			simulateSubmit(panel, {
				name: 'partial-srv',
				target: '/usr/bin/srv',
				env: ['BAD=x', 'GOOD=y'],
				headers: [],
			});
			await flush();
			assert.equal(client.calls.length, 1, 'server must still be created');
			assert.ok(mockCalls.warningMessages.some((m) => m.includes('failed to index credential "BAD"')));
			const creds = await credStore.getServerCredentials('partial-srv');
			assert.equal(creds.env.GOOD, 'y');
			assert.equal(creds.env.BAD, undefined);
			assert.ok(panel.disposed, 'credential failure should not block panel disposal on success');
		});
	});

	describe('server-side re-validation', () => {
		it('rejects malformed server name and nacks the webview', async () => {
			const client = createTrackingClient();
			const panel = await openPanel(client, credStore);
			simulateSubmit(panel, {
				name: '../evil',
				target: '/usr/bin/srv',
				env: [],
				headers: [],
			});
			await flush();
			assert.equal(client.calls.length, 0);
			const nacks = panel._postedMessages.filter(
				(m) => (m as { type?: string }).type === 'nack');
			assert.equal(nacks.length, 1);
			assert.ok(!panel.disposed);
		});

		it('rejects malformed http URL (space in URL)', async () => {
			const client = createTrackingClient();
			const panel = await openPanel(client, credStore);
			simulateSubmit(panel, {
				name: 'srv',
				target: 'http://host with spaces/x',
				env: [],
				headers: [],
			});
			await flush();
			assert.equal(client.calls.length, 0);
			const nacks = panel._postedMessages.filter(
				(m) => (m as { type?: string }).type === 'nack');
			assert.equal(nacks.length, 1);
		});

		it('ignores webview transport field — always recomputes from target (F-05 fix)', async () => {
			const client = createTrackingClient();
			const panel = await openPanel(client, credStore);
			// Crafted payload says transport=http but target looks like a stdio path.
			// Extension must recompute transport and reject it via validateStdioCommand.
			simulateSubmit(panel, {
				name: 'srv',
				target: 'ftp://not-an-http-url',
				transport: 'http', // ← ignored by coercePayload
				env: [],
				headers: [],
			});
			await flush();
			assert.equal(client.calls.length, 0);
			const nacks = panel._postedMessages.filter(
				(m) => (m as { type?: string }).type === 'nack') as Array<{ error?: string }>;
			assert.equal(nacks.length, 1);
			// Rejection reason must be the stdio path error, NOT the URL error —
			// proves the webview transport field was ignored.
			assert.ok(
				nacks[0].error && nacks[0].error.includes('absolute path'),
				`expected stdio rejection, got: ${nacks[0].error}`);
		});

		it('rejects relative stdio command', async () => {
			const client = createTrackingClient();
			const panel = await openPanel(client, credStore);
			simulateSubmit(panel, {
				name: 'srv',
				target: 'relative/path',
				env: [],
				headers: [],
			});
			await flush();
			assert.equal(client.calls.length, 0);
		});

		it('rejects invalid env entry (bad key chars)', async () => {
			const client = createTrackingClient();
			const panel = await openPanel(client, credStore);
			simulateSubmit(panel, {
				name: 'srv',
				target: '/usr/bin/srv',
				env: ['bad-key=v'],
				headers: [],
			});
			await flush();
			assert.equal(client.calls.length, 0);
		});

		it('rejects invalid header entry (bad name chars)', async () => {
			const client = createTrackingClient();
			const panel = await openPanel(client, credStore);
			simulateSubmit(panel, {
				name: 'srv',
				target: 'http://localhost:3000',
				headers: ['Bad Name: val'],
				env: [],
			});
			await flush();
			assert.equal(client.calls.length, 0);
		});

		it('coerces missing arrays to empty without crashing', async () => {
			const client = createTrackingClient();
			const panel = await openPanel(client, credStore);
			simulateSubmit(panel, {
				name: 'srv',
				target: '/usr/bin/srv',
			});
			await flush();
			assert.equal(client.calls.length, 1);
			const [, config] = client.calls[0].args as [string, Record<string, unknown>];
			assert.equal(config.env, undefined);
			assert.equal(config.headers, undefined);
		});

		it('nacks on non-object payload', async () => {
			const client = createTrackingClient();
			const panel = await openPanel(client, credStore);
			panel.webview._simulateMessage({ type: 'submit', payload: 'not-an-object' });
			await flush();
			assert.equal(client.calls.length, 0);
			const nacks = panel._postedMessages.filter(
				(m) => (m as { type?: string }).type === 'nack');
			assert.equal(nacks.length, 1);
		});
	});

	describe('client failure', () => {
		it('nacks when client.addServer throws and panel stays open', async () => {
			const client = createTrackingClient({ fail: true, failMessage: 'connection refused' });
			const panel = await openPanel(client, credStore);
			simulateSubmit(panel, {
				name: 'fail-srv',
				target: '/usr/bin/srv',
				env: [],
				headers: [],
			});
			await flush();
			assert.equal(client.calls.length, 1);
			const nacks = panel._postedMessages.filter(
				(m) => (m as { type?: string }).type === 'nack') as Array<{ error?: string }>;
			assert.equal(nacks.length, 1);
			assert.ok(nacks[0].error && nacks[0].error.includes('connection refused'));
			assert.ok(!panel.disposed, 'panel stays open on failure so user can correct');
		});
	});

	describe('concurrency and lifecycle (HIGH fixes from cross-review)', () => {
		it('drops a second submit while the first is in-flight (F-01 guard)', async () => {
			// Slow client — we control when addServer resolves.
			let resolve!: () => void;
			const gate = new Promise<void>((r) => { resolve = r; });
			const calls: TrackedCall[] = [];
			const slowClient = {
				calls,
				listServers: async () => [],
				getHealth: async () => ({}),
				getServer: async () => ({}),
				addServer: async (name: string, config: unknown) => {
					calls.push({ method: 'addServer', args: [name, config] });
					await gate;
					return { status: 'ok' };
				},
				removeServer: async () => ({}),
				patchServer: async () => ({}),
				restartServer: async () => ({}),
				resetCircuit: async () => ({}),
				callTool: async () => ({ content: null }),
				listTools: async () => [],
			} as IGatewayClient & { calls: TrackedCall[] };

			const panel = await openPanel(slowClient, credStore);
			const payload = { name: 'slow', target: '/usr/bin/x', env: [], headers: [] };
			simulateSubmit(panel, payload);
			// Immediately dispatch a second submit while the first awaits.
			simulateSubmit(panel, payload);
			await new Promise((r) => setImmediate(r));
			// Exactly one addServer call must have occurred so far.
			assert.equal(slowClient.calls.length, 1, 'second submit must be dropped by in-flight guard');
			resolve();
			await flush();
			assert.equal(slowClient.calls.length, 1, 'still one after gate resolves');
		});

		it('re-reveal updates onCreated callback (F-02 fix)', async () => {
			const client = createTrackingClient();
			let firstCalled = 0;
			let secondCalled = 0;
			const ctx1 = createMockContext();
			const ctx2 = createMockContext();
			// First createOrShow with callback A.
			await AddServerPanel.createOrShow(
				ctx1.extensionUri as any, client, credStore, () => { firstCalled++; });
			// Second createOrShow with callback B — panel is re-revealed, callback must update.
			await AddServerPanel.createOrShow(
				ctx2.extensionUri as any, client, credStore, () => { secondCalled++; });
			const panel = latestPanel();
			simulateSubmit(panel, {
				name: 'reveal-srv',
				target: '/usr/bin/x',
				env: [],
				headers: [],
			});
			await flush();
			assert.equal(client.calls.length, 1);
			assert.equal(firstCalled, 0, 'old callback must not fire');
			assert.equal(secondCalled, 1, 'new callback must fire on re-reveal');
		});

		it('onCreated exception does not leak the panel (F-03 fix)', async () => {
			const client = createTrackingClient();
			const ctx1 = createMockContext();
			await AddServerPanel.createOrShow(
				ctx1.extensionUri as any, client, credStore, () => {
					throw new Error('callback boom');
				});
			const panel = latestPanel();
			simulateSubmit(panel, {
				name: 'boom-srv',
				target: '/usr/bin/x',
				env: [],
				headers: [],
			});
			await flush();
			assert.equal(client.calls.length, 1, 'server must be created before callback runs');
			assert.ok(panel.disposed, 'panel must be disposed even when callback throws');
			assert.ok(mockCalls.errorMessages.some((m) => m.includes('callback boom')));
		});
	});
});
