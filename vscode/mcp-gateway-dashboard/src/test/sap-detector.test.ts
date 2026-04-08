import './mock-vscode';
import { strict as assert } from 'node:assert';
import { parseSapServerName, groupSapSystems, computeSapStatus, type SapSystem } from '../sap-detector';
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
