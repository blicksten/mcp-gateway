// Cross-language parity test for the SAP server-name grammar.
//
// Reads the same testdata/sap-name-fixtures.json that the Go test in
// internal/sapname/grammar_gen_test.go reads. New fixture cases added
// to the JSON flow into both tests automatically — no code change
// required. Plan reference: docs/PLAN-sap-picker-and-import-mcp.md
// task T-A.2 (R-21 codegen pipeline closing X1 grammar drift).

import './mock-vscode';
import { strict as assert } from 'node:assert';
import { readFileSync } from 'node:fs';
import * as path from 'node:path';

import {
	parseServerName,
	isVSP,
	isSAPGUI,
	isSAP,
	type SapKind,
} from '../sap-name-grammar.gen';

interface FixtureExpect {
	kind: SapKind;
	sid: string;
	client: string;
}

interface FixtureCase {
	name: string;
	expected: FixtureExpect | null;
	$reason?: string;
}

interface FixtureFile {
	version: number;
	cases: FixtureCase[];
}

function loadFixtures(): FixtureFile {
	// __dirname at runtime is .../vscode/mcp-gateway-dashboard/out/test
	// after the TS compile, but mocha uses ts-node so __dirname points at
	// the source directory: .../vscode/mcp-gateway-dashboard/src/test.
	// Repo root = three levels up.
	const repoRoot = path.resolve(__dirname, '..', '..', '..', '..');
	const fixturePath = path.join(repoRoot, 'testdata', 'sap-name-fixtures.json');
	const raw = readFileSync(fixturePath, 'utf-8');
	const parsed = JSON.parse(raw) as FixtureFile;
	if (parsed.version !== 1) {
		throw new Error(`unsupported fixtures version ${parsed.version} (this test understands 1)`);
	}
	if (parsed.cases.length < 40) {
		throw new Error(`fixtures too small: got ${parsed.cases.length}, plan T-A.2 requires ≥40`);
	}
	return parsed;
}

describe('sap-name-grammar.gen — fixture parity (Go + TS share testdata/sap-name-fixtures.json)', () => {
	const fixtures = loadFixtures();
	for (const c of fixtures.cases) {
		const label = c.name === '' ? '<empty>' : c.name;
		it(`parses ${JSON.stringify(label)} — ${c.expected === null ? 'reject' : 'accept'}`, () => {
			const got = parseServerName(c.name);
			if (c.expected === null) {
				assert.equal(got, null, `expected reject (${c.$reason ?? ''})`);
				return;
			}
			assert.notEqual(got, null, `expected accept (${c.$reason ?? ''})`);
			// `got` is non-null by the assert above.
			assert.deepEqual(got, c.expected, c.$reason ?? '');
		});
	}
});

describe('sap-name-grammar.gen — kind helpers', () => {
	const cases: Array<{ name: string; isVSP: boolean; isGUI: boolean; isAny: boolean }> = [
		{ name: 'vsp-DEV', isVSP: true, isGUI: false, isAny: true },
		{ name: 'vsp-DEV-100', isVSP: true, isGUI: false, isAny: true },
		{ name: 'sap-gui-DEV', isVSP: false, isGUI: true, isAny: true },
		{ name: 'sap-gui-DEV-100', isVSP: false, isGUI: true, isAny: true },
		{ name: 'my-server', isVSP: false, isGUI: false, isAny: false },
		{ name: 'vsp-dev', isVSP: false, isGUI: false, isAny: false }, // lowercase rejected
		{ name: '', isVSP: false, isGUI: false, isAny: false },
	];
	for (const c of cases) {
		const label = c.name === '' ? '<empty>' : c.name;
		it(`isVSP/isSAPGUI/isSAP for ${JSON.stringify(label)}`, () => {
			assert.equal(isVSP(c.name), c.isVSP, 'isVSP');
			assert.equal(isSAPGUI(c.name), c.isGUI, 'isSAPGUI');
			assert.equal(isSAP(c.name), c.isAny, 'isSAP');
		});
	}
});
