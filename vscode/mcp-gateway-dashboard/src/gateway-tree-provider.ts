import * as vscode from 'vscode';
import type { ServerDataCache } from './server-data-cache';
import type { HealthResponse } from './types';
import { escapeMd } from './markdown-utils';

/**
 * Phase D.4 — dedicated tree view for the gateway daemon itself.
 *
 * Shows a single "Gateway" root row with status icon + short description
 * (uptime or "offline"), expandable into detail rows (PID, Version,
 * Started, Uptime). Inline action buttons (start/stop/restart) are bound
 * via package.json menu contributions, gated on the root item's
 * `contextValue`:
 *   - `gateway-running`    → show stop + restart inline
 *   - `gateway-unreachable` → show start inline
 *
 * The view is driven off `ServerDataCache.onDidRefresh`, so it shares the
 * single poll cycle with backend tree + status bar — no independent timer.
 */
export class GatewayTreeProvider
	implements vscode.TreeDataProvider<vscode.TreeItem>, vscode.Disposable
{
	private readonly _onDidChangeTreeData = new vscode.EventEmitter<vscode.TreeItem | undefined | void>();
	readonly onDidChangeTreeData = this._onDidChangeTreeData.event;

	private readonly subscription: vscode.Disposable;
	private disposed = false;
	private lastFingerprint: string | null = null;

	constructor(private readonly cache: ServerDataCache) {
		this.subscription = cache.onDidRefresh(() => this.refresh());
	}

	refresh(): void {
		if (this.disposed) { return; }
		const health = this.cache.gatewayHealth;
		const reachable = !this.cache.lastRefreshFailed && health !== null;
		const fp = this.computeFingerprint(reachable, health);
		if (fp === this.lastFingerprint) { return; }
		this.lastFingerprint = fp;
		this._onDidChangeTreeData.fire();
	}

	getChildren(element?: vscode.TreeItem): vscode.TreeItem[] {
		const health = this.cache.gatewayHealth;
		const reachable = !this.cache.lastRefreshFailed && health !== null;

		if (!element) {
			return [new GatewayRootItem(reachable, health)];
		}
		// Expanded root: show detail rows. Only populate when reachable —
		// offline state has nothing useful to break out.
		if (element instanceof GatewayRootItem && reachable && health) {
			return buildDetailItems(health);
		}
		return [];
	}

	getTreeItem(element: vscode.TreeItem): vscode.TreeItem {
		return element;
	}

	/** Exposed for tests. */
	getFingerprint(): string | null {
		return this.lastFingerprint;
	}

	private computeFingerprint(reachable: boolean, health: HealthResponse | null): string {
		if (!reachable || health === null) { return 'offline'; }
		// Uptime is rounded to 5-second buckets so the tree doesn't re-render
		// every poll cycle when only `uptime_seconds` ticks forward.
		const uptimeBucket = Math.floor((health.uptime_seconds ?? 0) / 5);
		return [
			'online',
			health.pid ?? '?',
			health.version ?? '?',
			health.started_at ?? '?',
			uptimeBucket,
		].join('|');
	}

	dispose(): void {
		if (this.disposed) { return; }
		this.disposed = true;
		this.subscription.dispose();
		this._onDidChangeTreeData.dispose();
	}
}

export class GatewayRootItem extends vscode.TreeItem {
	constructor(reachable: boolean, health: HealthResponse | null) {
		const reachableAndHealth = reachable && health !== null;
		super(
			'Gateway',
			reachableAndHealth ? vscode.TreeItemCollapsibleState.Collapsed : vscode.TreeItemCollapsibleState.None,
		);

		if (reachableAndHealth) {
			this.contextValue = 'gateway-running';
			this.description = formatUptime(health.uptime_seconds);
			this.iconPath = new vscode.ThemeIcon('server-process', new vscode.ThemeColor('testing.iconPassed'));
			this.tooltip = buildRootTooltip(health);
		} else {
			this.contextValue = 'gateway-unreachable';
			this.description = 'offline';
			this.iconPath = new vscode.ThemeIcon('debug-disconnect', new vscode.ThemeColor('testing.iconFailed'));
			const md = new vscode.MarkdownString();
			md.isTrusted = false;
			md.supportHtml = false;
			md.appendMarkdown('**MCP Gateway daemon** — cannot reach `/api/v1/health`.\n\n');
			md.appendMarkdown('Start the daemon via the inline action, or check logs for the failure reason.\n');
			this.tooltip = md;
		}
	}
}

class GatewayDetailItem extends vscode.TreeItem {
	constructor(label: string, value: string, icon: string) {
		super(label, vscode.TreeItemCollapsibleState.None);
		this.description = value;
		this.contextValue = 'gateway-detail';
		this.iconPath = new vscode.ThemeIcon(icon);
	}
}

function buildDetailItems(health: HealthResponse): GatewayDetailItem[] {
	return [
		new GatewayDetailItem('PID', health.pid !== undefined ? String(health.pid) : 'unknown', 'symbol-number'),
		new GatewayDetailItem('Version', health.version ?? 'unknown', 'tag'),
		new GatewayDetailItem('Started', health.started_at ?? 'unknown', 'calendar'),
		new GatewayDetailItem('Uptime', formatUptime(health.uptime_seconds), 'watch'),
	];
}

function buildRootTooltip(health: HealthResponse): vscode.MarkdownString {
	const md = new vscode.MarkdownString();
	md.isTrusted = false;
	md.supportHtml = false;
	md.appendMarkdown('**MCP Gateway daemon** — running\n\n');
	if (health.version !== undefined) {
		md.appendMarkdown(`- Version: \`${escapeMd(health.version)}\`\n`);
	}
	if (health.pid !== undefined) {
		md.appendMarkdown(`- PID: \`${health.pid}\`\n`);
	}
	if (health.started_at !== undefined) {
		md.appendMarkdown(`- Started: \`${escapeMd(health.started_at)}\`\n`);
	}
	md.appendMarkdown(`- Uptime: ${formatUptime(health.uptime_seconds)}\n`);
	md.appendMarkdown(`- Servers: ${health.running}/${health.servers}\n`);
	return md;
}

/**
 * Human-readable uptime renderer. Mirrors mcp-ctl daemon status output:
 *  <60s     → "Ns"
 *  <1h      → "Nm Ss"
 *  <24h     → "Nh Mm"
 *  >=24h    → "Nd Hh"
 * Returns "unknown" when uptime_seconds is undefined (pre-D.1 daemons).
 */
export function formatUptime(seconds: number | undefined): string {
	if (seconds === undefined || seconds < 0) { return 'unknown'; }
	const s = Math.floor(seconds);
	const d = Math.floor(s / 86400);
	const h = Math.floor((s % 86400) / 3600);
	const m = Math.floor((s % 3600) / 60);
	const sec = s % 60;
	if (d > 0) { return `${d}d ${h}h`; }
	if (h > 0) { return `${h}h ${m}m`; }
	if (m > 0) { return `${m}m ${sec}s`; }
	return `${sec}s`;
}
