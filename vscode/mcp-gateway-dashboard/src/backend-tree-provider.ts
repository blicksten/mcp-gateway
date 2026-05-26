import * as vscode from 'vscode';
import type { ServerDataCache } from './server-data-cache';
import { BackendItem } from './backend-item';
import { PlaceholderTreeItem } from './tree-placeholder';
import type { ServerView } from './types';

/**
 * Footer item that USED to be appended to the backends tree to display
 * the mcp-gateway daemon version. Removed 2026-05-25 because operators
 * read it as a phantom server entry (visually adjacent to real backend
 * rows). The daemon version is still available via:
 *   - the MCP status-bar tooltip (`Gateway: …uptime… · v… · pid …`)
 *   - the REST endpoint `GET /api/v1/version`
 *
 * The class is retained — but no longer exported and never instantiated
 * — so older test code that imports the symbol still type-checks. New
 * code MUST NOT use this; the constructor exists only as a graveyard
 * marker.
 */
class GatewayVersionItem_Removed extends vscode.TreeItem {
	constructor(version: string | undefined) {
		super('mcp-gateway daemon', vscode.TreeItemCollapsibleState.None);
		this.description = version ? `v${version}` : 'version unknown';
		this.contextValue = 'gatewayVersion';
	}
}
// Keep the type alive for any historical import that escaped removal.
// Unreachable from production paths.
void GatewayVersionItem_Removed;

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
		// When the last refresh failed (gateway offline) but we still have
		// cached servers, mark them as stale so the UI does not mislead the
		// operator with green icons for servers that may no longer be running.
		const stale = this.cache.lastRefreshFailed;
		const items: vscode.TreeItem[] = servers.map((s) => new BackendItem(s, stale));
		// NOTE: the "mcp-gateway daemon" version footer was removed
		// 2026-05-25 because it visually appeared as a phantom server.
		// Daemon version is still exposed via the MCP status-bar tooltip
		// ("Gateway: …uptime… · v… · pid …") and `GET /api/v1/version`.
		return items;
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
		// Include lastRefreshFailed explicitly so the tree re-renders when the
		// gateway goes offline mid-session (stale icons need to become grey).
		const staleMark = lastRefreshFailed ? 'S' : '';
		// version removed from fingerprint 2026-05-25 along with the
		// "mcp-gateway daemon" footer (no longer renders to tree).
		const parts: string[] = [placeholder ? 'P' : 'N', staleMark];
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
