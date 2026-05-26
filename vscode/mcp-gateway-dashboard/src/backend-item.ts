import * as vscode from 'vscode';
import type { ServerView, ServerStatus } from './types';
import { escapeMd } from './markdown-utils';

interface IconDef {
	id: string;
	color?: string;
}

const STATUS_ICONS: Record<ServerStatus, IconDef> = {
	running:     { id: 'vm-running',    color: 'testing.iconPassed' },
	stopped:     { id: 'debug-stop',    color: 'disabledForeground' },
	error:       { id: 'error',         color: 'testing.iconFailed' },
	degraded:    { id: 'warning',       color: 'list.warningForeground' },
	disabled:    { id: 'circle-slash',  color: 'disabledForeground' },
	starting:    { id: 'loading~spin' },
	restarting:  { id: 'sync~spin' },
	// Yellow warning triangle, same color as 'degraded'. Stable badge —
	// no spinner — communicates "host is down, gateway knows, not in
	// a doom loop". See docs/PLAN-unreachable-handling.md.
	unreachable: { id: 'warning',       color: 'list.warningForeground' },
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
		// Build the right-aligned description text. Order of precedence:
		//   stale (cache, gateway offline)  →  "· offline"
		//   status=unreachable (host down)  →  "· host offline (slow-polling)"
		//   default                          →  no suffix.
		// "host offline" is the unreachable wording so operators read it
		// as "your network/VPN, not the gateway"; the slow-polling tag
		// signals gateway is patiently re-checking, no spinner needed.
		let suffix = '';
		if (stale) {
			suffix = ' · offline';
		} else if (server.status === 'unreachable') {
			suffix = ' · host offline (slow-polling)';
		}
		this.description = `${transport}${restartSuffix}${suffix}`;
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

