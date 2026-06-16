package api

import (
	"net/http"
	"testing"

	"mcp-gateway/internal/models"

	"github.com/stretchr/testify/assert"
)

// The sessionless-GET storm guard (anthropics/claude-code#57642) must reject a
// GET that opens the MCP stream with no Mcp-Session-Id, cheaply, under EVERY
// transport policy — it is a protocol-level error, not a policy decision. These
// tests pin that the guard fires above the policy switch so it covers
// bearer-required mode too (regression: it previously lived only in the
// loopback-only branch).

func TestStormGuard_SessionlessGet_LoopbackMode_400(t *testing.T) {
	h, _ := newAuthedServer(t, models.AuthMCPTransportLoopbackOnly, false)
	rec := doAuthRequest(t, h, http.MethodGet, "/mcp", "", "", nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code,
		"sessionless GET /mcp must be rejected 400 in loopback-only mode")
}

func TestStormGuard_SessionlessGet_BearerMode_400_NoBearer(t *testing.T) {
	h, _ := newAuthedServer(t, models.AuthMCPTransportBearerRequired, true)
	// No Authorization header: the protocol guard fires before auth, so the
	// hot-loop is rejected 400 cheaply rather than reaching the auth layer.
	rec := doAuthRequest(t, h, http.MethodGet, "/mcp", "", "", nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code,
		"sessionless GET /mcp must be rejected 400 in bearer-required mode")
}

func TestStormGuard_SessionlessGet_BearerMode_400_WithBearer(t *testing.T) {
	h, token := newAuthedServer(t, models.AuthMCPTransportBearerRequired, true)
	rec := doAuthRequest(t, h, http.MethodGet, "/mcp", "Bearer "+token, "", nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code,
		"authenticated sessionless GET /mcp must still be rejected 400 (storm guard)")
}
