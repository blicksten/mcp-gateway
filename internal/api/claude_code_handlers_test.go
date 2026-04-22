package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"mcp-gateway/internal/patchstate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ccTestBearer is the Bearer token used by claude-code handler tests.
const ccTestBearer = "test-claude-code-bearer"

// setupClaudeCodeServer returns a Server wired with an in-memory patchstate
// (persistPath empty to skip disk I/O) and Bearer auth enabled. Callers
// that need persistence pass a path and call setupClaudeCodeServerWithPath.
func setupClaudeCodeServer(t *testing.T) (*Server, *patchstate.State) {
	t.Helper()
	return setupClaudeCodeServerWithPath(t, "")
}

func setupClaudeCodeServerWithPath(t *testing.T, persistPath string) (*Server, *patchstate.State) {
	t.Helper()
	srv, _ := setupTestServer(t)
	srv.authEnabled = true
	srv.authToken = ccTestBearer
	ps := patchstate.New(persistPath, testLogger())
	srv.SetPatchState(ps)
	srv.InitClaudeCodeLimiters()
	// Bump heartbeat limiter budget so a test hammering the endpoint with
	// fresh session IDs isn't throttled by the per-session bucket quota.
	return srv, ps
}

func doClaudeCodeRequest(t *testing.T, h http.Handler, method, path, origin string, body any, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	var br *bytes.Buffer
	if body != nil {
		data, err := json.Marshal(body)
		require.NoError(t, err)
		br = bytes.NewBuffer(data)
	} else {
		br = &bytes.Buffer{}
	}
	req := httptest.NewRequest(method, path, br)
	req.Header.Set("Content-Type", "application/json")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestHeartbeatStoreAndRetrieve(t *testing.T) {
	srv, _ := setupClaudeCodeServer(t)
	h := srv.Handler()

	hb := patchstate.Heartbeat{
		SessionID:    "sess-alpha",
		PatchVersion: "1.0.0",
		CCVersion:    "2.0.0",
		FiberOK:      true,
		MCPMethodOK:  true,
		Timestamp:    time.Now().Unix(),
	}
	rr := doClaudeCodeRequest(t, h, "POST", "/api/v1/claude-code/patch-heartbeat", "", hb, ccTestBearer)
	require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())

	var resp heartbeatResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.True(t, resp.Acked)
	assert.Greater(t, resp.NextHeartbeatInMs, 0)
	// A-FIN-04 (architect review, checkpoint-finish-44f45055): ensure the
	// heartbeatResponse wire shape round-trips cleanly even when
	// ConfigOverride is nil (v1.6.0 default). The FROZEN contract
	// (docs/api/claude-code-endpoints.md) declares config_override as
	// optional; the `omitempty` tag must keep it out of the JSON payload.
	// Re-marshal the decoded response and confirm the string doesn't
	// contain the key — prevents future refactors from accidentally
	// emitting `"config_override":null`, which would exercise a different
	// code path in the webview patch's CONFIG merger.
	roundTrip, err := json.Marshal(resp)
	require.NoError(t, err)
	assert.NotContains(t, string(roundTrip), "config_override",
		"config_override must stay out of the payload when the server has nothing to push (omitempty contract)")
	assert.Nil(t, resp.ConfigOverride, "default response must not populate ConfigOverride")

	// GET /patch-status returns our heartbeat.
	rr2 := doClaudeCodeRequest(t, h, "GET", "/api/v1/claude-code/patch-status", "", nil, ccTestBearer)
	require.Equal(t, http.StatusOK, rr2.Code)
	var list []*patchstate.Heartbeat
	require.NoError(t, json.Unmarshal(rr2.Body.Bytes(), &list))
	require.Len(t, list, 1)
	assert.Equal(t, "sess-alpha", list[0].SessionID)
}

func TestPendingActionsFIFO(t *testing.T) {
	srv, ps := setupClaudeCodeServer(t)
	h := srv.Handler()

	a1, ok := ps.EnqueueReconnectAction("mcp-gateway")
	require.True(t, ok)
	// 100 ms margin keeps this stable on slow CI runners; patchstate unit
	// tests exercise the debounce window with a fake clock, so this test's
	// sole purpose is the HTTP/JSON round-trip — not precision timing.
	time.Sleep(patchstate.ReconnectActionDebounce + 100*time.Millisecond)
	a2, ok := ps.EnqueueReconnectAction("mcp-gateway")
	require.True(t, ok)

	// GET /pending-actions returns both in FIFO order.
	rr := doClaudeCodeRequest(t, h, "GET", "/api/v1/claude-code/pending-actions", "", nil, ccTestBearer)
	require.Equal(t, http.StatusOK, rr.Code)
	var list []*patchstate.PendingAction
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &list))
	require.Len(t, list, 2)
	assert.Equal(t, a1.ID, list[0].ID)
	assert.Equal(t, a2.ID, list[1].ID)

	// Ack a1, GET now returns only a2.
	rr2 := doClaudeCodeRequest(t, h, "POST", "/api/v1/claude-code/pending-actions/"+a1.ID+"/ack", "", nil, ccTestBearer)
	require.Equal(t, http.StatusOK, rr2.Code)

	rr3 := doClaudeCodeRequest(t, h, "GET", "/api/v1/claude-code/pending-actions", "", nil, ccTestBearer)
	require.Equal(t, http.StatusOK, rr3.Code)
	var list2 []*patchstate.PendingAction
	require.NoError(t, json.Unmarshal(rr3.Body.Bytes(), &list2))
	require.Len(t, list2, 1)
	assert.Equal(t, a2.ID, list2[0].ID)

	// Ack unknown id returns 404.
	rr4 := doClaudeCodeRequest(t, h, "POST", "/api/v1/claude-code/pending-actions/unknown-id/ack", "", nil, ccTestBearer)
	assert.Equal(t, http.StatusNotFound, rr4.Code)
}

func TestProbeResultTTL(t *testing.T) {
	srv, ps := setupClaudeCodeServer(t)
	h := srv.Handler()

	// Enqueue probe, record result, verify the patchstate stored it.
	act, err := ps.EnqueueProbeAction()
	require.NoError(t, err)

	result := patchstate.ProbeResult{
		Nonce: act.Nonce,
		OK:    false,
		Error: "server not found",
	}
	rr := doClaudeCodeRequest(t, h, "POST", "/api/v1/claude-code/probe-result", "", result, ccTestBearer)
	require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())

	pr := ps.ProbeResult(act.Nonce)
	require.NotNil(t, pr)
	assert.False(t, pr.OK)

	// Empty-nonce POST → 400.
	bad := patchstate.ProbeResult{OK: true}
	rr2 := doClaudeCodeRequest(t, h, "POST", "/api/v1/claude-code/probe-result", "", bad, ccTestBearer)
	assert.Equal(t, http.StatusBadRequest, rr2.Code)
}

func TestClaudeCodeRoutesBearerRequired(t *testing.T) {
	srv, _ := setupClaudeCodeServer(t)
	h := srv.Handler()

	endpoints := []struct {
		method string
		path   string
		body   any
	}{
		{"POST", "/api/v1/claude-code/patch-heartbeat", patchstate.Heartbeat{SessionID: "s"}},
		{"GET", "/api/v1/claude-code/patch-status", nil},
		{"GET", "/api/v1/claude-code/pending-actions", nil},
		{"POST", "/api/v1/claude-code/pending-actions/abc/ack", nil},
		{"POST", "/api/v1/claude-code/probe-trigger", map[string]string{"nonce": "dashboard-nonce-1234abcd"}},
		{"POST", "/api/v1/claude-code/probe-result", patchstate.ProbeResult{Nonce: "n"}},
		{"POST", "/api/v1/claude-code/plugin-sync", nil},
		{"GET", "/api/v1/claude-code/compat-matrix", nil},
	}
	for _, e := range endpoints {
		rr := doClaudeCodeRequest(t, h, e.method, e.path, "", e.body, "")
		assert.Equal(t, http.StatusUnauthorized, rr.Code, "%s %s should require bearer", e.method, e.path)
	}
}

func TestCORSVSCodeWebview(t *testing.T) {
	srv, _ := setupClaudeCodeServer(t)
	h := srv.Handler()

	origin := "vscode-webview://abc-123-guid"
	hb := patchstate.Heartbeat{SessionID: "s"}
	rr := doClaudeCodeRequest(t, h, "POST", "/api/v1/claude-code/patch-heartbeat", origin, hb, ccTestBearer)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, origin, rr.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "Origin", rr.Header().Get("Vary"))
}

func TestCORSWebExternal(t *testing.T) {
	srv, _ := setupClaudeCodeServer(t)
	h := srv.Handler()

	// Untrusted origin: request still succeeds (no cookie-auth here; browsers
	// enforce origin on the client side), but the Allow-Origin header is
	// absent — browser-side fetch will block the response.
	origin := "https://evil.com"
	hb := patchstate.Heartbeat{SessionID: "s"}
	rr := doClaudeCodeRequest(t, h, "POST", "/api/v1/claude-code/patch-heartbeat", origin, hb, ccTestBearer)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Empty(t, rr.Header().Get("Access-Control-Allow-Origin"),
		"untrusted origin must not get Access-Control-Allow-Origin header")
}

func TestClaudeCodeCORSPreflight(t *testing.T) {
	srv, _ := setupClaudeCodeServer(t)
	h := srv.Handler()

	origin := "vscode-webview://abc-123"
	// Preflight has NO Authorization header — must succeed BEFORE auth.
	rr := doClaudeCodeRequest(t, h, "OPTIONS", "/api/v1/claude-code/patch-heartbeat", origin, nil, "")
	require.Equal(t, http.StatusNoContent, rr.Code)
	assert.Equal(t, origin, rr.Header().Get("Access-Control-Allow-Origin"))
	assert.Contains(t, rr.Header().Get("Access-Control-Allow-Methods"), "POST")
	assert.Contains(t, rr.Header().Get("Access-Control-Allow-Headers"), "Authorization")
	assert.Equal(t, "300", rr.Header().Get("Access-Control-Max-Age"))

	// Unknown origin OPTIONS: 204 without Allow-* headers (preflight fails
	// client-side).
	rr2 := doClaudeCodeRequest(t, h, "OPTIONS", "/api/v1/claude-code/patch-heartbeat", "https://evil.com", nil, "")
	assert.Equal(t, http.StatusNoContent, rr2.Code)
	assert.Empty(t, rr2.Header().Get("Access-Control-Allow-Origin"))
}

func TestActionDebounce(t *testing.T) {
	_, ps := setupClaudeCodeServer(t)

	// Two EnqueueReconnectAction calls within 500 ms coalesce into one.
	a1, ok1 := ps.EnqueueReconnectAction("mcp-gateway")
	require.True(t, ok1)
	require.NotNil(t, a1)

	a2, ok2 := ps.EnqueueReconnectAction("mcp-gateway")
	assert.False(t, ok2, "second enqueue inside debounce should coalesce")
	assert.Nil(t, a2)

	list := ps.PendingActions("")
	require.Len(t, list, 1)
	assert.Equal(t, a1.ID, list[0].ID)
}

func TestPatchStatePersistenceRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "patch-state.json")

	srv1, ps1 := setupClaudeCodeServerWithPath(t, path)
	h1 := srv1.Handler()

	hb := patchstate.Heartbeat{SessionID: "sess-persist", PatchVersion: "1.0.0"}
	rr := doClaudeCodeRequest(t, h1, "POST", "/api/v1/claude-code/patch-heartbeat", "", hb, ccTestBearer)
	require.Equal(t, http.StatusOK, rr.Code)

	// Enqueue an action so both maps hit the persisted file.
	_, ok := ps1.EnqueueReconnectAction("mcp-gateway")
	require.True(t, ok)

	// Flush pending async persists before simulating restart.
	ps1.FlushPersists()

	// Simulate daemon restart: construct a fresh State on the same path.
	ps2 := patchstate.New(path, testLogger())
	require.NoError(t, ps2.Load())
	hbs := ps2.Heartbeats()
	require.Len(t, hbs, 1)
	assert.Equal(t, "sess-persist", hbs[0].SessionID)
	acts := ps2.PendingActions("")
	require.Len(t, acts, 1)
	assert.Equal(t, "reconnect", acts[0].Type)
	assert.Equal(t, "mcp-gateway", acts[0].ServerName)
}

func TestHeartbeatRejectsEmptySession(t *testing.T) {
	srv, _ := setupClaudeCodeServer(t)
	h := srv.Handler()

	hb := patchstate.Heartbeat{PatchVersion: "1.0.0"} // no session_id
	rr := doClaudeCodeRequest(t, h, "POST", "/api/v1/claude-code/patch-heartbeat", "", hb, ccTestBearer)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHeartbeatRateLimit_Returns429AfterThreshold(t *testing.T) {
	// REVIEW-16.3 M-03 regression: exhaust the per-session heartbeat budget
	// and verify the gateway returns 429 with Retry-After.
	srv, _ := setupClaudeCodeServer(t)
	// Downsize limiter for a fast test (default 5/min; squeeze to 2 so we
	// don't have to fire 6 requests and still prove the path).
	srv.heartbeatLimiter = newRateLimiter(2, sessionKey)
	h := srv.Handler()

	hb := patchstate.Heartbeat{SessionID: "sess-rl"}
	// First two must succeed.
	for range 2 {
		rr := doClaudeCodeRequest(t, h, "POST", "/api/v1/claude-code/patch-heartbeat", "", hb, ccTestBearer)
		require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())
	}
	// Third inside same minute: 429.
	rr := doClaudeCodeRequest(t, h, "POST", "/api/v1/claude-code/patch-heartbeat", "", hb, ccTestBearer)
	require.Equal(t, http.StatusTooManyRequests, rr.Code, "body=%s", rr.Body.String())
	assert.Equal(t, "60", rr.Header().Get("Retry-After"))
}

func TestPendingActionsRateLimit_Returns429AfterThreshold(t *testing.T) {
	srv, _ := setupClaudeCodeServer(t)
	srv.pendingActionsLimiter = newRateLimiter(2, ipKey)
	h := srv.Handler()

	for range 2 {
		rr := doClaudeCodeRequest(t, h, "GET", "/api/v1/claude-code/pending-actions", "", nil, ccTestBearer)
		require.Equal(t, http.StatusOK, rr.Code)
	}
	rr := doClaudeCodeRequest(t, h, "GET", "/api/v1/claude-code/pending-actions", "", nil, ccTestBearer)
	require.Equal(t, http.StatusTooManyRequests, rr.Code)
	assert.Equal(t, "60", rr.Header().Get("Retry-After"))
}

func TestProbeTrigger_EnqueuesAction(t *testing.T) {
	srv, ps := setupClaudeCodeServer(t)
	h := srv.Handler()

	nonce := "dashboard-nonce-1234abcd" // ≥ 16 chars per FROZEN contract
	req := map[string]string{"nonce": nonce}
	rr := doClaudeCodeRequest(t, h, "POST", "/api/v1/claude-code/probe-trigger", "", req, ccTestBearer)
	require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())

	var resp map[string]string
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, "enqueued", resp["status"])
	assert.NotEmpty(t, resp["action_id"])

	list := ps.PendingActions("")
	require.Len(t, list, 1)
	assert.Equal(t, "probe-reconnect", list[0].Type)
	assert.Equal(t, nonce, list[0].Nonce)
	assert.Contains(t, list[0].ServerName, "__probe_nonexistent_")
}

func TestProbeTrigger_RejectsShortNonce(t *testing.T) {
	srv, _ := setupClaudeCodeServer(t)
	h := srv.Handler()

	req := map[string]string{"nonce": "tooshort"} // < 16 chars
	rr := doClaudeCodeRequest(t, h, "POST", "/api/v1/claude-code/probe-trigger", "", req, ccTestBearer)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestPluginSync_ReturnsConflictWhenPluginDirUnset(t *testing.T) {
	// Default setupClaudeCodeServer leaves pluginDir empty → 409.
	srv, _ := setupClaudeCodeServer(t)
	h := srv.Handler()

	rr := doClaudeCodeRequest(t, h, "POST", "/api/v1/claude-code/plugin-sync", "", nil, ccTestBearer)
	assert.Equal(t, http.StatusConflict, rr.Code)
}

func TestCompatMatrix_Returns503WhenFileMissing(t *testing.T) {
	// Default project directory has no configs/supported_claude_code_versions.json
	// at this phase — the file is authored in Phase 16.4.7. Handler returns
	// 503 with a descriptive error so the dashboard distinguishes
	// "matrix not yet configured" from "bad URL".
	srv, _ := setupClaudeCodeServer(t)
	h := srv.Handler()

	rr := doClaudeCodeRequest(t, h, "GET", "/api/v1/claude-code/compat-matrix", "", nil, ccTestBearer)
	// The test binary's working directory is the package directory
	// (internal/api); configs/ does not exist there. Either 503 (missing
	// file) or 200 (file exists because this test ran in a checkout where
	// Phase 16.4.7 has landed). Accept both, assert body is JSON when 200.
	if rr.Code == http.StatusOK {
		var body map[string]any
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
		return
	}
	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestHandlersReturn503WhenPatchStateUnset(t *testing.T) {
	srv, _ := setupTestServer(t)
	srv.authEnabled = true
	srv.authToken = ccTestBearer
	// Intentionally skip SetPatchState/InitClaudeCodeLimiters.
	h := srv.Handler()

	rr := doClaudeCodeRequest(t, h, "GET", "/api/v1/claude-code/patch-status", "", nil, ccTestBearer)
	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)

	hb := patchstate.Heartbeat{SessionID: "s"}
	rr2 := doClaudeCodeRequest(t, h, "POST", "/api/v1/claude-code/patch-heartbeat", "", hb, ccTestBearer)
	assert.Equal(t, http.StatusServiceUnavailable, rr2.Code)
}
