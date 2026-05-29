// Unit tests for BackendItem's StatusUnreachable rendering. Closes the
// MEDIUM gap from cd931db commit message (review-feature-64baca01):
// "TS UI unit tests for backend-item icon" was listed under NOT-YET-DONE.
//
// The Unreachable state is a stable "yellow warning" badge — same theme
// color as Degraded — but with a different operator-facing description
// suffix ("· host offline (slow-polling)") so the badge reads as "your
// network/VPN is down" rather than "a backend bug". These assertions pin
// both the icon contract (id + theme color) and the description suffix.
import './mock-vscode';
import * as assert from 'node:assert';
import { describe, it } from 'mocha';
import { BackendItem } from '../backend-item';
import type { ServerView } from '../types';

function makeServer(overrides: Partial<ServerView> = {}): ServerView {
    return {
        name: 'pdap-docs',
        status: 'running',
        transport: 'http',
        restart_count: 0,
        ...overrides,
    };
}

describe('BackendItem — StatusUnreachable rendering', () => {
    it('uses warning icon (yellow warning triangle, list.warningForeground)', () => {
        const item = new BackendItem(makeServer({ status: 'unreachable' }));
        const icon = item.iconPath as { id: string; color?: { id: string } };
        assert.equal(icon.id, 'warning',
            'unreachable backend must use the warning icon id (same as degraded), NOT debug-disconnect / error / spinner');
        assert.equal(icon.color?.id, 'list.warningForeground',
            'unreachable badge color must match degraded theme color (list.warningForeground) so the visual cue says "needs attention", not "broken"');
    });

    it('appends " · host offline (slow-polling)" to the description', () => {
        const item = new BackendItem(makeServer({ status: 'unreachable' }));
        assert.equal(typeof item.description, 'string', 'description must be a string');
        const desc = item.description as string;
        assert.ok(desc.includes('host offline'),
            `description must mention "host offline" so operator reads "your network", not "gateway broken"; got: ${desc}`);
        assert.ok(desc.includes('slow-polling'),
            `description must mention "slow-polling" so operator understands gateway is patiently rechecking; got: ${desc}`);
    });

    it('preserves the transport prefix in the unreachable description', () => {
        const item = new BackendItem(makeServer({
            status: 'unreachable',
            transport: 'http',
        }));
        const desc = item.description as string;
        assert.ok(desc.startsWith('http'),
            `transport prefix must come first; got: ${desc}`);
        assert.ok(desc.endsWith('host offline (slow-polling)'),
            `host-offline suffix must be the trailing fragment; got: ${desc}`);
    });

    it('does NOT show the host-offline suffix for a healthy running backend', () => {
        const item = new BackendItem(makeServer({ status: 'running' }));
        const desc = item.description as string;
        assert.ok(!desc.includes('host offline'),
            `running backend must not carry the unreachable description; got: ${desc}`);
        assert.ok(!desc.includes('slow-polling'),
            `running backend must not mention slow-polling; got: ${desc}`);
    });

    it('does NOT show the host-offline suffix for a degraded backend', () => {
        // Degraded and Unreachable share the icon + color but have different
        // descriptions: degraded is a protocol-level failure (HTTP 4xx/5xx,
        // bad ping), unreachable is TCP-level (host down). The suffix is the
        // ONLY visual differentiator between the two — pin this so a future
        // refactor that consolidates description-rendering by color doesn't
        // silently collapse them.
        const item = new BackendItem(makeServer({ status: 'degraded' }));
        const desc = item.description as string;
        assert.ok(!desc.includes('host offline'),
            `degraded backend must not show the unreachable suffix; got: ${desc}`);
    });

    it('stale=true overrides unreachable rendering with the gateway-offline grey icon', () => {
        // When the dashboard itself cannot reach the gateway, every backend
        // becomes "untrusted last-known state". The stale-icon path takes
        // priority over status-specific icons because we cannot confirm
        // any state. The unreachable status is no exception.
        const item = new BackendItem(makeServer({ status: 'unreachable' }), true);
        const icon = item.iconPath as { id: string; color?: { id: string } };
        assert.equal(icon.id, 'debug-disconnect',
            'stale gateway view must show debug-disconnect icon regardless of last-known status (including unreachable)');
        assert.equal(icon.color?.id, 'disabledForeground',
            'stale icon must be greyed out via disabledForeground');
    });

    it('stale=true appends " · offline" (NOT host-offline) for any status', () => {
        const item = new BackendItem(makeServer({ status: 'unreachable' }), true);
        const desc = item.description as string;
        assert.ok(desc.includes('offline'),
            `stale description must include "offline" gateway suffix; got: ${desc}`);
        assert.ok(!desc.includes('host offline (slow-polling)'),
            `stale path must NOT use the per-backend host-offline wording (the gateway itself is offline, not just the host); got: ${desc}`);
    });
});
