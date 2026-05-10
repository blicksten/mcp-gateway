import '../mock-vscode';
import { strict as assert } from 'node:assert';
import { describe, it, beforeEach, afterEach } from 'mocha';
import {
	resetMockState,
	mockWebviewPanels,
	type MockWebviewPanel,
} from '../mock-vscode';
import { ImportClaudePanel } from '../../webview/import-claude-panel';
import type { IGatewayClient } from '../../extension';
import type { ImportSnapshot, ImportOpResult } from '../../import-claude-state';

interface RecordedCall { method: string; args: unknown[] }

interface FakeClient extends IGatewayClient {
	calls: RecordedCall[];
	snapshotResponses: ImportSnapshot[];
	applyResponse?: { results: ImportOpResult[] };
	importApplyError?: Error;
	importSnapshotError?: Error;
}

function makeClient(initial: ImportSnapshot): FakeClient {
	const calls: RecordedCall[] = [];
	const queue: ImportSnapshot[] = [initial];
	const c: FakeClient = {
		calls,
		snapshotResponses: queue,
		listServers: async () => [],
		getHealth: async () => ({ status: 'ok', servers: 0, running: 0 }),
		shutdown: async () => ({ status: 'shutting_down' }),
		getServer: async () => ({}),
		addServer: async () => ({ status: 'ok' }),
		removeServer: async () => ({ status: 'ok' }),
		patchServer: async () => ({}),
		restartServer: async () => ({}),
		resetCircuit: async () => ({}),
		callTool: async () => ({ content: null }),
		listTools: async () => [],
		importSnapshot: async (source: string, projectRoot?: string) => {
			calls.push({ method: 'importSnapshot', args: [source, projectRoot] });
			if (c.importSnapshotError) { throw c.importSnapshotError; }
			const snap = c.snapshotResponses.shift() ?? initial;
			return snap;
		},
		importApply: async (ops: unknown[]) => {
			calls.push({ method: 'importApply', args: [ops] });
			if (c.importApplyError) { throw c.importApplyError; }
			return c.applyResponse ?? { results: [] };
		},
	};
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

const SNAP_BASE: ImportSnapshot = {
	source: 'cc_global',
	path: '/Users/x/.claude.json',
	exists: true,
	rows: [
		{
			source: 'cc_global', name: 'fs', type: 'stdio', command: 'npx', args: ['-y', '@modelcontextprotocol/server-filesystem'],
			gateway_has_name: false, previously_imported: false,
		},
		{
			source: 'cc_global', name: 'github', type: 'stdio', command: 'npx', args: ['-y', '@modelcontextprotocol/server-github'],
			gateway_has_name: true, drift_fields: ['args'], previously_imported: true, previously_imported_at: '2026-05-01T10:00:00Z',
		},
	],
	warnings: ['1 entry uses deprecated transport'],
};

describe('ImportClaudePanel', () => {
	beforeEach(() => {
		resetMockState();
		ImportClaudePanel._reset();
	});
	afterEach(() => {
		ImportClaudePanel._reset();
	});

	it('opens a webview, fetches snapshot, posts init with rows + warnings + path', async () => {
		const client = makeClient(SNAP_BASE);
		await ImportClaudePanel.createOrShow(FAKE_URI, client);
		await flush();
		const panel = latestPanel();
		assert.ok(panel.webview.html.includes('Import from Claude'), 'expected title');
		assert.ok(panel.webview.html.includes('Content-Security-Policy'), 'expected CSP');
		const init = panel._postedMessages.find((m: any) => m && m.type === 'init') as any;
		assert.ok(init, 'expected init postMessage');
		assert.strictEqual(init.rows.length, 2);
		assert.strictEqual(init.source, 'cc_global');
		assert.strictEqual(init.path, '/Users/x/.claude.json');
		assert.strictEqual(init.exists, true);
		assert.deepStrictEqual(init.warnings, ['1 entry uses deprecated transport']);
		// Provenance + drift_fields propagate
		const github = init.rows.find((r: any) => r.name === 'github');
		assert.strictEqual(github.previously_imported, true);
		assert.deepStrictEqual(github.drift_fields, ['args']);
	});

	it('apply flow: importApply called with checked rows; status updates flow back', async () => {
		const client = makeClient(SNAP_BASE);
		client.applyResponse = {
			results: [
				{ name: 'fs', dest_name: 'fs', action: 'copy', status: 'applied', resolved_command: '/usr/bin/npx', source_updated: false },
			],
		};
		await ImportClaudePanel.createOrShow(FAKE_URI, client);
		await flush();
		const panel = latestPanel();
		// Operator checks 'fs' row, leaves defaults (action=copy, conflict=skip)
		panel.webview._simulateMessage({
			type: 'apply',
			edits: [
				{ rowKey: 'cc_global::fs', checked: true, action: 'copy', conflict: 'skip', destName: '' },
				{ rowKey: 'cc_global::github', checked: false, action: 'copy', conflict: 'skip', destName: '' },
			],
		});
		await flush(20);
		const applyCall = client.calls.find((c) => c.method === 'importApply');
		assert.ok(applyCall, 'importApply must be called');
		const ops = applyCall!.args[0] as Array<{ name: string; action: string; conflict: string }>;
		assert.strictEqual(ops.length, 1, 'only checked rows yield ops');
		assert.strictEqual(ops[0].name, 'fs');
		assert.strictEqual(ops[0].action, 'copy');
		assert.strictEqual(ops[0].conflict, 'skip');
		// rows postMessage shows applied status
		const rowsMsgs = panel._postedMessages.filter((m: any) => m && m.type === 'rows') as any[];
		const lastRows = rowsMsgs[rowsMsgs.length - 1];
		const fsRow = lastRows.rows.find((r: any) => r.key === 'cc_global::fs');
		assert.strictEqual(fsRow.status, 'applied');
		assert.strictEqual(fsRow.resolvedCommand, '/usr/bin/npx');
	});

	it('apply with no checked rows posts "no changes" without calling importApply', async () => {
		const client = makeClient(SNAP_BASE);
		await ImportClaudePanel.createOrShow(FAKE_URI, client);
		await flush();
		const panel = latestPanel();
		panel.webview._simulateMessage({
			type: 'apply',
			edits: [
				{ rowKey: 'cc_global::fs', checked: false, action: 'copy', conflict: 'skip', destName: '' },
			],
		});
		await flush(8);
		const applyCalls = client.calls.filter((c) => c.method === 'importApply').length;
		assert.strictEqual(applyCalls, 0, 'no checked rows → no apply call');
		const applied = panel._postedMessages.find((m: any) => m && m.type === 'applied') as any;
		assert.ok(applied);
		assert.match(applied.summary, /No changes/);
	});

	it('switchSource refetches snapshot for the new source', async () => {
		const client = makeClient(SNAP_BASE);
		const projectSnap: ImportSnapshot = {
			source: 'cc_project', path: '/ws/.mcp.json', exists: true, rows: [], warnings: [],
		};
		client.snapshotResponses.push(projectSnap);
		await ImportClaudePanel.createOrShow(FAKE_URI, client);
		await flush();
		const panel = latestPanel();
		panel.webview._simulateMessage({ type: 'switchSource', source: 'cc_project' });
		await flush(8);
		const importCalls = client.calls.filter((c) => c.method === 'importSnapshot').map((c) => c.args[0]);
		assert.deepStrictEqual(importCalls, ['cc_global', 'cc_project']);
	});

	it('rejects unknown source on switchSource (defence-in-depth)', async () => {
		const client = makeClient(SNAP_BASE);
		await ImportClaudePanel.createOrShow(FAKE_URI, client);
		await flush();
		const panel = latestPanel();
		const before = client.calls.length;
		panel.webview._simulateMessage({ type: 'switchSource', source: 'invalid_source' });
		await flush(4);
		const after = client.calls.length;
		assert.strictEqual(after, before, 'invalid source must not trigger refresh');
		const errs = panel._postedMessages.filter((m: any) => m && m.type === 'error');
		assert.ok(errs.length >= 1, 'expected error postMessage for invalid source');
	});

	it('rejects malformed apply payload (action=duplicate) and posts error', async () => {
		const client = makeClient(SNAP_BASE);
		await ImportClaudePanel.createOrShow(FAKE_URI, client);
		await flush();
		const panel = latestPanel();
		panel.webview._simulateMessage({
			type: 'apply',
			edits: [
				{ rowKey: 'cc_global::fs', checked: true, action: 'duplicate', conflict: 'skip', destName: '' },
			],
		});
		await flush(8);
		const applyCalls = client.calls.filter((c) => c.method === 'importApply').length;
		assert.strictEqual(applyCalls, 0, 'malformed action must reject before HTTP');
		const errs = panel._postedMessages.filter((m: any) => m && m.type === 'error');
		assert.ok(errs.length >= 1, 'expected error postMessage');
	});

	it('importApply throws → rows marked error, applying state resets', async () => {
		const client = makeClient(SNAP_BASE);
		client.importApplyError = new Error('daemon offline');
		await ImportClaudePanel.createOrShow(FAKE_URI, client);
		await flush();
		const panel = latestPanel();
		panel.webview._simulateMessage({
			type: 'apply',
			edits: [
				{ rowKey: 'cc_global::fs', checked: true, action: 'copy', conflict: 'skip', destName: '' },
			],
		});
		await flush(20);
		const rowsMsgs = panel._postedMessages.filter((m: any) => m && m.type === 'rows') as any[];
		const lastRows = rowsMsgs[rowsMsgs.length - 1];
		const fsRow = lastRows.rows.find((r: any) => r.key === 'cc_global::fs');
		assert.strictEqual(fsRow.status, 'error');
		assert.match(fsRow.error, /daemon offline/);
		// applying must reset to false in finally branch
		const applyingMsgs = panel._postedMessages.filter((m: any) => m && m.type === 'applying') as any[];
		const lastApplying = applyingMsgs[applyingMsgs.length - 1];
		assert.strictEqual(lastApplying.active, false);
	});

	it('retryFailed scopes ops by PRE-RESET failed keys, ignoring fresh idle+checked rows (review finding MEDIUM-1)', async () => {
		const client = makeClient(SNAP_BASE);
		// First Apply: only github checked (and it errors). fs stays idle.
		client.applyResponse = {
			results: [
				{ name: 'github', dest_name: 'github', action: 'copy', status: 'error', reason: 'boom', source_updated: false },
			],
		};
		await ImportClaudePanel.createOrShow(FAKE_URI, client);
		await flush();
		const panel = latestPanel();
		panel.webview._simulateMessage({
			type: 'apply',
			edits: [
				{ rowKey: 'cc_global::fs', checked: false, action: 'copy', conflict: 'skip', destName: '' },
				{ rowKey: 'cc_global::github', checked: true, action: 'copy', conflict: 'skip', destName: '' },
			],
		});
		await flush(20);
		// Now retry — operator ALSO checks fs (fresh idle row). Only github
		// (the actually-failed row) should be re-run; fs must NOT slip through
		// post-reset filtering.
		const callsBefore = client.calls.filter((c) => c.method === 'importApply').length;
		panel.webview._simulateMessage({
			type: 'retryFailed',
			edits: [
				{ rowKey: 'cc_global::fs', checked: true, action: 'copy', conflict: 'skip', destName: '' },
				{ rowKey: 'cc_global::github', checked: true, action: 'copy', conflict: 'skip', destName: '' },
			],
		});
		await flush(20);
		const importApplyCalls = client.calls.filter((c) => c.method === 'importApply');
		assert.strictEqual(importApplyCalls.length, callsBefore + 1);
		const retryOps = importApplyCalls[importApplyCalls.length - 1].args[0] as Array<{ name: string }>;
		assert.strictEqual(retryOps.length, 1, 'retry must only run the previously-failed row');
		assert.strictEqual(retryOps[0].name, 'github');
	});

	it('retryFailed only re-runs error/conflict rows, not applied/skipped', async () => {
		const client = makeClient(SNAP_BASE);
		// Initial apply: fs applied, github error
		client.applyResponse = {
			results: [
				{ name: 'fs', dest_name: 'fs', action: 'copy', status: 'applied', source_updated: false },
				{ name: 'github', dest_name: 'github', action: 'copy', status: 'error', reason: 'bad command', source_updated: false },
			],
		};
		await ImportClaudePanel.createOrShow(FAKE_URI, client);
		await flush();
		const panel = latestPanel();
		panel.webview._simulateMessage({
			type: 'apply',
			edits: [
				{ rowKey: 'cc_global::fs', checked: true, action: 'copy', conflict: 'skip', destName: '' },
				{ rowKey: 'cc_global::github', checked: true, action: 'copy', conflict: 'skip', destName: '' },
			],
		});
		await flush(20);
		// Now retry — second importApply should re-include only github
		client.applyResponse = {
			results: [
				{ name: 'github', dest_name: 'github', action: 'copy', status: 'applied', source_updated: false },
			],
		};
		const callsBefore = client.calls.filter((c) => c.method === 'importApply').length;
		panel.webview._simulateMessage({
			type: 'retryFailed',
			edits: [
				{ rowKey: 'cc_global::fs', checked: true, action: 'copy', conflict: 'skip', destName: '' },
				{ rowKey: 'cc_global::github', checked: true, action: 'copy', conflict: 'skip', destName: '' },
			],
		});
		await flush(20);
		const importApplyCalls = client.calls.filter((c) => c.method === 'importApply');
		assert.strictEqual(importApplyCalls.length, callsBefore + 1, 'retry triggers a new importApply');
		const retryOps = importApplyCalls[importApplyCalls.length - 1].args[0] as Array<{ name: string }>;
		// fs is in 'applied' terminal state — buildImportOpsList drops it.
		assert.strictEqual(retryOps.length, 1);
		assert.strictEqual(retryOps[0].name, 'github');
	});
});
