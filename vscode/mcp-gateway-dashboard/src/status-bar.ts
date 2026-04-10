import * as vscode from 'vscode';
import type { IGatewayClient } from './extension';

/**
 * Aggregate MCP status bar indicator.
 * Shows "MCP: N/M" with subtle foreground coloring:
 *   green = all running, yellow = partial, red = all offline,
 *   default = no servers or daemon unreachable.
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

	/** Reset item styling to defaults. */
	private resetStyle(): void {
		this.item.backgroundColor = undefined;
		this.item.color = undefined;
	}

	/** Update display with running/total counts. */
	private update(running: number, total: number): void {
		if (this.disposed) { return; }
		this.resetStyle();

		if (total === 0) {
			this.item.text = '$(circle-slash) MCP: \u2014';
			this.item.tooltip = 'MCP Gateway \u2014 no servers configured';
		} else if (running === total) {
			this.item.text = `$(check) MCP: ${running}/${total}`;
			this.item.tooltip = `MCP Gateway \u2014 all ${total} servers running`;
			this.item.color = new vscode.ThemeColor('testing.iconPassed');
		} else if (running === 0) {
			this.item.text = `$(error) MCP: 0/${total}`;
			this.item.tooltip = `MCP Gateway \u2014 all ${total} servers offline`;
			this.item.color = new vscode.ThemeColor('testing.iconFailed');
		} else {
			this.item.text = `$(warning) MCP: ${running}/${total}`;
			this.item.tooltip = `MCP Gateway \u2014 ${running} of ${total} servers running`;
			this.item.color = new vscode.ThemeColor('notificationsWarningIcon.foreground');
		}
	}

	/** Show offline state when gateway daemon is unreachable. */
	private updateOffline(): void {
		if (this.disposed) { return; }
		this.resetStyle();
		this.item.text = '$(debug-disconnect) MCP: offline';
		this.item.tooltip = 'MCP Gateway \u2014 cannot reach daemon';
	}

	dispose(): void {
		this.disposed = true;
		this.stopPolling();
		this.item.dispose();
	}
}
