// Import mock BEFORE production modules (CommonJS require order).
import './mock-vscode';

import * as assert from 'node:assert';
import { describe, it, beforeEach, afterEach } from 'mocha';
import { BackendTreeProvider } from '../backend-tree-provider';
import { BackendItem } from '../backend-item';
import { ServerDataCache } from '../server-data-cache';
import type { ServerView } from '../types';

// Mock client for ServerDataCache.
function createMockClient(servers: ServerView[] = []) {
	return {
		listServers: async () => servers,
		getHealth: async () => ({ status: 'ok', servers: 0, running: 0 }),
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

const sampleServers: ServerView[] = [
	{
		name: 'ctx7',
		status: 'running',
		transport: 'http',
		pid: 1234,
		restart_count: 0,
		tools: [
			{ name: 'ctx7__query-docs', description: 'Query docs', server: 'ctx7' },
			{ name: 'ctx7__resolve-library-id', description: 'Resolve lib', server: 'ctx7' },
		],
	},
	{
		name: 'orch',
		status: 'stopped',
		transport: 'stdio',
		restart_count: 2,
		last_error: 'exit code 1',
	},
	{
		name: 'pal',
		status: 'error',
		transport: 'stdio',
		restart_count: 5,
		last_error: 'segfault',
		tools: [],
	},
	{
		name: 'disabled-srv',
		status: 'disabled',
		transport: 'http',
		restart_count: 0,
	},
	{
		name: 'booting',
		status: 'starting',
		transport: 'stdio',
		restart_count: 0,
	},
	{
		name: 'cycling',
		status: 'restarting',
		transport: 'stdio',
		restart_count: 1,
	},
	{
		name: 'flaky',
		status: 'degraded',
		transport: 'http',
		restart_count: 3,
		tools: [
			{ name: 'flaky__ping', description: 'Health ping', server: 'flaky' },
		],
	},
];

// Mixed list with SAP servers — should be filtered out by cache.getMcpServers().
const mixedServers: ServerView[] = [
	...sampleServers,
	{ name: 'vsp-DEV', status: 'running', transport: 'stdio', restart_count: 0 },
	{ name: 'sap-gui-DEV-100', status: 'running', transport: 'http', restart_count: 0 },
];

describe('BackendTreeProvider', () => {
	let cache: ServerDataCache;
	let provider: BackendTreeProvider;

	afterEach(() => {
		provider?.dispose();
		cache?.dispose();
	});

	describe('getChildren (root)', () => {
		it('returns BackendItem for each server', async () => {
			cache = new ServerDataCache(createMockClient(sampleServers) as any);
			await cache.refresh();
			provider = new BackendTreeProvider(cache);
			const items = provider.getChildren();
			assert.strictEqual(items.length, 7);
			assert.ok(items[0] instanceof BackendItem);
			assert.strictEqual((items[0] as BackendItem).server.name, 'ctx7');
		});

		it('returns empty when cache has no data', () => {
			cache = new ServerDataCache(createMockClient([]) as any);
			provider = new BackendTreeProvider(cache);
			const items = provider.getChildren();
			assert.strictEqual(items.length, 0);
		});

		it('sets None state for all servers (flat list)', async () => {
			cache = new ServerDataCache(createMockClient(sampleServers) as any);
			await cache.refresh();
			provider = new BackendTreeProvider(cache);
			const items = provider.getChildren();
			for (const item of items) {
				assert.strictEqual(item.collapsibleState, 0, `Expected None (0) for ${(item as BackendItem).server?.name}`);
			}
		});
	});

	describe('getChildren (flat list)', () => {
		it('returns empty array for any element (no children)', async () => {
			cache = new ServerDataCache(createMockClient(sampleServers) as any);
			await cache.refresh();
			provider = new BackendTreeProvider(cache);
			const roots = provider.getChildren();
			const children = provider.getChildren(roots[0]);
			assert.strictEqual(children.length, 0);
		});
	});

	describe('getTreeItem', () => {
		it('returns the same element', async () => {
			cache = new ServerDataCache(createMockClient(sampleServers) as any);
			await cache.refresh();
			provider = new BackendTreeProvider(cache);
			const roots = provider.getChildren();
			assert.strictEqual(provider.getTreeItem(roots[0]), roots[0]);
		});
	});

	describe('refresh', () => {
		it('fires onDidChangeTreeData event', (done) => {
			cache = new ServerDataCache(createMockClient() as any);
			provider = new BackendTreeProvider(cache);
			provider.onDidChangeTreeData(() => { done(); });
			provider.refresh();
		});
	});

	describe('SAP filtering', () => {
		it('filters out SAP servers from MCP backends view', async () => {
			cache = new ServerDataCache(createMockClient(mixedServers) as any);
			await cache.refresh();
			provider = new BackendTreeProvider(cache);
			const items = provider.getChildren();
			// Only non-SAP servers should appear (7 original, no vsp-DEV or sap-gui-DEV-100).
			assert.strictEqual(items.length, 7);
			const names = items.map((i) => (i as BackendItem).server.name);
			assert.ok(!names.includes('vsp-DEV'), 'SAP VSP server should be filtered');
			assert.ok(!names.includes('sap-gui-DEV-100'), 'SAP GUI server should be filtered');
		});

		it('non-SAP servers still appear correctly', async () => {
			cache = new ServerDataCache(createMockClient(mixedServers) as any);
			await cache.refresh();
			provider = new BackendTreeProvider(cache);
			const items = provider.getChildren();
			const names = items.map((i) => (i as BackendItem).server.name);
			assert.ok(names.includes('ctx7'));
			assert.ok(names.includes('orch'));
		});
	});

	describe('dispose', () => {
		it('prevents refresh() from firing after dispose', () => {
			cache = new ServerDataCache(createMockClient() as any);
			provider = new BackendTreeProvider(cache);
			provider.dispose();
			provider.refresh(); // should not throw
		});

		it('resets fingerprint to null on dispose', async () => {
			cache = new ServerDataCache(createMockClient(sampleServers) as any);
			await cache.refresh();
			provider = new BackendTreeProvider(cache);
			provider.refresh();
			assert.notStrictEqual(provider.getFingerprint(), null);
			provider.dispose();
			assert.strictEqual(provider.getFingerprint(), null);
		});
	});

	describe('fingerprint (diff refresh)', () => {
		it('does not fire onDidChangeTreeData when fingerprint unchanged', async () => {
			cache = new ServerDataCache(createMockClient(sampleServers) as any);
			await cache.refresh();
			provider = new BackendTreeProvider(cache);
			// First refresh establishes the fingerprint and fires once.
			let fireCount = 0;
			provider.onDidChangeTreeData(() => { fireCount++; });
			provider.refresh();
			assert.strictEqual(fireCount, 1, 'first refresh should fire');
			// Second refresh against identical cached data should skip fire().
			provider.refresh();
			assert.strictEqual(fireCount, 1, 'identical-snapshot refresh should not fire');
		});

		it('fires onDidChangeTreeData when a status changes', async () => {
			const initial: ServerView[] = [
				{ name: 'a', status: 'running', transport: 'stdio', restart_count: 0 },
			];
			const client = {
				calls: 0,
				snapshots: [initial, [{ ...initial[0], status: 'error' as const }]],
				listServers: async function () { return this.snapshots[Math.min(this.calls++, this.snapshots.length - 1)]; },
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
			provider = new BackendTreeProvider(cache);
			let fireCount = 0;
			provider.onDidChangeTreeData(() => { fireCount++; });
			await cache.refresh(); // snapshot 1 — fires
			await cache.refresh(); // snapshot 2 (status changed) — fires again
			assert.strictEqual(fireCount, 2, 'status change should trigger a second fire');
		});

		it('fires when restart_count changes even with same status', async () => {
			const initial: ServerView[] = [
				{ name: 'a', status: 'degraded', transport: 'stdio', restart_count: 1 },
			];
			const client = {
				calls: 0,
				snapshots: [initial, [{ ...initial[0], restart_count: 2 }]],
				listServers: async function () { return this.snapshots[Math.min(this.calls++, this.snapshots.length - 1)]; },
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
			provider = new BackendTreeProvider(cache);
			let fireCount = 0;
			provider.onDidChangeTreeData(() => { fireCount++; });
			await cache.refresh();
			await cache.refresh();
			assert.strictEqual(fireCount, 2);
		});
	});
});

describe('BackendItem', () => {
	describe('status icons', () => {
		const statusIcons: Array<{ status: ServerView['status']; iconId: string; hasColor: boolean }> = [
			{ status: 'running',    iconId: 'vm-running',   hasColor: true },
			{ status: 'stopped',    iconId: 'debug-stop',   hasColor: true },
			{ status: 'error',      iconId: 'error',        hasColor: true },
			{ status: 'degraded',   iconId: 'warning',      hasColor: true },
			{ status: 'disabled',   iconId: 'circle-slash', hasColor: true },
			{ status: 'starting',   iconId: 'loading~spin', hasColor: false },
			{ status: 'restarting', iconId: 'sync~spin',    hasColor: false },
		];

		for (const { status, iconId, hasColor } of statusIcons) {
			it(`uses ${iconId} icon for ${status} status`, () => {
				const server: ServerView = { name: 'test', status, transport: 'stdio', restart_count: 0 };
				const item = new BackendItem(server);
				const icon = item.iconPath as { id: string; color?: { id: string } };
				assert.strictEqual(icon.id, iconId);
				if (hasColor) {
					assert.ok(icon.color, `Expected color for ${status}`);
				} else {
					assert.strictEqual(icon.color, undefined, `Expected no color for ${status}`);
				}
			});
		}
	});

	describe('unknown status fallback', () => {
		it('uses fallback icon for unknown status', () => {
			const server = { name: 'test', status: 'initializing' as any, transport: 'stdio', restart_count: 0 };
			const item = new BackendItem(server);
			const icon = item.iconPath as { id: string; color?: { id: string } };
			assert.strictEqual(icon.id, 'question');
			assert.ok(icon.color, 'Expected fallback color');
		});
	});

	describe('contextValue', () => {
		it('matches server status', () => {
			const server: ServerView = { name: 'srv', status: 'degraded', transport: 'http', restart_count: 0 };
			const item = new BackendItem(server);
			assert.strictEqual(item.contextValue, 'degraded');
		});
	});

	describe('tooltip', () => {
		it('includes name, status, transport, and optional fields', () => {
			const server: ServerView = {
				name: 'ctx7', status: 'running', transport: 'http',
				pid: 42, restart_count: 3, last_error: 'oops',
				tools: [{ name: 't', description: 'd', server: 'ctx7' }],
			};
			const item = new BackendItem(server);
			const tip = item.tooltip as string;
			assert.ok(tip.includes('ctx7'));
			assert.ok(tip.includes('running'));
			assert.ok(tip.includes('http'));
			assert.ok(tip.includes('42'));
			assert.ok(tip.includes('3'));
			assert.ok(tip.includes('oops'));
			assert.ok(tip.includes('Tools: 1'));
		});

		it('omits optional fields when absent', () => {
			const server: ServerView = { name: 'min', status: 'stopped', transport: 'stdio', restart_count: 0 };
			const item = new BackendItem(server);
			const tip = item.tooltip as string;
			assert.ok(!tip.includes('PID'));
			assert.ok(!tip.includes('Restarts'));
			assert.ok(!tip.includes('Error'));
			assert.ok(!tip.includes('Tools'));
		});
	});
});

describe('BackendItem description (transport badge + restart count)', () => {
	it('shows transport type as description', () => {
		const server: ServerView = { name: 'srv', status: 'running', transport: 'stdio', restart_count: 0 };
		const item = new BackendItem(server);
		assert.strictEqual(item.description, 'stdio');
	});

	it('shows http transport', () => {
		const server: ServerView = { name: 'srv', status: 'running', transport: 'http', restart_count: 0 };
		const item = new BackendItem(server);
		assert.strictEqual(item.description, 'http');
	});

	it('appends restart count when > 0', () => {
		const server: ServerView = { name: 'srv', status: 'degraded', transport: 'stdio', restart_count: 3 };
		const item = new BackendItem(server);
		assert.strictEqual(item.description, 'stdio (x3)');
	});

	it('omits restart count when 0', () => {
		const server: ServerView = { name: 'srv', status: 'running', transport: 'http', restart_count: 0 };
		const item = new BackendItem(server);
		assert.strictEqual(item.description, 'http');
	});

	it('falls back to "rest" when transport is empty', () => {
		const server: ServerView = { name: 'srv', status: 'running', transport: '', restart_count: 0 };
		const item = new BackendItem(server);
		assert.strictEqual(item.description, 'rest');
		const tip = item.tooltip as string;
		assert.ok(tip.includes('Transport: rest'), `Expected tooltip to contain "Transport: rest", got: ${tip}`);
	});

	it('falls back to "rest" with restart count when transport is empty', () => {
		const server: ServerView = { name: 'srv', status: 'running', transport: '', restart_count: 2 };
		const item = new BackendItem(server);
		assert.strictEqual(item.description, 'rest (x2)');
	});
});
