import { strict as assert } from 'node:assert';
import { describe, it } from 'mocha';

import {
	applyConfigOverride,
	colorForMode,
	computeSessionStatus,
	evaluateMode,
	ingestHeartbeat,
	type ExternalFacts,
} from '../claude-code/status';
import {
	CONFIG,
	type Heartbeat,
	type McpSessionState,
	type SessionTrack,
} from '../claude-code/types';

function emptyTrack(sessionId = 'sess-A'): SessionTrack {
	return {
		session_id: sessionId,
		consecutiveReconnectErrors: 0,
		consecutiveNonReadyHighRetry: 0,
	};
}

function makeHeartbeat(overrides: Partial<Heartbeat> = {}): Heartbeat {
	const base: Heartbeat = {
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
		mcp_session_state: 'ready' as McpSessionState,
		ts: Date.now(),
		received_at: new Date().toISOString(),
	};
	return { ...base, ...overrides };
}

function healthyFacts(overrides: Partial<ExternalFacts> = {}): ExternalFacts {
	const base: ExternalFacts = {
		patchInstalled: true,
		patchStale: false,
		pluginInstalled: true,
		gatewayReachable: true,
		tokenRotationDriftMs: null,
		ccVersion: '2.1.114',
		altEVerifiedVersions: ['2.1.114'],
		maxAltEVersion: '2.1.114',
		corsReachable: true,
		anyRecentHeartbeat: true,
	};
	return { ...base, ...overrides };
}

describe('Claude Code failure-mode state machine', () => {
	describe('ingestHeartbeat — P4-05 Mode M counter reset', () => {
		it('increments consecutive errors on last_reconnect_ok=false', () => {
			let track = emptyTrack();
			track = ingestHeartbeat(track, makeHeartbeat({ last_reconnect_ok: false }));
			track = ingestHeartbeat(track, makeHeartbeat({ last_reconnect_ok: false }));
			assert.equal(track.consecutiveReconnectErrors, 2);
		});

		it('resets counter to 0 on a single success', () => {
			let track = emptyTrack();
			track = ingestHeartbeat(track, makeHeartbeat({ last_reconnect_ok: false }));
			track = ingestHeartbeat(track, makeHeartbeat({ last_reconnect_ok: false }));
			assert.equal(track.consecutiveReconnectErrors, 2);
			track = ingestHeartbeat(track, makeHeartbeat({ last_reconnect_ok: true }));
			assert.equal(track.consecutiveReconnectErrors, 0);
		});

		it('leaves counter unchanged on idle (null) heartbeats', () => {
			let track = emptyTrack();
			track = ingestHeartbeat(track, makeHeartbeat({ last_reconnect_ok: false }));
			track = ingestHeartbeat(track, makeHeartbeat({ last_reconnect_ok: false }));
			track = ingestHeartbeat(track, makeHeartbeat({ last_reconnect_ok: null }));
			assert.equal(track.consecutiveReconnectErrors, 2, 'idle must be neutral');
			track = ingestHeartbeat(track, makeHeartbeat({ last_reconnect_ok: false }));
			assert.equal(track.consecutiveReconnectErrors, 3);
		});
	});

	describe('ingestHeartbeat — P4-09 Mode D saturation gate', () => {
		it('advances saturation counter only while retry_count >= threshold AND not-ready', () => {
			let track = emptyTrack();
			track = ingestHeartbeat(
				track,
				makeHeartbeat({
					fiber_walk_retry_count: 5,
					mcp_session_state: 'lost',
				}),
			);
			track = ingestHeartbeat(
				track,
				makeHeartbeat({
					fiber_walk_retry_count: 5,
					mcp_session_state: 'lost',
				}),
			);
			assert.equal(track.consecutiveNonReadyHighRetry, 2);
		});

		it('does NOT advance when retry_count is below threshold', () => {
			let track = emptyTrack();
			track = ingestHeartbeat(
				track,
				makeHeartbeat({ fiber_walk_retry_count: 4, mcp_session_state: 'lost' }),
			);
			assert.equal(track.consecutiveNonReadyHighRetry, 0);
		});

		it('resets to 0 on a single ready recovery frame', () => {
			let track = emptyTrack();
			for (let i = 0; i < 5; i++) {
				track = ingestHeartbeat(
					track,
					makeHeartbeat({ fiber_walk_retry_count: 10, mcp_session_state: 'lost' }),
				);
			}
			assert.equal(track.consecutiveNonReadyHighRetry, 5);
			track = ingestHeartbeat(
				track,
				makeHeartbeat({ mcp_session_state: 'ready', fiber_walk_retry_count: 5 }),
			);
			assert.equal(track.consecutiveNonReadyHighRetry, 0);
		});
	});

	describe('evaluateMode — severity priority', () => {
		it('H (gateway down) has highest priority', () => {
			const track = emptyTrack();
			const mode = evaluateMode(track, healthyFacts({ gatewayReachable: false, patchInstalled: false }));
			assert.equal(mode, 'H');
		});

		it('G (no plugin) outranks A (patch missing)', () => {
			const track = emptyTrack();
			const mode = evaluateMode(track, healthyFacts({ pluginInstalled: false, patchInstalled: false }));
			assert.equal(mode, 'G');
		});

		it('returns none for a fully-healthy state with ready heartbeat', () => {
			let track = emptyTrack();
			track = ingestHeartbeat(track, makeHeartbeat({ last_reconnect_ok: true, last_reconnect_latency_ms: 5000 }));
			const mode = evaluateMode(track, healthyFacts());
			assert.equal(mode, 'none');
		});
	});

	describe('evaluateMode — Mode L (latency) P4-06 boundary', () => {
		it('does not arm at LATENCY_WARN_MS - 1', () => {
			let track = emptyTrack();
			track = ingestHeartbeat(
				track,
				makeHeartbeat({
					last_reconnect_ok: true,
					last_reconnect_latency_ms: CONFIG.LATENCY_WARN_MS - 1,
				}),
			);
			assert.notEqual(evaluateMode(track, healthyFacts()), 'L');
		});

		it('arms at exactly LATENCY_WARN_MS', () => {
			let track = emptyTrack();
			track = ingestHeartbeat(
				track,
				makeHeartbeat({
					last_reconnect_ok: true,
					last_reconnect_latency_ms: CONFIG.LATENCY_WARN_MS,
				}),
			);
			assert.equal(evaluateMode(track, healthyFacts()), 'L');
		});

		it('arms above LATENCY_WARN_MS', () => {
			let track = emptyTrack();
			track = ingestHeartbeat(
				track,
				makeHeartbeat({
					last_reconnect_ok: true,
					last_reconnect_latency_ms: CONFIG.LATENCY_WARN_MS + 1,
				}),
			);
			assert.equal(evaluateMode(track, healthyFacts()), 'L');
		});

		it('does not arm on idle heartbeat (last_reconnect_ok=null) even if latency field is large', () => {
			let track = emptyTrack();
			track = ingestHeartbeat(
				track,
				makeHeartbeat({
					last_reconnect_ok: null,
					last_reconnect_latency_ms: 0, // FROZEN: 0 when no attempt
				}),
			);
			assert.notEqual(evaluateMode(track, healthyFacts()), 'L');
		});
	});

	describe('evaluateMode — Mode M (consecutive errors) P4-05', () => {
		it('arms at CONSECUTIVE_ERRORS_FAIL_THRESHOLD failures', () => {
			let track = emptyTrack();
			for (let i = 0; i < CONFIG.CONSECUTIVE_ERRORS_FAIL_THRESHOLD; i++) {
				track = ingestHeartbeat(track, makeHeartbeat({ last_reconnect_ok: false, last_reconnect_error: 'boom' }));
			}
			assert.equal(evaluateMode(track, healthyFacts()), 'M');
		});

		it('clears after one success (counter reset)', () => {
			let track = emptyTrack();
			for (let i = 0; i < CONFIG.CONSECUTIVE_ERRORS_FAIL_THRESHOLD; i++) {
				track = ingestHeartbeat(track, makeHeartbeat({ last_reconnect_ok: false }));
			}
			assert.equal(evaluateMode(track, healthyFacts()), 'M');
			track = ingestHeartbeat(track, makeHeartbeat({ last_reconnect_ok: true }));
			assert.notEqual(evaluateMode(track, healthyFacts()), 'M');
		});

		it('stays clear after reset until 3 new consecutive failures', () => {
			let track = emptyTrack();
			for (let i = 0; i < CONFIG.CONSECUTIVE_ERRORS_FAIL_THRESHOLD; i++) {
				track = ingestHeartbeat(track, makeHeartbeat({ last_reconnect_ok: false }));
			}
			track = ingestHeartbeat(track, makeHeartbeat({ last_reconnect_ok: true })); // reset
			track = ingestHeartbeat(track, makeHeartbeat({ last_reconnect_ok: false }));
			track = ingestHeartbeat(track, makeHeartbeat({ last_reconnect_ok: false }));
			assert.notEqual(evaluateMode(track, healthyFacts()), 'M', 'only 2 new failures after reset — must not arm');
		});

		it('idle heartbeats (null) are neutral — 2 false, 1 null, 1 false → still armed because 3 failures total', () => {
			let track = emptyTrack();
			track = ingestHeartbeat(track, makeHeartbeat({ last_reconnect_ok: false }));
			track = ingestHeartbeat(track, makeHeartbeat({ last_reconnect_ok: false }));
			track = ingestHeartbeat(track, makeHeartbeat({ last_reconnect_ok: null }));
			track = ingestHeartbeat(track, makeHeartbeat({ last_reconnect_ok: false }));
			assert.equal(evaluateMode(track, healthyFacts()), 'M');
		});
	});

	describe('evaluateMode — Mode D (fiber walk saturated) P4-09', () => {
		it('does NOT arm on fresh window with 1 heartbeat', () => {
			let track = emptyTrack();
			track = ingestHeartbeat(
				track,
				makeHeartbeat({ fiber_ok: false, fiber_walk_retry_count: 1, mcp_session_state: 'discovering' }),
			);
			assert.notEqual(evaluateMode(track, healthyFacts()), 'D');
		});

		it('does NOT arm when retry_count below threshold even across many heartbeats', () => {
			let track = emptyTrack();
			for (let i = 0; i < 10; i++) {
				track = ingestHeartbeat(
					track,
					makeHeartbeat({ fiber_walk_retry_count: 4, mcp_session_state: 'lost' }),
				);
			}
			assert.notEqual(evaluateMode(track, healthyFacts()), 'D');
		});

		it('arms at MODE_D_MIN_CONSECUTIVE_HEARTBEATS saturated heartbeats', () => {
			let track = emptyTrack();
			for (let i = 0; i < CONFIG.MODE_D_MIN_CONSECUTIVE_HEARTBEATS; i++) {
				track = ingestHeartbeat(
					track,
					makeHeartbeat({ fiber_walk_retry_count: 5, mcp_session_state: 'lost' }),
				);
			}
			assert.equal(evaluateMode(track, healthyFacts()), 'D');
		});

		it('clears on recovery heartbeat (fiber_ok + ready)', () => {
			let track = emptyTrack();
			for (let i = 0; i < 5; i++) {
				track = ingestHeartbeat(
					track,
					makeHeartbeat({ fiber_walk_retry_count: 10, mcp_session_state: 'lost' }),
				);
			}
			assert.equal(evaluateMode(track, healthyFacts()), 'D');
			track = ingestHeartbeat(
				track,
				makeHeartbeat({
					fiber_ok: true,
					mcp_method_ok: true,
					mcp_session_state: 'ready',
					fiber_walk_retry_count: 5,
				}),
			);
			assert.notEqual(evaluateMode(track, healthyFacts()), 'D');
		});
	});

	describe('evaluateMode — Mode K (token rotation)', () => {
		it('arms when auth.token mtime is newer than patched index.js', () => {
			const track = emptyTrack();
			assert.equal(evaluateMode(track, healthyFacts({ tokenRotationDriftMs: 60_000 })), 'K');
		});

		it('does not arm when drift is zero or negative', () => {
			const track = emptyTrack();
			assert.notEqual(evaluateMode(track, healthyFacts({ tokenRotationDriftMs: 0 })), 'K');
			assert.notEqual(evaluateMode(track, healthyFacts({ tokenRotationDriftMs: -1 })), 'K');
		});

		it('does not arm when token drift data is unavailable (null)', () => {
			const track = emptyTrack();
			assert.notEqual(evaluateMode(track, healthyFacts({ tokenRotationDriftMs: null })), 'K');
		});
	});

	describe('evaluateMode — Mode C (CC version unverified)', () => {
		it('arms when ccVersion not in alt_e_verified_versions', () => {
			const track = emptyTrack();
			const mode = evaluateMode(
				track,
				healthyFacts({ ccVersion: '2.6.1', altEVerifiedVersions: ['2.1.114'] }),
			);
			assert.equal(mode, 'C');
		});

		it('does not arm when ccVersion IS in the verified list', () => {
			const track = emptyTrack();
			const mode = evaluateMode(
				track,
				healthyFacts({ ccVersion: '2.1.114', altEVerifiedVersions: ['2.1.114', '2.5.8'] }),
			);
			assert.notEqual(mode, 'C');
		});

		it('does not arm when the verified list is empty (compat matrix missing)', () => {
			const track = emptyTrack();
			const mode = evaluateMode(
				track,
				healthyFacts({ ccVersion: '2.6.1', altEVerifiedVersions: [] }),
			);
			assert.notEqual(mode, 'C');
		});
	});

	describe('evaluateMode — no Mode E regression', () => {
		it('never emits an E-class mode for any heartbeat/fact combination', () => {
			const scenarios: Array<Partial<ExternalFacts>> = [
				{ patchInstalled: false },
				{ gatewayReachable: false },
				{ pluginInstalled: false },
				{ corsReachable: false },
				{ tokenRotationDriftMs: 100 },
				{ ccVersion: '99.0.0', altEVerifiedVersions: ['2.1.114'] },
			];
			for (const s of scenarios) {
				const track = emptyTrack();
				const mode = evaluateMode(track, healthyFacts(s));
				assert.notEqual(mode, 'E' as unknown, 'Mode E was obsoleted under Alt-E');
			}
		});
	});

	describe('colorForMode', () => {
		it('maps green to none', () => {
			assert.equal(colorForMode('none'), 'green');
		});
		it('maps yellow to I, C, L', () => {
			assert.equal(colorForMode('I'), 'yellow');
			assert.equal(colorForMode('C'), 'yellow');
			assert.equal(colorForMode('L'), 'yellow');
		});
		it('maps RED to H, G, A, B, D, F, K, M, J', () => {
			for (const m of ['H', 'G', 'A', 'B', 'D', 'F', 'K', 'M'] as const) {
				assert.equal(colorForMode(m), 'red', `mode ${m} should be red`);
			}
		});
	});

	describe('computeSessionStatus — end-to-end', () => {
		it('composes a GREEN status for healthy state', () => {
			let track = emptyTrack();
			track = ingestHeartbeat(track, makeHeartbeat({ last_reconnect_ok: true }));
			const status = computeSessionStatus(track, healthyFacts(), [track.lastHeartbeat as Heartbeat]);
			assert.equal(status.color, 'green');
			assert.equal(status.mode, 'none');
			assert.ok(status.banner.includes('Auto-reload is working'));
		});

		it('composes Mode L with rendered latency in seconds', () => {
			let track = emptyTrack();
			track = ingestHeartbeat(
				track,
				makeHeartbeat({ last_reconnect_ok: true, last_reconnect_latency_ms: 45_000 }),
			);
			const status = computeSessionStatus(track, healthyFacts(), [track.lastHeartbeat as Heartbeat]);
			assert.equal(status.mode, 'L');
			assert.equal(status.color, 'yellow');
			assert.ok(status.banner.includes('45s'), `banner was: ${status.banner}`);
		});

		it('composes Mode M with the last seen error string', () => {
			let track = emptyTrack();
			for (let i = 0; i < 3; i++) {
				track = ingestHeartbeat(
					track,
					makeHeartbeat({ last_reconnect_ok: false, last_reconnect_error: 'fetch failed: ECONNREFUSED' }),
				);
			}
			const status = computeSessionStatus(track, healthyFacts(), [track.lastHeartbeat as Heartbeat]);
			assert.equal(status.mode, 'M');
			assert.ok(status.banner.includes('ECONNREFUSED'), `banner was: ${status.banner}`);
		});
	});
});

describe('applyConfigOverride — SP4-L2 boundary', () => {
	it('accepts LATENCY_WARN_MS at min boundary (5000)', () => {
		const { effective, rejected } = applyConfigOverride({ LATENCY_WARN_MS: 5000 });
		assert.equal(effective.LATENCY_WARN_MS, 5000);
		assert.equal(rejected.length, 0);
	});

	it('rejects LATENCY_WARN_MS below min (4999)', () => {
		const { effective, rejected } = applyConfigOverride({ LATENCY_WARN_MS: 4999 });
		assert.equal(effective.LATENCY_WARN_MS, CONFIG.LATENCY_WARN_MS, 'must stay at compiled default');
		assert.equal(rejected.length, 1);
		assert.equal(rejected[0].key, 'LATENCY_WARN_MS');
	});

	it('rejects DEBOUNCE_WINDOW_MS below 2000', () => {
		const { rejected } = applyConfigOverride({ DEBOUNCE_WINDOW_MS: 1999 });
		assert.equal(rejected.length, 1);
		assert.equal(rejected[0].key, 'DEBOUNCE_WINDOW_MS');
	});

	it('accepts DEBOUNCE_WINDOW_MS at min (2000)', () => {
		const { effective, rejected } = applyConfigOverride({ DEBOUNCE_WINDOW_MS: 2000 });
		assert.equal(effective.DEBOUNCE_WINDOW_MS, 2000);
		assert.equal(rejected.length, 0);
	});

	it('rejects CONSECUTIVE_ERRORS_FAIL_THRESHOLD = 1 (below min 2)', () => {
		const { rejected } = applyConfigOverride({ CONSECUTIVE_ERRORS_FAIL_THRESHOLD: 1 });
		assert.equal(rejected.length, 1);
	});

	it('accepts CONSECUTIVE_ERRORS_FAIL_THRESHOLD = 2', () => {
		const { effective, rejected } = applyConfigOverride({ CONSECUTIVE_ERRORS_FAIL_THRESHOLD: 2 });
		assert.equal(effective.CONSECUTIVE_ERRORS_FAIL_THRESHOLD, 2);
		assert.equal(rejected.length, 0);
	});

	it('passes through when override is undefined', () => {
		const { effective, rejected } = applyConfigOverride(undefined);
		assert.equal(effective.LATENCY_WARN_MS, CONFIG.LATENCY_WARN_MS);
		assert.equal(rejected.length, 0);
	});

	it('ignores unknown keys silently', () => {
		const override = { LATENCY_WARN_MS: 10_000, unknown_key: 42 } as unknown as Partial<
			Record<keyof typeof CONFIG, number>
		>;
		const { effective, rejected } = applyConfigOverride(override);
		assert.equal(effective.LATENCY_WARN_MS, 10_000);
		assert.equal(rejected.length, 0);
	});
});
