#!/usr/bin/env node
// Cross-platform VSIX installer.
//
// Why this exists: on Windows, `code --install-extension ...` via npm/cmd
// resolves to `Code.exe` (the GUI binary in the VSCode install root,
// named `code` without an extension) rather than to `bin\code.cmd` (the
// CLI wrapper). The GUI binary rejects `--install-extension` with
// "bad option". PATHEXT order cannot be relied on across machines.
//
// Strategy: try known CLI paths in order, accept whichever one exits
// with code 0. Falls back to plain `code` for POSIX/WSL.
//
// Also accepts `cursor.cmd` — Cursor is a VSCode fork that installs
// .vsix files with the same flag.

'use strict';

const { spawnSync } = require('node:child_process');
const path = require('node:path');
const fs = require('node:fs');

const VSIX = process.argv[2] || 'mcp-gateway-dashboard-latest.vsix';

if (!fs.existsSync(VSIX)) {
	console.error(`[install-vsix] VSIX not found: ${VSIX}`);
	process.exit(2);
}

function candidates() {
	const list = [];

	if (process.platform === 'win32') {
		const localAppData = process.env.LOCALAPPDATA || path.join(process.env.USERPROFILE || '', 'AppData', 'Local');
		const programFiles = process.env['ProgramFiles'] || 'C:\\Program Files';

		// VSCode user-install (most common)
		list.push(path.join(localAppData, 'Programs', 'Microsoft VS Code', 'bin', 'code.cmd'));
		// VSCode system-install
		list.push(path.join(programFiles, 'Microsoft VS Code', 'bin', 'code.cmd'));
		// Cursor user-install
		list.push(path.join(localAppData, 'Programs', 'cursor', 'resources', 'app', 'bin', 'cursor.cmd'));
		// Cursor from D: drive (user has this per PATH snapshot)
		list.push('D:\\cursor\\resources\\app\\bin\\cursor.cmd');
	} else {
		// POSIX: rely on PATH — `code` CLI wrapper is a shell script there.
		list.push('code');
		list.push('cursor');
	}

	return list;
}

function tryInstall(cli) {
	if (process.platform === 'win32' && !fs.existsSync(cli)) {
		return { skipped: true };
	}
	console.log(`[install-vsix] Trying: ${cli}`);
	// On Windows, .cmd files must be run with shell:true so cmd.exe
	// interprets them. With shell:true, spawn concatenates argv and
	// sends a single command line to cmd.exe — so the executable path
	// has to be quoted ourselves if it contains spaces (e.g. the default
	// VSCode install under `Program Files` or `Microsoft VS Code`).
	// POSIX: plain exec, no quoting needed.
	let execPath = cli;
	const args = ['--install-extension', path.resolve(VSIX), '--force'];
	if (process.platform === 'win32') {
		execPath = `"${cli}"`;
		for (let i = 0; i < args.length; i++) {
			if (args[i].includes(' ')) { args[i] = `"${args[i]}"`; }
		}
	}
	const res = spawnSync(execPath, args, {
		stdio: 'inherit',
		shell: process.platform === 'win32',
		windowsVerbatimArguments: process.platform === 'win32',
	});
	return { skipped: false, status: res.status, error: res.error };
}

let lastErr;
for (const cli of candidates()) {
	const out = tryInstall(cli);
	if (out.skipped) { continue; }
	if (out.error) {
		lastErr = out.error;
		continue;
	}
	if (out.status === 0) {
		console.log(`[install-vsix] Installed via ${cli}`);
		console.log('[install-vsix] Reload the window: Command Palette > Developer: Reload Window');
		process.exit(0);
	}
	lastErr = new Error(`exit ${out.status}`);
}

console.error('[install-vsix] All candidates failed.');
if (lastErr) { console.error(`[install-vsix] Last error: ${lastErr.message}`); }
console.error('[install-vsix] Manual install: VSCode > Command Palette > Extensions: Install from VSIX...');
process.exit(1);
