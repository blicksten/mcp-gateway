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
import { buildSapPickerHtml } from '../../webview/sap-picker-html';
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

// ---------------------------------------------------------------------------
// Regression tests for 2026-06-03 bug fixes (bugfix-18185ec5)
// Bug 1: runOneOp post-add health poll timeout raised from 5_000 → 30_000 ms
// Bug 2: buildLiveStatusBadge present in HTML + init rows carry registered+status
// ---------------------------------------------------------------------------

describe('SapPickerPanel — regression: Bug 1 poll timeout 30_000 (2026-06-03)', () => {
	// Spy on the private pollServerRunning method via prototype patching.
	// Records every (serverName, timeoutMs) pair passed to it and returns
	// 'running' immediately so the test does not block on real polling.
	// This test FAILS when the constant is 5_000 and PASSES at 30_000.
	let pollCalls: Array<{ name: string; timeoutMs: number }> = [];
	let originalPoll: unknown;

	beforeEach(() => {
		resetMockState();
		SapPickerPanel._reset();
		pollCalls = [];
		// Patch prototype — TypeScript marks this private but JS allows it.
		// eslint-disable-next-line @typescript-eslint/no-explicit-any
		originalPoll = (SapPickerPanel.prototype as any)['pollServerRunning'];
		// eslint-disable-next-line @typescript-eslint/no-explicit-any
		(SapPickerPanel.prototype as any)['pollServerRunning'] = async function (
			name: string,
			timeoutMs: number,
		): Promise<'running' | 'error' | 'timeout'> {
			pollCalls.push({ name, timeoutMs });
			return 'running';
		};
	});

	afterEach(() => {
		// Restore original method before teardown.
		// eslint-disable-next-line @typescript-eslint/no-explicit-any
		(SapPickerPanel.prototype as any)['pollServerRunning'] = originalPoll;
		SapPickerPanel._reset();
	});

	it('runOneOp invokes pollServerRunning with 30_000 ms (not 5_000) after addServer', async () => {
		// Arrange: one unregistered VSP row that can be added.
		const snap: PickerSnapshot = {
			rows: [{ sid: 'DEV', client: '100', user: '', kpMissing: false, registered: { vsp: false, gui: false }, status: { vsp: '', gui: '' } }],
			warnings: [],
		};
		const client = makeClient(snap);
		const cache = await freshCache([]);
		await SapPickerPanel.createOrShow(FAKE_URI, client, cache, new MockSecretStorage() as any);
		await flush();
		const panel = latestPanel();

		// Act: trigger an apply that adds the VSP server.
		panel.webview._simulateMessage({
			type: 'apply',
			diffs: [{ rowKey: 'DEV-100', desired: { vsp: true, gui: false }, override: { vspCommand: '/opt/vsp' } }],
		});
		await flush(20);

		// Assert: pollServerRunning was called with the 30s timeout.
		// If the constant were still 5_000, this assertion FAILS: 5000 !== 30000.
		assert.ok(pollCalls.length >= 1, 'pollServerRunning must have been called');
		const vspPoll = pollCalls.find((c) => c.name === 'vsp-DEV-100');
		assert.ok(vspPoll, 'expected a poll call for vsp-DEV-100');
		assert.strictEqual(
			vspPoll.timeoutMs,
			30_000,
			`Bug 1 regression: poll timeout must be 30_000 ms, got ${vspPoll.timeoutMs}`,
		);
	});

	it('runOneOp with 30_000 poll returning "running" yields config_added_running row status', async () => {
		// This test verifies the full path: spy returns 'running' → row transitions
		// to config_added_running. It would show config_added_start_failed if the
		// poll returned 'timeout' (which it would if called with the old 5_000 and
		// Date.now jumped past 5 s).
		const snap: PickerSnapshot = {
			rows: [{ sid: 'QAS', client: '200', user: '', kpMissing: false, registered: { vsp: false, gui: false }, status: { vsp: '', gui: '' } }],
			warnings: [],
		};
		const client = makeClient(snap);
		const cache = await freshCache([]);
		await SapPickerPanel.createOrShow(FAKE_URI, client, cache, new MockSecretStorage() as any);
		await flush();
		const panel = latestPanel();

		panel.webview._simulateMessage({
			type: 'apply',
			diffs: [{ rowKey: 'QAS-200', desired: { vsp: true, gui: false }, override: { vspCommand: '/opt/vsp-qas' } }],
		});
		await flush(20);

		// The 'rows' postMessages carry the serialized row state.
		const rowsMsgs = panel._postedMessages.filter((m: any) => m && m.type === 'rows') as any[];
		assert.ok(rowsMsgs.length > 0, 'expected at least one rows postMessage');
		const lastRows = rowsMsgs[rowsMsgs.length - 1];
		const qas = lastRows.rows.find((r: any) => r.key === 'QAS-200');
		assert.ok(qas, 'QAS-200 row must be present');
		assert.strictEqual(
			qas.vspStatus,
			'config_added_running',
			`Bug 1 regression: expected config_added_running (poll→running), got ${qas.vspStatus}`,
		);
	});
});

describe('buildSapPickerHtml — regression: Bug 2 buildLiveStatusBadge present (2026-06-03)', () => {
	// The webview JS template lives inside the HTML string returned by
	// buildSapPickerHtml. Unit-testing the DOM rendering requires jsdom which
	// is not in the project test stack. Instead we assert the function
	// definition is present in the generated HTML — a compile-level check
	// that the dead-code-elimination or a template edit hasn't removed it.
	// Full rendering is covered by the tsc build + dogfood-smoke checklist.
	it('buildSapPickerHtml output contains buildLiveStatusBadge function definition', () => {
		const html = buildSapPickerHtml('test-nonce-abc', 'vscode-resource:');
		assert.ok(
			html.includes('buildLiveStatusBadge'),
			'Bug 2 regression: buildLiveStatusBadge must be present in webview HTML',
		);
	});

	it('buildSapPickerHtml output contains registered-row CSS highlight rule (tbody tr.registered)', () => {
		const html = buildSapPickerHtml('test-nonce-abc', 'vscode-resource:');
		assert.ok(
			html.includes('tbody tr.registered'),
			'Bug 2 regression: registered-row CSS class must be present in webview HTML',
		);
	});
});

describe('SapPickerPanel — regression: Bug 2 live status badge (2026-06-03)', () => {
	beforeEach(() => {
		resetMockState();
		SapPickerPanel._reset();
	});
	afterEach(() => {
		SapPickerPanel._reset();
	});

	it('init message rows carry both registered and status fields needed for live badge fallback', async () => {
		// The webview's buildComponentCell checks `row.registered.vsp` (or gui)
		// and `row.status.vsp` (or gui) to render the live badge when the row is
		// already registered but no in-session lifecycle activity has occurred.
		// This test verifies the host serializes those fields into every 'init' row.
		const snap: PickerSnapshot = {
			rows: [
				{
					sid: 'PRD',
					client: '300',
					user: '',
					kpMissing: false,
					// Already registered VSP with a running daemon status.
					registered: { vsp: true, gui: false },
					status: { vsp: 'running', gui: '' },
				},
				{
					sid: 'DEV',
					client: '100',
					user: '',
					kpMissing: false,
					// Unregistered row — badge should be absent.
					registered: { vsp: false, gui: false },
					status: { vsp: '', gui: '' },
				},
			],
			warnings: [],
		};
		const client = makeClient(snap);
		const cache = await freshCache([]);
		await SapPickerPanel.createOrShow(FAKE_URI, client, cache, new MockSecretStorage() as any);
		await flush();
		const panel = latestPanel();
		const init = panel._postedMessages.find((m: any) => m && m.type === 'init') as any;
		assert.ok(init, 'expected init postMessage');

		const prd = init.rows.find((r: any) => r.key === 'PRD-300');
		assert.ok(prd, 'PRD-300 row must be present');
		// registered + status must both be serialized (Bug 2 relies on them).
		assert.deepStrictEqual(prd.registered, { vsp: true, gui: false },
			'Bug 2: registered field must be serialized so webview can render live badge');
		assert.deepStrictEqual(prd.status, { vsp: 'running', gui: '' },
			'Bug 2: status field must be serialized so webview can render live badge colour');

		const dev = init.rows.find((r: any) => r.key === 'DEV-100');
		assert.ok(dev, 'DEV-100 row must be present');
		assert.deepStrictEqual(dev.registered, { vsp: false, gui: false });
		assert.deepStrictEqual(dev.status, { vsp: '', gui: '' });
	});
});
