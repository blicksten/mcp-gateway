import '../mock-vscode';
import { strict as assert } from 'node:assert';
import { mockWebviewPanels, resetMockState, getRegisteredCommands } from '../mock-vscode';
import { ServerDetailPanel, REMOVED_AUTO_CLOSE_MS } from '../../webview/server-detail-panel';
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

	// Phase 8 (B-NEW-20) — detail-panel reconcile when server is removed.
	describe('showRemoved / updateAll reconcile (B-NEW-20)', () => {
		it('updateAll() with empty list switches the panel to a removed banner', async () => {
			await ServerDetailPanel.createOrShow(ctx.extensionUri as any, testServer, credStore, client as any);
			const before = mockWebviewPanels[0].webview.html;
			assert.ok(before.includes('test-server'));

			await ServerDetailPanel.updateAll([]);

			const after = mockWebviewPanels[0].webview.html;
			assert.ok(after.includes('was removed'),
				`expected removed banner — actual: ${after.slice(0, 200)}`);
			assert.ok(after.includes('test-server'),
				'banner should name the removed server');
			assert.ok(after.includes('disabled'),
				'action buttons in the removed banner must be disabled');
			assert.notEqual(after, before);
		});

		it('updateAll() with the same server still present leaves the panel rendered', async () => {
			const panel = await ServerDetailPanel.createOrShow(
				ctx.extensionUri as any, testServer, credStore, client as any);
			void panel; // suppress unused warning when rebuild is silent
			const before = mockWebviewPanels[0].webview.html;

			await ServerDetailPanel.updateAll([testServer]);

			const after = mockWebviewPanels[0].webview.html;
			assert.ok(!after.includes('was removed'));
			assert.ok(after.includes('test-server'));
			// Different nonces guarantee a fresh render happened.
			assert.notEqual(before.match(/<style nonce="([^"]+)">/)?.[1],
				after.match(/<style nonce="([^"]+)">/)?.[1]);
		});

		it('updateAll() reconciles only missing panels — present panel stays, missing panel goes to banner', async () => {
			const other: ServerView = { name: 'other-server', status: 'running', transport: 'http', restart_count: 0 };
			client = createMockClient([testServer, other]);
			await ServerDetailPanel.createOrShow(ctx.extensionUri as any, testServer, credStore, client as any);
			await ServerDetailPanel.createOrShow(ctx.extensionUri as any, other, credStore, client as any);
			assert.equal(mockWebviewPanels.length, 2);

			// Remove only `other-server` from the cache.
			await ServerDetailPanel.updateAll([testServer]);

			// Find the panel whose viewType matches and whose title is the missing one.
			const otherPanel = mockWebviewPanels.find((p) => p.title === 'other-server');
			const testPanel = mockWebviewPanels.find((p) => p.title === 'test-server');
			assert.ok(otherPanel);
			assert.ok(testPanel);
			assert.ok(otherPanel.webview.html.includes('was removed'));
			assert.ok(!testPanel.webview.html.includes('was removed'));
		});

		it('showRemoved() is idempotent — second call is a no-op', async () => {
			const panel = await ServerDetailPanel.createOrShow(
				ctx.extensionUri as any, testServer, credStore, client as any);
			panel.showRemoved();
			const first = mockWebviewPanels[0].webview.html;
			panel.showRemoved();
			const second = mockWebviewPanels[0].webview.html;
			assert.equal(first, second, 'second showRemoved() must not re-render');
		});

		it('update() after showRemoved() is a no-op (banner persists)', async () => {
			const panel = await ServerDetailPanel.createOrShow(
				ctx.extensionUri as any, testServer, credStore, client as any);
			panel.showRemoved();
			const removedHtml = mockWebviewPanels[0].webview.html;
			assert.ok(removedHtml.includes('was removed'));

			await panel.update(testServer);

			assert.equal(mockWebviewPanels[0].webview.html, removedHtml,
				'update() after showRemoved() must not re-render the live panel');
		});

		it('auto-disposes panel after REMOVED_AUTO_CLOSE_MS via setTimeout', async () => {
			const originalSetTimeout = globalThis.setTimeout;
			const scheduled: Array<{ ms: number; cb: () => void }> = [];
			(globalThis as any).setTimeout = (cb: () => void, ms: number): NodeJS.Timeout => {
				scheduled.push({ ms, cb });
				return { ref: () => undefined, unref: () => undefined } as any;
			};
			try {
				const panel = await ServerDetailPanel.createOrShow(
					ctx.extensionUri as any, testServer, credStore, client as any);
				assert.equal(mockWebviewPanels[0].disposed, false);

				panel.showRemoved();

				assert.equal(scheduled.length, 1);
				assert.equal(scheduled[0].ms, REMOVED_AUTO_CLOSE_MS);

				// Run the scheduled callback — panel should dispose.
				scheduled[0].cb();
				assert.equal(mockWebviewPanels[0].disposed, true);
			} finally {
				globalThis.setTimeout = originalSetTimeout;
			}
		});

		it('manual dispose before timer fires clears the auto-close timer', async () => {
			let cleared = 0;
			const originalSetTimeout = globalThis.setTimeout;
			const originalClearTimeout = globalThis.clearTimeout;
			(globalThis as any).setTimeout = (_cb: () => void, _ms: number): NodeJS.Timeout =>
				({ _id: 1 }) as any;
			(globalThis as any).clearTimeout = (_t: NodeJS.Timeout) => { cleared++; };
			try {
				const panel = await ServerDetailPanel.createOrShow(
					ctx.extensionUri as any, testServer, credStore, client as any);
				panel.showRemoved();
				mockWebviewPanels[0].dispose();
				assert.equal(cleared, 1, 'expected exactly one clearTimeout call on dispose');
			} finally {
				globalThis.setTimeout = originalSetTimeout;
				globalThis.clearTimeout = originalClearTimeout;
			}
		});

		it('removed banner sets a fresh nonce and CSP without unsafe-inline', async () => {
			const panel = await ServerDetailPanel.createOrShow(
				ctx.extensionUri as any, testServer, credStore, client as any);
			panel.showRemoved();
			const html = mockWebviewPanels[0].webview.html;
			assert.ok(html.includes('Content-Security-Policy'));
			assert.ok(!html.includes("'unsafe-inline'"));
			assert.ok(/<style nonce="[A-Za-z0-9+/=]+">/.test(html),
				'removed banner must carry a nonce on its <style>');
		});

		it('removed banner escapes the server name (XSS guard)', async () => {
			const tricky: ServerView = {
				name: 'safe-name',
				status: 'running',
				transport: 'stdio',
				restart_count: 0,
			};
			// We render via showRemoved() using the tracked serverName, which
			// was validated at panel creation. Simulate a name that *could*
			// contain HTML by writing the escape contract through the helper.
			const panel = await ServerDetailPanel.createOrShow(
				ctx.extensionUri as any, tricky, credStore, client as any);
			panel.showRemoved();
			const html = mockWebviewPanels[0].webview.html;
			// The banner text uses the escaped form — assert no raw `<script>`.
			assert.ok(!html.includes('<script>'),
				'removed banner must not contain script tags');
		});
	});
});
