import * as vscode from 'vscode';
import type { ServerDataCache } from './server-data-cache';
import { BackendItem } from './backend-item';
import { PlaceholderTreeItem } from './tree-placeholder';
import type { ServerView } from './types';

export class BackendTreeProvider implements vscode.TreeDataProvider<vscode.TreeItem>, vscode.Disposable {
	private readonly _onDidChangeTreeData = new vscode.EventEmitter<vscode.TreeItem | undefined | void>();
	readonly onDidChangeTreeData = this._onDidChangeTreeData.event;

	private readonly cache: ServerDataCache;
	private readonly subscription: vscode.Disposable;
	private _disposed = false;
	private lastFingerprint: string | null = null;

	constructor(cache: ServerDataCache) {
		this.cache = cache;
		this.subscription = cache.onDidRefresh(() => this.refresh());
	}

	refresh(): void {
		if (this._disposed) { return; }
		const servers = this.cache.getMcpServers();
		const next = this.computeFingerprint(servers, this.cache.lastRefreshFailed);
		if (next === this.lastFingerprint) { return; }
		this.lastFingerprint = next;
		this._onDidChangeTreeData.fire();
	}

	getChildren(element?: vscode.TreeItem): vscode.TreeItem[] {
		if (element) { return []; }
		const servers = this.cache.getMcpServers();
		if (this.cache.lastRefreshFailed && servers.length === 0) {
			return [new PlaceholderTreeItem()];
		}
		return servers.map((s) => new BackendItem(s));
	}

	getTreeItem(element: vscode.TreeItem): vscode.TreeItem {
		return element;
	}

	// Exposed for tests; not part of the public TreeDataProvider contract.
	getFingerprint(): string | null {
		return this.lastFingerprint;
	}

	private computeFingerprint(servers: readonly ServerView[], lastRefreshFailed: boolean): string {
		// Render-affecting fields only: tree rows depend on name, status, transport,
		// restart_count (shown in description), pid and last_error (tooltip), and
		// tools count (tooltip "Tools: N"). Full tools array is excluded to keep
		// the fingerprint cheap on large backends.
		//
		// Phase 2 (debug-flicker): placeholder-state prefix distinguishes
		// "cold-start offline + empty" (shows PlaceholderTreeItem) from
		// "healthy daemon with 0 backends" (shows empty tree). Without this,
		// the cache transition from cold-start-failed to first-success-empty
		// would produce the same fingerprint and suppress the re-fire,
		// leaving the placeholder visible over an implicitly-empty list.
		const placeholder = lastRefreshFailed && servers.length === 0;
		const parts: string[] = [placeholder ? 'P' : 'N'];
		for (const s of servers) {
			parts.push([
				s.name,
				s.status,
				s.transport,
				String(s.restart_count),
				s.pid !== undefined ? String(s.pid) : '',
				s.last_error ?? '',
				String(s.tools?.length ?? 0),
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
