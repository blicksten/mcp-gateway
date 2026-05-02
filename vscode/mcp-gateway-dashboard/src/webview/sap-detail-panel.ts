import * as crypto from 'node:crypto';
import * as vscode from 'vscode';
import type { SapSystem } from '../sap-detector';
import type { CredentialStore } from '../credential-store';
import type { IGatewayClient } from '../extension';
import { buildSapDetailHtml, buildRemovedHtml } from './html-builder';
import { logger } from '../logger';

const ALLOWED_ACTIONS = new Set(['restart', 'showLogs']);
const ALLOWED_COMPONENTS = new Set(['vsp', 'gui']);
const SERVER_NAME_RE = /^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$/;

/** Auto-close grace period after showRemoved() (B-NEW-20). Mirrors the value
 *  in server-detail-panel.ts; exported for test assertions. */
export const REMOVED_AUTO_CLOSE_MS = 3_000;

export class SapDetailPanel {
	private static readonly panels = new Map<string, SapDetailPanel>();

	private readonly panel: vscode.WebviewPanel;
	private readonly systemKey: string;
	private readonly credentialStore: CredentialStore;
	private readonly client: IGatewayClient;
	private disposed = false;
	/** Latch so the render-error toast fires at most once per panel session (B-15). */
	private renderErrorNotified = false;
	/** Phase 8 (B-NEW-20) — once told its system was removed, the panel
	 *  rejects further update() calls and only `dispose()` advances state. */
	private removed = false;
	private removedTimer: NodeJS.Timeout | undefined;

	private constructor(
		panel: vscode.WebviewPanel,
		systemKey: string,
		credentialStore: CredentialStore,
		client: IGatewayClient,
	) {
		this.panel = panel;
		this.systemKey = systemKey;
		this.credentialStore = credentialStore;
		this.client = client;

		this.panel.onDidDispose(() => {
			this.disposed = true;
			if (this.removedTimer) {
				clearTimeout(this.removedTimer);
				this.removedTimer = undefined;
			}
			SapDetailPanel.panels.delete(this.systemKey);
		});

		this.panel.webview.onDidReceiveMessage((msg: unknown) => {
			this._handleMessage(msg);
		});
	}

	static async createOrShow(
		extensionUri: vscode.Uri,
		system: SapSystem,
		credentialStore: CredentialStore,
		client: IGatewayClient,
	): Promise<SapDetailPanel> {
		const existing = SapDetailPanel.panels.get(system.key);
		if (existing && !existing.disposed) {
			existing.panel.reveal();
			await existing._render(system);
			return existing;
		}

		const panel = vscode.window.createWebviewPanel(
			'mcpSapDetail',
			`SAP ${system.key}`,
			vscode.ViewColumn.One,
			{ enableScripts: true, localResourceRoots: [extensionUri] },
		);

		const instance = new SapDetailPanel(panel, system.key, credentialStore, client);
		SapDetailPanel.panels.set(system.key, instance);
		await instance._render(system);
		return instance;
	}

	async update(system: SapSystem): Promise<void> {
		if (this.disposed || this.removed) { return; }
		await this._render(system);
	}

	/** Phase 8 (B-NEW-20) — render a "system removed" banner and schedule
	 *  panel disposal after a short grace period. Idempotent.
	 *
	 *  Timer scheduled in `finally` so a render-time exception cannot leave
	 *  the panel stuck without an auto-close (PAL fallback finding F-1). */
	showRemoved(): void {
		if (this.disposed || this.removed) { return; }
		this.removed = true;
		try {
			const nonce = this._getNonce();
			this.panel.webview.html = buildRemovedHtml(
				this.systemKey,
				'sap',
				nonce,
				this.panel.webview.cspSource,
			);
		} catch (err) {
			logger.error(
				'sap-detail-panel',
				`Failed to render removed banner for system '${this.systemKey}'`,
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
							'sap-detail-panel',
							`dispose() after removal failed for '${this.systemKey}'`,
							err,
						);
					}
				}
			}, REMOVED_AUTO_CLOSE_MS);
		}
	}

	private async _render(system: SapSystem): Promise<void> {
		if (this.disposed) { return; }
		try {
			const nonce = this._getNonce();

			const vspCreds = system.vsp
				? await this.credentialStore.getServerCredentials(system.vsp.name)
				: { env: {}, headers: {} };
			if (this.disposed) { return; }
			const guiCreds = system.gui
				? await this.credentialStore.getServerCredentials(system.gui.name)
				: { env: {}, headers: {} };
			if (this.disposed) { return; }

			this.panel.webview.html = buildSapDetailHtml({
				system,
				vspCredentialKeys: { env: Object.keys(vspCreds.env), headers: Object.keys(vspCreds.headers) },
				guiCredentialKeys: { env: Object.keys(guiCreds.env), headers: Object.keys(guiCreds.headers) },
				nonce,
				cspSource: this.panel.webview.cspSource,
			});
		} catch (err) {
			logger.error('sap-detail-panel', `Render failed for system '${system.key}'`, err);
			if (!this.renderErrorNotified) {
				this.renderErrorNotified = true;
				void vscode.window.showWarningMessage(
					`MCP Gateway: failed to render SAP detail panel for '${system.key}'. Check the Output channel for details.`,
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
		const component = typeof m.component === 'string' && ALLOWED_COMPONENTS.has(m.component)
			? m.component : undefined;
		vscode.commands.executeCommand(`mcpGateway._webviewAction`, {
			action: m.action,
			serverName: m.serverName,
			component,
		});
	}

	/** Update all open panels with refreshed SAP system data.
	 *  Phase 8 (B-NEW-20): when an open panel's system is no longer in the
	 *  refreshed list, switch the panel into the removed-banner state. */
	static async updateAll(systems: SapSystem[]): Promise<void> {
		const byKey = new Map(systems.map((s) => [s.key, s]));
		const promises: Promise<void>[] = [];
		for (const [key, panel] of SapDetailPanel.panels) {
			const system = byKey.get(key);
			if (system) {
				promises.push(panel.update(system));
			} else {
				panel.showRemoved();
			}
		}
		await Promise.all(promises);
	}

	/** Clear all tracked panels (for testing). */
	static _clearPanels(): void {
		SapDetailPanel.panels.clear();
	}
}
