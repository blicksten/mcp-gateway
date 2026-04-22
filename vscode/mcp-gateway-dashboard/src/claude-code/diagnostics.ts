// Diagnostics report builder for the [Copy diagnostics] button (T16.5.7).
//
// Pure function — takes a snapshot of environment + status state and
// returns a formatted text block ready for clipboard. The webview layer
// wraps it with `vscode.env.clipboard.writeText`.

import { CompatMatrix, Heartbeat, SessionStatus } from './types';

export interface DiagnosticsInput {
	/** OS identifier (e.g. 'win32', 'darwin', 'linux'). */
	platform: string;
	/** VSCode version from `vscode.version`. */
	vscodeVersion: string;
	/** Gateway version from GET /api/v1/version or `"unknown"`. */
	gatewayVersion: string;
	/** Live Claude Code version from heartbeat or VSCode detection. */
	ccVersion: string;
	/** Plugin status from `claude plugin list --json`. */
	pluginStatus: {
		installed: boolean;
		location?: string;
		entries?: number;
	};
	/** Patch status from FS inspection of cc-webview-dir/index.js. */
	patchStatus: {
		installed: boolean;
		location?: string;
		version?: string;
		hasBackup?: boolean;
	};
	/** Compat matrix from GET /api/v1/claude-code/compat-matrix (may be null if unreachable or 503). */
	compatMatrix: CompatMatrix | null;
	/** Per-session status snapshots. */
	sessions: SessionStatus[];
	/** Optional failure trace — e.g. stack from last patch-apply error. */
	failureTrace?: string;
	/** URL template for issue reports; report-to URL embedded in output. */
	reportUrl?: string;
}

/**
 * Builds the diagnostics report as a plain-text block (markdown-friendly
 * fences for the copy-paste destination). Includes Alt-E metrics required
 * by T16.5.7: fiber_depth history, p50/p95 reconnect latency, recent
 * reconnect errors.
 */
export function buildDiagnosticsReport(input: DiagnosticsInput): string {
	const lines: string[] = [];
	const ts = new Date().toISOString();

	lines.push('## MCP Gateway — Claude Code Integration Diagnostics');
	lines.push(`**Generated:** ${ts}`);
	lines.push('');

	// Environment
	lines.push('### Environment');
	lines.push(`- Platform: \`${input.platform}\``);
	lines.push(`- VSCode: \`${input.vscodeVersion}\``);
	lines.push(`- Claude Code: \`${input.ccVersion || '(unknown)'}\``);
	lines.push(`- mcp-gateway: \`${input.gatewayVersion}\``);
	lines.push('');

	// Plugin
	lines.push('### Plugin');
	if (input.pluginStatus.installed) {
		lines.push(`- Status: \`installed\``);
		if (input.pluginStatus.location) {
			lines.push(`- Location: \`${input.pluginStatus.location}\``);
		}
		if (input.pluginStatus.entries !== undefined) {
			lines.push(`- Entries: ${input.pluginStatus.entries}`);
		}
	} else {
		lines.push('- Status: `not installed`');
	}
	lines.push('');

	// Patch
	lines.push('### Patch');
	if (input.patchStatus.installed) {
		lines.push(`- Status: \`installed\``);
		if (input.patchStatus.version) {
			lines.push(`- Version: \`${input.patchStatus.version}\``);
		}
		if (input.patchStatus.location) {
			lines.push(`- Location: \`${input.patchStatus.location}\``);
		}
		if (input.patchStatus.hasBackup !== undefined) {
			lines.push(`- Backup present: ${input.patchStatus.hasBackup ? 'yes' : 'no'}`);
		}
	} else {
		lines.push('- Status: `not installed`');
	}
	lines.push('');

	// Compat matrix
	lines.push('### Compat Matrix');
	if (input.compatMatrix === null) {
		lines.push('- `not available` (compat-matrix endpoint returned 503 or unreachable)');
	} else {
		const m = input.compatMatrix;
		lines.push(`- min: \`${m.min}\``);
		lines.push(`- max_tested: \`${m.max_tested}\``);
		lines.push(`- last_verified: \`${m.last_verified}\``);
		lines.push(`- alt_e_verified_versions: \`${m.alt_e_verified_versions.join(', ')}\``);
		lines.push(`- known_broken: \`${m.known_broken.join(', ') || '(none)'}\``);
		lines.push(`- max_accepted_fiber_depth: ${m.max_accepted_fiber_depth}`);
		lines.push(`- observed_reconnect_latency_ms_p50: ${m.observed_reconnect_latency_ms_p50}`);
		const ccPresent =
			input.ccVersion !== '' && m.alt_e_verified_versions.includes(input.ccVersion);
		lines.push(
			`- Current CC version (${input.ccVersion || '(unknown)'}) verified: ${ccPresent ? 'yes' : 'no'}`,
		);
	}
	lines.push('');

	// Sessions
	lines.push('### Sessions');
	if (input.sessions.length === 0) {
		lines.push('- `no active sessions`');
	}
	for (const s of input.sessions) {
		lines.push(`#### Session \`${s.session_id}\``);
		lines.push(`- Status: \`${s.color} / ${s.mode}\``);
		lines.push(`- Banner: ${s.banner}`);
		if (s.action) {
			lines.push(`- Action: ${s.action}`);
		}

		// Alt-E metrics — derived from recentHeartbeats.
		const metrics = extractAltEMetrics(s.recentHeartbeats);
		lines.push('');
		lines.push('**Alt-E metrics:**');
		lines.push(
			`- mcp_method_fiber_depth (last ${metrics.fiberDepthHistory.length}): ${metrics.fiberDepthHistory.join(', ') || '(none)'}`,
		);
		lines.push(
			`- last_reconnect_latency_ms p50/p95: ${metrics.latencyP50 ?? 'n/a'} / ${metrics.latencyP95 ?? 'n/a'}`,
		);
		lines.push(
			`- last_reconnect_ok count (true/false/null): ${metrics.okTrue}/${metrics.okFalse}/${metrics.okNull}`,
		);
		if (metrics.recentErrors.length > 0) {
			lines.push(`- recent last_reconnect_error (deduplicated):`);
			for (const err of metrics.recentErrors) {
				lines.push(`  - \`${err}\``);
			}
		}

		// Raw heartbeat payloads (last 5).
		lines.push('');
		lines.push('**Last 5 heartbeats (raw):**');
		lines.push('```json');
		for (const hb of s.recentHeartbeats.slice(0, 5)) {
			lines.push(JSON.stringify(hb));
		}
		lines.push('```');
	}
	lines.push('');

	// Failure trace
	if (input.failureTrace) {
		lines.push('### Failure Trace');
		lines.push('```');
		lines.push(input.failureTrace);
		lines.push('```');
		lines.push('');
	}

	// Report URL
	if (input.reportUrl) {
		lines.push('### Report This');
		lines.push(`<${input.reportUrl}>`);
		lines.push('');
	}

	return lines.join('\n');
}

/**
 * Derives Alt-E metrics from a heartbeat window. Exported for testing.
 * Heartbeats are expected newest-first; depth history preserves that order.
 */
export function extractAltEMetrics(heartbeats: Heartbeat[]): {
	fiberDepthHistory: number[];
	latencyP50: number | null;
	latencyP95: number | null;
	okTrue: number;
	okFalse: number;
	okNull: number;
	recentErrors: string[];
} {
	const depths: number[] = [];
	const latencies: number[] = [];
	let okTrue = 0;
	let okFalse = 0;
	let okNull = 0;
	const errorSet = new Set<string>();
	for (const hb of heartbeats) {
		depths.push(hb.mcp_method_fiber_depth);
		// Only count non-idle latencies — FROZEN contract: 0 when no attempt.
		if (hb.last_reconnect_latency_ms > 0) {
			latencies.push(hb.last_reconnect_latency_ms);
		}
		if (hb.last_reconnect_ok === true) {
			okTrue += 1;
		} else if (hb.last_reconnect_ok === false) {
			okFalse += 1;
			if (hb.last_reconnect_error) {
				errorSet.add(hb.last_reconnect_error);
			}
		} else {
			okNull += 1;
		}
	}
	const sorted = [...latencies].sort((a, b) => a - b);
	const latencyP50 = sorted.length > 0 ? percentile(sorted, 0.5) : null;
	const latencyP95 = sorted.length > 0 ? percentile(sorted, 0.95) : null;
	return {
		fiberDepthHistory: depths.slice(0, 5),
		latencyP50,
		latencyP95,
		okTrue,
		okFalse,
		okNull,
		recentErrors: Array.from(errorSet).slice(0, 5),
	};
}

function percentile(sorted: number[], p: number): number {
	if (sorted.length === 0) {
		return 0;
	}
	// Nearest-rank method — matches what operators eyeballing a p95 expect
	// for tiny samples. For ≤100 data points the interpolation variants
	// add no signal.
	const rank = Math.ceil(p * sorted.length) - 1;
	const clamped = Math.min(Math.max(rank, 0), sorted.length - 1);
	return sorted[clamped];
}
