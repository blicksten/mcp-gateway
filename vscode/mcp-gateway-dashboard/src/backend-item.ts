import * as vscode from 'vscode';
import type { ServerView, ServerStatus } from './types';

interface IconDef {
	id: string;
	color?: string;
}

const STATUS_ICONS: Record<ServerStatus, IconDef> = {
	running:    { id: 'vm-running',    color: 'testing.iconPassed' },
	stopped:    { id: 'debug-stop',    color: 'disabledForeground' },
	error:      { id: 'error',         color: 'testing.iconFailed' },
	degraded:   { id: 'warning',       color: 'list.warningForeground' },
	disabled:   { id: 'circle-slash',  color: 'disabledForeground' },
	starting:   { id: 'loading~spin' },
	restarting: { id: 'sync~spin' },
};

export class BackendItem extends vscode.TreeItem {
	constructor(
		public readonly server: ServerView,
	) {
		super(server.name, vscode.TreeItemCollapsibleState.None);
		this.contextValue = server.status;
		const transport = server.transport || 'rest';
		this.description = server.restart_count > 0
			? `${transport} (x${server.restart_count})`
			: transport;
		this.tooltip = BackendItem.buildTooltip(server);

		const iconDef = STATUS_ICONS[server.status] ?? { id: 'question', color: 'editorWarning.foreground' };
		this.iconPath = new vscode.ThemeIcon(
			iconDef.id,
			iconDef.color ? new vscode.ThemeColor(iconDef.color) : undefined,
		);
	}

	private static buildTooltip(server: ServerView): string {
		const lines = [`${server.name} \u2014 ${server.status}`];
		lines.push(`Transport: ${server.transport || 'rest'}`);
		if (server.pid) {
			lines.push(`PID: ${server.pid}`);
		}
		if (server.restart_count > 0) {
			lines.push(`Restarts: ${server.restart_count}`);
		}
		if (server.last_error) {
			lines.push(`Error: ${server.last_error}`);
		}
		if (server.tools?.length) {
			lines.push(`Tools: ${server.tools.length}`);
		}
		return lines.join('\n');
	}
}
