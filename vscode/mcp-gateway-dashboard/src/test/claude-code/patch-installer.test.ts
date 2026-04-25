// Tests for runPatchInstaller — Phase 0f (B-NEW-18, B-NEW-31).
//
// Pattern: mocha + node:assert/strict. The fake spawn captures argv + env
// without launching any real process. Platform branching is exercised by
// stubbing process.platform via Object.defineProperty.

import { strict as assert } from 'node:assert';
import { EventEmitter } from 'node:events';
import { PassThrough } from 'node:stream';
import { describe, it, afterEach } from 'mocha';
import { spawn as realSpawn } from 'node:child_process';

import { runPatchInstaller, type InstallerOptions } from '../../claude-code/patch-installer';
import { INSTALLER_ENV, LEGACY_INSTALLER_ENV } from '../../claude-code/installer-contract';

// --------------------------------------------------------------------------
// Fake child process that satisfies the ChildProcess interface consumed by
// runPatchInstaller. Emits 'close' with code 0 immediately.
// --------------------------------------------------------------------------

interface CapturedSpawnCall {
	command: string;
	argv: string[];
	env: Record<string, string | undefined>;
}

class FakeChild extends EventEmitter {
	readonly stdout = new PassThrough();
	readonly stderr = new PassThrough();

	constructor() {
		super();
		setImmediate(() => {
			this.stdout.end();
			this.stderr.end();
			this.emit('close', 0);
		});
	}
}

function makeSpawnStub(capture: CapturedSpawnCall[]): InstallerOptions['spawn'] {
	// Cast to the complex spawn overload type. The stub only needs to satisfy the
	// (command, args, options) overload that runPatchInstaller actually calls.
	return ((command: string, argv: string[], options?: { env?: Record<string, string | undefined> }) => {
		capture.push({ command, argv, env: options?.env ?? {} });
		return new FakeChild();
	}) as unknown as typeof realSpawn;
}

// --------------------------------------------------------------------------
// Helper to override process.platform for a single test and restore after.
// --------------------------------------------------------------------------
let originalPlatform: PropertyDescriptor | undefined;

function stubPlatform(value: string): void {
	originalPlatform = Object.getOwnPropertyDescriptor(process, 'platform');
	Object.defineProperty(process, 'platform', { value, configurable: true });
}

function restorePlatform(): void {
	if (originalPlatform !== undefined) {
		Object.defineProperty(process, 'platform', originalPlatform);
		originalPlatform = undefined;
	}
}

// --------------------------------------------------------------------------
// Base options for tests (posix defaults — override platform per test).
// --------------------------------------------------------------------------
function makeOpts(overrides: Partial<InstallerOptions> = {}): InstallerOptions {
	return {
		extensionPath: '/ext',
		gatewayUrl: 'http://localhost:9001',
		tokenPath: '/home/user/.mcp-gateway/auth.token',
		...overrides,
	};
}

// --------------------------------------------------------------------------
// Tests
// --------------------------------------------------------------------------

describe('patch-installer — env contract (B-NEW-18, B-NEW-31)', () => {
	afterEach(() => {
		restorePlatform();
	});

	it('emits MCP_GATEWAY_URL (canonical) with the supplied gatewayUrl', async () => {
		const calls: CapturedSpawnCall[] = [];
		stubPlatform('linux');

		await runPatchInstaller(makeOpts({ spawn: makeSpawnStub(calls) }));

		assert.strictEqual(calls.length, 1);
		assert.strictEqual(calls[0].env[INSTALLER_ENV.URL], 'http://localhost:9001');
	});

	it('emits MCP_GATEWAY_TOKEN_FILE (canonical) with the supplied tokenPath', async () => {
		const calls: CapturedSpawnCall[] = [];
		stubPlatform('linux');

		await runPatchInstaller(makeOpts({ spawn: makeSpawnStub(calls) }));

		assert.strictEqual(calls[0].env[INSTALLER_ENV.TOKEN_FILE], '/home/user/.mcp-gateway/auth.token');
	});

	it('emits GATEWAY_URL (legacy compat shim) equal to gatewayUrl', async () => {
		const calls: CapturedSpawnCall[] = [];
		stubPlatform('linux');

		await runPatchInstaller(makeOpts({ spawn: makeSpawnStub(calls) }));

		assert.strictEqual(calls[0].env[LEGACY_INSTALLER_ENV.URL], 'http://localhost:9001');
	});

	it('does NOT emit GATEWAY_AUTH_TOKEN — security regression check (B-NEW-31)', async () => {
		const calls: CapturedSpawnCall[] = [];
		stubPlatform('linux');

		await runPatchInstaller(makeOpts({ spawn: makeSpawnStub(calls) }));

		assert.strictEqual(
			calls[0].env['GATEWAY_AUTH_TOKEN'],
			undefined,
			'GATEWAY_AUTH_TOKEN must never be emitted — it would expose raw token bytes',
		);
	});

	it('argv always contains --auto', async () => {
		const calls: CapturedSpawnCall[] = [];
		stubPlatform('linux');

		await runPatchInstaller(makeOpts({ spawn: makeSpawnStub(calls) }));

		assert.ok(calls[0].argv.includes('--auto'), `expected --auto in argv: ${calls[0].argv.join(' ')}`);
	});

	it('argv does NOT contain --uninstall when opts.uninstall is falsy', async () => {
		const calls: CapturedSpawnCall[] = [];
		stubPlatform('linux');

		await runPatchInstaller(makeOpts({ spawn: makeSpawnStub(calls), uninstall: false }));

		assert.ok(
			!calls[0].argv.includes('--uninstall'),
			`--uninstall must not appear when uninstall=false: ${calls[0].argv.join(' ')}`,
		);
	});

	it('argv contains --uninstall when opts.uninstall is true', async () => {
		const calls: CapturedSpawnCall[] = [];
		stubPlatform('linux');

		await runPatchInstaller(makeOpts({ spawn: makeSpawnStub(calls), uninstall: true }));

		assert.ok(
			calls[0].argv.includes('--uninstall'),
			`expected --uninstall in argv: ${calls[0].argv.join(' ')}`,
		);
	});
});

describe('patch-installer — platform dispatch', () => {
	afterEach(() => {
		restorePlatform();
	});

	it('posix: command is /bin/sh, scriptPath ends with apply-mcp-gateway.sh', async () => {
		const calls: CapturedSpawnCall[] = [];
		stubPlatform('linux');

		await runPatchInstaller(makeOpts({ spawn: makeSpawnStub(calls) }));

		assert.strictEqual(calls[0].command, '/bin/sh');
		// argv[0] is the script path on posix
		assert.ok(
			calls[0].argv[0].endsWith('apply-mcp-gateway.sh'),
			`expected .sh script, got: ${calls[0].argv[0]}`,
		);
	});

	it('win32: command is powershell.exe, -File arg ends with apply-mcp-gateway.ps1', async () => {
		const calls: CapturedSpawnCall[] = [];
		stubPlatform('win32');

		await runPatchInstaller(makeOpts({ spawn: makeSpawnStub(calls) }));

		assert.strictEqual(calls[0].command, 'powershell.exe');
		// argv includes -File <scriptPath>
		const fileIdx = calls[0].argv.indexOf('-File');
		assert.ok(fileIdx !== -1, 'expected -File in win32 argv');
		assert.ok(
			calls[0].argv[fileIdx + 1].endsWith('apply-mcp-gateway.ps1'),
			`expected .ps1 script, got: ${calls[0].argv[fileIdx + 1]}`,
		);
	});

	it('darwin: treated as posix — command is /bin/sh', async () => {
		const calls: CapturedSpawnCall[] = [];
		stubPlatform('darwin');

		await runPatchInstaller(makeOpts({ spawn: makeSpawnStub(calls) }));

		assert.strictEqual(calls[0].command, '/bin/sh');
	});
});
