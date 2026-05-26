import * as crypto from 'node:crypto';
import * as path from 'node:path';
import * as vscode from 'vscode';
import type { IGatewayClient } from '../extension';
import type { ServerDataCache } from '../server-data-cache';
import {
	type PickerSnapshot,
	type PickerSnapshotRow,
	type RowState,
	type RowOverride,
	type BatchOp,
	type LifecycleEvent,
	serverName,
	initRowsFromSnapshot,
	buildOpsList,
	transitionRow,
	resetFailedRowsForRetry,
	runWithConcurrency,
} from '../sap-picker-state';
import { listPickerRows, SapPickerImportError, type PickerListRow } from '../sap-picker-importer';
import { buildSapPickerHtml } from './sap-picker-html';
import { logger } from '../logger';

/** Where the picker reads landscape + KP from, plus the resolved mcp-ctl
 *  executable. Built once per refresh() call from workspace configuration. */
interface PickerInputs {
	mcpCtlPath: string;
	kdbxPath: string;
	landscapePath: string;
	keyfile?: string;
}

/** Wire shape for what the webview sends back on Apply / Retry. The host
 *  re-derives the BatchOp[] from this so a tampered DOM cannot bypass
 *  R-30 (kpMissing) or skip command validation. */
interface RowDiffFromWebview {
	rowKey: string;
	desired: { vsp: boolean; gui: boolean };
	override: RowOverride;
}

/** SAP Picker — hybrid landscape ∪ KeePass picker webview (Phase B).
 *
 * Holds host-side authoritative state:
 *   - latestSnapshot: last picker-snapshot REST response
 *   - rows: derived RowState[] (with per-component status from the apply
 *     state machine in sap-picker-state.ts)
 *   - applying: in-flight guard so back-to-back Apply clicks cannot stack
 *
 * Communicates with the webview script via postMessage; the script renders
 * the UI and posts back user intent (Apply, Retry, ForceKill, Refresh).
 */
export class SapPickerPanel {
	private static current: SapPickerPanel | undefined;

	private readonly panel: vscode.WebviewPanel;
	private readonly client: IGatewayClient;
	private readonly cache: ServerDataCache;
	private readonly disposables: vscode.Disposable[] = [];
	private disposed = false;
	private applying = false;

	private latestSnapshot: PickerSnapshot = { rows: [], warnings: [] };
	private rows: RowState[] = [];

	/** Cached KeePass master password — lives in RAM only for the panel
	 *  lifetime so the operator doesn't re-enter it on every refresh /
	 *  apply. Cleared on dispose. Never persisted to SecretStorage (an
	 *  enhancement candidate, but it carries different threat-model
	 *  implications — opt-in only). */
	private kpMasterPassword?: string;

	private constructor(
		panel: vscode.WebviewPanel,
		client: IGatewayClient,
		cache: ServerDataCache,
	) {
		this.panel = panel;
		this.client = client;
		this.cache = cache;

		this.disposables.push(this.panel.onDidDispose(() => this.dispose()));
		this.disposables.push(this.panel.webview.onDidReceiveMessage((msg: unknown) => {
			void this.handleMessage(msg);
		}));
	}

	static async createOrShow(
		extensionUri: vscode.Uri,
		client: IGatewayClient,
		cache: ServerDataCache,
	): Promise<SapPickerPanel> {
		if (SapPickerPanel.current && !SapPickerPanel.current.disposed) {
			SapPickerPanel.current.panel.reveal();
			void SapPickerPanel.current.refresh();
			return SapPickerPanel.current;
		}

		const panel = vscode.window.createWebviewPanel(
			'mcpSapPicker',
			'SAP Picker',
			vscode.ViewColumn.One,
			{ enableScripts: true, localResourceRoots: [extensionUri], retainContextWhenHidden: true },
		);

		const instance = new SapPickerPanel(panel, client, cache);
		SapPickerPanel.current = instance;
		instance.render();
		void instance.refresh();
		return instance;
	}

	private render(): void {
		const nonce = crypto.randomBytes(16).toString('base64');
		this.panel.webview.html = buildSapPickerHtml(nonce, this.panel.webview.cspSource);
	}

	private async refresh(): Promise<void> {
		if (this.disposed) { return; }
		const snap = await this.loadSnapshot();
		if (!snap) { return; }    // loadSnapshot already posted an error / cancel banner
		this.latestSnapshot = snap;
		this.rows = initRowsFromSnapshot(snap);
		await this.postInit();
	}

	/**
	 * Build a fresh PickerSnapshot by:
	 *   1. Resolving picker inputs (kdbx / landscape / mcp-ctl paths) from
	 *      workspace settings. Missing kdbx → banner + return null.
	 *   2. Prompting for the KeePass master password (cached for the panel
	 *      lifetime). Cancelled prompt → null.
	 *   3. Spawning `mcp-ctl credential list-structured --password-stdin`
	 *      and parsing the JSON array.
	 *   4. Augmenting each row with `registered.vsp/gui` + `status.vsp/gui`
	 *      from the live gateway server cache (no extra REST round-trip).
	 *
	 * Returns null on any failure or operator cancellation — callers
	 * should treat null as "abort the current refresh path".
	 */
	private async loadSnapshot(): Promise<PickerSnapshot | null> {
		const inputs = this.resolveInputs();
		if (!inputs) { return null; }

		const password = await this.resolveMasterPassword();
		if (password === null) { return null; }    // operator cancelled

		let rows: PickerListRow[];
		try {
			rows = await listPickerRows({
				mcpCtlPath: inputs.mcpCtlPath,
				kdbxPath: inputs.kdbxPath,
				landscapePath: inputs.landscapePath,
				masterPassword: password,
				keyfile: inputs.keyfile,
			});
		} catch (err) {
			// SapPickerImportError gives a tight stderr-first-line message
			// already; surface it verbatim and forget the cached password
			// so a re-open re-prompts (likely wrong password).
			this.kpMasterPassword = undefined;
			const msg = err instanceof SapPickerImportError ? err.message : errorMsg(err);
			logger.error('sap-picker', 'list-structured failed', err);
			await this.postError(`Failed to load SAP systems: ${msg}`);
			return null;
		}

		const snapshotRows = this.augmentWithCache(rows);
		return { rows: snapshotRows, warnings: [] };
	}

	private resolveInputs(): PickerInputs | null {
		const cfg = vscode.workspace.getConfiguration('mcpGateway');
		const kdbxPath = cfg.get<string>('keepassPath', '').trim();
		if (!kdbxPath) {
			void this.postError(
				'SAP Picker needs mcpGateway.keepassPath set to your KDBX file. ' +
				'Open Settings → mcp-gateway → KeePass.',
			);
			return null;
		}

		// Landscape path: explicit setting wins; otherwise fall back to
		// the SAP Logon default %APPDATA%/SAP/Common/SAPUILandscape.xml
		// (Windows). Non-Windows operators must set it explicitly.
		let landscapePath = cfg.get<string>('sapLandscapePath', '').trim();
		if (!landscapePath) {
			const appData = process.env.APPDATA;
			if (!appData) {
				void this.postError(
					'SAP Picker needs mcpGateway.sapLandscapePath set ' +
					'(no APPDATA env var to derive the default from).',
				);
				return null;
			}
			landscapePath = path.join(appData, 'SAP', 'Common', 'SAPUILandscape.xml');
		}

		const mcpCtlPath = cfg.get<string>('mcpCtlPath', '').trim() || 'mcp-ctl';

		return { mcpCtlPath, kdbxPath, landscapePath };
	}

	/**
	 * Returns the KeePass master password — from cache on second+ call
	 * per panel lifetime, or via vscode.window.showInputBox on first
	 * call. Returns null on operator cancel.
	 */
	private async resolveMasterPassword(): Promise<string | null> {
		if (this.kpMasterPassword !== undefined) { return this.kpMasterPassword; }
		const pw = await vscode.window.showInputBox({
			prompt: 'KeePass master password (for SAP Picker)',
			password: true,
			ignoreFocusOut: true,
		});
		if (pw === undefined || pw === '') { return null; }
		this.kpMasterPassword = pw;
		return pw;
	}

	/**
	 * Translate the wire shape from mcp-ctl into the snapshot row shape
	 * the webview consumes. The wire shape carries only sid/client/user/
	 * kpMissing; we synthesise the `registered` and `status` fields by
	 * looking up the expected backend names in the live gateway cache so
	 * the operator sees the same green/yellow/red indicators they see
	 * in the backends tree.
	 */
	private augmentWithCache(wireRows: PickerListRow[]): PickerSnapshotRow[] {
		const servers = this.cache.getMcpServers();
		const byName = new Map(servers.map((s) => [s.name, s]));

		return wireRows.map((r) => {
			const vspName = serverName('vsp', r.sid, r.client);
			const guiName = serverName('gui', r.sid, r.client);
			const vsp = byName.get(vspName);
			const gui = byName.get(guiName);
			return {
				sid: r.sid,
				client: r.client,
				user: r.user || undefined,
				kpMissing: r.kpMissing,
				registered: {
					vsp: Boolean(vsp),
					gui: Boolean(gui),
				},
				status: {
					vsp: vsp ? vsp.status : '',
					gui: gui ? gui.status : '',
				},
			};
		});
	}

	private async handleMessage(msg: unknown): Promise<void> {
		if (this.disposed) { return; }
		if (!msg || typeof msg !== 'object') { return; }
		const m = msg as Record<string, unknown>;
		switch (m.type) {
			case 'apply':
				await this.handleApply(m.diffs, /*onlyFailed*/ false);
				break;
			case 'retryFailed':
				await this.handleApply(m.diffs, /*onlyFailed*/ true);
				break;
			case 'forceKill':
				await this.handleForceKill(m.rowKey, m.component);
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

	private async handleApply(rawDiffs: unknown, onlyFailed: boolean): Promise<void> {
		if (this.applying) { return; }
		const diffs = SapPickerPanel.coerceDiffs(rawDiffs);
		if (diffs === null) {
			await this.postError('Malformed Apply payload.');
			return;
		}

		// Reconcile webview-supplied diffs into the host-authoritative rows[].
		// Only desired + override are accepted from the webview; sid / client /
		// kpMissing come from latestSnapshot — the only tamper-resistant source.
		const byKey = new Map<string, RowDiffFromWebview>();
		for (const d of diffs) { byKey.set(d.rowKey, d); }

		// On retry, reset only failed rows to idle so the apply driver re-runs
		// them; succeeded rows keep their status untouched (R-04 retry).
		if (onlyFailed) {
			this.rows = resetFailedRowsForRetry(this.rows);
		}

		// Merge desired + override into rows; never touch snapshot fields.
		this.rows = this.rows.map((r) => {
			const d = byKey.get(r.key);
			if (!d) { return r; }
			return {
				...r,
				desired: { vsp: Boolean(d.desired.vsp), gui: Boolean(d.desired.gui) },
				override: SapPickerPanel.sanitizeOverride(d.override),
			};
		});

		const ops = buildOpsList(this.rows);
		// Filter to retry-only failed rows when onlyFailed is set: keep an op
		// only if its rowKey/component is currently in a failed status.
		const filteredOps = onlyFailed ? ops.filter((op) => this.opTargetsFailedRow(op)) : ops;
		if (filteredOps.length === 0) {
			await this.postApplied(0, 0, onlyFailed
				? 'No failed rows to retry.'
				: 'No changes to apply.');
			return;
		}

		await this.runBatch(filteredOps);
	}

	private opTargetsFailedRow(op: BatchOp): boolean {
		const r = this.rows.find((x) => x.key === op.rowKey);
		if (!r) { return false; }
		// After resetFailedRowsForRetry, formerly-failed rows are 'idle' again.
		// We re-derive failed-eligibility from the snapshot vs desired delta:
		// any row whose row had a failed status before reset would still appear
		// in buildOpsList output, so this branch is a defence-in-depth filter.
		const status = op.component === 'vsp' ? r.vspStatus : r.guiStatus;
		return status === 'idle' || status === 'pending';
	}

	private async runBatch(ops: BatchOp[]): Promise<void> {
		this.applying = true;
		await this.postApplying(true);

		let batchId: string | undefined;
		try {
			if (!this.client.beginSapBatch || !this.client.endSapBatch) {
				throw new Error('Gateway daemon does not support SAP batch endpoints (need v1.8+).');
			}
			const beg = (await this.client.beginSapBatch()) as { batch_id: string };
			batchId = beg.batch_id;

			// Mark all targeted rows as 'pending' before kicking off concurrent
			// work — the webview reflects this immediately.
			for (const op of ops) {
				this.rows = this.rows.map((r) =>
					transitionRow(r, { kind: 'queue', rowKey: op.rowKey, component: op.component }),
				);
			}
			await this.postRows();

			// Bounded concurrency = 4 per spike §3.5 (R-09). Driver lives in
			// the state module so the concurrency cap is unit-tested in one
			// place; this panel calls into it with a thin per-op closure.
			await runWithConcurrency(ops, (op) => this.runOneOp(op).then(() => undefined), 4);

			let failed = 0;
			let ok = 0;
			for (const op of ops) {
				const r = this.rows.find((x) => x.key === op.rowKey);
				if (!r) { continue; }
				const status = op.component === 'vsp' ? r.vspStatus : r.guiStatus;
				// `config_added` is a transient state that runOneOp always
				// transitions out of (to config_added_running or
				// config_added_start_failed). It only persists if a
				// post-add operation panics before the polling branch — in
				// that case treat it as ok (config DID land in the daemon),
				// which matches what the row badge shows the operator.
				if (status === 'config_added_running' || status === 'config_added' || status === 'removed') { ok++; }
				else { failed++; }
			}
			const summary = failed === 0
				? `Applied ${ok} change(s) successfully.`
				: `Applied ${ok}, failed ${failed}. Click "Retry failed rows" to re-run.`;
			await this.postApplied(ok, failed, summary);

			// Re-fetch snapshot so registered state + statuses reflect the new
			// gateway view; preserve the host-side error/orphan annotations by
			// merging on top of the fresh snapshot.
			void this.refreshAfterApply();
		} catch (err) {
			logger.error('sap-picker', 'apply failed', err);
			await this.postError(`Apply failed: ${errorMsg(err)}`);
		} finally {
			if (batchId) {
				try {
					if (this.client.endSapBatch) {
						await this.client.endSapBatch(batchId);
					}
				} catch (endErr) {
					logger.error('sap-picker', 'batch-end failed', endErr);
				}
			}
			this.applying = false;
			await this.postApplying(false);
		}
	}

	private async refreshAfterApply(): Promise<void> {
		if (this.disposed) { return; }
		// Use the cached cache view + cached KeePass password — no need
		// to re-prompt the operator after a successful Apply. If the
		// password somehow got cleared (panel quirk) loadSnapshot will
		// re-prompt; that's acceptable.
		const snap = await this.loadSnapshot();
		if (!snap) { return; }
		try {
			this.latestSnapshot = snap;
			// Carry forward orphan / start_failed annotations from old rows by
			// matching on rowKey — fresh snapshot resets desired = registered.
			const oldByKey = new Map(this.rows.map((r) => [r.key, r]));
			this.rows = initRowsFromSnapshot(snap).map((nr) => {
				const old = oldByKey.get(nr.key);
				if (!old) { return nr; }
				return {
					...nr,
					vspStatus: SapPickerPanel.carryForwardStatus(old.vspStatus, nr.vspStatus),
					guiStatus: SapPickerPanel.carryForwardStatus(old.guiStatus, nr.guiStatus),
					vspError: old.vspError,
					guiError: old.guiError,
					override: old.override, // override survives refresh until row collapse
				};
			});
			await this.postInit();
		} catch (err) {
			logger.warn('sap-picker', 'refresh after apply failed', err);
		}
	}

	private static carryForwardStatus(oldS: RowState['vspStatus'], freshS: RowState['vspStatus']): RowState['vspStatus'] {
		// Keep terminal-failure annotations (orphan, start_failed, removal_failed)
		// visible until the user retries / dismisses; otherwise use fresh snapshot
		// idle which reflects the now-canonical gateway state.
		if (
			oldS === 'removed_with_orphan' ||
			oldS === 'config_added_start_failed' ||
			oldS === 'removal_failed'
		) {
			return oldS;
		}
		return freshS;
	}

	private async runOneOp(op: BatchOp): Promise<void> {
		this.rows = this.rows.map((r) =>
			transitionRow(r, { kind: 'start_op', rowKey: op.rowKey, component: op.component }),
		);
		await this.postRows();

		try {
			if (op.kind === 'add') {
				await this.client.addServer(op.serverName, op.config ?? {});
				this.applyEvent({ kind: 'add_ok', rowKey: op.rowKey, component: op.component });
				// Poll /health to see whether the new entry transitions to running
				// or fails to start. 5 s deadline matches spike §3.4 acceptance.
				const started = await this.pollServerRunning(op.serverName, 5_000);
				if (started === 'running') {
					this.applyEvent({ kind: 'add_started', rowKey: op.rowKey, component: op.component });
				} else {
					const err = started === 'error'
						? 'Server reported error after start'
						: 'Server did not transition to running within 5s';
					this.applyEvent({
						kind: 'add_start_failed', rowKey: op.rowKey, component: op.component, error: err,
					});
				}
			} else {
				try {
					await this.client.removeServer(op.serverName);
					this.applyEvent({ kind: 'remove_ok', rowKey: op.rowKey, component: op.component });
				} catch (err) {
					// R-28 / X4: distinguish orphan (entry removed but Stop failed)
					// from outright removal failure. The daemon surfaces orphan via
					// a `Orphan: true` JSON field once T-A.5 is wired; for now we
					// inspect the error message — the body will contain "orphan"
					// for the orphan path.
					const msg = errorMsg(err);
					if (msg.toLowerCase().includes('orphan')) {
						this.applyEvent({
							kind: 'remove_orphan', rowKey: op.rowKey, component: op.component, error: msg,
						});
					} else {
						this.applyEvent({
							kind: 'remove_failed', rowKey: op.rowKey, component: op.component, error: msg,
						});
					}
				}
			}
		} catch (err) {
			const msg = errorMsg(err);
			if (op.kind === 'add') {
				this.applyEvent({ kind: 'add_failed', rowKey: op.rowKey, component: op.component, error: msg });
			} else {
				this.applyEvent({ kind: 'remove_failed', rowKey: op.rowKey, component: op.component, error: msg });
			}
		}
		await this.postRows();
	}

	private applyEvent(ev: LifecycleEvent): void {
		this.rows = this.rows.map((r) => transitionRow(r, ev));
	}

	/** Poll /health for `name` until status reflects running / error or
	 *  deadline elapses. Returns 'running' / 'error' / 'timeout'. The
	 *  HealthResponse shape exposes total counts not per-server status, so
	 *  we cross-reference with /servers via the cache when available. */
	private async pollServerRunning(name: string, timeoutMs: number): Promise<'running' | 'error' | 'timeout'> {
		const deadline = Date.now() + timeoutMs;
		while (Date.now() < deadline) {
			try {
				// Refresh cache so server view reflects fresh state.
				await this.cache.refresh();
				const servers = this.cache.getAllServers();
				const sv = servers.find((s) => s.name === name);
				if (sv) {
					if (sv.status === 'running') { return 'running'; }
					if (sv.status === 'error') { return 'error'; }
				}
			} catch (err) {
				logger.warn('sap-picker', `pollServerRunning(${name}) cache refresh failed`, err);
			}
			await sleep(500);
		}
		return 'timeout';
	}

	private async handleForceKill(rowKey: unknown, component: unknown): Promise<void> {
		if (typeof rowKey !== 'string' || (component !== 'vsp' && component !== 'gui')) {
			await this.postError('Force-kill payload malformed.');
			return;
		}
		const r = this.rows.find((x) => x.key === rowKey);
		if (!r) { return; }
		const name = serverName(component, r.snapshot.sid, r.snapshot.client);
		// VSCode confirmation — same shape as removeServer in extension.ts. The
		// actual kill is a removeServer retry which on the daemon side maps to
		// SIGKILL when the entry is in orphan state.
		const answer = await vscode.window.showWarningMessage(
			`Force-kill orphan process for "${name}"? This sends SIGKILL via the daemon.`,
			'Force kill', 'Cancel',
		);
		if (answer !== 'Force kill') { return; }
		try {
			await this.client.removeServer(name);
			this.applyEvent({ kind: 'remove_ok', rowKey, component });
			await this.postRows();
		} catch (err) {
			const msg = errorMsg(err);
			this.applyEvent({ kind: 'remove_failed', rowKey, component, error: msg });
			await this.postRows();
			await this.postError(`Force-kill failed: ${msg}`);
		}
	}

	private async postInit(): Promise<void> {
		if (this.disposed) { return; }
		await this.panel.webview.postMessage({
			type: 'init',
			rows: this.rows.map(serializeRowState),
			warnings: this.latestSnapshot.warnings,
		});
	}

	private async postRows(): Promise<void> {
		if (this.disposed) { return; }
		await this.panel.webview.postMessage({
			type: 'rows',
			rows: this.rows.map(serializeRowState),
		});
	}

	private async postApplying(active: boolean): Promise<void> {
		if (this.disposed) { return; }
		await this.panel.webview.postMessage({ type: 'applying', active });
	}

	private async postApplied(ok: number, failed: number, summary: string): Promise<void> {
		if (this.disposed) { return; }
		await this.panel.webview.postMessage({ type: 'applied', ok, failed, summary });
	}

	private async postError(message: string): Promise<void> {
		if (this.disposed) { return; }
		await this.panel.webview.postMessage({ type: 'error', message });
	}

	private static coerceDiffs(raw: unknown): RowDiffFromWebview[] | null {
		if (!Array.isArray(raw)) { return null; }
		const out: RowDiffFromWebview[] = [];
		for (const item of raw) {
			if (!item || typeof item !== 'object') { return null; }
			const r = item as Record<string, unknown>;
			if (typeof r.rowKey !== 'string' || r.rowKey.length === 0 || r.rowKey.length > 64) { return null; }
			const desired = r.desired;
			if (!desired || typeof desired !== 'object') { return null; }
			const dr = desired as Record<string, unknown>;
			out.push({
				rowKey: r.rowKey,
				desired: { vsp: dr.vsp === true, gui: dr.gui === true },
				override: SapPickerPanel.sanitizeOverride(r.override),
			});
		}
		return out;
	}

	/** Trim and length-cap override fields. Anything not a string is dropped. */
	private static sanitizeOverride(raw: unknown): RowOverride {
		const out: RowOverride = {};
		if (!raw || typeof raw !== 'object') { return out; }
		const r = raw as Record<string, unknown>;
		const cap = (v: unknown): string | undefined => {
			if (typeof v !== 'string') { return undefined; }
			const t = v.trim();
			if (t.length === 0) { return undefined; }
			if (t.length > 4096) { return undefined; }
			return t;
		};
		const vspCmd = cap(r.vspCommand);
		const guiCmd = cap(r.guiCommand);
		const guiUv = cap(r.guiUvProject);
		if (vspCmd) { out.vspCommand = vspCmd; }
		if (guiCmd) { out.guiCommand = guiCmd; }
		if (guiUv) { out.guiUvProject = guiUv; }
		return out;
	}

	dispose(): void {
		if (this.disposed) { return; }
		this.disposed = true;
		// Drop the cached KeePass master password explicitly. JS strings
		// are immutable so we can't zero the memory; the best we can do
		// is release the reference and rely on GC. Per-panel-lifetime
		// caching trades operator convenience for a larger window in
		// which the password lives in heap.
		this.kpMasterPassword = undefined;
		if (SapPickerPanel.current === this) {
			SapPickerPanel.current = undefined;
		}
		while (this.disposables.length > 0) {
			const d = this.disposables.pop();
			try { d?.dispose(); } catch { /* best-effort cleanup */ }
		}
		try { this.panel.dispose(); } catch { /* panel may already be disposed */ }
	}

	/** Reset the singleton (for testing). */
	static _reset(): void {
		if (SapPickerPanel.current && !SapPickerPanel.current.disposed) {
			SapPickerPanel.current.dispose();
		}
		SapPickerPanel.current = undefined;
	}

	/** Expose the current rows[] for testing assertions. */
	_rows(): readonly RowState[] { return this.rows; }
}

interface SerializedRowState {
	key: string;
	sid: string;
	client: string;
	user: string;
	kpMissing: boolean;
	registered: { vsp: boolean; gui: boolean };
	status: { vsp: string; gui: string };
	desired: { vsp: boolean; gui: boolean };
	vspStatus: string;
	guiStatus: string;
	vspError?: string;
	guiError?: string;
	override: RowOverride;
}

function serializeRowState(r: RowState): SerializedRowState {
	return {
		key: r.key,
		sid: r.snapshot.sid,
		client: r.snapshot.client,
		user: r.snapshot.user ?? '',
		kpMissing: r.snapshot.kpMissing,
		registered: r.snapshot.registered,
		status: r.snapshot.status,
		desired: r.desired,
		vspStatus: r.vspStatus,
		guiStatus: r.guiStatus,
		vspError: r.vspError,
		guiError: r.guiError,
		override: r.override,
	};
}

function errorMsg(err: unknown): string {
	if (err instanceof Error) { return err.message; }
	if (typeof err === 'object' && err !== null) { return JSON.stringify(err); }
	return String(err);
}

function sleep(ms: number): Promise<void> {
	return new Promise((resolve) => setTimeout(resolve, ms));
}

// Expose for tests — types only used inside this file otherwise.
export type { SerializedRowState };
// PickerSnapshotRow re-export to keep the panel + webview wire format aligned.
export type { PickerSnapshotRow };
