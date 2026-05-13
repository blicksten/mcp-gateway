package api

import (
	"bytes"
	"context"
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

func TestCompatMatrix_ReturnsEmbeddedJSON(t *testing.T) {
	// As of audit-e7618c9c Scope B SB-1 (commit 1b98785, 2026-05-06) the
	// compat matrix is //go:embed-ed into the daemon binary at compile
	// time. The handler now ALWAYS returns 200 with valid JSON because the
	// build-time embed guarantees presence.
	//
	// Previously (T16.4.7) the handler read configs/... via os.ReadFile
	// CWD-relative, and this test accepted BOTH 200 and 503 outcomes —
	// rationalising a path-resolution defect as documented behaviour
	// (audit SB-5 MEDIUM defect-as-feature anti-pattern). The dual-assert
	// is now removed: any 503 here is a regression of SB-1.
	srv, _ := setupClaudeCodeServer(t)
	h := srv.Handler()

	rr := doClaudeCodeRequest(t, h, "GET", "/api/v1/claude-code/compat-matrix", "", nil, ccTestBearer)
	require.Equal(t, http.StatusOK, rr.Code,
		"compat-matrix endpoint must return 200 with embedded JSON; any 503 is a regression of SB-1 (//go:embed bypass)")

	var body map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body),
		"compat-matrix response must be valid JSON")

	// The embedded matrix has a stable contract: top-level "min" + "max_tested"
	// keys plus optional "alt_e_verified_versions" array. Assert the
	// load-bearing fields rather than a deep-equal so the test survives
	// matrix data updates without code-side changes.
	if _, ok := body["min"]; !ok {
		t.Errorf("compat-matrix JSON missing required top-level key 'min'; got keys: %v", mapKeys(body))
	}
	if _, ok := body["max_tested"]; !ok {
		t.Errorf("compat-matrix JSON missing required top-level key 'max_tested'; got keys: %v", mapKeys(body))
	}
}

func mapKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
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

// --- Unfreeze-button endpoints (PLAN-unfreeze-button v3 T4) ------------

// mockUnfreezeExec installs a fake exec function for the duration of t.
// Restores the original on cleanup so test order does not matter.
func mockUnfreezeExec(t *testing.T, fn func(ctx context.Context, pid uint32) error) {
	t.Helper()
	orig := unfreezeExecFunc
	unfreezeExecFunc = fn
	t.Cleanup(func() { unfreezeExecFunc = orig })
}

// mockVerifyPid installs a fake image-name verifier for the duration of t.
// The default test installation returns true for all PIDs so tests do not
// need real claude.exe processes to register. Override selectively in tests
// that want to exercise the rejection path.
func mockVerifyPid(t *testing.T, fn func(pid uint32) bool) {
	t.Helper()
	orig := verifyClaudeExePidFunc
	verifyClaudeExePidFunc = fn
	t.Cleanup(func() { verifyClaudeExePidFunc = orig })
}

// init for tests — replace the real OpenProcess verifier with an always-true
// stub so all register-pid tests pass without requiring actual claude.exe PIDs.
func init() {
	verifyClaudeExePidFunc = func(_ uint32) bool { return true }
}

// TestRegisterPidHappyPath verifies the (a) case from plan v3 T4.
func TestRegisterPidHappyPath(t *testing.T) {
	srv, ps := setupClaudeCodeServer(t)
	h := srv.Handler()

	body := map[string]any{"session_id": "sess-reg-1", "pid": uint32(12345)}
	rr := doClaudeCodeRequest(t, h, "POST", "/api/v1/claude-code/register-pid", "", body, ccTestBearer)
	require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())

	var resp registerPidResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.True(t, resp.Stored)

	stored, ok := ps.GetSessionPid("sess-reg-1")
	require.True(t, ok, "patchState must hold the registered PID")
	assert.Equal(t, uint32(12345), stored.PID)
	assert.False(t, stored.RegisteredAt.IsZero())
}

// TestRegisterPidRejectsZero verifies the (b) case: PID=0 is rejected.
// Plan v3 T4(b): "register PID=0 → 400".
func TestRegisterPidRejectsZero(t *testing.T) {
	srv, _ := setupClaudeCodeServer(t)
	h := srv.Handler()

	body := map[string]any{"session_id": "sess-pid0", "pid": uint32(0)}
	rr := doClaudeCodeRequest(t, h, "POST", "/api/v1/claude-code/register-pid", "", body, ccTestBearer)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "pid must be at least 5")
}

// TestRegisterPidRejectsReserved verifies the (c) case: PID=2 (kernel
// reserved on Windows) is rejected. Plan v3 T4(c).
func TestRegisterPidRejectsReserved(t *testing.T) {
	srv, _ := setupClaudeCodeServer(t)
	h := srv.Handler()

	body := map[string]any{"session_id": "sess-pid2", "pid": uint32(2)}
	rr := doClaudeCodeRequest(t, h, "POST", "/api/v1/claude-code/register-pid", "", body, ccTestBearer)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "pid must be at least 5")
}

// TestUnfreezeHappyPath verifies the (d) case: registered session, mocked
// Stop-Process returns nil → 200 with killed PID. Plan v3 T4(d).
func TestUnfreezeHappyPath(t *testing.T) {
	srv, ps := setupClaudeCodeServer(t)
	h := srv.Handler()

	_, err := ps.RecordSessionPid("sess-unf-1", 54321)
	require.NoError(t, err)

	var calledPID uint32
	mockUnfreezeExec(t, func(ctx context.Context, pid uint32) error {
		calledPID = pid
		return nil
	})

	body := map[string]any{"session_id": "sess-unf-1"}
	rr := doClaudeCodeRequest(t, h, "POST", "/api/v1/claude-code/unfreeze", "", body, ccTestBearer)
	require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())

	var resp unfreezeResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, uint32(54321), resp.Killed)
	assert.Equal(t, uint32(54321), calledPID, "exec must be invoked with the registered PID")

	// Registration is dropped on success — second unfreeze returns 404.
	_, ok := ps.GetSessionPid("sess-unf-1")
	assert.False(t, ok, "successful unfreeze must drop the PID registration")
}

// TestUnfreezeNotRegistered verifies the (e) case: session has no PID → 404.
// Plan v3 T4(e).
func TestUnfreezeNotRegistered(t *testing.T) {
	srv, _ := setupClaudeCodeServer(t)
	h := srv.Handler()

	// No RecordSessionPid call — registration is absent.
	body := map[string]any{"session_id": "sess-unknown"}
	rr := doClaudeCodeRequest(t, h, "POST", "/api/v1/claude-code/unfreeze", "", body, ccTestBearer)
	assert.Equal(t, http.StatusNotFound, rr.Code)
	assert.Contains(t, rr.Body.String(), "session not registered")
}

// TestUnfreezeExecFailure verifies the (f) case: exec returns error → 500.
// Plan v3 T4(f). The registration must also be dropped so the next attempt
// returns 404 instead of retrying a dead PID.
func TestUnfreezeExecFailure(t *testing.T) {
	srv, ps := setupClaudeCodeServer(t)
	h := srv.Handler()

	_, err := ps.RecordSessionPid("sess-exec-fail", 99999)
	require.NoError(t, err)

	mockUnfreezeExec(t, func(ctx context.Context, pid uint32) error {
		return assertErr("Stop-Process: process already exited")
	})

	body := map[string]any{"session_id": "sess-exec-fail"}
	rr := doClaudeCodeRequest(t, h, "POST", "/api/v1/claude-code/unfreeze", "", body, ccTestBearer)
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	assert.Contains(t, rr.Body.String(), "unfreeze failed")

	// Stale registration is dropped on failure.
	_, ok := ps.GetSessionPid("sess-exec-fail")
	assert.False(t, ok, "failed unfreeze must also drop the stale PID registration")

	// Second attempt now returns 404 cleanly.
	rr2 := doClaudeCodeRequest(t, h, "POST", "/api/v1/claude-code/unfreeze", "", body, ccTestBearer)
	assert.Equal(t, http.StatusNotFound, rr2.Code)
}

// TestUnfreezeRateLimit verifies the (g) case: 11th call within the minute
// returns 429. Plan v3 T4(g). We compress the budget to 2 so the test runs
// in milliseconds; the limiter semantics are identical to the production
// 10/min bucket.
func TestUnfreezeRateLimit(t *testing.T) {
	srv, ps := setupClaudeCodeServer(t)
	srv.unfreezeLimiter = newRateLimiter(2, sessionKey)
	h := srv.Handler()

	mockUnfreezeExec(t, func(ctx context.Context, pid uint32) error { return nil })

	// Re-register before each successful kill since the handler drops the
	// PID after success. The two-budget bucket allows the first two calls
	// through; the third trips the limiter.
	for i := range 2 {
		_, err := ps.RecordSessionPid("sess-rl", uint32(1000+i))
		require.NoError(t, err)
		rr := doClaudeCodeRequest(t, h, "POST", "/api/v1/claude-code/unfreeze", "",
			map[string]any{"session_id": "sess-rl"}, ccTestBearer)
		require.Equal(t, http.StatusOK, rr.Code, "call %d body=%s", i, rr.Body.String())
	}
	// Third call within the window — limiter denies before exec runs.
	_, err := ps.RecordSessionPid("sess-rl", 9999)
	require.NoError(t, err)
	rr := doClaudeCodeRequest(t, h, "POST", "/api/v1/claude-code/unfreeze", "",
		map[string]any{"session_id": "sess-rl"}, ccTestBearer)
	assert.Equal(t, http.StatusTooManyRequests, rr.Code)
	assert.Equal(t, "60", rr.Header().Get("Retry-After"))
}

// TestUnfreezeRequiresSessionID verifies the (h) case: empty session_id is
// rejected with 400. Plan v3 T4(h). Same validation applies to the
// register-pid handler — both branches are covered.
func TestUnfreezeRequiresSessionID(t *testing.T) {
	srv, _ := setupClaudeCodeServer(t)
	h := srv.Handler()

	// Unfreeze with empty session_id.
	rr := doClaudeCodeRequest(t, h, "POST", "/api/v1/claude-code/unfreeze", "",
		map[string]any{"session_id": ""}, ccTestBearer)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "session_id is required")

	// Register-pid with empty session_id.
	rr2 := doClaudeCodeRequest(t, h, "POST", "/api/v1/claude-code/register-pid", "",
		map[string]any{"session_id": "", "pid": uint32(12345)}, ccTestBearer)
	assert.Equal(t, http.StatusBadRequest, rr2.Code)
	assert.Contains(t, rr2.Body.String(), "session_id is required")
}

// assertErr is a tiny test-local error helper. Avoids importing errors for
// a single-line constructor — staying consistent with the rest of this
// test file's minimal-imports style.
type assertErr string

func (e assertErr) Error() string { return string(e) }

// TestRegisterPidRejectsNonClaude verifies that the image-name guard (E-1
// thinkdeep finding) returns 400 when the claimed PID does not resolve to
// claude.exe. The verifier is mocked to simulate a non-claude image.
func TestRegisterPidRejectsNonClaude(t *testing.T) {
	srv, _ := setupClaudeCodeServer(t)
	h := srv.Handler()

	mockVerifyPid(t, func(_ uint32) bool { return false })

	body := map[string]any{"session_id": "sess-nonclaude", "pid": uint32(9999)}
	rr := doClaudeCodeRequest(t, h, "POST", "/api/v1/claude-code/register-pid", "", body, ccTestBearer)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "does not resolve to claude.exe")
}

// TestUnfreezeDoesNotDropFreshRegistrationOnConcurrentRegister verifies the
// compare-and-swap fix for thinkdeep finding A-1: when a concurrent
// register-pid POST overwrites the entry with a new PID during the 5-s exec
// window, the CAS delete (RemoveSessionPidIfPid) leaves the new PID intact.
func TestUnfreezeDoesNotDropFreshRegistrationOnConcurrentRegister(t *testing.T) {
	srv, ps := setupClaudeCodeServer(t)
	h := srv.Handler()

	// Register an initial PID.
	_, err := ps.RecordSessionPid("sess-cas", 11111)
	require.NoError(t, err)

	// Mock exec that simulates a concurrent register-pid arriving midway:
	// while "Stop-Process" is "in flight", the daemon receives a new
	// register-pid for the same session with PID 22222.
	mockUnfreezeExec(t, func(ctx context.Context, pid uint32) error {
		// Simulate the concurrent overwrite.
		_, _ = ps.RecordSessionPid("sess-cas", 22222)
		return nil // exec succeeds
	})

	body := map[string]any{"session_id": "sess-cas"}
	rr := doClaudeCodeRequest(t, h, "POST", "/api/v1/claude-code/unfreeze", "", body, ccTestBearer)
	require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())

	var resp unfreezeResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, uint32(11111), resp.Killed, "handler must report the original PID as killed")

	// KEY: the fresh registration for PID 22222 must NOT have been wiped.
	stored, ok := ps.GetSessionPid("sess-cas")
	require.True(t, ok, "CAS delete must leave the freshly-registered PID 22222 intact")
	assert.Equal(t, uint32(22222), stored.PID)
}

// TestUnfreezeExecTimeoutDropsRegistration verifies that a mock exec that
// blocks until context deadline fires causes the handler to:
//   (a) return 500 (exec failure propagates correctly through DeadlineExceeded)
//   (b) drop the stale PID registration so the next click returns 404
//
// This exercises the context.WithTimeout + cancel defer wiring in
// handleClaudeCodeUnfreeze. Without this test, a regression removing the
// context.WithTimeout would be invisible — the handler would hang on Stop-
// Process forever and the 5-second safety net would silently vanish.
func TestUnfreezeExecTimeoutDropsRegistration(t *testing.T) {
	srv, ps := setupClaudeCodeServer(t)
	h := srv.Handler()

	_, err := ps.RecordSessionPid("sess-timeout", 77777)
	require.NoError(t, err)

	// Mock blocks until ctx is cancelled, then returns its Err — simulates
	// powershell.exe hanging (e.g. OS paging hard, cold engine spin-up).
	mockUnfreezeExec(t, func(ctx context.Context, pid uint32) error {
		<-ctx.Done()
		return ctx.Err()
	})

	body := map[string]any{"session_id": "sess-timeout"}
	rr := doClaudeCodeRequest(t, h, "POST", "/api/v1/claude-code/unfreeze", "", body, ccTestBearer)
	assert.Equal(t, http.StatusInternalServerError, rr.Code,
		"deadline-exceeded exec must return 500; body=%s", rr.Body.String())
	assert.Contains(t, rr.Body.String(), "unfreeze failed",
		"500 body must include 'unfreeze failed' prefix")

	// Stale registration is dropped on deadline, same as on ordinary failure.
	_, ok := ps.GetSessionPid("sess-timeout")
	assert.False(t, ok, "timed-out exec must also drop the stale PID registration")

	// Subsequent unfreeze attempt returns 404 cleanly.
	rr2 := doClaudeCodeRequest(t, h, "POST", "/api/v1/claude-code/unfreeze", "", body, ccTestBearer)
	assert.Equal(t, http.StatusNotFound, rr2.Code)
}
