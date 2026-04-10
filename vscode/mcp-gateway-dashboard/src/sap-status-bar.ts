import * as vscode from 'vscode';
import type { ServerDataCache } from './server-data-cache';
import type { SapSystem } from './sap-detector';
import type { ServerStatus } from './types';

const STATUS_DOTS: Record<ServerStatus, string> = {
	running: '\u25CF',
	stopped: '\u25CB',
	error: '\u2716',
	degraded: '\u26A0',
	disabled: '\u2298',
	starting: '\u25CB',
	restarting: '\u25CB',
};

export class SapStatusBar implements vscode.Disposable {
	private readonly item: vscode.StatusBarItem;
	private readonly subscription: vscode.Disposable;

	constructor(cache: ServerDataCache) {
		this.item = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left, 99);
		this.subscription = cache.onDidRefresh(() => {
			this.update(cache.getSapSystems());
		});
		// Paint initial state from whatever is already cached (H-01: avoid
		// invisible window when constructor runs after first refresh fires).
		this.update(cache.getSapSystems());
	}

	private update(systems: SapSystem[]): void {
		if (systems.length === 0) {
			this.item.hide();
			return;
		}

		// Adaptive display: full SID-Client if ≤ 3, base SID if 4+.
		const labels = systems.map((s) => {
			const label = systems.length <= 3 ? s.key : s.sid;
			return `${label} ${STATUS_DOTS[s.status] ?? '?'}`;
		});

		this.item.text = `$(server) SAP: ${labels.join('  ')}`;

		const hasError = systems.some((s) => s.status === 'error');
		const hasDegraded = systems.some((s) => s.status === 'degraded');
		const allRunning = systems.every((s) => s.status === 'running');
		this.item.backgroundColor = undefined;
		if (hasError) {
			this.item.color = new vscode.ThemeColor('testing.iconFailed');
		} else if (hasDegraded) {
			this.item.color = new vscode.ThemeColor('notificationsWarningIcon.foreground');
		} else if (allRunning) {
			this.item.color = new vscode.ThemeColor('testing.iconPassed');
		} else {
			this.item.color = undefined;
		}

		this.item.tooltip = systems.map((s) => {
			const parts = [`${s.key}: ${s.status}`];
			if (s.vsp) { parts.push(`  VSP: ${s.vsp.name} (${s.vsp.status})`); }
			if (s.gui) { parts.push(`  GUI: ${s.gui.name} (${s.gui.status})`); }
			return parts.join('\n');
		}).join('\n\n');

		this.item.show();
	}

	dispose(): void {
		this.subscription.dispose();
		this.item.dispose();
	}
}
