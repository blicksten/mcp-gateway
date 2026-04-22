// Patch installer — invokes apply-mcp-gateway.sh / apply-mcp-gateway.ps1.
//
// Platform dispatch per REVIEW-16 M-04: win32 → PowerShell, else → sh.
// The scripts themselves (authored in Phase 16.4 — feature-b8f2decf
// pipeline) handle idempotency, token substitution, backup/restore, and
// chmod 600 / icacls DACL. This module is a thin spawn wrapper.

import { spawn } from 'node:child_process';
import * as path from 'node:path';

export interface InstallerOptions {
	/** Absolute path to the extension directory — used to resolve script paths. */
	extensionPath: string;
	/** Gateway URL the script substitutes into the patch (e.g. http://localhost:8765). */
	gatewayUrl: string;
	/** Bearer token substituted into the patch. */
	gatewayAuthToken: string;
	/** true → run with --uninstall flag to restore backup. */
	uninstall?: boolean;
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
 * Security: the token is passed through environment variables (not argv)
 * so it does not appear in process lists or shell history. The apply
 * script reads `$GATEWAY_AUTH_TOKEN` (documented in Phase 16.4 work orders).
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

	return new Promise((resolve) => {
		const child = spawn(command, argv, {
			env: {
				...process.env,
				GATEWAY_URL: opts.gatewayUrl,
				GATEWAY_AUTH_TOKEN: opts.gatewayAuthToken,
			},
			stdio: ['ignore', 'pipe', 'pipe'],
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
