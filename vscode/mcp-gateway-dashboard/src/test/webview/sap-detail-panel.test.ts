import '../mock-vscode';
import { strict as assert } from 'node:assert';
import { mockWebviewPanels, resetMockState, getRegisteredCommands } from '../mock-vscode';
import { SapDetailPanel, REMOVED_AUTO_CLOSE_MS } from '../../webview/sap-detail-panel';
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

	// Phase 8 (B-NEW-20) — SAP detail-panel reconcile when system is removed.
	describe('showRemoved / updateAll reconcile (B-NEW-20)', () => {
		it('updateAll() with empty list switches the panel to a removed banner', async () => {
			await SapDetailPanel.createOrShow(ctx.extensionUri as any, testSystem, credStore, client as any);
			const before = mockWebviewPanels[0].webview.html;
			assert.ok(before.includes('DEV-100'));

			await SapDetailPanel.updateAll([]);

			const after = mockWebviewPanels[0].webview.html;
			assert.ok(after.includes('was removed'),
				`expected SAP removed banner — actual: ${after.slice(0, 200)}`);
			assert.ok(after.includes('DEV-100'),
				'banner should name the removed SAP system key');
			assert.ok(after.includes('SAP system'),
				'banner should label this as a SAP system removal');
			assert.ok(after.includes('disabled'),
				'action buttons in the removed banner must be disabled');
		});

		it('showRemoved() is idempotent', async () => {
			const panel = await SapDetailPanel.createOrShow(
				ctx.extensionUri as any, testSystem, credStore, client as any);
			panel.showRemoved();
			const first = mockWebviewPanels[0].webview.html;
			panel.showRemoved();
			const second = mockWebviewPanels[0].webview.html;
			assert.equal(first, second);
		});

		it('update() after showRemoved() is a no-op', async () => {
			const panel = await SapDetailPanel.createOrShow(
				ctx.extensionUri as any, testSystem, credStore, client as any);
			panel.showRemoved();
			const removedHtml = mockWebviewPanels[0].webview.html;
			await panel.update(testSystem);
			assert.equal(mockWebviewPanels[0].webview.html, removedHtml);
		});

		it('auto-disposes panel after REMOVED_AUTO_CLOSE_MS', async () => {
			const originalSetTimeout = globalThis.setTimeout;
			const scheduled: Array<{ ms: number; cb: () => void }> = [];
			(globalThis as any).setTimeout = (cb: () => void, ms: number): NodeJS.Timeout => {
				scheduled.push({ ms, cb });
				return { ref: () => undefined, unref: () => undefined } as any;
			};
			try {
				const panel = await SapDetailPanel.createOrShow(
					ctx.extensionUri as any, testSystem, credStore, client as any);
				panel.showRemoved();
				assert.equal(scheduled.length, 1);
				assert.equal(scheduled[0].ms, REMOVED_AUTO_CLOSE_MS);

				scheduled[0].cb();
				assert.equal(mockWebviewPanels[0].disposed, true);
			} finally {
				globalThis.setTimeout = originalSetTimeout;
			}
		});

		it('reconciles only missing panels — present panel stays, missing panel goes to banner', async () => {
			const other: SapSystem = {
				key: 'QAS-200',
				sid: 'QAS',
				client: '200',
				vsp: { name: 'vsp-QAS-200', status: 'running', transport: 'stdio', restart_count: 0 },
				status: 'running',
			};
			await SapDetailPanel.createOrShow(ctx.extensionUri as any, testSystem, credStore, client as any);
			await SapDetailPanel.createOrShow(ctx.extensionUri as any, other, credStore, client as any);
			assert.equal(mockWebviewPanels.length, 2);

			await SapDetailPanel.updateAll([testSystem]);

			const otherPanel = mockWebviewPanels.find((p) => p.title === 'SAP QAS-200');
			const testPanel = mockWebviewPanels.find((p) => p.title === 'SAP DEV-100');
			assert.ok(otherPanel);
			assert.ok(testPanel);
			assert.ok(otherPanel.webview.html.includes('was removed'));
			assert.ok(!testPanel.webview.html.includes('was removed'));
		});
	});
});
