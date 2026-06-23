import { strict as assert } from 'node:assert';
import { describe, it } from 'mocha';
import {
	type PickerSnapshot,
	type PickerSnapshotRow,
	type RowState,
	rowKey,
	serverName,
	expandKey,
	categorizeRow,
	degenerateGuard,
	applyFilter,
	buildOpsList,
	buildOpsListWithDefaults,
	buildCloudVspArgs,
	guiServerExeFromProject,
	type CloudParams,
	initRowsFromSnapshot,
	transitionRow,
	isFailed,
	isTerminal,
	setExpand,
	pruneExpandForKpMissing,
	resetFailedRowsForRetry,
	runWithConcurrency,
	DEFAULT_FILTER,
	type BatchOp,
} from '../sap-picker-state';

function snapRow(over: Partial<PickerSnapshotRow>): PickerSnapshotRow {
	return {
		sid: 'DEV',
		client: '100',
		user: 'TEST',
		kpMissing: false,
		registered: { vsp: false, gui: false },
		status: { vsp: '', gui: '' },
		...over,
	};
}

function rs(over: Partial<RowState> & { snapshot: PickerSnapshotRow }): RowState {
	return {
		key: rowKey(over.snapshot),
		desired: { vsp: over.snapshot.registered.vsp, gui: over.snapshot.registered.gui },
		vspStatus: 'idle',
		guiStatus: 'idle',
		override: {},
		...over,
	};
}

describe('sap-picker-state — keys + names', () => {
	it('rowKey collapses bare SID when no client + no user', () => {
		// snapRow defaults user='TEST', so the bare-SID + bare-user case must
		// override both to '' — otherwise the "no user" claim is not exercised.
		assert.strictEqual(rowKey(snapRow({ sid: 'DEV', client: '', user: '' })), 'DEV');
		assert.strictEqual(rowKey(snapRow({ sid: 'DEV', client: '100', user: '' })), 'DEV-100');
	});

	it('rowKey includes user so multi-login SIDs do not collapse (2026-05-27)', () => {
		// Real-world case: KP has two entries for SID Q26 — users
		// "naumov" and "naumov1". Without user in the key both rows
		// would collapse to "Q26-800" and the second row would be lost.
		assert.strictEqual(
			rowKey(snapRow({ sid: 'Q26', client: '800', user: 'naumov' })),
			'Q26-800-naumov',
		);
		assert.strictEqual(
			rowKey(snapRow({ sid: 'Q26', client: '800', user: 'naumov1' })),
			'Q26-800-naumov1',
		);
		// Distinct users → distinct rowKeys.
		assert.notStrictEqual(
			rowKey(snapRow({ sid: 'Q26', client: '800', user: 'naumov' })),
			rowKey(snapRow({ sid: 'Q26', client: '800', user: 'naumov1' })),
		);
		// No client + user → empty middle field is intentional.
		assert.strictEqual(
			rowKey(snapRow({ sid: 'DEV', client: '', user: 'naumov' })),
			'DEV--naumov',
		);
	});

	it('serverName composes vsp and sap-gui prefixes', () => {
		assert.strictEqual(serverName('vsp', 'DEV', '100'), 'vsp-DEV-100');
		assert.strictEqual(serverName('gui', 'DEV', '100'), 'sap-gui-DEV-100');
		assert.strictEqual(serverName('vsp', 'DEV', ''), 'vsp-DEV');
		assert.strictEqual(serverName('gui', 'DEV', ''), 'sap-gui-DEV');
	});

	it('expandKey produces stable per-component keys (R-18)', () => {
		assert.strictEqual(expandKey('DEV', '100', 'vsp'), 'DEV-100-vsp');
		assert.strictEqual(expandKey('DEV', '100', 'gui'), 'DEV-100-gui');
		assert.notStrictEqual(expandKey('DEV', '100', 'vsp'), expandKey('DEV', '100', 'gui'));
	});
});

describe('sap-picker-state — categorization', () => {
	it('kpMissing wins over registered', () => {
		assert.strictEqual(categorizeRow(snapRow({ kpMissing: true, registered: { vsp: true, gui: true } })), 'no-credentials');
	});
	it('any registered component => registered', () => {
		assert.strictEqual(categorizeRow(snapRow({ registered: { vsp: true, gui: false } })), 'registered');
		assert.strictEqual(categorizeRow(snapRow({ registered: { vsp: false, gui: true } })), 'registered');
	});
	it('default available', () => {
		assert.strictEqual(categorizeRow(snapRow({})), 'available');
	});
});

describe('sap-picker-state — degenerateGuard (R-13)', () => {
	it('all-off restores available + noCredentials and flags restored', () => {
		const g = degenerateGuard({ registered: false, available: false, noCredentials: false });
		assert.strictEqual(g.restored, true);
		assert.deepStrictEqual(g.filter, { registered: false, available: true, noCredentials: true });
	});
	it('default filter passes through unchanged', () => {
		const g = degenerateGuard(DEFAULT_FILTER);
		assert.strictEqual(g.restored, false);
		assert.strictEqual(g.filter, DEFAULT_FILTER); // identity preserved
	});
	it('two-off, one-on passes through', () => {
		const g = degenerateGuard({ registered: false, available: true, noCredentials: false });
		assert.strictEqual(g.restored, false);
	});
});

describe('sap-picker-state — applyFilter', () => {
	const rows: RowState[] = [
		rs({ snapshot: snapRow({ sid: 'DEV', client: '100', user: 'ALICE', registered: { vsp: true, gui: false } }) }),
		rs({ snapshot: snapRow({ sid: 'QAS', client: '200', user: 'BOB', kpMissing: true }) }),
		rs({ snapshot: snapRow({ sid: 'PRD', client: '300', user: 'CAROL' }) }),
	];

	it('all-on filter + empty search returns everything', () => {
		const got = applyFilter(rows, DEFAULT_FILTER, '');
		assert.strictEqual(got.length, 3);
	});
	it('hides registered when toggle off', () => {
		const got = applyFilter(rows, { registered: false, available: true, noCredentials: true }, '');
		assert.strictEqual(got.length, 2);
		assert.ok(got.every((r) => r.snapshot.sid !== 'DEV'));
	});
	it('hides no-credentials when toggle off', () => {
		const got = applyFilter(rows, { registered: true, available: true, noCredentials: false }, '');
		assert.strictEqual(got.length, 2);
		assert.ok(got.every((r) => r.snapshot.sid !== 'QAS'));
	});
	it('substring match is case-insensitive on SID', () => {
		assert.strictEqual(applyFilter(rows, DEFAULT_FILTER, 'dev')[0].snapshot.sid, 'DEV');
		assert.strictEqual(applyFilter(rows, DEFAULT_FILTER, 'PRD')[0].snapshot.sid, 'PRD');
	});
	it('substring match works on Client and User', () => {
		assert.strictEqual(applyFilter(rows, DEFAULT_FILTER, '300')[0].snapshot.sid, 'PRD');
		assert.strictEqual(applyFilter(rows, DEFAULT_FILTER, 'bob')[0].snapshot.sid, 'QAS');
	});
	it('empty result when no match', () => {
		assert.deepStrictEqual(applyFilter(rows, DEFAULT_FILTER, 'XYZZY'), []);
	});
});

describe('sap-picker-state — buildOpsList (T-B.2 acceptance + R-30)', () => {
	it('skips kpMissing rows even if user toggles checkboxes (DOM tamper guard)', () => {
		const rows = [
			rs({ snapshot: snapRow({ sid: 'NOC', kpMissing: true }), desired: { vsp: true, gui: true } }),
		];
		assert.deepStrictEqual(buildOpsList(rows), []);
	});

	it('emits add op for newly checked component when override.vspCommand present', () => {
		const rows = [
			rs({
				snapshot: snapRow({ sid: 'DEV', client: '100', registered: { vsp: false, gui: false } }),
				desired: { vsp: true, gui: false },
				override: { vspCommand: '/opt/vsp' },
			}),
		];
		const ops = buildOpsList(rows);
		assert.strictEqual(ops.length, 1);
		assert.strictEqual(ops[0].kind, 'add');
		assert.strictEqual(ops[0].component, 'vsp');
		assert.strictEqual(ops[0].serverName, 'vsp-DEV-100');
		assert.deepStrictEqual(ops[0].config, { command: '/opt/vsp' });
	});

	it('skips add op when override command is missing', () => {
		const rows = [
			rs({
				snapshot: snapRow({ registered: { vsp: false, gui: false } }),
				desired: { vsp: true, gui: false },
				override: {},
			}),
		];
		assert.deepStrictEqual(buildOpsList(rows), []);
	});

	it('emits remove op for unchecked previously-registered component', () => {
		const rows = [
			rs({
				snapshot: snapRow({ sid: 'PRD', client: '', registered: { vsp: true, gui: false } }),
				desired: { vsp: false, gui: false },
			}),
		];
		const ops = buildOpsList(rows);
		assert.strictEqual(ops.length, 1);
		assert.strictEqual(ops[0].kind, 'remove');
		assert.strictEqual(ops[0].serverName, 'vsp-PRD');
	});

	it('emits both add and remove when both components change in opposite directions', () => {
		const rows = [
			rs({
				snapshot: snapRow({ sid: 'QAS', client: '200', registered: { vsp: true, gui: false } }),
				desired: { vsp: false, gui: true },
				override: { guiCommand: '/opt/gui' },
			}),
		];
		const ops = buildOpsList(rows);
		assert.strictEqual(ops.length, 2);
		const remove = ops.find((o) => o.kind === 'remove')!;
		const add = ops.find((o) => o.kind === 'add')!;
		assert.strictEqual(remove.component, 'vsp');
		assert.strictEqual(add.component, 'gui');
	});

	it('no-op when desired matches registered', () => {
		const rows = [
			rs({
				snapshot: snapRow({ registered: { vsp: true, gui: true } }),
				desired: { vsp: true, gui: true },
			}),
		];
		assert.deepStrictEqual(buildOpsList(rows), []);
	});
});

describe('sap-picker-state — initRowsFromSnapshot', () => {
	it('seeds desired = registered, status = idle, override = empty', () => {
		const snap: PickerSnapshot = {
			rows: [snapRow({ sid: 'DEV', client: '100', registered: { vsp: true, gui: false } })],
			warnings: [],
		};
		const rows = initRowsFromSnapshot(snap);
		assert.strictEqual(rows.length, 1);
		assert.deepStrictEqual(rows[0].desired, { vsp: true, gui: false });
		assert.strictEqual(rows[0].vspStatus, 'idle');
		assert.strictEqual(rows[0].guiStatus, 'idle');
		assert.deepStrictEqual(rows[0].override, {});
	});
});

describe('sap-picker-state — lifecycle transitions (R-04, R-09, R-28)', () => {
	const baseRow = rs({ snapshot: snapRow({ sid: 'DEV', client: '100' }) });

	it('queue → pending, start_op → in_progress, add_ok → config_added, add_started → config_added_running', () => {
		let r = baseRow;
		r = transitionRow(r, { kind: 'queue', rowKey: r.key, component: 'vsp' });
		assert.strictEqual(r.vspStatus, 'pending');
		r = transitionRow(r, { kind: 'start_op', rowKey: r.key, component: 'vsp' });
		assert.strictEqual(r.vspStatus, 'in_progress');
		r = transitionRow(r, { kind: 'add_ok', rowKey: r.key, component: 'vsp' });
		assert.strictEqual(r.vspStatus, 'config_added');
		r = transitionRow(r, { kind: 'add_started', rowKey: r.key, component: 'vsp' });
		assert.strictEqual(r.vspStatus, 'config_added_running');
	});

	it('add_start_failed records the error and is reported as failed', () => {
		const r = transitionRow(baseRow, {
			kind: 'add_start_failed', rowKey: baseRow.key, component: 'vsp', error: 'crashloop',
		});
		assert.strictEqual(r.vspStatus, 'config_added_start_failed');
		assert.strictEqual(r.vspError, 'crashloop');
		assert.ok(isFailed(r.vspStatus));
	});

	it('remove_orphan surfaces orphan state for force-kill UI (R-28)', () => {
		const r = transitionRow(baseRow, {
			kind: 'remove_orphan', rowKey: baseRow.key, component: 'gui', error: 'orphan: stop failed',
		});
		assert.strictEqual(r.guiStatus, 'removed_with_orphan');
		assert.strictEqual(r.guiError, 'orphan: stop failed');
		assert.ok(isFailed(r.guiStatus));
	});

	it('event with mismatched rowKey leaves the row untouched', () => {
		const r = transitionRow(baseRow, { kind: 'add_ok', rowKey: 'OTHER', component: 'vsp' });
		assert.strictEqual(r, baseRow);
	});

	it('isTerminal classifies all 6 terminal states', () => {
		for (const s of ['idle', 'config_added_running', 'config_added_start_failed',
			'removed', 'removed_with_orphan', 'removal_failed'] as const) {
			assert.ok(isTerminal(s), `${s} should be terminal`);
		}
		for (const s of ['pending', 'in_progress', 'config_added'] as const) {
			assert.ok(!isTerminal(s), `${s} should NOT be terminal`);
		}
	});
});

describe('sap-picker-state — expand state (R-18)', () => {
	it('setExpand survives filter cycles by living outside the row list', () => {
		// Simulate: expand row A, then filter it out, then clear filter — expand persists
		const rows: RowState[] = [
			rs({ snapshot: snapRow({ sid: 'A', client: '100', registered: { vsp: true, gui: false } }) }),
			rs({ snapshot: snapRow({ sid: 'B', client: '200' }) }),
		];
		let map: Record<string, boolean> = {};
		map = setExpand(map, expandKey('A', '100', 'vsp'), true);
		assert.strictEqual(map[expandKey('A', '100', 'vsp')], true);

		// Apply filter that hides A (registered=false)
		const filtered = applyFilter(rows, { registered: false, available: true, noCredentials: true }, '');
		assert.strictEqual(filtered.length, 1);
		assert.strictEqual(filtered[0].snapshot.sid, 'B');

		// Expand map untouched — confirmed by spike R-18 acceptance
		assert.strictEqual(map[expandKey('A', '100', 'vsp')], true);

		// Clear filter — A reappears, expand still set
		const refiltered = applyFilter(rows, DEFAULT_FILTER, '');
		assert.strictEqual(refiltered.length, 2);
		assert.strictEqual(map[expandKey('A', '100', 'vsp')], true);
	});

	it('setExpand returns same instance when no change is needed', () => {
		const map: Record<string, boolean> = {};
		assert.strictEqual(setExpand(map, 'k', false), map);
		const set = setExpand(map, 'k', true);
		assert.strictEqual(setExpand(set, 'k', true), set);
	});

	it('setExpand removes the entry on collapse (no unbounded growth)', () => {
		let map: Record<string, boolean> = {};
		map = setExpand(map, 'k', true);
		map = setExpand(map, 'k', false);
		assert.deepStrictEqual(map, {});
	});

	it('pruneExpandForKpMissing collapses overrides for kpMissing rows (R-22 / Q7.2)', () => {
		const rows: RowState[] = [
			rs({ snapshot: snapRow({ sid: 'A', client: '100' }) }),
			rs({ snapshot: snapRow({ sid: 'B', client: '200', kpMissing: true }) }),
		];
		let map: Record<string, boolean> = {};
		map = setExpand(map, expandKey('A', '100', 'vsp'), true);
		map = setExpand(map, expandKey('B', '200', 'vsp'), true);
		map = setExpand(map, expandKey('B', '200', 'gui'), true);
		const pruned = pruneExpandForKpMissing(map, rows);
		assert.strictEqual(pruned[expandKey('A', '100', 'vsp')], true);
		assert.strictEqual(pruned[expandKey('B', '200', 'vsp')], undefined);
		assert.strictEqual(pruned[expandKey('B', '200', 'gui')], undefined);
	});
});

describe('sap-picker-state — resetFailedRowsForRetry (R-04 retry)', () => {
	it('clears only failed component statuses; preserves successful ones', () => {
		const rows: RowState[] = [
			{
				...rs({ snapshot: snapRow({ sid: 'A', client: '100' }) }),
				vspStatus: 'config_added_running',
				guiStatus: 'removed_with_orphan',
				guiError: 'orphan',
			},
			{
				...rs({ snapshot: snapRow({ sid: 'B', client: '200' }) }),
				vspStatus: 'removed',
				guiStatus: 'removed',
			},
		];
		const reset = resetFailedRowsForRetry(rows);
		assert.strictEqual(reset[0].vspStatus, 'config_added_running'); // success preserved
		assert.strictEqual(reset[0].guiStatus, 'idle'); // orphan cleared
		assert.strictEqual(reset[0].guiError, undefined);
		assert.strictEqual(reset[1], rows[1]); // unchanged row instance preserved
	});

	it('returns same array instance when no rows are failed', () => {
		const rows: RowState[] = [
			{ ...rs({ snapshot: snapRow({}) }), vspStatus: 'config_added_running' },
		];
		assert.strictEqual(resetFailedRowsForRetry(rows), rows);
	});
});

describe('sap-picker-state — buildOpsList edge cases', () => {
	it('skips kpMissing rows even when desired flips both components and override is provided', () => {
		// Defence-in-depth: even if a tampered DOM somehow ships override values
		// for a kpMissing row, the host-side filter rejects them entirely.
		const rows = [
			rs({
				snapshot: snapRow({ sid: 'XXX', kpMissing: true, registered: { vsp: false, gui: false } }),
				desired: { vsp: true, gui: true },
				override: { vspCommand: '/x', guiCommand: '/y' },
			}),
		];
		assert.deepStrictEqual(buildOpsList(rows), []);
	});
});

describe('sap-picker-state — runWithConcurrency', () => {
	it('respects the concurrency cap and runs every op exactly once', async () => {
		const ops: BatchOp[] = Array.from({ length: 12 }, (_, i) => ({
			kind: 'add', component: 'vsp', rowKey: `R${i}`, serverName: `s-${i}`, config: {},
		}));
		let active = 0;
		let peak = 0;
		const indexes: number[] = [];
		const results = await runWithConcurrency(ops, async (op) => {
			active++;
			peak = Math.max(peak, active);
			await new Promise((r) => setTimeout(r, 5));
			active--;
			indexes.push(parseInt(op.rowKey.slice(1), 10));
			return op.rowKey;
		}, 4);
		assert.strictEqual(results.length, 12);
		assert.strictEqual(new Set(results).size, 12);
		assert.ok(peak <= 4, `peak concurrency ${peak} exceeded cap 4`);
		assert.ok(peak >= 1, 'peak should be at least 1');
	});

	it('clamps cap to ops.length when ops.length < concurrency', async () => {
		const ops: BatchOp[] = [
			{ kind: 'add', component: 'vsp', rowKey: 'A', serverName: 's', config: {} },
		];
		let peak = 0;
		await runWithConcurrency(ops, async () => { peak++; return 1; }, 8);
		assert.strictEqual(peak, 1);
	});

	it('handles empty ops list cleanly', async () => {
		const r = await runWithConcurrency<number>([], async () => 0);
		assert.deepStrictEqual(r, []);
	});
});

// ---------------------------------------------------------------------------
// Cloud SAP support (feature-a16d8b44 module 1).
// A cloud vsp add must emit config.args with the cookie-file launcher tokens;
// an on-prem vsp add must remain byte-identical to before (no `args` key).
// SECURITY: these assertions are on token SHAPE / KEYS only — no secret value
// (cookie contents) is ever present; the cookie file is referenced by PATH.
// ---------------------------------------------------------------------------

describe('sap-picker-state — buildCloudVspArgs (module 1)', () => {
	const fullCloud: CloudParams = {
		sapUrl: 'https://my-tenant.s4hana.cloud',
		cookieFile: '/abs/path/cookies.txt',
		readOnly: true,
		featureRap: true,
	};

	it('emits read-only + feature-rap on + cookie-file (two tokens) in order', () => {
		assert.deepStrictEqual(buildCloudVspArgs(fullCloud), [
			'--read-only',
			'--feature-rap', 'on',
			'--cookie-file', '/abs/path/cookies.txt',
		]);
	});

	it('omits --read-only when readOnly is false', () => {
		assert.deepStrictEqual(
			buildCloudVspArgs({ ...fullCloud, readOnly: false }),
			['--feature-rap', 'on', '--cookie-file', '/abs/path/cookies.txt'],
		);
	});

	it('omits --feature-rap when featureRap is false', () => {
		assert.deepStrictEqual(
			buildCloudVspArgs({ ...fullCloud, featureRap: false }),
			['--read-only', '--cookie-file', '/abs/path/cookies.txt'],
		);
	});

	it('omits BOTH conditional flags when readOnly and featureRap are false', () => {
		// Edge: operator opts out of both per-row toggles — only the
		// cookie-file launcher tokens remain. Order is still deterministic.
		assert.deepStrictEqual(
			buildCloudVspArgs({ ...fullCloud, readOnly: false, featureRap: false }),
			['--cookie-file', '/abs/path/cookies.txt'],
		);
	});

	it('emits BOTH conditional flags when readOnly and featureRap are true', () => {
		// Symmetry guard against the both-false case: read-only first,
		// then feature-rap on, then cookie-file — the canonical full shape.
		assert.deepStrictEqual(
			buildCloudVspArgs({ ...fullCloud, readOnly: true, featureRap: true }),
			['--read-only', '--feature-rap', 'on', '--cookie-file', '/abs/path/cookies.txt'],
		);
	});

	it('keeps --cookie-file + path as two separate tokens', () => {
		const args = buildCloudVspArgs(fullCloud);
		const idx = args.indexOf('--cookie-file');
		assert.ok(idx >= 0, '--cookie-file flag must be present');
		assert.strictEqual(args[idx + 1], '/abs/path/cookies.txt');
	});

	it('keeps a Windows cookie path with spaces as ONE literal token (no splitting)', () => {
		// Real-world: KeePass/temp cookie files often live under a path with
		// spaces ("C:\\Users\\My Name\\AppData\\..."). The launcher must receive
		// the path as a single arg token — never split on the space — so the
		// child process sees the exact file. No quoting is added by this layer:
		// argv passing keeps it literal.
		const winPath = 'C:\\Users\\My Name\\AppData\\Local\\cookies.txt';
		const args = buildCloudVspArgs({ ...fullCloud, cookieFile: winPath });
		const idx = args.indexOf('--cookie-file');
		assert.strictEqual(args.length, idx + 2, 'cookie path must be exactly one trailing token');
		assert.strictEqual(args[idx + 1], winPath, 'path must be passed byte-for-byte, not split on spaces');
	});

	it('keeps a forward-slash Windows cookie path as ONE literal token', () => {
		// Mixed/forward-slash Windows paths (C:/...) are valid and used by some
		// tooling. Same contract: two tokens, path verbatim.
		const fwdPath = 'C:/Users/My Name/AppData/Local/cookies.txt';
		const args = buildCloudVspArgs({ ...fullCloud, cookieFile: fwdPath });
		const idx = args.indexOf('--cookie-file');
		assert.strictEqual(args[idx + 1], fwdPath);
		assert.strictEqual(args.length, idx + 2);
	});

	it('contains no secret-bearing token (cookie referenced by path only)', () => {
		const args = buildCloudVspArgs(fullCloud);
		for (const tok of args) {
			assert.ok(!/password|token|cookie=/i.test(tok),
				`unexpected secret-like token: ${tok}`);
		}
	});
});

describe('sap-picker-state — buildOpsListWithDefaults cloud vs on-prem (module 1)', () => {
	const cloud: CloudParams = {
		sapUrl: 'https://my-tenant.s4hana.cloud',
		cookieFile: '/abs/path/cookies.txt',
		readOnly: true,
		featureRap: true,
	};

	it('cloud vsp add → config.args deep-equals expected token array + command', () => {
		const rows: RowState[] = [
			rs({
				snapshot: snapRow({ sid: 'CLD', client: '100', registered: { vsp: false, gui: false } }),
				desired: { vsp: true, gui: false },
				kind: 'cloud',
				cloud,
			}),
		];
		const { ops } = buildOpsListWithDefaults(rows, { vspCommand: '/opt/vsp' });
		assert.strictEqual(ops.length, 1);
		assert.strictEqual(ops[0].kind, 'add');
		assert.strictEqual(ops[0].component, 'vsp');
		assert.deepStrictEqual(ops[0].config, {
			command: '/opt/vsp',
			args: ['--read-only', '--feature-rap', 'on', '--cookie-file', '/abs/path/cookies.txt'],
		});
	});

	it('on-prem vsp add → config has NO args key (byte-identical regression)', () => {
		// Same row WITHOUT kind/cloud must produce exactly { command } — no args.
		const rows: RowState[] = [
			rs({
				snapshot: snapRow({ sid: 'DEV', client: '100', registered: { vsp: false, gui: false } }),
				desired: { vsp: true, gui: false },
				override: { vspCommand: '/opt/vsp' },
			}),
		];
		const { ops } = buildOpsListWithDefaults(rows, {});
		assert.strictEqual(ops.length, 1);
		// deepStrictEqual against the exact pre-feature shape: just { command }.
		assert.deepStrictEqual(ops[0].config, { command: '/opt/vsp' });
		assert.ok(!('args' in (ops[0].config as Record<string, unknown>)),
			'on-prem config must not carry an args key');
	});

	it('buildOpsList (legacy signature) on-prem vsp add stays { command } only', () => {
		// Regression guard via the legacy path the existing suite already uses.
		const rows = [
			rs({
				snapshot: snapRow({ sid: 'DEV', client: '100', registered: { vsp: false, gui: false } }),
				desired: { vsp: true, gui: false },
				override: { vspCommand: '/opt/vsp' },
			}),
		];
		const ops = buildOpsList(rows);
		assert.strictEqual(ops.length, 1);
		assert.deepStrictEqual(ops[0].config, { command: '/opt/vsp' });
	});

	it('cloud flag without cloud params falls back to on-prem shape (defensive)', () => {
		const rows: RowState[] = [
			rs({
				snapshot: snapRow({ sid: 'CLD', client: '100', registered: { vsp: false, gui: false } }),
				desired: { vsp: true, gui: false },
				override: { vspCommand: '/opt/vsp' },
				kind: 'cloud',
				// cloud intentionally undefined
			}),
		];
		const { ops } = buildOpsListWithDefaults(rows, {});
		assert.strictEqual(ops.length, 1);
		assert.deepStrictEqual(ops[0].config, { command: '/opt/vsp' });
	});
});

describe('sap-picker-state — GUI drop-uv (window-storm fix, spike Part A)', () => {
	it('guiServerExeFromProject builds the venv exe path (Windows backslash project)', () => {
		assert.strictEqual(
			guiServerExeFromProject('C:\\Users\\me\\sap-gui-control'),
			'C:\\Users\\me\\sap-gui-control\\.venv\\Scripts\\sap-gui-server.exe');
	});

	it('guiServerExeFromProject tolerates a trailing separator and posix paths', () => {
		assert.strictEqual(
			guiServerExeFromProject('C:\\Users\\me\\sap-gui-control\\'),
			'C:\\Users\\me\\sap-gui-control\\.venv\\Scripts\\sap-gui-server.exe');
		assert.strictEqual(
			guiServerExeFromProject('/home/me/sap-gui-control/'),
			'/home/me/sap-gui-control/.venv/Scripts/sap-gui-server.exe');
	});

	it('GUI uv-mode add → direct venv exe with empty args (NO uv, NO --project)', () => {
		const rows: RowState[] = [
			rs({
				snapshot: snapRow({ sid: 'DEV', client: '100', registered: { vsp: false, gui: false } }),
				desired: { vsp: false, gui: true },
			}),
		];
		const { ops } = buildOpsListWithDefaults(rows, {
			defaultGuiMode: 'uv',
			guiUvProject: 'C:\\proj\\sap-gui-control',
			// uvPath intentionally omitted — no longer required for GUI in uv mode.
		});
		assert.strictEqual(ops.length, 1);
		assert.strictEqual(ops[0].component, 'gui');
		assert.deepStrictEqual(ops[0].config, {
			command: 'C:\\proj\\sap-gui-control\\.venv\\Scripts\\sap-gui-server.exe',
			args: [],
		});
		const cmd = (ops[0].config as Record<string, unknown>).command as string;
		assert.ok(!cmd.includes('uv'), 'command must not reference uv');
		assert.ok(!(ops[0].config as Record<string, unknown>).args ||
			!((ops[0].config as Record<string, unknown>).args as string[]).includes('--project'),
			'args must not carry --project');
	});

	it('GUI uv-mode add without a project path → skipped with a clear reason', () => {
		const rows: RowState[] = [
			rs({
				snapshot: snapRow({ sid: 'DEV', client: '100', registered: { vsp: false, gui: false } }),
				desired: { vsp: false, gui: true },
			}),
		];
		const { ops, skipped } = buildOpsListWithDefaults(rows, { defaultGuiMode: 'uv' });
		assert.strictEqual(ops.length, 0);
		assert.strictEqual(skipped.length, 1);
		assert.strictEqual(skipped[0].component, 'gui');
		assert.ok(skipped[0].reason.includes('defaultGuiUvProject'));
	});

	it('GUI override command still wins over uv-mode derivation', () => {
		const rows: RowState[] = [
			rs({
				snapshot: snapRow({ sid: 'DEV', client: '100', registered: { vsp: false, gui: false } }),
				desired: { vsp: false, gui: true },
				override: { guiCommand: 'C:\\custom\\my-gui.exe' },
			}),
		];
		const { ops } = buildOpsListWithDefaults(rows, {
			defaultGuiMode: 'uv',
			guiUvProject: 'C:\\proj\\sap-gui-control',
		});
		assert.strictEqual(ops.length, 1);
		assert.deepStrictEqual(ops[0].config, { command: 'C:\\custom\\my-gui.exe' });
	});
});
