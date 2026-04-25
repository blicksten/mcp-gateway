// Patch installer — invokes apply-mcp-gateway.sh / apply-mcp-gateway.ps1.
//
// Platform dispatch per REVIEW-16 M-04: win32 → PowerShell, else → sh.
// The scripts themselves (authored in Phase 16.4 — feature-b8f2decf
// pipeline) handle idempotency, token substitution, backup/restore, and
// chmod 600 / icacls DACL. This module is a thin spawn wrapper.
//
// Env contract (B-NEW-18): canonical names are imported from installer-contract.ts.
// Token semantics (B-NEW-31): only the token FILE PATH travels via env — never
// raw token bytes.

import { spawn as nodeSpawn } from 'node:child_process';
import * as path from 'node:path';
import { INSTALLER_ENV, LEGACY_INSTALLER_ENV } from './installer-contract';

export interface InstallerOptions {
	/** Absolute path to the extension directory — used to resolve script paths. */
	extensionPath: string;
	/** Gateway URL the script substitutes into the patch (e.g. http://localhost:8765). */
	gatewayUrl: string;
	/**
	 * Filesystem path to the auth-token file (B-NEW-31). NEVER raw token bytes.
	 * Passed as MCP_GATEWAY_TOKEN_FILE env var so the apply script reads it.
	 */
	tokenPath: string;
	/** true → run with --uninstall flag to restore backup. */
	uninstall?: boolean;
	/**
	 * Injectable spawn function for testability. Defaults to node:child_process.spawn.
	 * Tests pass a fake that captures argv + env without launching a real process.
	 */
	spawn?: typeof nodeSpawn;
}

export interface InstallerResult {
	ok: boolean;
	exitCode: number;
	stdout: string;
	stderr: string;
}

/**
 * Runs the platform-appropriate apply script. Returns structured result so
 * the dashboard can surface exit code + output in the UI (and feed the
 * failure trace into [Copy diagnostics]).
 *
 * Env contract (B-NEW-18 + B-NEW-31): uses INSTALLER_ENV / LEGACY_INSTALLER_ENV
 * from installer-contract.ts. MCP_GATEWAY_URL is the canonical URL name;
 * GATEWAY_URL is emitted alongside it during the v1.10..v2.0 compat window.
 * MCP_GATEWAY_TOKEN_FILE carries only the token file path — never raw token
 * bytes (path leaks are non-secret; file is 0o600-protected).
 */
export function runPatchInstaller(opts: InstallerOptions): Promise<InstallerResult> {
	const scriptDir = path.join(opts.extensionPath, '..', '..', 'installer', 'patches');
	const isWindows = process.platform === 'win32';
	const scriptPath = isWindows
		? path.join(scriptDir, 'apply-mcp-gateway.ps1')
		: path.join(scriptDir, 'apply-mcp-gateway.sh');

	const argv: string[] = [];
	let command: string;
	if (isWindows) {
		command = 'powershell.exe';
		argv.push('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', scriptPath);
	} else {
		command = '/bin/sh';
		argv.push(scriptPath);
	}
	argv.push('--auto');
	if (opts.uninstall) {
		argv.push('--uninstall');
	}

	const spawnFn = opts.spawn ?? nodeSpawn;

	return new Promise((resolve) => {
		const child = spawnFn(command, argv, {
			env: {
				...process.env,
				[INSTALLER_ENV.URL]: opts.gatewayUrl,
				[INSTALLER_ENV.TOKEN_FILE]: opts.tokenPath,
				[LEGACY_INSTALLER_ENV.URL]: opts.gatewayUrl,
			},
			stdio: ['ignore', 'pipe', 'pipe'],
			windowsHide: true,
		});
		const stdoutChunks: Buffer[] = [];
		const stderrChunks: Buffer[] = [];
		child.stdout.on('data', (b: Buffer) => stdoutChunks.push(b));
		child.stderr.on('data', (b: Buffer) => stderrChunks.push(b));
		child.on('error', (err) => {
			resolve({
				ok: false,
				exitCode: -1,
				stdout: '',
				stderr: `spawn error: ${err.message}`,
			});
		});
		child.on('close', (code) => {
			resolve({
				ok: code === 0,
				exitCode: code ?? -1,
				stdout: Buffer.concat(stdoutChunks).toString('utf8'),
				stderr: Buffer.concat(stderrChunks).toString('utf8'),
			});
		});
	});
}
