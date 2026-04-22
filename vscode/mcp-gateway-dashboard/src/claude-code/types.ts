// Shared types for the Claude Code integration panel.
//
// Schema matches the FROZEN v1.6.0 contract in
// `docs/api/claude-code-endpoints.md`. Field names and JSON shapes here are
// locked — additive changes only.

/**
 * Heartbeat payload posted by the webview patch (see T16.4.3) and returned
 * by `GET /api/v1/claude-code/patch-status`.
 */
export interface Heartbeat {
	session_id: string;
	patch_version: string;
	cc_version: string;
	vscode_version: string;
	fiber_ok: boolean;
	mcp_method_ok: boolean;
	mcp_method_fiber_depth: number;
	last_reconnect_latency_ms: number;
	/**
	 * true/false = outcome of last reconnect attempt since previous heartbeat.
	 * null = idle (no reconnect attempted since last heartbeat).
	 * The idle case is neutral for the consecutive-error counter (P4-05).
	 */
	last_reconnect_ok: boolean | null;
	last_reconnect_error: string;
	pending_actions_inflight: number;
	fiber_walk_retry_count: number;
	mcp_session_state: McpSessionState;
	/** Client wall clock, ms since epoch. */
	ts: number;
	/** Server wall clock when the heartbeat arrived. Used for "last heartbeat 12s ago" display. */
	received_at: string;
}

/**
 * Finite state machine for the patch's view of Claude Code's MCP session
 * (see PLAN-16 T16.4.3 / P4-02). The dashboard accepts any of these four
 * verbatim; any other value is treated as "unknown".
 */
export type McpSessionState = 'unknown' | 'discovering' | 'ready' | 'lost';

/**
 * Per-endpoint compat matrix entry (T16.6.5 — GET /compat-matrix).
 */
export interface CompatMatrix {
	min: string;
	max_tested: string;
	known_broken: string[];
	last_verified: string;
	alt_e_verified_versions: string[];
	observed_fiber_depths: Record<string, number>;
	max_accepted_fiber_depth: number;
	observed_reconnect_latency_ms_p50: number;
	observed_reconnect_latency_note: string;
}

/**
 * Tuning thresholds mirrored from porfiry-mcp.js CONFIG block. Kept here so
 * the dashboard can evaluate the same rules the patch enforces without a
 * round-trip to the patch for Mode L/M/D decisions.
 */
export const CONFIG = {
	/** Reconnect latency > this ms triggers YELLOW Mode L. (P4-06) */
	LATENCY_WARN_MS: 30_000,
	/** 500 ms server-side coalescing; dashboard does not enforce this directly. */
	DEBOUNCE_WINDOW_MS: 10_000,
	/** N consecutive last_reconnect_ok=false heartbeats → RED Mode M. (P4-05) */
	CONSECUTIVE_ERRORS_FAIL_THRESHOLD: 3,
	/** Mode D minimum fiber_walk_retry_count. (P4-09) */
	MODE_D_MIN_RETRY_COUNT: 5,
	/** Mode D minimum consecutive non-ready heartbeats. (P4-09) */
	MODE_D_MIN_CONSECUTIVE_HEARTBEATS: 3,
} as const;

/**
 * Hard-bounded ranges for config_override (SP4-L2). Mirrors the table in
 * `docs/api/claude-code-endpoints.md` lines 122-126.
 */
export const CONFIG_OVERRIDE_RANGES = {
	LATENCY_WARN_MS: { min: 5_000, max: 300_000 },
	DEBOUNCE_WINDOW_MS: { min: 2_000, max: 60_000 },
	CONSECUTIVE_ERRORS_FAIL_THRESHOLD: { min: 2, max: 20 },
} as const;

/**
 * Failure-mode codes (T16.5.5). Mode `E` is intentionally absent —
 * obsoleted under Alt-E. The tests assert the UI never emits an E-class
 * message as a regression guard.
 */
export type FailureMode =
	| 'none'
	| 'A' // Patch file missing
	| 'B' // VSCode not reloaded after apply
	| 'C' // CC version unverified for Alt-E
	| 'D' // Fiber walk failed, retry-saturated + session not-ready across 3 heartbeats
	| 'F' // CORS blocks gateway
	| 'G' // No plugin installed
	| 'H' // Gateway not running
	| 'I' // VSCode idle
	| 'K' // Token rotated since patch install
	| 'L' // Reconnect latency above threshold
	| 'M'; // Consecutive reconnect errors

/**
 * Overall status severity for the banner + checkbox UI (T16.5.1).
 */
export type StatusColor = 'green' | 'yellow' | 'red';

/**
 * Per-session state tracked in the dashboard for P4-05 (Mode M counter) +
 * P4-09 (Mode D saturation check). The orchestrator pipes heartbeats in
 * order; these counters derive from the stream, not from a single frame.
 */
export interface SessionTrack {
	session_id: string;
	/** Running count of consecutive false last_reconnect_ok. Resets on true. */
	consecutiveReconnectErrors: number;
	/** Running count of consecutive non-ready heartbeats with retry_count >= threshold. */
	consecutiveNonReadyHighRetry: number;
	lastHeartbeat?: Heartbeat;
}

/**
 * The composed status for one session — what the webview renders.
 */
export interface SessionStatus {
	session_id: string;
	color: StatusColor;
	mode: FailureMode;
	banner: string;
	action?: string;
	/** 5 most recent heartbeats, newest first. Drives T16.5.7 diagnostics. */
	recentHeartbeats: Heartbeat[];
}
