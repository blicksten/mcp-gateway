import './mock-vscode';
import * as assert from 'node:assert';
import { describe, it, beforeEach } from 'mocha';
import {
	GatewayTreeProvider,
	GatewayRootItem,
	formatUptime,
} from '../gateway-tree-provider';
import { ServerDataCache, type CacheRefreshPayload } from '../server-data-cache';
import type { HealthResponse } from '../types';

// Stub cache — minimal surface used by GatewayTreeProvider. Fires onDidRefresh
// imperatively via fireRefresh() so tests can step through the state machine
// without awaiting the real 5-second poll.
function makeCache(initialHealth: HealthResponse | null = null, initialFailed = false) {
	const cache = new ServerDataCache({
		listServers: async () => [],
		getHealth: async () => initialHealth as unknown,
		shutdown: async () => ({ status: 'shutting_down' }),
		getServer: async () => ({}),
		addServer: async () => ({}),
		removeServer: async () => ({}),
		patchServer: async () => ({}),
		restartServer: async () => ({}),
		resetCircuit: async () => ({}),
		callTool: async () => ({ content: null }),
		listTools: async () => [],
	} as any);
	// Seed cached state directly via private members so tests don't race
	// with the mock HTTP client's async resolution.
	(cache as any).cachedGatewayHealth = initialHealth;
	(cache as any)._lastRefreshFailed = initialFailed;
	return cache;
}

function fireRefresh(cache: ServerDataCache, payload: CacheRefreshPayload): void {
	(cache as any).cachedGatewayHealth = payload.gatewayHealth;
	(cache as any)._lastRefreshFailed = payload.lastRefreshFailed;
	(cache as any)._onDidRefresh.fire(payload);
}

describe('GatewayTreeProvider', () => {
	describe('root item rendering', () => {
		it('shows offline state when cache.gatewayHealth is null', () => {
			const cache = makeCache(null, true);
			const provider = new GatewayTreeProvider(cache);
			const children = provider.getChildren();
			assert.strictEqual(children.length, 1);
			const root = children[0] as GatewayRootItem;
			assert.strictEqual(root.contextValue, 'gateway-unreachable');
			assert.strictEqual(root.description, 'offline');
			provider.dispose();
			cache.dispose();
		});

		it('shows running state with uptime in description when /health returned', () => {
			const health: HealthResponse = {
				status: 'ok', servers: 7, running: 7,
				pid: 12345, version: 'v1.7.3',
				started_at: '2026-04-24T06:00:00Z',
				uptime_seconds: 7384, // 2h 3m 4s
			};
			const cache = makeCache(health, false);
			const provider = new GatewayTreeProvider(cache);
			const children = provider.getChildren();
			const root = children[0] as GatewayRootItem;
			assert.strictEqual(root.contextValue, 'gateway-running');
			assert.strictEqual(root.description, '2h 3m');
			provider.dispose();
			cache.dispose();
		});

		it('running root is collapsible; offline root is not', () => {
			const health: HealthResponse = { status: 'ok', servers: 0, running: 0, uptime_seconds: 10 };
			const cache1 = makeCache(health, false);
			const rootOnline = new GatewayTreeProvider(cache1).getChildren()[0] as GatewayRootItem;
			assert.strictEqual(rootOnline.collapsibleState, 1 /* Collapsed */);

			const cache2 = makeCache(null, true);
			const rootOffline = new GatewayTreeProvider(cache2).getChildren()[0] as GatewayRootItem;
			assert.strictEqual(rootOffline.collapsibleState, 0 /* None */);

			cache1.dispose();
			cache2.dispose();
		});
	});

	describe('detail items', () => {
		it('produces PID/Version/Started/Uptime rows when root is expanded', () => {
			const health: HealthResponse = {
				status: 'ok', servers: 3, running: 2,
				pid: 54321, version: 'v1.7.2',
				started_at: '2026-04-24T05:00:00Z',
				uptime_seconds: 300,
			};
			const cache = makeCache(health, false);
			const provider = new GatewayTreeProvider(cache);
			const root = provider.getChildren()[0];
			const details = provider.getChildren(root);
			assert.strictEqual(details.length, 4);
			assert.deepStrictEqual(
				details.map((d) => [d.label, d.description]),
				[
					['PID', '54321'],
					['Version', 'v1.7.2'],
					['Started', '2026-04-24T05:00:00Z'],
					['Uptime', '5m 0s'],
				],
			);
			provider.dispose();
			cache.dispose();
		});

		it('renders "unknown" for missing fields against pre-D.1 daemons', () => {
			// Older daemon: only original fields present.
			const health: HealthResponse = { status: 'ok', servers: 0, running: 0 };
			const cache = makeCache(health, false);
			const provider = new GatewayTreeProvider(cache);
			const root = provider.getChildren()[0];
			const details = provider.getChildren(root);
			const vals = details.map((d) => d.description);
			assert.deepStrictEqual(vals, ['unknown', 'unknown', 'unknown', 'unknown']);
			provider.dispose();
			cache.dispose();
		});

		it('returns empty children for non-root elements', () => {
			const cache = makeCache(null, true);
			const provider = new GatewayTreeProvider(cache);
			const dummy = { label: 'dummy' } as any;
			assert.deepStrictEqual(provider.getChildren(dummy), []);
			provider.dispose();
			cache.dispose();
		});
	});

	describe('refresh + fingerprint', () => {
		let cache: ServerDataCache;
		let provider: GatewayTreeProvider;

		beforeEach(() => {
			cache = makeCache(null, true);
			provider = new GatewayTreeProvider(cache);
		});

		it('fingerprint is "offline" for null health', () => {
			(provider as any).refresh(); // force first computation
			assert.strictEqual(provider.getFingerprint(), 'offline');
		});

		it('fingerprint encodes online metadata', () => {
			const health: HealthResponse = {
				status: 'ok', servers: 1, running: 1,
				pid: 7, version: 'v1.0', started_at: 't0', uptime_seconds: 42,
			};
			fireRefresh(cache, { servers: [], lastRefreshFailed: false, gatewayHealth: health });
			assert.strictEqual(provider.getFingerprint(), 'online|7|v1.0|t0|8'); // 42/5 = 8 (bucket)
		});

		it('identical fingerprint does not re-fire change event', () => {
			const health: HealthResponse = {
				status: 'ok', servers: 1, running: 1,
				pid: 7, version: 'v1.0', started_at: 't0', uptime_seconds: 42,
			};
			let fireCount = 0;
			provider.onDidChangeTreeData(() => { fireCount++; });
			fireRefresh(cache, { servers: [], lastRefreshFailed: false, gatewayHealth: health });
			fireRefresh(cache, { servers: [], lastRefreshFailed: false, gatewayHealth: { ...health } });
			// Same fingerprint → only one fire.
			assert.strictEqual(fireCount, 1);
		});

		it('uptime change within same 5s bucket does not re-fire', () => {
			const base: HealthResponse = { status: 'ok', servers: 1, running: 1, pid: 7, uptime_seconds: 42 };
			let fireCount = 0;
			provider.onDidChangeTreeData(() => { fireCount++; });
			fireRefresh(cache, { servers: [], lastRefreshFailed: false, gatewayHealth: base });
			// 42 → 44 still in bucket 8 (Math.floor(44/5)=8), no re-fire.
			fireRefresh(cache, {
				servers: [],
				lastRefreshFailed: false,
				gatewayHealth: { ...base, uptime_seconds: 44 },
			});
			assert.strictEqual(fireCount, 1);
			// 42 → 47 moves to bucket 9, re-fire.
			fireRefresh(cache, {
				servers: [],
				lastRefreshFailed: false,
				gatewayHealth: { ...base, uptime_seconds: 47 },
			});
			assert.strictEqual(fireCount, 2);
		});

		it('dispose unsubscribes from cache and prevents further refresh', () => {
			const health: HealthResponse = { status: 'ok', servers: 0, running: 0, uptime_seconds: 10 };
			provider.dispose();
			let fireCount = 0;
			provider.onDidChangeTreeData(() => { fireCount++; });
			fireRefresh(cache, { servers: [], lastRefreshFailed: false, gatewayHealth: health });
			assert.strictEqual(fireCount, 0);
		});
	});
});

describe('formatUptime', () => {
	it('returns "unknown" for undefined or negative', () => {
		assert.strictEqual(formatUptime(undefined), 'unknown');
		assert.strictEqual(formatUptime(-1), 'unknown');
	});
	it('formats under 60s as "Ns"', () => {
		assert.strictEqual(formatUptime(0), '0s');
		assert.strictEqual(formatUptime(42), '42s');
	});
	it('formats under 1h as "Nm Ss"', () => {
		assert.strictEqual(formatUptime(60), '1m 0s');
		assert.strictEqual(formatUptime(3599), '59m 59s');
	});
	it('formats under 24h as "Nh Mm"', () => {
		assert.strictEqual(formatUptime(3600), '1h 0m');
		assert.strictEqual(formatUptime(7384), '2h 3m');
		assert.strictEqual(formatUptime(86_399), '23h 59m');
	});
	it('formats >=24h as "Nd Hh"', () => {
		assert.strictEqual(formatUptime(86_400), '1d 0h');
		assert.strictEqual(formatUptime(172_810), '2d 0h');
	});
	it('rounds fractional seconds down', () => {
		assert.strictEqual(formatUptime(42.9), '42s');
	});
});
