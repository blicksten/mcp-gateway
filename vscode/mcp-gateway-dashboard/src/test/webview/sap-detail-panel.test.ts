import '../mock-vscode';
import { strict as assert } from 'node:assert';
import { mockWebviewPanels, resetMockState, getRegisteredCommands } from '../mock-vscode';
import { SapDetailPanel } from '../../webview/sap-detail-panel';
import { MockSecretStorage, MockMemento } from '../mock-vscode';
import { CredentialStore } from '../../credential-store';
import type { SapSystem } from '../../sap-detector';

function createMockClient() {
	return {
		listServers: async () => [],
		getHealth: async () => ({}),
		getServer: async () => { throw new Error('not found'); },
		addServer: async () => ({}),
		removeServer: async () => ({}),
		patchServer: async () => ({}),
		restartServer: async () => ({}),
		resetCircuit: async () => ({}),
		callTool: async () => ({ content: null }),
		listTools: async () => [],
	};
}

function createMockContext() {
	return {
		secrets: new MockSecretStorage(),
		globalState: new MockMemento(),
		subscriptions: [] as Array<{ dispose(): void }>,
		extensionUri: { scheme: 'file', path: '/test', with: () => ({}), toString: () => 'file:///test' },
	};
}

const testSystem: SapSystem = {
	key: 'DEV-100',
	sid: 'DEV',
	client: '100',
	vsp: { name: 'vsp-DEV-100', status: 'running', transport: 'stdio', restart_count: 0 },
	gui: { name: 'sap-gui-DEV-100', status: 'running', transport: 'http', restart_count: 0 },
	status: 'running',
};

describe('SapDetailPanel', () => {
	let ctx: ReturnType<typeof createMockContext>;
	let credStore: CredentialStore;
	let client: ReturnType<typeof createMockClient>;

	beforeEach(() => {
		resetMockState();
		SapDetailPanel._clearPanels();
		ctx = createMockContext();
		credStore = new CredentialStore(ctx as any);
		client = createMockClient();
	});

	afterEach(() => {
		resetMockState();
		SapDetailPanel._clearPanels();
	});

	it('creates a webview panel', async () => {
		await SapDetailPanel.createOrShow(ctx.extensionUri as any, testSystem, credStore, client as any);
		assert.equal(mockWebviewPanels.length, 1);
		assert.equal(mockWebviewPanels[0].viewType, 'mcpSapDetail');
		assert.equal(mockWebviewPanels[0].title, 'SAP DEV-100');
	});

	it('generates HTML with CSP and no unsafe-inline', async () => {
		await SapDetailPanel.createOrShow(ctx.extensionUri as any, testSystem, credStore, client as any);
		const html = mockWebviewPanels[0].webview.html;
		assert.ok(html.includes('Content-Security-Policy'));
		assert.ok(!html.includes("'unsafe-inline'"));
	});

	it('nonce is fresh per render', async () => {
		await SapDetailPanel.createOrShow(ctx.extensionUri as any, testSystem, credStore, client as any);
		const html1 = mockWebviewPanels[0].webview.html;

		const panel = await SapDetailPanel.createOrShow(ctx.extensionUri as any, testSystem, credStore, client as any);
		await panel.update(testSystem);
		const html2 = mockWebviewPanels[0].webview.html;

		const nonce1 = html1.match(/<style nonce="([^"]+)">/)?.[1];
		const nonce2 = html2.match(/<style nonce="([^"]+)">/)?.[1];
		assert.ok(nonce1);
		assert.ok(nonce2);
		assert.notEqual(nonce1, nonce2);
	});

	it('singleton behavior: same system returns same panel', async () => {
		await SapDetailPanel.createOrShow(ctx.extensionUri as any, testSystem, credStore, client as any);
		await SapDetailPanel.createOrShow(ctx.extensionUri as any, testSystem, credStore, client as any);
		assert.equal(mockWebviewPanels.length, 1);
	});

	it('different systems create different panels', async () => {
		const system2: SapSystem = {
			key: 'QAS-200', sid: 'QAS', client: '200',
			vsp: { name: 'vsp-QAS-200', status: 'stopped', transport: 'stdio', restart_count: 0 },
			status: 'stopped',
		};
		await SapDetailPanel.createOrShow(ctx.extensionUri as any, testSystem, credStore, client as any);
		await SapDetailPanel.createOrShow(ctx.extensionUri as any, system2, credStore, client as any);
		assert.equal(mockWebviewPanels.length, 2);
	});

	it('dispose removes from singleton map', async () => {
		await SapDetailPanel.createOrShow(ctx.extensionUri as any, testSystem, credStore, client as any);
		mockWebviewPanels[0].dispose();
		await SapDetailPanel.createOrShow(ctx.extensionUri as any, testSystem, credStore, client as any);
		assert.equal(mockWebviewPanels.length, 2);
	});

	it('update() is a no-op after dispose', async () => {
		const panel = await SapDetailPanel.createOrShow(ctx.extensionUri as any, testSystem, credStore, client as any);
		mockWebviewPanels[0].dispose();
		await assert.doesNotReject(() => panel.update(testSystem));
	});

	it('valid action with valid component dispatches command', async () => {
		let dispatched: unknown = undefined;
		getRegisteredCommands().set('mcpGateway._webviewAction', (msg: unknown) => {
			dispatched = msg;
		});

		await SapDetailPanel.createOrShow(ctx.extensionUri as any, testSystem, credStore, client as any);
		mockWebviewPanels[0].webview._simulateMessage({
			type: 'action',
			action: 'restart',
			serverName: 'vsp-DEV-100',
			component: 'vsp',
		});

		assert.ok(dispatched);
		const d = dispatched as Record<string, unknown>;
		assert.equal(d.action, 'restart');
		assert.equal(d.component, 'vsp');
	});

	it('invalid component is dropped (set to undefined)', async () => {
		let dispatched: unknown = undefined;
		getRegisteredCommands().set('mcpGateway._webviewAction', (msg: unknown) => {
			dispatched = msg;
		});

		await SapDetailPanel.createOrShow(ctx.extensionUri as any, testSystem, credStore, client as any);
		mockWebviewPanels[0].webview._simulateMessage({
			type: 'action',
			action: 'restart',
			serverName: 'vsp-DEV-100',
			component: 'malicious',
		});

		assert.ok(dispatched);
		assert.equal((dispatched as Record<string, unknown>).component, undefined);
	});

	it('unknown action is silently dropped', async () => {
		let dispatched = false;
		getRegisteredCommands().set('mcpGateway._webviewAction', () => { dispatched = true; });

		await SapDetailPanel.createOrShow(ctx.extensionUri as any, testSystem, credStore, client as any);
		mockWebviewPanels[0].webview._simulateMessage({
			type: 'action',
			action: 'deleteDisk',
		});

		assert.equal(dispatched, false);
	});

	it('invalid serverName is silently dropped', async () => {
		let dispatched = false;
		getRegisteredCommands().set('mcpGateway._webviewAction', () => { dispatched = true; });

		await SapDetailPanel.createOrShow(ctx.extensionUri as any, testSystem, credStore, client as any);
		mockWebviewPanels[0].webview._simulateMessage({
			type: 'action',
			action: 'restart',
			serverName: '../evil',
		});

		assert.equal(dispatched, false);
	});

	it('null message is silently dropped', async () => {
		await SapDetailPanel.createOrShow(ctx.extensionUri as any, testSystem, credStore, client as any);
		assert.doesNotThrow(() => {
			mockWebviewPanels[0].webview._simulateMessage(null);
		});
	});

	it('updateAll updates matching panels', async () => {
		await SapDetailPanel.createOrShow(ctx.extensionUri as any, testSystem, credStore, client as any);
		const html1 = mockWebviewPanels[0].webview.html;

		const updated: SapSystem = { ...testSystem, status: 'degraded' };
		await SapDetailPanel.updateAll([updated]);
		const html2 = mockWebviewPanels[0].webview.html;

		assert.ok(html2.includes('degraded'));
		assert.notEqual(html1, html2);
	});
});
