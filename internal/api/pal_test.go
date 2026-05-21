package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mcp-gateway/internal/health"
	"mcp-gateway/internal/lifecycle"
	"mcp-gateway/internal/models"
	"mcp-gateway/internal/proxy"

	"github.com/stretchr/testify/assert"
)

// newAuthedServerForPal builds an admin-authed server for PAL endpoint
// tests. Mirrors newAuthedServerWithShutdown but does not wire shutdownFn
// (PAL tests do not exercise shutdown).
func newAuthedServerForPal(t *testing.T) (http.Handler, string, string) {
	t.Helper()
	cfg := &models.Config{Servers: make(map[string]*models.ServerConfig)}
	cfg.ApplyDefaults()

	lm := lifecycle.NewManager(cfg, "test", testLogger())
	gw := proxy.New(cfg, lm, "test", testLogger())
	mon := health.NewMonitor(lm, 0, testLogger())

	regularToken := "regular-bearer-pal"
	adminToken := "admin-bearer-pal"
	srv := NewServer(lm, gw, mon, cfg, "", testLogger(),
		AuthConfig{
			Enabled:      true,
			Token:        regularToken,
			AdminEnabled: true,
			AdminToken:   adminToken,
		}, "test")
	return srv.Handler(), regularToken, adminToken
}

// TestPalQueueReview_NoAuth_Returns401 — FM-34 auth guard.
// Without an Authorization header, /api/v1/pal/queue_review must reject
// with 401. Same admin-scope contract as /shutdown.
func TestPalQueueReview_NoAuth_Returns401(t *testing.T) {
	h, _, _ := newAuthedServerForPal(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/pal/queue_review",
		strings.NewReader(`{"gate_type":"codereview","prompt":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code,
		"PAL queue_review without auth must 401")
	assert.Equal(t, "Bearer", rr.Header().Get("WWW-Authenticate"))
}

// TestPalQueueReview_RegularBearer_Returns401 — FM-34 scope-isolation guard.
// PAL queue endpoints are admin-scoped (ADR-0007); presenting the regular
// Bearer must yield 401. Without this guard, VSCode's McpGatewayService —
// which only knows the regular Bearer via plugin .mcp.json — would have
// an unintended path into the PAL dispatch surface.
func TestPalQueueReview_RegularBearer_Returns401(t *testing.T) {
	h, regularToken, _ := newAuthedServerForPal(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/pal/queue_review",
		strings.NewReader(`{"gate_type":"codereview","prompt":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+regularToken)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code,
		"regular Bearer must NOT satisfy admin-scope /pal/queue_review")
}

// TestPalQueueReview_OrchestratorMissing_Returns503 — FM-34 graceful-fail.
// When the orchestrator backend is not registered, the recovery surface is
// unavailable. Returning 503 (not 500, not 404) signals "recovery path
// requires the orchestrator backend in gateway config" — operator-actionable
// guidance instead of an opaque dispatch failure.
func TestPalQueueReview_OrchestratorMissing_Returns503(t *testing.T) {
	h, _, adminToken := newAuthedServerForPal(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/pal/queue_review",
		strings.NewReader(`{"gate_type":"codereview","prompt":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+adminToken)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusServiceUnavailable, rr.Code,
		"PAL queue_review without orchestrator backend must 503")
	assert.Contains(t, rr.Body.String(), "orchestrator backend is not registered",
		"503 body must explain the missing prerequisite")
}

// TestPalGetReview_AdminAuth_OrchestratorMissing_Returns503 — FM-34 GET
// path coverage. Verifies that:
//  1. chi.URLParam("task_id") routes the path segment correctly
//  2. admin auth flows the same way as on POST
//  3. graceful-fail on missing orchestrator matches the POST path
//
// Combining all three in a single test keeps the suite size at the planned
// 4 tests while exercising every distinct edge of the GET path that
// is not already covered by the POST tests above.
func TestPalGetReview_AdminAuth_OrchestratorMissing_Returns503(t *testing.T) {
	h, _, adminToken := newAuthedServerForPal(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pal/get_review/rev-abc123", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusServiceUnavailable, rr.Code,
		"PAL get_review without orchestrator backend must 503 (URL param + auth must succeed first)")
	assert.Contains(t, rr.Body.String(), "orchestrator backend is not registered")

	// Companion: no-auth on GET must 401 (separate sub-check covered by
	// the route-group level guard but asserted here to lock the contract
	// for both verbs in one test file).
	reqNoAuth := httptest.NewRequest(http.MethodGet, "/api/v1/pal/get_review/rev-abc123", nil)
	rrNoAuth := httptest.NewRecorder()
	h.ServeHTTP(rrNoAuth, reqNoAuth)
	assert.Equal(t, http.StatusUnauthorized, rrNoAuth.Code,
		"PAL get_review without auth must 401")
}
