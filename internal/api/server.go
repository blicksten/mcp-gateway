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
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"mcp-gateway/internal/auth"
	"mcp-gateway/internal/config"
	"mcp-gateway/internal/health"
	"mcp-gateway/internal/lifecycle"
	"mcp-gateway/internal/models"
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
	r.Use(middleware.RealIP)

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
	mcpServer := s.gw.Server()
	streamableHandler := mcp.NewStreamableHTTPHandler(
		func(r *http.Request) *mcp.Server { return mcpServer }, nil,
	)
	sseHandler := mcp.NewSSEHandler(
		func(r *http.Request) *mcp.Server { return mcpServer }, nil,
	)

	s.cfgMu.RLock()
	transportMode := s.cfg.Gateway.AuthMCPTransport
	s.cfgMu.RUnlock()
	if transportMode == "" {
		transportMode = models.AuthMCPTransportLoopbackOnly
	}

	mcpPolicy := s.mcpTransportPolicy(transportMode, authMW)

	r.Handle("/mcp", mcpPolicy(streamableHandler))
	r.Handle("/mcp/*", mcpPolicy(streamableHandler))
	r.Handle("/sse", mcpPolicy(sseHandler))
	r.Handle("/sse/*", mcpPolicy(sseHandler))

	return r
}

// mcpTransportPolicy returns a handler wrapper enforcing the configured
// MCP transport policy (loopback-only vs bearer-required). See
// ADR-0003 §policy-matrix-mcp-modes.
func (s *Server) mcpTransportPolicy(mode string, authMW func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	switch mode {
	case models.AuthMCPTransportBearerRequired:
		return func(next http.Handler) http.Handler {
			// Bearer required. Auth is enforced even if --no-auth is set
			// on the REST side: MCP tool calls are security-sensitive
			// enough that bearer-required must actually require a bearer.
			// Startup guards in main.go refuse bearer-required + --no-auth.
			wrapped := authMW(next)
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				s.logMCPDecision(r, mode, "allow-if-bearer")
				wrapped.ServeHTTP(w, r)
			})
		}
	default: // loopback-only (default / empty / unknown)
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				host, _, err := net.SplitHostPort(r.RemoteAddr)
				if err != nil {
					host = r.RemoteAddr
				}
				ip := net.ParseIP(host)
				if ip == nil || !ip.IsLoopback() {
					s.logMCPDecision(r, mode, "deny-non-loopback")
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write([]byte(`{"error":"transport_policy_denied"}`))
					return
				}
				s.logMCPDecision(r, mode, "allow-loopback")
				next.ServeHTTP(w, r)
			})
		}
	}
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
	s.cfgMu.RUnlock()
	if nonLoopback && !allowRemote {
		return fmt.Errorf("bind_address %q is non-loopback but allow_remote is not set — "+
			"refusing to start without authentication. Set gateway.allow_remote=true "+
			"to override (DANGEROUS: REST API has no authentication)", bindAddr)
	}
	if nonLoopback {
		s.logger.Warn("binding to non-loopback address with allow_remote=true — gateway is exposed to the network; authentication is NOT enforced", "addr", bindAddr)
	}

	addr := fmt.Sprintf("%s:%d", bindAddr, port)
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
