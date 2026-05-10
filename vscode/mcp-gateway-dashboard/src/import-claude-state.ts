// Import-from-Claude — pure state module (Phase E T-E.1..T-E.3).
//
// Mirrors the Go REST contract in internal/api/claude_code_import_handler.go:
//   - ImportSnapshotRow            -> ImportSnapshotRow
//   - ImportSnapshotResponse       -> ImportSnapshot
//   - claudeimport.Op              -> ImportOp
//   - claudeimport.OpResult        -> ImportOpResult
//
// Responsibilities (DOM-free / vscode-free so unit tests run on plain node):
//   * Snapshot row shape including provenance (`previously_imported`),
//     drift (`drift_fields`), and gateway-name collision flag.
//   * 3-toggle filter (registered, available, with-drift) + degenerate guard.
//   * Per-row Action (copy / move) + ConflictPolicy (skip / overwrite) state.
//   * R-23 warning rule: action=move + conflict=overwrite surfaces an explicit
//     "mutates source AND discards local edits" banner.
//   * Per-row lifecycle state machine (idle / pending / in_progress /
//     applied / skipped / conflict / error) for the apply flow (mirrors
//     Phase B `RowStatus` shape).
//   * Diff: rows -> ImportOp[] (skipping unchecked rows; defence-in-depth
//     against tampered DOM by re-validating fields host-side).
//
// The 7 RowStatus values come from spike §4.2 / TASKS T-E.3 acceptance:
// idle / pending / in_progress / applied / skipped / conflict / error.

/** Source identifier — matches Go claudeconfig.Source enum. */
export type ImportSource = 'cc_global' | 'cc_project' | 'desktop';

/** Per-row status during an apply pass (spike §4.2). */
export type ImportRowStatus =
	| 'idle'
	| 'pending'
	| 'in_progress'
	| 'applied'
	| 'skipped'
	| 'conflict'
	| 'error';

/** Action enum — only copy and move (no duplicate per R-25). */
export type ImportAction = 'copy' | 'move';

/** Conflict policy — skip = leave gateway entry alone, overwrite = replace. */
export type ImportConflict = 'skip' | 'overwrite';

/** Visual category for the 3-toggle filter. */
export type ImportRowCategory = 'gateway-only' | 'available' | 'drift';

/** Mirrors Go ImportSnapshotRow at internal/api/claude_code_import_handler.go:19. */
export interface ImportSnapshotRow {
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
}

/** Mirrors Go ImportSnapshotResponse. */
export interface ImportSnapshot {
	source: ImportSource;
	path: string;
	exists: boolean;
	rows: ImportSnapshotRow[];
	warnings?: string[];
}

/** Mirrors Go claudeimport.OpResult shape. */
export interface ImportOpResult {
	name: string;
	dest_name: string;
	action: ImportAction;
	status: 'applied' | 'skipped' | 'conflict' | 'error';
	reason?: string;
	resolved_command?: string;
	drift_fields?: string[];
	source_updated: boolean;
	source_updated_at?: string;
	provenance_warning?: string;
}

/** Wire shape for an Op posted to /import-apply. */
export interface ImportOp {
	source: ImportSource;
	project_root?: string;
	name: string;
	dest_name?: string;
	action: ImportAction;
	conflict: ImportConflict;
}

/** Filter axes — all default ON per acceptance T-E.1. */
export interface ImportFilterFlags {
	gatewayOnly: boolean;
	available: boolean;
	drift: boolean;
}

export const DEFAULT_IMPORT_FILTER: ImportFilterFlags = {
	gatewayOnly: true,
	available: true,
	drift: true,
};

/** Working row state — extends snapshot with checkbox/action/conflict
 *  and per-component status. The `checked` flag controls inclusion in the
 *  apply batch; defaults to false until the operator opts in. */
export interface ImportRowState {
	key: string; // composite of source + name (stable across snapshot refresh)
	snapshot: ImportSnapshotRow;
	checked: boolean;
	action: ImportAction;
	conflict: ImportConflict;
	destName: string; // empty -> reuse snapshot.name
	status: ImportRowStatus;
	error?: string;
	resolvedCommand?: string;
	driftFieldsApplied?: string[]; // populated post-apply for display
}

/** Stable key for a (source, name) pair. */
export function importRowKey(source: ImportSource, name: string): string {
	return `${source}::${name}`;
}

/** Map a snapshot row to its visual category (filter axis). */
export function categorizeImportRow(row: ImportSnapshotRow): ImportRowCategory {
	if (row.gateway_has_name && row.drift_fields && row.drift_fields.length > 0) {
		return 'drift';
	}
	if (row.gateway_has_name) {
		return 'gateway-only'; // collision: gateway already has the same name
	}
	return 'available';
}

/** Degenerate-state guard — when all three filter axes are OFF, restore
 *  available + drift (the actionable axes) and surface a hint banner. */
export function degenerateImportGuard(
	filter: ImportFilterFlags,
): { filter: ImportFilterFlags; restored: boolean } {
	if (!filter.gatewayOnly && !filter.available && !filter.drift) {
		return {
			filter: { gatewayOnly: false, available: true, drift: true },
			restored: true,
		};
	}
	return { filter, restored: false };
}

/** Apply the 3-toggle filter and substring search to a row list. Search is
 *  case-insensitive across name, command, url. Empty search short-circuits.
 *  No regex per CLAUDE.md "Regex Discipline (MANDATORY)" — String.includes is
 *  sufficient for substring fuzzy match. */
export function applyImportFilter(
	rows: ImportRowState[],
	filter: ImportFilterFlags,
	search: string,
): ImportRowState[] {
	const trimmed = search.trim().toLowerCase();
	return rows.filter((r) => {
		const cat = categorizeImportRow(r.snapshot);
		if (cat === 'gateway-only' && !filter.gatewayOnly) { return false; }
		if (cat === 'available' && !filter.available) { return false; }
		if (cat === 'drift' && !filter.drift) { return false; }
		if (trimmed.length === 0) { return true; }
		const name = r.snapshot.name.toLowerCase();
		const command = (r.snapshot.command ?? '').toLowerCase();
		const url = (r.snapshot.url ?? '').toLowerCase();
		return name.includes(trimmed) || command.includes(trimmed) || url.includes(trimmed);
	});
}

/** Build the ImportOp[] payload for the /import-apply call.
 *
 * Defence-in-depth: skips rows that are not `checked`, validates the action
 * + conflict enum values, and drops bogus rows entirely (a tampered DOM
 * cannot inject anything outside the typed union).
 */
export function buildImportOpsList(
	rows: ImportRowState[],
	projectRoot?: string,
): ImportOp[] {
	const ops: ImportOp[] = [];
	for (const r of rows) {
		if (!r.checked) { continue; }
		if (r.action !== 'copy' && r.action !== 'move') { continue; }
		if (r.conflict !== 'skip' && r.conflict !== 'overwrite') { continue; }
		// Skip rows already terminal (applied/skipped) so they aren't re-run
		// when the user clicks Apply twice in a row without reloading.
		if (r.status === 'applied' || r.status === 'skipped') { continue; }
		const op: ImportOp = {
			source: r.snapshot.source,
			name: r.snapshot.name,
			action: r.action,
			conflict: r.conflict,
		};
		const trimmedDest = (r.destName ?? '').trim();
		if (trimmedDest.length > 0 && trimmedDest !== r.snapshot.name) {
			op.dest_name = trimmedDest;
		}
		if (r.snapshot.source === 'cc_project' && projectRoot && projectRoot.trim().length > 0) {
			op.project_root = projectRoot.trim();
		}
		ops.push(op);
	}
	return ops;
}

/** Initial ImportRowState[] seeded from a snapshot.
 *
 * Defaults (per T-E.1 / T-E.2 acceptance):
 *   - checked: false (operator opts in per row)
 *   - action: 'copy' (safer default — leaves source unchanged)
 *   - conflict: 'skip' (safer default — never overwrites without confirmation)
 *   - destName: '' (reuse snapshot.name unless edited)
 *   - status: 'idle'
 */
export function initImportRowsFromSnapshot(snapshot: ImportSnapshot): ImportRowState[] {
	return snapshot.rows.map((s) => ({
		key: importRowKey(s.source, s.name),
		snapshot: s,
		checked: false,
		action: 'copy' as ImportAction,
		conflict: 'skip' as ImportConflict,
		destName: '',
		status: 'idle' as ImportRowStatus,
	}));
}

/** R-23 acceptance: detect rows that combine action=move + conflict=overwrite,
 *  the only combination that mutates the source AND discards local edits.
 *  Webview surfaces an explicit warning banner when at least one *checked*
 *  row matches. Skipping unchecked rows so a default-overwrite row that the
 *  operator did not enable does not raise the alarm. */
export function detectMoveOverwriteRisk(rows: ImportRowState[]): {
	count: number;
	names: string[];
} {
	const names: string[] = [];
	for (const r of rows) {
		if (!r.checked) { continue; }
		if (r.action === 'move' && r.conflict === 'overwrite') {
			names.push(r.snapshot.name);
		}
	}
	return { count: names.length, names };
}

/** Lifecycle events drive the per-row status transitions. */
export type ImportLifecycleEvent =
	| { kind: 'queue'; rowKey: string }
	| { kind: 'start_op'; rowKey: string }
	| { kind: 'op_result'; rowKey: string; result: ImportOpResult }
	| { kind: 'op_error'; rowKey: string; error: string }
	| { kind: 'reset'; rowKey: string };

/** Pure transition. Returns the row unchanged if `ev.rowKey` mismatches. */
export function transitionImportRow(
	row: ImportRowState,
	ev: ImportLifecycleEvent,
): ImportRowState {
	if (ev.rowKey !== row.key) { return row; }
	switch (ev.kind) {
		case 'queue':
			return { ...row, status: 'pending', error: undefined };
		case 'start_op':
			return { ...row, status: 'in_progress', error: undefined };
		case 'op_result': {
			// Translate Go OpResult.status to TS RowStatus directly.
			const next: ImportRowState = {
				...row,
				status: ev.result.status,
				resolvedCommand: ev.result.resolved_command,
				driftFieldsApplied: ev.result.drift_fields,
				error: ev.result.reason,
			};
			return next;
		}
		case 'op_error':
			return { ...row, status: 'error', error: ev.error };
		case 'reset':
			return {
				...row,
				status: 'idle',
				error: undefined,
				resolvedCommand: undefined,
				driftFieldsApplied: undefined,
			};
	}
}

/** Recoverable failure — eligible for retry-failed-rows (T-E.3 acceptance). */
export function isImportFailed(status: ImportRowStatus): boolean {
	return status === 'error' || status === 'conflict';
}

/** Reset only failed-row statuses to `idle` so a retry-failed-rows pass
 *  re-runs them through the apply driver without disturbing succeeded rows.
 *  Returns a new array instance only when at least one row changes. */
export function resetFailedImportRowsForRetry(rows: ImportRowState[]): ImportRowState[] {
	let changed = false;
	const next = rows.map((r) => {
		if (!isImportFailed(r.status)) { return r; }
		changed = true;
		return {
			...r,
			status: 'idle' as ImportRowStatus,
			error: undefined,
			resolvedCommand: undefined,
			driftFieldsApplied: undefined,
		};
	});
	return changed ? next : rows;
}

/** Build a "preview" entry for the move-confirm modal (T-E.3): for each
 *  CHECKED row, return final-state intent so the operator sees exactly what
 *  will happen. Skip operations are suppressed (they're no-ops). */
export interface ImportPreviewEntry {
	source: ImportSource;
	name: string;
	destName: string;
	action: ImportAction;
	conflict: ImportConflict;
	gatewayHasName: boolean;
	driftFields: string[];
}

export function buildImportPreview(rows: ImportRowState[]): ImportPreviewEntry[] {
	const out: ImportPreviewEntry[] = [];
	for (const r of rows) {
		if (!r.checked) { continue; }
		out.push({
			source: r.snapshot.source,
			name: r.snapshot.name,
			destName: r.destName.trim() || r.snapshot.name,
			action: r.action,
			conflict: r.conflict,
			gatewayHasName: r.snapshot.gateway_has_name,
			driftFields: Array.isArray(r.snapshot.drift_fields) ? r.snapshot.drift_fields.slice() : [],
		});
	}
	return out;
}
