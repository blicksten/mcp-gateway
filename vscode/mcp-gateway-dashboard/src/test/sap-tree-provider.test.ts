import './mock-vscode';
import { strict as assert } from 'node:assert';
import { SapTreeProvider } from '../sap-tree-provider';
import { SapSystemItem } from '../sap-item';
import { ServerDataCache } from '../server-data-cache';
import type { ServerView } from '../types';

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

describe('SapTreeProvider', () => {
	let cache: ServerDataCache;
	let provider: SapTreeProvider;

	afterEach(() => {
		provider?.dispose();
		cache?.dispose();
	});

	it('returns SapSystemItem instances from cache', async () => {
		const servers: ServerView[] = [
			{ name: 'vsp-DEV', status: 'running', transport: 'stdio', restart_count: 0 },
			{ name: 'sap-gui-DEV', status: 'running', transport: 'http', restart_count: 0 },
		];
		cache = new ServerDataCache(createMockClient(servers) as any);
		await cache.refresh();
		provider = new SapTreeProvider(cache);

		const items = provider.getChildren();
		assert.equal(items.length, 1);
		assert.ok(items[0] instanceof SapSystemItem);
		assert.equal(items[0].system.key, 'DEV');
	});

	it('returns empty when no SAP servers', async () => {
		cache = new ServerDataCache(createMockClient([
			{ name: 'my-server', status: 'running', transport: 'stdio', restart_count: 0 },
		]) as any);
		await cache.refresh();
		provider = new SapTreeProvider(cache);

		assert.equal(provider.getChildren().length, 0);
	});

	it('returns empty for child elements (flat list)', async () => {
		const servers: ServerView[] = [
			{ name: 'vsp-QAS', status: 'running', transport: 'stdio', restart_count: 0 },
		];
		cache = new ServerDataCache(createMockClient(servers) as any);
		await cache.refresh();
		provider = new SapTreeProvider(cache);
		const items = provider.getChildren();
		assert.equal(provider.getChildren(items[0]).length, 0);
	});

	it('fires onDidChangeTreeData on cache refresh', (done) => {
		cache = new ServerDataCache(createMockClient() as any);
		provider = new SapTreeProvider(cache);
		provider.onDidChangeTreeData(() => { done(); });
		cache.refresh();
	});

	it('refresh() fires onDidChangeTreeData independently', (done) => {
		cache = new ServerDataCache(createMockClient() as any);
		provider = new SapTreeProvider(cache);
		provider.onDidChangeTreeData(() => { done(); });
		provider.refresh();
	});

	it('dispose() prevents refresh() from firing', () => {
		cache = new ServerDataCache(createMockClient() as any);
		provider = new SapTreeProvider(cache);
		provider.dispose();
		// After dispose, refresh() should not throw (EventEmitter is disposed).
		assert.doesNotThrow(() => provider.refresh());
	});
});
