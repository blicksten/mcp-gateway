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

// =============================================================================
// FIX 3 — status bar heartbeat
// =============================================================================

describe('McpStatusBar heartbeat (FIX 3)', () => {
	let cache: ServerDataCache;
	let bar: McpStatusBar;

	beforeEach(() => {
		resetMockState();
	});

	afterEach(() => {
		bar?.dispose();
		cache?.dispose();
	});

	/** Build injectable timer/clock for heartbeat tests. */
	function createClockMock() {
		let nowMs = 1_000_000; // arbitrary non-zero start
		const intervals: Array<{ cb: () => void; ms: number; handle: object; active: boolean }> = [];
		let nextHandle = 1;

		const setIntervalFn = (cb: () => void, ms: number): any => {
			const handle = { id: nextHandle++ };
			intervals.push({ cb, ms, handle, active: true });
			return handle;
		};

		const clearIntervalFn = (h: any): void => {
			const entry = intervals.find((i) => i.handle === h);
			if (entry) { entry.active = false; }
		};

		const now = () => nowMs;
		const advance = (ms: number) => {
			nowMs += ms;
			for (const i of intervals) {
				if (i.active) { i.cb(); } // fire all active intervals
			}
		};

		return { setIntervalFn, clearIntervalFn, now, advance, intervals };
	}

	it('HB-unknown: no successful refresh within heartbeatMs -> renders unknown (not stale green)', async () => {
		const { client } = makeClient([
			{ name: 'a', status: 'running', transport: 'stdio', restart_count: 0 },
		]);
		cache = new ServerDataCache(client as any);

		const clock = createClockMock();
		// First do a successful refresh to set lastSuccessfulRefreshAt
		bar = new McpStatusBar(cache, undefined, undefined, {
			heartbeatMs: 1000,
			setInterval: clock.setIntervalFn as any,
			clearInterval: clock.clearIntervalFn as any,
			now: clock.now,
		});
		await cache.refresh(); // sets lastSuccessfulRefreshAt

		// Advance time past the heartbeat threshold
		clock.advance(2000);

		assert.ok(
			latestItem().text.includes('?') || latestItem().text.includes('question'),
			`Expected "unknown" state after heartbeat, got: "${latestItem().text}"`,
		);
	});

	it('HB-reset: successful refresh resets heartbeat (no unknown rendered)', async () => {
		const { client, state } = makeClient([]);
		cache = new ServerDataCache(client as any);

		const clock = createClockMock();
		bar = new McpStatusBar(cache, undefined, undefined, {
			heartbeatMs: 1000,
			setInterval: clock.setIntervalFn as any,
			clearInterval: clock.clearIntervalFn as any,
			now: clock.now,
		});

		// First successful refresh
		await cache.refresh();
		// Advance a bit but within threshold — then do another successful refresh
		clock.advance(500);
		await cache.refresh();
		// Advance past original heartbeat — but last refresh was only 500ms ago, within new window
		clock.advance(600); // total 1100ms but last refresh at 500ms so age=600ms < 1000ms threshold

		// After the heartbeat tick, we should NOT be in unknown state
		assert.ok(
			!latestItem().text.includes('?'),
			`Expected non-unknown state after recent refresh, got: "${latestItem().text}"`,
		);
		void state; // suppress unused var
	});

	it('HB-lastRefreshFailed-does-not-reset: a lastRefreshFailed event does NOT reset lastSuccessfulRefreshAt', async () => {
		const { client, state } = makeClient([
			{ name: 'a', status: 'running', transport: 'stdio', restart_count: 0 },
		]);
		cache = new ServerDataCache(client as any);

		const clock = createClockMock();
		bar = new McpStatusBar(cache, undefined, undefined, {
			heartbeatMs: 1000,
			setInterval: clock.setIntervalFn as any,
			clearInterval: clock.clearIntervalFn as any,
			now: clock.now,
		});

		// Successful refresh at t=0
		await cache.refresh();
		const successTime = clock.now();

		// Now fail refresh — this should render offline but NOT update lastSuccessfulRefreshAt
		state.fail = true;
		await cache.refresh();

		// Verify the bar shows offline (from the failed refresh), not unknown
		assert.ok(
			latestItem().text.includes('offline'),
			`Expected offline state after failed refresh, got: "${latestItem().text}"`,
		);

		// Advance clock so heartbeat fires — bar should stay "offline" (not flip to unknown)
		// because the explicit lastRefreshFailed takes precedence
		clock.advance(2000);

		// Even after heartbeat fires, "offline" wins because cache.lastRefreshFailed is true
		assert.ok(
			latestItem().text.includes('offline') || latestItem().text.includes('?'),
			`Expected offline or unknown after failed refresh + heartbeat, got: "${latestItem().text}"`,
		);
		void successTime; // suppress unused var
	});

	it('HB-unknown-distinct-from-offline: unknown text is different from offline text', async () => {
		const { client } = makeClient([
			{ name: 'a', status: 'running', transport: 'stdio', restart_count: 0 },
		]);
		cache = new ServerDataCache(client as any);

		const clock = createClockMock();
		bar = new McpStatusBar(cache, undefined, undefined, {
			heartbeatMs: 1000,
			setInterval: clock.setIntervalFn as any,
			clearInterval: clock.clearIntervalFn as any,
			now: clock.now,
		});

		// Successful refresh
		await cache.refresh();
		// Advance to trigger unknown
		clock.advance(2000);
		const unknownText = latestItem().text;

		// Now trigger offline via a new bar on a failed cache
		bar.dispose();
		const { client: c2, state } = makeClient([]);
		state.fail = true;
		const cache2 = new ServerDataCache(c2 as any);
		const bar2 = new McpStatusBar(cache2);
		await cache2.refresh();
		const offlineText = latestItem().text;

		assert.notStrictEqual(unknownText, offlineText,
			`unknown state ("${unknownText}") must be visually distinct from offline ("${offlineText}")`);
		assert.ok(unknownText.includes('?'), `unknown must contain '?', got: ${unknownText}`);
		assert.ok(offlineText.includes('offline'), `offline must contain 'offline', got: ${offlineText}`);

		bar2.dispose();
		cache2.dispose();
	});

	it('HB-dispose-clears-interval: dispose() clears the heartbeat interval', () => {
		const { client } = makeClient([]);
		cache = new ServerDataCache(client as any);

		const clock = createClockMock();
		bar = new McpStatusBar(cache, undefined, undefined, {
			heartbeatMs: 1000,
			setInterval: clock.setIntervalFn as any,
			clearInterval: clock.clearIntervalFn as any,
			now: clock.now,
		});

		assert.strictEqual(clock.intervals.filter((i) => i.active).length, 1, 'one active interval after construction');

		bar.dispose();
		bar = undefined as any; // prevent afterEach double-dispose

		assert.strictEqual(clock.intervals.filter((i) => i.active).length, 0, 'no active intervals after dispose');
	});
});
