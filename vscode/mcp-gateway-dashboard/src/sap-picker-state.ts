// SAP Picker — pure state module (Phase B T-B.1..T-B.4).
//
// Mirrors the Go REST contract in internal/api/sap_picker_handler.go:
//   - SAPPickerRow                 -> PickerSnapshotRow
//   - SAPPickerSnapshot            -> PickerSnapshot
//   - SAPBatchBeginResponse        -> BeginBatchResponse
//   - SAPBatchEndRequest/Response  -> EndBatchResponse
//
// Responsibilities (kept DOM-free / vscode-free so unit tests run on plain
// node):
//   * Row state shape including desired-state checkboxes per component.
//   * 3-toggle filter + degenerate-state guard (R-12, R-13).
//   * Per-row lifecycle state machine for the batch Apply flow (R-04, R-09,
//     R-28 UI surface for orphan).
//   * Diff: registered-vs-desired -> BatchOp[] (skipping kpMissing rows so a
//     tampered DOM cannot bypass the R-30 backend guard — acceptance T-B.2).
//   * Expand state keyed by ${sid}-${client}-${component}, lives outside the
//     row list so filter toggles do not collapse already-expanded rows
//     (R-18, acceptance T-B.3).
//
// The 9 RowStatus values come verbatim from spike §3.4 / TASKS T-B.4. Keep
// them in lockstep with the panel's UI labels.

/** Component label — only `vsp` and `gui` are shipped in v1. */
export type Component = 'vsp' | 'gui';

/** Backend flavour for a SAP row. `on-prem` is the legacy KeePass-credential
 *  path (vsp.exe + SAP_USER/SAP_PASSWORD env); `cloud` targets an ADT-over-HTTPS
 *  endpoint authenticated by a browser cookie file — no password ever leaves
 *  KeePass, and the vsp launcher is configured with explicit args instead. */
export type SapKind = 'on-prem' | 'cloud';

/** Connection parameters for a `cloud` SAP row. `cookieFile` is referenced by
 *  PATH only — its contents (the SAP session cookie) are never read into this
 *  module, never logged, and never embedded in config. `readOnly` + `featureRap`
 *  default true for these systems (see buildCloudVspArgs). */
export interface CloudParams {
	sapUrl: string;
	cookieFile: string;
	readOnly: boolean;
	featureRap: boolean;
	/** ADT logon language injected as SAP_LANGUAGE. Optional — defaults to
	 *  'EN' at config-build time when absent. */
	lang?: string;
}

/** Row-level status for a single component (vsp or gui). */
export type RowStatus =
	| 'idle'
	| 'pending'
	| 'in_progress'
	| 'config_added'
	| 'config_added_running'
	| 'config_added_start_failed'
	| 'removed'
	| 'removed_with_orphan'
	| 'removal_failed';

/** Visual category for the 3-toggle filter (R-12, R-13). */
export type RowCategory = 'registered' | 'available' | 'no-credentials';

/** Mirrors Go SAPPickerRow at internal/api/sap_picker_handler.go:38. */
export interface PickerSnapshotRow {
	sid: string;
	client: string;
	user?: string;
	kpMissing: boolean;
	registered: { vsp: boolean; gui: boolean };
	status: { vsp: string; gui: string };
}

/** Mirrors Go SAPPickerSnapshot. */
export interface PickerSnapshot {
	rows: PickerSnapshotRow[];
	warnings: string[];
}

/** 3-toggle filter — all default ON per acceptance T-B.1. */
export interface FilterFlags {
	registered: boolean;
	available: boolean;
	noCredentials: boolean;
}

export const DEFAULT_FILTER: FilterFlags = { registered: true, available: true, noCredentials: true };

/** Per-row override fields (R-22, R-30 UI). Stored as plain strings so the
 *  webview can persist them across filter toggles without re-coercion. */
export interface RowOverride {
	vspCommand?: string;
	guiCommand?: string;
	guiUvProject?: string;
}

/** Working row state — extends the snapshot with desired (post-Apply) state,
 *  per-component status, error string, and override inputs. */
export interface RowState {
	key: string; // rowKey(snapshot)
	snapshot: PickerSnapshotRow;
	desired: { vsp: boolean; gui: boolean };
	vspStatus: RowStatus;
	guiStatus: RowStatus;
	vspError?: string;
	guiError?: string;
	override: RowOverride;
	/** Backend flavour. Absent / 'on-prem' = legacy KeePass path (unchanged).
	 *  'cloud' switches the vsp add to the cookie-file ADT-over-HTTPS launcher
	 *  and suppresses all password-based credential injection. */
	kind?: SapKind;
	/** Cloud connection params — required when `kind === 'cloud'`, ignored
	 *  otherwise. Cookie file is referenced by path only. */
	cloud?: CloudParams;
}

/** Single op produced by buildOpsList — consumed by the batch driver. */
export interface BatchOp {
	kind: 'add' | 'remove';
	component: Component;
	rowKey: string;
	serverName: string;
	config?: Record<string, unknown>;
}

/** Stable key for a row. Includes user so multiple KP entries for the
 *  same (sid, client) — e.g. SID Q26 with users "naumov" and "naumov1"
 *  (observed 2026-05-27) — get distinct rows instead of collapsing.
 *  Format:
 *    sid                 — no client, no user
 *    sid-client          — client, no user
 *    sid--user           — no client, user (empty middle is intentional
 *                          so a Q26 user is never confused with a Q26 client)
 *    sid-client-user     — both present (typical case) */
export function rowKey(snapshot: PickerSnapshotRow): string {
	const user = snapshot.user ?? '';
	if (!snapshot.client && !user) { return snapshot.sid; }
	if (!user) { return `${snapshot.sid}-${snapshot.client}`; }
	return `${snapshot.sid}-${snapshot.client}-${user}`;
}

/** Compose the gateway server name for (component, sid, client). Stays in
 *  lockstep with the YAML-driven generated parser at sap-name-grammar.gen.ts
 *  — kind/sid/client constraints there are the canonical contract. */
export function serverName(component: Component, sid: string, client: string): string {
	const suffix = client ? `-${sid}-${client}` : `-${sid}`;
	return component === 'vsp' ? `vsp${suffix}` : `sap-gui${suffix}`;
}

/** Stable key for the [⋮] expand-state map. */
export function expandKey(sid: string, client: string, component: Component): string {
	return `${sid}-${client}-${component}`;
}

/** Build the vsp launcher args for a `cloud` SAP row. Pure — no env, no
 *  secrets. The cookie file is passed by PATH as two tokens
 *  (`--cookie-file <path>`); its contents are never read here. `--read-only`
 *  and `--feature-rap on` are conditional on the row's flags (both default
 *  true for cloud systems) so an operator can opt out per row without the
 *  launcher receiving a contradictory flag. Order is deterministic so the
 *  emitted config is stable across re-adds and snapshot-comparable in tests. */
export function buildCloudVspArgs(cloud: CloudParams): string[] {
	const args: string[] = [];
	if (cloud.readOnly) { args.push('--read-only'); }
	if (cloud.featureRap) { args.push('--feature-rap', 'on'); }
	args.push('--cookie-file', cloud.cookieFile);
	return args;
}

/** Map a snapshot row to its visual category (filter axis). */
export function categorizeRow(row: PickerSnapshotRow): RowCategory {
	if (row.kpMissing) { return 'no-credentials'; }
	if (row.registered.vsp || row.registered.gui) { return 'registered'; }
	return 'available';
}

/** R-13 degenerate-state guard: when all three filter axes are OFF, the user
 *  has hidden every row. Restore `available + noCredentials` (the
 *  unregistered axes) and surface a hint banner via `restored: true`. */
export function degenerateGuard(filter: FilterFlags): { filter: FilterFlags; restored: boolean } {
	if (!filter.registered && !filter.available && !filter.noCredentials) {
		return {
			filter: { registered: false, available: true, noCredentials: true },
			restored: true,
		};
	}
	return { filter, restored: false };
}

/** Apply the 3-toggle filter and substring search to a row list. Search is
 *  case-insensitive across SID, Client, and User. Empty search short-circuits.
 *  No regex per CLAUDE.md "Regex Discipline (MANDATORY)" — String.includes is
 *  sufficient for substring fuzzy match. */
export function applyFilter(rows: RowState[], filter: FilterFlags, search: string): RowState[] {
	const trimmed = search.trim().toLowerCase();
	return rows.filter((r) => {
		const cat = categorizeRow(r.snapshot);
		if (cat === 'registered' && !filter.registered) { return false; }
		if (cat === 'available' && !filter.available) { return false; }
		if (cat === 'no-credentials' && !filter.noCredentials) { return false; }
		if (trimmed.length === 0) { return true; }
		const sid = r.snapshot.sid.toLowerCase();
		const client = r.snapshot.client.toLowerCase();
		const user = (r.snapshot.user ?? '').toLowerCase();
		return sid.includes(trimmed) || client.includes(trimmed) || user.includes(trimmed);
	});
}

/** Defaults pulled from workspace settings — applied as a fallback when
 *  the row-level override does not supply the field. Caller resolves
 *  these once before buildOpsList runs. */
export interface PickerDefaults {
	/** mcpGateway.defaultVspCommand OR mcpDashboard.vibingPath (legacy). */
	vspCommand?: string;
	/** mcpGateway.defaultGuiUvProject OR mcpDashboard.sapGuiPath (legacy). */
	guiUvProject?: string;
	/** mcpGateway.uvPath OR mcpDashboard.uvPath (legacy). */
	uvPath?: string;
	/** mcpGateway.defaultGuiMode — 'uv' wraps the GUI launcher with
	 *  `uv run --project <guiUvProject> sap-gui-server`; any other value
	 *  treats defaultVspCommand as the literal GUI command. */
	defaultGuiMode?: string;
}

/** Per-op outcome from buildOpsList — useful when the caller needs to
 *  surface "skipped because X is missing" instead of silently swallowing
 *  the operator's Apply intent (2026-05-27 user report: clicking Apply
 *  did nothing because the override commands were empty). */
export interface BuildOpsResult {
	ops: BatchOp[];
	skipped: { rowKey: string; component: Component; reason: string }[];
}

/** Diff registered-vs-desired into a flat BatchOp list, with defaults
 *  pulled from settings used as a fallback when override is empty. Skips
 *  `kpMissing` rows entirely (defence-in-depth: the UI disables their
 *  checkboxes, this layer rejects any change that slips through a
 *  tampered DOM). Skipped add ops surface in result.skipped with a
 *  human-readable reason so the panel can show a clear banner. */
export function buildOpsListWithDefaults(rows: RowState[], defaults: PickerDefaults): BuildOpsResult {
	const ops: BatchOp[] = [];
	const skipped: BuildOpsResult['skipped'] = [];

	for (const r of rows) {
		if (r.snapshot.kpMissing) { continue; }
		const { sid, client } = r.snapshot;

		// VSP delta
		if (r.desired.vsp !== r.snapshot.registered.vsp) {
			const name = serverName('vsp', sid, client);
			if (r.desired.vsp) {
				const cmd = (r.override.vspCommand && r.override.vspCommand.trim())
					|| (defaults.vspCommand && defaults.vspCommand.trim())
					|| '';
				if (!cmd) {
					skipped.push({
						rowKey: r.key,
						component: 'vsp',
						reason: 'VSP command not set — fill the row override or mcpGateway.defaultVspCommand',
					});
				} else if (r.kind === 'cloud' && r.cloud) {
					// Cloud flavour: cookie-file ADT-over-HTTPS launcher. The
					// vsp binary receives explicit args instead of relying on
					// password env vars; SAP_URL etc. are injected as env later
					// by the panel's cloud branch of enrichConfigWithCreds.
					ops.push({
						kind: 'add', component: 'vsp', rowKey: r.key, serverName: name,
						config: { command: cmd, args: buildCloudVspArgs(r.cloud) },
					});
				} else {
					ops.push({
						kind: 'add', component: 'vsp', rowKey: r.key, serverName: name,
						config: { command: cmd },
					});
				}
			} else {
				ops.push({ kind: 'remove', component: 'vsp', rowKey: r.key, serverName: name });
			}
		}

		// GUI delta
		if (r.desired.gui !== r.snapshot.registered.gui) {
			const name = serverName('gui', sid, client);
			if (r.desired.gui) {
				// uv mode (spike §3.5 buildConfig): launch via
				//   uv run --project <guiUvProject> sap-gui-server
				// Override.guiCommand wins when present. Otherwise
				// build from uvPath + guiUvProject if both available.
				const overrideCmd = r.override.guiCommand && r.override.guiCommand.trim();
				const overrideProject = (r.override.guiUvProject && r.override.guiUvProject.trim())
					|| (defaults.guiUvProject && defaults.guiUvProject.trim())
					|| '';
				const uv = defaults.uvPath && defaults.uvPath.trim();
				let config: Record<string, unknown> | undefined;
				let reason = '';
				if (overrideCmd) {
					config = { command: overrideCmd };
				} else if (defaults.defaultGuiMode === 'uv' && uv && overrideProject) {
					config = {
						command: uv,
						args: ['run', '--project', overrideProject, 'sap-gui-server'],
					};
				} else if (defaults.vspCommand && defaults.vspCommand.trim() && defaults.defaultGuiMode !== 'uv') {
					// Same launcher as VSP when not in uv mode (common
					// when one binary handles both component flavours).
					config = { command: defaults.vspCommand.trim() };
				} else {
					if (defaults.defaultGuiMode === 'uv') {
						if (!uv) {
							reason = 'GUI uv mode needs mcpGateway.uvPath';
						} else if (!overrideProject) {
							reason = 'GUI uv mode needs mcpGateway.defaultGuiUvProject';
						} else {
							reason = 'GUI command not resolved';
						}
					} else {
						reason = 'GUI command not set — fill the row override, or set mcpGateway.defaultGuiMode=uv + mcpGateway.uvPath + mcpGateway.defaultGuiUvProject';
					}
				}
				if (config) {
					ops.push({
						kind: 'add', component: 'gui', rowKey: r.key, serverName: name,
						config,
					});
				} else {
					skipped.push({ rowKey: r.key, component: 'gui', reason });
				}
			} else {
				ops.push({ kind: 'remove', component: 'gui', rowKey: r.key, serverName: name });
			}
		}
	}

	return { ops, skipped };
}

/** Legacy signature kept for the existing test suite. Drops skipped
 *  ops silently — new callers should use buildOpsListWithDefaults. */
export function buildOpsList(rows: RowState[]): BatchOp[] {
	return buildOpsListWithDefaults(rows, {}).ops;
}

/** Initial RowState[] seeded from a snapshot. Desired = current registered. */
export function initRowsFromSnapshot(snapshot: PickerSnapshot): RowState[] {
	return snapshot.rows.map((s) => ({
		key: rowKey(s),
		snapshot: s,
		desired: { vsp: s.registered.vsp, gui: s.registered.gui },
		vspStatus: 'idle',
		guiStatus: 'idle',
		override: {},
	}));
}

/** Lifecycle events drive the per-component status transitions (R-04, R-09,
 *  R-28 orphan UI). The state machine is total — every event has exactly one
 *  output state per current state. */
export type LifecycleEvent =
	| { kind: 'queue'; rowKey: string; component: Component }
	| { kind: 'start_op'; rowKey: string; component: Component }
	| { kind: 'add_ok'; rowKey: string; component: Component }
	| { kind: 'add_started'; rowKey: string; component: Component }
	| { kind: 'add_start_failed'; rowKey: string; component: Component; error: string }
	| { kind: 'add_failed'; rowKey: string; component: Component; error: string }
	| { kind: 'remove_ok'; rowKey: string; component: Component }
	| { kind: 'remove_orphan'; rowKey: string; component: Component; error: string }
	| { kind: 'remove_failed'; rowKey: string; component: Component; error: string }
	| { kind: 'reset'; rowKey: string; component: Component };

/** Pure transition. Returns the row unchanged if `ev.rowKey` mismatches. */
export function transitionRow(row: RowState, ev: LifecycleEvent): RowState {
	if (ev.rowKey !== row.key) { return row; }
	const setStatus = (component: Component, status: RowStatus, error?: string): RowState => {
		if (component === 'vsp') {
			return { ...row, vspStatus: status, vspError: error };
		}
		return { ...row, guiStatus: status, guiError: error };
	};
	switch (ev.kind) {
		case 'queue': return setStatus(ev.component, 'pending');
		case 'start_op': return setStatus(ev.component, 'in_progress');
		case 'add_ok': return setStatus(ev.component, 'config_added');
		case 'add_started': return setStatus(ev.component, 'config_added_running');
		case 'add_start_failed': return setStatus(ev.component, 'config_added_start_failed', ev.error);
		case 'add_failed': return setStatus(ev.component, 'idle', ev.error);
		case 'remove_ok': return setStatus(ev.component, 'removed');
		case 'remove_orphan': return setStatus(ev.component, 'removed_with_orphan', ev.error);
		case 'remove_failed': return setStatus(ev.component, 'removal_failed', ev.error);
		case 'reset': return setStatus(ev.component, 'idle');
	}
}

/** Recoverable failure — eligible for retry-failed-rows. */
export function isFailed(status: RowStatus): boolean {
	return (
		status === 'config_added_start_failed' ||
		status === 'removed_with_orphan' ||
		status === 'removal_failed'
	);
}

/** Terminal status — apply loop will not transition this further on its own. */
export function isTerminal(status: RowStatus): boolean {
	return (
		status === 'idle' ||
		status === 'config_added_running' ||
		status === 'config_added_start_failed' ||
		status === 'removed' ||
		status === 'removed_with_orphan' ||
		status === 'removal_failed'
	);
}

/** Expand state map — keyed by `${sid}-${client}-${component}`. Stored
 *  outside the row array so filter toggles do not erase the user's expand
 *  decisions (R-18). */
export type ExpandMap = Record<string, boolean>;

/** Set / clear an entry. Returns the same instance when no change is needed
 *  so React-style equality checks short-circuit. Frees the entry on collapse
 *  to avoid unbounded growth across long sessions. */
export function setExpand(map: ExpandMap, key: string, expanded: boolean): ExpandMap {
	if (expanded === Boolean(map[key])) { return map; }
	const next = { ...map };
	if (expanded) {
		next[key] = true;
	} else {
		delete next[key];
	}
	return next;
}

/** Drop expand entries for rows whose `kpMissing` flag flipped to true on
 *  re-snapshot — the row's checkboxes will be disabled, and per spike §9 #18
 *  Q7.2 / R-22 the override values must not persist past collapse for those
 *  rows. Caller invokes this on every snapshot refresh. */
export function pruneExpandForKpMissing(map: ExpandMap, rows: RowState[]): ExpandMap {
	const kpMissingKeys = new Set<string>();
	for (const r of rows) {
		if (r.snapshot.kpMissing) {
			kpMissingKeys.add(expandKey(r.snapshot.sid, r.snapshot.client, 'vsp'));
			kpMissingKeys.add(expandKey(r.snapshot.sid, r.snapshot.client, 'gui'));
		}
	}
	let next = map;
	for (const k of Object.keys(map)) {
		if (kpMissingKeys.has(k)) {
			next = setExpand(next, k, false);
		}
	}
	return next;
}

/** Reset only failed-component statuses to `idle` so a retry-failed-rows
 *  pass re-runs them through the apply driver without disturbing succeeded
 *  rows. Returns a new array instance only when at least one row changes. */
export function resetFailedRowsForRetry(rows: RowState[]): RowState[] {
	let changed = false;
	const next = rows.map((r) => {
		const vspFailed = isFailed(r.vspStatus);
		const guiFailed = isFailed(r.guiStatus);
		if (!vspFailed && !guiFailed) { return r; }
		changed = true;
		return {
			...r,
			vspStatus: vspFailed ? 'idle' : r.vspStatus,
			guiStatus: guiFailed ? 'idle' : r.guiStatus,
			vspError: vspFailed ? undefined : r.vspError,
			guiError: guiFailed ? undefined : r.guiError,
		};
	});
	return changed ? next : rows;
}

/** Drive a queue of ops with bounded concurrency. Each op runs `fn(op)` and
 *  resolves to its event (which the caller folds back via transitionRow).
 *  `concurrency` defaults to 4 per spike §3.5 (R-04, R-09). */
export async function runWithConcurrency<T>(
	ops: BatchOp[],
	fn: (op: BatchOp) => Promise<T>,
	concurrency = 4,
	onResult?: (op: BatchOp, result: T) => void,
): Promise<T[]> {
	const results: T[] = new Array(ops.length);
	let next = 0;
	const cap = Math.max(1, Math.min(concurrency, ops.length));
	const workers: Array<Promise<void>> = [];
	for (let w = 0; w < cap; w++) {
		workers.push((async () => {
			for (;;) {
				const idx = next++;
				if (idx >= ops.length) { return; }
				const op = ops[idx];
				const r = await fn(op);
				results[idx] = r;
				if (onResult) { onResult(op, r); }
			}
		})());
	}
	await Promise.all(workers);
	return results;
}
