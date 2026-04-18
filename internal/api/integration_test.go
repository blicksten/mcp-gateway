package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"mcp-gateway/internal/config"
	"mcp-gateway/internal/health"
	"mcp-gateway/internal/lifecycle"
	"mcp-gateway/internal/models"
	"mcp-gateway/internal/proxy"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func buildMockServer(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	binary := filepath.Join(tmpDir, "mock-server")
	if os.PathSeparator == '\\' {
		binary += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", binary, "mcp-gateway/internal/testutil")
	cmd.Dir = filepath.Join("..", "..") // module root
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "Failed to build mock server: %s", string(out))
	return binary
}

// TestIntegration_FullCycle tests the complete gateway flow:
// Start backends → REST API queries → tool call → shutdown.
func TestIntegration_FullCycle(t *testing.T) {
	binary := buildMockServer(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	expose := false
	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"backend1": {Command: binary},
			"backend2": {Command: binary},
			"hidden":   {Command: binary, ExposeTools: &expose},
		},
	}
	cfg.ApplyDefaults()

	lm := lifecycle.NewManager(cfg, "test", logger)
	gw := proxy.New(cfg, lm, "test", logger)
	monitor := health.NewMonitor(lm, 30*time.Second, logger)
	srv := NewServer(lm, gw, monitor, cfg, "", logger, AuthConfig{}, "test")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Start all backends.
	require.NoError(t, lm.StartAll(ctx))
	defer lm.StopAll(context.Background())

	// Rebuild tools after backends are up.
	gw.RebuildTools()

	handler := srv.Handler()

	// --- Test 1: Health endpoint ---
	rr := doRequest(t, handler, "GET", "/api/v1/health", nil)
	assert.Equal(t, http.StatusOK, rr.Code)
	var healthResp map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &healthResp))
	assert.Equal(t, float64(3), healthResp["servers"])
	assert.Equal(t, float64(3), healthResp["running"])

	// --- Test 2: List servers ---
	rr = doRequest(t, handler, "GET", "/api/v1/servers", nil)
	assert.Equal(t, http.StatusOK, rr.Code)
	var servers []ServerView
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &servers))
	assert.Len(t, servers, 3)
	for _, s := range servers {
		assert.Equal(t, models.StatusRunning, s.Status)
		assert.Equal(t, "stdio", s.Transport)
	}

	// --- Test 3: List tools (hidden server excluded) ---
	rr = doRequest(t, handler, "GET", "/api/v1/tools", nil)
	assert.Equal(t, http.StatusOK, rr.Code)
	var tools []models.ToolInfo
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &tools))

	// backend1 + backend2 expose tools, hidden does not.
	serverNames := make(map[string]bool)
	for _, tool := range tools {
		serverNames[tool.Server] = true
	}
	assert.True(t, serverNames["backend1"], "backend1 tools should be visible")
	assert.True(t, serverNames["backend2"], "backend2 tools should be visible")
	assert.False(t, serverNames["hidden"], "hidden tools should NOT be visible")
	t.Logf("Visible tools: %d from servers %v", len(tools), serverNames)

	// Each backend has 3 tools (echo, add, fail) → 6 total visible.
	assert.Equal(t, 6, len(tools))

	// --- Test 4: Direct call to hidden server (bypasses filtering) ---
	rr = doRequest(t, handler, "POST", "/api/v1/servers/hidden/call", map[string]any{
		"tool":      "echo",
		"arguments": map[string]any{"message": "hidden access"},
	})
	assert.Equal(t, http.StatusOK, rr.Code)
	t.Logf("Direct call to hidden server: %s", rr.Body.String())

	// --- Test 5: Get specific server ---
	rr = doRequest(t, handler, "GET", "/api/v1/servers/backend1", nil)
	assert.Equal(t, http.StatusOK, rr.Code)
	var sv ServerView
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &sv))
	assert.Equal(t, "backend1", sv.Name)
	assert.Equal(t, models.StatusRunning, sv.Status)
	assert.Greater(t, sv.PID, 0)
	assert.NotEmpty(t, sv.Tools)

	// --- Test 6: MCP endpoint responds ---
	rr = doRequest(t, handler, "POST", "/mcp", map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-03-26",
			"capabilities":   map[string]any{},
			"clientInfo":     map[string]any{"name": "integration-test", "version": "1.0"},
		},
	})
	assert.NotEqual(t, http.StatusNotFound, rr.Code)
	t.Logf("MCP endpoint response: %d", rr.Code)
}

// TestIntegration_AddRemoveServer tests dynamic server management via REST API.
func TestIntegration_AddRemoveServer(t *testing.T) {
	binary := buildMockServer(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cfg := &models.Config{
		Servers: make(map[string]*models.ServerConfig),
	}
	cfg.ApplyDefaults()

	lm := lifecycle.NewManager(cfg, "test", logger)
	gw := proxy.New(cfg, lm, "test", logger)
	srv := NewServer(lm, gw, nil, cfg, "", logger, AuthConfig{}, "test")
	handler := srv.Handler()

	// Add a server via REST.
	rr := doRequest(t, handler, "POST", "/api/v1/servers", map[string]any{
		"name":   "dynamic",
		"config": map[string]any{"command": binary},
	})
	assert.Equal(t, http.StatusCreated, rr.Code)

	// Verify it exists.
	rr = doRequest(t, handler, "GET", "/api/v1/servers/dynamic", nil)
	assert.Equal(t, http.StatusOK, rr.Code)

	// Remove it.
	rr = doRequest(t, handler, "DELETE", "/api/v1/servers/dynamic", nil)
	assert.Equal(t, http.StatusOK, rr.Code)

	// Verify it's gone.
	rr = doRequest(t, handler, "GET", "/api/v1/servers/dynamic", nil)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

// TestIntegration_HotReload tests config file change triggers Reconcile.
func TestIntegration_HotReload(t *testing.T) {
	binary := buildMockServer(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// Start with one server.
	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"alpha": {Command: binary},
		},
	}
	cfg.ApplyDefaults()

	// Write config to a temp file for the watcher.
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")
	require.NoError(t, config.Save(configPath, cfg))

	lm := lifecycle.NewManager(cfg, "test", logger)
	gw := proxy.New(cfg, lm, "test", logger)
	srv := NewServer(lm, gw, nil, cfg, configPath, logger, AuthConfig{}, "test")
	handler := srv.Handler()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	require.NoError(t, lm.StartAll(ctx))
	defer lm.StopAll(context.Background())
	gw.RebuildTools()

	// Verify initial state: 1 server.
	rr := doRequest(t, handler, "GET", "/api/v1/servers", nil)
	assert.Equal(t, http.StatusOK, rr.Code)
	var servers []ServerView
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &servers))
	assert.Len(t, servers, 1)

	// Modify config: add a second server.
	cfg.Servers["beta"] = &models.ServerConfig{Command: binary}
	require.NoError(t, config.Save(configPath, cfg))

	// Simulate what the watcher callback does.
	newCfg, err := config.Load(configPath)
	require.NoError(t, err)
	require.NoError(t, lm.Reconcile(ctx, newCfg))
	gw.RebuildTools()

	// Verify: 2 servers now.
	rr = doRequest(t, handler, "GET", "/api/v1/servers", nil)
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &servers))
	assert.Len(t, servers, 2)

	// Modify config: remove alpha.
	delete(cfg.Servers, "alpha")
	require.NoError(t, config.Save(configPath, cfg))

	newCfg, err = config.Load(configPath)
	require.NoError(t, err)
	require.NoError(t, lm.Reconcile(ctx, newCfg))
	gw.RebuildTools()

	// Verify: only beta remains.
	rr = doRequest(t, handler, "GET", "/api/v1/servers", nil)
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &servers))
	assert.Len(t, servers, 1)
	assert.Equal(t, "beta", servers[0].Name)
	t.Log("Hot-reload: add and remove verified")
}

// TestIntegration_GracefulShutdown tests that StopAll cleanly stops all backends.
func TestIntegration_GracefulShutdown(t *testing.T) {
	binary := buildMockServer(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"srv1": {Command: binary},
			"srv2": {Command: binary},
			"srv3": {Command: binary},
		},
	}
	cfg.ApplyDefaults()

	lm := lifecycle.NewManager(cfg, "test", logger)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	require.NoError(t, lm.StartAll(ctx))

	// Collect PIDs while running.
	entries := lm.Entries()
	pids := make([]int, 0, len(entries))
	for _, e := range entries {
		assert.Equal(t, models.StatusRunning, e.Status)
		assert.Greater(t, e.PID, 0)
		pids = append(pids, e.PID)
	}
	t.Logf("Running PIDs: %v", pids)

	// Graceful shutdown.
	lm.StopAll(context.Background())

	// Verify all stopped.
	for _, e := range lm.Entries() {
		assert.Equal(t, models.StatusStopped, e.Status, "server %s should be stopped", e.Name)
		assert.Equal(t, 0, e.PID, "server %s PID should be 0", e.Name)
	}

	// Verify processes are actually dead.
	for _, pid := range pids {
		proc, err := os.FindProcess(pid)
		if err != nil {
			continue // process not found = dead
		}
		// On Unix, FindProcess always succeeds. Send signal 0 to check liveness.
		// On Windows, FindProcess fails if the process is gone.
		if proc != nil {
			// Best-effort check: try to signal the process.
			// If it's dead, this returns an error (platform-dependent).
			err := proc.Signal(os.Signal(syscall.Signal(0)))
			if err == nil {
				t.Errorf("PID %d still alive after StopAll", pid)
			}
		}
	}
	t.Log("Graceful shutdown: all processes stopped")
}

// doRequest is defined in server_test.go (same package).
