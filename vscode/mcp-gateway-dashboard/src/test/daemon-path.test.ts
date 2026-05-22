// FM-16 binary drift prevention tests (P2.3 2026-05-22).

import { strict as assert } from 'node:assert';
import * as path from 'node:path';
import { goBinDaemonPath, resolveDefaultDaemonPath } from '../daemon-path';

describe('FM-16 daemon path resolution', () => {
	describe('goBinDaemonPath', () => {
		it('appends .exe on win32', () => {
			assert.equal(
				goBinDaemonPath('C:/Users/alice', 'win32'),
				path.join('C:/Users/alice', 'go', 'bin', 'mcp-gateway.exe'),
			);
		});

		it('omits .exe on linux', () => {
			assert.equal(
				goBinDaemonPath('/home/alice', 'linux'),
				path.join('/home/alice', 'go', 'bin', 'mcp-gateway'),
			);
		});

		it('omits .exe on darwin', () => {
			assert.equal(
				goBinDaemonPath('/Users/alice', 'darwin'),
				path.join('/Users/alice', 'go', 'bin', 'mcp-gateway'),
			);
		});
	});

	describe('resolveDefaultDaemonPath', () => {
		it('honors a non-empty configured path verbatim (full absolute)', () => {
			const got = resolveDefaultDaemonPath('C:/custom/build/mcp-gateway.exe', {
				homedir: 'C:/Users/alice',
				platform: 'win32',
				exists: () => true, // even though go-bin exists, explicit wins
			});
			assert.equal(got, 'C:/custom/build/mcp-gateway.exe');
		});

		it('honors a non-empty configured path verbatim (bare name, expecting PATH)', () => {
			const got = resolveDefaultDaemonPath('mcp-gateway-dev', {
				homedir: 'C:/Users/alice',
				platform: 'win32',
				exists: () => true,
			});
			assert.equal(got, 'mcp-gateway-dev');
		});

		it('treats a whitespace-only configured path as unset and falls back', () => {
			const got = resolveDefaultDaemonPath('   ', {
				homedir: 'C:/Users/alice',
				platform: 'win32',
				exists: () => true,
			});
			assert.equal(got, path.join('C:/Users/alice', 'go', 'bin', 'mcp-gateway.exe'));
		});

		it('returns ~/go/bin/mcp-gateway.exe on Windows when the binary exists', () => {
			const got = resolveDefaultDaemonPath('', {
				homedir: 'C:/Users/alice',
				platform: 'win32',
				exists: (p) => p === path.join('C:/Users/alice', 'go', 'bin', 'mcp-gateway.exe'),
			});
			assert.equal(got, path.join('C:/Users/alice', 'go', 'bin', 'mcp-gateway.exe'));
		});

		it('returns ~/go/bin/mcp-gateway on Linux when the binary exists', () => {
			const got = resolveDefaultDaemonPath('', {
				homedir: '/home/bob',
				platform: 'linux',
				exists: (p) => p === path.join('/home/bob', 'go', 'bin', 'mcp-gateway'),
			});
			assert.equal(got, path.join('/home/bob', 'go', 'bin', 'mcp-gateway'));
		});

		it('falls back to PATH lookup when go-bin binary is missing', () => {
			const got = resolveDefaultDaemonPath('', {
				homedir: 'C:/Users/alice',
				platform: 'win32',
				exists: () => false, // no go-bin binary
			});
			// FM-16 risk: this branch still permits cwd-first resolution.
			// Documented in the resolver: the only safe fix is for the
			// developer to install via `go install ./...` so ~/go/bin/ is
			// populated. The bare-name fallback is the historical behavior
			// preserved for users without a Go toolchain (e.g. those who
			// install the daemon via package manager onto PATH).
			assert.equal(got, 'mcp-gateway');
		});

		it('does NOT call `exists` when configured path is non-empty (perf + correctness)', () => {
			let existsCalls = 0;
			resolveDefaultDaemonPath('/explicit/path/mcp-gateway', {
				homedir: '/home/user',
				platform: 'linux',
				exists: () => { existsCalls++; return true; },
			});
			assert.equal(existsCalls, 0, '`exists` must not be called on explicit-path branch');
		});

		it('calls `exists` with the platform-correct go-bin path when configured is empty', () => {
			const calls: string[] = [];
			resolveDefaultDaemonPath('', {
				homedir: '/home/user',
				platform: 'linux',
				exists: (p) => { calls.push(p); return false; },
			});
			assert.deepEqual(calls, [path.join('/home/user', 'go', 'bin', 'mcp-gateway')]);
		});
	});
});
