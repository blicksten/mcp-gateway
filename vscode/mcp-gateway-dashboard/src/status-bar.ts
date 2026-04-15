import * as vscode from 'vscode';
import type { ServerDataCache } from './server-data-cache';
import type { ServerView } from './types';
import { escapeMd } from './markdown-utils';

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
 */
export class McpStatusBar implements vscode.Disposable {
	private readonly item: vscode.StatusBarItem;
	private readonly subscription: vscode.Disposable;
	private disposed = false;

	constructor(
		private readonly cache: ServerDataCache,
		alignment: vscode.StatusBarAlignment = vscode.StatusBarAlignment.Left,
		priority = 100,
	) {
		this.item = vscode.window.createStatusBarItem(alignment, priority);
		this.item.command = 'mcpBackends.focus';
		this.item.show();
		this.subscription = cache.onDidRefresh(() => this.refresh());
		// Paint initial state from whatever is already cached so the bar is
		// not blank until the first refresh fires.
		this.refresh();
	}

	/** Read current cache state and repaint the status bar item. */
	private refresh(): void {
		if (this.disposed) { return; }
		if (this.cache.lastRefreshFailed) {
			this.renderOffline();
			return;
		}
		const servers = this.cache.getMcpServers();
		const total = servers.length;
		const running = servers.filter((s) => s.status === 'running').length;
		this.renderCounts(running, total, servers);
	}

	/** Reset item styling to defaults. */
	private resetStyle(): void {
		this.item.backgroundColor = undefined;
		this.item.color = undefined;
	}

	/** Render running/total counts + rich MarkdownString tooltip. */
	private renderCounts(running: number, total: number, servers: readonly ServerView[]): void {
		this.resetStyle();

		if (total === 0) {
			this.item.text = '$(circle-slash) MCP: \u2014';
		} else if (running === total) {
			this.item.text = `$(check) MCP: ${running}/${total}`;
			this.item.color = new vscode.ThemeColor('testing.iconPassed');
		} else if (running === 0) {
			this.item.text = `$(error) MCP: 0/${total}`;
			this.item.color = new vscode.ThemeColor('testing.iconFailed');
		} else {
			this.item.text = `$(warning) MCP: ${running}/${total}`;
			this.item.color = new vscode.ThemeColor('notificationsWarningIcon.foreground');
		}

		this.item.tooltip = this.buildTooltip(running, total, servers);
	}

	/** Render the daemon-offline state. */
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
	 * Build a MarkdownString tooltip with a summary line + per-status
	 * sections listing server names. `isTrusted=false` — tooltips never
	 * execute command links.
	 */
	private buildTooltip(running: number, total: number, servers: readonly ServerView[]): vscode.MarkdownString {
		const md = new vscode.MarkdownString();
		md.isTrusted = false;
		md.supportHtml = false;

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
		const order = ['running', 'degraded', 'error', 'restarting', 'starting', 'stopped', 'disabled'];
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
	}
}
