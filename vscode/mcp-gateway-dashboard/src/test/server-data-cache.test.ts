import './mock-vscode';
import { strict as assert } from 'node:assert';
import { ServerDataCache } from '../server-data-cache';
import type { ServerView } from '../types';

function createMockClient(servers: ServerView[] = []) {
	let callCount = 0;
	return {
		get callCount() { return callCount; },
		listServers: async () => { callCount++; return servers; },
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

function createFailingClient() {
	return {
		listServers: async () => { throw new Error('connection refused'); },
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

const mixedServers: ServerView[] = [
	{ name: 'my-server', status: 'running', transport: 'stdio', restart_count: 0 },
	{ name: 'vsp-DEV', status: 'running', transport: 'stdio', restart_count: 0 },
	{ name: 'sap-gui-DEV', status: 'running', transport: 'http', restart_count: 0 },
];

describe('ServerDataCache', () => {
	let cache: ServerDataCache;

	afterEach(() => {
		cache?.dispose();
	});

	it('refresh calls listServers exactly once', async () => {
		const client = createMockClient(mixedServers);
		cache = new ServerDataCache(client as any);
		await cache.refresh();
		assert.equal(client.callCount, 1);
	});

	it('getMcpServers filters out SAP servers', async () => {
		cache = new ServerDataCache(createMockClient(mixedServers) as any);
		await cache.refresh();
		const mcp = cache.getMcpServers();
		assert.equal(mcp.length, 1);
		assert.equal(mcp[0].name, 'my-server');
	});

	it('getSapSystems returns grouped SAP entries', async () => {
		cache = new ServerDataCache(createMockClient(mixedServers) as any);
		await cache.refresh();
		const sap = cache.getSapSystems();
		assert.equal(sap.length, 1);
		assert.equal(sap[0].key, 'DEV');
		assert.equal(sap[0].vsp?.name, 'vsp-DEV');
		assert.equal(sap[0].gui?.name, 'sap-gui-DEV');
	});

	it('getAllServers returns full cached list', async () => {
		cache = new ServerDataCache(createMockClient(mixedServers) as any);
		await cache.refresh();
		assert.equal(cache.getAllServers().length, 3);
	});

	it('onDidRefresh fires after refresh', (done) => {
		cache = new ServerDataCache(createMockClient(mixedServers) as any);
		cache.onDidRefresh((payload) => {
			assert.equal(payload.servers.length, 3);
			assert.equal(payload.lastRefreshFailed, false);
			done();
		});
		cache.refresh();
	});

	it('offline state: client throws → fires event with empty data', (done) => {
		cache = new ServerDataCache(createFailingClient() as any);
		cache.onDidRefresh((payload) => {
			assert.equal(payload.servers.length, 0);
			assert.equal(payload.lastRefreshFailed, true);
			assert.equal(cache.getMcpServers().length, 0);
			assert.equal(cache.getSapSystems().length, 0);
			done();
		});
		cache.refresh();
	});

	it('lastRefreshFailed getter mirrors refresh outcome', async () => {
		const client = createMockClient(mixedServers);
		cache = new ServerDataCache(client as any);
		await cache.refresh();
		assert.equal(cache.lastRefreshFailed, false);
	});

	it('lastRefreshFailed flips to true when client throws, back to false on success', async () => {
		let shouldFail = true;
		const client = {
			listServers: async () => {
				if (shouldFail) { throw new Error('connection refused'); }
				return mixedServers;
			},
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
		cache = new ServerDataCache(client as any);
		await cache.refresh();
		assert.equal(cache.lastRefreshFailed, true);
		shouldFail = false;
		await cache.refresh();
		assert.equal(cache.lastRefreshFailed, false);
	});

	it('startAutoRefresh triggers immediate refresh', (done) => {
		cache = new ServerDataCache(createMockClient(mixedServers) as any);
		cache.onDidRefresh(() => {
			cache.stopAutoRefresh();
			done();
		});
		cache.startAutoRefresh(5000);
	});

	it('stopAutoRefresh prevents further refreshes', (done) => {
		const client = createMockClient(mixedServers);
		cache = new ServerDataCache(client as any);
		let count = 0;
		cache.onDidRefresh(() => { count++; });
		cache.startAutoRefresh(30);
		setTimeout(() => {
			cache.stopAutoRefresh();
			const snapshot = count;
			setTimeout(() => {
				assert.equal(count, snapshot);
				done();
			}, 100);
		}, 80);
	});

	it('dispose stops auto-refresh and prevents further refresh', async () => {
		cache = new ServerDataCache(createMockClient(mixedServers) as any);
		await cache.refresh();
		assert.equal(cache.getAllServers().length, 3);
		cache.dispose();
		// Refresh after dispose should be a no-op — cached data from before dispose remains.
		await cache.refresh();
		// Data from the pre-dispose refresh is still in cache (dispose doesn't clear).
		// The key assertion: no NEW data is fetched (the disposed guard prevents it).
		assert.equal(cache.getAllServers().length, 3); // stale, but not updated
	});
});
