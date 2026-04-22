package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"mcp-gateway/internal/patchstate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Phase 16.7 T16.7.2 — Integration-level assertions for the CORS policy
// on /api/v1/claude-code/* routes. Regular Go test (no build tag) so it
// runs as part of the default `go test ./...` surface. The narrower
// handler unit tests in claude_code_handlers_test.go already prove the
// middleware logic; this file asserts the end-to-end wiring is correct
// after route registration (a regression here indicates someone reordered
// `r.Use(claudeCodeCORS)` and `r.Use(authMW)` in server.go Handler()).
//
// REVIEW-16 L-02 coverage:
//   - OPTIONS preflight runs BEFORE bearer auth → 204 without Authorization.
//   - Exact origin echo for vscode-webview://<uuid> — never wildcard.
//   - Unknown origin → no Allow-* headers (browser will block).

const integrationCORSBearer = "integration-cors-bearer-token"

func setupCORSIntegrationServer(t *testing.T) *Server {
	t.Helper()
	srv, _ := setupTestServer(t)
	srv.authEnabled = true
	srv.authToken = integrationCORSBearer
	ps := patchstate.New("", nil)
	srv.SetPatchState(ps)
	srv.InitClaudeCodeLimiters()
	return srv
}

// TestIntegration_CORS_PreflightAllowsVSCodeWebview simulates the browser's
// OPTIONS preflight. Browser behaviour: send OPTIONS with `Origin:
// vscode-webview://<guid>` and `Access-Control-Request-Method: POST`
// BEFORE the actual POST. No `Authorization` header on preflight per CORS
// spec — so the middleware must short-circuit before authMW runs.
func TestIntegration_CORS_PreflightAllowsVSCodeWebview(t *testing.T) {
	srv := setupCORSIntegrationServer(t)
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/claude-code/patch-heartbeat", nil)
	req.Header.Set("Origin", "vscode-webview://abc-123-def-456")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "authorization, content-type")
	// Deliberately NO Authorization — preflight must pass.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	require.Equal(t, http.StatusNoContent, rr.Code,
		"preflight must return 204, got %d body=%s", rr.Code, rr.Body.String())
	assert.Equal(t, "vscode-webview://abc-123-def-456", rr.Header().Get("Access-Control-Allow-Origin"),
		"must echo exact origin, not wildcard")
	assert.Equal(t, "Origin", rr.Header().Get("Vary"),
		"Vary: Origin required because response varies with the Origin header value")
	assert.Contains(t, rr.Header().Get("Access-Control-Allow-Methods"), "POST")
	assert.Contains(t, rr.Header().Get("Access-Control-Allow-Headers"), "Authorization")
	assert.Equal(t, "300", rr.Header().Get("Access-Control-Max-Age"))
}

// TestIntegration_CORS_PreflightDeniesExternalOrigin verifies an attacker
// origin never receives the Allow-* headers. The 204 response itself is
// harmless — it's the ABSENCE of Allow-Origin that causes the browser to
// reject the subsequent fetch client-side.
func TestIntegration_CORS_PreflightDeniesExternalOrigin(t *testing.T) {
	srv := setupCORSIntegrationServer(t)
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/claude-code/patch-heartbeat", nil)
	req.Header.Set("Origin", "https://evil.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	require.Equal(t, http.StatusNoContent, rr.Code)
	assert.Empty(t, rr.Header().Get("Access-Control-Allow-Origin"),
		"attacker origin MUST NOT receive Access-Control-Allow-Origin")
	assert.Empty(t, rr.Header().Get("Access-Control-Allow-Methods"))
}

// TestIntegration_CORS_ActualPostEchoesOrigin verifies the response to the
// actual POST (not preflight) also echoes the origin, required by the
// browser to accept the response body under credentials mode.
func TestIntegration_CORS_ActualPostEchoesOrigin(t *testing.T) {
	srv := setupCORSIntegrationServer(t)
	h := srv.Handler()

	hb := patchstate.Heartbeat{SessionID: "integration-cors"}
	body, err := json.Marshal(hb)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/claude-code/patch-heartbeat", bytes.NewBuffer(body))
	req.Header.Set("Origin", "vscode-webview://abc-123")
	req.Header.Set("Authorization", "Bearer "+integrationCORSBearer)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())
	assert.Equal(t, "vscode-webview://abc-123", rr.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "Origin", rr.Header().Get("Vary"))
}

// TestIntegration_CORS_ActualPostFromExternalOriginOmitsEcho verifies an
// external-origin POST (if it ever reaches the server without the browser
// blocking it) succeeds on the server side but gets NO Allow-Origin back —
// so the browser client still blocks the response. This prevents the UI
// trap of "server returned data but browser couldn't read it" being
// indistinguishable from actual success.
func TestIntegration_CORS_ActualPostFromExternalOriginOmitsEcho(t *testing.T) {
	srv := setupCORSIntegrationServer(t)
	h := srv.Handler()

	hb := patchstate.Heartbeat{SessionID: "integration-cors-external"}
	body, err := json.Marshal(hb)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/claude-code/patch-heartbeat", bytes.NewBuffer(body))
	req.Header.Set("Origin", "https://evil.com")
	req.Header.Set("Authorization", "Bearer "+integrationCORSBearer)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	// Request body was well-formed + bearer valid — server processes it.
	// But no Allow-Origin → browser side blocks the response.
	require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())
	assert.Empty(t, rr.Header().Get("Access-Control-Allow-Origin"))
}

// TestIntegration_CORS_PreflightBeforeAuthOrdering is the regression guard
// for REVIEW-16 L-02 fix: OPTIONS must NOT go through authMW. If someone
// reorders the middleware chain in server.go Handler() such that authMW
// runs before claudeCodeCORS, this test fails with 401 instead of 204.
func TestIntegration_CORS_PreflightBeforeAuthOrdering(t *testing.T) {
	srv := setupCORSIntegrationServer(t)
	h := srv.Handler()

	// Preflight request with completely missing Authorization header.
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/claude-code/patch-heartbeat", nil)
	req.Header.Set("Origin", "vscode-webview://ordering-regression-test")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	// If auth ran before CORS, we'd see 401 — that's the bug this test guards.
	assert.NotEqual(t, http.StatusUnauthorized, rr.Code,
		"OPTIONS preflight must be answered by CORS middleware BEFORE authMW (REVIEW-16 L-02); ordering regression would return 401")
	assert.Equal(t, http.StatusNoContent, rr.Code)
}
