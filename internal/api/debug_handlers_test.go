package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"mcp-gateway/internal/auth"
	"mcp-gateway/internal/health"
	"mcp-gateway/internal/lifecycle"
	"mcp-gateway/internal/models"
	"mcp-gateway/internal/obs"
	"mcp-gateway/internal/proxy"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupDebugServer builds a Server with a monitor and a TRACE-enabled emitter
// (so RunID is non-empty and the KillRing is live), plus one disabled backend
// carrying an env KEY so the dump's env_keys path is exercised. Returns the
// server, its handler, and the live emitter for kill-event injection.
func setupDebugServer(t *testing.T) (*Server, http.Handler, *obs.Emitter) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"backend1": {
				URL: "http://localhost:3000/mcp",
				// Disabled so no real child process is started in the test.
				Disabled: true,
				// SECURITY: a secret-bearing env VALUE. The dump must surface
				// only the KEY ("SAP_PASSWORD"), never "hunter2-supersecret".
				Env: []string{"SAP_PASSWORD=hunter2-supersecret"},
			},
		},
	}
	cfg.ApplyDefaults()

	lm := lifecycle.NewManager(cfg, "test", logger)
	gw := proxy.New(cfg, lm, "test", logger)
	mon := health.NewMonitor(lm, time.Second, logger)
	srv := NewServer(lm, gw, mon, cfg, "", logger, AuthConfig{}, "test")

	// Enable tracing so RunID is minted and the KillRing is wired through the
	// emitter the dump reads. No daemon is started — source + emitter only.
	t.Setenv("MCP_GATEWAY_TRACE", "1")
	emitter := obs.NewEmitter(t.TempDir(), logger)
	require.True(t, emitter.Enabled())
	srv.SetEmitter(emitter)
	t.Cleanup(func() { _ = emitter.Close() })

	return srv, srv.Handler(), emitter
}

// TestDebugDump_ShapeAndKillHistory asserts 200, the documented JSON shape, and
// that kill_history reflects the KillRing contents pushed before the request.
func TestDebugDump_ShapeAndKillHistory(t *testing.T) {
	srv, handler, emitter := setupDebugServer(t)
	_ = srv

	// Push two kill events; the dump must reflect both, oldest-first.
	emitter.Kills().Push(obs.KillEvent{
		Backend: "backend1", Pid: 1111, Actor: "suture",
		Reason: "keepalive-miss", Method: "terminate-group",
	})
	emitter.Kills().Push(obs.KillEvent{
		Backend: "backend1", Pid: 2222, Actor: "connect-fail",
		Reason: "connect-failed", Method: "kill-group",
	})

	rr := doRequest(t, handler, "GET", "/api/v1/debug/dump", nil)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "application/json", rr.Header().Get("Content-Type"))

	var dump DebugDump
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &dump))

	// gateway section.
	assert.NotEmpty(t, dump.Gateway.RunID, "run_id must be set when tracing on")
	assert.Greater(t, dump.Gateway.PID, 0, "pid must be positive")
	assert.GreaterOrEqual(t, dump.Gateway.PPID, 0)
	assert.NotEmpty(t, dump.Gateway.StartTS, "start_ts must be set when monitor present")
	assert.GreaterOrEqual(t, dump.Gateway.UptimeMs, int64(0))
	// job_object is always present (enabled is platform-dependent).
	assert.Equal(t, dump.Gateway.JobObject.Enabled, dump.Gateway.JobObject.KillOnClose,
		"kill_on_close mirrors enabled")

	// backends section.
	require.Len(t, dump.Backends, 1)
	b := dump.Backends[0]
	assert.Equal(t, "backend1", b.Name)
	assert.Equal(t, "stopped", b.Status, "disabled backend is stopped")
	assert.Equal(t, []string{"SAP_PASSWORD"}, b.EnvKeys, "only env KEY names, never values")
	// last_restart_reason/actor derived from newest kill for this backend.
	assert.Equal(t, "connect-failed", b.LastRestartReason)
	assert.Equal(t, "connect-fail", b.LastRestartActor)

	// kill_history reflects the ring, oldest-first.
	require.Len(t, dump.KillHistory, 2)
	assert.Equal(t, "keepalive-miss", dump.KillHistory[0].Reason)
	assert.Equal(t, 1111, dump.KillHistory[0].Pid)
	assert.Equal(t, "connect-failed", dump.KillHistory[1].Reason)
	assert.Equal(t, 2222, dump.KillHistory[1].Pid)
}

// TestDebugDump_RedactsSecrets asserts the stricter /dump redaction: a
// synthetic secret pushed into a kill-history reason (and the secret-bearing
// env VALUE configured on the backend) never appears in the response; the
// secret span is scrubbed.
func TestDebugDump_RedactsSecrets(t *testing.T) {
	_, handler, emitter := setupDebugServer(t)

	const secretJWT = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U"
	const bearer = "Bearer abc123def456ghi789jkl012mno345pqr678stu"

	// Inject a kill event whose free-text reason carries secrets — the dump
	// must scrub them even though Reason is normally an enum label.
	emitter.Kills().Push(obs.KillEvent{
		Backend: "backend1", Pid: 3333, Actor: "manual",
		Reason: bearer, Method: secretJWT,
	})

	rr := doRequest(t, handler, "GET", "/api/v1/debug/dump", nil)
	require.Equal(t, http.StatusOK, rr.Code)

	raw := rr.Body.String()

	// The raw secret values must NEVER appear anywhere in the payload.
	assert.NotContains(t, raw, "hunter2-supersecret", "env VALUE must not leak")
	assert.NotContains(t, raw, secretJWT, "JWT in kill reason must be scrubbed")
	assert.NotContains(t, raw, "abc123def456ghi789jkl012mno345pqr678stu", "Bearer token must be scrubbed")
	// Redaction marker proves the scrub fired rather than the field being dropped.
	assert.Contains(t, raw, "redacted", "redaction marker must be present")

	// Structurally confirm the scrubbed reason/method round-trip as redacted.
	var dump DebugDump
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &dump))
	var found bool
	for _, k := range dump.KillHistory {
		if k.Pid == 3333 {
			found = true
			assert.NotEqual(t, bearer, k.Reason)
			assert.NotEqual(t, secretJWT, k.Method)
			assert.True(t, strings.Contains(k.Reason, "redacted"), "reason scrubbed")
			assert.True(t, strings.Contains(k.Method, "redacted"), "method scrubbed")
		}
	}
	require.True(t, found, "synthetic kill event must be present")
}

// TestDebugDump_RequiresAuth asserts the route lives in the Bearer-authed group:
// a request without a Bearer token is rejected with 401, and a correct Bearer
// reaches the handler (200).
func TestDebugDump_RequiresAuth(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg := &models.Config{Servers: map[string]*models.ServerConfig{}}
	cfg.ApplyDefaults()

	lm := lifecycle.NewManager(cfg, "test", logger)
	gw := proxy.New(cfg, lm, "test", logger)
	mon := health.NewMonitor(lm, time.Second, logger)

	token, err := auth.GenerateToken()
	require.NoError(t, err)
	srv := NewServer(lm, gw, mon, cfg, "", logger, AuthConfig{Enabled: true, Token: token}, "test")
	handler := srv.Handler()

	// No Bearer → 401.
	reqNoAuth := httptest.NewRequest("GET", "/api/v1/debug/dump", nil)
	rrNoAuth := httptest.NewRecorder()
	handler.ServeHTTP(rrNoAuth, reqNoAuth)
	assert.Equal(t, http.StatusUnauthorized, rrNoAuth.Code, "debug/dump must require Bearer")
	assert.Equal(t, "Bearer", rrNoAuth.Header().Get("WWW-Authenticate"))

	// Correct Bearer → 200.
	reqAuth := httptest.NewRequest("GET", "/api/v1/debug/dump", nil)
	reqAuth.Header.Set("Authorization", "Bearer "+token)
	rrAuth := httptest.NewRecorder()
	handler.ServeHTTP(rrAuth, reqAuth)
	assert.Equal(t, http.StatusOK, rrAuth.Code, "correct Bearer must reach the handler")
}
