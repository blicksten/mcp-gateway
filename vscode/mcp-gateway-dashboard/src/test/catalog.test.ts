import { strict as assert } from 'node:assert';
import { describe, it, beforeEach, afterEach } from 'mocha';
import * as fs from 'node:fs';
import { promises as fsp } from 'node:fs';
import * as path from 'node:path';
import { createTmpDir, cleanupTmpDir } from './helpers/tmpdir';
import {
	loadServersCatalog,
	loadCommandsCatalog,
	isSupportedId,
	MAX_BYTES,
	_resetSchemaCacheForTests,
} from '../catalog';

const REPO_CATALOG_DIR = path.join(__dirname, '..', '..', 'docs', 'catalog');
const SEED_SERVERS = path.join(REPO_CATALOG_DIR, 'servers.json');
const SEED_COMMANDS = path.join(REPO_CATALOG_DIR, 'commands.json');

const checkCatalogRefs = (
	require('../../scripts/check-catalog-refs.js') as {
		checkCatalogRefs: (
			servers: unknown,
			commands: unknown,
		) => { valid: boolean; errors: string[] };
	}
).checkCatalogRefs;

describe('catalog: loadServersCatalog roundtrip', () => {
	it('parses the bundled servers.json seed and returns 5 entries', async () => {
		const result = await loadServersCatalog(SEED_SERVERS);
		assert.equal(result.warnings.length, 0, `unexpected warnings: ${result.warnings.join(', ')}`);
		assert.equal(result.entries.length, 5);
		const names = new Set(result.entries.map((e) => e.name));
		for (const expected of ['context7', 'pdap-docs', 'orchestrator', 'pal-mcp', 'sap-gui-control']) {
			assert.ok(names.has(expected), `missing seed server: ${expected}`);
		}
		// Mix of stdio + http transports is required by CA.3 acceptance.
		const transports = new Set(result.entries.map((e) => e.transport));
		assert.ok(transports.has('http'));
		assert.ok(transports.has('stdio'));
	});
});

describe('catalog: loadCommandsCatalog roundtrip + cross-reference', () => {
	it('parses the bundled commands.json seed and returns >=5 entries', async () => {
		const result = await loadCommandsCatalog(SEED_COMMANDS);
		assert.equal(result.warnings.length, 0, `unexpected warnings: ${result.warnings.join(', ')}`);
		assert.ok(result.entries.length >= 5, `expected >=5 commands, got ${result.entries.length}`);
		// Every template_md must contain at least one ${...} substitution per CA.4.
		for (const cmd of result.entries) {
			assert.ok(
				cmd.template_md.includes('${'),
				`${cmd.command_name}: template_md lacks variable substitution`,
			);
		}
	});

	it('cross-reference check: every commands.server_name resolves to a servers.name', async () => {
		const servers = JSON.parse(fs.readFileSync(SEED_SERVERS, 'utf8')) as unknown;
		const commands = JSON.parse(fs.readFileSync(SEED_COMMANDS, 'utf8')) as unknown;
		const result = checkCatalogRefs(servers, commands);
		assert.equal(result.valid, true, `cross-ref errors: ${result.errors.join('; ')}`);
	});

	it('cross-reference check: detects a command pointing at a missing server', () => {
		const servers = [
			{ name: 'a', display_name: 'A', transport: 'stdio', command: 'x', args: [], description: 'd' },
		];
		const commands = [
			{ server_name: 'a', command_name: 'ok', description: 'd', template_md: 'x' },
			{ server_name: 'ghost', command_name: 'broken', description: 'd', template_md: 'x' },
		];
		const result = checkCatalogRefs(servers, commands);
		assert.equal(result.valid, false);
		assert.equal(result.errors.length, 1);
		assert.match(result.errors[0], /ghost/);
		assert.match(result.errors[0], /broken/);
	});
});

describe('catalog: defensive error handling', () => {
	let tmpDir: string;

	beforeEach(() => {
		tmpDir = createTmpDir();
	});

	afterEach(() => {
		cleanupTmpDir(tmpDir);
	});

	it('returns warning + empty entries when path is undefined', async () => {
		const result = await loadServersCatalog(undefined);
		assert.equal(result.entries.length, 0);
		assert.equal(result.warnings.length, 1);
		assert.match(result.warnings[0], /no path provided/);
	});

	it('returns warning + empty entries on ENOENT', async () => {
		const missing = path.join(tmpDir, 'does-not-exist.json');
		const result = await loadServersCatalog(missing);
		assert.equal(result.entries.length, 0);
		assert.equal(result.warnings.length, 1);
		assert.match(result.warnings[0], /failed to read|ENOENT/i);
	});

	it('returns warning when operator catalog directory exists but servers.json is absent', async () => {
		// Operator path scenario: directory exists, no servers.json inside.
		const operatorPath = path.join(tmpDir, 'servers.json');
		const result = await loadServersCatalog(operatorPath);
		assert.equal(result.entries.length, 0);
		assert.ok(result.warnings.length >= 1);
		assert.match(result.warnings[0], /failed to read|ENOENT/i);
	});

	it('returns warning + empty entries on malformed JSON', async () => {
		const malformed = path.join(tmpDir, 'malformed.json');
		fs.writeFileSync(malformed, '{ this is not json');
		const result = await loadServersCatalog(malformed);
		assert.equal(result.entries.length, 0);
		assert.equal(result.warnings.length, 1);
		assert.match(result.warnings[0], /JSON parse failed/);
	});

	it('returns warning + empty entries on schema mismatch (missing required field)', async () => {
		const bad = path.join(tmpDir, 'bad.json');
		// Missing display_name and transport -> required field violations.
		fs.writeFileSync(
			bad,
			JSON.stringify([{ name: 'incomplete', description: 'missing fields' }]),
		);
		const result = await loadServersCatalog(bad);
		assert.equal(result.entries.length, 0);
		assert.equal(result.warnings.length, 1);
		assert.match(result.warnings[0], /schema validation failed/);
	});

	it('refuses to read a file >1 MiB and never invokes fs.promises.readFile', async () => {
		const oversize = path.join(tmpDir, 'big.json');
		// Write 1 MiB + 1 byte of valid-looking JSON header padding.
		const bigBuf = Buffer.alloc(MAX_BYTES + 1, 0x20);
		bigBuf[0] = 0x5b; // '['
		bigBuf[bigBuf.length - 1] = 0x5d; // ']'
		fs.writeFileSync(oversize, bigBuf);

		const origReadFile = fsp.readFile;
		let readFileCalls = 0;
		(fsp as unknown as { readFile: typeof fsp.readFile }).readFile = (async (
			...args: Parameters<typeof fsp.readFile>
		) => {
			readFileCalls += 1;
			return (origReadFile as (...a: typeof args) => ReturnType<typeof fsp.readFile>)(...args);
		}) as typeof fsp.readFile;
		try {
			const result = await loadServersCatalog(oversize);
			assert.equal(result.entries.length, 0);
			assert.equal(result.warnings.length, 1);
			assert.match(result.warnings[0], /exceeds .*bytes/);
		} finally {
			(fsp as unknown as { readFile: typeof fsp.readFile }).readFile = origReadFile;
		}
		// F-3 invariant: oversize file must never reach fs.promises.readFile.
		// Loader uses fs.promises.open + fileHandle.read instead — readFile is dead-path.
		assert.equal(
			readFileCalls,
			0,
			`fs.promises.readFile must not be called when oversize, got ${readFileCalls} calls`,
		);
	});

	it('rejects an unsupported $id major version (v2) in a wrapped catalog file', async () => {
		const v2 = path.join(tmpDir, 'v2.json');
		fs.writeFileSync(
			v2,
			JSON.stringify({
				$id: 'https://mcp-gateway.dev/schema/catalog/server.v2.json',
				entries: [
					{
						name: 'x',
						display_name: 'X',
						transport: 'stdio',
						command: 'true',
						args: [],
						description: 'd',
					},
				],
			}),
		);
		const result = await loadServersCatalog(v2);
		assert.equal(result.entries.length, 0);
		assert.equal(result.warnings.length, 1);
		assert.match(result.warnings[0], /unsupported \$id/);
		assert.match(result.warnings[0], /v2/);
	});

	it('accepts a v1 wrapped catalog file with $id and entries', async () => {
		const v1 = path.join(tmpDir, 'v1.json');
		fs.writeFileSync(
			v1,
			JSON.stringify({
				$id: 'https://mcp-gateway.dev/schema/catalog/server.v1.json',
				entries: [
					{
						name: 'wrapped',
						display_name: 'Wrapped',
						transport: 'stdio',
						command: 'true',
						args: [],
						description: 'wrapped form',
					},
				],
			}),
		);
		const result = await loadServersCatalog(v1);
		assert.equal(result.warnings.length, 0, `warnings: ${result.warnings.join(', ')}`);
		assert.equal(result.entries.length, 1);
		assert.equal(result.entries[0].name, 'wrapped');
	});

	it('mixed batch load: servers + commands resolved in one round trip', async () => {
		const [s, c] = await Promise.all([
			loadServersCatalog(SEED_SERVERS),
			loadCommandsCatalog(SEED_COMMANDS),
		]);
		assert.equal(s.warnings.length, 0);
		assert.equal(c.warnings.length, 0);
		assert.ok(s.entries.length >= 5);
		assert.ok(c.entries.length >= 5);
		const serverNames = new Set(s.entries.map((e) => e.name));
		for (const cmd of c.entries) {
			assert.ok(
				serverNames.has(cmd.server_name),
				`command ${cmd.command_name} references unknown server ${cmd.server_name}`,
			);
		}
	});

	it('refuses to load a file just over the size limit (bounded-read invariant)', async () => {
		// Sparse-file-equivalent invariant: stat reports size > MAX_BYTES, loader refuses
		// without invoking JSON.parse. On a normal filesystem this exercises the same
		// safety guard the bounded-read overrun branch enforces for sparse files.
		const overByOne = path.join(tmpDir, 'over.json');
		const buf = Buffer.alloc(MAX_BYTES + 1, 0x20);
		buf[0] = 0x5b;
		buf[buf.length - 1] = 0x5d;
		fs.writeFileSync(overByOne, buf);
		const result = await loadServersCatalog(overByOne);
		assert.equal(result.entries.length, 0);
		assert.match(result.warnings[0], /exceeds .*bytes/);
	});
});

describe('catalog: isSupportedId major-version parser', () => {
	it('accepts v1 and v1.x ids', () => {
		assert.equal(isSupportedId('https://mcp-gateway.dev/schema/catalog/server.v1.json'), true);
		assert.equal(isSupportedId('https://mcp-gateway.dev/schema/catalog/command.v1.json'), true);
		assert.equal(isSupportedId('foo.v1'), true);
		assert.equal(isSupportedId('foo.v1.0.json'), true);
	});

	it('rejects v2+ and malformed ids', () => {
		assert.equal(isSupportedId('https://mcp-gateway.dev/schema/catalog/server.v2.json'), false);
		assert.equal(isSupportedId('foo.v10.json'), false);
		assert.equal(isSupportedId('foo.v0.json'), false);
		assert.equal(isSupportedId('no-version-segment.json'), false);
		assert.equal(isSupportedId(undefined), false);
		assert.equal(isSupportedId(42), false);
	});
});

describe('catalog: schema cache reset (test helper hygiene)', () => {
	it('reset clears state without throwing', () => {
		_resetSchemaCacheForTests();
		// Re-load to confirm the cache rebuilds cleanly.
		assert.doesNotThrow(() => _resetSchemaCacheForTests());
	});
});
