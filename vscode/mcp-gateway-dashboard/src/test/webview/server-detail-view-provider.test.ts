import '../mock-vscode';
import { strict as assert } from 'node:assert';
import {
	resetMockState,
	getRegisteredCommands,
	createMockWebviewView,
	MockSecretStorage,
	MockMemento,
	type MockWebviewView,
} from '../mock-vscode';
import { ServerDetailViewProvider } from '../../webview/server-detail-view-provider';
import { ServerDataCache } from '../../server-data-cache';
import { CredentialStore } from '../../credential-store';
import type { ServerView } from '../../types';
import type { SapSystem } from '../../sap-detector';

function createMockClient(servers: ServerView[] = []) {
	return {
		listServers: async () => servers,
		getHealth: async () => ({}),
		getServer: async () => ({}),
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

const mcpServer: ServerView = {
	name: 'alpha',
	status: 'running',
	transport: 'stdio',
	restart_count: 0,
};

const sapSystem: SapSystem = {
	sid: 'DEV',
	client: '100',
	key: 'DEV-100',
	status: 'running',
	vsp: {
		name: 'vsp-DEV-100',
		status: 'running',
		transport: 'stdio',
		restart_count: 0,
	},
};

describe('ServerDetailViewProvider', () => {
	let ctx: ReturnType<typeof createMockContext>;
	let cache: ServerDataCache;
	let credStore: CredentialStore;
	let provider: ServerDetailViewProvider;
	let view: MockWebviewView;

	beforeEach(async () => {
		resetMockState();
		ctx = createMockContext();
		credStore = new CredentialStore(ctx as any);
		const client = createMockClient([mcpServer, sapSystem.vsp!]);
		cache = new ServerDataCache(client as any);
		await cache.refresh();
		provider = new ServerDetailViewProvider(ctx.extensionUri as any, cache, credStore);
		view = createMockWebviewView();
		provider.resolveWebviewView(view as any);
		// resolveWebviewView triggers an async render() — wait a tick.
		await new Promise((r) => setImmediate(r));
	});

	afterEach(() => {
		provider?.dispose();
		cache?.dispose();
	});

	describe('initial render', () => {
		it('shows placeholder HTML when nothing is selected', () => {
			assert.ok(view.webview.html.includes('Select a server to view details'));
		});

		it('sets webview options with enableScripts=true and localResourceRoots', () => {
			const opts = (view.webview as unknown as { options: { enableScripts: boolean; localResourceRoots: unknown[] } }).options;
			assert.equal(opts.enableScripts, true);
			assert.ok(Array.isArray(opts.localResourceRoots));
		});
	});

	describe('MCP selection', () => {
		it('renders MCP detail HTML when an MCP server is selected', async () => {
			provider.setMcpSelection(mcpServer);
			await new Promise((r) => setImmediate(r));
			const html = view.webview.html;
			assert.ok(html.includes('alpha'));
			assert.ok(html.includes('Content-Security-Policy'));
			assert.ok(!html.includes("'unsafe-inline'"));
		});

		it('returns to placeholder when selection is cleared', async () => {
			provider.setMcpSelection(mcpServer);
			await new Promise((r) => setImmediate(r));
			provider.setMcpSelection(null);
			await new Promise((r) => setImmediate(r));
			assert.ok(view.webview.html.includes('Select a server to view details'));
		});

		it('falls back to placeholder when the selected server vanishes from cache', async () => {
			provider.setMcpSelection(mcpServer);
			await new Promise((r) => setImmediate(r));
			// Server removed from daemon.
			const client2 = createMockClient([]);
			cache.dispose();
			cache = new ServerDataCache(client2 as any);
			await cache.refresh();
			// Re-create provider with new cache and preserve selection.
			provider.dispose();
			provider = new ServerDetailViewProvider(ctx.extensionUri as any, cache, credStore);
			provider.resolveWebviewView(view as any);
			provider.setMcpSelection(mcpServer);
			await new Promise((r) => setImmediate(r));
			assert.ok(view.webview.html.includes('Select a server to view details'));
		});
	});

	describe('SAP selection', () => {
		it('renders SAP detail HTML when a SAP system is selected', async () => {
			provider.setSapSelection(sapSystem);
			await new Promise((r) => setImmediate(r));
			const html = view.webview.html;
			assert.ok(html.includes('DEV'));
			assert.ok(html.includes('vsp-DEV-100'));
		});
	});

	describe('cache refresh', () => {
		it('re-renders the currently-selected server on cache refresh', async () => {
			provider.setMcpSelection(mcpServer);
			await new Promise((r) => setImmediate(r));
			const htmlBefore = view.webview.html;
			// Fire a refresh.
			await cache.refresh();
			await new Promise((r) => setImmediate(r));
			// HTML should contain the server name (content matches, nonce differs).
			assert.ok(view.webview.html.includes('alpha'));
			// Nonces are regenerated per render, so hashes should differ.
			const nonceBefore = htmlBefore.match(/nonce="([^"]+)"/)?.[1];
			const nonceAfter = view.webview.html.match(/nonce="([^"]+)"/)?.[1];
			assert.ok(nonceBefore && nonceAfter && nonceBefore !== nonceAfter,
				'nonce should be regenerated on refresh');
		});
	});

	describe('message handling', () => {
		it('routes MCP action messages to mcpGateway._webviewAction', async () => {
			provider.setMcpSelection(mcpServer);
			await new Promise((r) => setImmediate(r));

			const calls: unknown[] = [];
			getRegisteredCommands().set('mcpGateway._webviewAction', (msg: unknown) => {
				calls.push(msg);
			});

			(view.webview as any)._simulateMessage({ type: 'action', action: 'restart' });
			await new Promise((r) => setImmediate(r));
			assert.equal(calls.length, 1);
			const first = calls[0] as { action: string; serverName: string };
			assert.equal(first.action, 'restart');
			assert.equal(first.serverName, 'alpha');
		});

		it('rejects messages with disallowed actions', async () => {
			provider.setMcpSelection(mcpServer);
			await new Promise((r) => setImmediate(r));

			const calls: unknown[] = [];
			getRegisteredCommands().set('mcpGateway._webviewAction', (msg: unknown) => {
				calls.push(msg);
			});

			(view.webview as any)._simulateMessage({ type: 'action', action: 'rm -rf' });
			(view.webview as any)._simulateMessage({ type: 'action', action: 'eval' });
			await new Promise((r) => setImmediate(r));
			assert.equal(calls.length, 0);
		});

		it('drops messages when nothing is selected', async () => {
			provider.clearSelection();
			await new Promise((r) => setImmediate(r));

			const calls: unknown[] = [];
			getRegisteredCommands().set('mcpGateway._webviewAction', (msg: unknown) => {
				calls.push(msg);
			});

			(view.webview as any)._simulateMessage({ type: 'action', action: 'restart' });
			await new Promise((r) => setImmediate(r));
			assert.equal(calls.length, 0);
		});

		it('rejects messages with malformed serverName', async () => {
			provider.setMcpSelection(mcpServer);
			await new Promise((r) => setImmediate(r));

			const calls: unknown[] = [];
			getRegisteredCommands().set('mcpGateway._webviewAction', (msg: unknown) => {
				calls.push(msg);
			});

			(view.webview as any)._simulateMessage({
				type: 'action',
				action: 'restart',
				serverName: '../../../etc/passwd',
			});
			await new Promise((r) => setImmediate(r));
			assert.equal(calls.length, 0);
		});
	});

	describe('security', () => {
		it('CSP forbids unsafe-inline scripts and styles', async () => {
			provider.setMcpSelection(mcpServer);
			await new Promise((r) => setImmediate(r));
			const html = view.webview.html;
			assert.ok(!html.includes("'unsafe-inline'"));
			assert.ok(html.includes("default-src 'none'"));
		});

		it('placeholder HTML also has CSP', () => {
			assert.ok(view.webview.html.includes('Content-Security-Policy'));
			assert.ok(!view.webview.html.includes("'unsafe-inline'"));
		});
	});

	describe('dispose', () => {
		it('releases subscriptions and clears view reference', () => {
			provider.dispose();
			// Post-dispose render should be a no-op — no throw.
			provider.setMcpSelection(mcpServer);
		});
	});

	describe('concurrent render generation guard', () => {
		it('a newer render wins: selecting beta while alpha is awaiting credentials yields beta HTML', async () => {
			// Replace the credentialStore with one whose getServerCredentials
			// can be deferred so we can simulate interleaved renders.
			let releaseAlpha: (() => void) | null = null;
			const deferredAlpha = new Promise<{ env: Record<string, string>; headers: Record<string, string> }>((resolve) => {
				releaseAlpha = () => resolve({ env: {}, headers: {} });
			});

			const betaServer: ServerView = {
				name: 'beta',
				status: 'running',
				transport: 'http',
				restart_count: 0,
			};

			const slowCredStore = {
				getServerCredentials: async (name: string) => {
					if (name === 'alpha') { return deferredAlpha; }
					return { env: {}, headers: {} };
				},
			} as unknown as CredentialStore;

			const client = createMockClient([mcpServer, betaServer]);
			cache.dispose();
			cache = new ServerDataCache(client as any);
			await cache.refresh();

			provider.dispose();
			provider = new ServerDetailViewProvider(ctx.extensionUri as any, cache, slowCredStore);
			provider.resolveWebviewView(view as any);

			// Start rendering alpha — it blocks on the deferred promise.
			provider.setMcpSelection(mcpServer);
			// Immediately switch to beta.
			provider.setMcpSelection(betaServer);
			// Beta render completes synchronously (no await).
			await new Promise((r) => setImmediate(r));
			// HTML should already reflect beta.
			const htmlAfterBeta = view.webview.html;
			assert.ok(htmlAfterBeta.includes('beta'), `expected beta HTML, got: ${htmlAfterBeta.slice(0, 120)}`);

			// Now release alpha's credential fetch.
			releaseAlpha!();
			await new Promise((r) => setImmediate(r));
			await new Promise((r) => setImmediate(r));

			// Stale alpha render must NOT clobber beta HTML.
			assert.ok(view.webview.html.includes('beta'),
				`beta HTML was clobbered by stale alpha render: ${view.webview.html.slice(0, 200)}`);
		});
	});
});
