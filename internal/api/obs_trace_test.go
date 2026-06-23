package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcp-gateway/internal/health"
	"mcp-gateway/internal/lifecycle"
	"mcp-gateway/internal/models"
	"mcp-gateway/internal/obs"
	"mcp-gateway/internal/proxy"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readEventLines returns the non-empty JSONL lines written by an enabled
// emitter under <configDir>/events/.
func readEventLines(t *testing.T, configDir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(configDir, "events", "*.jsonl"))
	require.NoError(t, err)
	var lines []string
	for _, p := range matches {
		b, err := os.ReadFile(p)
		require.NoError(t, err)
		for _, ln := range strings.Split(string(b), "\n") {
			if strings.TrimSpace(ln) != "" {
				lines = append(lines, ln)
			}
		}
	}
	return lines
}

// TestHandleCallTool_EmitsProxyCallWithTraceID is the gateway end of the trace
// propagation contract (PLAN §B.5 / item 2 + 3): a POST /servers/{name}/call
// carrying X-Trace-Id must (a) reach the backend and (b) emit a proxy.call
// event whose trace_id matches the inbound header, with tool + duration_ms +
// ok recorded. A long-duration proxy.call sharing a trace_id with a PAL
// timeout is exactly how F3 becomes attributable.
func TestHandleCallTool_EmitsProxyCallWithTraceID(t *testing.T) {
	binary := buildMockServer(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"backend1": {Command: binary},
		},
	}
	cfg.ApplyDefaults()

	lm := lifecycle.NewManager(cfg, "test", logger)
	gw := proxy.New(cfg, lm, "test", logger)
	monitor := health.NewMonitor(lm, 30*time.Second, logger)
	srv := NewServer(lm, gw, monitor, cfg, "", logger, AuthConfig{}, "test")

	// Enable + wire the emitter.
	t.Setenv("MCP_GATEWAY_TRACE", "1")
	configDir := t.TempDir()
	emitter := obs.NewEmitter(configDir, logger)
	require.True(t, emitter.Enabled())
	srv.SetEmitter(emitter)
	defer func() { _ = emitter.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	require.NoError(t, lm.StartAll(ctx))
	defer lm.StopAll(context.Background())
	gw.RebuildTools()

	handler := srv.Handler()

	const traceID = "tr-deadbeef"
	body, err := json.Marshal(map[string]any{
		"tool":      "echo",
		"arguments": map[string]any{"message": "hi"},
	})
	require.NoError(t, err)
	req := httptest.NewRequest("POST", "/api/v1/servers/backend1/call", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Trace-Id", traceID)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())

	require.NoError(t, emitter.Close()) // flush before reading
	lines := readEventLines(t, configDir)

	var proxyCall string
	for _, ln := range lines {
		if strings.Contains(ln, `"event":"proxy.call"`) {
			proxyCall = ln
			break
		}
	}
	require.NotEmpty(t, proxyCall, "expected a proxy.call event; lines=%v", lines)
	assert.Contains(t, proxyCall, `"trace_id":"`+traceID+`"`)
	assert.Contains(t, proxyCall, `"target":"backend1"`)
	assert.Contains(t, proxyCall, `"tool":"echo"`)
	assert.Contains(t, proxyCall, `"duration_ms":`)
	assert.Contains(t, proxyCall, `"ok":true`)
}

// TestHandleCallTool_NilEmitterNoOp asserts the call path is unaffected when no
// emitter is wired (default for legacy callers / tests) — nil-safe, no panic,
// and the tool call still succeeds.
func TestHandleCallTool_NilEmitterNoOp(t *testing.T) {
	binary := buildMockServer(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"backend1": {Command: binary},
		},
	}
	cfg.ApplyDefaults()

	lm := lifecycle.NewManager(cfg, "test", logger)
	gw := proxy.New(cfg, lm, "test", logger)
	monitor := health.NewMonitor(lm, 30*time.Second, logger)
	srv := NewServer(lm, gw, monitor, cfg, "", logger, AuthConfig{}, "test")
	// No SetEmitter — s.emitter stays nil.

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	require.NoError(t, lm.StartAll(ctx))
	defer lm.StopAll(context.Background())
	gw.RebuildTools()

	handler := srv.Handler()
	body, err := json.Marshal(map[string]any{
		"tool":      "echo",
		"arguments": map[string]any{"message": "hi"},
	})
	require.NoError(t, err)
	req := httptest.NewRequest("POST", "/api/v1/servers/backend1/call", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())
}
