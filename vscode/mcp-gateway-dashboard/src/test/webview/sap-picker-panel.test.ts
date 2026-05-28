import '../mock-vscode';
import { strict as assert } from 'node:assert';
import { describe, it, beforeEach, afterEach } from 'mocha';
import {
	resetMockState,
	mockWebviewPanels,
	dialogResponses,
	MockSecretStorage,
	type MockWebviewPanel,
} from '../mock-vscode';
import { SapPickerPanel } from '../../webview/sap-picker-panel';
import { ServerDataCache } from '../../server-data-cache';
import type { IGatewayClient } from '../../extension';
import type { ServerView } from '../../types';
import type { PickerSnapshot } from '../../sap-picker-state';

interface RecordedCall { method: string; args: unknown[] }

interface FakeClient extends IGatewayClient {
	calls: RecordedCall[];
	snapshotResponses: PickerSnapshot[]; // queue
	addServerError?: Error;
	removeServerError?: Error;
	beginBatchError?: Error;
}

function makeClient(initial: PickerSnapshot, opts: { servers?: ServerView[] } = {}): FakeClient {
	const calls: RecordedCall[] = [];
	const servers = opts.servers ?? [];
	let queue: PickerSnapshot[] = [initial];
	const c: FakeClient = {
		calls,
		snapshotResponses: queue,
		listServers: async () => { calls.push({ method: 'listServers', args: [] }); return servers; },
		getHealth: async () => ({ status: 'ok', servers: 0, running: 0 }),
		shutdown: async () => ({ status: 'shutting_down' }),
		getServer: async () => ({}),
		addServer: async (name: string, config: Record<string, unknown>) => {
			calls.push({ method: 'addServer', args: [name, config] });
			if (c.addServerError) { throw c.addServerError; }
			return { status: 'ok' };
		},
		removeServer: async (name: string) => {
			calls.push({ method: 'removeServer', args: [name] });
			if (c.removeServerError) { throw c.removeServerError; }
			return { status: 'ok' };
		},
		patchServer: async () => ({}),
		restartServer: async () => ({}),
		resetCircuit: async () => ({}),
		callTool: async () => ({ content: null }),
		listTools: async () => [],
		getSapPickerSnapshot: async () => {
			calls.push({ method: 'getSapPickerSnapshot', args: [] });
			const snap = c.snapshotResponses.shift() ?? initial;
			c.snapshotResponses = c.snapshotResponses; // keep type
			queue = c.snapshotResponses;
			return snap;
		},
		beginSapBatch: async () => {
			calls.push({ method: 'beginSapBatch', args: [] });
			if (c.beginBatchError) { throw c.beginBatchError; }
			return { batch_id: 'b-test-1' };
		},
		endSapBatch: async (batchId: string) => {
			calls.push({ method: 'endSapBatch', args: [batchId] });
			return { ok: true };
		},
	};
	return c;
}

async function freshCache(servers: ServerView[]): Promise<ServerDataCache> {
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

async function flush(times = 4): Promise<void> {
	for (let i = 0; i < times; i++) {
		await new Promise((r) => setImmediate(r));
	}
}

function latestPanel(): MockWebviewPanel {
	assert.ok(mockWebviewPanels.length > 0, 'expected a panel');
	return mockWebviewPanels[mockWebviewPanels.length - 1];
}

const FAKE_URI = { scheme: 'file', path: '/test', with: () => ({}), toString: () => 'file:///test' } as any;

describe('SapPickerPanel', () => {
	beforeEach(() => {
		resetMockState();
		SapPickerPanel._reset();
	});
	afterEach(() => {
		SapPickerPanel._reset();
	});

	it('opens a webview, fetches snapshot, posts init with rows + warnings', async () => {
		const snap: PickerSnapshot = {
			rows: [
				{ sid: 'DEV', client: '100', user: '', kpMissing: false, registered: { vsp: false, gui: false }, status: { vsp: '', gui: '' } },
			],
			warnings: ['picker data sources not yet wired'],
		};
		const client = makeClient(snap);
		const cache = await freshCache([]);
		await SapPickerPanel.createOrShow(FAKE_URI, client, cache, new MockSecretStorage() as any);
		await flush();
		const panel = latestPanel();
		assert.ok(panel.webview.html.includes('SAP Picker'), 'expected SAP Picker title in HTML');
		assert.ok(panel.webview.html.includes("Content-Security-Policy"), 'expected CSP meta');
		const init = panel._postedMessages.find((m: any) => m && m.type === 'init') as any;
		assert.ok(init, 'expected init postMessage');
		assert.strictEqual(init.rows.length, 1);
		assert.strictEqual(init.rows[0].sid, 'DEV');
		assert.deepStrictEqual(init.warnings, ['picker data sources not yet wired']);
	});

	it('apply flow: beginBatch â†’ addServer â†’ endBatch (in order)', async () => {
		const snap: PickerSnapshot = {
			rows: [{ sid: 'DEV', client: '100', user: '', kpMissing: false, registered: { vsp: false, gui: false }, status: { vsp: '', gui: '' } }],
			warnings: [],
		};
		const client = makeClient(snap);
		const cache = await freshCache([{ name: 'vsp-DEV-100', status: 'running', transport: 'stdio', restart_count: 0 } as ServerView]);
		await SapPickerPanel.createOrShow(FAKE_URI, client, cache, new MockSecretStorage() as any);
		await flush();
		const panel = latestPanel();
		// User toggles VSP on with command override
		panel.webview._simulateMessage({
			type: 'apply',
			diffs: [{ rowKey: 'DEV-100', desired: { vsp: true, gui: false }, override: { vspCommand: '/opt/vsp' } }],
		});
		await flush(20);
		const methodOrder = client.calls.map((c) => c.method);
		const begin = methodOrder.indexOf('beginSapBatch');
		const add = methodOrder.indexOf('addServer');
		const end = methodOrder.indexOf('endSapBatch');
		assert.ok(begin >= 0, 'beginSapBatch was called');
		assert.ok(add > begin, 'addServer called after beginSapBatch');
		assert.ok(end > add, 'endSapBatch called after addServer');
		// Verify addServer args mapping: serverName + command
		const addCall = client.calls.find((c) => c.method === 'addServer')!;
		assert.deepStrictEqual(addCall.args, ['vsp-DEV-100', { command: '/opt/vsp' }]);
	});

	it('R-30: kpMissing rows are stripped from ops even if webview sends desired=true (DOM tamper guard)', async () => {
		const snap: PickerSnapshot = {
			rows: [
				{ sid: 'NOC', client: '100', user: '', kpMissing: true, registered: { vsp: false, gui: false }, status: { vsp: '', gui: '' } },
			],
			warnings: [],
		};
		const client = makeClient(snap);
		const cache = await freshCache([]);
		await SapPickerPanel.createOrShow(FAKE_URI, client, cache, new MockSecretStorage() as any);
		await flush();
		const panel = latestPanel();
		panel.webview._simulateMessage({
			type: 'apply',
			// Tampered DOM: user-supplied desired.vsp=true with a command override
			diffs: [{ rowKey: 'NOC-100', desired: { vsp: true, gui: true }, override: { vspCommand: '/opt/x', guiCommand: '/opt/y' } }],
		});
		await flush(8);
		// addServer must NOT be called for kpMissing rows
		assert.strictEqual(client.calls.filter((c) => c.method === 'addServer').length, 0);
		// applied summary indicates "no changes"
		const applied = panel._postedMessages.find((m: any) => m && m.type === 'applied') as any;
		assert.ok(applied, 'expected applied postMessage');
		assert.strictEqual(applied.failed, 0);
	});

	it('endSapBatch is called even when addServer throws', async () => {
		const snap: PickerSnapshot = {
			rows: [{ sid: 'DEV', client: '100', user: '', kpMissing: false, registered: { vsp: false, gui: false }, status: { vsp: '', gui: '' } }],
			warnings: [],
		};
		const client = makeClient(snap);
		client.addServerError = new Error('config conflict');
		const cache = await freshCache([]);
		await SapPickerPanel.createOrShow(FAKE_URI, client, cache, new MockSecretStorage() as any);
		await flush();
		const panel = latestPanel();
		panel.webview._simulateMessage({
			type: 'apply',
			diffs: [{ rowKey: 'DEV-100', desired: { vsp: true, gui: false }, override: { vspCommand: '/opt/vsp' } }],
		});
		await flush(20);
		assert.ok(client.calls.some((c) => c.method === 'endSapBatch'),
			'endSapBatch must run in finally even when add fails');
	});

	it('R-28 orphan: removeServer error containing "orphan" surfaces removed_with_orphan status', async () => {
		const snap: PickerSnapshot = {
			rows: [{ sid: 'DEV', client: '100', user: '', kpMissing: false, registered: { vsp: true, gui: false }, status: { vsp: 'running', gui: '' } }],
			warnings: [],
		};
		const client = makeClient(snap);
		client.removeServerError = new Error('orphan: stop failed at PID 1234');
		const cache = await freshCache([]);
		await SapPickerPanel.createOrShow(FAKE_URI, client, cache, new MockSecretStorage() as any);
		await flush();
		const panel = latestPanel();
		panel.webview._simulateMessage({
			type: 'apply',
			diffs: [{ rowKey: 'DEV-100', desired: { vsp: false, gui: false }, override: {} }],
		});
		await flush(20);
		// Look for a 'rows' postMessage with the orphan status
		const rowsMsgs = panel._postedMessages.filter((m: any) => m && m.type === 'rows') as any[];
		const lastRows = rowsMsgs[rowsMsgs.length - 1];
		assert.ok(lastRows, 'expected rows postMessage');
		const dev = lastRows.rows.find((r: any) => r.key === 'DEV-100');
		assert.strictEqual(dev.vspStatus, 'removed_with_orphan');
		assert.match(dev.vspError, /orphan/);
	});

	it('forceKill cancel branch (showWarningMessage returns undefined) does not call removeServer', async () => {
		const snap: PickerSnapshot = {
			rows: [{ sid: 'DEV', client: '100', user: '', kpMissing: false, registered: { vsp: true, gui: false }, status: { vsp: 'running', gui: '' } }],
			warnings: [],
		};
		const client = makeClient(snap);
		const cache = await freshCache([]);
		// dialogResponses.showWarningMessage defaults to undefined â†’ user
		// dismissed dialog â†’ panel must NOT proceed to removeServer.
		await SapPickerPanel.createOrShow(FAKE_URI, client, cache, new MockSecretStorage() as any);
		await flush();
		const panel = latestPanel();
		const removeCallsBefore = client.calls.filter((c) => c.method === 'removeServer').length;
		panel.webview._simulateMessage({ type: 'forceKill', rowKey: 'DEV-100', component: 'vsp' });
		await flush(8);
		const removeCallsAfter = client.calls.filter((c) => c.method === 'removeServer').length;
		assert.strictEqual(removeCallsAfter, removeCallsBefore, 'cancel branch must not call removeServer');
	});

	it('forceKill confirm branch (user clicks "Force kill") calls removeServer and surfaces removed status', async () => {
		const snap: PickerSnapshot = {
			rows: [{ sid: 'DEV', client: '100', user: '', kpMissing: false, registered: { vsp: true, gui: false }, status: { vsp: 'running', gui: '' } }],
			warnings: [],
		};
		const client = makeClient(snap);
		const cache = await freshCache([]);
		// Simulate the operator clicking 'Force kill' in the warning dialog.
		dialogResponses.showWarningMessage = 'Force kill';
		await SapPickerPanel.createOrShow(FAKE_URI, client, cache, new MockSecretStorage() as any);
		await flush();
		const panel = latestPanel();
		panel.webview._simulateMessage({ type: 'forceKill', rowKey: 'DEV-100', component: 'vsp' });
		await flush(8);
		const removeCalls = client.calls.filter((c) => c.method === 'removeServer');
		assert.strictEqual(removeCalls.length, 1, 'expected exactly one removeServer call');
		assert.deepStrictEqual(removeCalls[0].args, ['vsp-DEV-100']);
		// rows postMessage should reflect the new 'removed' status for the VSP row.
		const rowsMsgs = panel._postedMessages.filter((m: any) => m && m.type === 'rows') as any[];
		const lastRows = rowsMsgs[rowsMsgs.length - 1];
		const dev = lastRows.rows.find((r: any) => r.key === 'DEV-100');
		assert.strictEqual(dev.vspStatus, 'removed');
	});

	it('forceKill confirm branch surfaces postError when removeServer throws', async () => {
		const snap: PickerSnapshot = {
			rows: [{ sid: 'DEV', client: '100', user: '', kpMissing: false, registered: { vsp: true, gui: false }, status: { vsp: 'running', gui: '' } }],
			warnings: [],
		};
		const client = makeClient(snap);
		client.removeServerError = new Error('still alive');
		const cache = await freshCache([]);
		dialogResponses.showWarningMessage = 'Force kill';
		await SapPickerPanel.createOrShow(FAKE_URI, client, cache, new MockSecretStorage() as any);
		await flush();
		const panel = latestPanel();
		panel.webview._simulateMessage({ type: 'forceKill', rowKey: 'DEV-100', component: 'vsp' });
		await flush(8);
		const errors = panel._postedMessages.filter((m: any) => m && m.type === 'error') as any[];
		assert.ok(errors.length > 0, 'expected error postMessage');
		assert.match(errors[errors.length - 1].message, /Force-kill failed/);
	});

	it('refresh re-fetches the snapshot and replays init', async () => {
		const snap1: PickerSnapshot = {
			rows: [{ sid: 'DEV', client: '100', user: '', kpMissing: false, registered: { vsp: false, gui: false }, status: { vsp: '', gui: '' } }],
			warnings: [],
		};
		const snap2: PickerSnapshot = {
			rows: [
				{ sid: 'DEV', client: '100', user: '', kpMissing: false, registered: { vsp: false, gui: false }, status: { vsp: '', gui: '' } },
				{ sid: 'QAS', client: '200', user: 'B', kpMissing: false, registered: { vsp: false, gui: false }, status: { vsp: '', gui: '' } },
			],
			warnings: [],
		};
		const client = makeClient(snap1);
		client.snapshotResponses.push(snap2);
		const cache = await freshCache([]);
		await SapPickerPanel.createOrShow(FAKE_URI, client, cache, new MockSecretStorage() as any);
		await flush();
		const panel = latestPanel();
		panel.webview._simulateMessage({ type: 'refresh' });
		await flush(8);
		const initMsgs = panel._postedMessages.filter((m: any) => m && m.type === 'init') as any[];
		assert.ok(initMsgs.length >= 2, 'expected at least 2 init messages (initial + refresh)');
		assert.strictEqual(initMsgs[initMsgs.length - 1].rows.length, 2);
	});

	it('rejects malformed apply payload via postError', async () => {
		const snap: PickerSnapshot = { rows: [], warnings: [] };
		const client = makeClient(snap);
		const cache = await freshCache([]);
		await SapPickerPanel.createOrShow(FAKE_URI, client, cache, new MockSecretStorage() as any);
		await flush();
		const panel = latestPanel();
		panel.webview._simulateMessage({ type: 'apply', diffs: 'not-an-array' });
		await flush();
		const errors = panel._postedMessages.filter((m: any) => m && m.type === 'error') as any[];
		assert.ok(errors.length > 0, 'expected error postMessage');
	});
});
