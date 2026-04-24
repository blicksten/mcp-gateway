import '../mock-vscode';
import { strict as assert } from 'node:assert';
import { describe, it, beforeEach, afterEach } from 'mocha';
import {
	resetMockState,
	mockWebviewPanels,
	mockCalls,
	type MockWebviewPanel,
} from '../mock-vscode';
import { AddSapPanel } from '../../webview/add-sap-panel';
import { ServerDataCache } from '../../server-data-cache';
import type { IGatewayClient } from '../../extension';
import type { ServerView } from '../../types';

interface TrackedCall { method: string; args: unknown[] }

function createTrackingClient(opts: { fail?: boolean; failMessage?: string; initialServers?: ServerView[] } = {}) {
	const calls: TrackedCall[] = [];
	const servers = opts.initialServers ?? [];
	const client = {
		calls,
		listServers: async () => servers,
		getHealth: async () => ({ status: 'ok', servers: 0, running: 0 }),
		shutdown: async () => ({ status: 'shutting_down' }),
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
	cache: ServerDataCache,
	onCreated: () => void = () => {},
): Promise<MockWebviewPanel> {
	const ctx = createMockContext();
	await AddSapPanel.createOrShow(ctx.extensionUri as any, client, cache, onCreated);
	return latestPanel();
}

async function freshCache(servers: ServerView[] = []): Promise<ServerDataCache> {
	const c = new ServerDataCache({
		listServers: async () => servers,
		getHealth: async () => ({}),
		shutdown: async () => ({ status: 'shutting_down' }),
		getServer: async () => ({}),
		addServer: async () => ({}),
		removeServer: async () => ({}),
		patchServer: async () => ({}),
		restartServer: async () => ({}),
		resetCircuit: async () => ({}),
		callTool: async () => ({ content: null }),
		listTools: async () => [],
	} as any);
	await c.refresh();
	return c;
}

const ABS_VSP_CMD = process.platform === 'win32' ? 'C:\\bin\\sap-vsp' : '/usr/bin/sap-vsp';
const ABS_GUI_CMD = process.platform === 'win32' ? 'C:\\bin\\sap-gui' : '/usr/bin/sap-gui';

/**
 * Simulate a webview submit. By default injects absolute paths for
 * vspCommand/guiCommand based on the components that are enabled — tests
 * that don't care about command-validation (the bulk of the suite) get
 * a working default. Tests that exercise command validation pass
 * explicit `vspCommand`/`guiCommand` in the payload to override.
 */
function simulateSubmit(panel: MockWebviewPanel, payload: Record<string, unknown>): void {
	const comps = (payload.components as { vsp?: boolean; gui?: boolean } | undefined) ?? { vsp: true, gui: true };
	const withDefaults: Record<string, unknown> = {
		vspCommand: comps.vsp !== false ? ABS_VSP_CMD : null,
		guiCommand: comps.gui !== false ? ABS_GUI_CMD : null,
		...payload,
	};
	panel.webview._simulateMessage({ type: 'submit', payload: withDefaults });
}

async function flush(): Promise<void> {
	await new Promise((r) => setImmediate(r));
	await new Promise((r) => setImmediate(r));
}

describe('AddSapPanel', () => {
	beforeEach(() => {
		resetMockState();
		AddSapPanel._reset();
	});

	afterEach(() => {
		AddSapPanel._reset();
	});

	describe('panel lifecycle', () => {
		it('creates a webview panel with CSP and nonce', async () => {
			const client = createTrackingClient();
			const cache = await freshCache();
			const panel = await openPanel(client, cache);
			assert.strictEqual(panel.viewType, 'mcpAddSapSystem');
			assert.strictEqual(panel.title, 'Add SAP System');
			assert.ok(panel.webview.html.includes('<title>Add SAP System</title>'));
			assert.ok(panel.webview.html.includes("default-src 'none'"));
			assert.ok(panel.webview.html.includes("form-action 'none'"));
			assert.ok(!panel.webview.html.includes("'unsafe-inline'"));
			cache.dispose();
		});

		it('singleton — second createOrShow reveals existing', async () => {
			const client = createTrackingClient();
			const cache = await freshCache();
			await openPanel(client, cache);
			const before = mockWebviewPanels.length;
			await openPanel(client, cache);
			assert.strictEqual(mockWebviewPanels.length, before, 'no new panel');
			cache.dispose();
		});

		it('cancel disposes the panel', async () => {
			const client = createTrackingClient();
			const cache = await freshCache();
			const panel = await openPanel(client, cache);
			panel.webview._simulateMessage({ type: 'cancel' });
			await flush();
			assert.ok(panel.disposed);
			cache.dispose();
		});

		it('malformed messages are ignored', async () => {
			const client = createTrackingClient();
			const cache = await freshCache();
			const panel = await openPanel(client, cache);
			panel.webview._simulateMessage(null);
			panel.webview._simulateMessage({ type: 'unknown' });
			panel.webview._simulateMessage('string');
			await flush();
			assert.strictEqual(client.calls.length, 0);
			assert.ok(!panel.disposed);
			cache.dispose();
		});
	});

	describe('happy path submits', () => {
		it('creates vsp + gui servers for SID+client', async () => {
			const client = createTrackingClient();
			const cache = await freshCache();
			let createdCount = 0;
			const panel = await openPanel(client, cache, () => { createdCount++; });
			simulateSubmit(panel, {
				sid: 'DEV',
				client: '100',
				components: { vsp: true, gui: true },
			});
			await flush();
			assert.strictEqual(client.calls.length, 2);
			const names = client.calls.map((c) => (c.args as [string, unknown])[0]);
			assert.deepStrictEqual(names, ['vsp-DEV-100', 'sap-gui-DEV-100']);
			// Verify the commands go through from the form to the addServer call.
			assert.deepStrictEqual(client.calls[0].args[1], { command: ABS_VSP_CMD });
			assert.deepStrictEqual(client.calls[1].args[1], { command: ABS_GUI_CMD });
			assert.strictEqual(createdCount, 1);
			assert.ok(panel.disposed);
			cache.dispose();
		});

		it('creates VSP only when GUI checkbox is off', async () => {
			const client = createTrackingClient();
			const cache = await freshCache();
			const panel = await openPanel(client, cache);
			simulateSubmit(panel, {
				sid: 'QAS',
				client: null,
				components: { vsp: true, gui: false },
			});
			await flush();
			assert.strictEqual(client.calls.length, 1);
			assert.strictEqual((client.calls[0].args as [string, unknown])[0], 'vsp-QAS');
			cache.dispose();
		});

		it('creates GUI only when VSP checkbox is off', async () => {
			const client = createTrackingClient();
			const cache = await freshCache();
			const panel = await openPanel(client, cache);
			simulateSubmit(panel, {
				sid: 'PRD',
				client: '800',
				components: { vsp: false, gui: true },
			});
			await flush();
			assert.strictEqual(client.calls.length, 1);
			assert.strictEqual((client.calls[0].args as [string, unknown])[0], 'sap-gui-PRD-800');
			cache.dispose();
		});

		it('uppercases lowercase SID server-side', async () => {
			const client = createTrackingClient();
			const cache = await freshCache();
			const panel = await openPanel(client, cache);
			simulateSubmit(panel, {
				sid: 'dev',
				client: '100',
				components: { vsp: true, gui: false },
			});
			await flush();
			assert.strictEqual(client.calls.length, 1);
			assert.strictEqual((client.calls[0].args as [string, unknown])[0], 'vsp-DEV-100');
			cache.dispose();
		});
	});

	describe('server-side validation', () => {
		it('rejects invalid SID (too short)', async () => {
			const client = createTrackingClient();
			const cache = await freshCache();
			const panel = await openPanel(client, cache);
			simulateSubmit(panel, { sid: 'AB', client: '100', components: { vsp: true, gui: true } });
			await flush();
			assert.strictEqual(client.calls.length, 0);
			const nacks = panel._postedMessages.filter((m) => (m as { type?: string }).type === 'nack');
			assert.strictEqual(nacks.length, 1);
			cache.dispose();
		});

		it('rejects invalid SID (lowercase-only accepted, punctuation rejected)', async () => {
			const client = createTrackingClient();
			const cache = await freshCache();
			const panel = await openPanel(client, cache);
			simulateSubmit(panel, { sid: 'A-H', client: '100', components: { vsp: true, gui: true } });
			await flush();
			assert.strictEqual(client.calls.length, 0);
			cache.dispose();
		});

		it('rejects invalid client (not 3 digits)', async () => {
			const client = createTrackingClient();
			const cache = await freshCache();
			const panel = await openPanel(client, cache);
			simulateSubmit(panel, { sid: 'DEV', client: '1000', components: { vsp: true, gui: true } });
			await flush();
			assert.strictEqual(client.calls.length, 0);
			cache.dispose();
		});

		it('accepts null/empty client (clientless SID)', async () => {
			const client = createTrackingClient();
			const cache = await freshCache();
			const panel = await openPanel(client, cache);
			simulateSubmit(panel, { sid: 'S42', client: null, components: { vsp: true, gui: true } });
			await flush();
			assert.strictEqual(client.calls.length, 2);
			assert.strictEqual((client.calls[0].args as [string, unknown])[0], 'vsp-S42');
			assert.strictEqual((client.calls[1].args as [string, unknown])[0], 'sap-gui-S42');
			cache.dispose();
		});

		it('rejects when no component is selected', async () => {
			const client = createTrackingClient();
			const cache = await freshCache();
			const panel = await openPanel(client, cache);
			simulateSubmit(panel, { sid: 'DEV', client: '100', components: { vsp: false, gui: false } });
			await flush();
			assert.strictEqual(client.calls.length, 0);
			const nacks = panel._postedMessages.filter((m) => (m as { type?: string }).type === 'nack') as Array<{ error?: string }>;
			assert.strictEqual(nacks.length, 1);
			assert.ok(nacks[0].error && nacks[0].error.includes('at least one component'));
			cache.dispose();
		});

		it('rejects malformed payload shape', async () => {
			const client = createTrackingClient();
			const cache = await freshCache();
			const panel = await openPanel(client, cache);
			panel.webview._simulateMessage({ type: 'submit', payload: 'not-an-object' });
			await flush();
			assert.strictEqual(client.calls.length, 0);
			cache.dispose();
		});
	});

	describe('duplicate detection', () => {
		it('warns on first submit when one server already exists, proceeds on second', async () => {
			const existing: ServerView[] = [
				{ name: 'vsp-DEV-100', status: 'running', transport: 'stdio', restart_count: 0 },
			];
			const client = createTrackingClient();
			const cache = await freshCache(existing);
			const panel = await openPanel(client, cache);

			// First submit — should warn, not call addServer.
			simulateSubmit(panel, { sid: 'DEV', client: '100', components: { vsp: true, gui: true } });
			await flush();
			assert.strictEqual(client.calls.length, 0);
			const warns = panel._postedMessages.filter((m) => (m as { type?: string }).type === 'warn') as Array<{ error?: string }>;
			assert.strictEqual(warns.length, 1);
			assert.ok(warns[0].error && warns[0].error.includes('vsp-DEV-100'));

			// Second submit with same key — should skip existing and create GUI only.
			simulateSubmit(panel, { sid: 'DEV', client: '100', components: { vsp: true, gui: true } });
			await flush();
			assert.strictEqual(client.calls.length, 1);
			assert.strictEqual((client.calls[0].args as [string, unknown])[0], 'sap-gui-DEV-100');
			assert.ok(panel.disposed);
			cache.dispose();
		});

		it('resets duplicate-key guard when submit targets a different key', async () => {
			const existing: ServerView[] = [
				{ name: 'vsp-DEV-100', status: 'running', transport: 'stdio', restart_count: 0 },
			];
			const client = createTrackingClient();
			const cache = await freshCache(existing);
			const panel = await openPanel(client, cache);

			// First submit on DEV-100 — warn.
			simulateSubmit(panel, { sid: 'DEV', client: '100', components: { vsp: true, gui: true } });
			await flush();
			assert.strictEqual(client.calls.length, 0);

			// Submit on different SID — must warn separately, not be auto-confirmed.
			simulateSubmit(panel, { sid: 'QAS', client: '100', components: { vsp: true, gui: true } });
			await flush();
			// QAS-100 has no conflicts in the cache, so this creates 2 servers immediately.
			assert.strictEqual(client.calls.length, 2);
			assert.ok(panel.disposed);
			cache.dispose();
		});

		it('remembers every warned key across SID switches (Set-based state machine)', async () => {
			// Populate cache with conflicts for BOTH DEV-100 and DEV-200.
			const existing: ServerView[] = [
				{ name: 'vsp-DEV-100', status: 'running', transport: 'stdio', restart_count: 0 },
				{ name: 'vsp-DEV-200', status: 'running', transport: 'stdio', restart_count: 0 },
			];
			const client = createTrackingClient();
			const cache = await freshCache(existing);
			const panel = await openPanel(client, cache);

			// Submit DEV-100 — warn.
			simulateSubmit(panel, { sid: 'DEV', client: '100', components: { vsp: true, gui: true } });
			await flush();
			assert.strictEqual(client.calls.length, 0);
			// Submit DEV-200 — warn (different key).
			simulateSubmit(panel, { sid: 'DEV', client: '200', components: { vsp: true, gui: true } });
			await flush();
			assert.strictEqual(client.calls.length, 0);
			// Return to DEV-100 — must be remembered as confirmed (fallback fixed M-1).
			simulateSubmit(panel, { sid: 'DEV', client: '100', components: { vsp: true, gui: true } });
			await flush();
			// DEV-100 proceed: skip vsp-DEV-100 (dup), create sap-gui-DEV-100.
			assert.strictEqual(client.calls.length, 1);
			assert.strictEqual((client.calls[0].args as [string, unknown])[0], 'sap-gui-DEV-100');
			cache.dispose();
		});
	});

	describe('command validation (F2 fix)', () => {
		it('rejects missing VSP command when VSP is selected', async () => {
			const client = createTrackingClient();
			const cache = await freshCache();
			const panel = await openPanel(client, cache);
			simulateSubmit(panel, {
				sid: 'DEV',
				client: '100',
				components: { vsp: true, gui: false },
				vspCommand: null,
			});
			await flush();
			assert.strictEqual(client.calls.length, 0);
			const nacks = panel._postedMessages.filter((m) => (m as { type?: string }).type === 'nack') as Array<{ error?: string }>;
			assert.strictEqual(nacks.length, 1);
			assert.ok(nacks[0].error && nacks[0].error.includes('VSP executable is required'));
			cache.dispose();
		});

		it('rejects missing GUI command when GUI is selected', async () => {
			const client = createTrackingClient();
			const cache = await freshCache();
			const panel = await openPanel(client, cache);
			simulateSubmit(panel, {
				sid: 'DEV',
				client: '100',
				components: { vsp: false, gui: true },
				guiCommand: null,
			});
			await flush();
			assert.strictEqual(client.calls.length, 0);
			const nacks = panel._postedMessages.filter((m) => (m as { type?: string }).type === 'nack') as Array<{ error?: string }>;
			assert.ok(nacks[0].error && nacks[0].error.includes('GUI executable is required'));
			cache.dispose();
		});

		it('rejects relative VSP command', async () => {
			const client = createTrackingClient();
			const cache = await freshCache();
			const panel = await openPanel(client, cache);
			simulateSubmit(panel, {
				sid: 'DEV',
				client: '100',
				components: { vsp: true, gui: false },
				vspCommand: 'sap-vsp',
			});
			await flush();
			assert.strictEqual(client.calls.length, 0);
			const nacks = panel._postedMessages.filter((m) => (m as { type?: string }).type === 'nack') as Array<{ error?: string }>;
			assert.ok(nacks[0].error && nacks[0].error.includes('VSP command'));
			cache.dispose();
		});

		it('uses the user-provided absolute commands in the addServer config', async () => {
			const client = createTrackingClient();
			const cache = await freshCache();
			const panel = await openPanel(client, cache);
			const vsp = process.platform === 'win32' ? 'C:\\custom\\vsp.exe' : '/opt/custom/vsp';
			const gui = process.platform === 'win32' ? 'C:\\custom\\gui.exe' : '/opt/custom/gui';
			simulateSubmit(panel, {
				sid: 'DEV',
				client: '100',
				components: { vsp: true, gui: true },
				vspCommand: vsp,
				guiCommand: gui,
			});
			await flush();
			assert.strictEqual(client.calls.length, 2);
			assert.deepStrictEqual(client.calls[0].args[1], { command: vsp });
			assert.deepStrictEqual(client.calls[1].args[1], { command: gui });
			cache.dispose();
		});

		it('ignores the command for an unchecked component', async () => {
			const client = createTrackingClient();
			const cache = await freshCache();
			const panel = await openPanel(client, cache);
			// Only VSP is checked — GUI command is irrelevant and should not be validated.
			simulateSubmit(panel, {
				sid: 'DEV',
				client: '100',
				components: { vsp: true, gui: false },
				vspCommand: ABS_VSP_CMD,
				guiCommand: 'relative/gui', // invalid, but GUI is off — must not trigger validation
			});
			await flush();
			assert.strictEqual(client.calls.length, 1);
			assert.strictEqual((client.calls[0].args as [string, unknown])[0], 'vsp-DEV-100');
			cache.dispose();
		});
	});

	describe('concurrency and error handling', () => {
		it('drops a second submit while the first is in flight', async () => {
			let resolve!: () => void;
			const gate = new Promise<void>((r) => { resolve = r; });
			const calls: TrackedCall[] = [];
			const slowClient = {
				calls,
				listServers: async () => [],
				getHealth: async () => ({}),
				shutdown: async () => ({ status: 'shutting_down' }),
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
			const cache = await freshCache();
			const panel = await openPanel(slowClient, cache);
			const payload = { sid: 'DEV', client: '100', components: { vsp: true, gui: false } };
			simulateSubmit(panel, payload);
			simulateSubmit(panel, payload);
			await new Promise((r) => setImmediate(r));
			assert.strictEqual(slowClient.calls.length, 1, 'second submit must be dropped');
			resolve();
			await flush();
			assert.strictEqual(slowClient.calls.length, 1);
			cache.dispose();
		});

		it('nacks when all addServer calls throw', async () => {
			const client = createTrackingClient({ fail: true, failMessage: 'connection refused' });
			const cache = await freshCache();
			const panel = await openPanel(client, cache);
			simulateSubmit(panel, { sid: 'DEV', client: '100', components: { vsp: true, gui: false } });
			await flush();
			assert.strictEqual(client.calls.length, 1);
			const nacks = panel._postedMessages.filter((m) => (m as { type?: string }).type === 'nack') as Array<{ error?: string }>;
			assert.strictEqual(nacks.length, 1);
			assert.ok(nacks[0].error && nacks[0].error.includes('connection refused'));
			assert.ok(!panel.disposed, 'panel stays open on total failure');
			cache.dispose();
		});

		it('partial failure: shows warning, still disposes panel', async () => {
			const calls: TrackedCall[] = [];
			let callCount = 0;
			const partialClient = {
				calls,
				listServers: async () => [],
				getHealth: async () => ({}),
				shutdown: async () => ({ status: 'shutting_down' }),
				getServer: async () => ({}),
				addServer: async (name: string, config: unknown) => {
					calls.push({ method: 'addServer', args: [name, config] });
					callCount++;
					// First call (vsp-*) succeeds, second (sap-gui-*) fails.
					if (callCount === 2) { throw new Error('gui registration failed'); }
					return { status: 'ok' };
				},
				removeServer: async () => ({}),
				patchServer: async () => ({}),
				restartServer: async () => ({}),
				resetCircuit: async () => ({}),
				callTool: async () => ({ content: null }),
				listTools: async () => [],
			} as IGatewayClient & { calls: TrackedCall[] };
			const cache = await freshCache();
			const panel = await openPanel(partialClient, cache);
			simulateSubmit(panel, { sid: 'DEV', client: '100', components: { vsp: true, gui: true } });
			await flush();
			assert.strictEqual(partialClient.calls.length, 2);
			assert.ok(mockCalls.warningMessages.some((m) => m.includes('partially added')));
			assert.ok(panel.disposed, 'panel disposes on partial success');
			cache.dispose();
		});
	});
});
