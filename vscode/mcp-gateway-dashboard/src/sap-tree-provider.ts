import * as vscode from 'vscode';
import type { ServerDataCache } from './server-data-cache';
import { SapSystemItem } from './sap-item';

export class SapTreeProvider implements vscode.TreeDataProvider<SapSystemItem>, vscode.Disposable {
	private readonly _onDidChangeTreeData = new vscode.EventEmitter<void>();
	readonly onDidChangeTreeData = this._onDidChangeTreeData.event;

	private readonly cache: ServerDataCache;
	private readonly subscription: vscode.Disposable;
	private _disposed = false;

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
		this._onDidChangeTreeData.fire();
	}

	dispose(): void {
		this._disposed = true;
		this.subscription.dispose();
		this._onDidChangeTreeData.dispose();
	}
}
