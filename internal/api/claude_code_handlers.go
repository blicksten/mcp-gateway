package api

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"mcp-gateway/internal/patchstate"

	"github.com/go-chi/chi/v5"
)

// embeddedCompatMatrix is the compat-matrix JSON baked into the daemon
// binary at compile time. Eliminates the CWD-relative path resolution
// that broke installer/`go install` deployments (audit Scope B SB-1
// HIGH, 2026-05-06). The matrix locks to the binary version, which is
// the right invariant: a new Claude Code spike pass requires both a
// matrix update AND a daemon rebuild anyway (per ADR-0005:139).
//
//go:embed compat_matrix.json
var embeddedCompatMatrix []byte

// vscodeWebviewOriginPrefix is the schema-prefix we accept for CORS
// preflight and Access-Control-Allow-Origin responses on the Claude Code
// route group. Webview origins are of the form `vscode-webview://<guid>`;
// browsers send the exact full origin and expect an exact echo back. We
// mirror the request's Origin when it has this prefix, else 204/deny.
//
// Rationale (REVIEW-16 L-02): the `*` wildcard is rejected by browsers when
// the request carries `Authorization` header (credentials mode), so we
// must echo the exact origin.
const vscodeWebviewOriginPrefix = "vscode-webview://"

// Claude Code CORS headers — narrow, route-scoped policy. Applied only to
// /api/v1/claude-code/*; the rest of /api/v1 retains its existing csrf
// origin policy.
const (
	accessControlAllowMethods = "GET, POST, OPTIONS"
	accessControlAllowHeaders = "Authorization, Content-Type"
	accessControlMaxAge       = "300"
)

// patchHeartbeatRateLimit and pendingActionsRateLimit are per-bucket token-
// per-minute budgets. Tokens replenish linearly; see rateLimiter below.
const (
	// Heartbeats: patch sends every 60 s, plus event-driven bursts after
	// reconnect attempts. 5/min per session_id is generous headroom.
	patchHeartbeatRateLimit = 5
	// /pending-actions: patch polls every 2 s = 30/min steady-state. 60/min
	// per client IP is generous but bounds abuse.
	pendingActionsRateLimit = 60
	// /register-pid: statusline hook posts once per session (idempotency
	// marker file on the client). 5/min per session is far above the natural
	// rate and bounds a misbehaving hook.
	registerPidRateLimit = 5
	// /unfreeze: operator-driven button click. 10/min per session is
	// generous for an operator hammering the button when claude.exe is
	// stuck; production rate is closer to 0–2 per hour.
	unfreezeRateLimit = 10
)

// unfreezeExecTimeout caps how long the Stop-Process call may run. The real
// Stop-Process is a near-instant kernel call; the timeout is a safety net
// for an OS that is paging hard or a cold PowerShell engine.
const unfreezeExecTimeout = 5 * time.Second

// verifyClaudeExePidFunc is the function register-pid calls to confirm that
// the claimed PID actually resolves to claude.exe. Tests override this to
// avoid real Windows OpenProcess calls against arbitrary PID values.
// Production default is verifyClaudeExePid (build-tag-selected).
var verifyClaudeExePidFunc = verifyClaudeExePid

// unfreezeExecFunc is the function the unfreeze handler invokes to kill a
// PID. Tests override this variable to avoid spawning real processes; the
// production default shells out to PowerShell Stop-Process.
//
// Why PowerShell over taskkill.exe: Stop-Process produces the same WerFault
// error-channel path that a natural claude.exe crash produces, which gives
// the operator the empirically verified red-overlay UX (PLAN-unfreeze-button
// v3 §1: dismissable `[x]`, "View output logs", tab stays alive). taskkill
// /F maps to TerminateProcess and produces a different (less graceful) exit
// signature.
var unfreezeExecFunc = func(ctx context.Context, pid uint32) error {
	cmd := exec.CommandContext(ctx, "powershell.exe",
		"-NoProfile", "-NonInteractive",
		"-Command", fmt.Sprintf("Stop-Process -Id %d -Force", pid))
	return cmd.Run()
}

// PatchState integration — Server-side accessors. These mirror the
// SetPluginRegen pattern so tests can wire/unwire the subsystem without
// touching the rest of the server.

// SetPatchState wires the patch-state store into the server. When nil, the
// Claude Code route group returns 503 (Service Unavailable) for all
// endpoints — useful if the daemon boots with the feature disabled via
// config flag (future).
func (s *Server) SetPatchState(ps *patchstate.State) {
	s.patchState = ps
}

// claudeCodeCORS applies narrow CORS headers to Claude Code routes. Called
// from the handler wrapper; OPTIONS preflight returns 204 and short-
// circuits before auth.
//
// Browsers send the literal header only when the request is cross-origin;
// we echo the exact Origin when it carries our prefix, otherwise deny by
// omitting the header.
func claudeCodeCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		// A preflight request (OPTIONS) MUST be answered BEFORE the bearer
		// auth middleware because browsers do not attach the Authorization
		// header to preflight (see REVIEW-16 L-02).
		if r.Method == http.MethodOptions {
			if strings.HasPrefix(origin, vscodeWebviewOriginPrefix) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Methods", accessControlAllowMethods)
				w.Header().Set("Access-Control-Allow-Headers", accessControlAllowHeaders)
				w.Header().Set("Access-Control-Max-Age", accessControlMaxAge)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			// No / unknown origin: plain 204 without Allow-* headers. The
			// browser will treat this as a preflight failure and abort.
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// Non-preflight: echo origin on actual response so the browser
		// accepts the body. Unknown origins get no header — their fetch
		// will be blocked client-side.
		if strings.HasPrefix(origin, vscodeWebviewOriginPrefix) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		next.ServeHTTP(w, r)
	})
}

// rateLimiter is a minimal per-key token bucket. Capacity = `perMinute`;
// tokens refill at `perMinute / 60` per second. The bucket is ever-full at
// start so a fresh client is not immediately denied.
//
// Buckets are kept in memory keyed by `key(r)` (e.g. session_id or IP).
// evictInterval prunes idle buckets to prevent unbounded memory growth.
type rateLimiter struct {
	mu        sync.Mutex
	buckets   map[string]*bucket
	perMinute int
	keyFn     func(r *http.Request) string
}

type bucket struct {
	tokens   float64
	lastFill time.Time
}

// Threshold for bucket eviction — buckets idle longer than this are dropped
// during the next acquire call (amortized cleanup, no goroutine needed).
const bucketIdleEvictionThreshold = 10 * time.Minute

// newRateLimiter constructs a limiter with the given budget and key
// function.
func newRateLimiter(perMinute int, keyFn func(r *http.Request) string) *rateLimiter {
	return &rateLimiter{
		buckets:   make(map[string]*bucket),
		perMinute: perMinute,
		keyFn:     keyFn,
	}
}

// Allow returns true if the request should proceed, consuming 1 token.
// Amortized O(1); cleanup fires on every call when the bucket count grows
// beyond a threshold.
func (rl *rateLimiter) Allow(r *http.Request) bool {
	key := rl.keyFn(r)
	if key == "" {
		return true // cannot limit without a key — fail-open (rare path)
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	b, ok := rl.buckets[key]
	if !ok {
		b = &bucket{tokens: float64(rl.perMinute), lastFill: now}
		rl.buckets[key] = b
		// Amortized cleanup: evict idle buckets once the map grows past a
		// soft threshold. Avoids pathological memory growth without a
		// dedicated goroutine.
		if len(rl.buckets) > 1024 {
			for k, existing := range rl.buckets {
				if now.Sub(existing.lastFill) > bucketIdleEvictionThreshold {
					delete(rl.buckets, k)
				}
			}
		}
	} else {
		elapsed := now.Sub(b.lastFill).Seconds()
		b.tokens += elapsed * float64(rl.perMinute) / 60
		if b.tokens > float64(rl.perMinute) {
			b.tokens = float64(rl.perMinute)
		}
		b.lastFill = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// ipKey returns the client IP, stripping the port from RemoteAddr. Falls
// back to the full RemoteAddr if no port is present.
func ipKey(r *http.Request) string {
	addr := r.RemoteAddr
	if idx := strings.LastIndex(addr, ":"); idx > 0 {
		return addr[:idx]
	}
	return addr
}

// sessionKey pulls session_id from the decoded JSON request body. Callers
// must populate r.Context().Value(sessionIDContextKey) before calling (the
// heartbeat handler does this after JSON decode).
func sessionKey(r *http.Request) string {
	v := r.Context().Value(sessionIDContextKey)
	if v == nil {
		return ipKey(r)
	}
	s, _ := v.(string)
	if s == "" {
		return ipKey(r)
	}
	return s
}

type contextKey string

const sessionIDContextKey contextKey = "patchstate-session-id"

// handleClaudeCodeHeartbeat accepts a heartbeat JSON payload and stores it
// in patchState. Responds with `{acked:true, next_heartbeat_in_ms:<n>,
// config_override?:{...}}` per PLAN-16 P4-07.
func (s *Server) handleClaudeCodeHeartbeat(w http.ResponseWriter, r *http.Request) {
	if s.patchState == nil {
		writeError(w, http.StatusServiceUnavailable, "patch state not initialized")
		return
	}
	var hb patchstate.Heartbeat
	if err := json.NewDecoder(r.Body).Decode(&hb); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	// Per-session rate limit check — enforced after decoding so we have
	// access to session_id. The decoder already rejected malformed JSON;
	// empty session_id is validated below.
	if hb.SessionID == "" {
		writeError(w, http.StatusBadRequest, "session_id is required")
		return
	}
	if s.heartbeatLimiter != nil {
		// Stuff session_id into request context so ipKey-like fallbacks
		// don't fire for heartbeats (per-session key is deterministic).
		if !s.heartbeatLimiter.Allow(requestWithSession(r, hb.SessionID)) {
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, "rate limit exceeded for session")
			return
		}
	}

	if _, err := s.patchState.RecordHeartbeat(hb); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	resp := heartbeatResponse{
		Acked:             true,
		NextHeartbeatInMs: 60_000, // 60 s — can be overridden via config in a future phase
	}
	writeJSON(w, http.StatusOK, resp)
}

// requestWithSession returns an r-equivalent carrying sessionID in context;
// used for per-session rate limiting.
func requestWithSession(r *http.Request, sessionID string) *http.Request {
	if sessionID == "" {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), sessionIDContextKey, sessionID))
}

// heartbeatResponse is the JSON body returned to the patch. config_override
// is reserved for runtime tuning; handler returns nil by default (no live
// override plumbing wired yet). PLAN-16 P4-07 §(b) allows maintainers to
// enable this without re-patching.
type heartbeatResponse struct {
	Acked             bool            `json:"acked"`
	NextHeartbeatInMs int             `json:"next_heartbeat_in_ms"`
	ConfigOverride    *configOverride `json:"config_override,omitempty"`
}

type configOverride struct {
	LatencyWarnMs                 *int `json:"LATENCY_WARN_MS,omitempty"`
	DebounceWindowMs              *int `json:"DEBOUNCE_WINDOW_MS,omitempty"`
	ConsecutiveErrorsFailThreshold *int `json:"CONSECUTIVE_ERRORS_FAIL_THRESHOLD,omitempty"`
}

// handleClaudeCodePatchStatus returns all active heartbeats for the
// dashboard to render. Bearer-auth protected; GET only. Uses its own
// limiter (patchStatusLimiter) so dashboard polling doesn't starve the
// patch's /pending-actions budget (PAL-CR2 fix).
func (s *Server) handleClaudeCodePatchStatus(w http.ResponseWriter, r *http.Request) {
	if s.patchState == nil {
		writeError(w, http.StatusServiceUnavailable, "patch state not initialized")
		return
	}
	if s.patchStatusLimiter != nil && !s.patchStatusLimiter.Allow(r) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}
	hbs := s.patchState.Heartbeats()
	writeJSON(w, http.StatusOK, hbs)
}

// handleClaudeCodePendingActions returns undelivered actions for the patch
// to execute. Supports ?after=<cursor> for at-most-once polling.
func (s *Server) handleClaudeCodePendingActions(w http.ResponseWriter, r *http.Request) {
	if s.patchState == nil {
		writeError(w, http.StatusServiceUnavailable, "patch state not initialized")
		return
	}
	if s.pendingActionsLimiter != nil && !s.pendingActionsLimiter.Allow(r) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}
	after := r.URL.Query().Get("after")
	list := s.patchState.PendingActions(after)
	writeJSON(w, http.StatusOK, list)
}

// handleClaudeCodePendingActionAck marks an action as delivered.
func (s *Server) handleClaudeCodePendingActionAck(w http.ResponseWriter, r *http.Request) {
	if s.patchState == nil {
		writeError(w, http.StatusServiceUnavailable, "patch state not initialized")
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "action id is required")
		return
	}
	if ok := s.patchState.AckAction(id); !ok {
		writeError(w, http.StatusNotFound, "action not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "acked"})
}

// handleClaudeCodeProbeTrigger enqueues a probe-reconnect action so the
// patch can exercise its reconnect round-trip. Dashboard [Probe reconnect]
// caller (T16.5.6). Request body:
//
//	{ "nonce": "<dashboard-generated hex ≥ 16 chars>" }
//
// The gateway echoes the dashboard-provided nonce rather than generating
// its own so the dashboard can correlate the subsequent /probe-result
// without an extra round-trip.
func (s *Server) handleClaudeCodeProbeTrigger(w http.ResponseWriter, r *http.Request) {
	if s.patchState == nil {
		writeError(w, http.StatusServiceUnavailable, "patch state not initialized")
		return
	}
	var req struct {
		Nonce string `json:"nonce"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	// FROZEN contract: nonce must be ≥ 16 chars.
	if len(req.Nonce) < 16 {
		writeError(w, http.StatusBadRequest, "nonce must be at least 16 chars")
		return
	}
	act, err := s.patchState.EnqueueProbeActionWithNonce(req.Nonce)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status":    "enqueued",
		"action_id": act.ID,
	})
}

// handleClaudeCodePluginSync triggers a plugin .mcp.json regen on demand.
// Wraps Server.TriggerPluginRegen with a REST surface so the dashboard's
// [Activate for Claude Code] button (T16.5.2), `mcp-ctl install-claude-code`
// (T16.8.2), and the dogfood smoke CI (T16.9.4.a) all share one code path.
//
// Returns 409 when the plugin directory was not discovered at daemon start
// (the regen would silently no-op, which hides a configuration mistake
// from the operator).
func (s *Server) handleClaudeCodePluginSync(w http.ResponseWriter, _ *http.Request) {
	if s.pluginRegen == nil || s.pluginDir == "" {
		writeError(w, http.StatusConflict, "plugin directory not configured — set GATEWAY_PLUGIN_DIR or install via `claude plugin install`")
		return
	}
	if s.patchState == nil {
		writeError(w, http.StatusServiceUnavailable, "patch state not initialized")
		return
	}

	// Snapshot the pre-call action count so we can tell whether the
	// post-regen EnqueueReconnectAction landed or was coalesced by the
	// 500 ms server-side debounce window. The FROZEN contract distinguishes
	// `action_enqueued: true/false` for callers that care (dogfood CI).
	preActions := s.patchState.PendingActions("")

	s.TriggerPluginRegen()

	postActions := s.patchState.PendingActions("")
	enqueued := len(postActions) > len(preActions)
	var actionID string
	if enqueued {
		actionID = postActions[len(postActions)-1].ID
	}

	resp := map[string]any{
		"status":          "synced",
		"mcp_json_path":   s.pluginMCPJSONPath(),
		"entries_count":   s.liveBackendCount(),
		"action_enqueued": enqueued,
	}
	if enqueued {
		resp["action_id"] = actionID
	}
	writeJSON(w, http.StatusOK, resp)
}

// pluginMCPJSONPath returns the expected .mcp.json path for informational
// reporting. Does not verify the file exists — handleClaudeCodePluginSync
// calls this after regen, which would have failed loudly if write failed.
func (s *Server) pluginMCPJSONPath() string {
	if s.pluginDir == "" {
		return ""
	}
	return s.pluginDir + string(os.PathSeparator) + ".mcp.json"
}

// liveBackendCount returns the number of non-disabled, non-nil entries in
// the current config. Matches the count the plugin regen writes into
// .mcp.json.
func (s *Server) liveBackendCount() int {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	n := 0
	for _, sc := range s.cfg.Servers {
		if sc != nil && !sc.Disabled {
			n++
		}
	}
	return n
}

// handleClaudeCodeCompatMatrix returns the compat matrix JSON embedded
// into the daemon binary at build time (T16.4.7, T16.6.5). The compat
// matrix is the single source of truth consumed by the dashboard for
// "is this Claude Code version Alt-E verified".
//
// As of audit Scope B SB-1 (2026-05-06) the matrix is //go:embed-ed
// from configs/supported_claude_code_versions.json. The previous
// implementation read the file CWD-relative, which silently 503-ed for
// any daemon not launched from the repo root (every operator-installed
// daemon, every `go install` deployment, the install.ps1 Scheduled Task
// because its WorkingDirectory is ~/.mcp-gateway not the binary dir).
// Embedding eliminates the path-resolution defect entirely.
func (s *Server) handleClaudeCodeCompatMatrix(w http.ResponseWriter, _ *http.Request) {
	if len(embeddedCompatMatrix) == 0 {
		// Should never happen — //go:embed enforces presence at build time.
		// Surfaced as 503 (not 500) so downstream UI can distinguish
		// "matrix unavailable" from a transient daemon failure.
		writeError(w, http.StatusServiceUnavailable, "compat matrix is empty (build asset missing)")
		return
	}
	// Sanity-check JSON validity once at first request — guards against a
	// corrupt build asset (e.g. zero-byte truncation under race) so the
	// dashboard's JSON parser can't be fed garbage with a confusing error.
	var probe map[string]any
	if err := json.Unmarshal(embeddedCompatMatrix, &probe); err != nil {
		writeError(w, http.StatusInternalServerError, "embedded compat matrix is not valid JSON: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(embeddedCompatMatrix)
}

// handleClaudeCodeProbeResult records a patch-reported probe outcome.
func (s *Server) handleClaudeCodeProbeResult(w http.ResponseWriter, r *http.Request) {
	if s.patchState == nil {
		writeError(w, http.StatusServiceUnavailable, "patch state not initialized")
		return
	}
	var pr patchstate.ProbeResult
	if err := json.NewDecoder(r.Body).Decode(&pr); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if err := s.patchState.RecordProbeResult(pr); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "recorded"})
}

// registerPidRequest is the JSON body for POST /api/v1/claude-code/register-pid.
// Sent by hooks/statusline.mjs once per session (idempotency marker file
// `~/.claude/sessions/<sid>.pid-registered` prevents repeat posts on the
// client side).
//
// WindowID (FM-9 multi-instance scope, P1.2 2026-05-22) is the VS Code
// window identifier supplied by the statusline hook (typically
// `process.env.VSCODE_PID` or extension-injected `VSCODE_WINDOW_ID`). When
// present, the gateway enforces "1 claude.exe per VS Code window"
// (governed by MCP_GATEWAY_ENFORCE_SINGLE_CLAUDE — see handler). Empty
// when claude.exe is launched outside VS Code; gateway gracefully no-ops.
type registerPidRequest struct {
	SessionID string `json:"session_id"`
	PID       uint32 `json:"pid"`
	WindowID  string `json:"window_id,omitempty"`
}

type registerPidResponse struct {
	Stored bool `json:"stored"`
}

// handleClaudeCodeRegisterPid stores the (session_id → pid) mapping in
// patchState so the /unfreeze handler can target the right process when
// the operator clicks the porfiry-taskbar 🔄 button.
//
// Validation:
//   - empty session_id → 400
//   - pid < 5 → 400 (Windows kernel reserves PIDs 0-4: System Idle, System,
//     secure System). Allowing these would silently miss-target Stop-Process.
//   - per-session rate limit (5/min) → 429
//
// Stores last-write-wins. claude.exe restart in the same VSCode tab
// produces a new PID under the same session_id; the new value overwrites.
func (s *Server) handleClaudeCodeRegisterPid(w http.ResponseWriter, r *http.Request) {
	if s.patchState == nil {
		writeError(w, http.StatusServiceUnavailable, "patch state not initialized")
		return
	}
	var req registerPidRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.SessionID == "" {
		writeError(w, http.StatusBadRequest, "session_id is required")
		return
	}
	if req.PID < 5 {
		writeError(w, http.StatusBadRequest, "pid must be at least 5 (Windows kernel reserves 0-4)")
		return
	}
	// Image-name guard (thinkdeep finding E-1): reject PIDs that do not
	// resolve to claude.exe. Prevents an on-host attacker with the Bearer
	// token from registering an arbitrary process PID so the next operator
	// unfreeze click kills a non-claude process. Non-Windows builds always
	// return true (feature is Windows-only v1; plan v3 §Out of scope).
	if !verifyClaudeExePidFunc(req.PID) {
		writeError(w, http.StatusBadRequest, "pid does not resolve to claude.exe")
		return
	}
	if s.registerPidLimiter != nil {
		if !s.registerPidLimiter.Allow(requestWithSession(r, req.SessionID)) {
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, "rate limit exceeded for session")
			return
		}
	}

	// FM-9 multi-instance hard-limit (P1.2 2026-05-22). When a window_id is
	// supplied AND there is an existing live entry for the same window_id
	// from a DIFFERENT session, gateway enforces "1 claude.exe per VS Code
	// window" per ADR-002.
	//
	// Atomic check-then-store via EnforceWindowAndRecordPid resolves the
	// TOCTOU race between the prior split FindSessionPidByWindow +
	// RecordSessionPidWithWindow path (Sonnet code-reviewer 2026-05-22:
	// HIGH-1). Both find + write run under a single State write lock.
	//
	// Liveness check reuses verifyClaudeExePidFunc — if the stored PID no
	// longer resolves to claude.exe it is stale (claude.exe exited; the
	// rare PID-recycle false-positive is bounded by Windows' sequential-
	// ascending PID assignment and the absence of a TTL on sessionPids
	// only matters for entries past 24h, which any normal session will
	// have re-registered). Stale entries are evicted in the same critical
	// section that stores the new one.
	//
	// Bypass mechanisms:
	//   - env MCP_GATEWAY_ALLOW_MULTI_INSTANCE=1 — full bypass (CI / pipeline
	//     sub-claude / operator override). Equivalent to feature OFF.
	//   - env MCP_GATEWAY_ENFORCE_SINGLE_CLAUDE not "1" — log-only mode
	//     (default). We emit a WARN and proceed so the rollout can observe
	//     real conflict frequency before the hard cutover.
	//   - req.WindowID == "" — graceful no-op (terminal launch, non-VS-Code
	//     IDE, statusline hook predating window_id support).
	bypass := req.WindowID == "" || os.Getenv("MCP_GATEWAY_ALLOW_MULTI_INSTANCE") == "1"
	if bypass {
		// Bypass path: behave like classic RecordSessionPidWithWindow.
		if _, err := s.patchState.RecordSessionPidWithWindow(req.SessionID, req.PID, req.WindowID); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, registerPidResponse{Stored: true})
		return
	}

	enforce := os.Getenv("MCP_GATEWAY_ENFORCE_SINGLE_CLAUDE") == "1"

	res, err := s.patchState.EnforceWindowAndRecordPid(
		req.SessionID, req.PID, req.WindowID, verifyClaudeExePidFunc,
	)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if res.Conflict != nil {
		if enforce {
			s.logger.Warn("multi-claude rejected at register-pid",
				"window_id", req.WindowID,
				"existing_pid", res.Conflict.PID,
				"existing_session", res.ConflictSID,
				"new_pid", req.PID,
				"new_session", req.SessionID,
			)
			writeError(w, http.StatusConflict, fmt.Sprintf(
				"another claude.exe (pid %d, session %s) is already running in this VS Code window. Open a new window for parallel claude.exe sessions, or set MCP_GATEWAY_ALLOW_MULTI_INSTANCE=1 to disable enforcement.",
				res.Conflict.PID, res.ConflictSID))
			return
		}
		// Log-only mode: the conflict was detected but the new entry was
		// NOT stored by EnforceWindowAndRecordPid (it returned the conflict
		// without storing). We must store it ourselves to preserve the
		// pre-FM-9 behaviour (last-write-wins under default flags).
		s.logger.Warn("multi-claude detected (log-only; set MCP_GATEWAY_ENFORCE_SINGLE_CLAUDE=1 to enforce)",
			"window_id", req.WindowID,
			"existing_pid", res.Conflict.PID,
			"existing_session", res.ConflictSID,
			"new_pid", req.PID,
			"new_session", req.SessionID,
		)
		if _, err := s.patchState.RecordSessionPidWithWindow(req.SessionID, req.PID, req.WindowID); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	} else if res.EvictedStale {
		s.logger.Info("multi-claude evicted stale entry",
			"window_id", req.WindowID,
			"evicted_pid", res.EvictedPID,
			"new_pid", req.PID,
			"new_session", req.SessionID,
		)
	}

	writeJSON(w, http.StatusOK, registerPidResponse{Stored: true})
}

// unfreezeRequest is the JSON body for POST /api/v1/claude-code/unfreeze.
// Sent by patches/porfiry-taskbar.js when the operator clicks the 🔄
// button. No PID in the body — server looks it up from patchState by
// session_id to prevent the webview from killing arbitrary processes.
type unfreezeRequest struct {
	SessionID string `json:"session_id"`
}

type unfreezeResponse struct {
	Killed uint32 `json:"killed"`
}

// handleClaudeCodeUnfreeze runs `Stop-Process -Id <pid> -Force` against the
// claude.exe PID registered for session_id. On success the registration is
// dropped (claude.exe is gone). On failure (usually the process already
// exited naturally) the registration is also dropped so the next click
// returns a clean 404 instead of retrying a dead PID.
//
// Validation:
//   - empty session_id → 400
//   - session_id has no PID registered → 404 "session not registered"
//   - per-session rate limit (10/min) → 429
//   - Stop-Process exec failed → 500 with the underlying error
func (s *Server) handleClaudeCodeUnfreeze(w http.ResponseWriter, r *http.Request) {
	if s.patchState == nil {
		writeError(w, http.StatusServiceUnavailable, "patch state not initialized")
		return
	}
	var req unfreezeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.SessionID == "" {
		writeError(w, http.StatusBadRequest, "session_id is required")
		return
	}
	if s.unfreezeLimiter != nil {
		if !s.unfreezeLimiter.Allow(requestWithSession(r, req.SessionID)) {
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, "rate limit exceeded for session")
			return
		}
	}
	entry, ok := s.patchState.GetSessionPid(req.SessionID)
	if !ok {
		writeError(w, http.StatusNotFound, "session not registered")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), unfreezeExecTimeout)
	defer cancel()
	if err := unfreezeExecFunc(ctx, entry.PID); err != nil {
		// CAS delete: only wipe the registration if the PID we just tried
		// to kill is still the one on record. A concurrent register-pid POST
		// during the 5-s exec window may have written a fresh PID (from a
		// claude.exe restart in the same tab); RemoveSessionPidIfPid leaves
		// that new entry intact. (PLAN-unfreeze-button thinkdeep A-1.)
		s.patchState.RemoveSessionPidIfPid(req.SessionID, entry.PID)
		writeError(w, http.StatusInternalServerError,
			fmt.Sprintf("unfreeze failed: %s", err.Error()))
		return
	}
	s.patchState.RemoveSessionPidIfPid(req.SessionID, entry.PID)
	writeJSON(w, http.StatusOK, unfreezeResponse{Killed: entry.PID})
}
