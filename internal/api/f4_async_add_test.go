package api

// F4 tests: POST /api/v1/servers returns 202 immediately for auto-starting backends.
//
// F4 fix: addServerInProcess now launches Start in a background goroutine and
// returns nil synchronously. handleAddServer returns 202 Accepted (not 201 Created)
// when the backend will auto-start, so clients are not blocked for the full
// cold-start duration. Clients poll GET /api/v1/servers/{name} for state transitions.
//
// CONTRACT CHANGE: previously always 201. Any 2xx is a success; dashboard accepts both.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"mcp-gateway/internal/lifecycle"
	"mcp-gateway/internal/models"
	"mcp-gateway/internal/proxy"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHandleAddServer_AutoStart_Returns202Immediately verifies the F4 fix:
// a POST with auto-start enabled returns 202 in well under the backend's
// tools/list delay. The backend eventually becomes running (polled via GET).
func TestHandleAddServer_AutoStart_Returns202Immediately(t *testing.T) {
	const toolsListDelay = 3 * time.Second
	const toolCount = 2
	// The handler must respond before this deadline — far smaller than the
	// 3 s tools/list delay to confirm we are NOT blocking on Start.
	const responseDeadline = 500 * time.Millisecond
	const pollTimeout = 10 * time.Second

	backendURL := slowMCPBackend(t, toolsListDelay, toolCount)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cfg := &models.Config{Servers: make(map[string]*models.ServerConfig)}
	cfg.ApplyDefaults()
	lm := lifecycle.NewManager(cfg, "test", logger)
	gw := proxy.New(cfg, lm, "test", logger)

	srv := NewServer(lm, gw, nil, cfg, "", logger, AuthConfig{}, "test")
	srv.daemonCtx = context.Background()

	// Use a real httptest.Server so we can measure wall-clock POST latency.
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	body, err := json.Marshal(map[string]any{
		"name":   "f4-svc",
		"config": map[string]any{"url": backendURL},
	})
	require.NoError(t, err)

	start := time.Now()
	// Give HTTP client more than enough time — we are verifying the server
	// responds early, not that the client times out.
	httpClient := &http.Client{Timeout: 30 * time.Second}
	resp, err := httpClient.Post(ts.URL+"/api/v1/servers", "application/json", bytes.NewReader(body))
	elapsed := time.Since(start)
	require.NoError(t, err)
	defer resp.Body.Close()

	t.Logf("POST /servers returned %d in %s (deadline %s)", resp.StatusCode, elapsed.Round(time.Millisecond), responseDeadline)

	// F4: must get 202, not 201.
	assert.Equal(t, http.StatusAccepted, resp.StatusCode,
		"F4: auto-starting backend must return 202 Accepted")

	var respBody map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&respBody))
	assert.Equal(t, "accepted", respBody["status"])

	// Elapsed must be well under the backend's 3 s tools/list delay.
	assert.Less(t, elapsed, responseDeadline,
		"F4: POST must return before backend Start completes (elapsed=%s, deadline=%s)",
		elapsed.Round(time.Millisecond), responseDeadline)

	// Poll GET /api/v1/servers/f4-svc until the backend is running.
	deadline := time.Now().Add(pollTimeout)
	var sv ServerView
	for time.Now().Before(deadline) {
		time.Sleep(300 * time.Millisecond)
		getResp, getErr := http.Get(fmt.Sprintf("%s/api/v1/servers/f4-svc", ts.URL))
		require.NoError(t, getErr)
		_ = json.NewDecoder(getResp.Body).Decode(&sv)
		getResp.Body.Close()
		if sv.Status == models.StatusRunning && len(sv.Tools) >= toolCount {
			break
		}
		t.Logf("polling: status=%s tools=%d", sv.Status, len(sv.Tools))
	}

	assert.Equal(t, models.StatusRunning, sv.Status,
		"backend must eventually reach StatusRunning")
	assert.GreaterOrEqual(t, len(sv.Tools), toolCount,
		"F4: tools must be populated after async Start completes")
	t.Logf("F4 pass: status=%s tools=%d elapsed=%s", sv.Status, len(sv.Tools), elapsed.Round(time.Millisecond))
}

// TestHandleAddServer_DisabledConfig_StillReturns201Sync verifies that a POST
// with Disabled=true still returns 201 Created synchronously (no async work).
func TestHandleAddServer_DisabledConfig_StillReturns201Sync(t *testing.T) {
	srv, _ := setupTestServer(t)
	srv.daemonCtx = context.Background()

	body, err := json.Marshal(map[string]any{
		"name":   "f4-disabled",
		"config": map[string]any{"url": "http://localhost:9999/mcp", "disabled": true},
	})
	require.NoError(t, err)

	start := time.Now()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/servers",
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rr, req)
	elapsed := time.Since(start)

	t.Logf("POST /servers (disabled) returned %d in %s", rr.Code, elapsed.Round(time.Millisecond))

	// Disabled=true → operation is complete synchronously → 201.
	assert.Equal(t, http.StatusCreated, rr.Code,
		"disabled config must return 201 Created")

	var respBody map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&respBody))
	assert.Equal(t, "added", respBody["status"])

	// Must respond quickly — no Start is scheduled.
	assert.Less(t, elapsed, 100*time.Millisecond,
		"disabled POST must complete in <100ms (no Start scheduled)")
}

// TestServer_GracefulStopDrainsAsyncStarts verifies that when the daemon context
// is cancelled immediately after a POST /servers, ListenAndServe drains bgStartWG
// and the async goroutine finishes (either completing or being cancelled by ctx).
func TestServer_GracefulStopDrainsAsyncStarts(t *testing.T) {
	const toolsListDelay = 5 * time.Second
	const toolCount = 1
	// After daemon ctx cancel, everything should drain within this window.
	// lifecycleCtx has a 60 s budget, but the backend context is cancelled so
	// the Start call will fail much sooner.
	const drainTimeout = 10 * time.Second

	backendURL := slowMCPBackend(t, toolsListDelay, toolCount)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cfg := &models.Config{Servers: make(map[string]*models.ServerConfig)}
	cfg.ApplyDefaults()
	lm := lifecycle.NewManager(cfg, "test", logger)
	gw := proxy.New(cfg, lm, "test", logger)

	daemonCtx, daemonCancel := context.WithCancel(context.Background())
	defer daemonCancel()

	srv := NewServer(lm, gw, nil, cfg, "", logger, AuthConfig{}, "test")
	srv.daemonCtx = daemonCtx

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	body, err := json.Marshal(map[string]any{
		"name":   "f4-drain",
		"config": map[string]any{"url": backendURL},
	})
	require.NoError(t, err)

	httpClient := &http.Client{Timeout: 30 * time.Second}
	resp, err := httpClient.Post(ts.URL+"/api/v1/servers", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	_ = resp.Body.Close()

	t.Logf("POST /servers returned %d — cancelling daemon ctx", resp.StatusCode)
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)

	// Cancel daemon context, which propagates into lifecycleCtx.
	daemonCancel()

	// bgStartWG.Wait() should return within drainTimeout.
	done := make(chan struct{})
	go func() {
		srv.bgStartWG.Wait()
		close(done)
	}()

	select {
	case <-done:
		t.Logf("bgStartWG drained successfully after daemon ctx cancel")
	case <-time.After(drainTimeout):
		t.Fatalf("bgStartWG did not drain within %s — goroutine leak", drainTimeout)
	}
}
