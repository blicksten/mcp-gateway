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

/**
 * Reset the module-level token cache. Exported for tests only — not part of
 * the public API. Allows deterministic test isolation without module reload.
 */
export function _clearTokenCacheForTests(): void {
	_tokenCache = null;
}

/** Env var name matching Go-side `auth.EnvVarName`. */
export const ENV_VAR_NAME = 'MCP_GATEWAY_AUTH_TOKEN';

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
 * no IO. File path: `fs.promises.stat` per call to compare mtime against the
 * cache (stat is cheap: no content transfer). File content is re-read only
 * when mtime changes (e.g. operator runs `mcp-ctl install-claude-code --refresh-token`).
 * On the hot path (token unchanged) the only async work is one stat syscall.
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
		try {
			const st = await fs.promises.stat(tokenPath);
			if (st.mtimeMs === _tokenCache.mtimeMs) {
				return _tokenCache.content; // cache hit — no re-read
			}
		} catch {
			// File removed or inaccessible — fall through to full read/throw.
			_tokenCache = null;
		}
	}

	// Cache miss or stale — read the file.
	try {
		const st = await fs.promises.stat(tokenPath);
		const raw = await fs.promises.readFile(tokenPath, 'utf8');
		const trimmed = raw.trim();
		if (looksLikeToken(trimmed)) {
			_tokenCache = { tokenPath, content: trimmed, mtimeMs: st.mtimeMs };
			return trimmed;
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
