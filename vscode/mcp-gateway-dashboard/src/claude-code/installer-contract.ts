// Single source of truth for env var names + semantics across the patch
// installer chain (TS spawn -> apply-mcp-gateway.{sh,ps1}).
//
// Closes B-NEW-18 (contract drift) by giving every caller a typed import
// instead of a free-string env name in three different files. Mirror these
// constants in the apply-script header comments — when adding a new entry,
// update both files.
//
// Compat window (v1.10..v1.x): the apply scripts also accept the legacy
// `GATEWAY_URL` env name and emit a deprecation warning to stderr when only
// the legacy form is set. Drop in v2.0.0 (track in CHANGELOG).
//
// Token semantics (B-NEW-31, security): only token-FILE PATHS travel via
// env — never raw token bytes. Env values can leak via process dumps,
// `ps eww`, `/proc/<pid>/environ`, or diagnostic exports; a path is not
// itself a secret, the file behind it is `0o600`-protected.

/** Canonical env var names (v1.10+). */
export const INSTALLER_ENV = Object.freeze({
	/** Gateway base URL the patched session will connect to. */
	URL: 'MCP_GATEWAY_URL',
	/** Filesystem path to the auth-token file. NEVER token bytes. */
	TOKEN_FILE: 'MCP_GATEWAY_TOKEN_FILE',
} as const);

/**
 * Legacy env var names emitted alongside the canonical ones during the
 * compat window for any external consumer that may have hard-coded the
 * old names. Drop in v2.0.0.
 *
 * Note: only `URL` is mirrored. The legacy `GATEWAY_AUTH_TOKEN` (raw
 * bytes) is NOT emitted — it was the security regression B-NEW-31. The
 * new contract is path-only on the auth axis.
 */
export const LEGACY_INSTALLER_ENV = Object.freeze({
	URL: 'GATEWAY_URL',
} as const);
