import * as crypto from 'node:crypto';
import * as vscode from 'vscode';
import type { CredentialStore } from '../credential-store';
import type { IGatewayClient } from '../extension';
import {
	detectTransport,
	parseEnvEntry,
	parseHeaderEntry,
	validateEnvEntry,
	validateHeaderEntry,
	validateServerName,
	validateStdioCommand,
	validateUrl,
} from '../validation';
import { buildAddServerHtml } from './html-builder';

interface SubmitPayload {
	name: string;
	target: string;
	// Transport is derived from target via detectTransport — the webview's
	// transport field (if any) is ignored to prevent a crafted payload from
	// producing a confusing "URL must use http:" error for a stdio-shaped path.
	transport: 'http' | 'stdio';
	env: string[];
	headers: string[];
}

/**
 * Webview form for adding a new MCP server.
 * Replaces the sequential {@link vscode.window.showInputBox} flow with a single
 * form that collects name, URL/command, env vars and headers at once.
 *
 * Trust boundary: the webview script performs client-side validation for UX,
 * but the extension host re-validates every field via the shared
 * {@link ../validation} helpers before touching the gateway — see
 * {@link AddServerPanel.handleSubmit}.
 */
export class AddServerPanel {
	private static current: AddServerPanel | undefined;

	private readonly panel: vscode.WebviewPanel;
	private readonly client: IGatewayClient;
	private readonly credentialStore: CredentialStore;
	// Mutable so a second createOrShow can refresh the callback even when the
	// existing panel is revealed — avoids silently dropping a new caller's
	// onCreated (fallback fixed F-02: stale-callback-on-reveal).
	private onCreated: () => void;
	private readonly disposables: vscode.Disposable[] = [];
	private disposed = false;
	// In-flight guard for handleSubmit: prevents double server creation when a
	// second submit postMessage is delivered while the first addServer call is
	// still awaiting the daemon response (fallback fixed F-01).
	private submitting = false;

	private constructor(
		panel: vscode.WebviewPanel,
		client: IGatewayClient,
		credentialStore: CredentialStore,
		onCreated: () => void,
	) {
		this.panel = panel;
		this.client = client;
		this.credentialStore = credentialStore;
		this.onCreated = onCreated;

		this.disposables.push(this.panel.onDidDispose(() => this.dispose()));
		this.disposables.push(this.panel.webview.onDidReceiveMessage((msg: unknown) => {
			void this.handleMessage(msg);
		}));
	}

	static async createOrShow(
		extensionUri: vscode.Uri,
		client: IGatewayClient,
		credentialStore: CredentialStore,
		onCreated: () => void,
	): Promise<AddServerPanel> {
		if (AddServerPanel.current && !AddServerPanel.current.disposed) {
			// Refresh callback on re-reveal so the most recent caller's
			// onCreated runs on successful submit.
			AddServerPanel.current.onCreated = onCreated;
			AddServerPanel.current.panel.reveal();
			return AddServerPanel.current;
		}

		const panel = vscode.window.createWebviewPanel(
			'mcpAddServer',
			'Add MCP Server',
			vscode.ViewColumn.One,
			{ enableScripts: true, localResourceRoots: [extensionUri], retainContextWhenHidden: true },
		);

		const instance = new AddServerPanel(panel, client, credentialStore, onCreated);
		AddServerPanel.current = instance;
		instance.render();
		return instance;
	}

	private render(): void {
		const nonce = crypto.randomBytes(16).toString('base64');
		this.panel.webview.html = buildAddServerHtml(nonce, this.panel.webview.cspSource);
	}

	private async handleMessage(msg: unknown): Promise<void> {
		if (!msg || typeof msg !== 'object') { return; }
		const m = msg as Record<string, unknown>;
		if (m.type === 'cancel') {
			this.dispose();
			return;
		}
		if (m.type !== 'submit') { return; }
		await this.handleSubmit(m.payload);
	}

	private async handleSubmit(payload: unknown): Promise<void> {
		if (this.submitting) { return; }
		this.submitting = true;
		let succeeded = false;
		let serverName = '';
		try {
			const parsed = AddServerPanel.coercePayload(payload);
			if (!parsed.ok) {
				await this.postNack(parsed.error);
				return;
			}
			const p = parsed.value;

			// Re-validate server-side — never trust webview input (REFINEMENT C-4).
			const nameErr = validateServerName(p.name);
			if (nameErr) { await this.postNack(nameErr); return; }

			const targetErr = p.transport === 'http'
				? validateUrl(p.target)
				: validateStdioCommand(p.target);
			if (targetErr) { await this.postNack(targetErr); return; }

			for (const e of p.env) {
				const err = validateEnvEntry(e);
				if (err) { await this.postNack(`Env var error: ${err}`); return; }
			}
			for (const h of p.headers) {
				const err = validateHeaderEntry(h);
				if (err) { await this.postNack(`Header error: ${err}`); return; }
			}

			const config: Record<string, unknown> = {};
			if (p.transport === 'http') {
				config.url = p.target;
			} else {
				config.command = p.target;
			}
			if (p.env.length > 0) {
				config.env = [...p.env];
			}
			if (p.headers.length > 0) {
				const headerMap: Record<string, string> = {};
				for (const entry of p.headers) {
					const parsedHeader = parseHeaderEntry(entry);
					if (parsedHeader) { headerMap[parsedHeader.name] = parsedHeader.value; }
				}
				config.headers = headerMap;
			}

			try {
				await this.client.addServer(p.name, config);
			} catch (err) {
				await this.postNack(`Failed to add server: ${errorMsg(err)}`);
				return;
			}

			// Partial-failure tolerant credential indexing — matches prior addServer behavior.
			// A credential store failure must NOT hide a successful server registration.
			for (const entry of p.env) {
				const parsedEnv = parseEnvEntry(entry);
				if (!parsedEnv) { continue; }
				try {
					await this.credentialStore.storeEnvVar(p.name, parsedEnv.key, parsedEnv.value);
				} catch (credErr) {
					vscode.window.showWarningMessage(
						`Server "${p.name}" added, but failed to index credential "${parsedEnv.key}": ${errorMsg(credErr)}`);
				}
			}
			for (const entry of p.headers) {
				const parsedHeader = parseHeaderEntry(entry);
				if (!parsedHeader) { continue; }
				try {
					await this.credentialStore.storeHeader(p.name, parsedHeader.name, parsedHeader.value);
				} catch (credErr) {
					vscode.window.showWarningMessage(
						`Server "${p.name}" added, but failed to index header "${parsedHeader.name}": ${errorMsg(credErr)}`);
				}
			}

			succeeded = true;
			serverName = p.name;
		} finally {
			this.submitting = false;
		}

		if (succeeded) {
			// Capture the callback before dispose() so a potential synchronous
			// exception inside onCreated cannot leak the panel (fallback fixed F-03).
			const callback = this.onCreated;
			vscode.window.showInformationMessage(`Server "${serverName}" added.`);
			this.dispose();
			try { callback(); } catch (cbErr) {
				vscode.window.showErrorMessage(`Server "${serverName}" added, but refresh failed: ${errorMsg(cbErr)}`);
			}
		}
	}

	private async postNack(error: string): Promise<void> {
		if (this.disposed) { return; }
		await this.panel.webview.postMessage({ type: 'nack', error });
	}

	/**
	 * Coerce the untrusted webview submit payload into a typed {@link SubmitPayload}.
	 * Returns a string error on shape mismatch so the panel can surface it to the
	 * user rather than crashing the extension host.
	 */
	private static coercePayload(raw: unknown):
		| { ok: true; value: SubmitPayload }
		| { ok: false; error: string }
	{
		if (!raw || typeof raw !== 'object') { return { ok: false, error: 'Malformed submit payload.' }; }
		const r = raw as Record<string, unknown>;
		if (typeof r.name !== 'string') { return { ok: false, error: 'Name must be a string.' }; }
		if (typeof r.target !== 'string') { return { ok: false, error: 'Target must be a string.' }; }
		const target = r.target.trim();
		// Always recompute transport from the target string. Ignore any
		// webview-supplied transport field — otherwise a crafted payload
		// could set `transport: 'http'` with a stdio-shaped target and
		// surface a confusing URL error (fallback fixed F-05).
		const transport = detectTransport(target);
		const envRaw = Array.isArray(r.env) ? r.env : [];
		const headersRaw = Array.isArray(r.headers) ? r.headers : [];
		const env: string[] = [];
		for (const e of envRaw) {
			if (typeof e !== 'string') { return { ok: false, error: 'Env entries must be strings.' }; }
			if (e.trim()) { env.push(e.trim()); }
		}
		const headers: string[] = [];
		for (const h of headersRaw) {
			if (typeof h !== 'string') { return { ok: false, error: 'Header entries must be strings.' }; }
			if (h.trim()) { headers.push(h.trim()); }
		}
		return {
			ok: true,
			value: { name: r.name.trim(), target, transport, env, headers },
		};
	}

	dispose(): void {
		if (this.disposed) { return; }
		this.disposed = true;
		if (AddServerPanel.current === this) {
			AddServerPanel.current = undefined;
		}
		while (this.disposables.length > 0) {
			const d = this.disposables.pop();
			try { d?.dispose(); } catch { /* best-effort cleanup */ }
		}
		try { this.panel.dispose(); } catch { /* panel may already be disposed */ }
	}

	/** Reset the singleton (for testing). */
	static _reset(): void {
		if (AddServerPanel.current && !AddServerPanel.current.disposed) {
			AddServerPanel.current.dispose();
		}
		AddServerPanel.current = undefined;
	}
}

function errorMsg(err: unknown): string {
	if (err instanceof Error) { return err.message; }
	if (typeof err === 'object' && err !== null) { return JSON.stringify(err); }
	return String(err);
}
