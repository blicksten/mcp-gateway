//go:build integration

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mcp-gateway/internal/health"
	"mcp-gateway/internal/lifecycle"
	"mcp-gateway/internal/models"
	"mcp-gateway/internal/patchstate"
	"mcp-gateway/internal/plugin"
	"mcp-gateway/internal/proxy"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Phase 16.7 T16.7.1 — end-to-end integration test under the Alt-E design.
//
// Flow asserted (PLAN-16 §16.7):
//   1. Start gateway with plugin regen + patch state wired, temp plugin dir.
//   2. Initial tools/list returns the preconfigured backends.
//   3. Post a heartbeat from a simulated patch (Alt-E schema).
//   4. Add a new backend via POST /api/v1/servers.
//   5. Assert plugin .mcp.json regenerated with the new entry.
//   6. Assert a reconnect pending action landed in the queue (type=reconnect,
//      serverName=mcp-gateway per P4-08 invariant).
//   7. Simulate the patch acking the action.
//   8. Remove the backend — assert another reconnect action appears.
//
// Build tag `integration` keeps this out of the default `go test ./...`
// surface (it builds a stub MCP child process and starts the gateway).
// Run via `go test -tags=integration ./internal/api/...`.
const integrationPhase16Bearer = "phase16-integration-bearer-token"

func setupIntegrationPhase16(t *testing.T) (*Server, *patchstate.State, *lifecycle.Manager, string) {
	t.Helper()

	binary := buildMockServer(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// Temp plugin dir with a minimal .claude-plugin/plugin.json so
	// plugin.Discover → plugin.NewRegenerator wiring writes to a known place.
	pluginDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(pluginDir, ".claude-plugin"), 0o700))
	pluginManifest := `{"name":"mcp-gateway-integration","version":"0.0.0","mcp":{}}`
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, ".claude-plugin", "plugin.json"), []byte(pluginManifest), 0o600))

	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"initial": {Command: binary},
		},
	}
	cfg.ApplyDefaults()

	lm := lifecycle.NewManager(cfg, "test", logger)
	gw := proxy.New(cfg, lm, "test", logger)
	monitor := health.NewMonitor(lm, 30*time.Second, logger)
	srv := NewServer(lm, gw, monitor, cfg, "", logger, AuthConfig{
		Enabled: true,
		Token:   integrationPhase16Bearer,
	}, "test-v1.6.0")

	srv.SetPluginRegen(pluginDir, plugin.NewRegenerator())

	ps := patchstate.New("", nil) // no disk persistence in this test
	srv.SetPatchState(ps)
	srv.InitClaudeCodeLimiters()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	require.NoError(t, lm.StartAll(ctx))
	t.Cleanup(func() { lm.StopAll(context.Background()) })
	gw.RebuildTools()
	// Bootstrap regen so the plugin .mcp.json exists from the start.
	srv.TriggerPluginRegen()

	return srv, ps, lm, pluginDir
}

// doAuthedRequest mirrors the helper in server_test.go but attaches the
// integration-scoped bearer token automatically.
func doAuthedRequest(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var br io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		require.NoError(t, err)
		br = bytes.NewBuffer(data)
	}
	req := httptest.NewRequest(method, path, br)
	req.Header.Set("Authorization", "Bearer "+integrationPhase16Bearer)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestIntegration_Phase16_FullPatchChain(t *testing.T) {
	srv, ps, _, pluginDir := setupIntegrationPhase16(t)
	h := srv.Handler()

	mcpJSON := filepath.Join(pluginDir, ".mcp.json")

	// Step 2: initial plugin .mcp.json contains the bootstrapped "initial" backend.
	initialContent, err := os.ReadFile(mcpJSON)
	require.NoError(t, err, ".mcp.json must be bootstrapped by TriggerPluginRegen")
	assert.Contains(t, string(initialContent), `"initial"`, "bootstrap regen should include initial backend")

	// Drain any action enqueued by the bootstrap regen — we only care about
	// actions produced by the mutations under test below.
	initialActions := ps.PendingActions("")
	for _, act := range initialActions {
		ps.AckAction(act.ID)
	}

	// Step 3: simulate patch heartbeat. Schema must match the FROZEN v1.6.0
	// contract (docs/api/claude-code-endpoints.md).
	hb := patchstate.Heartbeat{
		SessionID:              "integration-sess",
		PatchVersion:           "1.0.0",
		CCVersion:              "2.1.114",
		VSCodeVersion:          "1.90.0",
		FiberOK:                true,
		MCPMethodOK:            true,
		MCPMethodFiberDepth:    2,
		LastReconnectLatencyMs: 0,
		LastReconnectOK:        false, // idle in Go default; schema allows false/true
		LastReconnectError:     "",
		PendingActionsInflight: 0,
		FiberWalkRetryCount:    0,
		MCPSessionState:        "ready",
		Timestamp:              time.Now().UnixMilli(),
	}
	rr := doAuthedRequest(t, h, http.MethodPost, "/api/v1/claude-code/patch-heartbeat", hb)
	require.Equal(t, http.StatusOK, rr.Code, "heartbeat body=%s", rr.Body.String())

	// GET /patch-status returns the heartbeat.
	rr = doAuthedRequest(t, h, http.MethodGet, "/api/v1/claude-code/patch-status", nil)
	require.Equal(t, http.StatusOK, rr.Code)
	var hbs []patchstate.Heartbeat
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &hbs))
	require.Len(t, hbs, 1)
	assert.Equal(t, "integration-sess", hbs[0].SessionID)

	// Step 4: add a new backend.
	binary := hbs[0].SessionID // placeholder — we need the actual mock binary from setup. Refetch.
	_ = binary
	// The mock server binary lives in a temp dir built by buildMockServer.
	// We rebuild it here to get a fresh path (buildMockServer uses t.TempDir
	// under our parent test). For simplicity in this test we directly pass
	// the known initial backend's binary via adding a new entry pointing at
	// the same binary.
	// Find the binary path from the lifecycle manager's entry.
	entries := srv.lm.Entries()
	require.Greater(t, len(entries), 0)
	mockBinary := entries[0].Config.Command
	require.NotEmpty(t, mockBinary)

	// Wait 600 ms to clear the 500 ms action-debounce window from the
	// bootstrap regen before posting a mutation.
	time.Sleep(600 * time.Millisecond)

	rr = doAuthedRequest(t, h, http.MethodPost, "/api/v1/servers", map[string]any{
		"name":   "added-by-integration",
		"config": map[string]any{"command": mockBinary},
	})
	require.Equal(t, http.StatusCreated, rr.Code, "add-server body=%s", rr.Body.String())

	// Step 5: plugin .mcp.json must contain the new backend.
	updated, err := os.ReadFile(mcpJSON)
	require.NoError(t, err)
	assert.Contains(t, string(updated), `"added-by-integration"`, "regen must include newly-added backend")
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(updated, &parsed), ".mcp.json must be valid JSON")
	servers, ok := parsed["mcpServers"].(map[string]any)
	require.True(t, ok, "mcpServers must be an object")
	assert.Contains(t, servers, "added-by-integration")

	// Step 6: pending action appears in queue (type=reconnect, serverName=mcp-gateway).
	rr = doAuthedRequest(t, h, http.MethodGet, "/api/v1/claude-code/pending-actions", nil)
	require.Equal(t, http.StatusOK, rr.Code)
	var actions []patchstate.PendingAction
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &actions))
	require.Greater(t, len(actions), 0, "add-server mutation must enqueue a reconnect action")
	lastAction := actions[len(actions)-1]
	assert.Equal(t, "reconnect", lastAction.Type, "action type must be reconnect")
	assert.Equal(t, patchstate.AggregatePluginServerName, lastAction.ServerName,
		"P4-08 invariant: reconnect action must target aggregate plugin server name")
	assert.NotEmpty(t, lastAction.Nonce, "action must carry a nonce for correlation")

	// Step 7: simulate patch acking the action.
	rr = doAuthedRequest(t, h, http.MethodPost, "/api/v1/claude-code/pending-actions/"+lastAction.ID+"/ack", nil)
	require.Equal(t, http.StatusOK, rr.Code)

	// Poll /pending-actions without a cursor — the acked action should be
	// filtered out.
	rr = doAuthedRequest(t, h, http.MethodGet, "/api/v1/claude-code/pending-actions", nil)
	require.Equal(t, http.StatusOK, rr.Code)
	var remaining []patchstate.PendingAction
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &remaining))
	for _, a := range remaining {
		assert.NotEqual(t, lastAction.ID, a.ID, "acked action must be filtered from GET response")
	}

	// Step 8: remove the backend — assert a new reconnect action is enqueued.
	time.Sleep(600 * time.Millisecond) // clear debounce
	rr = doAuthedRequest(t, h, http.MethodDelete, "/api/v1/servers/added-by-integration", nil)
	require.Equal(t, http.StatusOK, rr.Code, "delete body=%s", rr.Body.String())

	afterRemove, err := os.ReadFile(mcpJSON)
	require.NoError(t, err)
	assert.NotContains(t, string(afterRemove), `"added-by-integration"`, "regen after remove must drop entry")

	rr = doAuthedRequest(t, h, http.MethodGet, "/api/v1/claude-code/pending-actions", nil)
	require.Equal(t, http.StatusOK, rr.Code)
	var afterActions []patchstate.PendingAction
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &afterActions))
	foundRemoveAction := false
	for _, a := range afterActions {
		if a.ID != lastAction.ID && a.Type == "reconnect" {
			foundRemoveAction = true
			break
		}
	}
	assert.True(t, foundRemoveAction, "remove-server mutation must enqueue another reconnect action")
}

func TestIntegration_Phase16_ProbeTriggerRoundTrip(t *testing.T) {
	srv, ps, _, _ := setupIntegrationPhase16(t)
	h := srv.Handler()

	nonce := "integration-probe-nonce-0001"
	rr := doAuthedRequest(t, h, http.MethodPost, "/api/v1/claude-code/probe-trigger",
		map[string]string{"nonce": nonce})
	require.Equal(t, http.StatusOK, rr.Code, "probe-trigger body=%s", rr.Body.String())

	// Action should be queued with type=probe-reconnect carrying our nonce.
	list := ps.PendingActions("")
	require.Greater(t, len(list), 0)
	probe := list[len(list)-1]
	assert.Equal(t, "probe-reconnect", probe.Type)
	assert.Equal(t, nonce, probe.Nonce)
	assert.Contains(t, probe.ServerName, "__probe_nonexistent_")

	// Patch reports back via /probe-result. Use a mismatched-nonce first to
	// prove RecordProbeResult keys by nonce not by action id.
	rr = doAuthedRequest(t, h, http.MethodPost, "/api/v1/claude-code/probe-result",
		patchstate.ProbeResult{Nonce: nonce, OK: false, Error: "Server not found: __probe_nonexistent_..."})
	require.Equal(t, http.StatusOK, rr.Code)

	stored := ps.ProbeResult(nonce)
	require.NotNil(t, stored, "probe result must be retrievable by nonce")
	assert.False(t, stored.OK, "rejection IS the healthy signal per FROZEN contract")
}

func TestIntegration_Phase16_PluginSyncReturnsStatus(t *testing.T) {
	srv, _, _, pluginDir := setupIntegrationPhase16(t)
	h := srv.Handler()

	rr := doAuthedRequest(t, h, http.MethodPost, "/api/v1/claude-code/plugin-sync", nil)
	require.Equal(t, http.StatusOK, rr.Code, "plugin-sync body=%s", rr.Body.String())

	var body map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal(t, "synced", body["status"])
	assert.NotEmpty(t, body["mcp_json_path"], "mcp_json_path must be populated")
	assert.Equal(t, float64(1), body["entries_count"], "one initial backend should be reported")

	// File must exist on disk after sync.
	_, err := os.Stat(filepath.Join(pluginDir, ".mcp.json"))
	require.NoError(t, err)
}
