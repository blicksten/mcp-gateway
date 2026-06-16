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

// These tests pin the Transports gating in Server.Handler(). The gateway serves
// MCP over HTTP only: /mcp (streamable) and /sse (legacy) are one HTTP family
// and mount together. "http" and "sse" are aliases for that family; "stdio" is
// unimplemented (warned + ignored); an empty/unknown/stdio-only list falls back
// to the HTTP family so the gateway is never unreachable. The decisive property
// is backward compatibility: a legacy config transports:["http"] (written when
// the field was inert) must KEEP /sse.

func newServerWithTransports(t *testing.T, transports []string) http.Handler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &models.Config{
		Gateway: models.GatewaySettings{Transports: transports},
		Servers: map[string]*models.ServerConfig{},
	}
	cfg.ApplyDefaults()
	// ApplyDefaults seeds Transports only when empty; re-apply the caller's
	// explicit list (non-empty) so each case controls the exact value. Passing
	// nil exercises the seeded default.
	if len(transports) > 0 {
		cfg.Gateway.Transports = transports
	}
	lm := lifecycle.NewManager(cfg, "test", logger)
	gw := proxy.New(cfg, lm, "test", logger)
	mon := health.NewMonitor(lm, 0, logger)
	srv := NewServer(lm, gw, mon, cfg, "", logger, AuthConfig{Enabled: false}, "test")
	return srv.Handler()
}

// probeStatus issues a sessionless GET (no Mcp-Session-Id) on a loopback
// connection. A MOUNTED MCP surface returns 400 — the sessionless-GET storm
// guard in mcpTransportPolicy intercepts before the streaming SSE/streamable
// handler runs, so this never blocks. An UNMOUNTED route returns chi's 404.
// Thus: mounted => != 404 (specifically 400); unmounted => 404.
func probeStatus(t *testing.T, h http.Handler, path string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "127.0.0.1:54321"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func assertMounted(t *testing.T, h http.Handler, path string) {
	t.Helper()
	assert.NotEqual(t, http.StatusNotFound, probeStatus(t, h, path),
		"%s must be mounted (got chi 404 — route not registered)", path)
}

// REGRESSION: the backward-compat guarantee. A legacy ["http"] config must keep
// /sse mounted (per-token gating previously broke this — silent /sse loss).
func TestTransportsGating_LegacyHTTP_KeepsSSE(t *testing.T) {
	h := newServerWithTransports(t, []string{"http"})
	assertMounted(t, h, "/mcp")
	assertMounted(t, h, "/sse")
}

func TestTransportsGating_SSEAlias_MountsBoth(t *testing.T) {
	h := newServerWithTransports(t, []string{"sse"})
	assertMounted(t, h, "/mcp")
	assertMounted(t, h, "/sse")
}

func TestTransportsGating_Defaults_MountBoth(t *testing.T) {
	// nil → ApplyDefaults seeds ["http"] → HTTP family mounts both surfaces.
	h := newServerWithTransports(t, nil)
	assertMounted(t, h, "/mcp")
	assertMounted(t, h, "/sse")
}

func TestTransportsGating_StdioOnly_FallsBackToHTTPFamily(t *testing.T) {
	// "stdio" is unimplemented; with no HTTP-family token the router falls back
	// to mounting the HTTP family so the gateway is never silently unreachable.
	h := newServerWithTransports(t, []string{"stdio"})
	assertMounted(t, h, "/mcp")
	assertMounted(t, h, "/sse")
}
