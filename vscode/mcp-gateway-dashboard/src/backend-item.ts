import * as vscode from 'vscode';
import type { ServerView, ServerStatus } from './types';
import { escapeMd } from './markdown-utils';

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
		/** When true the data is stale (gateway offline). Icon is greyed out. */
		stale = false,
	) {
		super(server.name, vscode.TreeItemCollapsibleState.None);
		this.contextValue = server.status;
		const transport = server.transport || 'rest';
		const restartSuffix = server.restart_count > 0 ? ` (x${server.restart_count})` : '';
		// When stale, append "· offline" so the user sees data is from cache.
		this.description = stale
			? `${transport}${restartSuffix} · offline`
			: `${transport}${restartSuffix}`;
		this.tooltip = BackendItem.buildTooltip(server, stale);

		if (stale) {
			// Grey disconnected icon regardless of last-known status —
			// we can't trust the status when the gateway is unreachable.
			this.iconPath = new vscode.ThemeIcon('debug-disconnect',
				new vscode.ThemeColor('disabledForeground'));
		} else {
			const iconDef = STATUS_ICONS[server.status] ?? { id: 'question', color: 'editorWarning.foreground' };
			this.iconPath = new vscode.ThemeIcon(
				iconDef.id,
				iconDef.color ? new vscode.ThemeColor(iconDef.color) : undefined,
			);
		}
	}

	private static buildTooltip(server: ServerView, stale: boolean): vscode.MarkdownString {
		const md = new vscode.MarkdownString();
		md.isTrusted = false;
		md.supportHtml = false;
		if (stale) {
			md.appendMarkdown(`**${escapeMd(server.name)}** — *(gateway offline — showing last known state)*\n\n`);
		} else {
			md.appendMarkdown(`**${escapeMd(server.name)}** — ${server.status}\n\n`);
		}
		md.appendMarkdown(`- Transport: \`${escapeMd(server.transport || 'rest')}\`\n`);
		if (server.pid) {
			md.appendMarkdown(`- PID: \`${server.pid}\`\n`);
		}
		if (server.restart_count > 0) {
			md.appendMarkdown(`- Restarts: ${server.restart_count}\n`);
		}
		if (server.last_error) {
			md.appendMarkdown(`- Error: ${escapeMd(server.last_error)}\n`);
		}
		if (server.tools?.length) {
			md.appendMarkdown(`- Tools: ${server.tools.length}\n`);
		}
		return md;
	}
}

