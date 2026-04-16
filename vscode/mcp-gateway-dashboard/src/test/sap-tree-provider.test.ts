import './mock-vscode';
import { strict as assert } from 'node:assert';
import { SapTreeProvider } from '../sap-tree-provider';
import { SapSystemItem, SapComponentItem } from '../sap-item';
import { ServerDataCache } from '../server-data-cache';
import type { ServerView } from '../types';
import { mockConfigValues, fireConfigChange, resetMockState } from './mock-vscode';

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

	beforeEach(() => {
		resetMockState();
	});

	afterEach(() => {
		provider?.dispose();
		cache?.dispose();
		resetMockState();
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

	it('does not fire onDidChangeTreeData when fingerprint unchanged', async () => {
		const servers: ServerView[] = [
			{ name: 'vsp-DEV', status: 'running', transport: 'stdio', restart_count: 0 },
			{ name: 'sap-gui-DEV', status: 'running', transport: 'http', restart_count: 0 },
		];
		cache = new ServerDataCache(createMockClient(servers) as any);
		await cache.refresh();
		provider = new SapTreeProvider(cache);
		let fireCount = 0;
		provider.onDidChangeTreeData(() => { fireCount++; });
		provider.refresh();
		assert.strictEqual(fireCount, 1);
		provider.refresh();
		assert.strictEqual(fireCount, 1, 'identical-snapshot refresh should not fire');
	});

	it('fires again when VSP restart_count changes', async () => {
		let counter = 0;
		const client = {
			listServers: async () => [
				{ name: 'vsp-DEV', status: 'running' as const, transport: 'stdio', restart_count: counter },
				{ name: 'sap-gui-DEV', status: 'running' as const, transport: 'http', restart_count: 0 },
			],
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
		provider = new SapTreeProvider(cache);
		let fireCount = 0;
		provider.onDidChangeTreeData(() => { fireCount++; });
		await cache.refresh(); // counter=0
		counter = 3;
		await cache.refresh(); // restart_count changed
		assert.strictEqual(fireCount, 2);
	});

	it('fires when vsp pid changes silently (same status)', async () => {
		let pid = 1111;
		const client = {
			listServers: async () => [
				{ name: 'vsp-DEV', status: 'running' as const, transport: 'stdio', restart_count: 0, pid },
				{ name: 'sap-gui-DEV', status: 'running' as const, transport: 'http', restart_count: 0 },
			],
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
		provider = new SapTreeProvider(cache);
		let fireCount = 0;
		provider.onDidChangeTreeData(() => { fireCount++; });
		await cache.refresh();
		pid = 2222;
		await cache.refresh();
		assert.strictEqual(fireCount, 2, 'pid change should trigger tooltip refresh');
	});

	it('fires when vsp last_error appears (same status)', async () => {
		let lastError: string | undefined = undefined;
		const client = {
			listServers: async () => [
				{ name: 'vsp-DEV', status: 'degraded' as const, transport: 'stdio', restart_count: 1, last_error: lastError },
			],
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
		provider = new SapTreeProvider(cache);
		let fireCount = 0;
		provider.onDidChangeTreeData(() => { fireCount++; });
		await cache.refresh();
		lastError = 'timeout';
		await cache.refresh();
		assert.strictEqual(fireCount, 2);
	});

	it('resets fingerprint to null on dispose', async () => {
		const servers: ServerView[] = [
			{ name: 'vsp-DEV', status: 'running', transport: 'stdio', restart_count: 0 },
		];
		cache = new ServerDataCache(createMockClient(servers) as any);
		await cache.refresh();
		provider = new SapTreeProvider(cache);
		provider.refresh();
		assert.notStrictEqual(provider.getFingerprint(), null);
		provider.dispose();
		assert.strictEqual(provider.getFingerprint(), null);
	});

	describe('hierarchical mode (Phase 11.D)', () => {
		async function setupHierarchical(initial: ServerView[]): Promise<SapTreeProvider> {
			// Constructor reads the config value — set before construction.
			mockConfigValues['mcpGateway.sapGroupBySid'] = true;
			cache = new ServerDataCache(createMockClient(initial) as any);
			await cache.refresh();
			return new SapTreeProvider(cache);
		}

		it('default mode is flat (sapGroupBySid=false)', async () => {
			cache = new ServerDataCache(createMockClient([
				{ name: 'vsp-DEV', status: 'running', transport: 'stdio', restart_count: 0 },
				{ name: 'sap-gui-DEV', status: 'running', transport: 'http', restart_count: 0 },
			]) as any);
			await cache.refresh();
			provider = new SapTreeProvider(cache);
			assert.strictEqual(provider.isHierarchical(), false);
			const roots = provider.getChildren() as SapSystemItem[];
			assert.strictEqual(roots.length, 1);
			assert.strictEqual(roots[0].collapsibleState, 0); // None
			assert.strictEqual(roots[0].contextValue, 'sap-running');
			assert.deepStrictEqual(provider.getChildren(roots[0]), []);
		});

		it('reads sapGroupBySid=true from config at construction time', async () => {
			provider = await setupHierarchical([
				{ name: 'vsp-DEV', status: 'running', transport: 'stdio', restart_count: 0 },
			]);
			assert.strictEqual(provider.isHierarchical(), true);
		});

		it('hierarchical mode returns collapsible parents with VSP/GUI children', async () => {
			provider = await setupHierarchical([
				{ name: 'vsp-DEV', status: 'running', transport: 'stdio', restart_count: 0 },
				{ name: 'sap-gui-DEV', status: 'running', transport: 'http', restart_count: 0 },
			]);

			const roots = provider.getChildren() as SapSystemItem[];
			assert.strictEqual(roots.length, 1);
			assert.ok(roots[0] instanceof SapSystemItem);
			assert.strictEqual(roots[0].collapsibleState, 1); // Collapsed
			assert.strictEqual(roots[0].contextValue, 'sap-group-running');

			const children = provider.getChildren(roots[0]) as SapComponentItem[];
			assert.strictEqual(children.length, 2);
			assert.ok(children[0] instanceof SapComponentItem);
			assert.strictEqual(children[0].kind, 'vsp');
			assert.strictEqual(children[0].contextValue, 'sap-vsp-running');
			assert.strictEqual(children[1].kind, 'gui');
			assert.strictEqual(children[1].contextValue, 'sap-gui-running');
		});

		it('hierarchical children omit missing components', async () => {
			provider = await setupHierarchical([
				{ name: 'vsp-QAS', status: 'running', transport: 'stdio', restart_count: 0 },
			]);
			const roots = provider.getChildren() as SapSystemItem[];
			const children = provider.getChildren(roots[0]) as SapComponentItem[];
			assert.strictEqual(children.length, 1);
			assert.strictEqual(children[0].kind, 'vsp');
		});

		it('hierarchical mode fingerprint includes the H/F marker', async () => {
			cache = new ServerDataCache(createMockClient([
				{ name: 'vsp-DEV', status: 'running', transport: 'stdio', restart_count: 0 },
			]) as any);
			await cache.refresh();
			provider = new SapTreeProvider(cache);
			provider.refresh();
			const flatFp = provider.getFingerprint();
			assert.ok(flatFp && flatFp.startsWith('F;'));

			// Flip the live config to hierarchical and fire the VS Code change event.
			mockConfigValues['mcpGateway.sapGroupBySid'] = true;
			fireConfigChange('mcpGateway.sapGroupBySid');
			provider.refresh();
			const hierFp = provider.getFingerprint();
			assert.ok(hierFp && hierFp.startsWith('H;'));
			assert.notStrictEqual(hierFp, flatFp);
		});

		it('onDidChangeConfiguration fires onDidChangeTreeData end-to-end (fallback fixed F-4)', async () => {
			cache = new ServerDataCache(createMockClient([
				{ name: 'vsp-DEV', status: 'running', transport: 'stdio', restart_count: 0 },
			]) as any);
			await cache.refresh();
			provider = new SapTreeProvider(cache);
			let fireCount = 0;
			provider.onDidChangeTreeData(() => { fireCount++; });
			provider.refresh(); // initial fire (count=1)
			const initialFireCount = fireCount;

			// Simulate the user toggling `mcpGateway.sapGroupBySid` in VSCode settings.
			mockConfigValues['mcpGateway.sapGroupBySid'] = true;
			fireConfigChange('mcpGateway.sapGroupBySid');
			assert.ok(fireCount > initialFireCount, 'config change must fire onDidChangeTreeData');
			assert.strictEqual(provider.isHierarchical(), true);
		});

		it('ignores unrelated configuration changes', async () => {
			provider = await setupHierarchical([
				{ name: 'vsp-DEV', status: 'running', transport: 'stdio', restart_count: 0 },
			]);
			let fireCount = 0;
			provider.onDidChangeTreeData(() => { fireCount++; });
			// Fire a different config key — must NOT trigger a tree refresh.
			fireConfigChange('mcpGateway.pollInterval');
			assert.strictEqual(fireCount, 0);
		});

		it('no-op when setting changes but value is unchanged', async () => {
			provider = await setupHierarchical([
				{ name: 'vsp-DEV', status: 'running', transport: 'stdio', restart_count: 0 },
			]);
			let fireCount = 0;
			provider.onDidChangeTreeData(() => { fireCount++; });
			// Fire the event without actually changing the value — the handler
			// should early-return because the derived value matches current state.
			fireConfigChange('mcpGateway.sapGroupBySid');
			assert.strictEqual(fireCount, 0);
		});

		it('flat-mode children query returns empty (no recursion)', async () => {
			cache = new ServerDataCache(createMockClient([
				{ name: 'vsp-DEV', status: 'running', transport: 'stdio', restart_count: 0 },
				{ name: 'sap-gui-DEV', status: 'running', transport: 'http', restart_count: 0 },
			]) as any);
			await cache.refresh();
			provider = new SapTreeProvider(cache);
			const roots = provider.getChildren() as SapSystemItem[];
			assert.deepStrictEqual(provider.getChildren(roots[0]), []);
		});
	});
});
