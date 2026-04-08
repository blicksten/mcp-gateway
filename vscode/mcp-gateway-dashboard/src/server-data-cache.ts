import * as vscode from 'vscode';
import type { IGatewayClient } from './extension';
import type { ServerView } from './types';
import { groupSapSystems, type SapSystem } from './sap-detector';

export class ServerDataCache implements vscode.Disposable {
	private readonly client: IGatewayClient;
	private readonly _onDidRefresh = new vscode.EventEmitter<ServerView[]>();
	readonly onDidRefresh = this._onDidRefresh.event;

	private cachedServers: ServerView[] = [];
	private cachedMcp: ServerView[] = [];
	private cachedSap: SapSystem[] = [];
	private timer: ReturnType<typeof setInterval> | undefined;
	private disposed = false;
	private refreshInFlight = false;

	constructor(client: IGatewayClient) {
		this.client = client;
	}

	async refresh(): Promise<void> {
		if (this.disposed || this.refreshInFlight) { return; }
		this.refreshInFlight = true;
		try {
			try {
				const raw = await this.client.listServers();
				this.cachedServers = raw as ServerView[];
			} catch {
				// Deliberate fail-clear: transient API errors clear all views.
				// This matches the pre-cache BackendTreeProvider behavior and is
				// specified in T3.10 ("client throws → fires event with empty data").
				this.cachedServers = [];
			}
			const { sap, mcp } = groupSapSystems(this.cachedServers);
			this.cachedMcp = mcp;
			this.cachedSap = sap;
			this._onDidRefresh.fire(this.cachedServers);
		} finally {
			this.refreshInFlight = false;
		}
	}

	getMcpServers(): ServerView[] {
		return this.cachedMcp;
	}

	getSapSystems(): SapSystem[] {
		return this.cachedSap;
	}

	getAllServers(): ServerView[] {
		return this.cachedServers;
	}

	startAutoRefresh(intervalMs: number): void {
		this.stopAutoRefresh();
		// Immediate first refresh.
		this.refresh().catch(() => {});
		this.timer = setInterval(() => {
			this.refresh().catch(() => {});
		}, intervalMs);
	}

	stopAutoRefresh(): void {
		if (this.timer !== undefined) {
			clearInterval(this.timer);
			this.timer = undefined;
		}
	}

	dispose(): void {
		this.disposed = true;
		this.stopAutoRefresh();
		this._onDidRefresh.dispose();
	}
}
