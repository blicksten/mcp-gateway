import './mock-vscode';
import { mockStatusBarItems, resetMockState, type MockStatusBarItem, MockMarkdownString } from './mock-vscode';

import * as assert from 'node:assert';
import { describe, it, beforeEach, afterEach } from 'mocha';
import { McpStatusBar } from '../status-bar';
import { ServerDataCache } from '../server-data-cache';
import type { ServerView } from '../types';

function makeClient(servers: ServerView[]) {
	const state = { servers, fail: false };
	const client = {
		listServers: async () => {
			if (state.fail) { throw new Error('connection refused'); }
			return state.servers;
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
	return { client, state };
}

function colorId(item: MockStatusBarItem): string | undefined {
	const c = item.color;
	if (c === undefined || c === null) { return undefined; }
	return (c as { id: string }).id;
}

function tooltipValue(item: MockStatusBarItem): string {
	const t = item.tooltip;
	if (t instanceof MockMarkdownString) { return t.value; }
	return typeof t === 'string' ? t : '';
}

function latestItem(): MockStatusBarItem {
	return mockStatusBarItems[mockStatusBarItems.length - 1];
}

describe('McpStatusBar (cache-driven)', () => {
	let cache: ServerDataCache;
	let bar: McpStatusBar;

	beforeEach(() => {
		resetMockState();
	});

	afterEach(() => {
		bar?.dispose();
		cache?.dispose();
	});

	describe('initial state', () => {
		it('creates a status bar item on construction', () => {
			const { client } = makeClient([]);
			cache = new ServerDataCache(client as any);
			bar = new McpStatusBar(cache);
			assert.strictEqual(mockStatusBarItems.length, 1);
		});

		it('shows the item immediately', () => {
			const { client } = makeClient([]);
			cache = new ServerDataCache(client as any);
			bar = new McpStatusBar(cache);
			assert.strictEqual(latestItem().visible, true);
		});

		it('sets command to focus tree view', () => {
			const { client } = makeClient([]);
			cache = new ServerDataCache(client as any);
			bar = new McpStatusBar(cache);
			assert.strictEqual(latestItem().command, 'mcpBackends.focus');
		});
	});

	describe('cache refresh — all running', () => {
		it('shows N/M with check icon when all MCP servers are running', async () => {
			const servers: ServerView[] = [
				{ name: 'a', status: 'running', transport: 'stdio', restart_count: 0 },
				{ name: 'b', status: 'running', transport: 'http', restart_count: 0 },
				{ name: 'c', status: 'running', transport: 'stdio', restart_count: 0 },
				{ name: 'd', status: 'running', transport: 'http', restart_count: 0 },
			];
			const { client } = makeClient(servers);
			cache = new ServerDataCache(client as any);
			bar = new McpStatusBar(cache);
			await cache.refresh();
			assert.strictEqual(latestItem().text, '$(check) MCP: 4/4');
			assert.strictEqual(colorId(latestItem()), 'testing.iconPassed');
		});

		it('tooltip is a MarkdownString with all-running phrasing and a server list', async () => {
			const servers: ServerView[] = [
				{ name: 'alpha', status: 'running', transport: 'stdio', restart_count: 0 },
				{ name: 'beta', status: 'running', transport: 'http', restart_count: 0 },
			];
			const { client } = makeClient(servers);
			cache = new ServerDataCache(client as any);
			bar = new McpStatusBar(cache);
			await cache.refresh();
			const tip = latestItem().tooltip;
			assert.ok(tip instanceof MockMarkdownString, 'tooltip should be a MarkdownString');
			assert.strictEqual((tip as MockMarkdownString).isTrusted, false);
			const value = (tip as MockMarkdownString).value;
			assert.ok(value.includes('all 2 servers running'), `unexpected tooltip: ${value}`);
			assert.ok(value.includes('alpha'), 'tooltip should list alpha');
			assert.ok(value.includes('beta'), 'tooltip should list beta');
		});
	});

	describe('cache refresh — partial', () => {
		it('shows warning color + partial tooltip', async () => {
			const servers: ServerView[] = [
				{ name: 'a', status: 'running', transport: 'stdio', restart_count: 0 },
				{ name: 'b', status: 'running', transport: 'http', restart_count: 0 },
				{ name: 'c', status: 'degraded', transport: 'stdio', restart_count: 0 },
				{ name: 'd', status: 'error', transport: 'http', restart_count: 0 },
			];
			const { client } = makeClient(servers);
			cache = new ServerDataCache(client as any);
			bar = new McpStatusBar(cache);
			await cache.refresh();
			assert.strictEqual(latestItem().text, '$(warning) MCP: 2/4');
			assert.strictEqual(latestItem().backgroundColor, undefined);
			assert.strictEqual(colorId(latestItem()), 'notificationsWarningIcon.foreground');
			assert.ok(tooltipValue(latestItem()).includes('2 of 4'));
		});
	});

	describe('cache refresh — all offline', () => {
		it('shows error color when no server is running', async () => {
			const servers: ServerView[] = [
				{ name: 'a', status: 'stopped', transport: 'stdio', restart_count: 0 },
				{ name: 'b', status: 'error', transport: 'http', restart_count: 0 },
				{ name: 'c', status: 'disabled', transport: 'stdio', restart_count: 0 },
			];
			const { client } = makeClient(servers);
			cache = new ServerDataCache(client as any);
			bar = new McpStatusBar(cache);
			await cache.refresh();
			assert.strictEqual(latestItem().text, '$(error) MCP: 0/3');
			assert.strictEqual(colorId(latestItem()), 'testing.iconFailed');
		});
	});

	describe('cache refresh — no servers', () => {
		it('shows dash icon + "no servers configured" tooltip', async () => {
			const { client } = makeClient([]);
			cache = new ServerDataCache(client as any);
			bar = new McpStatusBar(cache);
			await cache.refresh();
			assert.strictEqual(latestItem().text, '$(circle-slash) MCP: \u2014');
			assert.strictEqual(latestItem().backgroundColor, undefined);
			assert.ok(tooltipValue(latestItem()).includes('no servers configured'));
		});
	});

	describe('cache refresh — daemon unreachable', () => {
		it('shows offline state when cache refresh failed', async () => {
			const { client, state } = makeClient([]);
			cache = new ServerDataCache(client as any);
			bar = new McpStatusBar(cache);
			state.fail = true;
			await cache.refresh();
			assert.strictEqual(latestItem().text, '$(debug-disconnect) MCP: offline');
			assert.strictEqual(latestItem().backgroundColor, undefined);
			assert.ok(tooltipValue(latestItem()).includes('cannot reach daemon'));
		});
	});

	describe('SAP servers are excluded from counts', () => {
		it('only counts non-SAP servers', async () => {
			const servers: ServerView[] = [
				{ name: 'mcp-a', status: 'running', transport: 'stdio', restart_count: 0 },
				{ name: 'vsp-DEV-100', status: 'running', transport: 'stdio', restart_count: 0 },
				{ name: 'sap-gui-DEV-100', status: 'running', transport: 'http', restart_count: 0 },
			];
			const { client } = makeClient(servers);
			cache = new ServerDataCache(client as any);
			bar = new McpStatusBar(cache);
			await cache.refresh();
			// Only 'mcp-a' is counted; vsp-* and sap-gui-* go to SapStatusBar.
			assert.strictEqual(latestItem().text, '$(check) MCP: 1/1');
		});
	});

	describe('dispose', () => {
		it('disposes status bar item and unsubscribes from cache', async () => {
			const { client } = makeClient([
				{ name: 'a', status: 'running', transport: 'stdio', restart_count: 0 },
			]);
			cache = new ServerDataCache(client as any);
			bar = new McpStatusBar(cache);
			const item = latestItem();
			bar.dispose();
			assert.strictEqual(item.disposed, true);
			// Subsequent cache refresh must not update the disposed bar.
			const before = item.text;
			await cache.refresh();
			assert.strictEqual(item.text, before);
		});
	});

	describe('state transitions', () => {
		it('transitions from running to offline when cache reports failure', async () => {
			const { client, state } = makeClient([
				{ name: 'a', status: 'running', transport: 'stdio', restart_count: 0 },
				{ name: 'b', status: 'running', transport: 'http', restart_count: 0 },
			]);
			cache = new ServerDataCache(client as any);
			bar = new McpStatusBar(cache);
			await cache.refresh();
			assert.strictEqual(colorId(latestItem()), 'testing.iconPassed');

			state.fail = true;
			await cache.refresh();
			assert.strictEqual(latestItem().text, '$(debug-disconnect) MCP: offline');
			assert.strictEqual(latestItem().color, undefined);
		});

		it('transitions from offline to running', async () => {
			const { client, state } = makeClient([]);
			state.fail = true;
			cache = new ServerDataCache(client as any);
			bar = new McpStatusBar(cache);
			await cache.refresh();
			assert.strictEqual(latestItem().color, undefined);
			assert.strictEqual(latestItem().text, '$(debug-disconnect) MCP: offline');

			state.fail = false;
			state.servers.push(
				{ name: 'a', status: 'running', transport: 'stdio', restart_count: 0 },
				{ name: 'b', status: 'running', transport: 'http', restart_count: 0 },
			);
			await cache.refresh();
			assert.strictEqual(latestItem().text, '$(check) MCP: 2/2');
			assert.strictEqual(colorId(latestItem()), 'testing.iconPassed');
		});
	});

	describe('daemon version in status bar text', () => {
		function makeVersionClient(servers: ServerView[], version: string) {
			const { client, state } = makeClient(servers);
			(client as any).getHealth = async () => ({
				status: 'ok',
				servers: servers.length,
				running: servers.filter((s) => s.status === 'running').length,
				version,
			});
			return { client, state };
		}

		it('appends version suffix when all servers running', async () => {
			const { client } = makeVersionClient(
				[{ name: 'a', status: 'running', transport: 'stdio', restart_count: 0 }],
				'1.22.0',
			);
			cache = new ServerDataCache(client as any);
			bar = new McpStatusBar(cache);
			await cache.refresh();
			assert.strictEqual(latestItem().text, '$(check) MCP: 1/1 · v1.22.0');
		});

		it('appends version suffix when some servers offline', async () => {
			const { client } = makeVersionClient(
				[
					{ name: 'a', status: 'running', transport: 'stdio', restart_count: 0 },
					{ name: 'b', status: 'stopped', transport: 'http', restart_count: 0 },
				],
				'2.0.0',
			);
			cache = new ServerDataCache(client as any);
			bar = new McpStatusBar(cache);
			await cache.refresh();
			assert.strictEqual(latestItem().text, '$(warning) MCP: 1/2 · v2.0.0');
		});

		it('appends version suffix when no servers configured', async () => {
			const { client } = makeVersionClient([], '1.22.0');
			cache = new ServerDataCache(client as any);
			bar = new McpStatusBar(cache);
			await cache.refresh();
			assert.ok(latestItem().text.includes('· v1.22.0'), `expected version in: ${latestItem().text}`);
		});

		it('no version suffix when health has no version (older daemons)', async () => {
			const servers = [{ name: 'a', status: 'running', transport: 'stdio', restart_count: 0 }];
			const { client } = makeClient(servers as ServerView[]);
			cache = new ServerDataCache(client as any);
			bar = new McpStatusBar(cache);
			await cache.refresh();
			// No · v suffix — graceful degradation for daemons pre-dating D.1.
			assert.strictEqual(latestItem().text, '$(check) MCP: 1/1');
		});
	});

	describe('no legacy polling API', () => {
		it('does not expose startPolling / stopPolling / poll', () => {
			const { client } = makeClient([]);
			cache = new ServerDataCache(client as any);
			bar = new McpStatusBar(cache);
			assert.strictEqual((bar as unknown as { startPolling?: unknown }).startPolling, undefined);
			assert.strictEqual((bar as unknown as { stopPolling?: unknown }).stopPolling, undefined);
			assert.strictEqual((bar as unknown as { poll?: unknown }).poll, undefined);
		});
	});
});
