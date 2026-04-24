import * as vscode from 'vscode';
import type { ServerDataCache } from './server-data-cache';
import { SapSystemItem, SapComponentItem } from './sap-item';
import { PlaceholderTreeItem } from './tree-placeholder';
import type { SapSystem } from './sap-detector';

// Phase 2 (debug-flicker): class generic widened to `vscode.TreeItem` so the
// tree can surface a `PlaceholderTreeItem` at root during cold-start offline.
// SapSystemItem + SapComponentItem remain subtypes, so existing hierarchical
// logic (`element instanceof SapSystemItem`) keeps working unchanged.
export class SapTreeProvider implements vscode.TreeDataProvider<vscode.TreeItem>, vscode.Disposable {
	private readonly _onDidChangeTreeData = new vscode.EventEmitter<void>();
	readonly onDidChangeTreeData = this._onDidChangeTreeData.event;

	private readonly cache: ServerDataCache;
	private readonly subscription: vscode.Disposable;
	private readonly configSubscription: vscode.Disposable;
	private _disposed = false;
	private lastFingerprint: string | null = null;
	private hierarchical = false;

	constructor(cache: ServerDataCache) {
		this.cache = cache;
		this.subscription = cache.onDidRefresh(() => this.refresh());

		// Initial read + config watcher. A toggle of sapGroupBySid changes the
		// tree shape but not its data — we fire a forced refresh that bypasses
		// the fingerprint check so collapsibleState flips immediately.
		this.hierarchical = this.readGroupBySidSetting();
		this.configSubscription = vscode.workspace.onDidChangeConfiguration((e) => {
			if (!e.affectsConfiguration('mcpGateway.sapGroupBySid')) { return; }
			const next = this.readGroupBySidSetting();
			if (next === this.hierarchical) { return; }
			this.hierarchical = next;
			// Force refresh — fingerprint is unchanged (same servers), but the
			// tree shape changed so we must rebuild.
			this.lastFingerprint = null;
			if (!this._disposed) { this._onDidChangeTreeData.fire(); }
		});
	}

	getTreeItem(element: vscode.TreeItem): vscode.TreeItem {
		return element;
	}

	getChildren(element?: vscode.TreeItem): vscode.TreeItem[] {
		if (!element) {
			const systems = this.cache.getSapSystems();
			if (this.cache.lastRefreshFailed && systems.length === 0) {
				return [new PlaceholderTreeItem()];
			}
			return systems.map((sys) => new SapSystemItem(sys, this.hierarchical));
		}
		if (!this.hierarchical) { return []; }
		if (element instanceof SapSystemItem) {
			// Phase 17.5: imported rows have no daemon-backed VSP/GUI components.
			// Make the contract explicit so future refactors can't accidentally
			// treat them as expandable.
			if (element.system.imported) { return []; }
			const children: SapComponentItem[] = [];
			if (element.system.vsp) {
				children.push(new SapComponentItem(element.system, 'vsp', element.system.vsp));
			}
			if (element.system.gui) {
				children.push(new SapComponentItem(element.system, 'gui', element.system.gui));
			}
			return children;
		}
		return [];
	}

	refresh(): void {
		if (this._disposed) { return; }
		const systems = this.cache.getSapSystems();
		const next = this.computeFingerprint(systems, this.cache.lastRefreshFailed);
		if (next === this.lastFingerprint) { return; }
		this.lastFingerprint = next;
		this._onDidChangeTreeData.fire();
	}

	// Exposed for tests; not part of the public TreeDataProvider contract.
	getFingerprint(): string | null {
		return this.lastFingerprint;
	}

	// Exposed for tests; reflects the current hierarchical mode.
	isHierarchical(): boolean {
		return this.hierarchical;
	}

	private readGroupBySidSetting(): boolean {
		try {
			return vscode.workspace.getConfiguration('mcpGateway').get<boolean>('sapGroupBySid', false) === true;
		} catch {
			return false;
		}
	}

	private computeFingerprint(systems: readonly SapSystem[], lastRefreshFailed: boolean): string {
		// Mirrors BackendTreeProvider: must cover every render-affecting field on
		// the sub-servers so a silent process restart (new pid with same status)
		// still refreshes the tooltip. Hierarchical mode is part of the
		// fingerprint because it changes collapsibleState.
		//
		// Phase 2 (debug-flicker): placeholder-state prefix distinguishes
		// "cold-start offline + empty" (shows PlaceholderTreeItem) from
		// "no SAP rows found" (empty tree). Ensures the tree re-fires on
		// recovery even when the recovered list is also empty.
		const placeholder = lastRefreshFailed && systems.length === 0;
		const parts: string[] = [placeholder ? 'P' : 'N', this.hierarchical ? 'H' : 'F'];
		for (const s of systems) {
			parts.push([
				s.key,
				s.status,
				s.imported ? 'I' : 'D',
				s.vsp?.status ?? '',
				s.gui?.status ?? '',
				s.vsp ? String(s.vsp.restart_count) : '',
				s.gui ? String(s.gui.restart_count) : '',
				s.vsp?.pid !== undefined ? String(s.vsp.pid) : '',
				s.gui?.pid !== undefined ? String(s.gui.pid) : '',
				s.vsp?.last_error ?? '',
				s.gui?.last_error ?? '',
			].join('|'));
		}
		return parts.join(';');
	}

	dispose(): void {
		this._disposed = true;
		this.lastFingerprint = null;
		this.subscription.dispose();
		this.configSubscription.dispose();
		this._onDidChangeTreeData.dispose();
	}
}
