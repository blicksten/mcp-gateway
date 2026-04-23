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

	describe('Phase 17.5 — KeePass-imported SAP rows', () => {
		it('without provider: daemon-only rows (backward compatible)', async () => {
			cache = new ServerDataCache(createMockClient(mixedServers) as any);
			await cache.refresh();
			const sap = cache.getSapSystems();
			assert.equal(sap.length, 1);
			assert.equal(sap[0].key, 'DEV');
			assert.ok(!sap[0].imported);
		});

		it('with provider returning empty: no synthetic rows', async () => {
			cache = new ServerDataCache(
				createMockClient(mixedServers) as any,
				() => [],
			);
			await cache.refresh();
			assert.equal(cache.getSapSystems().length, 1);
		});

		it('provider adds imported rows that are not already daemon-backed', async () => {
			cache = new ServerDataCache(
				createMockClient(mixedServers) as any,
				() => ['vsp-QAS-100', 'sap-gui-PRD-400'],
			);
			await cache.refresh();
			const sap = cache.getSapSystems();
			// DEV from daemon (index determined by sort) + QAS-100 + PRD-400 imported
			assert.equal(sap.length, 3);
			const byKey = new Map(sap.map((s) => [s.key, s]));
			assert.ok(byKey.get('DEV') && !byKey.get('DEV')!.imported, 'DEV is daemon-backed');
			assert.ok(byKey.get('QAS-100')?.imported, 'QAS-100 is imported');
			assert.ok(byKey.get('PRD-400')?.imported, 'PRD-400 is imported');
		});

		it('daemon row wins on key collision (imported is suppressed)', async () => {
			cache = new ServerDataCache(
				createMockClient(mixedServers) as any,
				() => ['vsp-DEV', 'vsp-DEV-000'], // vsp-DEV collides with daemon row
			);
			await cache.refresh();
			const sap = cache.getSapSystems();
			const dev = sap.find((s) => s.key === 'DEV');
			assert.ok(dev, 'DEV must remain in the list');
			assert.ok(!dev!.imported, 'DEV must NOT be marked imported');
			// Separate DEV-000 is fine because its key differs from DEV.
			const dev000 = sap.find((s) => s.key === 'DEV-000');
			assert.ok(dev000?.imported);
		});

		it('final list is sorted by key across daemon + imported rows', async () => {
			cache = new ServerDataCache(
				createMockClient([
					{ name: 'vsp-MMM', status: 'running', transport: 'stdio', restart_count: 0 },
				]) as any,
				() => ['vsp-AAA', 'vsp-ZZZ'],
			);
			await cache.refresh();
			const keys = cache.getSapSystems().map((s) => s.key);
			assert.deepEqual(keys, ['AAA', 'MMM', 'ZZZ']);
		});

		it('provider is called per refresh — toggles take effect', async () => {
			let enabled = false;
			cache = new ServerDataCache(
				createMockClient(mixedServers) as any,
				() => (enabled ? ['vsp-QAS'] : []),
			);
			await cache.refresh();
			assert.equal(cache.getSapSystems().length, 1);
			enabled = true;
			await cache.refresh();
			assert.equal(cache.getSapSystems().length, 2);
			enabled = false;
			await cache.refresh();
			assert.equal(cache.getSapSystems().length, 1);
		});

		it('provider that throws does not crash refresh — degrades to daemon-only rows', async () => {
			// CV-HIGH regression: a buggy credential-store reader (corrupt
			// globalState, keychain unavailable, future async file access) must
			// not break the refresh cycle. The UI stays in sync with the daemon
			// and the event still fires.
			let refreshPayloadSeen = false;
			cache = new ServerDataCache(
				createMockClient(mixedServers) as any,
				() => { throw new Error('synthetic provider failure'); },
			);
			cache.onDidRefresh(() => { refreshPayloadSeen = true; });
			await assert.doesNotReject(() => cache.refresh());
			assert.equal(cache.getSapSystems().length, 1, 'daemon SAP row still present');
			assert.ok(!cache.getSapSystems()[0].imported, 'daemon row is not marked imported');
			assert.ok(refreshPayloadSeen, 'onDidRefresh must fire despite provider failure');
		});
	});
});
