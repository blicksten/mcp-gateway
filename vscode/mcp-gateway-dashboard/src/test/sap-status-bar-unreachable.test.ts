// Unit tests for SapStatusBar's StatusUnreachable handling. Closes the
// MEDIUM gap from cd931db commit message (review-feature-64baca01):
// "TS UI unit tests for sap-status-bar glyph" was listed under NOT-YET-DONE.
//
// The SAP status bar rolls 'unreachable' into the same yellow bucket as
// 'degraded' — both surface a "⚠" warning glyph and trigger the
// notificationsWarningIcon.foreground color. This keeps the SAP-level UX
// consistent with the per-row badge: an unreachable backend in a SAP
// composite must NOT escalate the SAP badge to red error.
import './mock-vscode';
import { strict as assert } from 'node:assert';
import { describe, it, afterEach } from 'mocha';
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

describe('SapStatusBar — StatusUnreachable handling', () => {
    let cache: ServerDataCache;
    let bar: SapStatusBar;

    afterEach(() => {
        bar?.dispose();
        cache?.dispose();
        resetMockState();
    });

    it('shows the warning glyph (⚠) for an unreachable SAP system', async () => {
        // SAP system VSP backend in StatusUnreachable. The SAP status bar
        // renders the system label followed by a per-status dot/glyph from
        // STATUS_DOTS. The unreachable glyph is "⚠" (warning sign) —
        // same as degraded, deliberately, so the SAP-level visual language
        // says "needs attention but not broken".
        cache = new ServerDataCache(createMockClient([
            { name: 'vsp-DEV', status: 'unreachable', transport: 'http', restart_count: 0 },
        ]) as any);
        await cache.refresh();
        bar = new SapStatusBar(cache);

        const item = mockStatusBarItems[mockStatusBarItems.length - 1];
        assert.equal(item.visible, true,
            'SAP status bar must be visible when there is at least one SAP system');
        assert.ok(item.text.includes('⚠'),
            `SAP label for an unreachable system must include the warning glyph "⚠"; got: ${item.text}`);
    });

    it('rolls unreachable into the degraded yellow bucket (notificationsWarningIcon.foreground)', async () => {
        // The color logic at sap-status-bar.ts:50 deliberately treats
        // 'unreachable' as equivalent to 'degraded' for color selection.
        // This test pins that semantic — a single unreachable component
        // must surface as the SAP-level yellow badge, NOT promote to red
        // (error) and NOT stay green (running).
        cache = new ServerDataCache(createMockClient([
            { name: 'vsp-DEV', status: 'running', transport: 'http', restart_count: 0 },
            { name: 'sap-gui-DEV', status: 'unreachable', transport: 'http', restart_count: 0 },
        ]) as any);
        await cache.refresh();
        bar = new SapStatusBar(cache);

        const item = mockStatusBarItems[mockStatusBarItems.length - 1];
        assert.equal(item.backgroundColor, undefined,
            'SAP status bar must not raise a background color for an unreachable+running mix (degraded-style yellow only)');
        assert.ok(item.color);
        assert.equal((item.color as any).id, 'notificationsWarningIcon.foreground',
            'unreachable must roll into the degraded yellow bucket — NOT testing.iconFailed (red) and NOT testing.iconPassed (green)');
    });

    it('error trumps unreachable (still uses red iconFailed when both are present)', async () => {
        // Pin the precedence rule at sap-status-bar.ts:53-54: a system with
        // an error component takes the red testing.iconFailed color even if
        // another component is unreachable. This is correct — error means
        // protocol-layer failure that needs investigation, regardless of
        // network reachability of siblings.
        cache = new ServerDataCache(createMockClient([
            { name: 'vsp-DEV', status: 'error', transport: 'http', restart_count: 0 },
            { name: 'sap-gui-DEV', status: 'unreachable', transport: 'http', restart_count: 0 },
        ]) as any);
        await cache.refresh();
        bar = new SapStatusBar(cache);

        const item = mockStatusBarItems[mockStatusBarItems.length - 1];
        assert.ok(item.color);
        assert.equal((item.color as any).id, 'testing.iconFailed',
            'error must take precedence over unreachable for the SAP badge (red beats yellow when both are present)');
    });

    it('all running stays green even when no unreachable rows exist', async () => {
        // Sanity check that the new unreachable branch did not regress
        // the all-running green path.
        cache = new ServerDataCache(createMockClient([
            { name: 'vsp-DEV', status: 'running', transport: 'http', restart_count: 0 },
            { name: 'sap-gui-DEV', status: 'running', transport: 'http', restart_count: 0 },
        ]) as any);
        await cache.refresh();
        bar = new SapStatusBar(cache);

        const item = mockStatusBarItems[mockStatusBarItems.length - 1];
        assert.ok(item.color);
        assert.equal((item.color as any).id, 'testing.iconPassed',
            'all-running SAP composite must stay green');
    });
});
