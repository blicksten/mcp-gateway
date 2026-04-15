import * as vscode from 'vscode';
import type { ServerDataCache } from './server-data-cache';
import { SapSystemItem } from './sap-item';
import type { SapSystem } from './sap-detector';

export class SapTreeProvider implements vscode.TreeDataProvider<SapSystemItem>, vscode.Disposable {
	private readonly _onDidChangeTreeData = new vscode.EventEmitter<void>();
	readonly onDidChangeTreeData = this._onDidChangeTreeData.event;

	private readonly cache: ServerDataCache;
	private readonly subscription: vscode.Disposable;
	private _disposed = false;
	private lastFingerprint: string | null = null;

	constructor(cache: ServerDataCache) {
		this.cache = cache;
		this.subscription = cache.onDidRefresh(() => this.refresh());
	}

	getTreeItem(element: SapSystemItem): SapSystemItem {
		return element;
	}

	getChildren(element?: SapSystemItem): SapSystemItem[] {
		if (element) { return []; } // flat list
		return this.cache.getSapSystems().map((sys) => new SapSystemItem(sys));
	}

	refresh(): void {
		if (this._disposed) { return; }
		const next = this.computeFingerprint(this.cache.getSapSystems());
		if (next === this.lastFingerprint) { return; }
		this.lastFingerprint = next;
		this._onDidChangeTreeData.fire();
	}

	// Exposed for tests; not part of the public TreeDataProvider contract.
	getFingerprint(): string | null {
		return this.lastFingerprint;
	}

	private computeFingerprint(systems: readonly SapSystem[]): string {
		// Mirrors BackendTreeProvider: must cover every render-affecting field on
		// the sub-servers so a silent process restart (new pid with same status)
		// still refreshes the tooltip.
		const parts: string[] = [];
		for (const s of systems) {
			parts.push([
				s.key,
				s.status,
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
		this._onDidChangeTreeData.dispose();
	}
}
