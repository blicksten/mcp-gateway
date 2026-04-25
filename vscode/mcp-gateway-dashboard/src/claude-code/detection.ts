// Claude Code detection helpers (Phase 1 — closes B-01..B-05, B-13).
//
// Three pure async helpers that detect the real state of the plugin,
// patch, and channel. All external side-effects (spawn, readFile) are
// injected via opts so tests can fake them without a real claude binary
// or filesystem.
//
// Cache: each helper caches its result for 60 s (module-level Map).
// Tests call _resetDetectionCache() between cases to avoid cross-test
// pollution.

import { spawn as nodeSpawn } from 'node:child_process';
import { readFile as nodeReadFile } from 'node:fs/promises';
import * as os from 'node:os';
import * as path from 'node:path';
import * as fs from 'node:fs';

// --------------------------------------------------------------------------
// Public types
// --------------------------------------------------------------------------

export interface PluginDetection {
	installed: boolean;
	version?: string;
	marketplace?: string;
}

export interface PatchDetection {
	installed: boolean;
	stale?: boolean;
	currentVersion?: string;
	latestVersion?: string;
}

export interface ChannelStatus {
	state: 'active' | 'inactive' | 'unknown';
	detail: string;
}

// --------------------------------------------------------------------------
// Internal cache
// --------------------------------------------------------------------------

interface CacheEntry<T> {
	value: T;
	expiresAt: number;
}

const CACHE_TTL_MS = 60_000;
const _cache = new Map<string, CacheEntry<unknown>>();

export function _resetDetectionCache(): void {
	_cache.clear();
}

function getCached<T>(key: string): T | undefined {
	const entry = _cache.get(key) as CacheEntry<T> | undefined;
	if (!entry) { return undefined; }
	if (Date.now() > entry.expiresAt) {
		_cache.delete(key);
		return undefined;
	}
	return entry.value;
}

function setCached<T>(key: string, value: T): void {
	_cache.set(key, { value, expiresAt: Date.now() + CACHE_TTL_MS });
}

// --------------------------------------------------------------------------
// detectPluginInstalled
// --------------------------------------------------------------------------

export interface PluginDetectionOpts {
	ctlPath?: string;
	spawn?: typeof nodeSpawn;
}

/**
 * Spawns `claude plugin list --json` and parses the output to determine
 * whether the mcp-gateway plugin is installed.
 *
 * Returns `{ installed: false }` on any error path (ENOENT, non-zero exit,
 * JSON parse failure, timeout) rather than throwing.
 */
export async function detectPluginInstalled(
	opts: PluginDetectionOpts = {},
): Promise<PluginDetection> {
	const cacheKey = `plugin:${opts.ctlPath ?? ''}`;
	const cached = getCached<PluginDetection>(cacheKey);
	if (cached !== undefined) { return cached; }

	const spawnFn = opts.spawn ?? nodeSpawn;
	const command = opts.ctlPath?.trim() || 'claude';

	const result = await spawnWithTimeout(spawnFn, command, ['plugin', 'list', '--json'], 5_000);
	if (!result.ok || result.stdout === '') {
		const detection: PluginDetection = Object.freeze({ installed: false });
		setCached(cacheKey, detection);
		return detection;
	}

	let parsed: unknown;
	try {
		parsed = JSON.parse(result.stdout);
	} catch {
		const detection: PluginDetection = Object.freeze({ installed: false });
		setCached(cacheKey, detection);
		return detection;
	}

	const detection = extractPluginDetection(parsed);
	setCached(cacheKey, detection);
	return detection;
}

/** Extracts plugin detection from the JSON returned by `claude plugin list --json`. */
function extractPluginDetection(parsed: unknown): PluginDetection {
	if (!Array.isArray(parsed)) {
		return Object.freeze({ installed: false });
	}
	for (const entry of parsed) {
		if (typeof entry !== 'object' || entry === null) { continue; }
		const rec = entry as Record<string, unknown>;
		const name = typeof rec['name'] === 'string' ? rec['name'] : '';
		const marketplace = typeof rec['marketplace'] === 'string' ? rec['marketplace'] : '';
		if (name === 'mcp-gateway' || marketplace === 'mcp-gateway-local') {
			const version = typeof rec['version'] === 'string' ? rec['version'] : undefined;
			return Object.freeze({
				installed: true,
				version,
				marketplace: marketplace || undefined,
			});
		}
	}
	return Object.freeze({ installed: false });
}

// --------------------------------------------------------------------------
// detectPatchInstalled
// --------------------------------------------------------------------------

/**
 * Marker format written at the top of porfiry-mcp.js and injected into
 * the CC webview index.js by apply-mcp-gateway.sh/.ps1.
 * Example: `/* === MCP Gateway Patch v1.0.0 === *​/`
 */
// Regex is necessary here: we need to capture a version number from a
// structured comment line in arbitrary file content.
const PATCH_MARKER_RE = /\/\* === MCP Gateway Patch v(\d+\.\d+\.\d+) === \*\//;

export interface PatchDetectionOpts {
	homeDir?: string;
	readFile?: typeof nodeReadFile;
	bundledPatchPath?: string;
}

/**
 * Checks whether the MCP Gateway patch marker is present in the latest
 * Claude Code extension's `webview/index.js`, and whether it's stale
 * relative to the bundled patch file.
 */
export async function detectPatchInstalled(opts: PatchDetectionOpts = {}): Promise<PatchDetection> {
	const cacheKey = `patch:${opts.homeDir ?? ''}:${opts.bundledPatchPath ?? ''}`;
	const cached = getCached<PatchDetection>(cacheKey);
	if (cached !== undefined) { return cached; }

	const readFileFn = opts.readFile ?? nodeReadFile;
	const homeDir = opts.homeDir ?? os.homedir();

	const indexPath = findLatestCcExtensionIndexJs(homeDir);
	if (!indexPath) {
		const detection: PatchDetection = Object.freeze({ installed: false });
		setCached(cacheKey, detection);
		return detection;
	}

	let indexContent: string;
	try {
		indexContent = await readFileFn(indexPath, 'utf8') as string;
	} catch {
		const detection: PatchDetection = Object.freeze({ installed: false });
		setCached(cacheKey, detection);
		return detection;
	}

	const match = PATCH_MARKER_RE.exec(indexContent);
	if (!match) {
		const detection: PatchDetection = Object.freeze({ installed: false });
		setCached(cacheKey, detection);
		return detection;
	}

	const currentVersion = match[1];
	const latestVersion = readBundledPatchVersion(opts.bundledPatchPath);

	if (latestVersion && isNewerVersion(latestVersion, currentVersion)) {
		const detection: PatchDetection = Object.freeze({
			installed: true,
			stale: true,
			currentVersion,
			latestVersion,
		});
		setCached(cacheKey, detection);
		return detection;
	}

	const detection: PatchDetection = Object.freeze({
		installed: true,
		stale: false,
		currentVersion,
		latestVersion: latestVersion ?? undefined,
	});
	setCached(cacheKey, detection);
	return detection;
}

/**
 * Globs `~/.vscode/extensions/anthropic.claude-code-*` and picks the
 * latest directory by semver. Returns the path to `webview/index.js`
 * inside that dir, or undefined if none found.
 */
function findLatestCcExtensionIndexJs(homeDir: string): string | undefined {
	const extensionsDir = path.join(homeDir, '.vscode', 'extensions');
	let entries: string[];
	try {
		entries = fs.readdirSync(extensionsDir);
	} catch {
		return undefined;
	}

	const prefix = 'anthropic.claude-code-';
	const candidates = entries
		.filter((e) => e.startsWith(prefix))
		.map((e) => ({ name: e, version: e.slice(prefix.length) }))
		.filter((c) => /^\d+\.\d+\.\d+/.test(c.version))
		.sort((a, b) => compareVersions(b.version, a.version));

	if (candidates.length === 0) { return undefined; }

	return path.join(extensionsDir, candidates[0].name, 'webview', 'index.js');
}

/**
 * Reads the first line of the bundled patch file and extracts the version.
 * Returns undefined if the file is missing or the marker is not found.
 */
function readBundledPatchVersion(bundledPatchPath?: string): string | undefined {
	const patchPath = bundledPatchPath ?? defaultBundledPatchPath();
	try {
		const content = fs.readFileSync(patchPath, 'utf8');
		const firstLine = content.split('\n')[0] ?? '';
		const match = PATCH_MARKER_RE.exec(firstLine);
		return match ? match[1] : undefined;
	} catch {
		return undefined;
	}
}

/**
 * Resolves the default bundled patch path relative to this module's location.
 * The extension lays out: src/claude-code/detection.ts → out/claude-code/detection.js
 * and installer/patches/porfiry-mcp.js is at the extension root.
 */
function defaultBundledPatchPath(): string {
	// __dirname in compiled JS is <extensionRoot>/out/claude-code/
	// porfiry-mcp.js is at <extensionRoot>/installer/patches/porfiry-mcp.js
	return path.join(__dirname, '..', '..', 'installer', 'patches', 'porfiry-mcp.js');
}

/** Returns true when versionA is strictly newer than versionB (semver major.minor.patch). */
function isNewerVersion(versionA: string, versionB: string): boolean {
	return compareVersions(versionA, versionB) > 0;
}

/** Numeric semver comparison. Returns positive if a > b, 0 if equal, negative if a < b. */
function compareVersions(a: string, b: string): number {
	const partsA = a.split('.').map((n) => parseInt(n, 10) || 0);
	const partsB = b.split('.').map((n) => parseInt(n, 10) || 0);
	for (let i = 0; i < 3; i++) {
		const diff = (partsA[i] ?? 0) - (partsB[i] ?? 0);
		if (diff !== 0) { return diff; }
	}
	return 0;
}

// --------------------------------------------------------------------------
// detectChannelStatus
// --------------------------------------------------------------------------

/**
 * Returns the channel telemetry status. Probe-trigger telemetry is not yet
 * wired; this stub ensures the HTML element shows actionable text instead of
 * a blank "—" (closes B-13).
 */
export function detectChannelStatus(): ChannelStatus {
	return Object.freeze({
		state: 'unknown' as const,
		detail: 'channel telemetry not yet wired',
	});
}

// --------------------------------------------------------------------------
// Internal spawn helper
// --------------------------------------------------------------------------

interface SpawnResult {
	ok: boolean;
	stdout: string;
	exitCode: number;
}

function spawnWithTimeout(
	spawnFn: typeof nodeSpawn,
	command: string,
	args: string[],
	timeoutMs: number,
): Promise<SpawnResult> {
	return new Promise((resolve) => {
		let child: ReturnType<typeof spawnFn>;
		let timedOut = false;
		let resolved = false;

		const finish = (result: SpawnResult) => {
			if (resolved) { return; }
			resolved = true;
			resolve(result);
		};

		try {
			child = spawnFn(command, args, { stdio: ['ignore', 'pipe', 'ignore'] });
		} catch (err) {
			finish({ ok: false, stdout: '', exitCode: -1 });
			return;
		}

		const timer = setTimeout(() => {
			timedOut = true;
			try { child.kill(); } catch { /* ignore */ }
			finish({ ok: false, stdout: '', exitCode: -1 });
		}, timeoutMs);

		const stdoutChunks: Buffer[] = [];
		child.stdout?.on('data', (chunk: Buffer) => stdoutChunks.push(chunk));

		child.on('error', () => {
			clearTimeout(timer);
			finish({ ok: false, stdout: '', exitCode: -1 });
		});

		child.on('close', (code: number | null) => {
			clearTimeout(timer);
			if (timedOut) { return; }
			const stdout = Buffer.concat(stdoutChunks).toString('utf8').trim();
			finish({ ok: code === 0, stdout, exitCode: code ?? -1 });
		});
	});
}
