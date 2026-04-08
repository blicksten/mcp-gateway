import * as vscode from 'vscode';
import type { SapSystem } from './sap-detector';
import type { ServerStatus } from './types';

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

		// Tooltip with details.
		const lines: string[] = [`SAP System: ${system.sid}`];
		if (system.client) { lines.push(`Client: ${system.client}`); }
		if (system.vsp) { lines.push(`VSP: ${system.vsp.name} (${system.vsp.status})`); }
		if (system.gui) { lines.push(`GUI: ${system.gui.name} (${system.gui.status})`); }
		this.tooltip = lines.join('\n');

		this.contextValue = `sap-${system.status}`;
	}
}
