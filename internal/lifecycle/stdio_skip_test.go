// TASK C1 — unit tests for the shouldSkipStdioSpawn predicate and
// probeStdioSAPEndpoint helper.
//
// All tests are pure or use a real local TCP listener (no mocks of net.Dial).
// The pure predicate tests cover every branch of the decision table documented
// in shouldSkipStdioSpawn. The probe test verifies reachable/unreachable paths
// without touching external hosts.
//
// Fail-without-fix property: before TASK C1, shouldSkipStdioSpawn did not
// exist; every test here would fail at compile time.
package lifecycle

import (
	"context"
	"fmt"
	"net"
	"testing"

	"mcp-gateway/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- helpers -----------------------------------------------------------------

// listenLocalStdioSkip starts a TCP listener on 127.0.0.1:0 and returns its
// port plus a close function. Used to create a "reachable" endpoint for tests.
func listenLocalStdioSkip(t *testing.T) (port int, close func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().(*net.TCPAddr)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	return addr.Port, func() { _ = ln.Close() }
}

// unboundLocalPortStdioSkip picks a port, closes the listener, and returns the
// port — subsequent connect attempts to it will be refused (unreachable path).
func unboundLocalPortStdioSkip(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return port
}

// vspCfg returns a ServerConfig that looks like a vsp-* backend with SAP_URL.
func vspCfg(sapURL string) models.ServerConfig {
	return models.ServerConfig{
		Command: "/fake/vsp.exe",
		Env:     []string{"SAP_URL=" + sapURL},
	}
}

// ---- shouldSkipStdioSpawn tests ---------------------------------------------

// TestShouldSkipStdioSpawn_HTTPBackend: HTTP/SSE backends (no Command) are
// never handled by shouldSkipStdioSpawn regardless of feature flag or reachability.
func TestShouldSkipStdioSpawn_HTTPBackend(t *testing.T) {
	cfg := models.ServerConfig{URL: "http://saphost:50000"}
	assert.False(t, shouldSkipStdioSpawn(cfg, "vsp-Q00", true, false),
		"HTTP backend must not be skipped by shouldSkipStdioSpawn (has no Command)")
}

// TestShouldSkipStdioSpawn_FeatureDisabled: when MCP_GATEWAY_SKIP_UNREACHABLE_STDIO=0
// the gate is a no-op even for a vsp-* backend with unreachable SAP_URL.
func TestShouldSkipStdioSpawn_FeatureDisabled(t *testing.T) {
	cfg := vspCfg("http://saphost:50000")
	assert.False(t, shouldSkipStdioSpawn(cfg, "vsp-Q00", false /*featureEnabled=false*/, false /*unreachable*/),
		"feature disabled: must never skip")
}

// TestShouldSkipStdioSpawn_NonSAPStdio: a stdio backend that is not a SAP
// server (no vsp-* / sap-gui-* name) must not be skipped.
func TestShouldSkipStdioSpawn_NonSAPStdio(t *testing.T) {
	cfg := models.ServerConfig{Command: "/usr/bin/orchestrator"}
	names := []string{"orchestrator", "pal", "context7", "playwright", "my-custom-server"}
	for _, name := range names {
		assert.False(t, shouldSkipStdioSpawn(cfg, name, true, false),
			"non-SAP stdio backend %q must not be skipped", name)
	}
}

// TestShouldSkipStdioSpawn_SAPGUIBackend: sap-gui-* backends use COM automation
// (no SAP_URL env) and must never be skipped by the predicate.
func TestShouldSkipStdioSpawn_SAPGUIBackend(t *testing.T) {
	cfg := models.ServerConfig{Command: "/fake/sap-gui-ctl"}
	names := []string{"sap-gui-Q00", "sap-gui-P01-100"}
	for _, name := range names {
		assert.False(t, shouldSkipStdioSpawn(cfg, name, true, false),
			"sap-gui-* backend %q must not be skipped (IsVSP=false)", name)
	}
}

// TestShouldSkipStdioSpawn_VSPNoSAPURL: a vsp-* backend without SAP_URL in env
// gets the benefit of the doubt and is not skipped (we can't probe what we
// don't know about).
func TestShouldSkipStdioSpawn_VSPNoSAPURL(t *testing.T) {
	cfg := models.ServerConfig{
		Command: "/fake/vsp.exe",
		// no SAP_URL in Env
	}
	assert.False(t, shouldSkipStdioSpawn(cfg, "vsp-Q00", true, false),
		"vsp-* without SAP_URL must not be skipped")
}

// TestShouldSkipStdioSpawn_VSP_Unreachable: the canonical skip case.
// vsp-* + SAP_URL + feature ON + unreachable = skip.
func TestShouldSkipStdioSpawn_VSP_Unreachable(t *testing.T) {
	cfg := vspCfg("http://saphost:50000")
	assert.True(t, shouldSkipStdioSpawn(cfg, "vsp-Q00", true, false /*unreachable*/),
		"vsp-* with SAP_URL unreachable must be skipped")
}

// TestShouldSkipStdioSpawn_VSP_Reachable: vsp-* + SAP_URL + feature ON + reachable
// must NOT be skipped (spawn proceeds normally).
func TestShouldSkipStdioSpawn_VSP_Reachable(t *testing.T) {
	cfg := vspCfg("http://saphost:50000")
	assert.False(t, shouldSkipStdioSpawn(cfg, "vsp-Q00", true, true /*reachable*/),
		"vsp-* with SAP_URL reachable must not be skipped")
}

// TestShouldSkipStdioSpawn_TableDriven: exhaustive table covering all branches.
func TestShouldSkipStdioSpawn_TableDriven(t *testing.T) {
	httpCfg := models.ServerConfig{URL: "http://host:8000"}
	noSAPURLCfg := models.ServerConfig{Command: "/fake/vsp.exe"}
	orchCfg := models.ServerConfig{Command: "/usr/bin/orchestrator"}
	sapGUICfg := models.ServerConfig{Command: "/fake/sap-gui-ctl"}
	vspWithURL := vspCfg("http://sap.corp:50000")

	tests := []struct {
		name        string
		cfg         models.ServerConfig
		serverName  string
		featEnabled bool
		reachable   bool
		want        bool
	}{
		{"http backend", httpCfg, "vsp-Q00", true, false, false},
		{"feature disabled", vspWithURL, "vsp-Q00", false, false, false},
		{"non-SAP stdio", orchCfg, "orchestrator", true, false, false},
		{"sap-gui no SAP_URL", sapGUICfg, "sap-gui-Q00", true, false, false},
		{"vsp no SAP_URL", noSAPURLCfg, "vsp-Q00", true, false, false},
		{"vsp reachable", vspWithURL, "vsp-Q00", true, true, false},
		{"vsp unreachable", vspWithURL, "vsp-Q00", true, false, true},
		{"vsp unreachable with client", vspWithURL, "vsp-Q00-800", true, false, true},
		{"vsp reachable with client", vspWithURL, "vsp-Q00-800", true, true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldSkipStdioSpawn(tt.cfg, tt.serverName, tt.featEnabled, tt.reachable)
			assert.Equal(t, tt.want, got)
		})
	}
}

// ---- probeStdioSAPEndpoint tests --------------------------------------------

// TestProbeStdioSAPEndpoint_NoSAPURL: when the config has no SAP_URL, the probe
// is skipped and (_, true) is returned (treat as reachable — don't skip).
func TestProbeStdioSAPEndpoint_NoSAPURL(t *testing.T) {
	cfg := models.ServerConfig{Command: "/fake/vsp.exe"}
	sapURL, reachable := probeStdioSAPEndpoint(context.Background(), cfg)
	assert.Equal(t, "", sapURL, "no SAP_URL: url must be empty")
	assert.True(t, reachable, "no SAP_URL: must be treated as reachable")
}

// TestProbeStdioSAPEndpoint_Reachable: SAP_URL pointing to a live local listener
// is reported as reachable.
func TestProbeStdioSAPEndpoint_Reachable(t *testing.T) {
	port, close := listenLocalStdioSkip(t)
	defer close()
	sapURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	cfg := models.ServerConfig{
		Command: "/fake/vsp.exe",
		Env:     []string{"SAP_URL=" + sapURL},
	}
	gotURL, reachable := probeStdioSAPEndpoint(context.Background(), cfg)
	assert.Equal(t, sapURL, gotURL)
	assert.True(t, reachable, "live listener must be reachable")
}

// TestProbeStdioSAPEndpoint_Unreachable: SAP_URL pointing to a closed port is
// reported as unreachable.
func TestProbeStdioSAPEndpoint_Unreachable(t *testing.T) {
	port := unboundLocalPortStdioSkip(t)
	sapURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	cfg := models.ServerConfig{
		Command: "/fake/vsp.exe",
		Env:     []string{"SAP_URL=" + sapURL},
	}
	gotURL, reachable := probeStdioSAPEndpoint(context.Background(), cfg)
	assert.Equal(t, sapURL, gotURL)
	assert.False(t, reachable, "closed port must be unreachable")
}

// ---- SkipUnreachableStdioEnabled tests --------------------------------------

// TestSkipUnreachableStdioEnabled_DefaultOn: absent env means the feature is ON.
func TestSkipUnreachableStdioEnabled_DefaultOn(t *testing.T) {
	t.Setenv(skipUnreachableStdioEnv, "")
	assert.True(t, SkipUnreachableStdioEnabled(), "absent env must default to enabled")
}

// TestSkipUnreachableStdioEnabled_ZeroDisables: "0" disables.
func TestSkipUnreachableStdioEnabled_ZeroDisables(t *testing.T) {
	t.Setenv(skipUnreachableStdioEnv, "0")
	assert.False(t, SkipUnreachableStdioEnabled(), "env=0 must disable the gate")
}

// TestSkipUnreachableStdioEnabled_NonZeroEnables: "1" (or any non-"0") enables.
func TestSkipUnreachableStdioEnabled_NonZeroEnables(t *testing.T) {
	for _, val := range []string{"1", "true", "yes", "on"} {
		t.Setenv(skipUnreachableStdioEnv, val)
		assert.True(t, SkipUnreachableStdioEnabled(), "env=%q must enable the gate", val)
	}
}
