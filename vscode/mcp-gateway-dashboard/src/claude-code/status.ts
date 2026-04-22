// Failure-mode state machine for the Claude Code Integration panel.
//
// Implements T16.5.5 (13 failure modes) + P4-05 counter reset + P4-06
// boundary rules + P4-09 Mode D saturation gate. The logic is a pure
// function of a heartbeat stream + external facts (patch installed?
// gateway reachable? CC version compat matrix) so it is fully unit-
// testable without spinning up a webview.
//
// Mode `E` (`reload-plugins` command missing) was obsoleted under Alt-E;
// the switch in evaluateMode returns `'none'` for any condition that used
// to produce it. Tests assert no E-class message leaks through.

import {
	CONFIG,
	FailureMode,
	Heartbeat,
	SessionStatus,
	SessionTrack,
	StatusColor,
} from './types';

/**
 * External facts the evaluator depends on. Sourced from the extension's
 * Node context (FS checks, REST polling results).
 */
export interface ExternalFacts {
	/** True when `<cc-webview-dir>/index.js` exists and contains the patch marker. */
	patchInstalled: boolean;
	/** True when the installed patch's version differs from what apply-mcp-gateway would write (needs VSCode reload). */
	patchStale: boolean;
	/** True when `claude plugin list --json` returned mcp-gateway. */
	pluginInstalled: boolean;
	/** True when `GET /api/v1/health` succeeded recently (within ~30 s). */
	gatewayReachable: boolean;
	/** Mtime of ~/.mcp-gateway/auth.token minus mtime of patched index.js, in ms. `null` when either file is missing. */
	tokenRotationDriftMs: number | null;
	/** Live Claude Code version read from VSCode or patch heartbeat. */
	ccVersion: string;
	/** alt_e_verified_versions array from the compat matrix. */
	altEVerifiedVersions: string[];
	/** Max CC version verified — `alt_e_verified_versions` sorted by semver, highest. */
	maxAltEVersion: string;
	/** CORS preflight last test result (null = never tested). */
	corsReachable: boolean | null;
	/** Heartbeats received for any session in the last 5 s. False when VSCode is idle. */
	anyRecentHeartbeat: boolean;
}

/**
 * Updates a per-session track with a new heartbeat. Keeps P4-05 +
 * P4-09 counters accurate across the heartbeat stream. Caller typically
 * holds a `Map<session_id, SessionTrack>` and calls ingest on each
 * incoming heartbeat.
 */
export function ingestHeartbeat(track: SessionTrack, hb: Heartbeat): SessionTrack {
	// P4-05: Mode M counter. true → reset to 0. null (idle) → neutral,
	// counter unchanged. false → +1.
	let errors = track.consecutiveReconnectErrors;
	if (hb.last_reconnect_ok === true) {
		errors = 0;
	} else if (hb.last_reconnect_ok === false) {
		errors = errors + 1;
	}
	// hb.last_reconnect_ok === null: neutral, leave errors unchanged.

	// P4-09: Mode D saturation gate. A "saturated" heartbeat is one with
	// fiber_walk_retry_count >= MODE_D_MIN_RETRY_COUNT AND
	// mcp_session_state !== 'ready'. Consecutive count advances only while
	// every incoming heartbeat stays saturated.
	const isSaturated =
		hb.fiber_walk_retry_count >= CONFIG.MODE_D_MIN_RETRY_COUNT &&
		hb.mcp_session_state !== 'ready';
	const nonReadyRetry = isSaturated
		? track.consecutiveNonReadyHighRetry + 1
		: 0; // one recovery frame clears Mode D immediately

	return {
		session_id: hb.session_id,
		consecutiveReconnectErrors: errors,
		consecutiveNonReadyHighRetry: nonReadyRetry,
		lastHeartbeat: hb,
	};
}

/**
 * Computes the failure mode for a single session from its tracked state +
 * external facts. Order of checks matters: the FIRST matching mode wins.
 * The order below encodes severity priority — RED modes are evaluated
 * before YELLOW modes so a red-light signal isn't shadowed by yellow one.
 */
export function evaluateMode(
	track: SessionTrack,
	facts: ExternalFacts,
): FailureMode {
	const hb = track.lastHeartbeat;

	// H — Gateway not running. Highest priority: everything else depends on it.
	if (!facts.gatewayReachable) {
		return 'H';
	}

	// G — No plugin installed. Activation flow must run first.
	if (!facts.pluginInstalled) {
		return 'G';
	}

	// A — Patch file missing. Required for Auto-reload to function.
	if (!facts.patchInstalled) {
		return 'A';
	}

	// B — Patch installed but stale (apply wrote new version but VSCode
	// still running the old one). Driven by patch_version mismatch vs
	// what apply script would write.
	if (facts.patchStale) {
		return 'B';
	}

	// K — Token rotation detected. Patched index.js carries a stale bearer.
	// Threshold: any drift > 0 means auth.token was written after the patch
	// (and therefore the inlined token is outdated).
	if (facts.tokenRotationDriftMs !== null && facts.tokenRotationDriftMs > 0) {
		return 'K';
	}

	// F — CORS blocks gateway. Explicit negative signal from last preflight.
	if (facts.corsReachable === false) {
		return 'F';
	}

	// D — Fiber walk failed AND session stayed non-ready across 3 heartbeats.
	// P4-09: prevents false-RED on a fresh window that hasn't opened /mcp yet.
	if (
		track.consecutiveNonReadyHighRetry >= CONFIG.MODE_D_MIN_CONSECUTIVE_HEARTBEATS
	) {
		return 'D';
	}

	// M — Consecutive reconnect errors. P4-05 counter-reset already applied
	// at ingest; we just threshold it here.
	if (track.consecutiveReconnectErrors >= CONFIG.CONSECUTIVE_ERRORS_FAIL_THRESHOLD) {
		return 'M';
	}

	// C — CC version not in alt_e_verified_versions (yellow advisory).
	if (
		facts.ccVersion !== '' &&
		facts.altEVerifiedVersions.length > 0 &&
		!facts.altEVerifiedVersions.includes(facts.ccVersion)
	) {
		return 'C';
	}

	// L — Reconnect latency above threshold. P4-06 boundary: >= LATENCY_WARN_MS
	// triggers; == fires; strictly less than does not; null-latency (idle)
	// does NOT arm. hb.last_reconnect_latency_ms is 0 when no reconnect
	// attempted since last heartbeat per FROZEN contract.
	if (
		hb !== undefined &&
		hb.last_reconnect_ok !== null &&
		hb.last_reconnect_latency_ms >= CONFIG.LATENCY_WARN_MS
	) {
		return 'L';
	}

	// I — VSCode idle. No heartbeats in the recent window.
	if (!facts.anyRecentHeartbeat) {
		return 'I';
	}

	return 'none';
}

/**
 * Color severity per mode. Mode M/K/D/A/B/G/H = RED, C/F/L = YELLOW,
 * I = YELLOW (idle is informational), none = GREEN.
 * F is RED in the UI because a blocked gateway renders everything else
 * broken — the spec says "RED" in T16.5.5 but the test matrix is what
 * we actually verify against.
 */
export function colorForMode(mode: FailureMode): StatusColor {
	switch (mode) {
		case 'none':
			return 'green';
		case 'I':
		case 'C':
		case 'L':
			return 'yellow';
		case 'A':
		case 'B':
		case 'D':
		case 'F':
		case 'G':
		case 'H':
		case 'K':
		case 'M':
			return 'red';
	}
}

/**
 * Renders the user-facing banner + action button for a given mode.
 * `action` may be undefined when the banner is informational (I, none).
 */
export function renderBanner(
	mode: FailureMode,
	facts: ExternalFacts,
	hb?: Heartbeat,
): { banner: string; action?: string } {
	switch (mode) {
		case 'none':
			return { banner: '✓ Auto-reload is working' };
		case 'A':
			return {
				banner: 'Patch not installed',
				action: 'Click ☑ to install patch',
			};
		case 'B':
			return {
				banner: 'Patch updated — VSCode reload required',
				action: "Ctrl+Shift+P → 'Developer: Reload Window'",
			};
		case 'C': {
			const max = facts.maxAltEVersion || '(none)';
			return {
				banner: `Claude Code v${facts.ccVersion} not in alt_e_verified_versions (last verified ${max}). Fiber walk may not locate reconnectMcpServer.`,
				action: 'Report success/failure on GitHub',
			};
		}
		case 'D':
			return {
				banner:
					'Claude Code internal API changed or /mcp panel never mounted in this window.',
				action: 'Open /mcp panel to trigger patch discovery, or revert to aggregate-only mode.',
			};
		case 'F':
			return {
				banner: 'Gateway unreachable from Claude Code webview.',
				action: 'Check gateway.allowed_origins setting.',
			};
		case 'G':
			return {
				banner: 'No Claude Code plugin installed',
				action: 'Click [Activate for Claude Code] first.',
			};
		case 'H':
			return {
				banner: 'mcp-gateway daemon not running',
				action: 'Start the daemon on port 8765.',
			};
		case 'I':
			return { banner: '⏸ Claude Code idle' };
		case 'K':
			return {
				banner: 'Gateway token rotated since patch install — inlined token is stale.',
				action: '[Reinstall patch] to pick up new token.',
			};
		case 'L': {
			const ms = hb?.last_reconnect_latency_ms ?? CONFIG.LATENCY_WARN_MS;
			const sec = Math.round(ms / 1000);
			return {
				banner: `Recent reconnectMcpServer took ${sec}s (threshold ${CONFIG.LATENCY_WARN_MS / 1000}s, baseline ~5s).`,
				action: 'Gateway may be slow or MCP backend hung. [Open gateway logs] / [Report issue]',
			};
		}
		case 'M': {
			const err = hb?.last_reconnect_error || 'unknown error';
			return {
				banner: `reconnectMcpServer failing: ${err}`,
				action: 'Check gateway + MCP backend health.',
			};
		}
	}
}

/**
 * One-shot computation of the SessionStatus that the webview renders.
 * Combines the ingested track, external facts, and a recent heartbeat
 * history into the single object the UI binds to.
 */
export function computeSessionStatus(
	track: SessionTrack,
	facts: ExternalFacts,
	recentHeartbeats: Heartbeat[],
): SessionStatus {
	const mode = evaluateMode(track, facts);
	const color = colorForMode(mode);
	const { banner, action } = renderBanner(mode, facts, track.lastHeartbeat);
	return {
		session_id: track.session_id,
		color,
		mode,
		banner,
		action,
		recentHeartbeats,
	};
}

/**
 * Validates + merges a `config_override` envelope from the heartbeat
 * response (SP4-L2). Returns the merged CONFIG and a list of rejected
 * keys+reasons. Rejection reasons are strictly "out of range" — unknown
 * keys are preserved in the input but silently filtered. The logic here
 * mirrors the patch-side CONFIG merge so dashboard advisory banners fire
 * in lockstep with what the patch actually applied.
 */
export function applyConfigOverride(
	override: Partial<Record<keyof typeof CONFIG, number>> | undefined,
): {
	effective: typeof CONFIG;
	rejected: Array<{ key: string; reason: string }>;
} {
	const effective = { ...CONFIG };
	const rejected: Array<{ key: string; reason: string }> = [];
	if (!override) {
		return { effective, rejected };
	}
	const ranges = {
		LATENCY_WARN_MS: { min: 5_000, max: 300_000 },
		DEBOUNCE_WINDOW_MS: { min: 2_000, max: 60_000 },
		CONSECUTIVE_ERRORS_FAIL_THRESHOLD: { min: 2, max: 20 },
	};
	for (const [k, v] of Object.entries(override)) {
		if (!(k in ranges)) {
			continue; // unknown key: ignore
		}
		const typedKey = k as keyof typeof ranges;
		const range = ranges[typedKey];
		if (typeof v !== 'number' || !Number.isFinite(v)) {
			rejected.push({ key: k, reason: 'not a finite number' });
			continue;
		}
		if (v < range.min || v > range.max) {
			rejected.push({
				key: k,
				reason: `value ${v} out of range [${range.min}, ${range.max}]`,
			});
			continue;
		}
		// Accepted. Override the matching CONFIG key (safe — only known keys reach here).
		(effective as unknown as Record<string, number>)[k] = v;
	}
	return { effective, rejected };
}
