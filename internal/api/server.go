// Package api implements the REST API and MCP HTTP transport server.
package api

import (
	"context"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"mcp-gateway/internal/auth"
	"mcp-gateway/internal/config"
	"mcp-gateway/internal/health"
	"mcp-gateway/internal/lifecycle"
	"mcp-gateway/internal/models"
	"mcp-gateway/internal/patchstate"
	"mcp-gateway/internal/plugin"
	"mcp-gateway/internal/proxy"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Server holds all dependencies for the HTTP server.
type Server struct {
	lm         *lifecycle.Manager
	gw         *proxy.Gateway
	monitor    *health.Monitor
	cfg        *models.Config
	cfgMu      sync.RWMutex // protects s.cfg reads/writes
	flushMu    sync.Mutex   // serializes flushConfig disk writes (F2 fix)
	configPath string
	logger     *slog.Logger
	httpServer *http.Server

	// Auth wiring — set by NewServer, consumed by Handler. See ADR-0003.
	authToken   string // empty ⇔ authEnabled=false
	authEnabled bool   // false only when --no-auth is set on the daemon CLI
	version     string // populated for /api/v1/version (public endpoint)

	// Claude Code plugin regen (Phase 16.2). pluginRegen and pluginDir are
	// set via SetPluginRegen; when either is zero-valued, regen is a no-op
	// (the plugin was not installed / discovery failed — not fatal).
	pluginRegen *plugin.Regenerator
	pluginDir   string

	// Claude Code webview patch state (Phase 16.3). Set via SetPatchState;
	// when nil, the /api/v1/claude-code/* route group returns 503 for all
	// endpoints (feature disabled). Rate limiters guard heartbeat and
	// pending-actions endpoints against misbehaving or compromised patches.
	patchState            *patchstate.State
	heartbeatLimiter      *rateLimiter
	pendingActionsLimiter *rateLimiter
	// patchStatusLimiter is separate from pendingActionsLimiter so the
	// dashboard's /patch-status polling (every 10 s per PLAN-16 T16.5.4)
	// doesn't compete for tokens with the patch's /pending-actions 2-s
	// polling when they originate from the same host IP (typical dev
	// loop). Shared bucket → spec violation against docs/api/claude-code-
	// endpoints.md "independent 60 req/min budgets" (PAL-CR2 fix).
	patchStatusLimiter *rateLimiter

	// listenerAddr records the bound listener address once ListenAndServe
	// has successfully called net.Listen. Nil before that point. Used by
	// tests to reach a random-port (":0") listener.
	listenerMu   sync.Mutex
	listenerAddr net.Addr
}

// Addr returns the bound listener address, or nil if ListenAndServe has
// not yet reached net.Listen. Safe to call from any goroutine.
func (s *Server) Addr() net.Addr {
	s.listenerMu.Lock()
	defer s.listenerMu.Unlock()
	return s.listenerAddr
}

// AuthConfig bundles Bearer auth parameters for the HTTP server.
// token is empty when enabled=false (--no-auth path).
type AuthConfig struct {
	Enabled bool
	Token   string
}

// NewServer creates a new API server.
func NewServer(
	lm *lifecycle.Manager,
	gw *proxy.Gateway,
	monitor *health.Monitor,
	cfg *models.Config,
	configPath string,
	logger *slog.Logger,
	authCfg AuthConfig,
	version string,
) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{
		lm:          lm,
		gw:          gw,
		monitor:     monitor,
		cfg:         cfg,
		configPath:  configPath,
		logger:      logger,
		authToken:   authCfg.Token,
		authEnabled: authCfg.Enabled,
		version:     version,
	}
}

// UpdateConfig replaces the server's config pointer (CR-4/AR-2 fix).
func (s *Server) UpdateConfig(cfg *models.Config) {
	s.cfgMu.Lock()
	s.cfg = cfg
	s.cfgMu.Unlock()
}

// SetPluginRegen wires the Claude Code plugin regenerator into the API
// server (Phase 16.2). When dir or regen is empty/nil, TriggerPluginRegen
// becomes a no-op — the daemon continues to serve normally even without a
// discovered plugin directory.
//
// The main daemon calls this once after NewServer: dir comes from
// plugin.Discover, regen from plugin.NewRegenerator. Tests can leave it
// unset; server_test.go does not exercise the plugin surface.
func (s *Server) SetPluginRegen(dir string, regen *plugin.Regenerator) {
	s.pluginDir = dir
	s.pluginRegen = regen
}

// InitClaudeCodeLimiters creates the per-session heartbeat limiter and
// per-IP pending-actions limiter. Called once from NewServer so the
// patch-state wiring (SetPatchState) can be deferred without separating
// limiter lifecycle from server lifecycle. See PLAN-16 T16.3.5.
func (s *Server) InitClaudeCodeLimiters() {
	s.heartbeatLimiter = newRateLimiter(patchHeartbeatRateLimit, sessionKey)
	s.pendingActionsLimiter = newRateLimiter(pendingActionsRateLimit, ipKey)
	s.patchStatusLimiter = newRateLimiter(pendingActionsRateLimit, ipKey)
}

// TriggerPluginRegen rebuilds the plugin's `.mcp.json` from the current
// config (Phase 16.2). Best-effort: regen errors are logged but never
// propagated to the caller — the mutation already succeeded against
// the lifecycle manager and config file; plugin regen is a downstream
// notification, not a precondition.
//
// Exposed publicly (not just called from REST handlers) so the daemon
// can bootstrap the file once on startup and rebuild on config-watcher
// reloads (PAL-TD-GAP1 + GAP2, 2026-04-21). Prior to this, mutation-only
// triggers meant a fresh daemon with config-only management kept the
// stub `.mcp.json`, and live edits to config.json never propagated to
// the plugin surface.
//
// Holding no locks on entry so the caller can invoke this from within or
// outside cfgMu critical sections; we take our own RLock here to build an
// immutable snapshot (deep-copy) of each ServerConfig and release before
// calling Regenerate (which does its own I/O and internal mutex
// serialization).
//
// Deep-copy rationale (PAL-CR-H1, 2026-04-21): copying only pointers left
// a race window after RUnlock — a concurrent handlePatchServer holding
// cfgMu.Lock could do `*sc = scCopy`, torning the struct from an
// unsynchronized reader (Regenerate dereferencing the same pointer).
// Even though only `Disabled` is read in practice, Go's memory model
// treats any unsynchronized concurrent read+write as a data race. Cloning
// the value under RLock gives Regenerate a private, stable view.
func (s *Server) TriggerPluginRegen() {
	if s.pluginRegen == nil || s.pluginDir == "" {
		return
	}
	s.cfgMu.RLock()
	snapshot := make(map[string]*models.ServerConfig, len(s.cfg.Servers))
	for name, sc := range s.cfg.Servers {
		if sc == nil {
			continue
		}
		clone := *sc // value copy — severs pointer aliasing with live cfg.Servers
		snapshot[name] = &clone
	}
	s.cfgMu.RUnlock()

	// Production: pass the default placeholder so Claude Code substitutes
	// the gateway URL from userConfig at MCP-client runtime. This keeps
	// the regenerated file portable across machines (different hosts,
	// different ports).
	if err := s.pluginRegen.Regenerate(s.pluginDir, snapshot, plugin.DefaultGatewayURLPlaceholder); err != nil {
		s.logger.Warn("plugin regen failed", "error", err, "plugin_dir", s.pluginDir)
		return
	}

	// Phase 16.3 T16.3.3: after a successful regen, enqueue a reconnect
	// action so the webview patch can reload Claude Code's view of OUR
	// aggregate plugin. Coalesced at 500 ms on the server side (and again
	// at 10 s on the webview side, see PLAN-16 T16.4.3). P4-08 invariant:
	// serverName is always AggregatePluginServerName regardless of which
	// individual backend inside the gateway mutated.
	if s.patchState != nil {
		s.patchState.EnqueueReconnectAction(patchstate.AggregatePluginServerName)
	}
}

// Handler returns the chi router with all routes mounted.
//
// Middleware policy (ADR-0003 §policy-matrix):
//   Public     GET /api/v1/health, /api/v1/version — no auth, no csrf
//   Authed REST  all other /api/v1/* — auth THEN csrf (cheap 401 first)
//   SSE /logs  separate group; auth BEFORE Throttle(20) so unauthed
//              clients cannot exhaust the throttle budget (T12A.3d)
//   MCP transports /mcp, /sse — policy from GatewaySettings.AuthMCPTransport
//                 (loopback-only default; bearer-required when remote)
//   /api/* redirect — 307 to /api/v1; csrf/auth applied at destination
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	// middleware.RealIP is deliberately NOT applied at the router root.
	// It trusts X-Forwarded-For / X-Real-IP unconditionally — without a
	// trusted-proxy allowlist, a remote client can spoof
	// `X-Forwarded-For: 127.0.0.1` and bypass the loopback-only MCP
	// transport policy (PAL-2026-04-18 CRITICAL). If a future deployment
	// ever sits behind a trusted proxy, reapply RealIP INSIDE the authed
	// /api/v1 group (where RemoteAddr is not a security decision input)
	// rather than at the root.

	// Auth middleware — identity for --no-auth path so the same wiring
	// compiles in both modes without branching every route.
	authMW := func(next http.Handler) http.Handler { return next }
	if s.authEnabled {
		authMW = auth.Middleware(s.authToken, s.logger)
	}

	// REST API v1 — throttled and body-size-limited routes (T5.1).
	r.Route("/api/v1", func(r chi.Router) {
		// Rate limiting: max 100 concurrent, 200 backlog, 30s timeout (T2.1).
		r.Use(middleware.ThrottleBacklog(100, 200, 30*time.Second))
		// Body size limit: 1 MB max (T2.2). Applied to all verbs; GET bodies
		// are typically nil and pass through with zero overhead.
		r.Use(maxBodySize(1 << 20))

		// Public group — health/version endpoints. No auth, no csrf.
		// Monitoring probes and the extension's first-start handshake
		// depend on reaching these without credentials.
		r.Group(func(r chi.Router) {
			r.Get("/health", s.handleHealth)
			r.Get("/version", s.handleVersion)
		})

		// Authed group — every mutating endpoint AND sensitive reads.
		// Auth runs BEFORE csrf: cheap constant-time 401 short-circuits
		// unauthenticated traffic so csrf only examines authenticated
		// browser-bearing requests (ADR-0003 §csrf-scope).
		r.Group(func(r chi.Router) {
			r.Use(authMW)
			r.Use(csrfProtect) // F-3 fix, now scoped to /api/v1 authed routes

			r.Get("/servers", s.handleListServers)
			r.Get("/servers/{name}", s.handleGetServer)
			r.Post("/servers", s.handleAddServer)
			r.Delete("/servers/{name}", s.handleRemoveServer)
			r.Patch("/servers/{name}", s.handlePatchServer)
			r.Post("/servers/{name}/restart", s.handleRestartServer)
			r.Post("/servers/{name}/reset-circuit", s.handleResetCircuit)
			r.Post("/servers/{name}/call", s.handleCallTool)
			r.Get("/tools", s.handleListTools)
			r.Get("/metrics", s.handleMetrics)
		})

		// Claude Code integration group (Phase 16.3).
		//
		// CORS + OPTIONS preflight runs BEFORE bearer auth because browsers
		// do not attach Authorization to preflight (REVIEW-16 L-02). The
		// CORS middleware short-circuits OPTIONS and passes everything
		// else through to authMW. csrfProtect is intentionally NOT
		// applied — the webview patch is not a cookie-auth browser session
		// and has its own Bearer token; csrf is only relevant to cookie-
		// bearing requests (ADR-0003 §csrf-scope).
		r.Route("/claude-code", func(r chi.Router) {
			r.Use(claudeCodeCORS)
			r.Use(authMW)

			r.Post("/patch-heartbeat", s.handleClaudeCodeHeartbeat)
			r.Get("/patch-status", s.handleClaudeCodePatchStatus)
			r.Get("/pending-actions", s.handleClaudeCodePendingActions)
			r.Post("/pending-actions/{id}/ack", s.handleClaudeCodePendingActionAck)
			r.Post("/probe-trigger", s.handleClaudeCodeProbeTrigger)
			r.Post("/probe-result", s.handleClaudeCodeProbeResult)
			r.Post("/plugin-sync", s.handleClaudeCodePluginSync)
			r.Get("/compat-matrix", s.handleClaudeCodeCompatMatrix)
		})
	})

	// SSE log streaming — outside the REST throttle group so long-lived
	// connections don't consume rate-limit tokens and starve REST (F4 fix).
	// Separate concurrency limit: max 20 concurrent SSE connections (F-4 DoS).
	// T12A.3d: auth runs BEFORE Throttle so unauthed clients don't consume
	// a throttle slot (DoS hardening). csrf does not apply to SSE (daemon-
	// to-daemon / extension-to-daemon, no cookie session).
	r.Group(func(r chi.Router) {
		r.Use(authMW)
		r.Use(middleware.Throttle(20))
		r.Get("/api/v1/servers/{name}/logs", s.handleServerLogs)
	})

	// Backward-compat redirect: /api/* → /api/v1/* (T5.1).
	// Uses 307 Temporary Redirect to preserve HTTP method (POST stays POST).
	// Per ADR-0003 §csrf-scope: csrfProtect does NOT apply here — the
	// destination /api/v1 group enforces it on arrival. Applying it here
	// would block legitimate method-preserving clients for no security gain.
	r.HandleFunc("/api/*", func(w http.ResponseWriter, r *http.Request) {
		suffix := path.Clean("/" + chi.URLParam(r, "*"))
		target := "/api/v1" + suffix // suffix starts with / after Clean
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusTemporaryRedirect)
	})

	// MCP transports. Policy per ADR-0003 §policy-matrix-mcp-modes.
	//
	// Phase 16.1 dual-mode routing (streamable handler only, per T16.1.5):
	//   /mcp                          -> aggregate server (namespaced tools)
	//   /mcp/{backend}                -> per-backend server (unnamespaced) if
	//                                    backend name matches SERVER_NAME_RE
	//                                    and backend is currently registered
	//   /mcp/{backend-not-registered} -> nil return -> SDK responds per
	//                                    streamable.go:190 (400 Bad Request)
	//   /mcp/{invalid-name}           -> fall through to aggregate
	//
	// T16.1.5.a SDK path verification (go-sdk v1.4.1): read of
	// streamable.go::ServeHTTP (lines ~246-365) confirms the handler is a
	// single-endpoint design. It parses `mcp-session-id` from the request
	// header (not from URL path) and routes by HTTP method, never by URL
	// sub-path. Therefore the `/mcp/{backend}` scheme cannot collide with
	// any SDK-internal routing primitive. The "fall through to aggregate"
	// behavior for invalid names is defense-in-depth against a future SDK
	// that might introduce sub-paths (e.g. /mcp/session/{id}); such names
	// would fail SERVER_NAME_RE (dashes allowed, but "session" is accepted
	// — a future denylist may be required if the SDK adopts such a scheme).
	//
	// SSE surface remains aggregate-only in Phase 16.1 — per-backend SSE
	// adds no plugin-integration value (Claude Code uses HTTP streamable).
	streamableHandler := mcp.NewStreamableHTTPHandler(
		s.mcpServerForRequest, nil,
	)
	sseHandler := mcp.NewSSEHandler(
		func(r *http.Request) *mcp.Server { return s.gw.Server() }, nil,
	)

	mcpPolicy := s.mcpTransportPolicy(authMW)

	r.Handle("/mcp", mcpPolicy(streamableHandler))
	r.Handle("/mcp/*", mcpPolicy(streamableHandler))
	r.Handle("/sse", mcpPolicy(sseHandler))
	r.Handle("/sse/*", mcpPolicy(sseHandler))

	return r
}

// mcpServerForRequest is the per-request getServer callback passed to the
// go-sdk streamable handler. It inspects r.URL.Path and returns:
//   - aggregate server for exact "/mcp"
//   - per-backend server for "/mcp/{backend}" when {backend} validates as a
//     backend name AND the backend is currently registered
//   - nil for "/mcp/{valid-name}" where no such backend exists — SDK then
//     responds 400 (streamable.go getServer-returns-nil contract)
//   - aggregate server (fall-through) for any other shape, including paths
//     that do not begin with "/mcp" (middleware only mounts this handler on
//     /mcp and /mcp/*, so this branch is effectively unreachable but
//     defensive).
//
// This is a free-standing method rather than an inline closure so it can
// be unit-tested via server_proxy_test.go (T16.1.6).
func (s *Server) mcpServerForRequest(r *http.Request) *mcp.Server {
	agg := s.gw.Server()

	remainder := strings.TrimPrefix(r.URL.Path, "/mcp")
	// Exact /mcp (or, defensively, any non-/mcp path routed here).
	if remainder == "" || remainder == "/" {
		return agg
	}
	if !strings.HasPrefix(remainder, "/") {
		// Path like "/mcpfoo" — not under our scheme; fall through safely.
		return agg
	}
	remainder = strings.TrimPrefix(remainder, "/")

	// Only the first path segment is the candidate backend name. Any
	// trailing segments (e.g. a hypothetical "/mcp/context7/extra") are
	// ignored by this router; the SDK then receives the request and
	// decides on its own behavior. See T16.1.5.a analysis above.
	seg, _, _ := strings.Cut(remainder, "/")
	if seg == "" {
		return agg
	}
	// Validate the segment against the backend-name regex. Invalid names
	// fall back to the aggregate surface for defense-in-depth (see
	// T16.1.5.a: defensive against future SDK sub-path introductions).
	if err := models.ValidateServerName(seg); err != nil {
		return agg
	}
	// Registered backend? Return its per-backend server. Unregistered name
	// returns nil, which the SDK translates to a 400 response.
	return s.gw.ServerFor(seg)
}

// mcpTransportPolicy returns a handler wrapper that enforces the
// configured MCP transport policy. The mode is read per-request from
// s.cfg so live config reloads take effect without restarting the
// daemon (PAL-2026-04-18 MEDIUM).
//
// Policies:
//   - loopback-only (default / empty / unknown): refuse non-loopback
//     RemoteAddr with 403 transport_policy_denied; also refuse
//     cross-site browser-originated requests (Sec-Fetch-Site) to guard
//     against browser-to-localhost CSRF on /mcp (PAL-2026-04-18 HIGH).
//   - bearer-required: apply BearerAuthMiddleware on every request.
//
// See ADR-0003 §policy-matrix-mcp-modes.
func (s *Server) mcpTransportPolicy(authMW func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		// Wrap once; reused for every request in bearer-required mode.
		authedNext := authMW(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.cfgMu.RLock()
			mode := s.cfg.Gateway.AuthMCPTransport
			s.cfgMu.RUnlock()
			if mode == "" {
				mode = models.AuthMCPTransportLoopbackOnly
			}

			switch mode {
			case models.AuthMCPTransportBearerRequired:
				s.logMCPDecision(r, mode, "allow-if-bearer")
				authedNext.ServeHTTP(w, r)

			default: // loopback-only
				// Cross-site browser guard: reject non-same-origin/non-none
				// fetch metadata so a malicious web page cannot drive MCP
				// tool calls against the user's localhost daemon.
				if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
					s.logMCPDecision(r, mode, "deny-cross-site-browser")
					denyMCPTransport(w)
					return
				}
				host, _, err := net.SplitHostPort(r.RemoteAddr)
				if err != nil {
					host = r.RemoteAddr
				}
				ip := net.ParseIP(host)
				if ip == nil || !ip.IsLoopback() {
					s.logMCPDecision(r, mode, "deny-non-loopback")
					denyMCPTransport(w)
					return
				}
				s.logMCPDecision(r, mode, "allow-loopback")
				next.ServeHTTP(w, r)
			}
		})
	}
}

// denyMCPTransport writes the uniform 403 transport_policy_denied body.
func denyMCPTransport(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"error":"transport_policy_denied"}`))
}

// logMCPDecision emits one structured line per MCP transport request.
// Never logs the received Authorization header value — only the path
// the policy decision took.
func (s *Server) logMCPDecision(r *http.Request, mode, decision string) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	s.logger.Info("mcp transport request",
		"policy", mode,
		"remote", host,
		"decision", decision,
		"path", r.URL.Path,
	)
}

// handleVersion reports the daemon build metadata. Public endpoint.
func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"version": s.version,
	})
}

// ListenAndServe starts the HTTP server. Blocks until context is cancelled.
func (s *Server) ListenAndServe(ctx context.Context) error {
	s.cfgMu.RLock()
	port := s.cfg.Gateway.HTTPPort
	bindAddr := s.cfg.Gateway.BindAddress
	s.cfgMu.RUnlock()
	if bindAddr == "" {
		bindAddr = "127.0.0.1"
	}
	// Refuse non-loopback binding unless explicitly allowed (F-2 security fix).
	// Non-loopback + no auth = RCE via handleAddServer.
	nonLoopback, err := models.ValidateBindAddress(bindAddr)
	if err != nil {
		return fmt.Errorf("invalid bind_address %q: %w", bindAddr, err)
	}
	s.cfgMu.RLock()
	allowRemote := s.cfg.Gateway.AllowRemote
	tlsCertPath := s.cfg.Gateway.TLSCertPath
	tlsKeyPath := s.cfg.Gateway.TLSKeyPath
	s.cfgMu.RUnlock()
	// T15B.3 (v1.5.0): refuse half-configured TLS. Prior behavior silently
	// fell back to plain HTTP when only one of the two paths was set — an
	// operator who edited gateway.tls_cert_path but forgot tls_key_path
	// would see no warning, assume TLS, actually run cleartext. Wording is
	// deliberate and names BOTH paths — tests grep for it, and the
	// CHANGELOG entry quotes it verbatim (same pattern as middleware.go:16).
	if tlsCertPath != "" && tlsKeyPath == "" {
		return fmt.Errorf("TLS is half-configured: gateway.tls_cert_path is set but gateway.tls_key_path is empty — " +
			"both must be set to enable TLS, or both must be empty for plain HTTP")
	}
	if tlsKeyPath != "" && tlsCertPath == "" {
		return fmt.Errorf("TLS is half-configured: gateway.tls_key_path is set but gateway.tls_cert_path is empty — " +
			"both must be set to enable TLS, or both must be empty for plain HTTP")
	}
	tlsEnabled := tlsCertPath != "" && tlsKeyPath != ""

	if nonLoopback && !allowRemote {
		return fmt.Errorf("bind_address %q is non-loopback but allow_remote is not set — "+
			"refusing to start. Set gateway.allow_remote=true to override", bindAddr)
	}
	// T13B/F-7: when binding non-loopback with auth enabled, require TLS.
	// --no-auth paths have already navigated the combo guard in setupAuth
	// and explicit WARN lines; we don't re-enforce TLS there (the
	// operator has signed the "no auth" blood pact). But when auth IS
	// enabled, cleartext Bearer tokens on the wire are unacceptable.
	if nonLoopback && s.authEnabled && !tlsEnabled {
		return fmt.Errorf("bind_address %q is non-loopback and Bearer auth is enabled — "+
			"refusing to start without TLS. Set gateway.tls_cert_path and gateway.tls_key_path, "+
			"or bind to a loopback address, or run with --no-auth (DANGEROUS)", bindAddr)
	}
	if nonLoopback {
		switch {
		case s.authEnabled && tlsEnabled:
			s.logger.Info("binding to non-loopback address with TLS + Bearer auth", "addr", bindAddr)
		case s.authEnabled:
			// Unreachable — guard above refuses to start. Kept for clarity
			// if someone relaxes the guard in the future.
			s.logger.Warn("binding to non-loopback address — Bearer auth is enabled without TLS (cleartext token exposure)", "addr", bindAddr)
		default:
			s.logger.Warn("binding to non-loopback address with allow_remote=true — gateway is exposed and authentication is DISABLED (--no-auth)", "addr", bindAddr)
		}
	}

	// PAL HIGH fix: use net.JoinHostPort so IPv6 addresses like "::1"
	// get the mandatory [brackets]. fmt.Sprintf("%s:%d") would produce
	// "::1:8765" which net.Listen rejects.
	addr := net.JoinHostPort(bindAddr, strconv.Itoa(port))
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}

	s.httpServer = &http.Server{
		Handler:           s.Handler(),
		ReadTimeout:       30 * time.Second,
		ReadHeaderTimeout: 10 * time.Second, // CR-7 fix: prevent Slowloris
		WriteTimeout:      60 * time.Second, // H-001 fix: prevent slow-write exhaustion
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10, // 64 KB — M-003 fix
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.httpServer.Shutdown(shutdownCtx)
	}()

	// Publish Addr() last. When observers see Addr() != nil, s.httpServer
	// is already initialized and the shutdown goroutine is wired up, so
	// the invariant "Addr() non-nil ⇒ ready to serve" holds without any
	// half-initialized window on s.httpServer.
	s.listenerMu.Lock()
	s.listenerAddr = listener.Addr()
	s.listenerMu.Unlock()

	// Branch on TLS — one path uses ServeTLS, the other Serve. The
	// listener is created the same way because the TLS handshake happens
	// per-connection inside http.Server.
	if tlsEnabled {
		s.logger.Info("HTTPS server listening", "addr", addr, "cert", tlsCertPath)
		if err := s.httpServer.ServeTLS(listener, tlsCertPath, tlsKeyPath); err != nil && err != http.ErrServerClosed {
			return err
		}
		return nil
	}
	s.logger.Info("HTTP server listening", "addr", addr)
	if err := s.httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// --- REST handlers ---

// ServerView is the API response for a server entry (no secrets).
type ServerView struct {
	Name         string              `json:"name"`
	Status       models.ServerStatus `json:"status"`
	Transport    string              `json:"transport"`
	PID          int                 `json:"pid,omitempty"`
	Tools        []models.ToolInfo   `json:"tools,omitempty"`
	RestartCount int                 `json:"restart_count"`
	LastError    string              `json:"last_error,omitempty"`
	EnvKeys      []string            `json:"env_keys,omitempty"`
	HeaderKeys   []string            `json:"header_keys,omitempty"`
}

func toView(e models.ServerEntry) ServerView {
	v := ServerView{
		Name:         e.Name,
		Status:       e.Status,
		Transport:    e.Config.TransportType(),
		PID:          e.PID,
		Tools:        e.Tools,
		RestartCount: e.RestartCount,
		LastError:    e.LastError,
	}
	// Expose env key names (not values) and header key names.
	for _, env := range e.Config.Env {
		if key, _, ok := strings.Cut(env, "="); ok {
			v.EnvKeys = append(v.EnvKeys, key)
		}
	}
	for k := range e.Config.Headers {
		v.HeaderKeys = append(v.HeaderKeys, k)
	}
	sort.Strings(v.HeaderKeys)
	return v
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	entries := s.lm.Entries()
	total, running := len(entries), 0
	for _, e := range entries {
		if e.Status == models.StatusRunning {
			running++
		}
	}
	authState := "enabled"
	if !s.authEnabled {
		authState = "disabled"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"servers": total,
		"running": running,
		"auth":    authState,
	})
}

func (s *Server) handleListServers(w http.ResponseWriter, _ *http.Request) {
	entries := s.lm.Entries()
	views := make([]ServerView, len(entries))
	for i, e := range entries {
		views[i] = toView(e)
	}
	writeJSON(w, http.StatusOK, views)
}

func (s *Server) handleGetServer(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	entry, ok := s.lm.Entry(name)
	if !ok {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}
	writeJSON(w, http.StatusOK, toView(entry))
}

func (s *Server) handleAddServer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string              `json:"name"`
		Config models.ServerConfig `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if err := models.ValidateServerName(req.Name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := req.Config.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := s.lm.AddServer(req.Name, &req.Config); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	// CR-4 fix: update in-memory config to reflect the new server.
	s.cfgMu.Lock()
	s.cfg.Servers[req.Name] = &req.Config
	data := s.marshalConfig()
	s.cfgMu.Unlock()
	s.flushConfig(data)

	// AR-5 fix: auto-start unless disabled. A concurrent config watcher reload
	// may also call lm.Start; lm's internal starting guard prevents double-start.
	if !req.Config.Disabled {
		if err := s.lm.Start(r.Context(), req.Name); err != nil {
			s.logger.Warn("auto-start after add failed", "server", req.Name, "error", err)
		}
		if s.gw != nil {
			s.gw.RebuildTools()
		}
	}
	// T16.2.4: regen Claude Code plugin's .mcp.json (best-effort; any
	// failure is logged but never surfaced to the REST client).
	s.TriggerPluginRegen()

	writeJSON(w, http.StatusCreated, map[string]string{"status": "added"})
}

func (s *Server) handleRemoveServer(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := s.lm.RemoveServer(r.Context(), name); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	// CR-4 fix: update in-memory config. There is a theoretical TOCTOU window
	// between lm.RemoveServer and this persist, but the config watcher's 500ms
	// debounce makes the race practically impossible for a localhost daemon.
	s.cfgMu.Lock()
	delete(s.cfg.Servers, name)
	data := s.marshalConfig()
	s.cfgMu.Unlock()
	s.flushConfig(data)
	if s.gw != nil {
		s.gw.RebuildTools()
	}
	// T16.2.4: regen Claude Code plugin's .mcp.json after removal.
	s.TriggerPluginRegen()

	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

func (s *Server) handlePatchServer(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	_, ok := s.lm.Entry(name)
	if !ok {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}

	var patch models.ServerPatch
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	// Validate incoming env entries individually before merge.
	if err := models.ValidateEnvEntries(patch.AddEnv); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := models.ValidateHeaderEntries(patch.AddHeaders); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	needsRestart := false

	s.cfgMu.Lock()
	sc, scOK := s.cfg.Servers[name]
	if !scOK {
		s.cfgMu.Unlock()
		writeError(w, http.StatusNotFound, "server not found")
		return
	}

	// Work on a shallow copy to avoid partial mutation on validation failure.
	// Env is safe: removeEnvKeys/mergeEnv produce new slices.
	// Headers must be deep-copied before mutation.
	scCopy := *sc

	// Apply disabled toggle.
	if patch.Disabled != nil {
		scCopy.Disabled = *patch.Disabled
	}

	// Apply env changes.
	if len(patch.RemoveEnv) > 0 {
		scCopy.Env = removeEnvKeys(scCopy.Env, patch.RemoveEnv)
		needsRestart = true
	}
	if len(patch.AddEnv) > 0 {
		scCopy.Env = mergeEnv(scCopy.Env, patch.AddEnv)
		needsRestart = true
	}

	// Deep-copy headers map before mutation to preserve original on validation failure.
	if len(patch.RemoveHeaders) > 0 || len(patch.AddHeaders) > 0 {
		newHeaders := make(map[string]string, len(scCopy.Headers)+len(patch.AddHeaders))
		for k, v := range scCopy.Headers {
			newHeaders[k] = v
		}
		scCopy.Headers = newHeaders
	}
	if len(patch.RemoveHeaders) > 0 {
		for _, k := range patch.RemoveHeaders {
			delete(scCopy.Headers, k)
		}
		needsRestart = true
	}
	if len(patch.AddHeaders) > 0 {
		for k, v := range patch.AddHeaders {
			scCopy.Headers[k] = v
		}
		needsRestart = true
	}

	// Validate full merged config.
	if err := scCopy.Validate(); err != nil {
		s.cfgMu.Unlock()
		writeError(w, http.StatusBadRequest, fmt.Sprintf("merged config invalid: %v", err))
		return
	}

	// Commit validated copy back to the live config.
	*sc = scCopy
	data := s.marshalConfig()
	s.cfgMu.Unlock()
	s.flushConfig(data)

	// Handle disabled toggle (outside mutexes).
	if patch.Disabled != nil {
		if *patch.Disabled {
			_ = s.lm.Stop(r.Context(), name)
			s.lm.SetStatus(name, models.StatusDisabled, "disabled by user")
		} else {
			current, _ := s.lm.Entry(name)
			if current.Status == models.StatusDisabled {
				if err := s.lm.Start(r.Context(), name); err != nil {
					writeError(w, http.StatusInternalServerError, err.Error())
					return
				}
			}
		}
		// T16.2.4: disabled-flag flip changes which backends appear in
		// the plugin's .mcp.json. env/header-only patches are invisible
		// to the plugin surface (they only affect the backend process),
		// so regen is gated specifically on the Disabled toggle.
		s.TriggerPluginRegen()
	} else if needsRestart {
		// Restart server to pick up env/header changes (outside mutexes).
		if err := s.lm.Restart(r.Context(), name); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// removeEnvKeys removes entries from env whose key matches any in keys.
// Matches by exact key with "=" suffix — removing "FOO" does not remove "FOOBAR".
func removeEnvKeys(env []string, keys []string) []string {
	remove := make(map[string]bool, len(keys))
	for _, k := range keys {
		remove[k] = true
	}
	result := make([]string, 0, len(env))
	for _, e := range env {
		key, _, ok := strings.Cut(e, "=")
		if ok && remove[key] {
			continue
		}
		result = append(result, e)
	}
	return result
}

// mergeEnv appends new env entries, replacing existing ones with the same key.
// When add contains duplicate keys, the last value wins (e.g. ["K=1","K=2"] → "K=2").
// The merged entry appears at the position of the first occurrence in add.
func mergeEnv(env []string, add []string) []string {
	// Build map of new keys for dedup.
	addKeys := make(map[string]string, len(add))
	for _, e := range add {
		if key, _, ok := strings.Cut(e, "="); ok {
			addKeys[key] = e
		}
	}
	// Keep existing entries that are NOT being replaced.
	result := make([]string, 0, len(env)+len(add))
	for _, e := range env {
		key, _, ok := strings.Cut(e, "=")
		if ok {
			if _, replacing := addKeys[key]; replacing {
				continue // skip — will be replaced by new value
			}
		}
		result = append(result, e)
	}
	// Append all new entries (deduped by key).
	seen := make(map[string]bool, len(addKeys))
	for _, e := range add {
		if key, _, ok := strings.Cut(e, "="); ok {
			if seen[key] {
				continue
			}
			seen[key] = true
			result = append(result, addKeys[key])
		}
	}
	return result
}

func (s *Server) handleRestartServer(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := s.lm.Restart(r.Context(), name); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "restarted"})
}

func (s *Server) handleResetCircuit(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if s.monitor == nil {
		writeError(w, http.StatusServiceUnavailable, "health monitor not available")
		return
	}
	if err := s.monitor.ResetCircuit(r.Context(), name); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "circuit reset"})
}

func (s *Server) handleCallTool(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var req struct {
		Tool      string         `json:"tool"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Tool == "" {
		writeError(w, http.StatusBadRequest, "tool is required")
		return
	}

	result, err := s.gw.Router().CallDirect(r.Context(), name, req.Tool, req.Arguments)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	// D2-01 fix: normalize nil Content to empty slice to avoid JSON null.
	if result.Content == nil {
		result.Content = []mcp.Content{}
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleListTools(w http.ResponseWriter, _ *http.Request) {
	tools := s.gw.ListTools()
	writeJSON(w, http.StatusOK, tools)
}

// handleMetrics returns operational metrics for all servers and token cost estimates.
// Note: entries and server metrics are fetched in two steps (lm.Entries() then
// AllServerMetrics) — a server added/removed between the calls may appear stale.
// This is acceptable eventual consistency for a monitoring endpoint.
// Token estimation is O(n) over all tools with json.Marshal per schema; acceptable
// since /metrics is not a hot-path endpoint.
func (s *Server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	if s.monitor == nil {
		writeError(w, http.StatusServiceUnavailable, "health monitor not available")
		return
	}

	entries := s.lm.Entries()
	serverMetrics := s.monitor.AllServerMetrics(entries)

	// Token estimation: iterate all tools visible to clients (rune-based ≈ GPT tokens).
	tools := s.gw.ListTools()
	var descTokens, schemaTokens int
	for _, tool := range tools {
		descTokens += utf8.RuneCountInString(tool.Description) / 4
		if tool.InputSchema != nil {
			if b, err := json.Marshal(tool.InputSchema); err == nil {
				schemaTokens += utf8.RuneCount(b) / 4
			} else {
				s.logger.Debug("schema marshal failed for token estimation", "tool", tool.Name, "error", err)
			}
		}
	}

	resp := models.MetricsResponse{
		Timestamp:     time.Now(),
		GatewayUptime: models.Duration(s.monitor.GatewayUptime()),
		Servers:       serverMetrics,
		Tokens: models.TokenMetrics{
			TotalTools:      len(tools),
			EstSchemaTokens: schemaTokens,
			EstDescTokens:   descTokens,
			EstTotalTokens:  descTokens + schemaTokens,
		},
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleServerLogs(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	ring, ok := s.lm.LogBuffer(name)
	if !ok {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// F-8 fix: disable WriteTimeout for SSE connections. The server's default
	// 60s WriteTimeout kills long-lived SSE streams that idle between events.
	// http.ResponseController.SetWriteDeadline(time.Time{}) clears the deadline.
	if rc := http.NewResponseController(w); rc != nil {
		_ = rc.SetWriteDeadline(time.Time{}) // zero = no deadline
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	// CR-11 fix: subscribe BEFORE reading history to avoid missing lines.
	ch := ring.Subscribe()
	defer ring.Unsubscribe(ch)

	// Send buffered history first (may overlap slightly with ch — acceptable).
	for _, line := range ring.Lines() {
		writeSSEData(w, line.Text)
	}
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case line, ok := <-ch:
			if !ok {
				return
			}
			writeSSEData(w, line.Text)
			flusher.Flush()
		}
	}
}

// writeSSEData writes a single SSE data frame, sanitizing embedded newlines
// to prevent SSE frame injection from backend log output.
func writeSSEData(w http.ResponseWriter, text string) {
	safe := strings.ReplaceAll(text, "\r", "")
	safe = strings.ReplaceAll(safe, "\n", "\\n")
	fmt.Fprintf(w, "data: %s\n\n", safe)
}

// marshalConfig serializes the current config to JSON bytes.
// Must be called while cfgMu is held (json.MarshalIndent iterates the Servers map).
func (s *Server) marshalConfig() []byte {
	if s.configPath == "" {
		return nil
	}
	data, err := json.MarshalIndent(s.cfg, "", "  ")
	if err != nil {
		s.logger.Error("failed to marshal config", "error", err)
		return nil
	}
	return append(data, '\n')
}

// flushConfig writes pre-serialized config data to disk. Safe to call without cfgMu.
// Serialized by flushMu to prevent out-of-order writes from concurrent handlers (F2 fix).
func (s *Server) flushConfig(data []byte) {
	if data == nil || s.configPath == "" {
		return
	}
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	if err := config.SaveBytes(s.configPath, data); err != nil {
		s.logger.Error("failed to persist config", "error", err)
	}
}

// maxBodySize returns middleware that limits request body to maxBytes.
// Pre-reads the body through http.MaxBytesReader; returns 413 if the limit
// is exceeded, then replaces r.Body with the buffered content (T2.2).
func maxBodySize(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil && r.Body != http.NoBody {
				limited := http.MaxBytesReader(w, r.Body, maxBytes)
				data, err := io.ReadAll(limited)
				if err != nil {
					var mbe *http.MaxBytesError
					if errors.As(err, &mbe) {
						writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
						return
					}
					writeError(w, http.StatusBadRequest, "failed to read request body")
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(data))
			}
			next.ServeHTTP(w, r)
		})
	}
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// csrfProtect rejects cross-origin browser requests (F-3 security fix).
// Browsers send Sec-Fetch-Site on all requests. If present and not
// "same-origin" or "none", the request is from a different origin (CSRF).
// Non-browser clients (curl, CLI) never send Sec-Fetch-Site, so they pass.
// Safe methods (GET, HEAD, OPTIONS) are exempt — they must not have side effects.
func csrfProtect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
				writeError(w, http.StatusForbidden, "cross-origin request blocked")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
