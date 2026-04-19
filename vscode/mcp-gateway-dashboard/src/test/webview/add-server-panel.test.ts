import '../mock-vscode';
import { strict as assert } from 'node:assert';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { describe, it, beforeEach, afterEach } from 'mocha';
import {
	resetMockState,
	mockWebviewPanels,
	mockCalls,
	mockConfigValues,
	MockSecretStorage,
	MockMemento,
	type MockWebviewPanel,
} from '../mock-vscode';
import { AddServerPanel } from '../../webview/add-server-panel';
import { _resetSchemaCacheForTests } from '../../catalog';
import { CredentialStore } from '../../credential-store';
import type { IGatewayClient } from '../../extension';
import { createTmpDir, cleanupTmpDir } from '../helpers/tmpdir';

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

function createMockContext(fsPath: string = '/test') {
	return {
		secrets: new MockSecretStorage(),
		globalState: new MockMemento(),
		subscriptions: [] as Array<{ dispose(): void }>,
		// fsPath is consumed by AddServerPanel.resolveCatalogDir (CB.1/CB.4).
		// Default '/test' is a non-existent path so loadServersCatalog returns an
		// empty result with warnings — the panel renders fine without a catalog.
		extensionUri: { scheme: 'file', path: fsPath, fsPath, with: () => ({}), toString: () => `file://${fsPath}` },
	};
}

/**
 * CB.1/CB.4 fixture helper — stage a catalog directory under
 * `<root>/docs/catalog/servers.json` and return the root path so callers can
 * point `extensionUri.fsPath` at it (bundled fallback) or feed it directly into
 * `mcpGateway.catalogPath` (operator override).
 */
function stageCatalogFixture(root: string, entries: unknown[]): string {
	const dir = path.join(root, 'docs', 'catalog');
	fs.mkdirSync(dir, { recursive: true });
	fs.writeFileSync(path.join(dir, 'servers.json'), JSON.stringify(entries), 'utf8');
	return dir;
}

function collectPosted<T = Record<string, unknown>>(
	panel: MockWebviewPanel,
	type: string,
): T[] {
	return panel._postedMessages.filter(
		(m) => (m as { type?: string }).type === type,
	) as T[];
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

/**
 * Poll until a posted message of the given `type` appears on the panel, or the
 * deadline is hit. The init message dispatches after an async catalog load
 * (fs.open + stat + read + close), which needs more than two setImmediate
 * ticks on Windows. Default 500 ms — generous enough for the fs round-trip
 * even on a busy CI box, well under Mocha's 2 s per-test timeout.
 *
 * Throws on timeout — see code-review finding LOW-2 (Round 1, Sonnet 4.6). A
 * silent return turns a missing message into a confusing "0 === 1" assertion
 * far from the root cause; the throw surfaces the wait-site as the failure.
 */
async function waitForPostedMessage(
	panel: MockWebviewPanel,
	type: string,
	maxMs: number = 500,
): Promise<void> {
	const deadline = Date.now() + maxMs;
	while (Date.now() < deadline) {
		if (panel._postedMessages.some((m) => (m as { type?: string }).type === type)) {
			return;
		}
		await new Promise((r) => setTimeout(r, 5));
	}
	throw new Error(`waitForPostedMessage: timed out waiting for "${type}" after ${maxMs}ms`);
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

	// CB.0 — baseline regression group (3 cases): covers the pre-catalog free-form
	// flow (no catalogId in payload) so the catalog additions in CB.1–CB.5 can
	// layer on without re-asserting the same pre-existing behaviour elsewhere.
	describe('catalog regression (no catalog selection)', () => {
		it('free-form stdio submit without catalogId still creates server', async () => {
			const client = createTrackingClient();
			const panel = await openPanel(client, credStore);
			const command = process.platform === 'win32' ? 'C:\\bin\\cat-regression.exe' : '/usr/bin/cat-regression';
			simulateSubmit(panel, {
				name: 'regression-stdio',
				target: command,
				env: [],
				headers: [],
				// catalogId intentionally omitted — simulates the Phase 11.C free-form path.
			});
			await flush();
			assert.equal(client.calls.length, 1);
			assert.deepEqual(client.calls[0].args, ['regression-stdio', { command }]);
			assert.ok(panel.disposed);
		});

		it('free-form http submit without catalogId still creates server', async () => {
			const client = createTrackingClient();
			const panel = await openPanel(client, credStore);
			simulateSubmit(panel, {
				name: 'regression-http',
				target: 'https://example.test/mcp',
				env: [],
				headers: [],
			});
			await flush();
			assert.equal(client.calls.length, 1);
			assert.deepEqual(
				client.calls[0].args,
				['regression-http', { url: 'https://example.test/mcp' }],
			);
			assert.ok(panel.disposed);
		});

		it('free-form submit with invalid name still rejects via server-side re-validation', async () => {
			const client = createTrackingClient();
			const panel = await openPanel(client, credStore);
			simulateSubmit(panel, {
				name: '../evil',
				target: '/usr/bin/regression',
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

	// CB.5 — catalog features: init message, pre-fill via catalogId, host
	// re-validation of forged ids, XSS safety, operator-path override, and the
	// 11.C in-flight guard across a catalog switch.
	describe('catalog browse (CB.1–CB.3, CB.5)', () => {
		let tmpDir: string;

		beforeEach(() => {
			// Reset the module-level catalog schema cache — CA catalog.test.ts
			// may leave it in either a populated or errored state depending on
			// its last assertion, which would make our loadServersCatalog return
			// empty entries on the first attempt here (the cache's schemaCacheError
			// short-circuits compilation).
			_resetSchemaCacheForTests();
			tmpDir = createTmpDir();
		});

		afterEach(() => {
			cleanupTmpDir(tmpDir);
		});

		it('posts an init message with catalog entries after panel creation (CB.1 a)', async () => {
			stageCatalogFixture(tmpDir, [
				{
					name: 'context7',
					display_name: 'Context7 Documentation',
					transport: 'http',
					description: 'Docs lookup.',
					url: 'https://mcp.context7.com/mcp',
					header_keys: ['Authorization'],
				},
				{
					name: 'orchestrator',
					display_name: 'Claude Orchestrator',
					transport: 'stdio',
					description: 'Pipeline routing.',
					command: 'uv',
					args: ['run', '--', 'orchestrator-mcp'],
				},
			]);
			const client = createTrackingClient();
			const ctx = createMockContext(tmpDir);
			await AddServerPanel.createOrShow(ctx.extensionUri as any, client, credStore, () => {});
			const panel = latestPanel();
			await waitForPostedMessage(panel, 'init');
			const inits = collectPosted<{ entries: unknown[]; warnings: string[] }>(panel, 'init');
			assert.equal(inits.length, 1, 'exactly one init message must be posted');
			assert.equal(
				inits[0].warnings.length, 0,
				`unexpected warnings: ${inits[0].warnings.join(' | ')}`);
			assert.equal(inits[0].entries.length, 2);
			const names = (inits[0].entries as Array<{ name: string }>).map((e) => e.name);
			assert.deepEqual(names.sort(), ['context7', 'orchestrator']);
		});

		it('catalog selection pre-fill: submit with valid catalogId routes through addServer (CB.1/CB.3)', async () => {
			stageCatalogFixture(tmpDir, [
				{
					name: 'context7',
					display_name: 'Context7 Documentation',
					transport: 'http',
					description: 'Docs lookup.',
					url: 'https://mcp.context7.com/mcp',
					header_keys: ['Authorization'],
				},
			]);
			const client = createTrackingClient();
			const ctx = createMockContext(tmpDir);
			await AddServerPanel.createOrShow(ctx.extensionUri as any, client, credStore, () => {});
			const panel = latestPanel();
			await waitForPostedMessage(panel, 'init');
			// Simulate the webview pre-filling, then submitting — operator kept
			// the pre-filled url and added a secret value for the Authorization
			// header. catalogId flows through to host; host re-validates.
			simulateSubmit(panel, {
				name: 'context7',
				target: 'https://mcp.context7.com/mcp',
				env: [],
				headers: ['Authorization: Bearer secret'],
				catalogId: 'context7',
			});
			// handleSubmit re-loads the catalog (async fs I/O) before reaching
			// client.addServer, so two setImmediate flushes aren't enough on
			// Windows — poll the tracked calls list until the call lands.
			const deadline = Date.now() + 500;
			while (client.calls.length === 0 && Date.now() < deadline) {
				await new Promise((r) => setTimeout(r, 5));
			}
			assert.equal(client.calls.length, 1, 'valid catalogId must not block addServer');
			const [, config] = client.calls[0].args as [string, Record<string, unknown>];
			assert.equal(config.url, 'https://mcp.context7.com/mcp');
			assert.deepEqual(config.headers, { Authorization: 'Bearer secret' });
		});

		it('host re-validation rejects forged catalogId — never reaches client.addServer (CB.3)', async () => {
			stageCatalogFixture(tmpDir, [
				{
					name: 'real-entry',
					display_name: 'Real',
					transport: 'stdio',
					description: 'real',
					command: '/usr/bin/real',
					args: [],
				},
			]);
			const client = createTrackingClient();
			const ctx = createMockContext(tmpDir);
			await AddServerPanel.createOrShow(ctx.extensionUri as any, client, credStore, () => {});
			const panel = latestPanel();
			await waitForPostedMessage(panel, 'init');
			simulateSubmit(panel, {
				name: 'evil',
				target: '/usr/bin/evil',
				env: [],
				headers: [],
				catalogId: 'forged-ghost',
			});
			await waitForPostedMessage(panel, 'nack');
			assert.equal(client.calls.length, 0, 'forged catalogId must be rejected before addServer');
			const nacks = collectPosted<{ error?: string }>(panel, 'nack');
			assert.equal(nacks.length, 1);
			assert.ok(
				nacks[0].error && nacks[0].error.includes('Unknown catalog entry'),
				`expected forged-id rejection, got: ${nacks[0].error}`);
			assert.ok(!panel.disposed, 'panel stays open so operator can correct');
		});

		it('malformed catalog file: init warnings fire but panel stays functional (CB.5)', async () => {
			// Stage malformed JSON — loader returns warnings, never throws.
			const dir = path.join(tmpDir, 'docs', 'catalog');
			fs.mkdirSync(dir, { recursive: true });
			fs.writeFileSync(path.join(dir, 'servers.json'), '{ not valid json ', 'utf8');
			const client = createTrackingClient();
			const ctx = createMockContext(tmpDir);
			await AddServerPanel.createOrShow(ctx.extensionUri as any, client, credStore, () => {});
			const panel = latestPanel();
			await waitForPostedMessage(panel, 'init');
			const inits = collectPosted<{ entries: unknown[]; warnings: string[] }>(panel, 'init');
			assert.equal(inits.length, 1);
			assert.equal(inits[0].entries.length, 0);
			assert.ok(inits[0].warnings.length > 0, 'loader must surface warnings for malformed JSON');
			// Panel is still usable — a free-form submit still goes through.
			simulateSubmit(panel, {
				name: 'still-works',
				target: '/usr/bin/fallback',
				env: [],
				headers: [],
			});
			await flush();
			assert.equal(client.calls.length, 1, 'malformed catalog must not break the submit path');
		});

		// LOW-3 (Sonnet 4.6 review): assertions below verify the architectural
		// invariant (catalog strings never embed in HTML at build time) and the
		// static source text (textContent — not innerHTML — is what the script
		// uses). The runtime DOM defence itself cannot be unit-tested without a
		// real browser; these two checks are grep-level regression guards.
		it('XSS safety: <script>-laden display_name passes through init and HTML uses textContent (CB.5)', async () => {
			stageCatalogFixture(tmpDir, [
				{
					name: 'evil',
					display_name: '<script>alert(1)</script>',
					transport: 'stdio',
					description: '<img src=x onerror=alert(1)>',
					command: '/usr/bin/e',
					args: [],
				},
			]);
			const client = createTrackingClient();
			const ctx = createMockContext(tmpDir);
			await AddServerPanel.createOrShow(ctx.extensionUri as any, client, credStore, () => {});
			const panel = latestPanel();
			await waitForPostedMessage(panel, 'init');
			// The init message carries the raw entries — XSS defence is at render
			// time via textContent (verified statically below).
			const inits = collectPosted<{ entries: Array<{ display_name: string }> }>(panel, 'init');
			assert.equal(inits.length, 1);
			assert.equal(inits[0].entries[0].display_name, '<script>alert(1)</script>');
			// The HTML template must NEVER interpolate catalog strings directly,
			// and the dropdown script must use textContent (auto-escaped) rather
			// than innerHTML on catalog-derived values.
			assert.ok(
				panel.webview.html.includes('opt.textContent'),
				'dropdown build must use textContent, not innerHTML');
			assert.ok(
				!panel.webview.html.includes('<script>alert(1)</script>'),
				'malicious display_name must not reach the HTML template — entries arrive via init postMessage');
		});

		it('operator mcpGateway.catalogPath override wins when non-empty and directory exists (CB.4)', async () => {
			const operatorRoot = createTmpDir();
			try {
				stageCatalogFixture(operatorRoot, [
					{
						name: 'operator-only',
						display_name: 'Operator Only',
						transport: 'stdio',
						description: 'Only in the operator catalog.',
						command: '/opt/operator',
						args: [],
					},
				]);
				// Bundled path (extensionUri.fsPath + /docs/catalog) holds a different
				// entry — the operator override must win.
				stageCatalogFixture(tmpDir, [
					{
						name: 'bundled-only',
						display_name: 'Bundled Only',
						transport: 'stdio',
						description: 'Only in the bundled catalog.',
						command: '/opt/bundled',
						args: [],
					},
				]);
				mockConfigValues['mcpGateway.catalogPath'] = path.join(operatorRoot, 'docs', 'catalog');

				const client = createTrackingClient();
				const ctx = createMockContext(tmpDir);
				await AddServerPanel.createOrShow(ctx.extensionUri as any, client, credStore, () => {});
				const panel = latestPanel();
				await waitForPostedMessage(panel, 'init');
				const inits = collectPosted<{ entries: Array<{ name: string }> }>(panel, 'init');
				assert.equal(inits.length, 1);
				const names = inits[0].entries.map((e) => e.name);
				assert.deepEqual(names, ['operator-only'], 'operator override must win over bundled');
			} finally {
				cleanupTmpDir(operatorRoot);
			}
		});

		it('in-flight submit guard holds even when catalog is staged (11.C preserved under catalog flow)', async () => {
			stageCatalogFixture(tmpDir, [
				{
					name: 'slow-entry',
					display_name: 'Slow',
					transport: 'stdio',
					description: 's',
					command: '/usr/bin/slow',
					args: [],
				},
			]);
			let resolve!: () => void;
			const gate = new Promise<void>((r) => { resolve = r; });
			const calls: Array<{ method: string; args: unknown[] }> = [];
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
			} as IGatewayClient & { calls: Array<{ method: string; args: unknown[] }> };

			const ctx = createMockContext(tmpDir);
			await AddServerPanel.createOrShow(ctx.extensionUri as any, slowClient, credStore, () => {});
			const panel = latestPanel();
			await waitForPostedMessage(panel, 'init');
			const payload = {
				name: 'slow-entry', target: '/usr/bin/slow',
				env: [], headers: [], catalogId: 'slow-entry',
			};
			simulateSubmit(panel, payload);
			// Wait until the first submit has actually reached client.addServer
			// (which requires re-loading the catalog first — async). Only then
			// is dispatching the second submit a meaningful "in-flight" test.
			while (slowClient.calls.length === 0) {
				await new Promise((r) => setTimeout(r, 5));
			}
			simulateSubmit(panel, payload);
			// Give the second submit a full event-loop chance to reach handleSubmit.
			await new Promise((r) => setTimeout(r, 20));
			assert.equal(slowClient.calls.length, 1, 'second submit must be dropped across catalog switch too');
			resolve();
			await flush();
			assert.equal(slowClient.calls.length, 1, 'still one after gate resolves');
		});
	});

	// CB.4 — manifest registration. Asserts package.json contributes the
	// mcpGateway.catalogPath setting with the exact contract promised by the
	// plan (string, default "", machine scope). This check lives in CB (not CA)
	// because it tests CB-introduced state — see CV-gate D-1.
	describe('manifest registration (CB.4)', () => {
		it('package.json contributes mcpGateway.catalogPath with machine scope', () => {
			const manifestPath = path.join(__dirname, '..', '..', '..', 'package.json');
			const manifest = JSON.parse(fs.readFileSync(manifestPath, 'utf8')) as {
				contributes?: { configuration?: { properties?: Record<string, unknown> } };
			};
			const props = manifest.contributes?.configuration?.properties;
			assert.ok(props, 'package.json must declare contributes.configuration.properties');
			const entry = props['mcpGateway.catalogPath'] as Record<string, unknown> | undefined;
			assert.ok(entry, 'mcpGateway.catalogPath must be registered');
			assert.equal(entry.type, 'string');
			assert.equal(entry.default, '');
			assert.equal(entry.scope, 'machine');
			assert.ok(
				typeof entry.description === 'string' && entry.description.length > 0,
				'catalogPath must carry a description for the Settings UI');
		});
	});
});
