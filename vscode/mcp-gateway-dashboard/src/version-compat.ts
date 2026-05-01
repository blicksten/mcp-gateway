/**
 * Version compatibility guardrail (Phase 4 — B-10 addendum per PAL gpt-5.2-pro 2026-04-25).
 *
 * Checks that the running gateway daemon meets the minimum version required
 * by this extension build. Returns a structured error when the daemon is too
 * old, so the caller can surface an actionable toast instead of a generic
 * "offline" banner.
 *
 * Design constraints:
 * - Pure module — no VSCode imports, no network calls, no side effects.
 * - Fail-safe: malformed or missing version strings resolve as "compatible"
 *   to avoid false-positive blocks when the daemon predates versioned health
 *   responses.
 */

/** Minimum gateway daemon version required by this extension build. */
export const MIN_GATEWAY_VERSION = '1.5.0';

export interface IncompatibleVersionError {
	kind: 'version-skew';
	extensionVersion: string;
	minRequiredGatewayVersion: string;
	actualGatewayVersion: string;
	remediation: string;
}

/** Parsed semver triple (all integers). */
interface SemVer {
	major: number;
	minor: number;
	patch: number;
}

/**
 * Parse a version string of the form `vMAJOR.MINOR.PATCH` or
 * `MAJOR.MINOR.PATCH`. Returns null for any malformed input so
 * callers can choose the fail-safe path.
 */
function parseSemVer(raw: string): SemVer | null {
	if (typeof raw !== 'string') { return null; }
	const stripped = raw.startsWith('v') ? raw.slice(1) : raw;
	const parts = stripped.split('.');
	if (parts.length !== 3) { return null; }
	const [major, minor, patch] = parts.map(Number);
	if (!Number.isInteger(major) || !Number.isInteger(minor) || !Number.isInteger(patch)) {
		return null;
	}
	if (major < 0 || minor < 0 || patch < 0) { return null; }
	return { major, minor, patch };
}

/**
 * Returns true when `actual >= required` (semver component-by-component
 * comparison as integers). Returns false when actual is strictly below
 * required.
 */
function isAtLeast(actual: SemVer, required: SemVer): boolean {
	if (actual.major !== required.major) { return actual.major > required.major; }
	if (actual.minor !== required.minor) { return actual.minor > required.minor; }
	return actual.patch >= required.patch;
}

/**
 * Check whether the running gateway daemon is compatible with this extension.
 *
 * @param extensionVersion - The extension's own version string (from package.json).
 * @param gatewayHealth - The cached /api/v1/health response (may be null or have
 *   no `version` field for pre-D.1 daemons).
 * @returns null when compatible (or when version cannot be determined — fail-safe),
 *   or an `IncompatibleVersionError` when the daemon is provably too old.
 */
export function assertCompatible(
	extensionVersion: string,
	gatewayHealth: { version?: string } | null | undefined,
): IncompatibleVersionError | null {
	// No health response yet — defer to next refresh.
	if (!gatewayHealth) { return null; }

	const actualStr = gatewayHealth.version;
	// Missing or malformed version → fail-safe (compatible).
	if (!actualStr) { return null; }
	const actual = parseSemVer(actualStr);
	if (!actual) { return null; }

	// Malformed extension version → fail-safe (compatible).
	const min = parseSemVer(MIN_GATEWAY_VERSION);
	if (!min) { return null; }

	if (isAtLeast(actual, min)) { return null; }

	const remediation =
		`Extension v${extensionVersion} expected gateway >= v${MIN_GATEWAY_VERSION}, ` +
		`but daemon is v${actualStr}. ` +
		`Update the daemon: 'go install ./cmd/mcp-gateway' or pull the latest release.`;

	return {
		kind: 'version-skew',
		extensionVersion,
		minRequiredGatewayVersion: MIN_GATEWAY_VERSION,
		actualGatewayVersion: actualStr,
		remediation,
	};
}
