#!/usr/bin/env node
// Unified build + deploy + verify for mcp-gateway.
//
// WHY THIS EXISTS — kills the "we tested a stale build" class of bug:
//   * `npm run deploy` only rebuilt the VS Code extension VSIX. The Go daemon
//     (~/go/bin/mcp-gateway.exe) had to be `go install`-ed and the running
//     daemon restarted by hand — easy to forget, so the live daemon ran old
//     code while we hunted bugs that were already fixed in source.
//   * `code --install-extension` left OLD extension versions on disk next to
//     the new one (1.33.21 + 1.33.22 both present) — duplicate installs.
//
// This single command does the full chain and then VERIFIES the running
// state matches the just-built artifacts:
//   1. go install daemon + cli         -> fresh ~/go/bin binaries
//   2. restart daemon                  -> running process == fresh binary
//   3. bump extension patch version    -> every deploy is a distinct version
//   4. compile + package VSIX
//   5. uninstall ALL installed versions, then install the fresh one -> no dupes
//   6. verify: daemon restarted & healthy; exactly ONE extension version
//      installed and it equals package.json
//
// Flags:
//   --skip-daemon-restart   build+install the daemon binary but do NOT restart
//                           the running daemon (use when you must not drop the
//                           current MCP transport). The binary is staged; the
//                           change goes live on the next natural restart.
//   --no-version-bump       reuse the current package.json version (re-deploy
//                           the same version, still de-duped).
'use strict';

const { spawnSync } = require('node:child_process');
const path = require('node:path');
const fs = require('node:fs');
const http = require('node:http');

const repoRoot = path.resolve(__dirname, '..');
const extDir = path.join(repoRoot, 'vscode', 'mcp-gateway-dashboard');
const pkgPath = path.join(extDir, 'package.json');
const EXT_ID = 'mcp-gateway.mcp-gateway-dashboard';
const VSIX = 'mcp-gateway-dashboard-latest.vsix';
const HEALTH_URL = 'http://127.0.0.1:8765/api/v1/health';

const args = process.argv.slice(2);
const skipDaemonRestart = args.includes('--skip-daemon-restart');
const noVersionBump = args.includes('--no-version-bump');

function step(n, msg) { console.log(`\n== [${n}/6] ${msg} ==`); }
function fail(msg) { console.error(`\n[deploy-all] FAIL: ${msg}`); process.exit(1); }
function run(cmd, cmdArgs, opts) {
	const res = spawnSync(cmd, cmdArgs, { stdio: 'inherit', ...opts });
	if (res.error) { fail(`${cmd} ${cmdArgs.join(' ')}: ${res.error.message}`); }
	if (res.status !== 0) { fail(`${cmd} ${cmdArgs.join(' ')}: exit ${res.status}`); }
	return res;
}
function capture(cmd, cmdArgs, opts) {
	return spawnSync(cmd, cmdArgs, { encoding: 'utf8', ...opts });
}

// Resolve the VS Code CLI wrapper (code.cmd / code). Mirrors install-vsix.js.
function resolveCode() {
	if (process.platform === 'win32') {
		const localAppData = process.env.LOCALAPPDATA || path.join(process.env.USERPROFILE || '', 'AppData', 'Local');
		const programFiles = process.env['ProgramFiles'] || 'C:\\Program Files';
		const list = [
			path.join(localAppData, 'Programs', 'Microsoft VS Code', 'bin', 'code.cmd'),
			path.join(programFiles, 'Microsoft VS Code', 'bin', 'code.cmd'),
			path.join(localAppData, 'Programs', 'Microsoft VS Code Insiders', 'bin', 'code-insiders.cmd'),
		];
		for (const c of list) { if (fs.existsSync(c)) { return c; } }
		fail('VS Code CLI (code.cmd) not found in known locations');
	}
	return 'code';
}
// On Windows .cmd must run through a shell with the path quoted.
function code(cliArgs) {
	const cli = resolveCode();
	if (process.platform === 'win32') {
		return run(`"${cli}"`, cliArgs.map(a => (a.includes(' ') ? `"${a}"` : a)),
			{ shell: true, windowsVerbatimArguments: true });
	}
	return run(cli, cliArgs);
}
function codeCapture(cliArgs) {
	const cli = resolveCode();
	if (process.platform === 'win32') {
		return capture(`"${cli}"`, cliArgs, { shell: true });
	}
	return capture(cli, cliArgs);
}

function gitShortSha12() {
	const r = capture('git', ['rev-parse', 'HEAD'], { cwd: repoRoot });
	return (r.stdout || '').trim().slice(0, 12);
}

function httpGetJson(url) {
	return new Promise((resolve) => {
		const req = http.get(url, { timeout: 5000 }, (res) => {
			let body = '';
			res.on('data', (c) => { body += c; });
			res.on('end', () => { try { resolve(JSON.parse(body)); } catch { resolve(null); } });
		});
		req.on('error', () => resolve(null));
		req.on('timeout', () => { req.destroy(); resolve(null); });
	});
}

function bumpPatchVersion() {
	// Targeted string edit — do NOT JSON.stringify the whole file (would
	// reformat and blow up the diff). String ops only, no regex.
	const raw = fs.readFileSync(pkgPath, 'utf8');
	const marker = '"version": "';
	const i = raw.indexOf(marker);
	if (i < 0) { fail('could not find "version" in package.json'); }
	const start = i + marker.length;
	const end = raw.indexOf('"', start);
	const cur = raw.slice(start, end);
	const parts = cur.split('.');
	if (parts.length !== 3) { fail(`unexpected version shape: ${cur}`); }
	parts[2] = String(Number(parts[2]) + 1);
	const next = parts.join('.');
	fs.writeFileSync(pkgPath, raw.slice(0, start) + next + raw.slice(end));
	return { cur, next };
}
function currentVersion() {
	const raw = fs.readFileSync(pkgPath, 'utf8');
	const marker = '"version": "';
	const i = raw.indexOf(marker);
	const start = i + marker.length;
	return raw.slice(start, raw.indexOf('"', start));
}

(async () => {
	const before = await httpGetJson(HEALTH_URL);
	const beforePid = before && before.pid;

	const win = process.platform === 'win32';

	step(1, 'go install daemon + cli');
	run('go', ['install', './cmd/mcp-gateway', './cmd/mcp-ctl'], { cwd: repoRoot, shell: win });

	step(2, skipDaemonRestart ? 'restart daemon (SKIPPED)' : 'restart daemon');
	if (!skipDaemonRestart) {
		const mcpctl = path.join(process.env.USERPROFILE || process.env.HOME || '', 'go', 'bin',
			win ? 'mcp-ctl.exe' : 'mcp-ctl');
		run(win ? `"${mcpctl}"` : mcpctl, ['daemon', 'restart'], { shell: win });
	}

	step(3, noVersionBump ? 'version bump (SKIPPED)' : 'bump extension patch version');
	const ver = noVersionBump ? { cur: currentVersion(), next: currentVersion() } : bumpPatchVersion();
	console.log(`[deploy-all] extension version: ${ver.cur} -> ${ver.next}`);

	step(4, 'compile + package VSIX');
	run('npm', ['run', 'compile'], { cwd: extDir, shell: process.platform === 'win32' });
	run('npm', ['run', 'package'], { cwd: extDir, shell: process.platform === 'win32' });

	step(5, 'dedupe + install (uninstall all versions, install fresh)');
	code(['--uninstall-extension', EXT_ID]); // removes every installed version
	code(['--install-extension', path.join(extDir, VSIX), '--force']);

	step(6, 'verify running state');
	let ok = true;

	// 6a. daemon healthy + actually restarted (unless skipped)
	const after = await httpGetJson(HEALTH_URL);
	if (!after || after.status !== 'ok') {
		console.error('  [x] daemon health: NOT ok'); ok = false;
	} else if (!skipDaemonRestart && after.pid === beforePid) {
		console.error(`  [x] daemon PID unchanged (${after.pid}) — restart did not take effect`); ok = false;
	} else {
		const sha = gitShortSha12();
		const verNote = sha && after.version && after.version.includes(sha)
			? ` (version embeds commit ${sha})`
			: ` (version=${after.version}; HEAD=${sha} — note: only matches when binary built from committed tree)`;
		console.log(`  [ok] daemon healthy: pid=${after.pid} version=${after.version}${verNote}`);
	}

	// 6b. exactly one extension version installed, equal to package.json
	const listed = codeCapture(['--list-extensions', '--show-versions']);
	const lines = (listed.stdout || '').split(/\r?\n/).filter(l => l.startsWith(EXT_ID + '@'));
	if (lines.length !== 1) {
		console.error(`  [x] expected exactly 1 installed ${EXT_ID}, found ${lines.length}: ${lines.join(', ')}`); ok = false;
	} else {
		const installedVer = lines[0].split('@')[1];
		if (installedVer !== ver.next) {
			console.error(`  [x] installed ${installedVer} != package.json ${ver.next}`); ok = false;
		} else {
			console.log(`  [ok] exactly one extension installed: ${EXT_ID}@${installedVer}`);
		}
	}

	if (!ok) { fail('post-deploy verification failed — see [x] lines above'); }
	console.log('\n[deploy-all] OK — daemon + extension are both the freshly built artifacts.');
	console.log('[deploy-all] Reload the VS Code window: Command Palette > Developer: Reload Window');
})();
