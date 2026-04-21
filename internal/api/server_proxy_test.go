package api

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mcp-gateway/internal/auth"
	"mcp-gateway/internal/health"
	"mcp-gateway/internal/lifecycle"
	"mcp-gateway/internal/models"
	"mcp-gateway/internal/proxy"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Phase 16.1 — HTTP dispatcher tests for the dual-mode (aggregate + per-backend)
// streamable endpoints. Covers T16.1.5 (dispatcher path logic) and T16.1.7
// (mcpTransportPolicy uniformity across /mcp and /mcp/*).
//
// Subtests:
//   TestStreamablePerBackendRoute       — /mcp/{backend} returns backend's
//                                          unnamespaced tools via SDK client
//   TestStreamableUnknownBackend400     — /mcp/{validname-but-unregistered}
//                                          returns HTTP 400 (SDK nil-server
//                                          contract)
//   TestAggregateRouteStillWorks        — /mcp returns namespaced tools
//   TestPerBackendAuthRequiresSameBearer — bearer-required policy applies
//                                          uniformly to both paths

// newPhase16Server builds a test Server pre-populated with running backends
// and tools, then calls RebuildTools so per-backend servers exist. Auth
// is off and policy is loopback-only unless the caller overrides cfg
// via before().
func newPhase16Server(t *testing.T, mode string, authEnabled bool) (http.Handler, string, *proxy.Gateway) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	cfg := &models.Config{
		Gateway: models.GatewaySettings{
			AuthMCPTransport: mode,
			AllowRemote:      mode == models.AuthMCPTransportBearerRequired,
		},
		Servers: map[string]*models.ServerConfig{
			"alpha": {Command: "echo"},
			"beta":  {Command: "echo"},
		},
	}
	cfg.ApplyDefaults()

	lm := lifecycle.NewManager(cfg, "test", logger)
	gw := proxy.New(cfg, lm, "test", logger)
	mon := health.NewMonitor(lm, 0, logger)

	// Mark backends running and inject tools so per-backend servers
	// materialize on the next RebuildTools.
	lm.SetStatus("alpha", models.StatusRunning, "")
	lm.SetStatus("beta", models.StatusRunning, "")
	lm.SetTools("alpha", []models.ToolInfo{
		{Name: "read", Description: "Read a file"},
		{Name: "write", Description: "Write a file"},
	})
	lm.SetTools("beta", []models.ToolInfo{
		{Name: "search", Description: "Search code"},
	})
	gw.RebuildTools()

	token := ""
	if authEnabled {
		tok, err := auth.GenerateToken()
		require.NoError(t, err)
		token = tok
	}
	authCfg := AuthConfig{Enabled: authEnabled, Token: token}
	srv := NewServer(lm, gw, mon, cfg, "", logger, authCfg, "test")
	return srv.Handler(), token, gw
}

// connectMCPClient opens a StreamableClient session against a live test
// server endpoint (must include /mcp or /mcp/<backend>). Returns the open
// session; the caller must Close it.
func connectMCPClient(t *testing.T, endpoint string, headers map[string]string) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	c := mcp.NewClient(&mcp.Implementation{Name: "phase16-api-test", Version: "1.0"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint:   endpoint,
		HTTPClient: &http.Client{Timeout: 10 * time.Second},
	}
	// Authorization + other headers flow through an http.RoundTripper.
	if len(headers) > 0 {
		transport.HTTPClient = &http.Client{
			Timeout:   10 * time.Second,
			Transport: &headerRoundTripper{base: http.DefaultTransport, headers: headers},
		}
	}
	sess, err := c.Connect(ctx, transport, nil)
	require.NoError(t, err, "mcp client failed to connect to %s", endpoint)
	return sess
}

// headerRoundTripper injects fixed headers on every request — a test-only
// shim for supplying Authorization to the SDK StreamableClientTransport.
type headerRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
}

func (h *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range h.headers {
		req.Header.Set(k, v)
	}
	return h.base.RoundTrip(req)
}

// TestStreamablePerBackendRoute verifies that /mcp/{backend} routes to the
// per-backend server and exposes the backend's unnamespaced tools.
func TestStreamablePerBackendRoute(t *testing.T) {
	h, _, _ := newPhase16Server(t, models.AuthMCPTransportLoopbackOnly, false)
	ts := httptest.NewServer(h)
	defer ts.Close()

	sess := connectMCPClient(t, ts.URL+"/mcp/alpha", nil)
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := sess.ListTools(ctx, nil)
	require.NoError(t, err)

	names := make(map[string]struct{}, len(res.Tools))
	for _, tool := range res.Tools {
		names[tool.Name] = struct{}{}
	}
	assert.Contains(t, names, "read", "/mcp/alpha must expose alpha's 'read' tool unnamespaced")
	assert.Contains(t, names, "write", "/mcp/alpha must expose alpha's 'write' tool unnamespaced")
	assert.NotContains(t, names, "alpha__read", "/mcp/alpha must NOT expose namespaced names")
	assert.NotContains(t, names, "search", "/mcp/alpha must NOT expose beta's tools")
}

// TestStreamableUnknownBackend400 verifies that /mcp/{valid-name} where the
// backend is not registered returns HTTP 400 (go-sdk behavior when getServer
// returns nil, per streamable.go).
func TestStreamableUnknownBackend400(t *testing.T) {
	h, _, _ := newPhase16Server(t, models.AuthMCPTransportLoopbackOnly, false)
	ts := httptest.NewServer(h)
	defer ts.Close()

	// Minimal JSON-RPC initialize so the SDK handler sees a well-formed
	// request before it decides what to do with the nil server.
	initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"phase16-neg","version":"1.0"}}}`
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/mcp/unregistered", strings.NewReader(initBody))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode,
		"unregistered backend must produce 400 Bad Request; got %d body=%s", resp.StatusCode, string(body))
}

// TestAggregateRouteStillWorks verifies the legacy /mcp aggregate endpoint
// keeps working: it exposes namespaced tools with "[backend] " description
// prefix.
func TestAggregateRouteStillWorks(t *testing.T) {
	h, _, _ := newPhase16Server(t, models.AuthMCPTransportLoopbackOnly, false)
	ts := httptest.NewServer(h)
	defer ts.Close()

	sess := connectMCPClient(t, ts.URL+"/mcp", nil)
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := sess.ListTools(ctx, nil)
	require.NoError(t, err)

	descByName := make(map[string]string, len(res.Tools))
	for _, tool := range res.Tools {
		descByName[tool.Name] = tool.Description
	}
	assert.Contains(t, descByName, "alpha__read", "aggregate must expose namespaced alpha__read")
	assert.Contains(t, descByName, "alpha__write", "aggregate must expose namespaced alpha__write")
	assert.Contains(t, descByName, "beta__search", "aggregate must expose namespaced beta__search")
	assert.True(t, strings.HasPrefix(descByName["alpha__read"], "[alpha] "),
		"aggregate description must carry [alpha] prefix; got %q", descByName["alpha__read"])
	assert.True(t, strings.HasPrefix(descByName["beta__search"], "[beta] "),
		"aggregate description must carry [beta] prefix; got %q", descByName["beta__search"])
}

// TestPerBackendAuthRequiresSameBearer verifies mcpTransportPolicy applies
// uniformly to /mcp and /mcp/{backend}. With bearer-required mode: requests
// without Bearer are rejected at both paths; with the correct Bearer both
// paths succeed. This also covers T16.1.7 (uniform policy across /mcp*).
func TestPerBackendAuthRequiresSameBearer(t *testing.T) {
	h, token, _ := newPhase16Server(t, models.AuthMCPTransportBearerRequired, true)
	ts := httptest.NewServer(h)
	defer ts.Close()

	initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"phase16-bearer","version":"1.0"}}}`

	post := func(path, auth string) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, ts.URL+path, bytes.NewReader([]byte(initBody)))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	// No Bearer → 401 on both paths (middleware applied uniformly).
	assert.Equal(t, http.StatusUnauthorized, post("/mcp", ""), "/mcp without Bearer must be 401")
	assert.Equal(t, http.StatusUnauthorized, post("/mcp/alpha", ""), "/mcp/alpha without Bearer must be 401")

	// With correct Bearer → should NOT be 401/403; exact status depends on
	// SDK initialize response (typically 200 for aggregate, 200 for per-backend).
	aggStatus := post("/mcp", "Bearer "+token)
	perStatus := post("/mcp/alpha", "Bearer "+token)
	assert.NotEqual(t, http.StatusUnauthorized, aggStatus, "/mcp with Bearer must not 401")
	assert.NotEqual(t, http.StatusForbidden, aggStatus, "/mcp with Bearer must not 403")
	assert.NotEqual(t, http.StatusUnauthorized, perStatus, "/mcp/alpha with Bearer must not 401")
	assert.NotEqual(t, http.StatusForbidden, perStatus, "/mcp/alpha with Bearer must not 403")
}
