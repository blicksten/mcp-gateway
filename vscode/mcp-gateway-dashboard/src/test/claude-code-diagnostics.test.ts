import { strict as assert } from 'node:assert';
import { describe, it } from 'mocha';

import {
	buildDiagnosticsReport,
	extractAltEMetrics,
	type DiagnosticsInput,
} from '../claude-code/diagnostics';
import type { Heartbeat, SessionStatus } from '../claude-code/types';

function hb(overrides: Partial<Heartbeat>): Heartbeat {
	return {
		session_id: 'sess-A',
		patch_version: '1.0.0',
		cc_version: '2.1.114',
		vscode_version: '1.90.0',
		fiber_ok: true,
		mcp_method_ok: true,
		mcp_method_fiber_depth: 2,
		last_reconnect_latency_ms: 0,
		last_reconnect_ok: null,
		last_reconnect_error: '',
		pending_actions_inflight: 0,
		fiber_walk_retry_count: 0,
		mcp_session_state: 'ready',
		ts: Date.now(),
		received_at: new Date().toISOString(),
		...overrides,
	};
}

describe('extractAltEMetrics', () => {
	it('computes p50 / p95 over non-idle latencies only', () => {
		const hbs = [
			hb({ last_reconnect_ok: true, last_reconnect_latency_ms: 1000 }),
			hb({ last_reconnect_ok: true, last_reconnect_latency_ms: 2000 }),
			hb({ last_reconnect_ok: true, last_reconnect_latency_ms: 3000 }),
			hb({ last_reconnect_ok: true, last_reconnect_latency_ms: 4000 }),
			hb({ last_reconnect_ok: true, last_reconnect_latency_ms: 5000 }),
			// idle (latency=0) excluded from percentiles
			hb({ last_reconnect_ok: null, last_reconnect_latency_ms: 0 }),
		];
		const m = extractAltEMetrics(hbs);
		assert.equal(m.latencyP50, 3000);
		// nearest-rank 95th of 5 samples is index ceil(0.95*5)-1 = 4 → 5000
		assert.equal(m.latencyP95, 5000);
	});

	it('counts ok true/false/null buckets', () => {
		const hbs = [
			hb({ last_reconnect_ok: true }),
			hb({ last_reconnect_ok: true }),
			hb({ last_reconnect_ok: false, last_reconnect_error: 'boom' }),
			hb({ last_reconnect_ok: null }),
		];
		const m = extractAltEMetrics(hbs);
		assert.equal(m.okTrue, 2);
		assert.equal(m.okFalse, 1);
		assert.equal(m.okNull, 1);
	});

	it('deduplicates recent errors', () => {
		const hbs = [
			hb({ last_reconnect_ok: false, last_reconnect_error: 'boom' }),
			hb({ last_reconnect_ok: false, last_reconnect_error: 'boom' }),
			hb({ last_reconnect_ok: false, last_reconnect_error: 'ECONNREFUSED' }),
		];
		const m = extractAltEMetrics(hbs);
		assert.equal(m.recentErrors.length, 2);
		assert.ok(m.recentErrors.includes('boom'));
		assert.ok(m.recentErrors.includes('ECONNREFUSED'));
	});

	it('returns null percentiles when no non-idle samples', () => {
		const hbs = [hb({ last_reconnect_ok: null })];
		const m = extractAltEMetrics(hbs);
		assert.equal(m.latencyP50, null);
		assert.equal(m.latencyP95, null);
	});

	it('fiberDepthHistory preserves newest-first order, capped at 5', () => {
		const hbs = [
			hb({ mcp_method_fiber_depth: 2 }),
			hb({ mcp_method_fiber_depth: 3 }),
			hb({ mcp_method_fiber_depth: 4 }),
			hb({ mcp_method_fiber_depth: 5 }),
			hb({ mcp_method_fiber_depth: 6 }),
			hb({ mcp_method_fiber_depth: 7 }), // should be dropped
		];
		const m = extractAltEMetrics(hbs);
		assert.deepEqual(m.fiberDepthHistory, [2, 3, 4, 5, 6]);
	});
});

describe('buildDiagnosticsReport', () => {
	const session: SessionStatus = {
		session_id: 'sess-A',
		color: 'green',
		mode: 'none',
		banner: '✓ Auto-reload is working',
		recentHeartbeats: [hb({ last_reconnect_ok: true, last_reconnect_latency_ms: 5000 })],
	};

	const baseInput: DiagnosticsInput = {
		platform: 'win32',
		vscodeVersion: '1.90.0',
		gatewayVersion: '1.6.0',
		ccVersion: '2.1.114',
		pluginStatus: { installed: true, location: '/home/alice/.claude/plugins/...', entries: 3 },
		patchStatus: { installed: true, version: '1.0.0', hasBackup: true },
		compatMatrix: {
			min: '2.0.0',
			max_tested: '2.5.8',
			known_broken: [],
			last_verified: '2026-04-21',
			alt_e_verified_versions: ['2.1.114'],
			observed_fiber_depths: { '2.1.114': 2 },
			max_accepted_fiber_depth: 80,
			observed_reconnect_latency_ms_p50: 5400,
			observed_reconnect_latency_note: 'live-verified',
		},
		sessions: [session],
	};

	it('includes Alt-E metric fields in the output (T16.5.7 contract)', () => {
		const report = buildDiagnosticsReport(baseInput);
		assert.ok(report.includes('mcp_method_fiber_depth'), 'depth history missing');
		assert.ok(report.includes('last_reconnect_latency_ms p50/p95'), 'p50/p95 missing');
		assert.ok(report.includes('last_reconnect_ok count'), 'ok counter missing');
	});

	it('flags current CC version as verified when present in the matrix', () => {
		const report = buildDiagnosticsReport(baseInput);
		assert.ok(report.includes('Current CC version (2.1.114) verified: yes'));
	});

	it('flags current CC version as unverified when missing from the matrix', () => {
		const report = buildDiagnosticsReport({
			...baseInput,
			ccVersion: '2.6.1',
		});
		assert.ok(report.includes('Current CC version (2.6.1) verified: no'));
	});

	it('reports compat matrix unavailable when null', () => {
		const report = buildDiagnosticsReport({ ...baseInput, compatMatrix: null });
		assert.ok(report.includes('not available'));
		assert.ok(report.includes('503 or unreachable'));
	});

	it('emits "no active sessions" when sessions list is empty', () => {
		const report = buildDiagnosticsReport({ ...baseInput, sessions: [] });
		assert.ok(report.includes('no active sessions'));
	});

	it('includes the failure trace when provided', () => {
		const report = buildDiagnosticsReport({
			...baseInput,
			failureTrace: 'Error: apply-mcp-gateway.sh exited 2\n  at ...',
		});
		assert.ok(report.includes('### Failure Trace'));
		assert.ok(report.includes('apply-mcp-gateway.sh exited 2'));
	});

	it('omits the Failure Trace section when absent', () => {
		const report = buildDiagnosticsReport(baseInput);
		assert.ok(!report.includes('### Failure Trace'));
	});

	it('embeds the report URL when provided', () => {
		const report = buildDiagnosticsReport({
			...baseInput,
			reportUrl: 'https://github.com/example/mcp-gateway/issues/new',
		});
		assert.ok(report.includes('### Report This'));
		assert.ok(report.includes('github.com/example/mcp-gateway/issues/new'));
	});
});
