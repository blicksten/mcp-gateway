package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"mcp-gateway/internal/health"
	"mcp-gateway/internal/lifecycle"
	"mcp-gateway/internal/models"
	"mcp-gateway/internal/proxy"

	"github.com/stretchr/testify/assert"
)

// newServerWithTransports builds a minimal test Server whose router is gated by
// the given GatewaySettings.Transports list. Auth is disabled to keep the probe
// focused on route mounting.
//
// Sequencing: ApplyDefaults() IS called (to fill port/ping defaults), which also
// seeds Transports=["http","sse"] when the passed slice is empty/nil. We then
// re-apply the caller's explicit list ONLY when it is non-empty — so passing
// `nil` exercises the seeded-default path, while passing `["http"]` / `["sse"]`
// / `["stdio"]` exercises that exact value. An empty non-nil slice would be
// indistinguishable from nil here, so callers must pass nil for the default case.
func newServerWithTransports(t *testing.T, transports []string) http.Handler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &models.Config{
		Gateway: models.GatewaySettings{Transports: transports},
		Servers: map[string]*models.ServerConfig{},
	}
	cfg.ApplyDefaults()
	if len(transports) > 0 {
		cfg.Gateway.Transports = transports
	}
	lm := lifecycle.NewManager(cfg, "test", logger)
	gw := proxy.New(cfg, lm, "test", logger)
	mon := health.NewMonitor(lm, 0, logger)
	srv := NewServer(lm, gw, mon, cfg, "", logger, AuthConfig{Enabled: false}, "test")
	return srv.Handler()
}

// probeStatus issues a GET against path on a loopback connection and returns the
// HTTP status. A mounted route yields a handler/middleware status (e.g. 400/403
// for /mcp without MCP headers); an unmounted route yields chi's 404 NotFound.
// GET /sse is deliberately never probed when the SSE route is mounted because
// the SSE handler opens a long-lived event stream that would block the recorder.
func probeStatus(t *testing.T, h http.Handler, path string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "127.0.0.1:54321"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestTransportsGating_HTTPOnly_DisablesSSE(t *testing.T) {
	h := newServerWithTransports(t, []string{"http"})
	assert.NotEqual(t, http.StatusNotFound, probeStatus(t, h, "/mcp"),
		"/mcp must be mounted when transports includes \"http\"")
	assert.Equal(t, http.StatusNotFound, probeStatus(t, h, "/sse"),
		"/sse must NOT be mounted when transports excludes \"sse\"")
}

func TestTransportsGating_SSEOnly_DisablesMCP(t *testing.T) {
	h := newServerWithTransports(t, []string{"sse"})
	assert.Equal(t, http.StatusNotFound, probeStatus(t, h, "/mcp"),
		"/mcp must NOT be mounted when transports excludes \"http\"")
	// /sse mounting is asserted indirectly: with only "sse" present and "http"
	// absent, /mcp is 404 above; the fallback (mount /mcp) does not fire because
	// sseEnabled is true. We avoid GET /sse to prevent the streaming handler
	// from blocking the recorder.
}

func TestTransportsGating_Defaults_MountMCP(t *testing.T) {
	// nil → ApplyDefaults seeds ["http","sse"] → /mcp mounted.
	h := newServerWithTransports(t, nil)
	assert.NotEqual(t, http.StatusNotFound, probeStatus(t, h, "/mcp"),
		"/mcp must be mounted under default transports")
}

func TestTransportsGating_StdioOnly_FallsBackToMCP(t *testing.T) {
	// "stdio" is unimplemented; with no HTTP-family transport the router falls
	// back to mounting /mcp so the gateway is never silently unreachable.
	h := newServerWithTransports(t, []string{"stdio"})
	assert.NotEqual(t, http.StatusNotFound, probeStatus(t, h, "/mcp"),
		"/mcp must be mounted as fallback when only \"stdio\" is requested")
	assert.Equal(t, http.StatusNotFound, probeStatus(t, h, "/sse"),
		"/sse must NOT be mounted when only \"stdio\" is requested")
}
