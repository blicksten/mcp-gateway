import '../mock-vscode';
import { strict as assert } from 'node:assert';
import { mockWebviewPanels, resetMockState, getRegisteredCommands } from '../mock-vscode';
import { ServerDetailPanel } from '../../webview/server-detail-panel';
import { MockSecretStorage, MockMemento } from '../mock-vscode';
import { CredentialStore } from '../../credential-store';
import type { ServerView } from '../../types';

function createMockClient(servers: ServerView[] = []) {
	return {
		listServers: async () => servers,
		getHealth: async () => ({}),
		getServer: async (name: string) => servers.find((s) => s.name === name) ?? (() => { throw new Error('not found'); })(),
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

const testServer: ServerView = {
	name: 'test-server',
	status: 'running',
	transport: 'stdio',
	restart_count: 0,
};

describe('ServerDetailPanel', () => {
	let ctx: ReturnType<typeof createMockContext>;
	let credStore: CredentialStore;
	let client: ReturnType<typeof createMockClient>;

	beforeEach(() => {
		resetMockState();
		ServerDetailPanel._clearPanels();
		ctx = createMockContext();
		credStore = new CredentialStore(ctx as any);
		client = createMockClient([testServer]);
	});

	afterEach(() => {
		resetMockState();
		ServerDetailPanel._clearPanels();
	});

	it('creates a webview panel', async () => {
		await ServerDetailPanel.createOrShow(ctx.extensionUri as any, testServer, credStore, client as any);
		assert.equal(mockWebviewPanels.length, 1);
		assert.equal(mockWebviewPanels[0].viewType, 'mcpServerDetail');
		assert.equal(mockWebviewPanels[0].title, 'test-server');
	});

	it('generates HTML with CSP and no unsafe-inline', async () => {
		await ServerDetailPanel.createOrShow(ctx.extensionUri as any, testServer, credStore, client as any);
		const html = mockWebviewPanels[0].webview.html;
		assert.ok(html.includes('Content-Security-Policy'));
		assert.ok(!html.includes("'unsafe-inline'"));
	});

	it('nonce is fresh per render (two calls produce different nonces)', async () => {
		const panel = await ServerDetailPanel.createOrShow(ctx.extensionUri as any, testServer, credStore, client as any);
		const html1 = mockWebviewPanels[0].webview.html;

		await panel.update(testServer);
		const html2 = mockWebviewPanels[0].webview.html;

		const nonce1 = html1.match(/<style nonce="([^"]+)">/)?.[1];
		const nonce2 = html2.match(/<style nonce="([^"]+)">/)?.[1];
		assert.ok(nonce1);
		assert.ok(nonce2);
		assert.notEqual(nonce1, nonce2);
	});

	it('singleton behavior: same server returns same panel', async () => {
		await ServerDetailPanel.createOrShow(ctx.extensionUri as any, testServer, credStore, client as any);
		await ServerDetailPanel.createOrShow(ctx.extensionUri as any, testServer, credStore, client as any);
		// Only 1 panel created — second call reveals the existing one.
		assert.equal(mockWebviewPanels.length, 1);
	});

	it('different servers create different panels', async () => {
		const server2: ServerView = { name: 'other-server', status: 'stopped', transport: 'http', restart_count: 0 };
		client = createMockClient([testServer, server2]);
		await ServerDetailPanel.createOrShow(ctx.extensionUri as any, testServer, credStore, client as any);
		await ServerDetailPanel.createOrShow(ctx.extensionUri as any, server2, credStore, client as any);
		assert.equal(mockWebviewPanels.length, 2);
	});

	it('dispose removes from singleton map, new createOrShow creates fresh panel', async () => {
		await ServerDetailPanel.createOrShow(ctx.extensionUri as any, testServer, credStore, client as any);
		mockWebviewPanels[0].dispose(); // triggers onDidDispose
		await ServerDetailPanel.createOrShow(ctx.extensionUri as any, testServer, credStore, client as any);
		assert.equal(mockWebviewPanels.length, 2); // new panel created
	});

	it('update() renders fresh content', async () => {
		const panel = await ServerDetailPanel.createOrShow(ctx.extensionUri as any, testServer, credStore, client as any);
		const html1 = mockWebviewPanels[0].webview.html;
		assert.ok(html1.includes('test-server'));

		await panel.update(testServer);
		const html2 = mockWebviewPanels[0].webview.html;
		assert.ok(html2.includes('test-server'));
	});

	it('update() is a no-op after dispose', async () => {
		const panel = await ServerDetailPanel.createOrShow(ctx.extensionUri as any, testServer, credStore, client as any);
		mockWebviewPanels[0].dispose();
		await assert.doesNotReject(() => panel.update(testServer));
	});

	it('valid action message dispatches command', async () => {
		let dispatched: unknown = undefined;
		getRegisteredCommands().set('mcpGateway._webviewAction', (msg: unknown) => {
			dispatched = msg;
		});

		await ServerDetailPanel.createOrShow(ctx.extensionUri as any, testServer, credStore, client as any);
		mockWebviewPanels[0].webview._simulateMessage({
			type: 'action',
			action: 'restart',
			serverName: 'test-server',
		});

		assert.ok(dispatched);
		assert.equal((dispatched as Record<string, unknown>).action, 'restart');
	});

	it('unknown action is silently dropped', async () => {
		let dispatched = false;
		getRegisteredCommands().set('mcpGateway._webviewAction', () => { dispatched = true; });

		await ServerDetailPanel.createOrShow(ctx.extensionUri as any, testServer, credStore, client as any);
		mockWebviewPanels[0].webview._simulateMessage({
			type: 'action',
			action: 'deleteDisk',
		});

		assert.equal(dispatched, false);
	});

	it('missing type field is silently dropped', async () => {
		let dispatched = false;
		getRegisteredCommands().set('mcpGateway._webviewAction', () => { dispatched = true; });

		await ServerDetailPanel.createOrShow(ctx.extensionUri as any, testServer, credStore, client as any);
		mockWebviewPanels[0].webview._simulateMessage({ action: 'restart' });

		assert.equal(dispatched, false);
	});

	it('invalid serverName (containing /) is silently dropped', async () => {
		let dispatched = false;
		getRegisteredCommands().set('mcpGateway._webviewAction', () => { dispatched = true; });

		await ServerDetailPanel.createOrShow(ctx.extensionUri as any, testServer, credStore, client as any);
		mockWebviewPanels[0].webview._simulateMessage({
			type: 'action',
			action: 'restart',
			serverName: '../evil',
		});

		assert.equal(dispatched, false);
	});

	it('null message is silently dropped', async () => {
		await ServerDetailPanel.createOrShow(ctx.extensionUri as any, testServer, credStore, client as any);
		assert.doesNotThrow(() => {
			mockWebviewPanels[0].webview._simulateMessage(null);
		});
	});
});
