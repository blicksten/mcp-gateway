import './mock-vscode';
import { strict as assert } from 'node:assert';
import { mockStatusBarItems, resetMockState } from './mock-vscode';
import { SapStatusBar } from '../sap-status-bar';
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

describe('SapStatusBar', () => {
	let cache: ServerDataCache;
	let bar: SapStatusBar;

	afterEach(() => {
		bar?.dispose();
		cache?.dispose();
		resetMockState();
	});

	it('hides when no SAP systems', async () => {
		cache = new ServerDataCache(createMockClient([
			{ name: 'my-server', status: 'running', transport: 'stdio', restart_count: 0 },
		]) as any);
		await cache.refresh();
		bar = new SapStatusBar(cache);

		const item = mockStatusBarItems[mockStatusBarItems.length - 1];
		assert.equal(item.visible, false);
	});

	it('shows with correct text when SAP systems present', async () => {
		cache = new ServerDataCache(createMockClient([
			{ name: 'vsp-DEV', status: 'running', transport: 'stdio', restart_count: 0 },
			{ name: 'sap-gui-DEV', status: 'running', transport: 'http', restart_count: 0 },
		]) as any);
		await cache.refresh();
		bar = new SapStatusBar(cache);

		const item = mockStatusBarItems[mockStatusBarItems.length - 1];
		assert.equal(item.visible, true);
		assert.ok(item.text.includes('SAP:'));
		assert.ok(item.text.includes('DEV'));
	});

	it('updates on cache refresh', async () => {
		const servers: ServerView[] = [];
		const client = createMockClient(servers);
		cache = new ServerDataCache(client as any);
		await cache.refresh();
		bar = new SapStatusBar(cache);

		const item = mockStatusBarItems[mockStatusBarItems.length - 1];
		assert.equal(item.visible, false);

		// Add SAP servers and refresh.
		servers.push(
			{ name: 'vsp-QAS', status: 'running', transport: 'stdio', restart_count: 0 },
		);
		client.listServers = async () => servers;
		await cache.refresh();

		assert.equal(item.visible, true);
		assert.ok(item.text.includes('QAS'));
	});

	it('shows error background when a system has error status', async () => {
		cache = new ServerDataCache(createMockClient([
			{ name: 'vsp-DEV', status: 'error', transport: 'stdio', restart_count: 0 },
		]) as any);
		await cache.refresh();
		bar = new SapStatusBar(cache);

		const item = mockStatusBarItems[mockStatusBarItems.length - 1];
		assert.ok(item.backgroundColor);
		assert.equal(item.backgroundColor!.id, 'statusBarItem.errorBackground');
	});

	it('shows warning background when composite status is degraded (VSP running, GUI error)', async () => {
		cache = new ServerDataCache(createMockClient([
			{ name: 'vsp-DEV', status: 'running', transport: 'stdio', restart_count: 0 },
			{ name: 'sap-gui-DEV', status: 'error', transport: 'http', restart_count: 0 },
		]) as any);
		await cache.refresh();
		bar = new SapStatusBar(cache);

		const item = mockStatusBarItems[mockStatusBarItems.length - 1];
		assert.ok(item.backgroundColor);
		assert.equal(item.backgroundColor!.id, 'statusBarItem.warningBackground');
	});

	it('clears background when all systems are running', async () => {
		cache = new ServerDataCache(createMockClient([
			{ name: 'vsp-DEV', status: 'running', transport: 'stdio', restart_count: 0 },
			{ name: 'sap-gui-DEV', status: 'running', transport: 'http', restart_count: 0 },
		]) as any);
		await cache.refresh();
		bar = new SapStatusBar(cache);

		const item = mockStatusBarItems[mockStatusBarItems.length - 1];
		assert.equal(item.backgroundColor, undefined);
	});

	it('uses full SID-Client for ≤3 systems with client codes', async () => {
		cache = new ServerDataCache(createMockClient([
			{ name: 'vsp-AA1-100', status: 'running', transport: 'stdio', restart_count: 0 },
			{ name: 'vsp-BB2-200', status: 'running', transport: 'stdio', restart_count: 0 },
		]) as any);
		await cache.refresh();
		bar = new SapStatusBar(cache);
		const item = mockStatusBarItems[mockStatusBarItems.length - 1];
		// ≤3 systems → full key (SID-CLIENT) displayed.
		assert.ok(item.text.includes('AA1-100'));
		assert.ok(item.text.includes('BB2-200'));
	});

	it('uses adaptive display: full key for ≤3, base SID for 4+', async () => {
		cache = new ServerDataCache(createMockClient([
			{ name: 'vsp-AA1', status: 'running', transport: 'stdio', restart_count: 0 },
			{ name: 'vsp-BB2', status: 'running', transport: 'stdio', restart_count: 0 },
			{ name: 'vsp-CC3', status: 'running', transport: 'stdio', restart_count: 0 },
			{ name: 'vsp-DD4', status: 'running', transport: 'stdio', restart_count: 0 },
		]) as any);
		await cache.refresh();
		bar = new SapStatusBar(cache);

		const item = mockStatusBarItems[mockStatusBarItems.length - 1];
		// 4 systems → base SID displayed (3 chars each).
		assert.ok(item.text.includes('AA1'));
		assert.ok(item.text.includes('DD4'));
	});

	it('dispose does not throw', async () => {
		cache = new ServerDataCache(createMockClient([
			{ name: 'vsp-DEV', status: 'running', transport: 'stdio', restart_count: 0 },
		]) as any);
		await cache.refresh();
		bar = new SapStatusBar(cache);

		assert.doesNotThrow(() => bar.dispose());
		// Double dispose is safe.
		assert.doesNotThrow(() => bar.dispose());
	});

	it('paints initial state from pre-existing cache data (H-01)', async () => {
		cache = new ServerDataCache(createMockClient([
			{ name: 'vsp-DEV', status: 'running', transport: 'stdio', restart_count: 0 },
		]) as any);
		await cache.refresh();

		// Construct AFTER cache already has data — should show immediately.
		bar = new SapStatusBar(cache);
		const item = mockStatusBarItems[mockStatusBarItems.length - 1];
		assert.equal(item.visible, true);
		assert.ok(item.text.includes('DEV'));
	});
});
