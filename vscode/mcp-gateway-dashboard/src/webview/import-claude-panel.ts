import * as crypto from 'node:crypto';
import * as vscode from 'vscode';
import type { IGatewayClient } from '../extension';
import {
	type ImportSnapshot,
	type ImportSnapshotRow,
	type ImportRowState,
	type ImportSource,
	type ImportAction,
	type ImportConflict,
	type ImportLifecycleEvent,
	type ImportOpResult,
	importRowKey,
	initImportRowsFromSnapshot,
	buildImportOpsList,
	transitionImportRow,
	resetFailedImportRowsForRetry,
	isImportFailed,
} from '../import-claude-state';
import { buildImportClaudeHtml } from './import-claude-html';
import { logger } from '../logger';

/** Wire shape for what the webview sends back on Apply / Retry. The host
 *  re-derives the ImportOp[] from this so a tampered DOM cannot bypass
 *  conflict-policy validation or skip the gateway-collision check. */
interface RowEditFromWebview {
	rowKey: string;
	checked: boolean;
	action: ImportAction;
	conflict: ImportConflict;
	destName: string;
}

/** Allowed import sources — typed as a const so a wire payload that smuggles
 *  an unknown string is rejected at the seam. */
const ALLOWED_SOURCES: ReadonlySet<ImportSource> = new Set<ImportSource>([
	'cc_global',
	'cc_project',
	'desktop',
]);

/** Import-from-Claude — picker webview (Phase E).
 *
 * Holds host-side authoritative state:
 *   - source: currently-selected radio (cc_global / cc_project / desktop)
 *   - latestSnapshot: last import-snapshot REST response for that source
 *   - rows: derived ImportRowState[]
 *   - applying: in-flight guard so back-to-back Apply clicks cannot stack
 *
 * Communicates with the webview script via postMessage; the script renders
 * the UI and posts back operator intent (apply, retryFailed, refresh,
 * switchSource, cancel).
 */
export class ImportClaudePanel {
	private static current: ImportClaudePanel | undefined;

	private readonly panel: vscode.WebviewPanel;
	private readonly client: IGatewayClient;
	private readonly disposables: vscode.Disposable[] = [];
	private disposed = false;
	private applying = false;

	private source: ImportSource = 'cc_global';
	private latestSnapshot: ImportSnapshot = {
		source: 'cc_global',
		path: '',
		exists: false,
		rows: [],
		warnings: [],
	};
	private rows: ImportRowState[] = [];

	private constructor(panel: vscode.WebviewPanel, client: IGatewayClient) {
		this.panel = panel;
		this.client = client;

		this.disposables.push(this.panel.onDidDispose(() => this.dispose()));
		this.disposables.push(this.panel.webview.onDidReceiveMessage((msg: unknown) => {
			void this.handleMessage(msg);
		}));
	}

	static async createOrShow(
		extensionUri: vscode.Uri,
		client: IGatewayClient,
	): Promise<ImportClaudePanel> {
		if (ImportClaudePanel.current && !ImportClaudePanel.current.disposed) {
			ImportClaudePanel.current.panel.reveal();
			void ImportClaudePanel.current.refresh();
			return ImportClaudePanel.current;
		}

		const panel = vscode.window.createWebviewPanel(
			'mcpImportClaude',
			'Import from Claude',
			vscode.ViewColumn.One,
			{ enableScripts: true, localResourceRoots: [extensionUri], retainContextWhenHidden: true },
		);

		const instance = new ImportClaudePanel(panel, client);
		ImportClaudePanel.current = instance;
		instance.render();
		void instance.refresh();
		return instance;
	}

	private render(): void {
		const nonce = crypto.randomBytes(16).toString('base64');
		this.panel.webview.html = buildImportClaudeHtml(nonce, this.panel.webview.cspSource);
	}

	private async refresh(): Promise<void> {
		if (this.disposed) { return; }
		if (!this.client.importSnapshot) {
			await this.postError(
				'Import-from-Claude requires gateway daemon v1.9+ — upgrade the daemon and try again.',
			);
			return;
		}
		try {
			const projectRoot = this.workspaceProjectRoot();
			const snap = (await this.client.importSnapshot(this.source, projectRoot)) as ImportSnapshot;
			// Defence: if backend returns rows for a different source (shouldn't
			// happen, but pin to the request), normalise so the rowKey carries
			// the requested source — the webview filter relies on it.
			snap.source = this.source;
			snap.rows = (snap.rows ?? []).map((r) => ({
				...r,
				source: this.source,
			})) as ImportSnapshotRow[];
			this.latestSnapshot = snap;
			this.rows = initImportRowsFromSnapshot(snap);
			await this.postInit();
		} catch (err) {
			logger.error('import-claude', 'snapshot fetch failed', err);
			await this.postError(`Failed to load Claude entries: ${errorMsg(err)}`);
		}
	}

	private workspaceProjectRoot(): string | undefined {
		const folders = vscode.workspace.workspaceFolders;
		if (!folders || folders.length === 0) { return undefined; }
		return folders[0].uri.fsPath;
	}

	private async handleMessage(msg: unknown): Promise<void> {
		if (this.disposed) { return; }
		if (!msg || typeof msg !== 'object') { return; }
		const m = msg as Record<string, unknown>;
		switch (m.type) {
			case 'apply':
				await this.handleApply(m.edits, /*onlyFailed*/ false);
				break;
			case 'retryFailed':
				await this.handleApply(m.edits, /*onlyFailed*/ true);
				break;
			case 'switchSource':
				await this.handleSwitchSource(m.source);
				break;
			case 'refresh':
				await this.refresh();
				break;
			case 'cancel':
				this.dispose();
				break;
			default: break;
		}
	}

	private async handleSwitchSource(rawSource: unknown): Promise<void> {
		if (typeof rawSource !== 'string') { return; }
		// Normalise narrow-string into the typed union before mutating state.
		const source = rawSource as ImportSource;
		if (!ALLOWED_SOURCES.has(source)) {
			await this.postError(`Unknown source: ${rawSource}`);
			return;
		}
		this.source = source;
		await this.refresh();
	}

	private async handleApply(rawEdits: unknown, onlyFailed: boolean): Promise<void> {
		if (this.applying) { return; }
		const edits = ImportClaudePanel.coerceEdits(rawEdits);
		if (edits === null) {
			await this.postError('Malformed Apply payload.');
			return;
		}

		// Reconcile webview-supplied edits into the host-authoritative rows[].
		// Only checked / action / conflict / destName are accepted — snapshot
		// fields stay tamper-resistant.
		const byKey = new Map<string, RowEditFromWebview>();
		for (const e of edits) { byKey.set(e.rowKey, e); }

		// Snapshot the keys that were FAILED before any state mutation so a
		// retry-failed pass cannot pull in adjacent never-failed-but-checked
		// rows once `resetFailedImportRowsForRetry` flips them error → idle
		// (review finding MEDIUM-1).
		const preResetFailedKeys = onlyFailed
			? new Set(this.rows.filter((r) => isImportFailed(r.status)).map((r) => r.key))
			: undefined;

		if (onlyFailed) {
			this.rows = resetFailedImportRowsForRetry(this.rows);
		}

		this.rows = this.rows.map((r) => {
			const e = byKey.get(r.key);
			if (!e) { return r; }
			return {
				...r,
				checked: Boolean(e.checked),
				action: e.action,
				conflict: e.conflict,
				destName: typeof e.destName === 'string' ? e.destName : '',
			};
		});

		const projectRoot = this.workspaceProjectRoot();
		const ops = buildImportOpsList(this.rows, projectRoot);
		const filteredOps = onlyFailed && preResetFailedKeys
			? ops.filter((op) => preResetFailedKeys.has(importRowKey(op.source, op.name)))
			: ops;
		if (filteredOps.length === 0) {
			await this.postApplied(0, 0, 0, onlyFailed
				? 'No failed rows to retry.'
				: 'No changes to apply.');
			return;
		}

		await this.runBatch(filteredOps as Array<{ source: ImportSource; name: string; action: ImportAction; conflict: ImportConflict; project_root?: string; dest_name?: string }>);
	}

	private async runBatch(ops: Array<{ source: ImportSource; name: string; action: ImportAction; conflict: ImportConflict; project_root?: string; dest_name?: string }>): Promise<void> {
		this.applying = true;
		await this.postApplying(true);

		try {
			if (!this.client.importApply) {
				throw new Error('Gateway daemon does not support /import-apply (need v1.9+).');
			}

			// Mark all targeted rows as 'pending' before kicking off — the
			// webview reflects this immediately. /import-apply is a single
			// server-side batch (one POST), so we do not need bounded
			// concurrency here as Phase B does for SAP.
			for (const op of ops) {
				const key = importRowKey(op.source, op.name);
				this.rows = this.rows.map((r) =>
					transitionImportRow(r, { kind: 'queue', rowKey: key }),
				);
			}
			await this.postRows();

			// Kick off — flip every targeted row to 'in_progress' first so
			// the webview shows live activity before the response lands.
			for (const op of ops) {
				const key = importRowKey(op.source, op.name);
				this.rows = this.rows.map((r) =>
					transitionImportRow(r, { kind: 'start_op', rowKey: key }),
				);
			}
			await this.postRows();

			const resp = (await this.client.importApply(ops)) as { results: ImportOpResult[] };
			const results = Array.isArray(resp?.results) ? resp.results : [];

			let ok = 0; let skipped = 0; let failed = 0;
			for (const result of results) {
				const key = importRowKey(this.source, result.name);
				this.rows = this.rows.map((r) =>
					transitionImportRow(r, { kind: 'op_result', rowKey: key, result }),
				);
				if (result.status === 'applied') { ok++; }
				else if (result.status === 'skipped') { skipped++; }
				else { failed++; }
			}

			// Any op without a corresponding result row → mark error so the
			// operator sees the orphan; defence against partial backend reply.
			const resultKeys = new Set(results.map((r) => importRowKey(this.source, r.name)));
			for (const op of ops) {
				const key = importRowKey(op.source, op.name);
				if (!resultKeys.has(key)) {
					this.rows = this.rows.map((r) =>
						transitionImportRow(r, {
							kind: 'op_error',
							rowKey: key,
							error: 'No result returned by daemon for this op',
						}),
					);
					failed++;
				}
			}

			// Post the final row state so the webview reflects applied /
			// skipped / error transitions before the summary banner lands.
			await this.postRows();

			const summary = failed === 0
				? `Imported ${ok} change(s)${skipped > 0 ? `, skipped ${skipped}` : ''}.`
				: `Imported ${ok}, skipped ${skipped}, failed ${failed}. Click "Retry failed" to re-run.`;
			await this.postApplied(ok, skipped, failed, summary);

			void this.refreshAfterApply();
		} catch (err) {
			logger.error('import-claude', 'apply failed', err);
			// Mark all in-progress rows as error so the UI is consistent.
			const inProgressKeys = new Set<string>();
			for (const op of ops) { inProgressKeys.add(importRowKey(op.source, op.name)); }
			const errStr = errorMsg(err);
			this.rows = this.rows.map((r) => {
				if (!inProgressKeys.has(r.key)) { return r; }
				return transitionImportRow(r, { kind: 'op_error', rowKey: r.key, error: errStr });
			});
			await this.postRows();
			await this.postError(`Apply failed: ${errStr}`);
		} finally {
			this.applying = false;
			await this.postApplying(false);
		}
	}

	private async refreshAfterApply(): Promise<void> {
		if (this.disposed) { return; }
		if (!this.client.importSnapshot) { return; }
		try {
			const projectRoot = this.workspaceProjectRoot();
			const snap = (await this.client.importSnapshot(this.source, projectRoot)) as ImportSnapshot;
			snap.source = this.source;
			snap.rows = (snap.rows ?? []).map((r) => ({ ...r, source: this.source })) as ImportSnapshotRow[];
			this.latestSnapshot = snap;
			// Re-init rows but carry forward post-apply status + error +
			// resolved command + drift_fields so the operator still sees
			// what happened in the previous Apply pass. Both old.status
			// and nr.status are 'idle' for fresh rows, so the carry-forward
			// is just `old.status` (review finding LOW-2 — was a tautology
			// dressed up as a conditional).
			const oldByKey = new Map(this.rows.map((r) => [r.key, r]));
			this.rows = initImportRowsFromSnapshot(snap).map((nr) => {
				const old = oldByKey.get(nr.key);
				if (!old) { return nr; }
				return {
					...nr,
					status: old.status,
					error: old.error,
					resolvedCommand: old.resolvedCommand,
					driftFieldsApplied: old.driftFieldsApplied,
				};
			});
			await this.postInit();
		} catch (err) {
			logger.warn('import-claude', 'refresh after apply failed', err);
		}
	}

	private async postInit(): Promise<void> {
		if (this.disposed) { return; }
		await this.panel.webview.postMessage({
			type: 'init',
			source: this.source,
			path: this.latestSnapshot.path,
			exists: this.latestSnapshot.exists,
			rows: this.rows.map(serializeImportRow),
			warnings: this.latestSnapshot.warnings ?? [],
		});
	}

	private async postRows(): Promise<void> {
		if (this.disposed) { return; }
		await this.panel.webview.postMessage({
			type: 'rows',
			rows: this.rows.map(serializeImportRow),
		});
	}

	private async postApplying(active: boolean): Promise<void> {
		if (this.disposed) { return; }
		await this.panel.webview.postMessage({ type: 'applying', active });
	}

	private async postApplied(ok: number, skipped: number, failed: number, summary: string): Promise<void> {
		if (this.disposed) { return; }
		await this.panel.webview.postMessage({ type: 'applied', ok, skipped, failed, summary });
	}

	private async postError(message: string): Promise<void> {
		if (this.disposed) { return; }
		await this.panel.webview.postMessage({ type: 'error', message });
	}

	/** Fail-closed payload coercion. Any single malformed item rejects the
	 *  whole array (returns null) — the webview always serialises every
	 *  visible row regardless of `checked`, so a stray invalid value can
	 *  only come from genuine DOM tampering. Refusing the entire batch in
	 *  that case is the conservative choice (review finding LOW-1). */
	private static coerceEdits(raw: unknown): RowEditFromWebview[] | null {
		if (!Array.isArray(raw)) { return null; }
		const out: RowEditFromWebview[] = [];
		for (const item of raw) {
			if (!item || typeof item !== 'object') { return null; }
			const r = item as Record<string, unknown>;
			if (typeof r.rowKey !== 'string' || r.rowKey.length === 0 || r.rowKey.length > 256) { return null; }
			const action = r.action;
			if (action !== 'copy' && action !== 'move') { return null; }
			const conflict = r.conflict;
			if (conflict !== 'skip' && conflict !== 'overwrite') { return null; }
			const destName = typeof r.destName === 'string' ? r.destName.slice(0, 256) : '';
			out.push({
				rowKey: r.rowKey,
				checked: r.checked === true,
				action,
				conflict,
				destName,
			});
		}
		return out;
	}

	dispose(): void {
		if (this.disposed) { return; }
		this.disposed = true;
		if (ImportClaudePanel.current === this) {
			ImportClaudePanel.current = undefined;
		}
		while (this.disposables.length > 0) {
			const d = this.disposables.pop();
			try { d?.dispose(); } catch { /* best-effort cleanup */ }
		}
		try { this.panel.dispose(); } catch { /* panel may already be disposed */ }
	}

	/** Reset the singleton (for testing). */
	static _reset(): void {
		if (ImportClaudePanel.current && !ImportClaudePanel.current.disposed) {
			ImportClaudePanel.current.dispose();
		}
		ImportClaudePanel.current = undefined;
	}

	/** Expose the current rows[] for testing assertions. */
	_rows(): readonly ImportRowState[] { return this.rows; }
	_source(): ImportSource { return this.source; }
}

interface SerializedImportRow {
	key: string;
	source: ImportSource;
	name: string;
	type: string;
	command?: string;
	args?: string[];
	url?: string;
	gateway_has_name: boolean;
	drift_fields?: string[];
	previously_imported: boolean;
	previously_imported_at?: string;
	checked: boolean;
	action: ImportAction;
	conflict: ImportConflict;
	destName: string;
	status: string;
	error?: string;
	resolvedCommand?: string;
	driftFieldsApplied?: string[];
}

function serializeImportRow(r: ImportRowState): SerializedImportRow {
	return {
		key: r.key,
		source: r.snapshot.source,
		name: r.snapshot.name,
		type: r.snapshot.type,
		command: r.snapshot.command,
		args: r.snapshot.args,
		url: r.snapshot.url,
		gateway_has_name: r.snapshot.gateway_has_name,
		drift_fields: r.snapshot.drift_fields,
		previously_imported: r.snapshot.previously_imported,
		previously_imported_at: r.snapshot.previously_imported_at,
		checked: r.checked,
		action: r.action,
		conflict: r.conflict,
		destName: r.destName,
		status: r.status,
		error: r.error,
		resolvedCommand: r.resolvedCommand,
		driftFieldsApplied: r.driftFieldsApplied,
	};
}

function errorMsg(err: unknown): string {
	if (err instanceof Error) { return err.message; }
	if (typeof err === 'object' && err !== null) { return JSON.stringify(err); }
	return String(err);
}

export type { SerializedImportRow };
