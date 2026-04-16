import * as crypto from 'node:crypto';
import * as vscode from 'vscode';
import type { IGatewayClient } from '../extension';
import type { ServerDataCache } from '../server-data-cache';
import { validateSapSid, validateSapClient, validateStdioCommand } from '../validation';
import { buildAddSapHtml } from './html-builder';

interface SubmitPayload {
	sid: string;
	client: string | null;
	components: { vsp: boolean; gui: boolean };
	vspCommand: string | null;
	guiCommand: string | null;
}

interface CreationResult {
	name: string;
	ok: boolean;
	error?: string;
}

/**
 * Webview form for adding a new SAP system. Creates up to two servers at
 * once (`vsp-<SID>[-<CLIENT>]` and `sap-gui-<SID>[-<CLIENT>]`) according to
 * the components the user selected. Uses the same trust-boundary pattern
 * as AddServerPanel: client-side validation for UX, extension-side
 * re-validation for authority.
 */
export class AddSapPanel {
	private static current: AddSapPanel | undefined;

	private readonly panel: vscode.WebviewPanel;
	private readonly client: IGatewayClient;
	private readonly cache: ServerDataCache;
	private onCreated: () => void;
	private readonly disposables: vscode.Disposable[] = [];
	private disposed = false;
	private submitting = false;
	// Keys the user has been warned about. A resubmit targeting any of these
	// keys is treated as confirmation and proceeds with duplicate-skipping.
	// Using a Set (instead of a single-slot string) so the user can switch
	// between conflicting SIDs and return to a previously-warned one without
	// having to re-dismiss the warning.
	private readonly warnedDuplicateKeys = new Set<string>();

	private constructor(
		panel: vscode.WebviewPanel,
		client: IGatewayClient,
		cache: ServerDataCache,
		onCreated: () => void,
	) {
		this.panel = panel;
		this.client = client;
		this.cache = cache;
		this.onCreated = onCreated;

		this.disposables.push(this.panel.onDidDispose(() => this.dispose()));
		this.disposables.push(this.panel.webview.onDidReceiveMessage((msg: unknown) => {
			void this.handleMessage(msg);
		}));
	}

	static async createOrShow(
		extensionUri: vscode.Uri,
		client: IGatewayClient,
		cache: ServerDataCache,
		onCreated: () => void,
	): Promise<AddSapPanel> {
		if (AddSapPanel.current && !AddSapPanel.current.disposed) {
			AddSapPanel.current.onCreated = onCreated;
			AddSapPanel.current.panel.reveal();
			return AddSapPanel.current;
		}

		const panel = vscode.window.createWebviewPanel(
			'mcpAddSapSystem',
			'Add SAP System',
			vscode.ViewColumn.One,
			{ enableScripts: true, localResourceRoots: [extensionUri], retainContextWhenHidden: true },
		);

		const instance = new AddSapPanel(panel, client, cache, onCreated);
		AddSapPanel.current = instance;
		instance.render();
		return instance;
	}

	private render(): void {
		const nonce = crypto.randomBytes(16).toString('base64');
		this.panel.webview.html = buildAddSapHtml(nonce, this.panel.webview.cspSource);
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
		try {
			const parsed = AddSapPanel.coercePayload(payload);
			if (!parsed.ok) {
				await this.postNack(parsed.error);
				return;
			}
			const p = parsed.value;

			// Re-validate server-side — never trust webview input.
			const sidErr = validateSapSid(p.sid);
			if (sidErr) { await this.postNack(sidErr); return; }
			const clientErr = validateSapClient(p.client ?? '');
			if (clientErr) { await this.postNack(clientErr); return; }
			if (!p.components.vsp && !p.components.gui) {
				await this.postNack('Select at least one component (VSP or GUI).');
				return;
			}

			// Re-validate executable paths for whichever components are selected.
			if (p.components.vsp) {
				if (!p.vspCommand) { await this.postNack('VSP executable is required when VSP is selected.'); return; }
				const vspCmdErr = validateStdioCommand(p.vspCommand);
				if (vspCmdErr) { await this.postNack(`VSP command: ${vspCmdErr}`); return; }
			}
			if (p.components.gui) {
				if (!p.guiCommand) { await this.postNack('GUI executable is required when GUI is selected.'); return; }
				const guiCmdErr = validateStdioCommand(p.guiCommand);
				if (guiCmdErr) { await this.postNack(`GUI command: ${guiCmdErr}`); return; }
			}

			const client = p.client ?? '';
			const suffix = client ? `-${p.sid}-${client}` : `-${p.sid}`;
			const key = client ? `${p.sid}-${client}` : p.sid;
			const plannedNames: Array<{ kind: 'vsp' | 'gui'; name: string; config: Record<string, unknown> }> = [];
			if (p.components.vsp) {
				plannedNames.push({
					kind: 'vsp',
					name: `vsp${suffix}`,
					config: { command: p.vspCommand! },
				});
			}
			if (p.components.gui) {
				plannedNames.push({
					kind: 'gui',
					name: `sap-gui${suffix}`,
					config: { command: p.guiCommand! },
				});
			}

			// Duplicate detection via cache — warn once per key, confirm on resubmit.
			// Uses a Set so previously-warned keys remain confirmed even if the user
			// submits a different SID in between (fallback fixed M-1).
			const existing = new Set(this.cache.getAllServers().map((s) => s.name));
			const duplicates = plannedNames.filter((n) => existing.has(n.name)).map((n) => n.name);
			if (duplicates.length > 0 && !this.warnedDuplicateKeys.has(key)) {
				this.warnedDuplicateKeys.add(key);
				await this.postWarn(
					`These servers already exist: ${duplicates.join(', ')}. Click "Add SAP system" again to confirm skip and continue with the new ones.`,
				);
				return;
			}

			const results: CreationResult[] = [];
			for (const planned of plannedNames) {
				if (existing.has(planned.name)) {
					results.push({ name: planned.name, ok: false, error: 'already exists (skipped)' });
					continue;
				}
				try {
					await this.client.addServer(planned.name, planned.config);
					results.push({ name: planned.name, ok: true });
				} catch (err) {
					results.push({ name: planned.name, ok: false, error: errorMsg(err) });
				}
			}

			const created = results.filter((r) => r.ok).map((r) => r.name);
			const failed = results.filter((r) => !r.ok && r.error !== 'already exists (skipped)');

			if (created.length === 0) {
				const detail = failed.length > 0
					? failed.map((r) => `${r.name}: ${r.error}`).join('; ')
					: 'No new servers created.';
				await this.postNack(`Failed to add SAP system: ${detail}`);
				return;
			}

			if (failed.length > 0) {
				vscode.window.showWarningMessage(
					`SAP system ${key} partially added. Failed: ${failed.map((r) => `${r.name} (${r.error})`).join('; ')}`,
				);
			} else {
				vscode.window.showInformationMessage(`SAP system ${key} added (${created.join(', ')}).`);
			}

			succeeded = true;
		} finally {
			this.submitting = false;
		}

		if (succeeded) {
			const callback = this.onCreated;
			this.dispose();
			try { callback(); } catch (cbErr) {
				vscode.window.showErrorMessage(`SAP system added, but refresh failed: ${errorMsg(cbErr)}`);
			}
		}
	}

	private async postNack(error: string): Promise<void> {
		if (this.disposed) { return; }
		await this.panel.webview.postMessage({ type: 'nack', error });
	}

	private async postWarn(error: string): Promise<void> {
		if (this.disposed) { return; }
		await this.panel.webview.postMessage({ type: 'warn', error });
	}

	private static coercePayload(raw: unknown):
		| { ok: true; value: SubmitPayload }
		| { ok: false; error: string }
	{
		if (!raw || typeof raw !== 'object') { return { ok: false, error: 'Malformed submit payload.' }; }
		const r = raw as Record<string, unknown>;
		if (typeof r.sid !== 'string') { return { ok: false, error: 'SID must be a string.' }; }
		const sid = r.sid.trim().toUpperCase();
		let client: string | null = null;
		if (r.client === null || r.client === undefined || r.client === '') {
			client = null;
		} else if (typeof r.client === 'string') {
			const trimmed = r.client.trim();
			client = trimmed.length > 0 ? trimmed : null;
		} else {
			return { ok: false, error: 'Client must be a string, empty, or null.' };
		}
		const compsRaw = r.components;
		if (!compsRaw || typeof compsRaw !== 'object') {
			return { ok: false, error: 'Components must be an object.' };
		}
		const comps = compsRaw as Record<string, unknown>;
		const components = { vsp: comps.vsp === true, gui: comps.gui === true };
		const vspCommand = AddSapPanel.coerceOptionalString(r.vspCommand);
		if (vspCommand instanceof Error) { return { ok: false, error: 'VSP command must be a string or null.' }; }
		const guiCommand = AddSapPanel.coerceOptionalString(r.guiCommand);
		if (guiCommand instanceof Error) { return { ok: false, error: 'GUI command must be a string or null.' }; }
		return { ok: true, value: { sid, client, components, vspCommand, guiCommand } };
	}

	/** Coerce an optional string payload field to `string | null | Error`. */
	private static coerceOptionalString(raw: unknown): string | null | Error {
		if (raw === null || raw === undefined || raw === '') { return null; }
		if (typeof raw !== 'string') { return new Error('not a string'); }
		const trimmed = raw.trim();
		return trimmed.length > 0 ? trimmed : null;
	}

	dispose(): void {
		if (this.disposed) { return; }
		this.disposed = true;
		if (AddSapPanel.current === this) {
			AddSapPanel.current = undefined;
		}
		while (this.disposables.length > 0) {
			const d = this.disposables.pop();
			try { d?.dispose(); } catch { /* best-effort cleanup */ }
		}
		try { this.panel.dispose(); } catch { /* panel may already be disposed */ }
	}

	/** Reset the singleton (for testing). */
	static _reset(): void {
		if (AddSapPanel.current && !AddSapPanel.current.disposed) {
			AddSapPanel.current.dispose();
		}
		AddSapPanel.current = undefined;
	}
}

function errorMsg(err: unknown): string {
	if (err instanceof Error) { return err.message; }
	if (typeof err === 'object' && err !== null) { return JSON.stringify(err); }
	return String(err);
}
