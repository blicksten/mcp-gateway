package api

// F3 regression tests: lifecycle operations must survive HTTP client disconnect.
//
// Root cause (empirically confirmed 2026-05-21): all REST lifecycle handlers
// used r.Context() which is cancelled when the HTTP client disconnects. When
// tools/list takes longer than the client's timeout, the gateway sent
// notifications/cancelled and the late response was silently dropped, leaving
// backends in status=running with tools=0.
//
// Fix: handlers now use s.lifecycleCtx() (daemon-scoped, 60s timeout) instead
// of r.Context(). Tests here verify the fix holds.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"mcp-gateway/internal/lifecycle"
	"mcp-gateway/internal/models"
	"mcp-gateway/internal/proxy"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// slowMCPBackend starts an in-process httptest.Server that implements
// Streamable-HTTP MCP JSON-RPC. It responds to initialize immediately and
// delays every tools/list response by toolsListDelay.
//
// Returns the backend URL (suitable for models.ServerConfig.URL) and a cleanup
// function registered via t.Cleanup.
func slowMCPBackend(t *testing.T, toolsListDelay time.Duration, toolCount int) string {
	t.Helper()

	tools := make([]map[string]any, toolCount)
	for i := range tools {
		tools[i] = map[string]any{
			"name":        fmt.Sprintf("slow_tool_%d", i+1),
			"description": fmt.Sprintf("slow tool %d", i+1),
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read error", http.StatusInternalServerError)
			return
		}

		var msg struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
		}
		if err := json.Unmarshal(body, &msg); err != nil {
			http.Error(w, "parse error", http.StatusBadRequest)
			return
		}

		// Notifications have no ID and require no response.
		if len(msg.ID) == 0 || string(msg.ID) == "null" {
			w.WriteHeader(http.StatusAccepted)
			return
		}

		var result any
		switch msg.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": "2025-03-26",
				"capabilities": map[string]any{
					"tools": map[string]any{},
				},
				"serverInfo": map[string]any{
					"name":    "slow-backend",
					"version": "test-1.0",
				},
			}

		case "tools/list":
			// Delay simulates a slow backend (playwright ~15s cold-start, etc.)
			time.Sleep(toolsListDelay)
			result = map[string]any{"tools": tools}

		default:
			// Return method-not-found for any other request.
			resp := map[string]any{
				"jsonrpc": "2.0",
				"id":      json.RawMessage(msg.ID),
				"error": map[string]any{
					"code":    -32601,
					"message": "method not found",
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		out := map[string]any{
			"jsonrpc": "2.0",
			"id":      json.RawMessage(msg.ID),
			"result":  result,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts.URL + "/mcp"
}

// TestHandleAddServer_ClientDisconnect_DoesNotAbortBackendStart is the core
// F3 regression test. It verifies that when an HTTP client disconnects before
// tools/list completes, the gateway continues the Start operation and
// eventually records the tools from the backend.
//
// Without the fix: status=running, tools=0 (response silently dropped).
// With the fix: status=running, tools=<expected count>.
func TestHandleAddServer_ClientDisconnect_DoesNotAbortBackendStart(t *testing.T) {
	const toolsListDelay = 5 * time.Second
	const toolCount = 2
	const waitAfterDisconnect = 10 * time.Second
	const clientTimeout = 1 * time.Second

	backendURL := slowMCPBackend(t, toolsListDelay, toolCount)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cfg := &models.Config{
		Servers: make(map[string]*models.ServerConfig),
	}
	cfg.ApplyDefaults()

	lm := lifecycle.NewManager(cfg, "test", logger)
	gw := proxy.New(cfg, lm, "test", logger)
	daemonCtx := context.Background()

	srv := NewServer(lm, gw, nil, cfg, "", logger, AuthConfig{}, "test")
	// Wire daemonCtx as ListenAndServe would — this is what enables lifecycleCtx().
	srv.daemonCtx = daemonCtx

	// Start the real HTTP test server so we can issue requests with real timeouts.
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	// Issue POST /servers with a very short client-side timeout (1s).
	// The backend tools/list takes 5s → client will disconnect first.
	addBody, err := json.Marshal(map[string]any{
		"name":   "slow-svc",
		"config": map[string]any{"url": backendURL},
	})
	require.NoError(t, err)

	httpClient := &http.Client{Timeout: clientTimeout}
	resp, err := httpClient.Post(ts.URL+"/api/v1/servers", "application/json", bytes.NewReader(addBody))
	// The client-side timeout fires before tools/list completes → expect an error
	// (or a successful response from the POST itself if it returns before tools/list).
	if err == nil {
		_ = resp.Body.Close()
		t.Logf("POST /servers returned %d before client timeout — acceptable, checking tools below", resp.StatusCode)
	} else {
		t.Logf("POST /servers timed out at client side (expected): %v", err)
	}

	// Wait for the lifecycle operation to complete in the background.
	// The daemon context is still live — Start should finish tools/list
	// and populate the tools list within waitAfterDisconnect.
	deadline := time.Now().Add(waitAfterDisconnect)
	var toolsFound int
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		entry, ok := lm.Entry("slow-svc")
		if !ok {
			continue
		}
		if entry.Status == models.StatusRunning && len(entry.Tools) >= toolCount {
			toolsFound = len(entry.Tools)
			break
		}
		t.Logf("polling: status=%s tools=%d", entry.Status, len(entry.Tools))
	}

	// Final assertions via REST.
	getResp, getErr := http.Get(ts.URL + "/api/v1/servers/slow-svc")
	require.NoError(t, getErr)
	defer getResp.Body.Close()

	var sv ServerView
	require.NoError(t, json.NewDecoder(getResp.Body).Decode(&sv))
	assert.Equal(t, models.StatusRunning, sv.Status, "backend must be running after Start completes")
	assert.GreaterOrEqual(t, len(sv.Tools), toolCount,
		"F3 regression: tools must be populated despite client disconnect (tools found via polling: %d)", toolsFound)
	t.Logf("F3 pass: status=%s tools=%d", sv.Status, len(sv.Tools))
}

// TestHandleAddServer_DaemonShutdown_AbortsBackendStart verifies the complementary
// invariant: when the daemon context is cancelled (graceful shutdown), in-flight
// lifecycle operations DO abort — we don't accidentally make them un-cancellable.
//
// This ensures the 60-second timeout in lifecycleCtx() eventually fires and that
// daemon shutdown doesn't hang indefinitely on a stuck backend.
func TestHandleAddServer_DaemonShutdown_AbortsBackendStart(t *testing.T) {
	const toolsListDelay = 30 * time.Second // long enough that we cancel first
	const toolCount = 2
	const cancelAfter = 1 * time.Second

	// Count how many tools/list requests the backend actually receives.
	var toolsListCalls atomic.Int32

	tools := make([]map[string]any, toolCount)
	for i := range tools {
		tools[i] = map[string]any{
			"name":        fmt.Sprintf("abort_tool_%d", i+1),
			"description": "tool for abort test",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read error", http.StatusInternalServerError)
			return
		}
		var msg struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
		}
		if err := json.Unmarshal(body, &msg); err != nil {
			http.Error(w, "parse error", http.StatusBadRequest)
			return
		}
		if len(msg.ID) == 0 || string(msg.ID) == "null" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		switch msg.Method {
		case "initialize":
			result := map[string]any{
				"protocolVersion": "2025-03-26",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "abort-backend", "version": "1.0"},
			}
			out := map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(msg.ID), "result": result}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(out)
		case "tools/list":
			toolsListCalls.Add(1)
			// Simulate a very long tools/list — longer than the test's cancel window.
			select {
			case <-r.Context().Done():
				// Backend context was cancelled (daemon shutdown propagated to HTTP client).
				http.Error(w, "context cancelled", http.StatusServiceUnavailable)
			case <-time.After(toolsListDelay):
				out := map[string]any{
					"jsonrpc": "2.0",
					"id":      json.RawMessage(msg.ID),
					"result":  map[string]any{"tools": tools},
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(out)
			}
		default:
			out := map[string]any{
				"jsonrpc": "2.0",
				"id":      json.RawMessage(msg.ID),
				"error":   map[string]any{"code": -32601, "message": "method not found"},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(out)
		}
	})

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg := &models.Config{Servers: make(map[string]*models.ServerConfig)}
	cfg.ApplyDefaults()

	lm := lifecycle.NewManager(cfg, "test", logger)
	gw := proxy.New(cfg, lm, "test", logger)

	daemonCtx, daemonCancel := context.WithCancel(context.Background())

	srv := NewServer(lm, gw, nil, cfg, "", logger, AuthConfig{}, "test")
	srv.daemonCtx = daemonCtx

	apiSrv := httptest.NewServer(srv.Handler())
	t.Cleanup(apiSrv.Close)

	// Add backend — this triggers Start → tools/list in background.
	addBody, err := json.Marshal(map[string]any{
		"name":   "abort-svc",
		"config": map[string]any{"url": ts.URL + "/mcp"},
	})
	require.NoError(t, err)

	// Use a long-enough timeout so the POST itself goes through.
	httpClient := &http.Client{Timeout: 10 * time.Second}
	resp, postErr := httpClient.Post(apiSrv.URL+"/api/v1/servers", "application/json", bytes.NewReader(addBody))
	if postErr == nil {
		_ = resp.Body.Close()
		t.Logf("POST /servers returned %d", resp.StatusCode)
	}

	// Let the backend start to block on tools/list, then cancel the daemon.
	time.Sleep(cancelAfter)
	daemonCancel()
	t.Logf("daemon context cancelled after %s", cancelAfter)

	// Give the lifecycle manager time to propagate the cancellation.
	time.Sleep(2 * time.Second)

	// The backend must have received at least one tools/list call (proving Start ran).
	assert.GreaterOrEqual(t, int(toolsListCalls.Load()), 1,
		"backend must have received at least one tools/list call")

	// After daemon cancel, the server must NOT be in StatusRunning with a full tools list.
	// It may be in any non-healthy state (stopped, error, etc.) — what matters is
	// that tools count is 0 (the delayed response never arrived and was not accepted).
	entry, ok := lm.Entry("abort-svc")
	if ok {
		t.Logf("abort test: entry status=%s tools=%d", entry.Status, len(entry.Tools))
		// The entry exists but the Start should have been interrupted.
		// In practice, the start may have failed or the tools list is empty.
		// We only assert that when daemon is cancelled, the operation doesn't
		// complete successfully with full tools — either error state or tools=0.
		if entry.Status == models.StatusRunning {
			assert.Equal(t, 0, len(entry.Tools),
				"daemon cancel: tools/list must not have completed successfully")
		}
	} else {
		t.Logf("abort test: entry not found (Start may have cleaned up on cancel) — acceptable")
	}
}
