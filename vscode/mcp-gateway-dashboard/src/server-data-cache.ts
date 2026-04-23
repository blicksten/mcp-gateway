import * as vscode from 'vscode';
import type { IGatewayClient } from './extension';
import type { ServerView } from './types';
import { groupSapSystems, synthesizeKeepassSapSystems, compareByName, type SapSystem } from './sap-detector';

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
	// F-2 (Phase 17 audit): when a caller triggers refresh() while one is
	// already in flight, remember it and re-run once the in-flight finishes.
	// Without this, a config-change triggered refresh can be silently dropped
	// and the toggle effect is delayed by up to one poll tick.
	private pendingRefresh = false;
	private _lastRefreshFailed = false;

	constructor(client: IGatewayClient, importedProvider?: ImportedSystemsProvider) {
		this.client = client;
		this.importedProvider = importedProvider;
	}

	async refresh(): Promise<void> {
		if (this.disposed) { return; }
		if (this.refreshInFlight) {
			// F-2: do not drop the call — re-queue it so a config-change
			// driven refresh still runs after the currently in-flight poll.
			this.pendingRefresh = true;
			return;
		}
		this.refreshInFlight = true;
		try {
			try {
				const raw = await this.client.listServers();
				this.cachedServers = raw as ServerView[];
				this._lastRefreshFailed = false;
			} catch {
				// Preserve last-known-good data on transient API errors. This
				// keeps tree views stable (same fingerprint → no re-render) and
				// avoids flicker when the daemon momentarily drops (auto-start
				// race, brief network hiccup, circuit breaker open). Cold-start
				// starts from `cachedServers = []`, so the UI correctly shows
				// nothing until the first successful refresh.
				//
				// Consumers distinguish daemon-offline from genuinely empty
				// server lists via `lastRefreshFailed=true` (e.g., status bar
				// flips to "offline", slash-command-generator skips orphan
				// cleanup).
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
						(a, b) => compareByName(a.key, b.key),
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

		// F-2: drain a re-queued refresh before returning control. This keeps
		// the config-change path deterministic ("toggle → next refresh cycle
		// reflects the new value") instead of silently waiting up to one poll
		// tick.
		if (this.pendingRefresh && !this.disposed) {
			this.pendingRefresh = false;
			await this.refresh();
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
