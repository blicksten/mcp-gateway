import * as vscode from 'vscode';
import type { IGatewayClient } from './extension';
import type { ServerView } from './types';
import { groupSapSystems, synthesizeKeepassSapSystems, type SapSystem } from './sap-detector';

export interface CacheRefreshPayload {
	servers: ServerView[];
	lastRefreshFailed: boolean;
}

/**
 * Phase 17.5 — Optional provider of credential-backed server names (KeePass,
 * OS keychain). When provided AND the user has enabled keepass integration,
 * the cache synthesizes additional SAP rows for names matching the SAP regex
 * that the daemon does not yet know about.
 */
export type ImportedSystemsProvider = () => readonly string[];

export class ServerDataCache implements vscode.Disposable {
	private readonly client: IGatewayClient;
	private readonly importedProvider: ImportedSystemsProvider | undefined;
	private readonly _onDidRefresh = new vscode.EventEmitter<CacheRefreshPayload>();
	readonly onDidRefresh = this._onDidRefresh.event;

	private cachedServers: ServerView[] = [];
	private cachedMcp: ServerView[] = [];
	private cachedSap: SapSystem[] = [];
	private timer: ReturnType<typeof setInterval> | undefined;
	private disposed = false;
	private refreshInFlight = false;
	private _lastRefreshFailed = false;

	constructor(client: IGatewayClient, importedProvider?: ImportedSystemsProvider) {
		this.client = client;
		this.importedProvider = importedProvider;
	}

	async refresh(): Promise<void> {
		if (this.disposed || this.refreshInFlight) { return; }
		this.refreshInFlight = true;
		try {
			try {
				const raw = await this.client.listServers();
				this.cachedServers = raw as ServerView[];
				this._lastRefreshFailed = false;
			} catch {
				// Deliberate fail-clear: transient API errors clear all views.
				// This matches the pre-cache BackendTreeProvider behavior and is
				// specified in T3.10 ("client throws → fires event with empty data").
				// lastRefreshFailed=true lets consumers distinguish daemon-offline
				// from genuinely empty server lists (e.g., slash-command orphan
				// cleanup must not run while the daemon is unreachable).
				this.cachedServers = [];
				this._lastRefreshFailed = true;
			}
			const { sap, mcp } = groupSapSystems(this.cachedServers);
			this.cachedMcp = mcp;
			this.cachedSap = sap;

			// Phase 17.5 — merge KeePass-imported SAP rows (if enabled).
			// The provider itself gates on the mcpGateway.keepassEnabled setting,
			// so the cache does not read configuration directly.
			//
			// CV-HIGH fix: provider exceptions must never crash refresh — a buggy
			// credential-store reader or corrupt globalState should degrade to
			// "daemon rows only" silently, not leave the UI stale with no event.
			if (this.importedProvider) {
				try {
					const importedNames = this.importedProvider();
					const existingKeys = new Set(sap.map((s) => s.key));
					const synthesized = synthesizeKeepassSapSystems(importedNames, existingKeys);
					this.cachedSap = [...sap, ...synthesized].sort(
						(a, b) => a.key.localeCompare(b.key),
					);
				} catch {
					// Non-fatal: keep daemon-only rows. Already assigned above.
				}
			}

			this._onDidRefresh.fire({
				servers: this.cachedServers,
				lastRefreshFailed: this._lastRefreshFailed,
			});
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

	get lastRefreshFailed(): boolean {
		return this._lastRefreshFailed;
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
