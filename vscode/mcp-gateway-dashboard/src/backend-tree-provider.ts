import * as vscode from 'vscode';
import type { ServerDataCache } from './server-data-cache';
import { BackendItem } from './backend-item';

export class BackendTreeProvider implements vscode.TreeDataProvider<vscode.TreeItem>, vscode.Disposable {
	private readonly _onDidChangeTreeData = new vscode.EventEmitter<vscode.TreeItem | undefined | void>();
	readonly onDidChangeTreeData = this._onDidChangeTreeData.event;

	private readonly cache: ServerDataCache;
	private readonly subscription: vscode.Disposable;
	private _disposed = false;

	constructor(cache: ServerDataCache) {
		this.cache = cache;
		this.subscription = cache.onDidRefresh(() => this.refresh());
	}

	refresh(): void {
		if (this._disposed) { return; }
		this._onDidChangeTreeData.fire();
	}

	getChildren(element?: vscode.TreeItem): vscode.TreeItem[] {
		if (element) { return []; }
		const servers = this.cache.getMcpServers();
		return servers.map((s) => new BackendItem(s));
	}

	getTreeItem(element: vscode.TreeItem): vscode.TreeItem {
		return element;
	}

	dispose(): void {
		this._disposed = true;
		this.subscription.dispose();
		this._onDidChangeTreeData.dispose();
	}
}
