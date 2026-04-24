import * as vscode from 'vscode';

/**
 * Tree item rendered at root of MCP Backends / SAP Systems views while the
 * gateway daemon is unreachable AND no last-known-good data is cached.
 *
 * Distinct `contextValue='gateway-connecting'` keeps the item out of every
 * per-server/per-system context menu — no menu `when` clause matches it, so
 * it stays inert (no restart / patch / remove actions).
 *
 * Phase 2 (debug-flicker) surface note: this is shown only on cold start.
 * Once the cache has real data, the preservation path keeps that data on
 * transient errors — tree shows the last-known-good list, not the placeholder.
 */
export class PlaceholderTreeItem extends vscode.TreeItem {
	constructor() {
		super('Connecting to gateway…', vscode.TreeItemCollapsibleState.None);
		this.contextValue = 'gateway-connecting';
		this.iconPath = new vscode.ThemeIcon('sync~spin');
		this.tooltip = 'Waiting for gateway daemon to respond…';
	}
}
