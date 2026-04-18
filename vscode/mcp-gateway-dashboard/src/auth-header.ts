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
