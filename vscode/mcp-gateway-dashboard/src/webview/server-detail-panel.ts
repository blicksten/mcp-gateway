import * as crypto from 'node:crypto';
import * as vscode from 'vscode';
import type { ServerView } from '../types';
import type { CredentialStore } from '../credential-store';
import type { IGatewayClient } from '../extension';
import { buildMcpDetailHtml, buildRemovedHtml } from './html-builder';
import { logger } from '../logger';

const ALLOWED_ACTIONS = new Set(['restart', 'showLogs', 'resetCircuit', 'enable', 'disable']);
const SERVER_NAME_RE = /^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$/;

/** Auto-close grace period after showRemoved() (B-NEW-20). Exposed so tests
 *  can assert the disposal timing without sleeping. */
export const REMOVED_AUTO_CLOSE_MS = 3_000;

export class ServerDetailPanel {
	private static readonly panels = new Map<string, ServerDetailPanel>();

	private readonly panel: vscode.WebviewPanel;
	private readonly serverName: string;
	private readonly credentialStore: CredentialStore;
	private readonly client: IGatewayClient;
	private disposed = false;
	/** Latch so the render-error toast fires at most once per panel session (B-15). */
	private renderErrorNotified = false;
	/** Phase 8 (B-NEW-20) — once a panel has been told its server was removed,
	 *  later updateAll() / update() calls become no-ops; only dispose advances state. */
	private removed = false;
	private removedTimer: NodeJS.Timeout | undefined;

	private constructor(
		panel: vscode.WebviewPanel,
		serverName: string,
		credentialStore: CredentialStore,
		client: IGatewayClient,
	) {
		this.panel = panel;
		this.serverName = serverName;
		this.credentialStore = credentialStore;
		this.client = client;

		this.panel.onDidDispose(() => {
			this.disposed = true;
			if (this.removedTimer) {
				clearTimeout(this.removedTimer);
				this.removedTimer = undefined;
			}
			ServerDetailPanel.panels.delete(this.serverName);
		});

		this.panel.webview.onDidReceiveMessage((msg: unknown) => {
			this._handleMessage(msg);
		});
	}

	static async createOrShow(
		extensionUri: vscode.Uri,
		server: ServerView,
		credentialStore: CredentialStore,
		client: IGatewayClient,
	): Promise<ServerDetailPanel> {
		const existing = ServerDetailPanel.panels.get(server.name);
		if (existing && !existing.disposed) {
			existing.panel.reveal();
			await existing._render(server);
			return existing;
		}

		const panel = vscode.window.createWebviewPanel(
			'mcpServerDetail',
			server.name,
			vscode.ViewColumn.One,
			{ enableScripts: true, localResourceRoots: [extensionUri] },
		);

		const instance = new ServerDetailPanel(panel, server.name, credentialStore, client);
		ServerDetailPanel.panels.set(server.name, instance);
		await instance._render(server);
		return instance;
	}

	/** Refresh panel content with provided server data. */
	async update(server: ServerView): Promise<void> {
		if (this.disposed || this.removed) { return; }
		await this._render(server);
	}

	/** Phase 8 (B-NEW-20) — render a "server removed" banner with disabled
	 *  action buttons and schedule the panel to auto-dispose after a short
	 *  grace period. Idempotent: repeat calls are no-ops. The webview action
	 *  handler still rejects messages because the buttons in the banner HTML
	 *  do not post any.
	 *
	 *  The flag is set up-front to block concurrent re-entry, and the timer
	 *  is scheduled in `finally` so a render-time exception cannot leave the
	 *  panel stuck without an auto-close (PAL fallback finding F-1). */
	showRemoved(): void {
		if (this.disposed || this.removed) { return; }
		this.removed = true;
		try {
			const nonce = this._getNonce();
			this.panel.webview.html = buildRemovedHtml(
				this.serverName,
				'server',
				nonce,
				this.panel.webview.cspSource,
			);
		} catch (err) {
			logger.error(
				'server-detail-panel',
				`Failed to render removed banner for '${this.serverName}'`,
				err,
			);
		} finally {
			this.removedTimer = setTimeout(() => {
				this.removedTimer = undefined;
				if (!this.disposed) {
					try {
						this.panel.dispose();
					} catch (err) {
						logger.error(
							'server-detail-panel',
							`dispose() after removal failed for '${this.serverName}'`,
							err,
						);
					}
				}
			}, REMOVED_AUTO_CLOSE_MS);
		}
	}

	private async _render(server: ServerView): Promise<void> {
		if (this.disposed) { return; }
		try {
			const nonce = this._getNonce();
			const creds = await this.credentialStore.getServerCredentials(server.name);
			if (this.disposed) { return; }
			const credentialKeys = {
				env: Object.keys(creds.env),
				headers: Object.keys(creds.headers),
			};
			this.panel.webview.html = buildMcpDetailHtml({
				server,
				credentialKeys,
				nonce,
				cspSource: this.panel.webview.cspSource,
			});
		} catch (err) {
			logger.error('server-detail-panel', `Render failed for server '${server.name}'`, err);
			if (!this.renderErrorNotified) {
				this.renderErrorNotified = true;
				void vscode.window.showWarningMessage(
					`MCP Gateway: failed to render detail panel for '${server.name}'. Check the Output channel for details.`,
				);
			}
		}
	}

	private _getNonce(): string {
		return crypto.randomBytes(16).toString('base64');
	}

	private _handleMessage(msg: unknown): void {
		if (!msg || typeof msg !== 'object') { return; }
		const m = msg as Record<string, unknown>;
		if (m.type !== 'action') { return; }
		if (typeof m.action !== 'string' || !ALLOWED_ACTIONS.has(m.action)) { return; }
		if (m.serverName !== undefined) {
			if (typeof m.serverName !== 'string' || !SERVER_NAME_RE.test(m.serverName)) { return; }
		}
		vscode.commands.executeCommand(`mcpGateway._webviewAction`, {
			action: m.action,
			serverName: m.serverName ?? this.serverName,
		});
	}

	/** Update all open panels with refreshed server data from cache.
	 *  Phase 8 (B-NEW-20): when an open panel's server is no longer in the
	 *  refreshed list, switch the panel into the removed-banner state instead
	 *  of leaving stale data on screen. */
	static async updateAll(servers: ServerView[]): Promise<void> {
		const byName = new Map(servers.map((s) => [s.name, s]));
		const promises: Promise<void>[] = [];
		for (const [name, panel] of ServerDetailPanel.panels) {
			const server = byName.get(name);
			if (server) {
				promises.push(panel.update(server));
			} else {
				panel.showRemoved();
			}
		}
		await Promise.all(promises);
	}

	/** Clear all tracked panels (for testing). */
	static _clearPanels(): void {
		ServerDetailPanel.panels.clear();
	}
}
