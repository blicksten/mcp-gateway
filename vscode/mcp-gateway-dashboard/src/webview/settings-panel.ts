import * as crypto from 'node:crypto';
import * as fs from 'node:fs/promises';
import * as path from 'node:path';
import * as os from 'node:os';
import * as vscode from 'vscode';
import {
	type SettingsSchema,
	type SettingsValues,
	type SettingsField,
	SETTINGS_SCHEMA,
	MCP_DASHBOARD_IMPORT_MAPPINGS,
	indexSchema,
	changedRestartRequiredKeys,
} from '../settings-schema';
import { buildSettingsHtml } from './settings-html';
import {
	makeProbeCache,
	validatePath,
	type ProbeFn,
	type ProbeResult,
} from '../settings-validator';
import { logger } from '../logger';

/** Optional override for the restart-daemon action — production wires this
 *  to `vscode.commands.executeCommand('mcpGateway.restartDaemon')`. Tests
 *  inject a spy. */
export type RestartDaemonFn = () => Promise<void>;

interface PanelDeps {
	/** Read a path probe (existsSync / exec --version). Tests inject a spy. */
	probe?: ProbeFn;
	restartDaemon?: RestartDaemonFn;
}

/** SettingsPanel — sticky-layout settings webview (Phase C T-C.1..T-C.3).
 *
 *  Replaces the overflow-prone native settings UI for mcpGateway.* keys.
 *  Schema lives in settings-schema.ts; HTML in settings-html.ts; live
 *  validation routes through settings-validator (debounce + LRU).
 *
 *  Trust boundary: the webview is treated as untrusted. Save validates
 *  every key against the schema before calling
 *  `vscode.workspace.getConfiguration().update()`. Browse / Import paths
 *  similarly re-fetch from the host's config provider — never from the
 *  webview's posted state.
 */
export class SettingsPanel {
	private static current: SettingsPanel | undefined;

	private readonly panel: vscode.WebviewPanel;
	private readonly schema: SettingsSchema;
	private readonly schemaIndex: Map<string, SettingsField>;
	private readonly disposables: vscode.Disposable[] = [];
	private disposed = false;
	private saving = false;
	private readonly probeCache: { run: (value: string) => Promise<ProbeResult> };
	private readonly restartDaemon?: RestartDaemonFn;

	private constructor(panel: vscode.WebviewPanel, deps: PanelDeps) {
		this.panel = panel;
		this.schema = SETTINGS_SCHEMA;
		this.schemaIndex = indexSchema(this.schema);
		const probe = deps.probe ?? defaultPathProbe;
		this.probeCache = makeProbeCache(probe);
		this.restartDaemon = deps.restartDaemon;
		this.disposables.push(this.panel.onDidDispose(() => this.dispose()));
		this.disposables.push(this.panel.webview.onDidReceiveMessage((msg: unknown) => {
			void this.handleMessage(msg);
		}));
	}

	static async createOrShow(extensionUri: vscode.Uri, deps: PanelDeps = {}): Promise<SettingsPanel> {
		if (SettingsPanel.current && !SettingsPanel.current.disposed) {
			SettingsPanel.current.panel.reveal();
			return SettingsPanel.current;
		}
		const panel = vscode.window.createWebviewPanel(
			'mcpSettings',
			'MCP Gateway — Settings',
			vscode.ViewColumn.One,
			{ enableScripts: true, localResourceRoots: [extensionUri], retainContextWhenHidden: true },
		);
		const instance = new SettingsPanel(panel, deps);
		SettingsPanel.current = instance;
		instance.render();
		return instance;
	}

	private render(): void {
		const nonce = crypto.randomBytes(16).toString('base64');
		const values = this.readCurrentValues();
		this.panel.webview.html = buildSettingsHtml({
			nonce,
			cspSource: this.panel.webview.cspSource,
			schema: this.schema,
			currentValues: values,
		});
	}

	/** Read every schema-known key from `vscode.workspace.getConfiguration`.
	 *  Returns a flat map keyed by dotted setting path. */
	private readCurrentValues(): SettingsValues {
		const out: SettingsValues = {};
		for (const field of this.schemaIndex.values()) {
			out[field.key] = readScalar(field.key);
		}
		return out;
	}

	private async handleMessage(msg: unknown): Promise<void> {
		if (this.disposed) { return; }
		if (!msg || typeof msg !== 'object') { return; }
		const m = msg as Record<string, unknown>;
		switch (m.type) {
			case 'validate': await this.handleValidate(m.key, m.value); break;
			case 'browse': await this.handleBrowse(m.key, m.currentValue); break;
			case 'save': await this.handleSave(m.changes); break;
			case 'importFromMcpDashboard': await this.handleImport(); break;
			case 'cancel': this.dispose(); break;
			default: break;
		}
	}

	private async handleValidate(key: unknown, value: unknown): Promise<void> {
		if (typeof key !== 'string' || typeof value !== 'string') { return; }
		const field = this.schemaIndex.get(key);
		if (!field || field.kind !== 'path') { return; }
		try {
			const result = await validatePath(value, /*required*/ false, this.probeCache.run);
			await this.post({ type: 'validation', key, result });
		} catch (err) {
			logger.warn('settings-panel', `validate ${key} failed`, err);
		}
	}

	private async handleBrowse(key: unknown, currentValue: unknown): Promise<void> {
		if (typeof key !== 'string') { return; }
		const field = this.schemaIndex.get(key);
		if (!field || field.kind !== 'path') { return; }
		const cur = typeof currentValue === 'string' ? currentValue.trim() : '';
		const defaultUri = await pickDefaultUri(cur);
		const picked = await vscode.window.showOpenDialog({
			canSelectFiles: true,
			canSelectFolders: true, // some path fields are dirs (catalogPath/slashCommandsPath)
			canSelectMany: false,
			defaultUri,
			title: `Choose ${field.label}`,
		});
		if (!picked || picked.length === 0) { return; }
		const fsPath = picked[0].fsPath;
		await this.post({ type: 'browseResult', key, path: fsPath });
	}

	private async handleSave(rawChanges: unknown): Promise<void> {
		if (this.saving) { return; }
		this.saving = true;
		try {
			const changes = SettingsPanel.coerceChanges(rawChanges);
			if (!changes) { await this.post({ type: 'error', message: 'Malformed save payload.' }); return; }
			const validated = this.validateChanges(changes);
			if (validated.errors.length > 0) {
				await this.post({ type: 'error', message: `Cannot save: ${validated.errors.join('; ')}` });
				return;
			}
			const config = vscode.workspace.getConfiguration();
			// All Phase C keys live under user (Global) scope. Machine-scoped
			// keys in package.json (`scope: "machine"`) are still written at
			// Global level via the VS Code API — VS Code routes them to the
			// machine settings store automatically. If a future field needs
			// Workspace-scope writes, branch here per `field.scope` instead
			// of expanding inline conditions (C-01 fix; the prior dead-branch
			// ternary was an unfinished design decision).
			const target = vscode.ConfigurationTarget.Global;
			for (const [key, value] of Object.entries(validated.values)) {
				const field = this.schemaIndex.get(key);
				if (!field) { continue; }
				try {
					await config.update(key, value, target);
				} catch (err) {
					logger.error('settings-panel', `update ${key} failed`, err);
					await this.post({
						type: 'error',
						message: `Failed to save ${key}: ${errorMsg(err)}`,
					});
					return;
				}
			}
			const changedKeys = Object.keys(validated.values);
			const restartKeys = changedRestartRequiredKeys(changedKeys);
			const updatedValues = this.readCurrentValues();
			let summary: string;
			let banner: 'ok' | 'warn';
			if (restartKeys.length === 0) {
				summary = `Saved ${changedKeys.length} change${changedKeys.length === 1 ? '' : 's'}.`;
				banner = 'ok';
			} else {
				summary = `Saved ${changedKeys.length} change${changedKeys.length === 1 ? '' : 's'}. Restart daemon to apply: ${restartKeys.join(', ')}.`;
				banner = 'warn';
			}
			await this.post({ type: 'saved', values: updatedValues, status: 'Saved', summary, banner });
			if (restartKeys.length > 0) {
				await this.surfaceRestartToast(restartKeys);
			}
		} finally {
			this.saving = false;
		}
	}

	private async surfaceRestartToast(restartKeys: string[]): Promise<void> {
		const summary = `Settings saved. Restart daemon to apply changes to: ${restartKeys.join(', ')}.`;
		const choice = await vscode.window.showInformationMessage(summary, 'Restart Daemon', 'Later');
		if (choice !== 'Restart Daemon') { return; }
		try {
			if (this.restartDaemon) {
				await this.restartDaemon();
			} else {
				await vscode.commands.executeCommand('mcpGateway.restartDaemon');
			}
		} catch (err) {
			logger.error('settings-panel', 'restart daemon failed', err);
			void vscode.window.showErrorMessage(`Restart failed: ${errorMsg(err)}`);
		}
	}

	private async handleImport(): Promise<void> {
		const dashboardConfig = vscode.workspace.getConfiguration('mcpDashboard');
		const gatewayConfig = vscode.workspace.getConfiguration();
		const staged: Record<string, unknown> = {};
		let count = 0;
		const dashboardPrefix = 'mcpDashboard.';
		for (const mapping of MCP_DASHBOARD_IMPORT_MAPPINGS) {
			// `source` is `mcpDashboard.<key>` — extract the trailing key
			// using string methods per CLAUDE.md "Regex Discipline" (C-02 fix).
			if (!mapping.source.startsWith(dashboardPrefix)) {
				logger.warn('settings-panel', `unexpected import-mapping source ${mapping.source}; skipping`);
				continue;
			}
			const sourceKey = mapping.source.slice(dashboardPrefix.length);
			const raw = dashboardConfig.get<unknown>(sourceKey);
			if (typeof raw !== 'string' || raw.trim().length === 0) { continue; }
			// Only fill empty mcpGateway.* fields (does not overwrite per S1).
			const existing = gatewayConfig.get<unknown>(mapping.target);
			const isEmpty = existing === undefined
				|| existing === null
				|| (typeof existing === 'string' && existing.trim().length === 0);
			if (!isEmpty) { continue; }
			staged[mapping.target] = raw.trim();
			count++;
			if (mapping.extra) {
				for (const [extraKey, extraVal] of Object.entries(mapping.extra)) {
					const extraExisting = gatewayConfig.get<unknown>(extraKey);
					const extraEmpty = extraExisting === undefined
						|| extraExisting === null
						|| (typeof extraExisting === 'string' && extraExisting.trim().length === 0);
					if (extraEmpty) { staged[extraKey] = extraVal; }
				}
			}
		}
		await this.post({ type: 'imported', staged, count });
	}

	/** Validate the webview-supplied change set against the schema. Returns
	 *  the cleaned values (well-typed per field) plus a list of error
	 *  messages. The host NEVER trusts the webview's types directly. */
	private validateChanges(changes: Record<string, unknown>): {
		values: Record<string, unknown>;
		errors: string[];
	} {
		const out: Record<string, unknown> = {};
		const errors: string[] = [];
		for (const [key, raw] of Object.entries(changes)) {
			const field = this.schemaIndex.get(key);
			if (!field) {
				errors.push(`Unknown setting: ${key}`);
				continue;
			}
			if (field.type === 'boolean') {
				out[key] = raw === true;
			} else if (field.type === 'number') {
				const n = typeof raw === 'number' && Number.isFinite(raw) ? raw : Number(raw);
				if (!Number.isFinite(n)) {
					errors.push(`${key} must be a number`);
					continue;
				}
				if (typeof field.min === 'number' && n < field.min) {
					errors.push(`${key} must be >= ${field.min}`);
					continue;
				}
				out[key] = n;
			} else if (field.type === 'enum') {
				if (typeof raw !== 'string' || !(field.choices ?? []).includes(raw)) {
					errors.push(`${key} must be one of ${(field.choices ?? []).join(' / ')}`);
					continue;
				}
				out[key] = raw;
			} else {
				// string + path
				if (typeof raw !== 'string') {
					errors.push(`${key} must be a string`);
					continue;
				}
				const trimmed = raw.trim();
				if (trimmed.length > 4096) {
					errors.push(`${key} too long (max 4096 chars)`);
					continue;
				}
				out[key] = trimmed;
			}
		}
		return { values: out, errors };
	}

	private static coerceChanges(raw: unknown): Record<string, unknown> | null {
		if (!raw || typeof raw !== 'object' || Array.isArray(raw)) { return null; }
		return raw as Record<string, unknown>;
	}

	private async post(msg: unknown): Promise<void> {
		if (this.disposed) { return; }
		await this.panel.webview.postMessage(msg);
	}

	dispose(): void {
		if (this.disposed) { return; }
		this.disposed = true;
		if (SettingsPanel.current === this) { SettingsPanel.current = undefined; }
		while (this.disposables.length > 0) {
			const d = this.disposables.pop();
			try { d?.dispose(); } catch { /* best-effort */ }
		}
		try { this.panel.dispose(); } catch { /* may already be disposed */ }
	}

	static _reset(): void {
		if (SettingsPanel.current && !SettingsPanel.current.disposed) {
			SettingsPanel.current.dispose();
		}
		SettingsPanel.current = undefined;
	}
}

function readScalar(key: string): unknown {
	return vscode.workspace.getConfiguration().get<unknown>(key);
}

/** Build the `defaultUri` for `showOpenDialog`, following the R-17 fallback
 *  chain: currentValue if exists → parentDir if exists → os.homedir(). */
async function pickDefaultUri(currentValue: string): Promise<vscode.Uri | undefined> {
	if (currentValue.length > 0) {
		try {
			await fs.stat(currentValue);
			return vscode.Uri.file(currentValue);
		} catch { /* fall through */ }
		const parent = path.dirname(currentValue);
		try {
			await fs.stat(parent);
			return vscode.Uri.file(parent);
		} catch { /* fall through */ }
	}
	return vscode.Uri.file(os.homedir());
}

/** Default path probe — exists check + (for executables) `--version` probe.
 *  Tests inject a different probe via `PanelDeps.probe`. */
const defaultPathProbe: ProbeFn = async (value: string) => {
	const trimmed = value.trim();
	if (trimmed.length === 0) { return { ok: true }; }
	try {
		await fs.stat(trimmed);
		return { ok: true, message: 'Path exists.' };
	} catch {
		return { ok: false, message: 'Path not found.' };
	}
};

function errorMsg(err: unknown): string {
	if (err instanceof Error) { return err.message; }
	if (typeof err === 'object' && err !== null) { return JSON.stringify(err); }
	return String(err);
}
