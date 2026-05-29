// Unit tests for McpStatusBar's yellow-not-red bucket logic introduced for
// the StatusUnreachable feature. Closes the MEDIUM gap from cd931db
// commit message (review-feature-64baca01): "TS UI unit tests for
// status-bar yellow-not-red bucket" was listed under NOT-YET-DONE.
//
// The status bar must distinguish three buckets when the running count is
// less than total:
//   - all servers unreachable  → yellow ("$(warning)") + "0/N (offline)"
//                                 + list.warningForeground theme color
//   - any unreachable + partial running → yellow ("$(warning)") + N/M
//                                 + list.warningForeground (matches per-row)
//   - generic partial (no unreachable) → notificationsWarningIcon.foreground
//                                 (existing yellow path, unchanged)
//   - all offline non-unreachable     → red ("$(error)") + testing.iconFailed
import './mock-vscode';
import { mockStatusBarItems, resetMockState, type MockStatusBarItem } from './mock-vscode';
import * as assert from 'node:assert';
import { describe, it, beforeEach, afterEach } from 'mocha';
import { McpStatusBar } from '../status-bar';
import { ServerDataCache } from '../server-data-cache';
import type { ServerView } from '../types';

function makeClient(servers: ServerView[]) {
    return {
        client: {
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
        },
    };
}

function colorId(item: MockStatusBarItem): string | undefined {
    const c = item.color;
    if (c === undefined || c === null) { return undefined; }
    return (c as { id: string }).id;
}

function latestItem(): MockStatusBarItem {
    return mockStatusBarItems[mockStatusBarItems.length - 1];
}

describe('McpStatusBar — StatusUnreachable buckets', () => {
    let cache: ServerDataCache;
    let bar: McpStatusBar;

    beforeEach(() => {
        resetMockState();
    });

    afterEach(() => {
        bar?.dispose();
        cache?.dispose();
    });

    it('all servers unreachable → yellow warning, NOT red error', async () => {
        // The whole point of StatusUnreachable: a VPN-off / network-partition
        // scenario must read as "your network is down, gateway knows, no
        // doom loop" — NOT as "everything is broken, panic". Yellow not red.
        const servers: ServerView[] = [
            { name: 'pdap-docs', status: 'unreachable', transport: 'http', restart_count: 0 },
            { name: 'pdap-prod', status: 'unreachable', transport: 'http', restart_count: 0 },
        ];
        const { client } = makeClient(servers);
        cache = new ServerDataCache(client as any);
        bar = new McpStatusBar(cache);
        await cache.refresh();

        const item = latestItem();
        assert.equal(item.text, '$(warning) MCP: 0/2 (offline)',
            'all-unreachable text must read "0/N (offline)" with warning icon — NOT $(error) and NOT a bare "0/N" partial label');
        assert.equal(colorId(item), 'list.warningForeground',
            'all-unreachable color must be list.warningForeground (yellow warning), NOT testing.iconFailed (red error)');
    });

    it('partial running + unreachable → yellow (list.warningForeground), not generic yellow', async () => {
        // Mixed state must match the dominant per-row icon color so the tree
        // icon and the aggregate badge agree visually. When at least one row
        // is unreachable, the aggregate switches from
        // notificationsWarningIcon.foreground (generic warning) to
        // list.warningForeground (the per-row unreachable color).
        const servers: ServerView[] = [
            { name: 'a', status: 'running', transport: 'stdio', restart_count: 0 },
            { name: 'b', status: 'running', transport: 'http', restart_count: 0 },
            { name: 'c', status: 'unreachable', transport: 'http', restart_count: 0 },
        ];
        const { client } = makeClient(servers);
        cache = new ServerDataCache(client as any);
        bar = new McpStatusBar(cache);
        await cache.refresh();

        const item = latestItem();
        assert.equal(item.text, '$(warning) MCP: 2/3',
            'partial state text format must remain "$(warning) MCP: N/M" — only the COLOR changes');
        assert.equal(colorId(item), 'list.warningForeground',
            'partial state including unreachable rows must use list.warningForeground (matches per-row badge color)');
    });

    it('partial running WITHOUT any unreachable preserves the generic notification yellow', async () => {
        // Pin the unchanged path: an "ordinary" partial state (some running,
        // some degraded/error/stopped, NO unreachable) keeps the historic
        // notificationsWarningIcon.foreground color. Guards against a
        // future refactor that consolidates both yellow paths.
        const servers: ServerView[] = [
            { name: 'a', status: 'running', transport: 'stdio', restart_count: 0 },
            { name: 'b', status: 'degraded', transport: 'http', restart_count: 0 },
            { name: 'c', status: 'error', transport: 'http', restart_count: 0 },
        ];
        const { client } = makeClient(servers);
        cache = new ServerDataCache(client as any);
        bar = new McpStatusBar(cache);
        await cache.refresh();

        const item = latestItem();
        assert.equal(item.text, '$(warning) MCP: 1/3');
        assert.equal(colorId(item), 'notificationsWarningIcon.foreground',
            'partial state with NO unreachable rows must keep the historic notificationsWarningIcon.foreground theme color');
    });

    it('all offline + NO unreachable still uses red error color', async () => {
        // Pin the unchanged red path. Without unreachable rows, an
        // all-offline state is genuinely broken (stopped/error/disabled
        // means "running into bugs / configuration drift", not "network
        // partition"). Red is correct here.
        const servers: ServerView[] = [
            { name: 'a', status: 'stopped', transport: 'stdio', restart_count: 0 },
            { name: 'b', status: 'error', transport: 'http', restart_count: 0 },
        ];
        const { client } = makeClient(servers);
        cache = new ServerDataCache(client as any);
        bar = new McpStatusBar(cache);
        await cache.refresh();

        const item = latestItem();
        assert.equal(item.text, '$(error) MCP: 0/2');
        assert.equal(colorId(item), 'testing.iconFailed');
    });

    it('all running takes precedence over an empty unreachable check', async () => {
        // Defensive: when every server is running, the all-running branch
        // wins regardless of the unreachable accumulators. Pin that
        // refactoring the unreachable branch does not break the green path.
        const servers: ServerView[] = [
            { name: 'a', status: 'running', transport: 'stdio', restart_count: 0 },
            { name: 'b', status: 'running', transport: 'http', restart_count: 0 },
        ];
        const { client } = makeClient(servers);
        cache = new ServerDataCache(client as any);
        bar = new McpStatusBar(cache);
        await cache.refresh();

        const item = latestItem();
        assert.equal(item.text, '$(check) MCP: 2/2');
        assert.equal(colorId(item), 'testing.iconPassed');
    });
});
