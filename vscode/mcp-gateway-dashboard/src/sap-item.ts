import * as vscode from 'vscode';
import type { SapSystem } from './sap-detector';
import type { ServerStatus } from './types';
import { escapeMd } from './markdown-utils';

const STATUS_ICONS: Record<ServerStatus, string> = {
	running: 'vm-running',
	stopped: 'debug-stop',
	error: 'error',
	degraded: 'warning',
	disabled: 'circle-slash',
	starting: 'loading~spin',
	restarting: 'sync~spin',
};

const STATUS_DOTS: Record<ServerStatus, string> = {
	running: '\u25CF',      // ●
	stopped: '\u25CB',      // ○
	error: '\u2716',        // ✖
	degraded: '\u26A0',     // ⚠
	disabled: '\u2298',     // ⊘
	starting: '\u25CB',
	restarting: '\u25CB',
};

export class SapSystemItem extends vscode.TreeItem {
	readonly system: SapSystem;

	constructor(system: SapSystem) {
		super(system.key, vscode.TreeItemCollapsibleState.None);
		this.system = system;

		// Icon based on composite status.
		const iconId = STATUS_ICONS[system.status] ?? 'question';
		this.iconPath = new vscode.ThemeIcon(iconId);

		// Description: component status dots.
		const parts: string[] = [];
		if (system.vsp) {
			parts.push(`vsp ${STATUS_DOTS[system.vsp.status] ?? '?'}`);
		}
		if (system.gui) {
			parts.push(`gui ${STATUS_DOTS[system.gui.status] ?? '?'}`);
		}
		this.description = parts.join('  ');

		// Tooltip with details (MarkdownString for rich rendering).
		const md = new vscode.MarkdownString();
		md.isTrusted = false;
		md.supportHtml = false;
		md.appendMarkdown(`**SAP System:** ${escapeMd(system.sid)}\n\n`);
		if (system.client) { md.appendMarkdown(`- Client: \`${escapeMd(system.client)}\`\n`); }
		if (system.vsp) {
			md.appendMarkdown(`- VSP: \`${escapeMd(system.vsp.name)}\` (${system.vsp.status})\n`);
			if (system.vsp.pid) { md.appendMarkdown(`  - PID: \`${system.vsp.pid}\`\n`); }
			if (system.vsp.restart_count > 0) { md.appendMarkdown(`  - Restarts: ${system.vsp.restart_count}\n`); }
			if (system.vsp.last_error) { md.appendMarkdown(`  - Error: ${escapeMd(system.vsp.last_error)}\n`); }
		}
		if (system.gui) {
			md.appendMarkdown(`- GUI: \`${escapeMd(system.gui.name)}\` (${system.gui.status})\n`);
			if (system.gui.pid) { md.appendMarkdown(`  - PID: \`${system.gui.pid}\`\n`); }
			if (system.gui.restart_count > 0) { md.appendMarkdown(`  - Restarts: ${system.gui.restart_count}\n`); }
			if (system.gui.last_error) { md.appendMarkdown(`  - Error: ${escapeMd(system.gui.last_error)}\n`); }
		}
		this.tooltip = md;

		this.contextValue = `sap-${system.status}`;
	}
}

