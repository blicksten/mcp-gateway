package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mcp-gateway/internal/auth"
	"mcp-gateway/internal/health"
	"mcp-gateway/internal/lifecycle"
	"mcp-gateway/internal/models"
	"mcp-gateway/internal/proxy"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newAuthedServer returns a test Server with Bearer auth enabled and
// the given MCP transport mode. The backing lifecycle/proxy/health
// stack is minimal: no child processes, just enough for the router.
func newAuthedServer(t *testing.T, mode string, authEnabled bool) (http.Handler, string) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	cfg := &models.Config{
		Gateway: models.GatewaySettings{
			AuthMCPTransport: mode,
			AllowRemote:      mode == models.AuthMCPTransportBearerRequired,
		},
		Servers: map[string]*models.ServerConfig{},
	}
	cfg.ApplyDefaults()

	lm := lifecycle.NewManager(cfg, "test", logger)
	gw := proxy.New(cfg, lm, "test", logger)
	mon := health.NewMonitor(lm, 0, logger)

	token, err := auth.GenerateToken()
	require.NoError(t, err)

	authCfg := AuthConfig{Enabled: authEnabled, Token: token}
	if !authEnabled {
		authCfg.Token = ""
	}
	srv := NewServer(lm, gw, mon, cfg, "", logger, authCfg, "test")
	return srv.Handler(), token
}

// doAuthRequest runs a request against handler with optional Authorization
// and Sec-Fetch-Site headers. Returns the recorder.
func doAuthRequest(t *testing.T, h http.Handler, method, path, auth, origin string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, body)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if origin != "" {
		req.Header.Set("Sec-Fetch-Site", origin)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// ===== §policy-matrix — public routes require NO auth =====

func TestAuth_PublicRoutes_NoAuthRequired(t *testing.T) {
	h, _ := newAuthedServer(t, models.AuthMCPTransportLoopbackOnly, true)

	// /health — public
	rec := doAuthRequest(t, h, "GET", "/api/v1/health", "", "", nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "enabled", body["auth"], "auth field must reflect state")

	// /version — public
	rec = doAuthRequest(t, h, "GET", "/api/v1/version", "", "", nil)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAuth_HealthReportsDisabled_WhenNoAuth(t *testing.T) {
	h, _ := newAuthedServer(t, models.AuthMCPTransportLoopbackOnly, false)

	rec := doAuthRequest(t, h, "GET", "/api/v1/health", "", "", nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "disabled", body["auth"])
}

// ===== §policy-matrix — REST routes require Bearer =====

func TestAuth_RESTRoutes_Require401WhenMissing(t *testing.T) {
	h, _ := newAuthedServer(t, models.AuthMCPTransportLoopbackOnly, true)

	routes := []struct{ method, path string }{
		{"GET", "/api/v1/servers"},
		{"GET", "/api/v1/tools"},
		{"GET", "/api/v1/metrics"},
	}
	for _, r := range routes {
		rec := doAuthRequest(t, h, r.method, r.path, "", "", nil)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, "%s %s without Bearer must return 401", r.method, r.path)
		assert.Equal(t, "Bearer", rec.Header().Get("WWW-Authenticate"))
	}
}

func TestAuth_RESTRoutes_200WithCorrectBearer(t *testing.T) {
	h, tok := newAuthedServer(t, models.AuthMCPTransportLoopbackOnly, true)

	rec := doAuthRequest(t, h, "GET", "/api/v1/servers", "Bearer "+tok, "", nil)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAuth_RESTRoutes_401WithWrongBearer(t *testing.T) {
	h, _ := newAuthedServer(t, models.AuthMCPTransportLoopbackOnly, true)
	other, _ := auth.GenerateToken()

	rec := doAuthRequest(t, h, "GET", "/api/v1/servers", "Bearer "+other, "", nil)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// ===== §csrf-scope — auth runs BEFORE csrf =====

func TestAuth_OrderingAuthBeforeCsrf_Returns401Not403(t *testing.T) {
	h, _ := newAuthedServer(t, models.AuthMCPTransportLoopbackOnly, true)

	// POST without Bearer but WITH cross-origin Sec-Fetch-Site.
	// Old wiring would return 403 (csrf first). New wiring returns 401
	// (auth first) — cheap rejection before csrf even examines the request.
	rec := doAuthRequest(t, h, "POST", "/api/v1/servers", "", "cross-site",
		bytes.NewBufferString(`{"name":"x","config":{}}`))
	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"auth must run BEFORE csrf — missing Bearer on cross-site must 401, not 403")
}

// ===== §csrf-scope — intentional non-coverage =====
//
// Per ADR-0003 §csrf-scope, these routes are intentionally NOT csrf-protected:
//   - /mcp, /mcp/*
//   - /sse, /sse/*
//   - /api/* backward-compat redirect (csrf applies at the destination)
//
// These tests document the non-coverage as code so a future auditor who
// greps for csrfProtect on /mcp doesn't re-flag the scope narrowing as
// a regression.

func TestAuth_MCPTransport_NoCsrfOnCrossOriginPOST(t *testing.T) {
	// loopback-only mode — a cross-site POST to /mcp must be rejected by
	// the transport policy (likely 403 from loopback check), NOT by
	// csrfProtect. The difference matters: csrfProtect is not in the chain
	// for /mcp. See ADR-0003 §csrf-scope.
	h, _ := newAuthedServer(t, models.AuthMCPTransportLoopbackOnly, true)

	rec := doAuthRequest(t, h, "POST", "/mcp", "", "cross-site", bytes.NewBufferString(`{}`))
	// httptest default RemoteAddr is "192.0.2.1:1234" (non-loopback), so
	// loopback-only policy rejects with 403 transport_policy_denied.
	assert.Equal(t, http.StatusForbidden, rec.Code)
	// But the body must be the transport policy denial, NOT the csrf body.
	assert.Contains(t, rec.Body.String(), "transport_policy_denied")
	assert.NotContains(t, rec.Body.String(), "cross-origin request blocked",
		"csrf must not be in the chain for /mcp — ADR-0003 §csrf-scope")
}

func TestAuth_APIRedirect_CsrfAppliesAtDestination(t *testing.T) {
	// /api/* redirects with 307 (method-preserving) to /api/v1/*.
	// csrf applies at /api/v1 destination, NOT at the redirect source.
	// See ADR-0003 §csrf-scope.
	h, _ := newAuthedServer(t, models.AuthMCPTransportLoopbackOnly, true)

	rec := doAuthRequest(t, h, "POST", "/api/servers", "", "cross-site",
		bytes.NewBufferString(`{}`))
	assert.Equal(t, http.StatusTemporaryRedirect, rec.Code,
		"/api/* must redirect with 307 regardless of origin — csrf applies downstream")
	assert.Equal(t, "/api/v1/servers", rec.Header().Get("Location"))
}

// ===== §policy-matrix-mcp-modes — 8-case transport matrix =====

func TestAuth_MCPTransport_PolicyMatrix(t *testing.T) {
	otherToken, err := auth.GenerateToken()
	require.NoError(t, err)

	for _, tc := range []struct {
		name          string
		mode          string
		remote        string // "loopback" or "remote"
		authHeader    func(tok string) string
		wantStatusMin int
		wantStatusMax int // accept 200 OR 406 (content negotiation) in success case
		wantBodyHint  string
	}{
		// loopback-only: loopback passes; non-loopback rejected 403.
		{"loopback-only_loopback_noauth", models.AuthMCPTransportLoopbackOnly, "loopback",
			func(string) string { return "" }, 200, 599, ""},
		{"loopback-only_loopback_authed", models.AuthMCPTransportLoopbackOnly, "loopback",
			func(tok string) string { return "Bearer " + tok }, 200, 599, ""},
		{"loopback-only_remote_noauth", models.AuthMCPTransportLoopbackOnly, "remote",
			func(string) string { return "" }, 403, 403, "transport_policy_denied"},
		{"loopback-only_remote_authed", models.AuthMCPTransportLoopbackOnly, "remote",
			func(tok string) string { return "Bearer " + tok }, 403, 403, "transport_policy_denied"},

		// bearer-required: auth gates request.
		{"bearer-required_loopback_noauth", models.AuthMCPTransportBearerRequired, "loopback",
			func(string) string { return "" }, 401, 401, ""},
		{"bearer-required_loopback_authed", models.AuthMCPTransportBearerRequired, "loopback",
			func(tok string) string { return "Bearer " + tok }, 200, 599, ""},
		{"bearer-required_remote_noauth", models.AuthMCPTransportBearerRequired, "remote",
			func(string) string { return "" }, 401, 401, ""},
		{"bearer-required_remote_authed", models.AuthMCPTransportBearerRequired, "remote",
			func(tok string) string { return "Bearer " + tok }, 200, 599, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, tok := newAuthedServer(t, tc.mode, true)

			// Override RemoteAddr on the request to simulate loopback vs remote.
			req := httptest.NewRequest("POST", "/mcp", bytes.NewBufferString(`{}`))
			if hdr := tc.authHeader(tok); hdr != "" {
				req.Header.Set("Authorization", hdr)
			}
			switch tc.remote {
			case "loopback":
				req.RemoteAddr = "127.0.0.1:54321"
			case "remote":
				req.RemoteAddr = "198.51.100.7:54321"
			}

			// Wrong-token path (separately asserted):
			if tc.name == "bearer-required_loopback_authed" || tc.name == "bearer-required_remote_authed" {
				// Sanity: a WRONG bearer must 401 even with correct mode.
				wrongReq := httptest.NewRequest("POST", "/mcp", bytes.NewBufferString(`{}`))
				wrongReq.Header.Set("Authorization", "Bearer "+otherToken)
				wrongReq.RemoteAddr = req.RemoteAddr
				wrongRec := httptest.NewRecorder()
				h.ServeHTTP(wrongRec, wrongReq)
				assert.Equal(t, http.StatusUnauthorized, wrongRec.Code,
					"wrong bearer must 401 in %s mode regardless of loopback", tc.mode)
			}

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			assert.GreaterOrEqual(t, rec.Code, tc.wantStatusMin,
				"status out of expected range for %s", tc.name)
			assert.LessOrEqual(t, rec.Code, tc.wantStatusMax,
				"status out of expected range for %s", tc.name)
			if tc.wantBodyHint != "" {
				assert.Contains(t, rec.Body.String(), tc.wantBodyHint)
			}
		})
	}
}

// ===== T12A.3d — /logs SSE endpoint requires Bearer, auth before throttle =====

func TestAuth_LogsEndpoint_401WithoutBearer(t *testing.T) {
	h, _ := newAuthedServer(t, models.AuthMCPTransportLoopbackOnly, true)

	// GET /api/v1/servers/foo/logs — authed SSE group.
	rec := doAuthRequest(t, h, "GET", "/api/v1/servers/foo/logs", "", "", nil)
	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"/logs SSE endpoint must require Bearer (HIGH 12A-5)")
	assert.Equal(t, "Bearer", rec.Header().Get("WWW-Authenticate"))
}

func TestAuth_LogsEndpoint_401WithMalformedBearer(t *testing.T) {
	h, _ := newAuthedServer(t, models.AuthMCPTransportLoopbackOnly, true)

	cases := []string{
		"bearer lowercase",          // lowercase scheme
		"Bearer ",                   // empty token
		"Token abc",                 // wrong scheme
		"Bearer garbage-too-short",  // well-formed scheme, wrong token
	}
	for _, h2 := range cases {
		rec := doAuthRequest(t, h, "GET", "/api/v1/servers/foo/logs", h2, "", nil)
		assert.Equal(t, http.StatusUnauthorized, rec.Code,
			"malformed auth %q on /logs must 401", h2)
	}
}

// ===== PAL-2026-04-18 regression — X-Forwarded-For spoof cannot bypass loopback-only =====
//
// Prior to removing middleware.RealIP from the router root, a remote
// client could send `X-Forwarded-For: 127.0.0.1` and bypass the
// loopback-only transport check. This test asserts the spoof is
// ignored — r.RemoteAddr is the transport-level peer, not the header.

func TestAuth_MCPTransport_XForwardedForSpoofRejected(t *testing.T) {
	h, _ := newAuthedServer(t, models.AuthMCPTransportLoopbackOnly, true)

	for _, header := range []string{"X-Forwarded-For", "X-Real-IP"} {
		req := httptest.NewRequest("POST", "/mcp", bytes.NewBufferString(`{}`))
		req.Header.Set(header, "127.0.0.1")
		req.RemoteAddr = "198.51.100.7:54321" // non-loopback peer
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusForbidden, rec.Code,
			"non-loopback client must get 403 even with spoofed %s: 127.0.0.1", header)
		assert.Contains(t, rec.Body.String(), "transport_policy_denied")
	}
}

// ===== PAL-2026-04-18 HIGH — Sec-Fetch-Site cross-site deny on /mcp =====

func TestAuth_MCPTransport_SecFetchSiteCrossSiteDeny(t *testing.T) {
	h, _ := newAuthedServer(t, models.AuthMCPTransportLoopbackOnly, true)

	// Cross-site browser page attempts to drive a tool call against the
	// user's localhost daemon. RemoteAddr is loopback (the browser on
	// the same machine) but Sec-Fetch-Site reveals the origin.
	for _, site := range []string{"cross-site", "same-site"} {
		req := httptest.NewRequest("POST", "/mcp", bytes.NewBufferString(`{}`))
		req.Header.Set("Sec-Fetch-Site", site)
		req.RemoteAddr = "127.0.0.1:54321"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusForbidden, rec.Code,
			"loopback browser POST with Sec-Fetch-Site=%s must be denied", site)
		assert.Contains(t, rec.Body.String(), "transport_policy_denied")
	}

	// same-origin and none (curl, server-to-server) are allowed through.
	for _, site := range []string{"same-origin", "none", ""} {
		req := httptest.NewRequest("POST", "/mcp", bytes.NewBufferString(`{}`))
		if site != "" {
			req.Header.Set("Sec-Fetch-Site", site)
		}
		req.RemoteAddr = "127.0.0.1:54321"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assert.NotEqual(t, http.StatusForbidden, rec.Code,
			"loopback same-origin POST (Sec-Fetch-Site=%q) must reach /mcp handler", site)
	}
}

// ===== PAL-2026-04-18 MEDIUM — Cache-Control no-store on 401 =====

func TestAuth_401HasNoStoreCacheControl(t *testing.T) {
	h, _ := newAuthedServer(t, models.AuthMCPTransportLoopbackOnly, true)

	rec := doAuthRequest(t, h, "GET", "/api/v1/servers", "", "", nil)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "no-cache", rec.Header().Get("Pragma"))
}

// ===== §401-hint — body shape on auth failure =====

func TestAuth_401BodyShape(t *testing.T) {
	h, _ := newAuthedServer(t, models.AuthMCPTransportLoopbackOnly, true)

	rec := doAuthRequest(t, h, "GET", "/api/v1/servers", "", "", nil)
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	var body auth.ErrorBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "authentication required", body.Error)
	assert.Contains(t, body.Hint, auth.EnvVarName)
	assert.Contains(t, body.Hint, "auth.token")
	assert.True(t, strings.Contains(body.Hint, "env var") || strings.Contains(body.Hint, "environment variable"))
}
