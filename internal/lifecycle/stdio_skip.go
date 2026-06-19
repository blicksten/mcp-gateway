// Package lifecycle — TASK C1: skip-spawn pre-check for STDIO backends
// carrying a SAP_URL env that is unreachable (VPN off).
//
// When MCP_GATEWAY_SKIP_UNREACHABLE_STDIO is not "0" (default enabled),
// Start() TCP-probes the SAP_URL before spawning a stdio backend that
// carries that env key. An unreachable probe sets StatusUnreachable with a
// dashboard-friendly reason string and returns without spawning the child.
// The health monitor's slow-poll loop picks the backend up once the
// endpoint becomes reachable (VPN comes up) and calls Start() again.
//
// Non-SAP stdio backends (orchestrator, pal, context7, playwright, …) do NOT
// carry SAP_URL and are therefore never gated by this check. Only backends
// where Config.Command != "" AND SAPEnvURL returns a non-empty URL are in scope.
//
// UX:
//   - Skipped backend last-error string: skippedStdioReason (see below).
//     Dashboard should display this as "offline / VPN" rather than "error".
//   - Status is StatusUnreachable (same as HTTP unreachable) so it uses the
//     existing slow-poll recovery path after monitor.go is extended in C1.
//
// Guard:
//   - MCP_GATEWAY_SKIP_UNREACHABLE_STDIO=0 disables the feature.
//   - The env is read once per Start() call (via SkipUnreachableStdioEnabled),
//     not cached at startup, so it can be toggled in tests via t.Setenv.

package lifecycle

import (
	"context"
	"os"
	"time"

	"mcp-gateway/internal/models"
	"mcp-gateway/internal/sapname"
)

// skipUnreachableStdioEnv is the environment variable that controls the
// C1 pre-spawn reachability gate for SAP stdio backends.
// Set to "0" to disable; any other value (or absent) enables the gate.
const skipUnreachableStdioEnv = "MCP_GATEWAY_SKIP_UNREACHABLE_STDIO"

// skippedStdioReason is the LastError message set on a skipped backend.
// The health monitor's slow-poll recovery path detects StatusUnreachable
// and re-probes; this string is surfaced in the dashboard as context.
const skippedStdioReason = "skipped: SAP endpoint unreachable — connect VPN to enable"

// stdioTCPProbeTimeout is the per-probe budget for the pre-spawn SAP
// reachability check. Kept short (3s) so StartAll with many SAP backends
// does not stall boot; they run concurrently via goroutines in StartAll.
const stdioTCPProbeTimeout = 3 * time.Second

// SkipUnreachableStdioEnabled reports whether the C1 pre-spawn gate is active.
// It reads MCP_GATEWAY_SKIP_UNREACHABLE_STDIO from the environment at call time
// so tests can toggle it with t.Setenv without restarting the binary.
// Default (env absent or empty) is ENABLED (returns true).
func SkipUnreachableStdioEnabled() bool {
	return os.Getenv(skipUnreachableStdioEnv) != "0"
}

// shouldSkipStdioSpawn is a pure predicate: returns true when a stdio backend
// should be skipped at spawn time because its SAP endpoint is unreachable.
//
// Parameters:
//   - cfg: the backend's ServerConfig.
//   - name: the backend's name (used for sapname.IsVSP / IsSAPGUI checks).
//   - featureEnabled: result of SkipUnreachableStdioEnabled() — injected so
//     callers can override in table tests without touching the env.
//   - reachable: result of the TCP probe for the SAP_URL — injected so
//     callers can unit-test all branches without real network I/O.
//
// Decision table:
//
//	not a stdio backend (Config.Command == "")       → false (HTTP path unchanged)
//	feature disabled (featureEnabled == false)        → false
//	no SAP_URL in env                                → false (non-SAP stdio)
//	not a SAP server name (not vsp-* or sap-gui-*)   → false (conservative guard)
//	sap-gui-* (COM automation, no TCP endpoint)      → false (can't TCP-probe)
//	vsp-* with SAP_URL AND reachable == false        → TRUE  (skip spawn)
//	vsp-* with SAP_URL AND reachable == true         → false (spawn normally)
func shouldSkipStdioSpawn(cfg models.ServerConfig, name string, featureEnabled bool, reachable bool) bool {
	// Only stdio backends are in scope; HTTP/SSE keeps its own pre-check path.
	if cfg.Command == "" {
		return false
	}
	if !featureEnabled {
		return false
	}
	// Only SAP backends (vsp-* or sap-gui-*) carry a SAP endpoint env.
	// Non-SAP stdio (orchestrator, pal, context7, …) must not be probed.
	if !sapname.IsSAP(name) {
		return false
	}
	// Only vsp-* backends have a SAP_URL for TCP probing.
	// sap-gui-* backends use COM automation (no TCP endpoint) and should
	// spawn unconditionally so their GUI sessions can be detected later.
	if !sapname.IsVSP(name) {
		return false
	}
	// A vsp-* backend without SAP_URL is unusual but should not be skipped —
	// we have no endpoint to probe, so give the benefit of the doubt.
	if _, hasSAPURL := cfg.SAPEnvURL(); !hasSAPURL {
		return false
	}
	// Skip only when the endpoint is unreachable.
	return !reachable
}

// probeStdioSAPEndpoint TCP-dials the SAP_URL extracted from cfg.Env.
// Returns (sapURL, reachable): sapURL is the URL probed (for logging),
// reachable is true when the dial succeeds within stdioTCPProbeTimeout.
// Returns ("", true) when cfg carries no SAP_URL (no probe performed;
// caller must not skip in that case).
func probeStdioSAPEndpoint(ctx context.Context, cfg models.ServerConfig) (sapURL string, reachable bool) {
	sapURL, ok := cfg.SAPEnvURL()
	if !ok {
		// No SAP_URL — cannot probe; treat as reachable so spawn proceeds normally.
		return "", true
	}
	err := checkTCPReachable(ctx, sapURL, stdioTCPProbeTimeout)
	return sapURL, err == nil
}
