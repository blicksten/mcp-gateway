/**
 * Centralised gateway-version semantics for the extension UI.
 *
 * The daemon's /health.version field has 4 possible shapes:
 *   - `undefined`       — old daemon without the field, or refresh failure
 *   - `""`              — should never happen but treat as undefined
 *   - `"dev"`           — go run / pre-1.18 fallback in cmd/mcp-gateway/main.go
 *   - `"vX.Y.Z..."`     — real version (ldflags-injected by goreleaser, OR
 *                          synthesised by runtime/debug.ReadBuildInfo() since
 *                          audit Scope A F-A1 commit 1b98785 / 2026-05-06)
 *
 * Pre-audit, the predicate "this is a real version" was inlined at 4+ sites
 * with subtle drift (`!== 'dev'`, `!== undefined`, plain truthy check). The
 * audit MEDIUM finding SC-C-M1 flagged this; the helper centralises the
 * predicate so the next change to the dev-build sentinel (e.g. to Go's
 * native `(devel)`) needs only one edit.
 */

/**
 * Returns true when the daemon reports a real version — ldflags-injected
 * or VCS-derived. False for `undefined`, empty string, or the `"dev"`
 * sentinel.
 *
 * Use for predicate checks (footer visibility, version-compat short-circuit,
 * tree-item state). For a display string, use {@link formatGatewayVersion}.
 */
export function hasRealVersion(version: string | undefined): boolean {
	return typeof version === 'string' && version.length > 0 && version !== 'dev';
}

/**
 * Returns a renderable string for a daemon version, suitable for status-bar
 * tooltips, tree-item labels, and Copy-Diagnostics:
 *
 *   - real version (`"v1.29.0"` or `"1.29.0"`) → `"v1.29.0"` (forces v-prefix)
 *   - `"dev"`                                  → `"dev build"` (operator-friendly)
 *   - undefined / empty                         → `placeholder` (default `"unknown"`)
 *
 * The "dev build" label is intentional — a local-dev daemon should still be
 * visible in the UI rather than rendering as a missing field. The audit
 * Scope D LOW finding noted that hiding dev-build entirely was hostile to
 * developers.
 */
export function formatGatewayVersion(
	version: string | undefined,
	placeholder = 'unknown',
): string {
	if (typeof version !== 'string' || version.length === 0) { return placeholder; }
	if (version === 'dev') { return 'dev build'; }
	return version.startsWith('v') ? version : `v${version}`;
}
