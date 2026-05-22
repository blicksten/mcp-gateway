// FM-16 binary drift prevention (P2.3 2026-05-22).
//
// Before this module, the extension passed an empty `daemonPath` through
// to DaemonManager which then called `spawn('mcp-gateway')`. Node's
// child_process.spawn resolves an unqualified command cwd-first then PATH.
// VS Code opens workspaces at the project root, so when developers opened
// the mcp-gateway project itself, `spawn('mcp-gateway')` found a stale
// project-local `mcp-gateway.exe` build BEFORE the freshly-installed
// `~/go/bin/mcp-gateway.exe` on PATH. This caused a 2026-05-08 incident
// where the extension launched an April binary missing MCPR.3 admin
// token, MCPR.4 plugin reannounce, and all version-reporting machinery.
//
// resolveDefaultDaemonPath closes the gap: when the user has not
// configured a daemon path explicitly, prefer the standard `~/go/bin/`
// install location over PATH-based lookup. This eliminates the cwd-first
// vulnerability while preserving the explicit-override knob via the
// `mcpGateway.daemonPath` setting.

import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';

/**
 * Returns the path to the canonical `~/go/bin/mcp-gateway` binary for the
 * current platform (Windows appends `.exe`). Used as the default install
 * location of `go install ./...` on every supported platform.
 */
export function goBinDaemonPath(homedir: string = os.homedir(), platform: NodeJS.Platform = process.platform): string {
	const exeName = platform === 'win32' ? 'mcp-gateway.exe' : 'mcp-gateway';
	return path.join(homedir, 'go', 'bin', exeName);
}

/**
 * Resolve the daemon path that DaemonManager.start should pass to
 * child_process.spawn.
 *
 * Resolution order:
 *  1. `configuredPath` non-empty — caller has set `mcpGateway.daemonPath`
 *     explicitly. Honour it as-is (full path or PATH-relative name).
 *  2. `~/go/bin/mcp-gateway[.exe]` exists — return the absolute path so
 *     spawn does not consult cwd or PATH. Eliminates the FM-16 cwd-first
 *     vulnerability.
 *  3. Fall back to the bare name `mcp-gateway`. Node's spawn will then
 *     resolve via PATH. Project-local cwd binaries can still be picked
 *     up here, but this branch is only reached when `~/go/bin/` does not
 *     contain a freshly-installed binary — i.e. either the user uses an
 *     OS package install on PATH, or no binary is available at all.
 *
 * `accessSync` is the lightest filesystem probe and matches the
 * sync-resolution flow at extension activation (this runs once before
 * DaemonManager is constructed). On Windows, `accessSync` does not require
 * the execute bit (Windows has no Unix permission bits) so X_OK is
 * implicit for any readable file.
 */
export function resolveDefaultDaemonPath(
	configuredPath: string,
	options?: {
		homedir?: string;
		platform?: NodeJS.Platform;
		exists?: (p: string) => boolean;
	},
): string {
	if (configuredPath && configuredPath.trim() !== '') {
		return configuredPath;
	}
	const homedir = options?.homedir ?? os.homedir();
	const platform = options?.platform ?? process.platform;
	const exists = options?.exists ?? defaultExists;
	const goBin = goBinDaemonPath(homedir, platform);
	if (exists(goBin)) {
		return goBin;
	}
	return 'mcp-gateway';
}

function defaultExists(p: string): boolean {
	try {
		fs.accessSync(p, fs.constants.F_OK);
		return true;
	} catch {
		return false;
	}
}
