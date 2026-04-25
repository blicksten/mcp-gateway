// Tests for detection helpers — Phase 1 (closes B-01..B-05, B-13).
//
// Pattern: mocha + node:assert/strict. All external side-effects (spawn,
// readFile) are injected via opts. No real claude binary or filesystem is
// touched.

import { strict as assert } from 'node:assert';
import { EventEmitter } from 'node:events';
import { PassThrough } from 'node:stream';
import { describe, it, beforeEach } from 'mocha';
import { spawn as realSpawn } from 'node:child_process';

import {
	detectPluginInstalled,
	detectPatchInstalled,
	detectChannelStatus,
	_resetDetectionCache,
	type PluginDetectionOpts,
	type PatchDetectionOpts,
} from '../../claude-code/detection';

// --------------------------------------------------------------------------
// Fake child process — mirrors patch-installer.test.ts pattern.
// --------------------------------------------------------------------------

class FakeChild extends EventEmitter {
	readonly stdout = new PassThrough();
	readonly stderr = new PassThrough();

	/** Emit stdout content and close with exit code 0. */
	succeed(stdoutData: string): void {
		setImmediate(() => {
			this.stdout.write(stdoutData);
			this.stdout.end();
			this.stderr.end();
			this.emit('close', 0);
		});
	}

	/** Close with non-zero exit code. */
	fail(code: number = 1): void {
		setImmediate(() => {
			this.stdout.end();
			this.stderr.end();
			this.emit('close', code);
		});
	}

	/** Emit an error event (e.g. ENOENT). */
	error(err: Error): void {
		setImmediate(() => this.emit('error', err));
	}
}

type SpawnFn = PluginDetectionOpts['spawn'];

/**
 * Makes a spawn stub that returns the given FakeChild instance and captures
 * how many times it was called.
 */
function makeSpawnStub(child: FakeChild, calls: { count: number }): SpawnFn {
	return ((_command: string, _args: string[]) => {
		calls.count++;
		return child;
	}) as unknown as typeof realSpawn;
}

// --------------------------------------------------------------------------
// detectPluginInstalled tests
// --------------------------------------------------------------------------

describe('detectPluginInstalled', () => {
	beforeEach(() => {
		_resetDetectionCache();
	});

	it('returns installed=true with version when spawn outputs JSON with mcp-gateway plugin', async () => {
		const child = new FakeChild();
		const calls = { count: 0 };
		const pluginList = JSON.stringify([
			{ name: 'some-other-plugin', version: '0.1.0' },
			{ name: 'mcp-gateway', version: '1.2.3', marketplace: 'mcp-gateway-local' },
		]);
		child.succeed(pluginList);

		const result = await detectPluginInstalled({ spawn: makeSpawnStub(child, calls) });

		assert.strictEqual(result.installed, true);
		assert.strictEqual(result.version, '1.2.3');
		assert.strictEqual(result.marketplace, 'mcp-gateway-local');
		assert.strictEqual(calls.count, 1);
	});

	it('returns installed=false on ENOENT (claude binary missing)', async () => {
		const child = new FakeChild();
		child.error(Object.assign(new Error('spawn ENOENT'), { code: 'ENOENT' }));

		const result = await detectPluginInstalled({ spawn: makeSpawnStub(child, { count: 0 }) });

		assert.strictEqual(result.installed, false);
	});

	it('returns installed=false on non-zero exit code', async () => {
		const child = new FakeChild();
		child.fail(1);

		const result = await detectPluginInstalled({ spawn: makeSpawnStub(child, { count: 0 }) });

		assert.strictEqual(result.installed, false);
	});

	it('returns installed=false on JSON.parse failure', async () => {
		const child = new FakeChild();
		child.succeed('not-valid-json{{{{');

		const result = await detectPluginInstalled({ spawn: makeSpawnStub(child, { count: 0 }) });

		assert.strictEqual(result.installed, false);
	});

	it('returns installed=false when plugin list does not contain mcp-gateway', async () => {
		const child = new FakeChild();
		child.succeed(JSON.stringify([{ name: 'other-plugin', version: '1.0.0' }]));

		const result = await detectPluginInstalled({ spawn: makeSpawnStub(child, { count: 0 }) });

		assert.strictEqual(result.installed, false);
	});

	it('cache: second call within TTL returns same value without re-spawning', async () => {
		const child1 = new FakeChild();
		const calls = { count: 0 };
		const pluginList = JSON.stringify([{ name: 'mcp-gateway', version: '2.0.0' }]);
		child1.succeed(pluginList);

		const result1 = await detectPluginInstalled({ ctlPath: 'claude', spawn: makeSpawnStub(child1, calls) });

		// Second child would return different data — but cache should prevent the call.
		const child2 = new FakeChild();
		child2.succeed(JSON.stringify([]));
		const result2 = await detectPluginInstalled({ ctlPath: 'claude', spawn: makeSpawnStub(child2, calls) });

		assert.strictEqual(result1.installed, true);
		assert.deepStrictEqual(result1, result2, 'cached result must equal first result');
		assert.strictEqual(calls.count, 1, 'spawn must be called only once within TTL');
	});

	it('cache: _resetDetectionCache clears the cache so next call re-spawns', async () => {
		const child1 = new FakeChild();
		const calls = { count: 0 };
		child1.succeed(JSON.stringify([{ name: 'mcp-gateway', version: '1.0.0' }]));

		await detectPluginInstalled({ ctlPath: 'my-claude', spawn: makeSpawnStub(child1, calls) });
		assert.strictEqual(calls.count, 1);

		_resetDetectionCache();

		const child2 = new FakeChild();
		child2.succeed(JSON.stringify([]));
		await detectPluginInstalled({ ctlPath: 'my-claude', spawn: makeSpawnStub(child2, calls) });
		assert.strictEqual(calls.count, 2, 'after cache reset, spawn must be called again');
	});
});

// --------------------------------------------------------------------------
// detectPatchInstalled tests
// --------------------------------------------------------------------------

describe('detectPatchInstalled', () => {
	beforeEach(() => {
		_resetDetectionCache();
	});

	/** Builds a fake readFile that maps path → content (rejects on unknown paths). */
	function makeReadFileFake(files: Map<string, string>): PatchDetectionOpts['readFile'] {
		return (async (filePath: string) => {
			if (files.has(filePath)) {
				return files.get(filePath) as string;
			}
			const err = Object.assign(new Error(`ENOENT: ${filePath}`), { code: 'ENOENT' });
			throw err;
		}) as unknown as PatchDetectionOpts['readFile'];
	}

	it('returns installed=false when index.js is missing (readFile rejects ENOENT)', async () => {
		// Provide a homeDir that will produce a path we control, but the readFile will reject.
		// We use a custom homeDir that makes findLatestCcExtensionIndexJs return no extension
		// by providing a fake readFile that always rejects ENOENT.
		// Because findLatestCcExtensionIndexJs uses sync fs.readdirSync, we need to make
		// it not find any extensions. Use a non-existent homeDir.
		const result = await detectPatchInstalled({
			homeDir: '/nonexistent-home-xyz',
			readFile: makeReadFileFake(new Map()),
		});

		assert.strictEqual(result.installed, false);
	});

	it('returns installed=true with correct version when marker matches bundled version', async () => {
		// We create a fake bundled patch first-line marker at the same version.
		// Since findLatestCcExtensionIndexJs reads real fs, we bypass it by using
		// a bundledPatchPath that does NOT exist, which means latestVersion = undefined.
		// To test installed=true path, we need to feed the readFile with the marker.
		//
		// The tricky part: findLatestCcExtensionIndexJs uses real fs.readdirSync.
		// For isolated unit testing, we feed a homeDir that has no extensions
		// (non-existent), which means installed=false. To cover installed=true,
		// we test detectPatchInstalled by exporting a testable inner function or
		// by accepting that the installed=true path requires a real extension dir.
		//
		// Instead, test the full flow by pointing homeDir to a temp dir structure.
		const os = await import('node:os');
		const fs = await import('node:fs');
		const path = await import('node:path');

		const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), 'detection-test-'));
		try {
			const extDir = path.join(tmpDir, '.vscode', 'extensions', 'anthropic.claude-code-1.5.0', 'webview');
			fs.mkdirSync(extDir, { recursive: true });

			const markerVersion = '1.0.0';
			const indexContent = `/* === MCP Gateway Patch v${markerVersion} === */\n// rest of file`;
			const indexPath = path.join(extDir, 'index.js');
			fs.writeFileSync(indexPath, indexContent, 'utf8');

			// Provide a bundled patch path that also has version 1.0.0.
			const bundledPatch = path.join(tmpDir, 'porfiry-mcp.js');
			fs.writeFileSync(bundledPatch, `/* === MCP Gateway Patch v${markerVersion} === */\n`, 'utf8');

			const result = await detectPatchInstalled({
				homeDir: tmpDir,
				bundledPatchPath: bundledPatch,
			});

			assert.strictEqual(result.installed, true);
			assert.strictEqual(result.stale, false);
			assert.strictEqual(result.currentVersion, markerVersion);
		} finally {
			fs.rmSync(tmpDir, { recursive: true, force: true });
		}
	});

	it('returns stale=true when bundled version is newer than installed version', async () => {
		const os = await import('node:os');
		const fs = await import('node:fs');
		const path = await import('node:path');

		const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), 'detection-test-stale-'));
		try {
			const extDir = path.join(tmpDir, '.vscode', 'extensions', 'anthropic.claude-code-1.5.0', 'webview');
			fs.mkdirSync(extDir, { recursive: true });

			const installedVersion = '1.0.0';
			const latestVersion = '1.1.0';
			const indexContent = `/* === MCP Gateway Patch v${installedVersion} === */\n// content`;
			fs.writeFileSync(path.join(extDir, 'index.js'), indexContent, 'utf8');

			const bundledPatch = path.join(tmpDir, 'porfiry-mcp.js');
			fs.writeFileSync(bundledPatch, `/* === MCP Gateway Patch v${latestVersion} === */\n`, 'utf8');

			const result = await detectPatchInstalled({
				homeDir: tmpDir,
				bundledPatchPath: bundledPatch,
			});

			assert.strictEqual(result.installed, true);
			assert.strictEqual(result.stale, true);
			assert.strictEqual(result.currentVersion, installedVersion);
			assert.strictEqual(result.latestVersion, latestVersion);
		} finally {
			fs.rmSync(tmpDir, { recursive: true, force: true });
		}
	});
});

// --------------------------------------------------------------------------
// detectChannelStatus tests
// --------------------------------------------------------------------------

describe('detectChannelStatus', () => {
	it('returns state=unknown with a non-empty detail string', () => {
		const result = detectChannelStatus();

		assert.strictEqual(result.state, 'unknown');
		assert.ok(result.detail.length > 0, 'detail must be non-empty');
	});

	it('returns an immutable result (Object.freeze)', () => {
		const result = detectChannelStatus();

		assert.throws(
			() => {
				// TypeScript will flag this but we test runtime behavior.
				(result as unknown as Record<string, unknown>)['state'] = 'active';
			},
			TypeError,
			'frozen object must throw on property assignment in strict mode',
		);
	});
});
