import './mock-vscode';
import { strict as assert } from 'node:assert';
import { describe, it } from 'mocha';
import { SapSystemItem, SapComponentItem } from '../sap-item';
import type { SapSystem } from '../sap-detector';
import type { ServerView } from '../types';
import { MockMarkdownString } from './mock-vscode';

function makeVsp(status: ServerView['status'] = 'running', overrides: Partial<ServerView> = {}): ServerView {
	return {
		name: 'vsp-DEV',
		status,
		transport: 'stdio',
		restart_count: 0,
		...overrides,
	};
}

function makeGui(status: ServerView['status'] = 'running', overrides: Partial<ServerView> = {}): ServerView {
	return {
		name: 'sap-gui-DEV',
		status,
		transport: 'http',
		restart_count: 0,
		...overrides,
	};
}

function makeSystem(overrides: Partial<SapSystem> = {}): SapSystem {
	return {
		key: 'DEV',
		sid: 'DEV',
		status: 'running',
		vsp: makeVsp(),
		gui: makeGui(),
		...overrides,
	};
}

describe('SapSystemItem (flat mode)', () => {
	it('uses sap-<status> contextValue by default', () => {
		const item = new SapSystemItem(makeSystem());
		assert.strictEqual(item.contextValue, 'sap-running');
		assert.strictEqual(item.collapsibleState, 0); // None
		assert.strictEqual(item.hierarchical, false);
	});

	it('reflects composite status in contextValue', () => {
		const item = new SapSystemItem(makeSystem({ status: 'degraded' }));
		assert.strictEqual(item.contextValue, 'sap-degraded');
	});

	it('builds a MarkdownString tooltip with VSP + GUI breakdown', () => {
		const item = new SapSystemItem(makeSystem({
			client: '100',
			vsp: makeVsp('running', { pid: 1234, restart_count: 2 }),
			gui: makeGui('stopped', { last_error: 'timeout' }),
		}));
		assert.ok(item.tooltip instanceof MockMarkdownString);
		const md = item.tooltip as unknown as MockMarkdownString;
		assert.strictEqual(md.isTrusted, false);
		assert.strictEqual(md.supportHtml, false);
		assert.ok(md.value.includes('**SAP System:** DEV'));
		assert.ok(md.value.includes('Client: `100`'));
		// escapeMd escapes the hyphen in server names (CommonMark/GFM special).
		assert.ok(md.value.includes('VSP: `vsp\\-DEV`'));
		assert.ok(md.value.includes('PID: `1234`'));
		assert.ok(md.value.includes('Restarts: 2'));
		assert.ok(md.value.includes('GUI: `sap\\-gui\\-DEV`'));
		assert.ok(md.value.includes('timeout'));
	});
});

describe('SapSystemItem (hierarchical mode)', () => {
	it('uses sap-group-<status> contextValue and is collapsible', () => {
		const item = new SapSystemItem(makeSystem(), true);
		assert.strictEqual(item.contextValue, 'sap-group-running');
		assert.strictEqual(item.collapsibleState, 1); // Collapsed
		assert.strictEqual(item.hierarchical, true);
	});

	it('carries the same tooltip whether hierarchical or not', () => {
		const flat = new SapSystemItem(makeSystem());
		const group = new SapSystemItem(makeSystem(), true);
		const a = (flat.tooltip as unknown as MockMarkdownString).value;
		const b = (group.tooltip as unknown as MockMarkdownString).value;
		assert.strictEqual(a, b);
	});
});

describe('SapSystemItem (Phase 17.5 — imported KeePass row)', () => {
	function importedSystem(overrides: Partial<SapSystem> = {}): SapSystem {
		return {
			key: 'DEV-001',
			sid: 'DEV',
			client: '001',
			status: 'stopped',
			imported: true,
			...overrides,
		};
	}

	it('uses sap-imported contextValue regardless of hierarchical mode', () => {
		const flat = new SapSystemItem(importedSystem());
		const hier = new SapSystemItem(importedSystem(), true);
		assert.strictEqual(flat.contextValue, 'sap-imported');
		assert.strictEqual(hier.contextValue, 'sap-imported');
	});

	it('imported rows are never collapsible (no daemon-backed children)', () => {
		const hier = new SapSystemItem(importedSystem(), true);
		assert.strictEqual(hier.collapsibleState, 0); // None — not Collapsed
	});

	it('description signals imported source', () => {
		const item = new SapSystemItem(importedSystem());
		assert.ok((item.description as string).includes('imported'));
	});

	it('tooltip explains imported state and points to Add SAP System', () => {
		const item = new SapSystemItem(importedSystem());
		const tip = (item.tooltip as unknown as MockMarkdownString).value;
		assert.ok(tip.includes('KeePass'), `expected 'KeePass' in tooltip, got: ${tip}`);
		assert.ok(tip.includes('not running'));
		assert.ok(tip.includes('Add SAP System'));
	});

	it('imported icon differs from status icon', () => {
		const imported = new SapSystemItem(importedSystem());
		const normal = new SapSystemItem({
			key: 'DEV-001', sid: 'DEV', client: '001', status: 'stopped',
		});
		const importedIcon = (imported.iconPath as { id: string }).id;
		const normalIcon = (normal.iconPath as { id: string }).id;
		assert.notStrictEqual(importedIcon, normalIcon);
		assert.strictEqual(importedIcon, 'cloud-download');
	});
});

describe('SapComponentItem', () => {
	const system = makeSystem();

	it('VSP component has sap-vsp-<status> contextValue', () => {
		const item = new SapComponentItem(system, 'vsp', makeVsp('running'));
		assert.strictEqual(item.contextValue, 'sap-vsp-running');
		assert.strictEqual(item.kind, 'vsp');
		assert.strictEqual(item.server.name, 'vsp-DEV');
		assert.strictEqual(item.collapsibleState, 0); // None — no grandchildren
	});

	it('GUI component has sap-gui-<status> contextValue', () => {
		const item = new SapComponentItem(system, 'gui', makeGui('error'));
		assert.strictEqual(item.contextValue, 'sap-gui-error');
		assert.strictEqual(item.kind, 'gui');
	});

	it('label is the uppercase component kind', () => {
		const vsp = new SapComponentItem(system, 'vsp', makeVsp());
		const gui = new SapComponentItem(system, 'gui', makeGui());
		assert.strictEqual(vsp.label, 'VSP');
		assert.strictEqual(gui.label, 'GUI');
	});

	it('description is the component status', () => {
		const item = new SapComponentItem(system, 'vsp', makeVsp('degraded'));
		assert.strictEqual(item.description, 'degraded');
	});

	it('tooltip is a MarkdownString with component details', () => {
		const item = new SapComponentItem(
			system,
			'vsp',
			makeVsp('running', { pid: 4321, restart_count: 1, last_error: 'transient failure' }),
		);
		assert.ok(item.tooltip instanceof MockMarkdownString);
		const md = item.tooltip as unknown as MockMarkdownString;
		assert.strictEqual(md.isTrusted, false);
		assert.strictEqual(md.supportHtml, false);
		assert.ok(md.value.includes('**VSP:**'));
		// escapeMd escapes the hyphen in server names.
		assert.ok(md.value.includes('`vsp\\-DEV`'));
		assert.ok(md.value.includes('Status: running'));
		assert.ok(md.value.includes('PID: `4321`'));
		assert.ok(md.value.includes('Restarts: 1'));
		assert.ok(md.value.includes('transient failure'));
	});

	it('tooltip omits empty pid/restart_count/last_error sections', () => {
		const item = new SapComponentItem(system, 'gui', makeGui('running'));
		const md = item.tooltip as unknown as MockMarkdownString;
		assert.ok(!md.value.includes('PID'));
		assert.ok(!md.value.includes('Restarts'));
		assert.ok(!md.value.includes('Error'));
	});
});
