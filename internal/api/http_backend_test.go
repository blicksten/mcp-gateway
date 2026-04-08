package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"mcp-gateway/internal/health"
	"mcp-gateway/internal/lifecycle"
	"mcp-gateway/internal/models"
	"mcp-gateway/internal/proxy"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startHTTPMockForAPI builds and spawns the mock server over HTTP/SSE,
// waits for readiness, and registers cleanup via t.Cleanup.
func startHTTPMockForAPI(t *testing.T, transport string) string {
	t.Helper()

	binary := buildMockServer(t)
	cmd := exec.Command(binary, "--transport="+transport, "--port=0")
	cmd.Stderr = os.Stderr

	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())

	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = stdout.Close()
		_ = cmd.Wait()
	})

	portCh := make(chan int, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if portStr, ok := strings.CutPrefix(scanner.Text(), "READY port="); ok {
				if p, err := strconv.Atoi(portStr); err == nil {
					portCh <- p
					return
				}
			}
		}
		_, _ = io.Copy(io.Discard, stdout)
	}()

	var port int
	select {
	case port = <-portCh:
	case <-time.After(30 * time.Second):
		t.Fatal("mock server did not print READY within 30s")
	}

	suffix := "/mcp"
	if transport == "sse" {
		suffix = "/sse"
	}
	return fmt.Sprintf("http://127.0.0.1:%d%s", port, suffix)
}

// TestIntegration_HTTPBackendFullCycle tests the full gateway flow with HTTP backends:
// start HTTP backend -> REST API queries -> tool call -> shutdown.
func TestIntegration_HTTPBackendFullCycle(t *testing.T) {
	mockURL := startHTTPMockForAPI(t, "http")
	binary := buildMockServer(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"stdio-srv": {Command: binary},
			"http-srv":  {URL: mockURL},
		},
	}
	cfg.ApplyDefaults()

	lm := lifecycle.NewManager(cfg, "test", logger)
	gw := proxy.New(cfg, lm, "test", logger)
	monitor := health.NewMonitor(lm, 30*time.Second, logger)
	srv := NewServer(lm, gw, monitor, cfg, "", logger)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	require.NoError(t, lm.StartAll(ctx))
	defer lm.StopAll(context.Background())

	gw.RebuildTools()
	handler := srv.Handler()

	// --- Test 1: List servers shows both transports ---
	rr := doRequest(t, handler, "GET", "/api/v1/servers", nil)
	assert.Equal(t, http.StatusOK, rr.Code)
	var servers []ServerView
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &servers))
	assert.Len(t, servers, 2)

	transportMap := make(map[string]string)
	for _, s := range servers {
		transportMap[s.Name] = s.Transport
		assert.Equal(t, models.StatusRunning, s.Status)
	}
	assert.Equal(t, "stdio", transportMap["stdio-srv"])
	assert.Equal(t, "http", transportMap["http-srv"])
	t.Logf("Servers: %v", transportMap)

	// --- Test 2: Tools from both backends visible ---
	rr = doRequest(t, handler, "GET", "/api/v1/tools", nil)
	assert.Equal(t, http.StatusOK, rr.Code)
	var tools []models.ToolInfo
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &tools))

	serverTools := make(map[string]int)
	for _, tool := range tools {
		serverTools[tool.Server]++
	}
	assert.Equal(t, 3, serverTools["stdio-srv"], "stdio-srv should have 3 tools")
	assert.Equal(t, 3, serverTools["http-srv"], "http-srv should have 3 tools")
	t.Logf("Tools per server: %v", serverTools)

	// --- Test 3: Direct tool call to HTTP backend ---
	rr = doRequest(t, handler, "POST", "/api/v1/servers/http-srv/call", map[string]any{
		"tool":      "echo",
		"arguments": map[string]any{"message": "hello-from-http"},
	})
	assert.Equal(t, http.StatusOK, rr.Code)
	var callResp map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &callResp))
	assert.NotNil(t, callResp["content"], "tool call response should have content")
	t.Logf("HTTP backend tool call response: %s", rr.Body.String())

	// --- Test 4: Get specific HTTP server ---
	rr = doRequest(t, handler, "GET", "/api/v1/servers/http-srv", nil)
	assert.Equal(t, http.StatusOK, rr.Code)
	var sv ServerView
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &sv))
	assert.Equal(t, "http-srv", sv.Name)
	assert.Equal(t, models.StatusRunning, sv.Status)
	assert.Equal(t, "http", sv.Transport)
	assert.Equal(t, 0, sv.PID, "HTTP backend should have no PID")
	assert.NotEmpty(t, sv.Tools)

	// --- Test 5: Health endpoint counts HTTP backend ---
	rr = doRequest(t, handler, "GET", "/api/v1/health", nil)
	assert.Equal(t, http.StatusOK, rr.Code)
	var healthResp map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &healthResp))
	assert.Equal(t, float64(2), healthResp["servers"])
	assert.Equal(t, float64(2), healthResp["running"])
}

// TestIntegration_AddHTTPServerViaREST tests dynamically adding an HTTP backend via REST API.
func TestIntegration_AddHTTPServerViaREST(t *testing.T) {
	mockURL := startHTTPMockForAPI(t, "http")
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cfg := &models.Config{
		Servers: make(map[string]*models.ServerConfig),
	}
	cfg.ApplyDefaults()

	lm := lifecycle.NewManager(cfg, "test", logger)
	gw := proxy.New(cfg, lm, "test", logger)
	srv := NewServer(lm, gw, nil, cfg, "", logger)
	handler := srv.Handler()

	// Add HTTP server via REST.
	rr := doRequest(t, handler, "POST", "/api/v1/servers", map[string]any{
		"name":   "dynamic-http",
		"config": map[string]any{"url": mockURL},
	})
	assert.Equal(t, http.StatusCreated, rr.Code)

	// Verify it exists and is running (auto-started).
	rr = doRequest(t, handler, "GET", "/api/v1/servers/dynamic-http", nil)
	assert.Equal(t, http.StatusOK, rr.Code)
	var sv ServerView
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &sv))
	assert.Equal(t, models.StatusRunning, sv.Status)
	assert.Equal(t, "http", sv.Transport)
	t.Logf("Dynamic HTTP server added and running, tools: %d", len(sv.Tools))

	// Remove it.
	rr = doRequest(t, handler, "DELETE", "/api/v1/servers/dynamic-http", nil)
	assert.Equal(t, http.StatusOK, rr.Code)

	// Verify gone.
	rr = doRequest(t, handler, "GET", "/api/v1/servers/dynamic-http", nil)
	assert.Equal(t, http.StatusNotFound, rr.Code)

	// Verify tools cleaned up after removal.
	rr = doRequest(t, handler, "GET", "/api/v1/tools", nil)
	assert.Equal(t, http.StatusOK, rr.Code)
	var tools []models.ToolInfo
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &tools))
	for _, tool := range tools {
		assert.NotEqual(t, "dynamic-http", tool.Server, "removed server's tools should not appear")
	}
}
