/**
 * Bearer auth header builder — shared by GatewayClient (REST) and LogViewer
 * (raw http.request against SSE /logs). Matches the Go-side contract in
 * internal/auth/client.go: env var > file > error.
 *
 * See docs/ADR-0003-bearer-token-auth.md §auth-header-fallback.
 */

import * as fs from 'node:fs';
import * as path from 'node:path';
import * as os from 'node:os';

// AUDIT B-NEW-29 (Phase 11): token-file cache for the async resolution path.
// Pre-fix: resolveToken() used fs.readFileSync on every REST call (2 syncIO
// events per 5s poll cycle), blocking the extension host event loop on
// encrypted home dirs / WSL-NTFS / slow disks.
// Fix: cache {content, mtimeMs, tokenPath}; async stat per call (cheap, no
// content transfer); re-read file only when mtime changes.
interface TokenFileCache {
	tokenPath: string;
	content: string;
	mtimeMs: number;
}

let _tokenCache: TokenFileCache | null = null;

// MCPR.3: separate cache slot for the admin-token path. Two reasons:
//   (1) admin and regular tokens live at different paths
//       (~/.mcp-gateway/admin.token vs auth.token), so a single cache keyed
//       on tokenPath would invalidate on every alternation between calls.
//   (2) admin lookups are rare (only daemon-control endpoints), so an
//       independent slot keeps the regular-path cache hot for the
//       per-poll REST traffic that drives the dashboard.
let _adminTokenCache: TokenFileCache | null = null;

/**
 * Maximum time (ms) to wait for a single `fs.promises.stat` call before
 * giving up. On corporate Windows (domain user, encrypted profile, antivirus)
 * `stat` can hang for minutes when multiple extension-host processes call it
 * concurrently at VSCode startup, saturating libuv's 4-thread pool. A 2 s cap
 * means: cache-hit path returns cached content immediately; cache-miss path
 * falls back to readFileSync (bypasses the pool).
 */
const STAT_TIMEOUT_MS = 2000;

/**
 * Race `fs.promises.stat(p)` against a 2 s timeout.
 * Returns `null` on timeout or any FS error (ENOENT, EPERM, EMFILE, etc.) so
 * callers can fall back gracefully instead of hanging.
 *
 * Trade-off accepted: a genuine FS error (not just a timeout) is also mapped
 * to `null`, which causes the cache-hit path to serve the last-known-good
 * token rather than evicting and re-reading.  For the error cases:
 *   - ENOENT (file deleted after first read): cache serves the stale token
 *     until the gateway rejects it with 401.
 *   - EPERM / EMFILE: transient lock / fd exhaustion — stale token is safe
 *     for one poll cycle; the next call will succeed when the condition clears.
 * These are deliberate trade-offs: serving a briefly stale 128-byte token is
 * far less disruptive than showing the gateway as "offline" for minutes.
 *
 * Timer cleanup: the timeout handle is always cancelled after the race settles
 * so it does not prevent Node's event loop from draining between test runs.
 */
async function statOrNull(p: string): Promise<fs.Stats | null> {
	let timer: ReturnType<typeof setTimeout> | undefined;
	const timeout = new Promise<null>((resolve) => {
		timer = setTimeout(() => resolve(null), STAT_TIMEOUT_MS);
	});
	try {
		return await Promise.race([fs.promises.stat(p).catch(() => null), timeout]);
	} finally {
		clearTimeout(timer);
	}
}

/**
 * Reset the module-level token caches. Exported for tests only — not part of
 * the public API. Allows deterministic test isolation without module reload.
 */
export function _clearTokenCacheForTests(): void {
	_tokenCache = null;
	_adminTokenCache = null;
}

/** Env var name matching Go-side `auth.EnvVarName`. */
export const ENV_VAR_NAME = 'MCP_GATEWAY_AUTH_TOKEN';

/**
 * MCPR.3: Env var name matching Go-side `auth.EnvVarNameAdmin`. Holds the
 * admin Bearer that gates daemon-control endpoints (currently
 * /api/v1/shutdown). Distinct from ENV_VAR_NAME so the regular Bearer
 * cannot accidentally satisfy admin scope.
 */
export const ENV_VAR_NAME_ADMIN = 'MCP_GATEWAY_ADMIN_TOKEN';

/**
 * Minimum base64url character count for a valid persisted token.
 * 32 random bytes → 43 base64url chars (no padding). Matches
 * Go-side `auth.MinTokenLen`.
 */
export const MIN_TOKEN_LEN = 43;

const BASE64URL_RE = /^[A-Za-z0-9_-]+$/;

/** Throws when no token can be resolved (env absent, file absent/malformed). */
export class AuthTokenError extends Error {
	constructor(
		message: string,
		public readonly tokenPath: string,
	) {
		super(message);
		this.name = 'AuthTokenError';
	}
}

/** Checks the structural shape of a token string — length + base64url alphabet. */
export function looksLikeToken(s: string): boolean {
	if (!s || s.length < MIN_TOKEN_LEN) { return false; }
	return BASE64URL_RE.test(s);
}

/**
 * Default platform-resolved token path (`~/.mcp-gateway/auth.token`).
 * Callers typically override this with the `mcpGateway.authTokenPath`
 * VS Code setting (see T12A.10).
 */
export function defaultTokenPath(): string {
	return path.join(os.homedir(), '.mcp-gateway', 'auth.token');
}

/**
 * Synchronously resolve the Bearer token using the ladder env > file.
 * Throws AuthTokenError if neither source yields a well-formed token.
 *
 * - Env path returns verbatim (ephemeral override, never writes disk).
 * - File path reads and trims; short/malformed content is treated as absent.
 */
export function resolveToken(tokenPath: string): string {
	const env = process.env[ENV_VAR_NAME];
	if (env && env.length > 0) {
		if (!looksLikeToken(env)) {
			throw new AuthTokenError(
				`${ENV_VAR_NAME} env var is set but malformed (expected >=${MIN_TOKEN_LEN} base64url chars)`,
				tokenPath,
			);
		}
		return env;
	}
	try {
		const raw = fs.readFileSync(tokenPath, 'utf8').trim();
		if (looksLikeToken(raw)) { return raw; }
	} catch (err) {
		const code = (err as NodeJS.ErrnoException).code;
		if (code !== 'ENOENT' && code !== 'EACCES') {
			throw err;
		}
		// ENOENT / EACCES fall through to the no-token error below.
	}
	throw new AuthTokenError(
		`no auth token found: set ${ENV_VAR_NAME} env var or create ${tokenPath}`,
		tokenPath,
	);
}

/**
 * Returns the full `Authorization: Bearer <token>` header value.
 * Throws AuthTokenError on failure.
 */
export function buildAuthHeader(tokenPath: string): string {
	return `Bearer ${resolveToken(tokenPath)}`;
}

/**
 * Async version of resolveToken — uses a mtime-checked cache to avoid
 * blocking the extension host event loop on every REST call.
 *
 * Env-var path (MCP_GATEWAY_AUTH_TOKEN): unchanged — process.env is in-memory,
 * no IO. File path: `statOrNull` per call to compare mtime against the cache
 * (stat is cheap: no content transfer). File content is re-read only when mtime
 * changes (e.g. operator runs `mcp-ctl install-claude-code --refresh-token`).
 * On the hot path (token unchanged) the only async work is one stat syscall.
 *
 * Hang-resilience (fix for corporate Windows domain hang):
 *   - Cache-hit  : if stat times out, return cached content (assume unchanged).
 *   - Cache-miss : use readFileSync to bypass libuv thread pool saturation;
 *                  stat is attempted after the read for mtime population only.
 *
 * AUDIT B-NEW-29 (Phase 11).
 */
export async function resolveTokenAsync(tokenPath: string): Promise<string> {
	const env = process.env[ENV_VAR_NAME];
	if (env && env.length > 0) {
		if (!looksLikeToken(env)) {
			throw new AuthTokenError(
				`${ENV_VAR_NAME} env var is set but malformed (expected >=${MIN_TOKEN_LEN} base64url chars)`,
				tokenPath,
			);
		}
		return env;
	}

	// Check the cache: if we have a valid entry for this path, stat the file
	// and re-read only when mtime changed.
	if (_tokenCache && _tokenCache.tokenPath === tokenPath) {
		const st = await statOrNull(tokenPath);
		if (st === null) {
			// stat timed out (FS hang) — assume token unchanged, serve from cache.
			return _tokenCache.content;
		}
		if (st.mtimeMs === _tokenCache.mtimeMs) {
			return _tokenCache.content; // cache hit — no re-read
		}
		// mtime changed — fall through to re-read.
		_tokenCache = null;
	}

	// Cache miss or stale — read the file.
	// Use readFileSync to bypass libuv thread-pool saturation: on domain Windows
	// fs.promises.stat can block all 4 threads for minutes at startup, so async
	// readFile would queue behind them.  The token file is tiny (≤ 128 bytes);
	// a synchronous read blocks the event loop for < 1 ms on any local disk.
	try {
		const raw = fs.readFileSync(tokenPath, 'utf8').trim();
		if (looksLikeToken(raw)) {
			// Best-effort mtime — used only for future cache invalidation.
			// statOrNull timeout here just means mtimeMs=0 (re-read next call).
			const st = await statOrNull(tokenPath);
			_tokenCache = { tokenPath, content: raw, mtimeMs: st ? st.mtimeMs : 0 };
			return raw;
		}
	} catch (err) {
		const code = (err as NodeJS.ErrnoException).code;
		if (code !== 'ENOENT' && code !== 'EACCES') {
			throw err;
		}
		// ENOENT / EACCES fall through to no-token error.
	}

	throw new AuthTokenError(
		`no auth token found: set ${ENV_VAR_NAME} env var or create ${tokenPath}`,
		tokenPath,
	);
}

/**
 * Async version of buildAuthHeader. Uses the mtime-cached resolveTokenAsync.
 * AUDIT B-NEW-29 (Phase 11).
 */
export async function buildAuthHeaderAsync(tokenPath: string): Promise<string> {
	return `Bearer ${await resolveTokenAsync(tokenPath)}`;
}

/**
 * Resolve the token path from VS Code configuration with a sensible default.
 * Accepts a minimal interface so tests can inject a mock config object.
 */
export interface AuthConfig {
	get(section: string): string | undefined;
}

export function resolveTokenPath(cfg: AuthConfig | undefined): string {
	const configured = cfg?.get('authTokenPath');
	if (configured && configured.trim().length > 0) {
		// Expand leading `~/` — VS Code settings do not expand home automatically.
		if (configured.startsWith('~/') || configured.startsWith('~\\')) {
			return path.join(os.homedir(), configured.slice(2));
		}
		return configured;
	}
	return defaultTokenPath();
}

// ---------------------------------------------------------------------------
// MCPR.3 — admin token resolution (parallel to the regular path above)
// ---------------------------------------------------------------------------

/**
 * Default platform-resolved admin-token path (`~/.mcp-gateway/admin.token`).
 * Path-distinct from `auth.token` so the daemon and the extension never
 * confuse the two scopes. Plugin manifest invariant (ADR-0007 §two-tier-auth):
 * this path is NEVER substituted into the .mcp.json template, so VSCode
 * 1.119's built-in McpGatewayService cannot acquire it.
 */
export function defaultAdminTokenPath(): string {
	return path.join(os.homedir(), '.mcp-gateway', 'admin.token');
}

/**
 * MCPR.3: resolve the admin token path from VS Code configuration with a
 * sensible default. Mirror of `resolveTokenPath` for the admin scope.
 */
export function resolveAdminTokenPath(cfg: AuthConfig | undefined): string {
	const configured = cfg?.get('adminTokenPath');
	if (configured && configured.trim().length > 0) {
		if (configured.startsWith('~/') || configured.startsWith('~\\')) {
			return path.join(os.homedir(), configured.slice(2));
		}
		return configured;
	}
	return defaultAdminTokenPath();
}

/**
 * MCPR.3: synchronous resolution of the admin Bearer using the env >
 * file ladder. Mirror of `resolveToken` for the admin scope. Uses
 * ENV_VAR_NAME_ADMIN so the regular Bearer never satisfies admin scope.
 */
export function resolveAdminToken(tokenPath: string): string {
	const env = process.env[ENV_VAR_NAME_ADMIN];
	if (env && env.length > 0) {
		if (!looksLikeToken(env)) {
			throw new AuthTokenError(
				`${ENV_VAR_NAME_ADMIN} env var is set but malformed (expected >=${MIN_TOKEN_LEN} base64url chars)`,
				tokenPath,
			);
		}
		return env;
	}
	try {
		const raw = fs.readFileSync(tokenPath, 'utf8').trim();
		if (looksLikeToken(raw)) { return raw; }
	} catch (err) {
		const code = (err as NodeJS.ErrnoException).code;
		if (code !== 'ENOENT' && code !== 'EACCES') {
			throw err;
		}
	}
	throw new AuthTokenError(
		`no admin token found: set ${ENV_VAR_NAME_ADMIN} env var or create ${tokenPath}`,
		tokenPath,
	);
}

/**
 * MCPR.3: async admin-token resolution with a dedicated mtime cache so
 * regular-path caching is not invalidated by admin lookups. Mirror of
 * `resolveTokenAsync` for the admin scope. Applies the same hang-resilience
 * strategy (statOrNull + readFileSync on cache-miss) as resolveTokenAsync.
 */
export async function resolveAdminTokenAsync(tokenPath: string): Promise<string> {
	const env = process.env[ENV_VAR_NAME_ADMIN];
	if (env && env.length > 0) {
		if (!looksLikeToken(env)) {
			throw new AuthTokenError(
				`${ENV_VAR_NAME_ADMIN} env var is set but malformed (expected >=${MIN_TOKEN_LEN} base64url chars)`,
				tokenPath,
			);
		}
		return env;
	}

	if (_adminTokenCache && _adminTokenCache.tokenPath === tokenPath) {
		const st = await statOrNull(tokenPath);
		if (st === null) {
			return _adminTokenCache.content;
		}
		if (st.mtimeMs === _adminTokenCache.mtimeMs) {
			return _adminTokenCache.content;
		}
		_adminTokenCache = null;
	}

	try {
		const raw = fs.readFileSync(tokenPath, 'utf8').trim();
		if (looksLikeToken(raw)) {
			const st = await statOrNull(tokenPath);
			_adminTokenCache = { tokenPath, content: raw, mtimeMs: st ? st.mtimeMs : 0 };
			return raw;
		}
	} catch (err) {
		const code = (err as NodeJS.ErrnoException).code;
		if (code !== 'ENOENT' && code !== 'EACCES') {
			throw err;
		}
	}

	throw new AuthTokenError(
		`no admin token found: set ${ENV_VAR_NAME_ADMIN} env var or create ${tokenPath}`,
		tokenPath,
	);
}

/**
 * MCPR.3: returns the full `Authorization: Bearer <admin-token>` header.
 * Mirror of `buildAuthHeader` for the admin scope.
 */
export function buildAdminAuthHeader(tokenPath: string): string {
	return `Bearer ${resolveAdminToken(tokenPath)}`;
}

/**
 * MCPR.3: async admin Bearer header builder using the cached resolver.
 * Mirror of `buildAuthHeaderAsync` for the admin scope.
 */
export async function buildAdminAuthHeaderAsync(tokenPath: string): Promise<string> {
	return `Bearer ${await resolveAdminTokenAsync(tokenPath)}`;
}
