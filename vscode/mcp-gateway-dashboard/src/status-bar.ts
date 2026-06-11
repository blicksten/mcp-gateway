import * as vscode from 'vscode';
import type { ServerDataCache } from './server-data-cache';
import type { HealthResponse, ServerView } from './types';
import { formatUptime } from './gateway-tree-provider';
import { escapeMd } from './markdown-utils';
import { formatGatewayVersion } from './version-format';

/** Options for McpStatusBar heartbeat (test-injectable). */
export interface McpStatusBarOptions {
	/**
	 * Heartbeat interval in ms. When the last successful refresh is older than
	 * this value, the bar renders an "unknown / no signal" state rather than
	 * stale green. Default: pollInterval * statusHeartbeatMultiplier.
	 * Production sets this from settings; tests pass a small value directly.
	 */
	heartbeatMs?: number;
	/** Injectable setInterval for tests. Defaults to global setInterval. */
	setInterval?: (cb: () => void, ms: number) => ReturnType<typeof setInterval>;
	/** Injectable clearInterval for tests. Defaults to global clearInterval. */
	clearInterval?: (h: ReturnType<typeof setInterval>) => void;
	/** Injectable Date.now() for tests. Defaults to Date.now. */
	now?: () => number;
}

/**
 * Aggregate MCP status bar indicator.
 *
 * Shows "MCP: N/M" with subtle foreground coloring:
 *   green = all running, yellow = partial, red = all offline,
 *   default = no servers or daemon unreachable.
 *
 * Phase 11.B: driven by `ServerDataCache.onDidRefresh`. No independent timer;
 * the cache's `lastRefreshFailed` flag distinguishes daemon-offline from an
 * empty server list, so the bar can render "offline" vs "no servers" without
 * its own /health call.
 *
 * FIX 3: Added heartbeat timer. If no successful refresh is seen for
 * `heartbeatMs`, the bar renders an "unknown" state (visually distinct from
 * "offline") so a stalled poll does not freeze the bar on stale green.
 */
export class McpStatusBar implements vscode.Disposable {
	private readonly item: vscode.StatusBarItem;
	private readonly subscription: vscode.Disposable;
	private disposed = false;
	// FIX 3: heartbeat fields
	private lastSuccessfulRefreshAt = 0;
	private heartbeatTimer: ReturnType<typeof setInterval> | undefined;
	private readonly heartbeatMs: number;
	private readonly _clearInterval: (h: ReturnType<typeof setInterval>) => void;
	private readonly _now: () => number;

	constructor(
		private readonly cache: ServerDataCache,
		alignment: vscode.StatusBarAlignment = vscode.StatusBarAlignment.Left,
		priority = 100,
		options?: McpStatusBarOptions,
	) {
		// FIX 3: assign injectable options BEFORE refresh() so this._now is
		// available when refresh() calls it (even during the initial paint below).
		this._now = options?.now ?? (() => Date.now());
		this._clearInterval = options?.clearInterval ?? ((h) => clearInterval(h));
		this.heartbeatMs = options?.heartbeatMs ?? 0;
		const _setInterval = options?.setInterval ?? ((cb, ms) => setInterval(cb, ms));

		this.item = vscode.window.createStatusBarItem(alignment, priority);
		this.item.command = 'mcpBackends.focus';
		this.item.show();
		this.subscription = cache.onDidRefresh(() => this.refresh());
		// Paint initial state from whatever is already cached so the bar is
		// not blank until the first refresh fires.
		this.refresh();

		if (this.heartbeatMs > 0) {
			this.heartbeatTimer = _setInterval(() => this.checkHeartbeat(), this.heartbeatMs);
		}
	}

	/** Read current cache state and repaint the status bar item. */
	private refresh(): void {
		if (this.disposed) { return; }
		if (this.cache.lastRefreshFailed) {
			// An explicit lastRefreshFailed event wins over heartbeat-unknown.
			this.renderOffline();
			return;
		}
		// Successful refresh — track the timestamp for the heartbeat.
		this.lastSuccessfulRefreshAt = this._now();
		const servers = this.cache.getMcpServers();
		const total = servers.length;
		const running = servers.filter((s) => s.status === 'running').length;
		this.renderCounts(running, total, servers, this.cache.gatewayHealth);
	}

	/**
	 * FIX 3: heartbeat check — called by the independent timer.
	 * If no successful refresh has arrived within heartbeatMs, render the
	 * "unknown / no signal" state (visually distinct from renderOffline).
	 * An explicit lastRefreshFailed event (renderOffline) takes precedence
	 * over the heartbeat within the same interval.
	 */
	private checkHeartbeat(): void {
		if (this.disposed) { return; }
		// If the cache explicitly reports a failure, renderOffline already fired.
		// Do not overwrite with "unknown" — offline is the more precise state.
		if (this.cache.lastRefreshFailed) { return; }
		if (this.lastSuccessfulRefreshAt === 0) { return; } // never had a successful refresh yet
		if (this._now() - this.lastSuccessfulRefreshAt > this.heartbeatMs) {
			this.renderUnknown();
		}
	}

	/** Reset item styling to defaults. */
	private resetStyle(): void {
		this.item.backgroundColor = undefined;
		this.item.color = undefined;
	}

	/** Render running/total counts + rich MarkdownString tooltip. */
	private renderCounts(
		running: number,
		total: number,
		servers: readonly ServerView[],
		health: HealthResponse | null,
	): void {
		this.resetStyle();

		const allUnreachable = total > 0 && servers.every((s) => s.status === 'unreachable');
		const anyUnreachable = servers.some((s) => s.status === 'unreachable');

		if (total === 0) {
			this.item.text = '$(circle-slash) MCP: \u2014';
		} else if (running === total) {
			this.item.text = `$(check) MCP: ${running}/${total}`;
			this.item.color = new vscode.ThemeColor('testing.iconPassed');
		} else if (allUnreachable) {
			// All servers unreachable (e.g. VPN-off, all backends behind it):
			// yellow warning, NOT red error. The gateway is healthy; the
			// hosts are. Operator sees "your network, fix that" rather than
			// "gateway broken". See docs/PLAN-unreachable-handling.md.
			this.item.text = `$(warning) MCP: 0/${total} (offline)`;
			this.item.color = new vscode.ThemeColor('list.warningForeground');
		} else if (running === 0) {
			this.item.text = `$(error) MCP: 0/${total}`;
			this.item.color = new vscode.ThemeColor('testing.iconFailed');
		} else if (anyUnreachable) {
			// Partial running + some unreachable: still yellow (matches
			// per-server colour) so the aggregate icon matches the dominant
			// per-server icon when the operator opens the tree.
			this.item.text = `$(warning) MCP: ${running}/${total}`;
			this.item.color = new vscode.ThemeColor('list.warningForeground');
		} else {
			this.item.text = `$(warning) MCP: ${running}/${total}`;
			this.item.color = new vscode.ThemeColor('notificationsWarningIcon.foreground');
		}

		this.item.tooltip = this.buildTooltip(running, total, servers, health);
	}

	/** Render the daemon-offline state (confirmed unreachable). */
	private renderOffline(): void {
		this.resetStyle();
		this.item.text = '$(debug-disconnect) MCP: offline';
		const md = new vscode.MarkdownString();
		md.isTrusted = false;
		md.supportHtml = false;
		md.appendMarkdown('**MCP Gateway** — cannot reach daemon\n');
		this.item.tooltip = md;
	}

	/**
	 * FIX 3: render "unknown / no signal" state — visually distinct from
	 * renderOffline (offline = confirmed-unreachable; unknown = no update heard).
	 * Shown when the heartbeat fires and no successful refresh has arrived
	 * within heartbeatMs.
	 */
	private renderUnknown(): void {
		this.resetStyle();
		this.item.text = '$(question) MCP: ?';
		const md = new vscode.MarkdownString();
		md.isTrusted = false;
		md.supportHtml = false;
		const ageS = Math.round((this._now() - this.lastSuccessfulRefreshAt) / 1000);
		md.appendMarkdown(`**MCP Gateway** — no update in ${ageS}s\n`);
		this.item.tooltip = md;
	}

	/**
	 * Build a MarkdownString tooltip with a summary line + per-status
	 * sections listing server names. `isTrusted=false` — tooltips never
	 * execute command links.
	 */
	private buildTooltip(
		running: number,
		total: number,
		servers: readonly ServerView[],
		health: HealthResponse | null,
	): vscode.MarkdownString {
		const md = new vscode.MarkdownString();
		md.isTrusted = false;
		md.supportHtml = false;

		// Phase D.4: lead the tooltip with a daemon-meta line when /health
		// is available. Renders: "Gateway: 2h 14m · v1.7.2 · pid 12345".
		// Fields are optional against older daemons that pre-date D.1; skip
		// missing pieces rather than print "unknown" (keeps the line tidy).
		if (health !== null) {
			const parts: string[] = [];
			if (health.uptime_seconds !== undefined) {
				parts.push(formatUptime(health.uptime_seconds));
			}
			if (health.version !== undefined) {
				// formatGatewayVersion handles "dev" → "dev build" and adds
				// a v-prefix only when missing, avoiding the historic "vdev"
				// status-bar string. Audit SC-C-M1 helper consolidation.
				parts.push(escapeMd(formatGatewayVersion(health.version)));
			}
			if (health.pid !== undefined) {
				parts.push(`pid ${health.pid}`);
			}
			if (parts.length > 0) {
				md.appendMarkdown(`**Gateway**: ${parts.join(' · ')}\n\n`);
			}
		}

		if (total === 0) {
			md.appendMarkdown('**MCP Gateway** — no servers configured\n');
			return md;
		}

		if (running === total) {
			md.appendMarkdown(`**MCP Gateway** — all ${total} servers running\n`);
		} else if (running === 0) {
			md.appendMarkdown(`**MCP Gateway** — all ${total} servers offline\n`);
		} else {
			md.appendMarkdown(`**MCP Gateway** — ${running} of ${total} servers running\n`);
		}

		// Per-status breakdown (only non-empty buckets).
		const byStatus = new Map<string, string[]>();
		for (const s of servers) {
			const bucket = byStatus.get(s.status) ?? [];
			bucket.push(s.name);
			byStatus.set(s.status, bucket);
		}
		// Deterministic order: running first, then problematic, then others.
		// 'unreachable' grouped with 'degraded' (both yellow / "needs
		// attention but not broken"). Ahead of 'error' to highlight the
		// network/VPN angle first when both classes are present.
		const order = ['running', 'unreachable', 'degraded', 'error', 'restarting', 'starting', 'stopped', 'disabled'];
		for (const status of order) {
			const names = byStatus.get(status);
			if (!names || names.length === 0) { continue; }
			md.appendMarkdown(`\n**${status}** (${names.length}):\n`);
			for (const name of names) {
				md.appendMarkdown(`- ${escapeMd(name)}\n`);
			}
		}
		return md;
	}

	dispose(): void {
		this.disposed = true;
		this.subscription.dispose();
		this.item.dispose();
		// FIX 3: clear heartbeat timer
		if (this.heartbeatTimer !== undefined) {
			this._clearInterval(this.heartbeatTimer);
			this.heartbeatTimer = undefined;
		}
	}
}
