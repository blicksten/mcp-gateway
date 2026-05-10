import { strict as assert } from 'node:assert';
import { describe, it } from 'mocha';
import {
	type ImportSnapshot,
	type ImportSnapshotRow,
	type ImportRowState,
	type ImportSource,
	type ImportOpResult,
	importRowKey,
	categorizeImportRow,
	degenerateImportGuard,
	applyImportFilter,
	buildImportOpsList,
	buildImportPreview,
	initImportRowsFromSnapshot,
	transitionImportRow,
	isImportFailed,
	resetFailedImportRowsForRetry,
	detectMoveOverwriteRisk,
	DEFAULT_IMPORT_FILTER,
} from '../import-claude-state';

function snapRow(over: Partial<ImportSnapshotRow>): ImportSnapshotRow {
	return {
		source: 'cc_global',
		name: 'serverA',
		type: 'stdio',
		command: 'npx',
		args: ['-y', '@example/mcp-server'],
		gateway_has_name: false,
		previously_imported: false,
		...over,
	};
}

function rs(over: Partial<ImportRowState> & { snapshot: ImportSnapshotRow }): ImportRowState {
	return {
		key: importRowKey(over.snapshot.source, over.snapshot.name),
		checked: false,
		action: 'copy',
		conflict: 'skip',
		destName: '',
		status: 'idle',
		...over,
	};
}

describe('import-claude-state — keys', () => {
	it('importRowKey embeds source so cc_global/cc_project share-name does not collide', () => {
		assert.strictEqual(importRowKey('cc_global', 'serverA'), 'cc_global::serverA');
		assert.strictEqual(importRowKey('cc_project', 'serverA'), 'cc_project::serverA');
		assert.notStrictEqual(
			importRowKey('cc_global', 'serverA'),
			importRowKey('cc_project', 'serverA'),
		);
	});
});

describe('import-claude-state — categorization', () => {
	it('drift wins when gateway has name + drift_fields set', () => {
		const r = snapRow({ gateway_has_name: true, drift_fields: ['args'] });
		assert.strictEqual(categorizeImportRow(r), 'drift');
	});
	it('gateway-only when gateway has name + no drift', () => {
		const r = snapRow({ gateway_has_name: true });
		assert.strictEqual(categorizeImportRow(r), 'gateway-only');
	});
	it('available default', () => {
		assert.strictEqual(categorizeImportRow(snapRow({})), 'available');
	});
});

describe('import-claude-state — degenerateImportGuard (R-13 mirror)', () => {
	it('all-off restores available + drift and flags restored', () => {
		const g = degenerateImportGuard({ gatewayOnly: false, available: false, drift: false });
		assert.strictEqual(g.restored, true);
		assert.deepStrictEqual(g.filter, { gatewayOnly: false, available: true, drift: true });
	});
	it('default filter passes through unchanged', () => {
		const g = degenerateImportGuard(DEFAULT_IMPORT_FILTER);
		assert.strictEqual(g.restored, false);
		assert.strictEqual(g.filter, DEFAULT_IMPORT_FILTER);
	});
});

describe('import-claude-state — applyImportFilter', () => {
	const rows: ImportRowState[] = [
		rs({ snapshot: snapRow({ name: 'gw-only-server', gateway_has_name: true }) }),
		rs({ snapshot: snapRow({ name: 'available-server' }) }),
		rs({ snapshot: snapRow({ name: 'drift-server', gateway_has_name: true, drift_fields: ['args', 'env'] }) }),
	];

	it('all-on filter + empty search returns everything', () => {
		assert.strictEqual(applyImportFilter(rows, DEFAULT_IMPORT_FILTER, '').length, 3);
	});
	it('hides gateway-only when toggle off', () => {
		const got = applyImportFilter(rows, { gatewayOnly: false, available: true, drift: true }, '');
		assert.strictEqual(got.length, 2);
		assert.ok(got.every((r) => r.snapshot.name !== 'gw-only-server'));
	});
	it('hides drift when toggle off', () => {
		const got = applyImportFilter(rows, { gatewayOnly: true, available: true, drift: false }, '');
		assert.strictEqual(got.length, 2);
		assert.ok(got.every((r) => r.snapshot.name !== 'drift-server'));
	});
	it('substring match is case-insensitive on name', () => {
		const got = applyImportFilter(rows, DEFAULT_IMPORT_FILTER, 'DRIFT');
		assert.strictEqual(got.length, 1);
		assert.strictEqual(got[0].snapshot.name, 'drift-server');
	});
	it('substring match works on command', () => {
		const r = rs({ snapshot: snapRow({ name: 'x', command: 'uvx my-tool' }) });
		assert.strictEqual(applyImportFilter([r], DEFAULT_IMPORT_FILTER, 'uvx').length, 1);
	});
});

describe('import-claude-state — buildImportOpsList', () => {
	it('skips unchecked rows entirely (defence-in-depth)', () => {
		const rows: ImportRowState[] = [
			rs({ snapshot: snapRow({ name: 'a' }), checked: false }),
			rs({ snapshot: snapRow({ name: 'b' }), checked: false }),
		];
		assert.deepStrictEqual(buildImportOpsList(rows), []);
	});
	it('emits ops only for checked rows', () => {
		const rows: ImportRowState[] = [
			rs({ snapshot: snapRow({ name: 'a' }), checked: true }),
			rs({ snapshot: snapRow({ name: 'b' }), checked: false }),
		];
		const ops = buildImportOpsList(rows);
		assert.strictEqual(ops.length, 1);
		assert.strictEqual(ops[0].name, 'a');
		assert.strictEqual(ops[0].action, 'copy');
		assert.strictEqual(ops[0].conflict, 'skip');
	});
	it('threads project_root only for cc_project source', () => {
		const rows: ImportRowState[] = [
			rs({ snapshot: snapRow({ name: 'g', source: 'cc_global' }), checked: true }),
			rs({ snapshot: snapRow({ name: 'p', source: 'cc_project' }), checked: true }),
		];
		const ops = buildImportOpsList(rows, '/workspace/abc');
		const opG = ops.find((o) => o.name === 'g');
		const opP = ops.find((o) => o.name === 'p');
		assert.ok(opG);
		assert.ok(opP);
		assert.strictEqual(opG!.project_root, undefined);
		assert.strictEqual(opP!.project_root, '/workspace/abc');
	});
	it('emits dest_name only when distinct from snapshot.name', () => {
		const r1 = rs({ snapshot: snapRow({ name: 'a' }), checked: true, destName: '   a   ' });
		const r2 = rs({ snapshot: snapRow({ name: 'a' }), checked: true, destName: 'gateway-a' });
		const r3 = rs({ snapshot: snapRow({ name: 'a' }), checked: true, destName: '' });
		const ops1 = buildImportOpsList([r1]);
		const ops2 = buildImportOpsList([r2]);
		const ops3 = buildImportOpsList([r3]);
		assert.strictEqual(ops1[0].dest_name, undefined);
		assert.strictEqual(ops2[0].dest_name, 'gateway-a');
		assert.strictEqual(ops3[0].dest_name, undefined);
	});
	it('skips terminal-status rows so a second Apply does not re-run them', () => {
		const r1 = rs({ snapshot: snapRow({ name: 'a' }), checked: true, status: 'applied' });
		const r2 = rs({ snapshot: snapRow({ name: 'b' }), checked: true, status: 'skipped' });
		const r3 = rs({ snapshot: snapRow({ name: 'c' }), checked: true, status: 'idle' });
		const ops = buildImportOpsList([r1, r2, r3]);
		assert.strictEqual(ops.length, 1);
		assert.strictEqual(ops[0].name, 'c');
	});
	it('rejects invalid action / conflict (defence-in-depth)', () => {
		const r1 = rs({
			snapshot: snapRow({ name: 'a' }),
			checked: true,
			action: 'duplicate' as 'copy' | 'move',  // tampered
		});
		const r2 = rs({
			snapshot: snapRow({ name: 'b' }),
			checked: true,
			conflict: 'merge' as 'skip' | 'overwrite', // tampered
		});
		assert.deepStrictEqual(buildImportOpsList([r1, r2]), []);
	});
});

describe('import-claude-state — detectMoveOverwriteRisk (R-23)', () => {
	it('counts only checked move+overwrite rows', () => {
		const rows: ImportRowState[] = [
			rs({ snapshot: snapRow({ name: 'a' }), checked: true, action: 'move', conflict: 'overwrite' }),
			rs({ snapshot: snapRow({ name: 'b' }), checked: false, action: 'move', conflict: 'overwrite' }),
			rs({ snapshot: snapRow({ name: 'c' }), checked: true, action: 'copy', conflict: 'overwrite' }),
		];
		const r = detectMoveOverwriteRisk(rows);
		assert.strictEqual(r.count, 1);
		assert.deepStrictEqual(r.names, ['a']);
	});
	it('returns 0/[] when no checked move+overwrite present', () => {
		const rows: ImportRowState[] = [
			rs({ snapshot: snapRow({ name: 'a' }), checked: true, action: 'copy', conflict: 'skip' }),
		];
		const r = detectMoveOverwriteRisk(rows);
		assert.strictEqual(r.count, 0);
		assert.deepStrictEqual(r.names, []);
	});
});

describe('import-claude-state — transitionImportRow + retry', () => {
	const r0 = rs({ snapshot: snapRow({ name: 'a' }) });

	it('queue → pending', () => {
		const r1 = transitionImportRow(r0, { kind: 'queue', rowKey: r0.key });
		assert.strictEqual(r1.status, 'pending');
	});
	it('start_op → in_progress', () => {
		const r1 = transitionImportRow(r0, { kind: 'start_op', rowKey: r0.key });
		assert.strictEqual(r1.status, 'in_progress');
	});
	it('op_result(applied) carries resolved_command + drift_fields', () => {
		const result: ImportOpResult = {
			name: 'a',
			dest_name: 'a',
			action: 'copy',
			status: 'applied',
			resolved_command: '/usr/bin/npx',
			drift_fields: ['args'],
			source_updated: false,
		};
		const r1 = transitionImportRow(r0, { kind: 'op_result', rowKey: r0.key, result });
		assert.strictEqual(r1.status, 'applied');
		assert.strictEqual(r1.resolvedCommand, '/usr/bin/npx');
		assert.deepStrictEqual(r1.driftFieldsApplied, ['args']);
	});
	it('op_result(error) surfaces reason as error', () => {
		const result: ImportOpResult = {
			name: 'a', dest_name: 'a', action: 'copy', status: 'error',
			reason: 'boom', source_updated: false,
		};
		const r1 = transitionImportRow(r0, { kind: 'op_result', rowKey: r0.key, result });
		assert.strictEqual(r1.status, 'error');
		assert.strictEqual(r1.error, 'boom');
	});
	it('op_error sets error', () => {
		const r1 = transitionImportRow(r0, { kind: 'op_error', rowKey: r0.key, error: 'kapow' });
		assert.strictEqual(r1.status, 'error');
		assert.strictEqual(r1.error, 'kapow');
	});
	it('mismatching rowKey returns row unchanged (instance equality preserved)', () => {
		const r1 = transitionImportRow(r0, { kind: 'queue', rowKey: 'OTHER' });
		assert.strictEqual(r1, r0);
	});
});

describe('import-claude-state — isImportFailed + resetFailedImportRowsForRetry', () => {
	it('error and conflict are failed; applied/skipped/idle/in_progress are not', () => {
		assert.strictEqual(isImportFailed('error'), true);
		assert.strictEqual(isImportFailed('conflict'), true);
		assert.strictEqual(isImportFailed('applied'), false);
		assert.strictEqual(isImportFailed('skipped'), false);
		assert.strictEqual(isImportFailed('idle'), false);
		assert.strictEqual(isImportFailed('in_progress'), false);
		assert.strictEqual(isImportFailed('pending'), false);
	});
	it('resets only failed rows, preserves rest', () => {
		const rows: ImportRowState[] = [
			rs({ snapshot: snapRow({ name: 'a' }), status: 'applied' }),
			rs({ snapshot: snapRow({ name: 'b' }), status: 'error', error: 'boom' }),
			rs({ snapshot: snapRow({ name: 'c' }), status: 'conflict' }),
		];
		const next = resetFailedImportRowsForRetry(rows);
		assert.strictEqual(next[0].status, 'applied'); // unchanged
		assert.strictEqual(next[1].status, 'idle');    // reset
		assert.strictEqual(next[1].error, undefined);  // error cleared
		assert.strictEqual(next[2].status, 'idle');    // reset
	});
	it('returns same instance when nothing to reset', () => {
		const rows: ImportRowState[] = [
			rs({ snapshot: snapRow({ name: 'a' }), status: 'applied' }),
		];
		assert.strictEqual(resetFailedImportRowsForRetry(rows), rows);
	});
});

describe('import-claude-state — initImportRowsFromSnapshot', () => {
	it('seeds defaults: checked=false, action=copy, conflict=skip', () => {
		const snap: ImportSnapshot = {
			source: 'cc_global',
			path: '/.claude.json',
			exists: true,
			rows: [snapRow({ name: 'a' }), snapRow({ name: 'b' })],
		};
		const rows = initImportRowsFromSnapshot(snap);
		assert.strictEqual(rows.length, 2);
		assert.ok(rows.every((r) => r.checked === false));
		assert.ok(rows.every((r) => r.action === 'copy'));
		assert.ok(rows.every((r) => r.conflict === 'skip'));
		assert.ok(rows.every((r) => r.destName === ''));
		assert.ok(rows.every((r) => r.status === 'idle'));
	});
});

describe('import-claude-state — buildImportPreview', () => {
	it('returns one entry per checked row with computed destName fallback', () => {
		const rows: ImportRowState[] = [
			rs({ snapshot: snapRow({ name: 'a' }), checked: true }),
			rs({ snapshot: snapRow({ name: 'b' }), checked: true, destName: 'b-renamed' }),
			rs({ snapshot: snapRow({ name: 'c' }), checked: false }),
		];
		const preview = buildImportPreview(rows);
		assert.strictEqual(preview.length, 2);
		assert.strictEqual(preview[0].destName, 'a');
		assert.strictEqual(preview[1].destName, 'b-renamed');
	});
	it('drift_fields=undefined yields preview entry with empty array (review finding LOW-3)', () => {
		const r = rs({
			snapshot: snapRow({ name: 'a' }), // drift_fields omitted
			checked: true,
		});
		// Defensive: assert the snapshot truly has no drift_fields field.
		assert.strictEqual(r.snapshot.drift_fields, undefined);
		const preview = buildImportPreview([r]);
		assert.strictEqual(preview.length, 1);
		assert.deepStrictEqual(preview[0].driftFields, []);
	});
});
