package proxy

// TASK C2.1 — filteredTools StatusIdle branch tests.
//
// Tests that verify:
//   - With the flag OFF, StatusIdle backends are NOT included in filteredTools.
//   - With the flag ON and a manifest, StatusIdle backends ARE included using
//     cached tools.
//   - With the flag ON but no manifest wired (nil), Idle backends are skipped.

import (
	"log/slog"
	"testing"

	"mcp-gateway/internal/lifecycle"
	"mcp-gateway/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// vspP01Config is the canonical ServerConfig for the "vsp-P01" backend used
// throughout gateway_idle_test.go. Any manifest entry for vsp-P01 must use
// lifecycle.BackendConfigSig(vspP01Config) so that GetValid (Guard 1) accepts it.
var vspP01Config = models.ServerConfig{
	Command: "/usr/bin/vsp",
	Env:     []string{"SAP_URL=https://sap:8443"},
}

// vspP01Sig returns the BackendConfigSig for vspP01Config.
// Used by tests that need to populate a manifest entry with the correct sig.
func vspP01Sig() string {
	return lifecycle.BackendConfigSig(vspP01Config)
}

// buildIdleTestGateway creates a Gateway with one Running backend and one Idle
// backend using the provided manifest. The manifest may be nil.
func buildIdleTestGateway(t *testing.T, manifest *lifecycle.Manifest) (*Gateway, *lifecycle.Manager) {
	t.Helper()
	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"core":    {URL: "http://127.0.0.1:19999"},
			"vsp-P01": &vspP01Config,
		},
	}
	cfg.ApplyDefaults()
	logger := slog.Default()
	lm := lifecycle.NewManager(cfg, "test", logger)

	// Core is running with one tool.
	lm.SetStatus("core", models.StatusRunning, "")
	lm.SetTools("core", []models.ToolInfo{
		{Name: "core_tool", Description: "Core tool", Server: "core"},
	})

	// vsp-P01 is idle (not spawned, tools from cache only).
	lm.SetStatus("vsp-P01", models.StatusIdle, models.StatusIdleReason)

	gw := New(cfg, lm, "test", logger)
	if manifest != nil {
		gw.SetManifest(manifest)
	}
	return gw, lm
}

// TestFilteredTools_IdleIncludedWhenFlagOn verifies that a StatusIdle backend
// with a manifest entry appears in filteredTools when the flag is ON.
func TestFilteredTools_IdleIncludedWhenFlagOn(t *testing.T) {
	t.Setenv("MCP_GATEWAY_LAZY_SPAWN", "1")

	path := t.TempDir() + "/tool-manifest.json"
	m, err := lifecycle.LoadManifest(path)
	require.NoError(t, err)
	m.Put("vsp-P01", vspP01Sig(), []models.ToolInfo{
		{Name: "sap_read", Description: "Read SAP table", Server: "vsp-P01"},
		{Name: "sap_write", Description: "Write SAP table", Server: "vsp-P01"},
	})
	require.NoError(t, m.Persist())

	gw, _ := buildIdleTestGateway(t, m)

	tools := gw.filteredTools()

	// Both core_tool and the cached SAP tools should be present.
	names := toolNames(tools)
	assert.Contains(t, names, "core__core_tool", "running backend tool must appear")
	assert.Contains(t, names, "vsp-P01__sap_read", "idle backend cached tool must appear")
	assert.Contains(t, names, "vsp-P01__sap_write", "idle backend cached tool must appear")
}

// TestFilteredTools_IdleExcludedWhenFlagOff verifies that a StatusIdle backend
// does NOT appear in filteredTools when the flag is OFF (behavior-neutral).
func TestFilteredTools_IdleExcludedWhenFlagOff(t *testing.T) {
	t.Setenv("MCP_GATEWAY_LAZY_SPAWN", "") // OFF

	path := t.TempDir() + "/tool-manifest.json"
	// Even if there is a manifest file on disk, it should not be consulted.
	m, err := lifecycle.LoadManifest(path)
	require.NoError(t, err)
	// Put is a no-op when flag OFF, but we still wire the manifest to the
	// gateway to test that filteredTools ignores Get() returning false.
	m.Put("vsp-P01", vspP01Sig(), []models.ToolInfo{
		{Name: "sap_read", Description: "Read SAP table", Server: "vsp-P01"},
	})

	gw, _ := buildIdleTestGateway(t, m)

	tools := gw.filteredTools()

	names := toolNames(tools)
	assert.Contains(t, names, "core__core_tool", "running backend must still appear")
	// Idle backend must NOT appear when flag is OFF.
	for _, n := range names {
		assert.NotContains(t, n, "vsp-P01",
			"idle backend must not appear in filteredTools when flag is OFF")
	}
}

// TestFilteredTools_IdleSkippedWhenNoManifest verifies that a StatusIdle backend
// is skipped when no manifest is wired (nil) even if the flag is ON.
func TestFilteredTools_IdleSkippedWhenNoManifest(t *testing.T) {
	t.Setenv("MCP_GATEWAY_LAZY_SPAWN", "1")

	// Pass nil manifest — simulates flag ON but manifest not yet loaded.
	gw, _ := buildIdleTestGateway(t, nil)

	tools := gw.filteredTools()

	names := toolNames(tools)
	assert.Contains(t, names, "core__core_tool")
	for _, n := range names {
		assert.NotContains(t, n, "vsp-P01",
			"idle backend must be skipped when manifest is nil")
	}
}

// TestFilteredTools_IdleSkippedWhenNoManifestEntry verifies that a StatusIdle
// backend without a manifest entry is skipped even with the flag ON.
func TestFilteredTools_IdleSkippedWhenNoManifestEntry(t *testing.T) {
	t.Setenv("MCP_GATEWAY_LAZY_SPAWN", "1")

	path := t.TempDir() + "/tool-manifest.json"
	m, err := lifecycle.LoadManifest(path)
	require.NoError(t, err)
	// No entry for vsp-P01 in the manifest.

	gw, _ := buildIdleTestGateway(t, m)

	tools := gw.filteredTools()

	names := toolNames(tools)
	assert.Contains(t, names, "core__core_tool")
	for _, n := range names {
		assert.NotContains(t, n, "vsp-P01",
			"idle backend without manifest entry must be skipped")
	}
}

// toolNames extracts the namespaced tool names from a filteredTools result.
func toolNames(tools []namespacedTool) []string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.namespaced
	}
	return names
}

// ----- Fix 8: IsLazyPending + over-budget consolidation tests -----------------

// buildIdleTestGatewayWithPending creates a Gateway where "vsp-P01" has
// StatusStarting + IsLazyPending==true (mid-spawn race window).
// Uses the same vspP01Config as buildIdleTestGateway so manifest sigs match.
func buildIdleTestGatewayWithPending(t *testing.T, manifest *lifecycle.Manifest) (*Gateway, *lifecycle.Manager) {
	t.Helper()
	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"core":    {URL: "http://127.0.0.1:19999"},
			"vsp-P01": &vspP01Config,
		},
	}
	cfg.ApplyDefaults()
	logger := slog.Default()
	lm := lifecycle.NewManager(cfg, "test", logger)

	lm.SetStatus("core", models.StatusRunning, "")
	lm.SetTools("core", []models.ToolInfo{
		{Name: "core_tool", Description: "Core tool", Server: "core"},
	})
	// vsp-P01 is mid-spawn: StatusStarting (not Idle, not Running) with IsLazyPending.
	lm.SetStatus("vsp-P01", models.StatusStarting, "")

	gw := New(cfg, lm, "test", logger)
	if manifest != nil {
		gw.SetManifest(manifest)
	}
	return gw, lm
}

// TestFilteredTools_LazyPendingIncludesTools verifies that a backend with
// StatusStarting + IsLazyPending==true appears in filteredTools (tools from manifest).
func TestFilteredTools_LazyPendingIncludesTools(t *testing.T) {
	t.Setenv("MCP_GATEWAY_LAZY_SPAWN", "1")

	path := t.TempDir() + "/tool-manifest.json"
	m, err := lifecycle.LoadManifest(path)
	require.NoError(t, err)
	m.Put("vsp-P01", vspP01Sig(), []models.ToolInfo{
		{Name: "sap_read", Description: "Read SAP table", Server: "vsp-P01"},
	})
	require.NoError(t, m.Persist())

	gw, lm := buildIdleTestGatewayWithPending(t, m)
	// Mark vsp-P01 as lazy-pending so IsLazyPending returns true.
	lm.SetLazyPendingForTest("vsp-P01")
	defer lm.ClearLazyPendingForTest("vsp-P01")

	tools := gw.filteredTools()
	names := toolNames(tools)

	assert.Contains(t, names, "vsp-P01__sap_read",
		"lazy-pending backend tools must appear in filteredTools during mid-spawn window")
}

// TestFilteredTools_StartingNotPendingExcluded verifies that StatusStarting with
// IsLazyPending==false (not a lazy spawn) does NOT appear in filteredTools.
func TestFilteredTools_StartingNotPendingExcluded(t *testing.T) {
	t.Setenv("MCP_GATEWAY_LAZY_SPAWN", "1")

	path := t.TempDir() + "/tool-manifest.json"
	m, err := lifecycle.LoadManifest(path)
	require.NoError(t, err)
	m.Put("vsp-P01", vspP01Sig(), []models.ToolInfo{
		{Name: "sap_read", Description: "Read SAP table", Server: "vsp-P01"},
	})
	require.NoError(t, m.Persist())

	// IsLazyPending is false for vsp-P01 (not set).
	gw, _ := buildIdleTestGatewayWithPending(t, m)

	tools := gw.filteredTools()
	names := toolNames(tools)

	for _, n := range names {
		assert.NotContains(t, n, "vsp-P01",
			"StatusStarting without IsLazyPending must NOT appear in filteredTools")
	}
}

// TestFilteredTools_IdleOverBudgetConsolidated verifies that an Idle backend with
// more manifest tools than PerServerBudget emits a synthetic more_tools entry
// instead of silently dropping the excess (Fix 2).
func TestFilteredTools_IdleOverBudgetConsolidated(t *testing.T) {
	t.Setenv("MCP_GATEWAY_LAZY_SPAWN", "1")

	path := t.TempDir() + "/tool-manifest.json"
	m, err := lifecycle.LoadManifest(path)
	require.NoError(t, err)
	// Put 3 tools; budget will be 2, so 1 excess tool must be folded.
	// The sig must match BackendConfigSig for the config used below.
	overBudgetCfg := models.ServerConfig{Command: "/usr/bin/vsp"}
	m.Put("vsp-P01", lifecycle.BackendConfigSig(overBudgetCfg), []models.ToolInfo{
		{Name: "sap_read", Description: "Read", Server: "vsp-P01"},
		{Name: "sap_write", Description: "Write", Server: "vsp-P01"},
		{Name: "sap_delete", Description: "Delete", Server: "vsp-P01"},
	})
	require.NoError(t, m.Persist())

	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"vsp-P01": &overBudgetCfg,
		},
		Gateway: models.GatewaySettings{
			ToolFilter: &models.ToolFilter{
				PerServerBudget:   2,
				ConsolidateExcess: true,
			},
		},
	}
	cfg.ApplyDefaults()
	lm := lifecycle.NewManager(cfg, "test", slog.Default())
	lm.SetStatus("vsp-P01", models.StatusIdle, models.StatusIdleReason)

	gw := New(cfg, lm, "test", slog.Default())
	gw.SetManifest(m)

	tools := gw.filteredTools()
	names := toolNames(tools)

	// The two within-budget tools must appear directly.
	assert.Contains(t, names, "vsp-P01__sap_read", "first within-budget tool must appear")
	assert.Contains(t, names, "vsp-P01__sap_write", "second within-budget tool must appear")

	// The excess tool must be folded into a more_tools meta-tool.
	assert.Contains(t, names, "vsp-P01__more_tools",
		"excess tool must be folded into more_tools meta-tool, not silently dropped")

	// Verify the meta-tool carries the excess tool in allowedTools.
	for _, tool := range tools {
		if tool.namespaced == "vsp-P01__more_tools" {
			assert.Contains(t, tool.allowedTools, "sap_delete",
				"more_tools allowedTools must include the excess tool")
		}
	}
}
