import './mock-vscode';
import { strict as assert } from 'node:assert';
import {
	parseSapServerName,
	groupSapSystems,
	computeSapStatus,
	synthesizeKeepassSapSystems,
	type SapSystem,
} from '../sap-detector';
import type { ServerView } from '../types';

describe('parseSapServerName', () => {
	it('parses vsp-DEV', () => {
		const r = parseSapServerName('vsp-DEV');
		assert.deepEqual(r, { sid: 'DEV', client: undefined, component: 'vsp' });
	});

	it('parses vsp-DEV-100', () => {
		const r = parseSapServerName('vsp-DEV-100');
		assert.deepEqual(r, { sid: 'DEV', client: '100', component: 'vsp' });
	});

	it('parses sap-gui-S23-800', () => {
		const r = parseSapServerName('sap-gui-S23-800');
		assert.deepEqual(r, { sid: 'S23', client: '800', component: 'gui' });
	});

	it('parses sap-gui-QAS', () => {
		const r = parseSapServerName('sap-gui-QAS');
		assert.deepEqual(r, { sid: 'QAS', client: undefined, component: 'gui' });
	});

	it('returns null for non-SAP server', () => {
		assert.equal(parseSapServerName('my-server'), null);
	});

	it('returns null for lowercase SID', () => {
		assert.equal(parseSapServerName('vsp-dev'), null);
	});

	it('returns null for 4-char SID', () => {
		assert.equal(parseSapServerName('vsp-ABCD'), null);
	});

	it('returns null for 2-char SID', () => {
		assert.equal(parseSapServerName('vsp-DE'), null);
	});

	it('returns null for 4-digit client', () => {
		assert.equal(parseSapServerName('vsp-DEV-1234'), null);
	});

	it('returns null for sap-gui- with no SID', () => {
		assert.equal(parseSapServerName('sap-gui-'), null);
	});

	it('handles alphanumeric SID', () => {
		const r = parseSapServerName('vsp-D01');
		assert.deepEqual(r, { sid: 'D01', client: undefined, component: 'vsp' });
	});
});

describe('computeSapStatus', () => {
	const mkSystem = (vspStatus?: string, guiStatus?: string): SapSystem => ({
		key: 'DEV', sid: 'DEV',
		vsp: vspStatus ? { name: 'vsp-DEV', status: vspStatus as any, transport: 'stdio', restart_count: 0 } : undefined,
		gui: guiStatus ? { name: 'sap-gui-DEV', status: guiStatus as any, transport: 'http', restart_count: 0 } : undefined,
		status: 'stopped',
	});

	it('both running → running', () => {
		assert.equal(computeSapStatus(mkSystem('running', 'running')), 'running');
	});

	it('VSP running, no GUI → running', () => {
		assert.equal(computeSapStatus(mkSystem('running')), 'running');
	});

	it('VSP running, GUI error → degraded', () => {
		assert.equal(computeSapStatus(mkSystem('running', 'error')), 'degraded');
	});

	it('VSP running, GUI degraded → degraded', () => {
		assert.equal(computeSapStatus(mkSystem('running', 'degraded')), 'degraded');
	});

	it('VSP running, GUI stopped → degraded', () => {
		assert.equal(computeSapStatus(mkSystem('running', 'stopped')), 'degraded');
	});

	it('VSP starting, GUI stopped → starting (boot sequence, not degraded)', () => {
		assert.equal(computeSapStatus(mkSystem('starting', 'stopped')), 'starting');
	});

	it('VSP restarting, GUI stopped → restarting (boot sequence, not degraded)', () => {
		assert.equal(computeSapStatus(mkSystem('restarting', 'stopped')), 'restarting');
	});

	it('VSP error → error', () => {
		assert.equal(computeSapStatus(mkSystem('error', 'running')), 'error');
	});

	it('VSP stopped → stopped', () => {
		assert.equal(computeSapStatus(mkSystem('stopped', 'running')), 'stopped');
	});

	it('VSP disabled → disabled', () => {
		assert.equal(computeSapStatus(mkSystem('disabled')), 'disabled');
	});

	it('no VSP → stopped', () => {
		assert.equal(computeSapStatus(mkSystem(undefined, 'running')), 'stopped');
	});
});

describe('groupSapSystems', () => {
	it('groups mixed list correctly', () => {
		const servers: ServerView[] = [
			{ name: 'my-server', status: 'running', transport: 'stdio', restart_count: 0 },
			{ name: 'vsp-DEV', status: 'running', transport: 'stdio', restart_count: 0 },
			{ name: 'sap-gui-DEV-100', status: 'running', transport: 'http', restart_count: 0 },
			{ name: 'vsp-DEV-100', status: 'running', transport: 'stdio', restart_count: 0 },
		];
		const { sap, mcp } = groupSapSystems(servers);
		assert.equal(mcp.length, 1);
		assert.equal(mcp[0].name, 'my-server');
		// Two SAP systems: DEV (vsp only) and DEV-100 (vsp + gui).
		assert.equal(sap.length, 2);
		const dev = sap.find(s => s.key === 'DEV');
		const dev100 = sap.find(s => s.key === 'DEV-100');
		assert.ok(dev);
		assert.ok(dev100);
		assert.equal(dev!.vsp?.name, 'vsp-DEV');
		assert.equal(dev!.gui, undefined);
		assert.equal(dev100!.vsp?.name, 'vsp-DEV-100');
		assert.equal(dev100!.gui?.name, 'sap-gui-DEV-100');
	});

	it('returns only MCP servers when no SAP', () => {
		const servers: ServerView[] = [
			{ name: 'a', status: 'running', transport: 'stdio', restart_count: 0 },
		];
		const { sap, mcp } = groupSapSystems(servers);
		assert.equal(sap.length, 0);
		assert.equal(mcp.length, 1);
	});

	it('returns only SAP systems when all are SAP', () => {
		const servers: ServerView[] = [
			{ name: 'vsp-QAS', status: 'running', transport: 'stdio', restart_count: 0 },
		];
		const { sap, mcp } = groupSapSystems(servers);
		assert.equal(sap.length, 1);
		assert.equal(mcp.length, 0);
	});

	it('sorts SAP systems by key', () => {
		const servers: ServerView[] = [
			{ name: 'vsp-ZZZ', status: 'running', transport: 'stdio', restart_count: 0 },
			{ name: 'vsp-AAA', status: 'running', transport: 'stdio', restart_count: 0 },
		];
		const { sap } = groupSapSystems(servers);
		assert.equal(sap[0].key, 'AAA');
		assert.equal(sap[1].key, 'ZZZ');
	});
});

describe('synthesizeKeepassSapSystems (Phase 17.5)', () => {
	it('returns empty when no credential name matches the SAP regex', () => {
		const rows = synthesizeKeepassSapSystems(['context7', 'pal', 'other'], new Set());
		assert.deepEqual(rows, []);
	});

	it('returns empty when every matching name is already daemon-backed', () => {
		const rows = synthesizeKeepassSapSystems(
			['vsp-DEV-001', 'sap-gui-DEV-001'],
			new Set(['DEV-001']),
		);
		assert.deepEqual(rows, []);
	});

	it('synthesizes one imported row per SID/client pair with imported=true', () => {
		const rows = synthesizeKeepassSapSystems(['vsp-QAS-100'], new Set());
		assert.equal(rows.length, 1);
		assert.equal(rows[0].key, 'QAS-100');
		assert.equal(rows[0].sid, 'QAS');
		assert.equal(rows[0].client, '100');
		assert.equal(rows[0].status, 'stopped');
		assert.strictEqual(rows[0].imported, true);
		assert.strictEqual(rows[0].vsp, undefined);
		assert.strictEqual(rows[0].gui, undefined);
	});

	it('deduplicates when both vsp-<SID> and sap-gui-<SID> are imported for the same key', () => {
		const rows = synthesizeKeepassSapSystems(
			['vsp-PRD-400', 'sap-gui-PRD-400'],
			new Set(),
		);
		assert.equal(rows.length, 1);
		assert.equal(rows[0].key, 'PRD-400');
	});

	it('sorts output by key for stable ordering', () => {
		const rows = synthesizeKeepassSapSystems(
			['vsp-ZZZ', 'vsp-AAA-100', 'vsp-MMM-200'],
			new Set(),
		);
		assert.deepEqual(
			rows.map((r) => r.key),
			['AAA-100', 'MMM-200', 'ZZZ'],
		);
	});

	it('client-less (no-client) entry is distinct from a clientful entry with same SID', () => {
		const rows = synthesizeKeepassSapSystems(
			['vsp-DEV', 'vsp-DEV-001'],
			new Set(),
		);
		assert.equal(rows.length, 2);
		assert.deepEqual(rows.map((r) => r.key).sort(), ['DEV', 'DEV-001']);
	});

	it('non-matching names co-existing with matching names is fine', () => {
		const rows = synthesizeKeepassSapSystems(
			['context7', 'vsp-ABC', 'pal', 'sap-gui-ABC-900'],
			new Set(),
		);
		assert.equal(rows.length, 2);
		assert.deepEqual(rows.map((r) => r.key).sort(), ['ABC', 'ABC-900']);
	});

	it('readonly inputs — function does not mutate credentialNames or existingKeys', () => {
		const names = Object.freeze(['vsp-DEV']);
		const keys = new Set(['X-999']);
		const before = [...keys];
		synthesizeKeepassSapSystems(names, keys);
		assert.deepEqual([...keys], before);
	});
});
