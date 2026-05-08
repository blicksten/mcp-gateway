package api

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"mcp-gateway/internal/models"
	"mcp-gateway/internal/patchstate"
	"mcp-gateway/internal/plugin"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTriggerPluginReannounce_BumpsMtimeAndEnqueuesReconnect is the
// MCPR.4 integration test: validates the daemon-startup two-layer
// recovery path works end-to-end against a real Regenerator + real
// patchState (no mocks). Real-boundary evidence per Invariant 5
// (docs/spikes/2026-04-27-invariants-for-plan-rewrite.md):
//   - actual fs operations on a temp directory
//   - actual patchstate.State (in-memory persist for test isolation)
//   - asserts both observable side effects fire on a steady-state
//     respawn (existing identical .mcp.json):
//     1. .mcp.json mtime advances (L2 fs-watcher signal)
//     2. exactly one reconnect action enqueued (L1 patch flow)
//
// The failure mode this catches: "daemon respawn does not trigger
// Claude Code plugin re-evaluation when .mcp.json content is
// unchanged." If TriggerPluginReannounce regresses to plain
// TriggerPluginRegen, assertion (1) fails. If TriggerPluginRegen
// itself regresses (loses its EnqueueReconnectAction call), assertion
// (2) fails.
func TestTriggerPluginReannounce_BumpsMtimeAndEnqueuesReconnect(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Wire pluginDir + Regenerator so TriggerPluginReannounce is not a
	// no-op early-return. Use a temp dir; Regenerate will write the
	// initial .mcp.json there, then the second call (the unit-under-test)
	// hits the idempotent no-op path because content is unchanged.
	pluginDir := t.TempDir()
	regen := plugin.NewRegenerator()
	srv.SetPluginRegen(pluginDir, regen)

	// One backend so Regenerate produces non-trivial content. Disabled
	// stays false so the entry survives buildMCPJSON's filter.
	srv.cfg.Servers = map[string]*models.ServerConfig{
		"alpha": {URL: "http://127.0.0.1:1/"},
	}

	// First Regenerate writes the initial .mcp.json. Bypass the public
	// API (TriggerPluginRegen) for this fixture step so we can isolate
	// the patchState side-effect to the unit-under-test below; Regenerate
	// directly is a pure file-write with no patchState interaction.
	err := regen.Regenerate(pluginDir, srv.cfg.Servers, plugin.DefaultGatewayURLPlaceholder)
	require.NoError(t, err, "fixture: initial Regenerate")

	target := filepath.Join(pluginDir, plugin.MCPJSONFileName)
	infoBefore, err := os.Stat(target)
	require.NoError(t, err, "fixture: stat .mcp.json before reannounce")

	// Wire patchState — empty persist path → in-memory only, no disk I/O.
	ps := patchstate.New("", testLogger())
	srv.SetPatchState(ps)

	// Sanity: no actions enqueued yet (clean fixture state).
	preActions := ps.PendingActions("")
	require.Empty(t, preActions, "fixture: patchState should start empty")

	// Sleep past filesystem mtime granularity so the touch is observable.
	// 50ms is comfortably above sub-second resolution on NTFS/ReFS/ext4
	// in normal test environments.
	time.Sleep(50 * time.Millisecond)

	// === ACT: simulate daemon-startup steady-state respawn path ===
	srv.TriggerPluginReannounce()

	// === ASSERT L2 (fs-watcher signal): mtime advanced ===
	infoAfter, err := os.Stat(target)
	require.NoError(t, err, "stat .mcp.json after reannounce")
	assert.True(t, infoAfter.ModTime().After(infoBefore.ModTime()),
		"TriggerPluginReannounce did not advance .mcp.json mtime: before=%v after=%v",
		infoBefore.ModTime(), infoAfter.ModTime())

	// Content must NOT have changed (we asserted the idempotent regen
	// branch was hit — Regenerate skipped the rewrite). A divergence
	// would mean either the cfg snapshot was mutated OR Regenerate's
	// idempotency contract regressed.
	bodyBefore, err := os.ReadFile(target)
	require.NoError(t, err)
	bodyAfter, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, bodyBefore, bodyAfter,
		"content changed unexpectedly — expected idempotent no-op regen, got rewrite")

	// === ASSERT L1 (patch flow): exactly one reconnect action enqueued ===
	postActions := ps.PendingActions("")
	require.Len(t, postActions, 1, "expected exactly one reconnect action enqueued by TriggerPluginRegen path")
	assert.Equal(t, "reconnect", postActions[0].Type,
		"action type should be 'reconnect' (P4-08: AggregatePluginServerName)")
	assert.Equal(t, patchstate.AggregatePluginServerName, postActions[0].ServerName,
		"P4-08 invariant: serverName always AggregatePluginServerName")
}

// TestTriggerPluginReannounce_NoOpWhenPluginDirEmpty asserts the same
// guard as TriggerPluginRegen — when the daemon was started without a
// plugin directory (e.g., test fixtures or a non-Claude-Code deployment)
// TriggerPluginReannounce returns silently rather than touching some
// unrelated path.
func TestTriggerPluginReannounce_NoOpWhenPluginDirEmpty(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Wire patchState but NOT pluginRegen / pluginDir.
	ps := patchstate.New("", testLogger())
	srv.SetPatchState(ps)

	// Should not panic, should not enqueue any action.
	srv.TriggerPluginReannounce()

	actions := ps.PendingActions("")
	assert.Empty(t, actions,
		"TriggerPluginReannounce should be a no-op when pluginDir is empty — got %d actions", len(actions))
}
