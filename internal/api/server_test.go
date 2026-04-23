package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"mcp-gateway/internal/health"
	"mcp-gateway/internal/lifecycle"
	"mcp-gateway/internal/models"
	"mcp-gateway/internal/proxy"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func setupTestServer(t *testing.T) (*Server, *lifecycle.Manager) {
	t.Helper()
	cfg := &models.Config{
		Servers: make(map[string]*models.ServerConfig),
	}
	cfg.ApplyDefaults()

	lm := lifecycle.NewManager(cfg, "test", testLogger())
	gw := proxy.New(cfg, lm, "test", testLogger())

	srv := NewServer(lm, gw, nil, cfg, "", testLogger(), AuthConfig{}, "test")
	return srv, lm
}

func doRequest(t *testing.T, handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var bodyReader *bytes.Buffer
	if body != nil {
		data, err := json.Marshal(body)
		require.NoError(t, err)
		bodyReader = bytes.NewBuffer(data)
	} else {
		bodyReader = &bytes.Buffer{}
	}
	req := httptest.NewRequest(method, path, bodyReader)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func TestHealth(t *testing.T) {
	srv, _ := setupTestServerWithMonitor(t)
	handler := srv.Handler()

	rr := doRequest(t, handler, "GET", "/api/v1/health", nil)
	assert.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, "ok", resp["status"])
	assert.Equal(t, float64(0), resp["servers"])

	// Phase D.1 — assert new observability fields are present and correctly typed.
	assert.IsType(t, "", resp["started_at"], "started_at must be a string")
	assert.NotEmpty(t, resp["started_at"], "started_at must not be empty")
	// PID is a JSON number; json.Unmarshal into map[string]any gives float64.
	assert.IsType(t, float64(0), resp["pid"], "pid must be a number")
	assert.Greater(t, resp["pid"].(float64), float64(0), "pid must be positive")
	assert.IsType(t, "", resp["version"], "version must be a string")
	assert.IsType(t, float64(0), resp["uptime_seconds"], "uptime_seconds must be a number")
	assert.GreaterOrEqual(t, resp["uptime_seconds"].(float64), float64(0), "uptime_seconds must be non-negative")
}

func TestListServers_Empty(t *testing.T) {
	srv, _ := setupTestServer(t)
	handler := srv.Handler()

	rr := doRequest(t, handler, "GET", "/api/v1/servers", nil)
	assert.Equal(t, http.StatusOK, rr.Code)

	var resp []ServerView
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Empty(t, resp)
}

func TestAddServer(t *testing.T) {
	srv, lm := setupTestServer(t)
	handler := srv.Handler()

	body := map[string]any{
		"name": "test",
		"config": map[string]any{
			"url":      "http://localhost:3000/mcp",
			"disabled": true, // prevent auto-start in test (no real backend)
		},
	}
	rr := doRequest(t, handler, "POST", "/api/v1/servers", body)
	assert.Equal(t, http.StatusCreated, rr.Code)

	// Verify it's in the manager (disabled → not auto-started).
	entry, ok := lm.Entry("test")
	require.True(t, ok)
	assert.Equal(t, models.StatusStopped, entry.Status)
	assert.Equal(t, "http://localhost:3000/mcp", entry.Config.URL)
}

func TestAddServer_Duplicate(t *testing.T) {
	srv, _ := setupTestServer(t)
	handler := srv.Handler()

	body := map[string]any{
		"name":   "test",
		"config": map[string]any{"url": "http://localhost:3000/mcp", "disabled": true},
	}
	doRequest(t, handler, "POST", "/api/v1/servers", body)
	rr := doRequest(t, handler, "POST", "/api/v1/servers", body)
	assert.Equal(t, http.StatusConflict, rr.Code)
}

func TestAddServer_InvalidConfig(t *testing.T) {
	srv, _ := setupTestServer(t)
	handler := srv.Handler()

	body := map[string]any{
		"name":   "bad",
		"config": map[string]any{}, // no command, url, or rest_url
	}
	rr := doRequest(t, handler, "POST", "/api/v1/servers", body)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestGetServer(t *testing.T) {
	srv, lm := setupTestServer(t)
	handler := srv.Handler()

	_ = lm.AddServer("s1", &models.ServerConfig{URL: "http://localhost:3000/mcp"})

	rr := doRequest(t, handler, "GET", "/api/v1/servers/s1", nil)
	assert.Equal(t, http.StatusOK, rr.Code)

	var resp ServerView
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, "s1", resp.Name)
	assert.Equal(t, models.StatusStopped, resp.Status)
}

func TestGetServer_NotFound(t *testing.T) {
	srv, _ := setupTestServer(t)
	handler := srv.Handler()

	rr := doRequest(t, handler, "GET", "/api/v1/servers/nonexistent", nil)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestRemoveServer(t *testing.T) {
	srv, lm := setupTestServer(t)
	handler := srv.Handler()

	_ = lm.AddServer("s1", &models.ServerConfig{URL: "http://localhost:3000/mcp"})

	rr := doRequest(t, handler, "DELETE", "/api/v1/servers/s1", nil)
	assert.Equal(t, http.StatusOK, rr.Code)

	_, ok := lm.Entry("s1")
	assert.False(t, ok)
}

func TestRestartServer(t *testing.T) {
	srv, lm := setupTestServer(t)
	handler := srv.Handler()

	_ = lm.AddServer("s1", &models.ServerConfig{URL: "http://localhost:3000/mcp"})

	// Restart will try to start, which will fail (no real server), but API should handle it.
	rr := doRequest(t, handler, "POST", "/api/v1/servers/s1/restart", nil)
	// The error is acceptable — restart of an HTTP backend that doesn't exist.
	assert.Contains(t, []int{http.StatusOK, http.StatusInternalServerError}, rr.Code)
}

func TestListTools_Empty(t *testing.T) {
	srv, _ := setupTestServer(t)
	handler := srv.Handler()

	rr := doRequest(t, handler, "GET", "/api/v1/tools", nil)
	assert.Equal(t, http.StatusOK, rr.Code)

	var resp []models.ToolInfo
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Empty(t, resp) // no response is null in JSON
}

func TestPatchServer_Disable(t *testing.T) {
	srv, lm := setupTestServer(t)
	handler := srv.Handler()

	sc := &models.ServerConfig{URL: "http://localhost:3000/mcp"}
	_ = lm.AddServer("s1", sc)
	srv.cfg.Servers["s1"] = sc

	disabled := true
	body := map[string]any{"disabled": disabled}
	rr := doRequest(t, handler, "PATCH", "/api/v1/servers/s1", body)
	assert.Equal(t, http.StatusOK, rr.Code)

	entry, _ := lm.Entry("s1")
	assert.Equal(t, models.StatusDisabled, entry.Status)
}

func TestPatchServer_AddEnv(t *testing.T) {
	srv, lm := setupTestServer(t)
	handler := srv.Handler()
	sc := &models.ServerConfig{URL: "http://localhost:3000/mcp"}
	_ = lm.AddServer("s1", sc)
	srv.cfg.Servers["s1"] = sc

	body := map[string]any{"add_env": []string{"FOO=bar", "BAZ=qux"}}
	rr := doRequest(t, handler, "PATCH", "/api/v1/servers/s1", body)
	// Accept 200 or 500 (restart may fail without a real backend in test).
	assert.Contains(t, []int{http.StatusOK, http.StatusInternalServerError}, rr.Code)

	// Verify config state: env applied regardless of restart outcome.
	srv.cfgMu.RLock()
	env := srv.cfg.Servers["s1"].Env
	srv.cfgMu.RUnlock()
	assert.Equal(t, []string{"FOO=bar", "BAZ=qux"}, env)
}

func TestPatchServer_AddEnv_DangerousKey(t *testing.T) {
	srv, lm := setupTestServer(t)
	handler := srv.Handler()
	_ = lm.AddServer("s1", &models.ServerConfig{URL: "http://localhost:3000/mcp"})

	body := map[string]any{"add_env": []string{"LD_PRELOAD=/evil.so"}}
	rr := doRequest(t, handler, "PATCH", "/api/v1/servers/s1", body)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "not permitted")
}

func TestPatchServer_RemoveEnv_ExactMatch(t *testing.T) {
	srv, lm := setupTestServer(t)
	handler := srv.Handler()
	sc := &models.ServerConfig{
		URL: "http://localhost:3000/mcp",
		Env: []string{"FOO=1", "FOOBAR=2", "BAZ=3"},
	}
	_ = lm.AddServer("s1", sc)
	srv.cfg.Servers["s1"] = sc

	// Remove FOO — must NOT remove FOOBAR.
	body := map[string]any{"remove_env": []string{"FOO"}}
	rr := doRequest(t, handler, "PATCH", "/api/v1/servers/s1", body)
	// Accept 200 or 500 (restart may fail without a real backend in test).
	assert.Contains(t, []int{http.StatusOK, http.StatusInternalServerError}, rr.Code)

	// Verify config state: FOO removed, FOOBAR and BAZ remain.
	srv.cfgMu.RLock()
	env := srv.cfg.Servers["s1"].Env
	srv.cfgMu.RUnlock()
	assert.Equal(t, []string{"FOOBAR=2", "BAZ=3"}, env)
}

func TestPatchServer_AddHeaders(t *testing.T) {
	srv, lm := setupTestServer(t)
	handler := srv.Handler()
	sc := &models.ServerConfig{URL: "http://localhost:3000/mcp"}
	_ = lm.AddServer("s1", sc)
	srv.cfg.Servers["s1"] = sc

	body := map[string]any{"add_headers": map[string]string{"X-Custom": "val"}}
	rr := doRequest(t, handler, "PATCH", "/api/v1/servers/s1", body)
	// Accept 200 or 500 (restart may fail without a real backend in test).
	assert.Contains(t, []int{http.StatusOK, http.StatusInternalServerError}, rr.Code)

	// Verify config state.
	srv.cfgMu.RLock()
	headers := srv.cfg.Servers["s1"].Headers
	srv.cfgMu.RUnlock()
	assert.Equal(t, map[string]string{"X-Custom": "val"}, headers)
}

func TestPatchServer_AddHeaders_DangerousHeader(t *testing.T) {
	srv, lm := setupTestServer(t)
	handler := srv.Handler()
	_ = lm.AddServer("s1", &models.ServerConfig{URL: "http://localhost:3000/mcp"})

	body := map[string]any{"add_headers": map[string]string{"Host": "evil.com"}}
	rr := doRequest(t, handler, "PATCH", "/api/v1/servers/s1", body)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestPatchServer_RemoveHeaders(t *testing.T) {
	srv, lm := setupTestServer(t)
	handler := srv.Handler()
	sc := &models.ServerConfig{
		URL:     "http://localhost:3000/mcp",
		Headers: map[string]string{"X-Old": "val", "X-Keep": "val2"},
	}
	_ = lm.AddServer("s1", sc)
	srv.cfg.Servers["s1"] = sc

	body := map[string]any{"remove_headers": []string{"X-Old"}}
	rr := doRequest(t, handler, "PATCH", "/api/v1/servers/s1", body)
	// Accept 200 or 500 (restart may fail without a real backend in test).
	assert.Contains(t, []int{http.StatusOK, http.StatusInternalServerError}, rr.Code)

	// Verify config state: X-Old removed, X-Keep remains.
	srv.cfgMu.RLock()
	headers := srv.cfg.Servers["s1"].Headers
	srv.cfgMu.RUnlock()
	assert.Equal(t, map[string]string{"X-Keep": "val2"}, headers)
}

func TestPatchServer_EnvValueNewline(t *testing.T) {
	srv, lm := setupTestServer(t)
	handler := srv.Handler()
	_ = lm.AddServer("s1", &models.ServerConfig{URL: "http://localhost:3000/mcp"})

	body := map[string]any{"add_env": []string{"FOO=bar\nbaz"}}
	rr := doRequest(t, handler, "PATCH", "/api/v1/servers/s1", body)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "invalid characters")
}

func TestPatchServer_HeaderValueCRLF(t *testing.T) {
	srv, lm := setupTestServer(t)
	handler := srv.Handler()
	_ = lm.AddServer("s1", &models.ServerConfig{URL: "http://localhost:3000/mcp"})

	body := map[string]any{"add_headers": map[string]string{"X-Custom": "val\r\nX-Injected: evil"}}
	rr := doRequest(t, handler, "PATCH", "/api/v1/servers/s1", body)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "invalid characters")
}

func TestPatchServer_NotFound(t *testing.T) {
	srv, _ := setupTestServer(t)
	handler := srv.Handler()

	body := map[string]any{"add_env": []string{"FOO=bar"}}
	rr := doRequest(t, handler, "PATCH", "/api/v1/servers/nonexistent", body)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestPatchServer_ValidateRollback(t *testing.T) {
	srv, lm := setupTestServer(t)
	handler := srv.Handler()
	_ = lm.AddServer("s1", &models.ServerConfig{
		URL: "http://localhost:3000/mcp",
		Env: []string{"KEEP=original"},
	})
	// Populate the config map to match lifecycle.
	srv.cfgMu.Lock()
	srv.cfg.Servers["s1"] = &models.ServerConfig{
		URL: "http://localhost:3000/mcp",
		Env: []string{"KEEP=original"},
	}
	srv.cfgMu.Unlock()

	// Send a patch that adds valid env but also adds headers with CRLF (will fail validation).
	body := map[string]any{
		"add_env":     []string{"NEW=value"},
		"add_headers": map[string]string{"X-Bad": "val\r\nevil"},
	}
	rr := doRequest(t, handler, "PATCH", "/api/v1/servers/s1", body)
	assert.Equal(t, http.StatusBadRequest, rr.Code)

	// Verify the original config was NOT mutated (rollback on validation failure).
	srv.cfgMu.RLock()
	env := srv.cfg.Servers["s1"].Env
	srv.cfgMu.RUnlock()
	assert.Equal(t, []string{"KEEP=original"}, env, "env should not be mutated on validation failure")
}

func TestServerView_NoSecrets(t *testing.T) {
	entry := models.ServerEntry{
		Name:   "s1",
		Status: models.StatusRunning,
		Config: models.ServerConfig{
			URL:     "http://localhost:3000/mcp",
			Env:     []string{"SECRET_KEY=mysecretvalue123"},
			Headers: map[string]string{"Authorization": "Bearer tok123"},
		},
	}
	view := toView(entry)

	data, err := json.Marshal(view)
	require.NoError(t, err)

	// Values must NOT appear in the JSON output.
	assert.NotContains(t, string(data), "mysecretvalue123")
	assert.NotContains(t, string(data), "Bearer tok123")

	// Key names ARE exposed (by design — Phase 9.1 T1.3).
	assert.Contains(t, string(data), "SECRET_KEY")
	assert.Contains(t, string(data), "Authorization")
	assert.Equal(t, []string{"SECRET_KEY"}, view.EnvKeys)
	assert.Equal(t, []string{"Authorization"}, view.HeaderKeys)
}

// TestConcurrentConfigReloadAndHandlers exercises the cfgMu lock by running
// UpdateConfig concurrently with API handler access (T1.6 — config race fix).
func TestConcurrentConfigReloadAndHandlers(t *testing.T) {
	srv, _ := setupTestServer(t)
	handler := srv.Handler()

	const goroutines = 10
	const iterations = 50

	var wg sync.WaitGroup

	// Goroutines calling UpdateConfig (simulating config reload).
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				newCfg := &models.Config{
					Servers: make(map[string]*models.ServerConfig),
				}
				newCfg.ApplyDefaults()
				srv.UpdateConfig(newCfg)
			}
		}()
	}

	// Goroutines hitting API handlers that read/mutate config.
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				doRequest(t, handler, "GET", "/api/v1/servers", nil)
				doRequest(t, handler, "GET", "/api/v1/tools", nil)
				doRequest(t, handler, "GET", "/api/v1/health", nil)
			}
		}()
	}

	// Goroutines adding/removing servers (mutate config).
	for i := 0; i < goroutines/2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			name := fmt.Sprintf("concurrent-%d", idx)
			body := map[string]any{
				"name": name,
				"config": map[string]any{
					"url":      "http://localhost:3000/mcp",
					"disabled": true,
				},
			}
			doRequest(t, handler, "POST", "/api/v1/servers", body)
			doRequest(t, handler, "DELETE", "/api/v1/servers/"+name, nil)
		}(i)
	}

	wg.Wait()
}

// TestThrottleBacklog_Rejects verifies that ThrottleBacklog returns 429
// (Too Many Requests) when the concurrency limit and backlog are exhausted (T2.3).
// Uses a low-limit handler to make the test fast and deterministic.
func TestThrottleBacklog_Rejects(t *testing.T) {
	// Build a minimal chi router with a tight throttle (2 concurrent, 0 backlog).
	r := chi.NewRouter()
	block := make(chan struct{})
	ready := make(chan struct{}, 2) // signals when handlers have entered
	r.Route("/throttled", func(r chi.Router) {
		r.Use(middleware.ThrottleBacklog(2, 0, 1*time.Millisecond))
		r.Get("/", func(w http.ResponseWriter, _ *http.Request) {
			ready <- struct{}{} // signal entry
			<-block             // block until released
			w.WriteHeader(http.StatusOK)
		})
	})

	// Fill both slots.
	for i := 0; i < 2; i++ {
		go func() {
			req := httptest.NewRequest("GET", "/throttled/", nil)
			r.ServeHTTP(httptest.NewRecorder(), req)
		}()
	}

	// Wait until both goroutines are inside the handler (deterministic, no sleep).
	<-ready
	<-ready

	// Third request should get 429 (no backlog capacity).
	req := httptest.NewRequest("GET", "/throttled/", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusTooManyRequests, rr.Code)

	close(block) // release blocked goroutines
}

func TestMaxBodySize_OversizedPayload(t *testing.T) {
	srv, _ := setupTestServer(t)
	handler := srv.Handler()

	// Create a payload larger than 1 MB.
	huge := make([]byte, (1<<20)+1024)
	for i := range huge {
		huge[i] = 'A'
	}
	req := httptest.NewRequest("POST", "/api/v1/servers", bytes.NewReader(huge))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	// MaxBytesReader triggers http.MaxBytesError; handler detects it and returns 413.
	assert.Equal(t, http.StatusRequestEntityTooLarge, rr.Code)
}

// TestMaxBodySize_NormalPayload verifies normal-sized payloads pass through.
func TestMaxBodySize_NormalPayload(t *testing.T) {
	srv, _ := setupTestServer(t)
	handler := srv.Handler()

	body := map[string]any{
		"name": "small",
		"config": map[string]any{
			"url":      "http://localhost:3000/mcp",
			"disabled": true,
		},
	}
	rr := doRequest(t, handler, "POST", "/api/v1/servers", body)
	assert.Equal(t, http.StatusCreated, rr.Code)
}

// TestAPIv1Redirect verifies /api/* → /api/v1/* 307 redirect (T5.5).
func TestAPIv1Redirect(t *testing.T) {
	srv, _ := setupTestServer(t)
	handler := srv.Handler()

	// GET /api/health → 307 → /api/v1/health.
	req := httptest.NewRequest("GET", "/api/health", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusTemporaryRedirect, rr.Code)
	assert.Equal(t, "/api/v1/health", rr.Header().Get("Location"))

	// POST /api/servers → 307 (preserves method).
	body := map[string]any{
		"name":   "test",
		"config": map[string]any{"url": "http://localhost:3000/mcp", "disabled": true},
	}
	rr = doRequest(t, handler, "POST", "/api/servers", body)
	assert.Equal(t, http.StatusTemporaryRedirect, rr.Code)
	assert.Equal(t, "/api/v1/servers", rr.Header().Get("Location"))

	// DELETE /api/servers/name → 307 (preserves method).
	req = httptest.NewRequest("DELETE", "/api/servers/my-srv", nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusTemporaryRedirect, rr.Code)
	assert.Equal(t, "/api/v1/servers/my-srv", rr.Header().Get("Location"))

	// GET with query string → 307 preserves query params.
	req = httptest.NewRequest("GET", "/api/health?detail=true&fmt=json", nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusTemporaryRedirect, rr.Code)
	assert.Equal(t, "/api/v1/health?detail=true&fmt=json", rr.Header().Get("Location"))
}

func TestMCPEndpointExists(t *testing.T) {
	srv, _ := setupTestServer(t)
	handler := srv.Handler()

	// POST /mcp — MCP Streamable HTTP should respond (even without init, it responds).
	rr := doRequest(t, handler, "POST", "/mcp", map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-03-26",
			"capabilities":   map[string]any{},
			"clientInfo":     map[string]any{"name": "test", "version": "1.0"},
		},
	})
	// MCP endpoint is mounted — it should not return 404.
	assert.NotEqual(t, http.StatusNotFound, rr.Code)
}

// --- Phase 10.4: Metrics endpoint tests ---

func setupTestServerWithMonitor(t *testing.T) (*Server, *lifecycle.Manager) {
	t.Helper()
	cfg := &models.Config{
		Servers: make(map[string]*models.ServerConfig),
	}
	cfg.ApplyDefaults()

	lm := lifecycle.NewManager(cfg, "test", testLogger())
	gw := proxy.New(cfg, lm, "test", testLogger())
	mon := health.NewMonitor(lm, 1*time.Second, testLogger())

	srv := NewServer(lm, gw, mon, cfg, "", testLogger(), AuthConfig{}, "test")
	return srv, lm
}

func TestMetrics_NilMonitor(t *testing.T) {
	srv, _ := setupTestServer(t) // no monitor
	handler := srv.Handler()

	rr := doRequest(t, handler, "GET", "/api/v1/metrics", nil)
	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestMetrics_ZeroServers(t *testing.T) {
	srv, _ := setupTestServerWithMonitor(t)
	handler := srv.Handler()

	rr := doRequest(t, handler, "GET", "/api/v1/metrics", nil)
	assert.Equal(t, http.StatusOK, rr.Code)

	var resp models.MetricsResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Empty(t, resp.Servers)
	assert.True(t, time.Duration(resp.GatewayUptime) >= 0)
	assert.Equal(t, 0, resp.Tokens.TotalTools)
}

func TestMetrics_WithServers(t *testing.T) {
	srv, lm := setupTestServerWithMonitor(t)
	handler := srv.Handler()

	// Add a server with tools.
	err := lm.AddServer("test-srv", &models.ServerConfig{Command: "echo", Disabled: true})
	require.NoError(t, err)
	lm.SetStatus("test-srv", models.StatusRunning, "")
	lm.SetTools("test-srv", []models.ToolInfo{
		{Name: "read", Description: "Read a file from disk", InputSchema: map[string]any{"type": "object"}},
		{Name: "write", Description: "Write content to a file", InputSchema: map[string]any{"type": "object"}},
	})
	srv.gw.RebuildTools()

	rr := doRequest(t, handler, "GET", "/api/v1/metrics", nil)
	assert.Equal(t, http.StatusOK, rr.Code)

	var resp models.MetricsResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))

	require.Len(t, resp.Servers, 1)
	assert.Equal(t, "test-srv", resp.Servers[0].Name)
	assert.Equal(t, 0, resp.Servers[0].RestartCount)

	assert.Equal(t, 2, resp.Tokens.TotalTools)
	assert.True(t, resp.Tokens.EstDescTokens > 0, "description tokens should be positive")
	assert.True(t, resp.Tokens.EstSchemaTokens > 0, "schema tokens should be positive with InputSchema present")
	assert.Equal(t, resp.Tokens.EstDescTokens+resp.Tokens.EstSchemaTokens, resp.Tokens.EstTotalTokens)
}

func TestMetrics_TokenEstimation_WithCompressSchemas(t *testing.T) {
	srv, lm := setupTestServerWithMonitor(t)
	handler := srv.Handler()

	// Enable CompressSchemas — descriptions should be truncated to first sentence.
	srv.cfgMu.Lock()
	srv.cfg.Gateway.CompressSchemas = true
	srv.cfgMu.Unlock()

	err := lm.AddServer("cs", &models.ServerConfig{Command: "echo", Disabled: true})
	require.NoError(t, err)
	lm.SetStatus("cs", models.StatusRunning, "")
	longDesc := "Read a file from disk. This tool supports multiple encodings and handles binary files gracefully with configurable buffer sizes"
	lm.SetTools("cs", []models.ToolInfo{
		{Name: "read", Description: longDesc, InputSchema: map[string]any{"type": "object"}},
	})
	srv.gw.RebuildTools()

	rr := doRequest(t, handler, "GET", "/api/v1/metrics", nil)
	require.Equal(t, http.StatusOK, rr.Code)

	var resp models.MetricsResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))

	// With CompressSchemas, description is truncated to first sentence:
	// "Read a file from disk." = 22 runes → 22/4 = 5 tokens.
	// Without compression it would be ~132 runes → 33 tokens.
	assert.Equal(t, 1, resp.Tokens.TotalTools)
	assert.True(t, resp.Tokens.EstDescTokens <= 6,
		"compressed description should yield <=6 tokens, got %d", resp.Tokens.EstDescTokens)
	assert.True(t, resp.Tokens.EstDescTokens > 0,
		"description tokens should be positive")
}

func TestMetrics_TokenEstimation_Multibyte(t *testing.T) {
	srv, lm := setupTestServerWithMonitor(t)
	handler := srv.Handler()

	err := lm.AddServer("mb", &models.ServerConfig{Command: "echo", Disabled: true})
	require.NoError(t, err)
	lm.SetStatus("mb", models.StatusRunning, "")
	// 4 runes = 1 estimated token; 12 bytes in UTF-8 but only 4 runes.
	lm.SetTools("mb", []models.ToolInfo{
		{Name: "tool", Description: "日本語テ", InputSchema: map[string]any{"type": "object"}},
	})
	srv.gw.RebuildTools()

	rr := doRequest(t, handler, "GET", "/api/v1/metrics", nil)
	require.Equal(t, http.StatusOK, rr.Code)

	var resp models.MetricsResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))

	// ListTools returns raw description "日本語テ" = 4 runes → 4/4 = 1 token.
	assert.Equal(t, 1, resp.Tokens.EstDescTokens)
}

// --- Phase D.1: handleShutdown tests ---

// newAuthedServerWithShutdown builds a test server with Bearer auth enabled
// and a real shutdownFn wired in. Returns the handler, the token, and a
// channel that receives a value when shutdownFn is invoked.
func newAuthedServerWithShutdown(t *testing.T) (http.Handler, string, <-chan struct{}) {
	t.Helper()
	cfg := &models.Config{Servers: make(map[string]*models.ServerConfig)}
	cfg.ApplyDefaults()

	lm := lifecycle.NewManager(cfg, "test", testLogger())
	gw := proxy.New(cfg, lm, "test", testLogger())
	mon := health.NewMonitor(lm, 1*time.Second, testLogger())

	token := "test-shutdown-token"
	authCfg := AuthConfig{Enabled: true, Token: token}
	srv := NewServer(lm, gw, mon, cfg, "", testLogger(), authCfg, "test")

	called := make(chan struct{}, 1)
	srv.SetShutdownFn(func() { called <- struct{}{} })

	return srv.Handler(), token, called
}

// doShutdownRequest sends POST /api/v1/shutdown with optional bearer token.
func doShutdownRequest(t *testing.T, h http.Handler, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/shutdown", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestShutdown_NoAuth_Returns401(t *testing.T) {
	h, _, _ := newAuthedServerWithShutdown(t)
	rr := doShutdownRequest(t, h, "")
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestShutdown_ValidToken_Returns202AndCallsShutdownFn(t *testing.T) {
	h, token, called := newAuthedServerWithShutdown(t)

	rr := doShutdownRequest(t, h, token)
	assert.Equal(t, http.StatusAccepted, rr.Code)

	var body map[string]string
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal(t, "shutting_down", body["status"])

	// shutdownFn must have been invoked (go fn() is async — give it a moment).
	select {
	case <-called:
		// Good.
	case <-time.After(500 * time.Millisecond):
		t.Fatal("shutdownFn was not called within 500ms")
	}
}

func TestShutdown_NilShutdownFn_IsNoOp(t *testing.T) {
	cfg := &models.Config{Servers: make(map[string]*models.ServerConfig)}
	cfg.ApplyDefaults()
	lm := lifecycle.NewManager(cfg, "test", testLogger())
	gw := proxy.New(cfg, lm, "test", testLogger())
	mon := health.NewMonitor(lm, 1*time.Second, testLogger())

	token := "tok"
	srv := NewServer(lm, gw, mon, cfg, "", testLogger(), AuthConfig{Enabled: true, Token: token}, "test")
	// SetShutdownFn(nil) — must not panic.
	srv.SetShutdownFn(nil)
	h := srv.Handler()

	rr := doShutdownRequest(t, h, token)
	assert.Equal(t, http.StatusAccepted, rr.Code)
}

func TestShutdown_Concurrent_InvokesShutdownFnExactlyOnce(t *testing.T) {
	h, token, _ := newAuthedServerWithShutdown(t)

	const goroutines = 10
	results := make([]int, goroutines)
	bodies := make([]string, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := range goroutines {
		go func(i int) {
			defer wg.Done()
			rr := doShutdownRequest(t, h, token)
			results[i] = rr.Code
			bodies[i] = rr.Body.String()
		}(i)
	}
	wg.Wait()

	// All responses must be 202.
	for i, code := range results {
		assert.Equal(t, http.StatusAccepted, code, "goroutine %d got unexpected status", i)
	}

	// Exactly one response should be "shutting_down"; the rest "already_shutting_down".
	shutting, already := 0, 0
	for _, b := range bodies {
		var resp map[string]string
		if json.Unmarshal([]byte(b), &resp) == nil {
			switch resp["status"] {
			case "shutting_down":
				shutting++
			case "already_shutting_down":
				already++
			}
		}
	}
	assert.Equal(t, 1, shutting, "expected exactly one 'shutting_down' response")
	assert.Equal(t, goroutines-1, already, "expected %d 'already_shutting_down' responses", goroutines-1)
}
