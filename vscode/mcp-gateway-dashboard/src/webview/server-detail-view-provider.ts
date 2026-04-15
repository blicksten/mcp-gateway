import * as crypto from 'node:crypto';
import * as vscode from 'vscode';
import type { ServerView } from '../types';
import type { SapSystem } from '../sap-detector';
import type { CredentialStore } from '../credential-store';
import type { ServerDataCache } from '../server-data-cache';
import { buildDetailPlaceholderHtml, buildMcpDetailHtml, buildSapDetailHtml } from './html-builder';

const ALLOWED_MCP_ACTIONS = new Set(['restart', 'showLogs', 'resetCircuit', 'enable', 'disable']);
const ALLOWED_SAP_ACTIONS = new Set(['restart', 'showLogs']);
const ALLOWED_SAP_COMPONENTS = new Set(['vsp', 'gui']);
const SERVER_NAME_RE = /^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$/;

type Selection =
	| { kind: 'mcp'; name: string }
	| { kind: 'sap'; key: string }
	| null;

/**
 * Sidebar WebviewView provider showing details for the currently-selected
 * tree item. Replaces the click-toggle UX pitfall of WebviewPanel (VS Code
 * issues #34130, #51536, #77418, #85636, #105256) with an always-visible
 * sidebar that reacts to tree selection + cache refresh events.
 */
export class ServerDetailViewProvider implements vscode.WebviewViewProvider {
	public static readonly viewType = 'mcpGateway.serverDetail';

	private view: vscode.WebviewView | undefined;
	private extensionUri: vscode.Uri;
	private selection: Selection = null;
	private readonly disposables: vscode.Disposable[] = [];
	// Generation counter used to discard stale async renders. Every call to
	// render() captures mySeq = ++renderSeq; if renderSeq has advanced by the
	// time the credential fetch resolves, the render aborts before writing
	// webview.html — preventing older selections from clobbering newer ones.
	private renderSeq = 0;

	constructor(
		extensionUri: vscode.Uri,
		private readonly cache: ServerDataCache,
		private readonly credentialStore: CredentialStore,
	) {
		this.extensionUri = extensionUri;
		// Re-render on cache refresh so the currently-selected server's fields
		// stay in sync with the daemon state.
		this.disposables.push(this.cache.onDidRefresh(() => {
			void this.render();
		}));
	}

	resolveWebviewView(view: vscode.WebviewView): void {
		if (this.view) {
			// Idempotent: VS Code may call resolveWebviewView again after the
			// view is hidden and re-shown. Dispose the previous subscription
			// before re-binding to avoid leaking handlers.
			this.view = undefined;
		}
		this.view = view;
		view.webview.options = {
			enableScripts: true,
			localResourceRoots: [this.extensionUri],
		};
		// Store the message-handler disposable so provider.dispose() tears it
		// down explicitly — VS Code auto-cleans webview resources, but binding
		// to our own disposables list keeps the contract explicit and prevents
		// leaks if the provider outlives the webview.
		this.disposables.push(view.webview.onDidReceiveMessage((msg: unknown) => {
			this.handleMessage(msg);
		}));
		this.disposables.push(view.onDidDispose(() => {
			this.view = undefined;
		}));
		// Initial paint.
		void this.render();
	}

	/** Update selection to an MCP server (or clear with null). */
	setMcpSelection(server: ServerView | null): void {
		this.selection = server ? { kind: 'mcp', name: server.name } : null;
		void this.render();
	}

	/** Update selection to a SAP system (or clear with null). */
	setSapSelection(system: SapSystem | null): void {
		this.selection = system ? { kind: 'sap', key: system.key } : null;
		void this.render();
	}

	/** Clear current selection and show placeholder. */
	clearSelection(): void {
		this.selection = null;
		void this.render();
	}

	private async render(): Promise<void> {
		if (!this.view) { return; }
		const mySeq = ++this.renderSeq;
		const webview = this.view.webview;
		const nonce = crypto.randomBytes(16).toString('base64');
		const cspSource = webview.cspSource;

		// Capture selection as a local so TypeScript narrowing survives the
		// async boundary (this.selection can mutate between awaits).
		const sel = this.selection;
		if (!sel) {
			if (this.renderSeq !== mySeq || !this.view) { return; }
			webview.html = buildDetailPlaceholderHtml(nonce, cspSource);
			return;
		}

		if (sel.kind === 'mcp') {
			const server = this.cache.getMcpServers().find((s) => s.name === sel.name);
			if (!server) {
				// Selected server vanished from the daemon — fall back to placeholder.
				if (this.renderSeq !== mySeq || !this.view) { return; }
				webview.html = buildDetailPlaceholderHtml(nonce, cspSource);
				return;
			}
			const creds = await this.credentialStore.getServerCredentials(server.name);
			// A newer render() started while we were awaiting credentials — abort.
			if (this.renderSeq !== mySeq || !this.view) { return; }
			webview.html = buildMcpDetailHtml({
				server,
				credentialKeys: {
					env: Object.keys(creds.env),
					headers: Object.keys(creds.headers),
				},
				nonce,
				cspSource,
			});
			return;
		}

		// SAP selection
		const system = this.cache.getSapSystems().find((s) => s.key === sel.key);
		if (!system) {
			if (this.renderSeq !== mySeq || !this.view) { return; }
			webview.html = buildDetailPlaceholderHtml(nonce, cspSource);
			return;
		}
		const vspCreds = system.vsp
			? await this.credentialStore.getServerCredentials(system.vsp.name)
			: { env: {}, headers: {} };
		if (this.renderSeq !== mySeq || !this.view) { return; }
		const guiCreds = system.gui
			? await this.credentialStore.getServerCredentials(system.gui.name)
			: { env: {}, headers: {} };
		if (this.renderSeq !== mySeq || !this.view) { return; }
		webview.html = buildSapDetailHtml({
			system,
			vspCredentialKeys: { env: Object.keys(vspCreds.env), headers: Object.keys(vspCreds.headers) },
			guiCredentialKeys: { env: Object.keys(guiCreds.env), headers: Object.keys(guiCreds.headers) },
			nonce,
			cspSource,
		});
	}

	private handleMessage(msg: unknown): void {
		if (!msg || typeof msg !== 'object') { return; }
		const m = msg as Record<string, unknown>;
		if (m.type !== 'action') { return; }
		if (typeof m.action !== 'string') { return; }

		if (!this.selection) { return; }

		if (this.selection.kind === 'mcp') {
			if (!ALLOWED_MCP_ACTIONS.has(m.action)) { return; }
			const name = typeof m.serverName === 'string' ? m.serverName : this.selection.name;
			if (!SERVER_NAME_RE.test(name)) { return; }
			vscode.commands.executeCommand('mcpGateway._webviewAction', {
				action: m.action,
				serverName: name,
			});
			return;
		}

		// SAP
		if (!ALLOWED_SAP_ACTIONS.has(m.action)) { return; }
		if (typeof m.serverName !== 'string' || !SERVER_NAME_RE.test(m.serverName)) { return; }
		const component = typeof m.component === 'string' && ALLOWED_SAP_COMPONENTS.has(m.component)
			? m.component : undefined;
		vscode.commands.executeCommand('mcpGateway._webviewAction', {
			action: m.action,
			serverName: m.serverName,
			component,
		});
	}

	dispose(): void {
		for (const d of this.disposables) {
			d.dispose();
		}
		this.view = undefined;
		this.selection = null;
	}
}
