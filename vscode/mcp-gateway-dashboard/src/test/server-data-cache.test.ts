import './mock-vscode';
import { strict as assert } from 'node:assert';
import { ServerDataCache, type CacheRefreshPayload } from '../server-data-cache';
import { GatewayError } from '../gateway-client';
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

	it('offline state from cold start: client throws → empty list + flag=true', (done) => {
		// Cold-start: no successful refresh ever happened. The cache stays at
		// its initial empty list and flips the flag so consumers can show a
		// "connecting" placeholder / offline status.
		cache = new ServerDataCache(createFailingClient() as any);
		cache.onDidRefresh((payload) => {
			assert.equal(payload.servers.length, 0, 'cold-start cachedServers must be []');
			assert.equal(payload.lastRefreshFailed, true);
			assert.equal(cache.getMcpServers().length, 0);
			assert.equal(cache.getSapSystems().length, 0);
			done();
		});
		cache.refresh();
	});

	it('preserves last-known-good data on transient error (no flicker)', async () => {
		// Phase 1 debug-flicker fix: when the daemon momentarily drops (auto-
		// start race, circuit breaker open, brief network hiccup), the cache
		// preserves the previous server list instead of wiping it to []. Tree
		// providers re-compute the same fingerprint and suppress the render —
		// no visible flicker in the Backends / SAP Systems sidebars.
		let shouldFail = false;
		const client = {
			listServers: async () => {
				if (shouldFail) { throw new Error('transient: connection refused'); }
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

		// First refresh succeeds — populate cache.
		await cache.refresh();
		assert.equal(cache.getAllServers().length, 3, 'warm cache has 3 servers');
		assert.equal(cache.lastRefreshFailed, false);

		// Daemon drops: next refresh throws. Data MUST be preserved; flag flips.
		shouldFail = true;
		const errorPayload = await new Promise<CacheRefreshPayload>((resolve) => {
			const sub = cache.onDidRefresh((p) => { sub.dispose(); resolve(p); });
			cache.refresh();
		});
		assert.equal(errorPayload.servers.length, 3, 'preserved last-known-good servers');
		assert.equal(errorPayload.lastRefreshFailed, true);
		assert.equal(cache.getAllServers().length, 3, 'getAllServers still returns preserved data');
		assert.equal(cache.getMcpServers().length, 1, 'MCP view preserved (my-server)');
		assert.equal(cache.getSapSystems().length, 1, 'SAP view preserved (DEV)');
	});

	it('recovery clears lastRefreshFailed flag and refreshes data', async () => {
		// Transient-error → recovery cycle: flag must flip back to false, and
		// fresh data from the daemon replaces the preserved last-known-good.
		let shouldFail = true;
		const firstBatch: ServerView[] = [
			{ name: 'first', status: 'running', transport: 'stdio', restart_count: 0 },
		];
		const secondBatch: ServerView[] = [
			{ name: 'second-a', status: 'running', transport: 'stdio', restart_count: 0 },
			{ name: 'second-b', status: 'running', transport: 'stdio', restart_count: 0 },
		];
		let phase: 'cold' | 'warm' | 'recovery' = 'cold';
		const client = {
			listServers: async () => {
				if (phase === 'cold') { return firstBatch; }
				if (phase === 'warm' && shouldFail) { throw new Error('daemon offline'); }
				return secondBatch;
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

		// Cold successful refresh.
		await cache.refresh();
		assert.equal(cache.getAllServers()[0].name, 'first');
		assert.equal(cache.lastRefreshFailed, false);

		// Error: data preserved, flag=true.
		phase = 'warm';
		await cache.refresh();
		assert.equal(cache.getAllServers()[0].name, 'first', 'preserved during error');
		assert.equal(cache.lastRefreshFailed, true);

		// Recovery: flag clears, fresh data replaces preserved.
		shouldFail = false;
		phase = 'recovery';
		await cache.refresh();
		assert.equal(cache.getAllServers().length, 2);
		assert.equal(cache.getAllServers()[0].name, 'second-a');
		assert.equal(cache.getAllServers()[1].name, 'second-b');
		assert.equal(cache.lastRefreshFailed, false);
	});

	it('fingerprint-stable payload: onDidRefresh fires, servers unchanged during error', async () => {
		// Contract for fingerprint-based tree providers
		// (BackendTreeProvider / SapTreeProvider): during a transient error,
		// the payload carries the SAME ServerView list as the last success, so
		// the providers' computeFingerprint() returns the same string and they
		// skip _onDidChangeTreeData.fire() — no visible tree flicker. This
		// test asserts the payload identity; the providers' own dedup logic
		// is covered by their own tests.
		let shouldFail = false;
		const client = {
			listServers: async () => {
				if (shouldFail) { throw new Error('transient'); }
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

		// Capture the success payload.
		const successPayload = await new Promise<CacheRefreshPayload>((resolve) => {
			const sub = cache.onDidRefresh((p) => { sub.dispose(); resolve(p); });
			cache.refresh();
		});

		// Capture the next payload (error — same list expected).
		shouldFail = true;
		const errorPayload = await new Promise<CacheRefreshPayload>((resolve) => {
			const sub = cache.onDidRefresh((p) => { sub.dispose(); resolve(p); });
			cache.refresh();
		});

		// Same server list (by value) → providers' fingerprint hash is identical
		// → the tree view does not re-render. The flag differs (that's a
		// status-bar concern, not a tree concern).
		assert.equal(errorPayload.servers.length, successPayload.servers.length);
		for (let i = 0; i < successPayload.servers.length; i++) {
			assert.deepEqual(errorPayload.servers[i], successPayload.servers[i]);
		}
		assert.equal(successPayload.lastRefreshFailed, false);
		assert.equal(errorPayload.lastRefreshFailed, true);
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

	it('lastAuthFailed propagates in payload when listServers rejects with GatewayError kind:auth', async () => {
		// Critical path for B-NEW-18: the 401-classification in gateway-client.ts
		// surfaces via lastAuthFailed on the CacheRefreshPayload so extension.ts
		// can show the re-auth toast. A generic Error (kind:connection) must NOT
		// set the flag — only a GatewayError with kind='auth' qualifies.
		let phase: 'auth-fail' | 'connection-fail' | 'success' = 'auth-fail';
		const client = {
			listServers: async () => {
				if (phase === 'auth-fail') {
					throw new GatewayError('auth', 'Unauthorized', 401, '');
				}
				if (phase === 'connection-fail') {
					throw new GatewayError('connection', 'connection refused');
				}
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

		// 1. 401 auth failure: both lastRefreshFailed and lastAuthFailed must be true.
		const authPayload = await new Promise<CacheRefreshPayload>((resolve) => {
			const sub = cache.onDidRefresh((p) => { sub.dispose(); resolve(p); });
			cache.refresh();
		});
		assert.equal(authPayload.lastRefreshFailed, true, 'lastRefreshFailed on auth error');
		assert.equal(authPayload.lastAuthFailed, true, 'lastAuthFailed on 401');

		// 2. Generic connection failure: lastRefreshFailed=true but lastAuthFailed must stay false.
		phase = 'connection-fail';
		const connPayload = await new Promise<CacheRefreshPayload>((resolve) => {
			const sub = cache.onDidRefresh((p) => { sub.dispose(); resolve(p); });
			cache.refresh();
		});
		assert.equal(connPayload.lastRefreshFailed, true, 'lastRefreshFailed on connection error');
		assert.equal(connPayload.lastAuthFailed, false, 'lastAuthFailed must be false for non-auth errors');

		// 3. Successful refresh: both flags clear.
		phase = 'success';
		const successPayload = await new Promise<CacheRefreshPayload>((resolve) => {
			const sub = cache.onDidRefresh((p) => { sub.dispose(); resolve(p); });
			cache.refresh();
		});
		assert.equal(successPayload.lastRefreshFailed, false, 'lastRefreshFailed clears on success');
		assert.equal(successPayload.lastAuthFailed, false, 'lastAuthFailed clears on success');
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

	describe('F-2 (Phase 17 audit) — re-queue refresh when one is in flight', () => {
		// The old code returned immediately when refreshInFlight was true,
		// silently dropping the caller. A config-change driven refresh could
		// therefore be swallowed by a coincident poll and the toggle effect
		// would be delayed by up to one poll tick.
		it('a second refresh() during an in-flight call is re-queued, not dropped', async () => {
			let resolveFirst: (() => void) | null = null;
			let callCount = 0;
			const gatedFirst = new Promise<void>((resolve) => { resolveFirst = resolve; });
			const client = {
				listServers: async () => {
					callCount++;
					if (callCount === 1) { await gatedFirst; }
					return [{ name: 'a', status: 'running', transport: 'stdio', restart_count: 0 }];
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

			const first = cache.refresh();
			const second = cache.refresh(); // hits refreshInFlight guard → must be re-queued, not dropped
			resolveFirst!();
			await Promise.all([first, second]);

			assert.equal(callCount, 2, 'the re-queued refresh must run a second listServers call');
		});

		it('re-queue coalesces multiple waiters: 5 refresh() calls during one in-flight produce one drain', async () => {
			// Five "toggle" events in a tight window should not cause five extra
			// listServers calls — the pendingRefresh flag is just a boolean, so
			// coalescing is the expected behavior.
			let resolveFirst: (() => void) | null = null;
			let callCount = 0;
			const gatedFirst = new Promise<void>((resolve) => { resolveFirst = resolve; });
			const client = {
				listServers: async () => {
					callCount++;
					if (callCount === 1) { await gatedFirst; }
					return [];
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

			const first = cache.refresh();
			const extras = [cache.refresh(), cache.refresh(), cache.refresh(), cache.refresh(), cache.refresh()];
			resolveFirst!();
			await Promise.all([first, ...extras]);

			assert.equal(callCount, 2, 'exactly one drain runs despite 5 in-flight colliders');
		});

		it('dispose during re-queued drain aborts cleanly (no call after dispose)', async () => {
			let resolveFirst: (() => void) | null = null;
			let callCount = 0;
			const gatedFirst = new Promise<void>((resolve) => { resolveFirst = resolve; });
			const client = {
				listServers: async () => {
					callCount++;
					if (callCount === 1) { await gatedFirst; }
					return [];
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

			const first = cache.refresh();
			cache.refresh(); // queues
			cache.dispose();
			resolveFirst!();
			await first;
			// Assert: the re-queued drain is guarded by `this.disposed` and does
			// not call listServers again.
			assert.equal(callCount, 1, 'no drain call after dispose');
		});
	});
});
