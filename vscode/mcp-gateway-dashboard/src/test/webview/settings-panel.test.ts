import '../mock-vscode';
import { strict as assert } from 'node:assert';
import { describe, it, beforeEach, afterEach } from 'mocha';
import {
	resetMockState,
	mockWebviewPanels,
	mockConfigValues,
	mockConfigUpdateCalls,
	dialogResponses,
	mockCalls,
	setMockOpenDialogResult,
	type MockWebviewPanel,
} from '../mock-vscode';
import { SettingsPanel } from '../../webview/settings-panel';

const FAKE_URI = { scheme: 'file', path: '/test', with: () => ({}), toString: () => 'file:///test' } as any;

async function flush(times = 4): Promise<void> {
	for (let i = 0; i < times; i++) {
		await new Promise((r) => setImmediate(r));
	}
}

function latestPanel(): MockWebviewPanel {
	assert.ok(mockWebviewPanels.length > 0, 'expected a webview panel');
	return mockWebviewPanels[mockWebviewPanels.length - 1];
}

describe('SettingsPanel', () => {
	beforeEach(() => {
		resetMockState();
		SettingsPanel._reset();
		// Clear any test-set config values from prior test runs
		for (const k of Object.keys(mockConfigValues)) { delete mockConfigValues[k]; }
	});
	afterEach(() => {
		SettingsPanel._reset();
	});

	it('opens a webview with CSP nonce and sticky-layout markup', async () => {
		mockConfigValues['mcpGateway.apiUrl'] = 'http://localhost:8765';
		await SettingsPanel.createOrShow(FAKE_URI, {});
		const panel = latestPanel();
		assert.match(panel.webview.html, /MCP Gateway — Settings/);
		assert.match(panel.webview.html, /Content-Security-Policy/);
		assert.match(panel.webview.html, /position: sticky/);
		// 4 new Phase C keys must appear in the schema injected into the page
		assert.match(panel.webview.html, /mcpGateway\.defaultVspCommand/);
		assert.match(panel.webview.html, /mcpGateway\.defaultGuiUvProject/);
		assert.match(panel.webview.html, /mcpGateway\.defaultGuiMode/);
		assert.match(panel.webview.html, /mcpGateway\.uvPath/);
	});

	it('save batches changed fields into a single sequence of update() calls', async () => {
		await SettingsPanel.createOrShow(FAKE_URI, {});
		const panel = latestPanel();
		panel.webview._simulateMessage({
			type: 'save',
			changes: {
				'mcpGateway.apiUrl': 'http://localhost:9999',
				'mcpGateway.verboseLogging': true,
				'mcpGateway.pollInterval': 7500,
			},
		});
		await flush(8);
		assert.strictEqual(mockConfigUpdateCalls.length, 3, 'expected exactly 3 update() calls');
		const apiCall = mockConfigUpdateCalls.find((c) => c.key === 'mcpGateway.apiUrl');
		const verboseCall = mockConfigUpdateCalls.find((c) => c.key === 'mcpGateway.verboseLogging');
		const pollCall = mockConfigUpdateCalls.find((c) => c.key === 'mcpGateway.pollInterval');
		assert.strictEqual(apiCall?.value, 'http://localhost:9999');
		assert.strictEqual(verboseCall?.value, true);
		assert.strictEqual(pollCall?.value, 7500);
		// Saved postMessage with status
		const saved = panel._postedMessages.find((m: any) => m && m.type === 'saved') as any;
		assert.ok(saved, 'expected saved postMessage');
	});

	it('save surfaces restart-required toast when a restart-required key changes (R-29 / X5)', async () => {
		dialogResponses.showInformationMessage = 'Later'; // user dismisses restart prompt
		await SettingsPanel.createOrShow(FAKE_URI, {});
		const panel = latestPanel();
		panel.webview._simulateMessage({
			type: 'save',
			changes: { 'mcpGateway.daemonPath': '/opt/mcp-gateway' },
		});
		await flush(8);
		// showInformationMessage was called with restart language
		const infoMsgs = mockCalls.infoMessages;
		assert.ok(infoMsgs.length > 0, 'expected showInformationMessage call');
		assert.match(infoMsgs[infoMsgs.length - 1], /Restart daemon/);
	});

	it('restart-required toast invokes injected restartDaemon when user clicks "Restart Daemon"', async () => {
		let restartCalled = 0;
		const restartDaemon = async () => { restartCalled++; };
		dialogResponses.showInformationMessage = 'Restart Daemon';
		await SettingsPanel.createOrShow(FAKE_URI, { restartDaemon });
		const panel = latestPanel();
		panel.webview._simulateMessage({
			type: 'save',
			changes: { 'mcpGateway.authTokenPath': '/etc/auth.token' },
		});
		await flush(10);
		assert.strictEqual(restartCalled, 1, 'restartDaemon must be called when user clicks "Restart Daemon"');
	});

	it('save with no restart-required keys does not surface the toast', async () => {
		await SettingsPanel.createOrShow(FAKE_URI, {});
		const panel = latestPanel();
		panel.webview._simulateMessage({
			type: 'save',
			changes: { 'mcpGateway.verboseLogging': true },
		});
		await flush(8);
		// No restart-required key => showInformationMessage not invoked
		const infoMsgs = mockCalls.infoMessages;
		assert.strictEqual(infoMsgs.length, 0, 'no toast for non-restart key');
	});

	it('rejects unknown setting key on save with postError', async () => {
		await SettingsPanel.createOrShow(FAKE_URI, {});
		const panel = latestPanel();
		panel.webview._simulateMessage({
			type: 'save',
			changes: { 'mcpGateway.totallyMadeUpKey': 'nope' },
		});
		await flush(8);
		const errors = panel._postedMessages.filter((m: any) => m && m.type === 'error') as any[];
		assert.ok(errors.length > 0, 'expected error postMessage for unknown key');
		assert.match(errors[0].message, /Unknown setting/);
		// No update call should have happened
		assert.strictEqual(mockConfigUpdateCalls.length, 0);
	});

	it('rejects bad number value on save with postError', async () => {
		await SettingsPanel.createOrShow(FAKE_URI, {});
		const panel = latestPanel();
		panel.webview._simulateMessage({
			type: 'save',
			changes: { 'mcpGateway.pollInterval': 'not-a-number' },
		});
		await flush(8);
		const errors = panel._postedMessages.filter((m: any) => m && m.type === 'error') as any[];
		assert.ok(errors.length > 0);
		assert.match(errors[0].message, /must be a number/);
	});

	it('importFromMcpDashboard maps mcpDashboard.* → mcpGateway.* (S1) and only fills empty targets', async () => {
		// Two dashboard fields populated, one not
		mockConfigValues['mcpDashboard.keepassDbPath'] = '/imported/keepass.kdbx';
		mockConfigValues['mcpDashboard.vibingPath'] = '/imported/sap-vsp';
		// Pre-existing non-empty target on uvPath should NOT be overwritten
		mockConfigValues['mcpGateway.uvPath'] = '/already/uv';
		// sapGuiPath dashboard field set → should map to defaultGuiUvProject + defaultGuiMode='uv'
		mockConfigValues['mcpDashboard.sapGuiPath'] = '/imported/sap-gui-uv-project';
		mockConfigValues['mcpDashboard.uvPath'] = '/dashboard-uv'; // exists but target uvPath is non-empty

		await SettingsPanel.createOrShow(FAKE_URI, {});
		const panel = latestPanel();
		panel.webview._simulateMessage({ type: 'importFromMcpDashboard' });
		await flush(4);
		const imported = panel._postedMessages.find((m: any) => m && m.type === 'imported') as any;
		assert.ok(imported, 'expected imported postMessage');
		// keepassPath was empty → imported. vibingPath → defaultVspCommand mapped → imported.
		// sapGuiPath → defaultGuiUvProject + extra defaultGuiMode='uv'.
		// uvPath was non-empty (mcpGateway.uvPath = /already/uv) → NOT imported.
		assert.strictEqual(imported.staged['mcpGateway.keepassPath'], '/imported/keepass.kdbx');
		assert.strictEqual(imported.staged['mcpGateway.defaultVspCommand'], '/imported/sap-vsp');
		assert.strictEqual(imported.staged['mcpGateway.defaultGuiUvProject'], '/imported/sap-gui-uv-project');
		assert.strictEqual(imported.staged['mcpGateway.defaultGuiMode'], 'uv');
		assert.strictEqual(imported.staged['mcpGateway.uvPath'], undefined,
			'pre-filled uvPath must NOT be overwritten');
		// Import does not commit — no update calls until Save fires
		assert.strictEqual(mockConfigUpdateCalls.length, 0,
			'Import must stage only; Save flushes');
	});

	it('importFromMcpDashboard returns count=0 + empty staged when no dashboard values exist', async () => {
		await SettingsPanel.createOrShow(FAKE_URI, {});
		const panel = latestPanel();
		panel.webview._simulateMessage({ type: 'importFromMcpDashboard' });
		await flush();
		const imported = panel._postedMessages.find((m: any) => m && m.type === 'imported') as any;
		assert.strictEqual(imported.count, 0);
		assert.deepStrictEqual(imported.staged, {});
	});

	it('Browse posts browseResult with the picked path (T-C.2)', async () => {
		setMockOpenDialogResult(['/picked/path']);
		await SettingsPanel.createOrShow(FAKE_URI, {});
		const panel = latestPanel();
		panel.webview._simulateMessage({
			type: 'browse',
			key: 'mcpGateway.daemonPath',
			currentValue: '',
		});
		await flush(8);
		const result = panel._postedMessages.find((m: any) => m && m.type === 'browseResult') as any;
		assert.ok(result, 'expected browseResult postMessage');
		assert.strictEqual(result.path, '/picked/path');
		assert.strictEqual(result.key, 'mcpGateway.daemonPath');
	});

	it('Browse cancel branch posts no browseResult', async () => {
		setMockOpenDialogResult(null);
		await SettingsPanel.createOrShow(FAKE_URI, {});
		const panel = latestPanel();
		panel.webview._simulateMessage({
			type: 'browse',
			key: 'mcpGateway.daemonPath',
			currentValue: '',
		});
		await flush(4);
		const result = panel._postedMessages.find((m: any) => m && m.type === 'browseResult') as any;
		assert.strictEqual(result, undefined);
	});

	it('Browse on a non-path field does NOT open a dialog', async () => {
		setMockOpenDialogResult(['/should/not/get/used']);
		await SettingsPanel.createOrShow(FAKE_URI, {});
		const panel = latestPanel();
		panel.webview._simulateMessage({
			type: 'browse',
			key: 'mcpGateway.verboseLogging', // boolean kind=plain
			currentValue: '',
		});
		await flush(4);
		const result = panel._postedMessages.find((m: any) => m && m.type === 'browseResult') as any;
		assert.strictEqual(result, undefined,
			'browse on plain (non-path) field must be a no-op');
	});

	it('validate posts validation result for path fields with injected probe (waits past 300ms debounce)', async () => {
		const probe = async (v: string) => ({ ok: v === '/exists', message: v === '/exists' ? 'OK' : 'Not found' });
		await SettingsPanel.createOrShow(FAKE_URI, { probe });
		const panel = latestPanel();
		panel.webview._simulateMessage({
			type: 'validate',
			key: 'mcpGateway.daemonPath',
			value: '/exists',
		});
		// 300ms debounce + small buffer so the trailing-edge probe fires
		await new Promise((r) => setTimeout(r, 360));
		await flush(4);
		const validation = panel._postedMessages.find((m: any) => m && m.type === 'validation') as any;
		assert.ok(validation, 'expected validation postMessage');
		assert.strictEqual(validation.key, 'mcpGateway.daemonPath');
		assert.strictEqual(validation.result.ok, true);
	});

	it('save with mixed valid + invalid keys: rejects atomically (no partial writes)', async () => {
		await SettingsPanel.createOrShow(FAKE_URI, {});
		const panel = latestPanel();
		// One valid key + one bad value — entire batch must reject
		panel.webview._simulateMessage({
			type: 'save',
			changes: {
				'mcpGateway.apiUrl': 'http://localhost:8765',
				'mcpGateway.pollInterval': 'bogus', // invalid number
			},
		});
		await flush(8);
		assert.strictEqual(mockConfigUpdateCalls.length, 0,
			'partial write must not happen; reject atomically');
		const errors = panel._postedMessages.filter((m: any) => m && m.type === 'error') as any[];
		assert.ok(errors.length > 0);
	});

	it('cancel disposes the panel', async () => {
		await SettingsPanel.createOrShow(FAKE_URI, {});
		const panel = latestPanel();
		panel.webview._simulateMessage({ type: 'cancel' });
		await flush();
		assert.strictEqual(panel.disposed, true);
	});
});
