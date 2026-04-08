import * as vscode from 'vscode';
import type { IGatewayClient } from './extension';

/**
 * Aggregate MCP status bar indicator.
 * Shows "$(server) MCP: N/M" where N = running, M = total.
 * Background: default (all OK), warning (partial), error (offline).
 */
export class McpStatusBar {
	private readonly item: vscode.StatusBarItem;
	private timer: ReturnType<typeof setInterval> | undefined;
	private disposed = false;

	constructor(
		private readonly client: IGatewayClient,
		alignment: vscode.StatusBarAlignment = vscode.StatusBarAlignment.Left,
		priority = 100,
	) {
		this.item = vscode.window.createStatusBarItem(alignment, priority);
		this.item.command = 'mcpBackends.focus';
		this.item.show();
	}

	/** Start periodic polling. Runs one immediate poll, then repeats at interval. */
	startPolling(intervalMs: number): void {
		this.poll();
		this.timer = setInterval(() => this.poll(), intervalMs);
	}

	/** Stop polling. Does not dispose the status bar item. */
	stopPolling(): void {
		if (this.timer !== undefined) {
			clearInterval(this.timer);
			this.timer = undefined;
		}
	}

	/** Single poll cycle — query health and update display. */
	async poll(): Promise<void> {
		if (this.disposed) { return; }
		try {
			const health = await this.client.getHealth() as { status: string; servers: number; running: number };
			this.update(health.running, health.servers);
		} catch {
			this.updateOffline();
		}
	}

	/** Update display with running/total counts. */
	private update(running: number, total: number): void {
		if (this.disposed) { return; }
		this.item.text = `$(server) MCP: ${running}/${total}`;

		if (total === 0) {
			this.item.tooltip = 'MCP Gateway — no servers configured';
			this.item.backgroundColor = undefined;
		} else if (running === total) {
			this.item.tooltip = `MCP Gateway — all ${total} servers running`;
			this.item.backgroundColor = undefined;
		} else if (running === 0) {
			this.item.tooltip = `MCP Gateway — all ${total} servers offline`;
			this.item.backgroundColor = new vscode.ThemeColor('statusBarItem.errorBackground');
		} else {
			this.item.tooltip = `MCP Gateway — ${running} of ${total} servers running`;
			this.item.backgroundColor = new vscode.ThemeColor('statusBarItem.warningBackground');
		}
	}

	/** Show offline state when gateway is unreachable. */
	private updateOffline(): void {
		if (this.disposed) { return; }
		this.item.text = '$(server) MCP: offline';
		this.item.tooltip = 'MCP Gateway — cannot reach API';
		this.item.backgroundColor = new vscode.ThemeColor('statusBarItem.errorBackground');
	}

	dispose(): void {
		this.disposed = true;
		this.stopPolling();
		this.item.dispose();
	}
}
