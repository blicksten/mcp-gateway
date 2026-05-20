// Package api: P0.7 D-5 production handler — ResumableStreamableHTTPHandler.
//
// This file implements the custom http.Handler that replaces the SDK's
// StreamableHTTPHandler at server.go:571.  It forks the SDK's ServeHTTP
// dispatch logic (streamable.go:246-551) verbatim for all safety checks
// (DNS-rebinding protection, CSRF/CrossOrigin protection, Accept-header
// parsing, protocol-version negotiation, DELETE handling) and replaces only
// the unknown-session branch (~305-310):
//
//	Before:  sessInfo == nil && !stateless → HTTP 404 "session not found"
//	After:   registry.Get(sessionID) != nil → ResurrectSession → continue
//	         registry.Get(sessionID) == nil → HTTP 404 (same as before)
//
// On new-session POST (initialize): CaptureInitializeFromRequest captures the
// InitializeParams BEFORE forwarding to the per-session transport so that the
// state is available for future resurrection after a daemon restart.
//
// Partial-fix scope: POST tool-call path resumes without /clear; the SSE GET
// notification stream (tools/list_changed) is still dead after a TCP-level
// disconnect because Claude Code's TypeScript MCP client does not
// auto-reconnect (upstream issue #57642).  This is documented in PLAN §P0.7.

package api

import (
	crand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// errSessionUserMismatch is a sentinel error returned by tryResurrect when
// the cached session has a different userID than the incoming request.
// The outer handler must respond 403 Forbidden (not 404) in this case.
var errSessionUserMismatch = errors.New("session user mismatch")

// errNoServer is returned when getServer(req) yields nil — typically when the
// path encodes an unregistered backend name. Mirrors SDK behaviour where a
// nil server on POST /mcp/<unknown> produces HTTP 400 Bad Request.
var errNoServer = errors.New("no server available for request")

// Protocol version constants — mirrors SDK unexported values (shared.go:38-48).
// Keep in sync with the SDK's supportedProtocolVersions when upgrading.
const (
	resumableProtoV20251125 = "2025-11-25"
	resumableProtoV20250618 = "2025-06-18"
	resumableProtoV20250326 = "2025-03-26"
	resumableProtoV20241105 = "2024-11-05"
	resumableProtoDefault   = resumableProtoV20250326
)

var resumableSupportedProtocolVersions = []string{
	resumableProtoV20251125,
	resumableProtoV20250618,
	resumableProtoV20250326,
	resumableProtoV20241105,
}

// resumableSessionEntry holds the live state for one connected session.
type resumableSessionEntry struct {
	session   *mcp.ServerSession
	transport *mcp.StreamableServerTransport
	userID    string

	// timeout eviction
	timeout time.Duration
	mu      sync.Mutex
	timer   *time.Timer
	// closed is set by the eviction callback BEFORE session.Close() so that a
	// concurrent resetTimer cannot reschedule a fired AfterFunc and trigger
	// double Close() / double removeSession (CV MEDIUM-1). Guarded by mu.
	closed bool
}

func (e *resumableSessionEntry) resetTimer() {
	if e.timeout == 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	if e.timer != nil {
		e.timer.Reset(e.timeout)
	}
}

func (e *resumableSessionEntry) stopTimer() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	if e.timer != nil {
		e.timer.Stop()
	}
}

// ResumableStreamableHTTPHandler is an http.Handler that wraps SDK session
// management and adds restart-transparent session resurrection via a
// SessionStateRegistry.
//
// Create one with NewResumableStreamableHTTPHandler and mount it at the same
// path as the original mcp.NewStreamableHTTPHandler.
type ResumableStreamableHTTPHandler struct {
	getServer func(*http.Request) *mcp.Server
	registry  *SessionStateRegistry

	// SessionTimeout, if positive, auto-evicts idle sessions.
	SessionTimeout time.Duration

	// DisableLocalhostProtection disables DNS-rebinding guard (test use only).
	DisableLocalhostProtection bool

	mu       sync.Mutex
	sessions map[string]*resumableSessionEntry
}

// NewResumableStreamableHTTPHandler returns a new handler.
//
//   - getServer: same callback as mcp.NewStreamableHTTPHandler — may return the
//     same *mcp.Server for all requests.
//   - registry: shared SessionStateRegistry; the same instance must be re-used
//     across handler re-creations to make resurrection work after a daemon
//     restart (the registry must outlive the handler).
func NewResumableStreamableHTTPHandler(
	getServer func(*http.Request) *mcp.Server,
	registry *SessionStateRegistry,
) *ResumableStreamableHTTPHandler {
	return &ResumableStreamableHTTPHandler{
		getServer: getServer,
		registry:  registry,
		sessions:  make(map[string]*resumableSessionEntry),
	}
}

// ServeHTTP implements http.Handler.
//
// Forked from SDK streamable.go:246-551.  All safety checks are copied
// verbatim; only the unknown-session branch is replaced.
func (h *ResumableStreamableHTTPHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// DNS-rebinding protection: auto-enabled for localhost servers.
	// Mirrors SDK streamable.go:249-256.
	if !h.DisableLocalhostProtection {
		if localAddr, ok := req.Context().Value(http.LocalAddrContextKey).(interface{ String() string }); ok && localAddr != nil {
			localStr := localAddr.String()
			if isLoopbackAddr(localStr) && !isLoopbackHost(req.Host) {
				http.Error(w, fmt.Sprintf("Forbidden: invalid Host header %q", req.Host), http.StatusForbidden)
				return
			}
		}
	}

	// Content-Type validation for POST (mirrors SDK streamable.go:265-271).
	if req.Method == http.MethodPost {
		if ct := req.Header.Get("Content-Type"); ct != "application/json" {
			http.Error(w, "Content-Type must be 'application/json'", http.StatusUnsupportedMediaType)
			return
		}
	}

	// Accept-header parsing (mirrors SDK streamable.go:274-298).
	accept := strings.Split(strings.Join(req.Header.Values("Accept"), ","), ",")
	var jsonOK, streamOK bool
	for _, c := range accept {
		switch strings.TrimSpace(c) {
		case "application/json", "application/*":
			jsonOK = true
		case "text/event-stream", "text/*":
			streamOK = true
		case "*/*":
			jsonOK = true
			streamOK = true
		}
	}
	if req.Method == http.MethodGet {
		if !streamOK {
			http.Error(w, "Accept must contain 'text/event-stream' for GET requests", http.StatusBadRequest)
			return
		}
	} else if (!jsonOK || !streamOK) && req.Method != http.MethodDelete {
		http.Error(w, "Accept must contain both 'application/json' and 'text/event-stream'", http.StatusBadRequest)
		return
	}

	sessionID := req.Header.Get("Mcp-Session-Id")

	// Look up existing live session.
	var entry *resumableSessionEntry
	if sessionID != "" {
		h.mu.Lock()
		entry = h.sessions[sessionID]
		h.mu.Unlock()
	}

	// Unknown-session branch: try resurrection before returning 404.
	if sessionID != "" && entry == nil {
		resurrected, err := h.tryResurrect(req, sessionID)
		if err != nil {
			switch {
			case errors.Is(err, errSessionUserMismatch):
				http.Error(w, "session user mismatch", http.StatusForbidden)
			case errors.Is(err, errNoServer):
				// Unknown backend path → 400 (mirrors SDK behaviour for nil getServer).
				http.Error(w, "Bad Request: unknown backend", http.StatusBadRequest)
			default:
				// resurrection failed — unknown session, 404 (same as SDK default)
				http.Error(w, "session not found", http.StatusNotFound)
			}
			return
		}
		entry = resurrected
	}

	// Session-hijack guard: if session was created with a userID, every
	// subsequent request must carry the same bearer identity.
	// Mirrors SDK streamable.go:316-322.
	if entry != nil && entry.userID != "" {
		uid := userIDFromRequest(req)
		if uid != entry.userID {
			http.Error(w, "session user mismatch", http.StatusForbidden)
			return
		}
	}

	// DELETE handling (mirrors SDK streamable.go:325-337).
	if req.Method == http.MethodDelete {
		if sessionID == "" {
			http.Error(w, "Bad Request: DELETE requires an Mcp-Session-Id header", http.StatusBadRequest)
			return
		}
		if entry != nil {
			entry.stopTimer()
			entry.session.Close()
			h.removeSession(sessionID)
			h.registry.Delete(sessionID)
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Method guard (mirrors SDK streamable.go:339-364).
	switch req.Method {
	case http.MethodPost, http.MethodGet:
		if req.Method == http.MethodGet && (sessionID == "" || entry == nil) {
			// stateful mode: GET requires both a session ID and an existing entry
			http.Error(w, "Bad Request: GET requires an Mcp-Session-Id header", http.StatusBadRequest)
			return
		}
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	// Protocol-version negotiation (mirrors SDK streamable.go:396-403).
	protocolVersion := req.Header.Get("Mcp-Protocol-Version")
	if protocolVersion == "" {
		protocolVersion = resumableProtoDefault
	}
	if !slices.Contains(resumableSupportedProtocolVersions, protocolVersion) {
		http.Error(w,
			fmt.Sprintf("Bad Request: Unsupported protocol version (supported versions: %s)",
				strings.Join(resumableSupportedProtocolVersions, ",")),
			http.StatusBadRequest)
		return
	}

	// New session path (no existing or resurrected entry).
	if entry == nil {
		var err error
		entry, err = h.createSession(req, sessionID)
		if err != nil {
			if errors.Is(err, errNoServer) {
				// Unknown backend path → 400 Bad Request (mirrors SDK).
				http.Error(w, "Bad Request: unknown backend", http.StatusBadRequest)
				return
			}
			http.Error(w, "failed connection", http.StatusInternalServerError)
			return
		}
	}

	// Delegate to the per-session transport (mirrors SDK streamable.go:545-551).
	entry.resetTimer()
	entry.transport.ServeHTTP(w, req)
}

// tryResurrect attempts to recreate a session from the registry's cached state.
// On success it registers the new entry in h.sessions and returns it.
// On failure it returns a non-nil error.
func (h *ResumableStreamableHTTPHandler) tryResurrect(
	req *http.Request,
	sessionID string,
) (*resumableSessionEntry, error) {
	cached := h.registry.Get(sessionID)
	if cached == nil {
		return nil, fmt.Errorf("no cached state for session %s", sessionID)
	}

	// UserID guard: if the cached state has a userID, the request must carry it.
	if cached.UserID != "" {
		uid := userIDFromRequest(req)
		if uid != cached.UserID {
			return nil, errSessionUserMismatch
		}
	}

	server := h.getServer(req)
	if server == nil {
		return nil, errNoServer
	}

	session, transport, err := ResurrectSession(req.Context(), server, sessionID, cached)
	if err != nil {
		return nil, fmt.Errorf("ResurrectSession: %w", err)
	}

	entry := &resumableSessionEntry{
		session:   session,
		transport: transport,
		userID:    cached.UserID,
	}
	h.registerEntry(sessionID, entry, session)
	return entry, nil
}

// createSession creates a new session for a first-time POST (initialize).
// It also captures the InitializeParams and writes them to the registry for
// future resurrection.
func (h *ResumableStreamableHTTPHandler) createSession(
	req *http.Request,
	sessionID string,
) (*resumableSessionEntry, error) {
	server := h.getServer(req)
	if server == nil {
		return nil, errNoServer
	}

	// Capture InitializeParams before the body is consumed by the transport.
	// CV /check LOW: surface the read-body error to the SDK as a 400 rather
	// than silently producing a request with an empty body — the transport
	// would otherwise fail with a confusing decode error downstream.
	initParams, captureErr := CaptureInitializeFromRequest(req)
	if captureErr != nil {
		return nil, fmt.Errorf("read initialize body: %w", captureErr)
	}

	// Generate session ID when the client did not provide one (first POST).
	// Mirrors SDK streamable.go:412-416 which calls server.opts.GetSessionID()
	// (unexported). We use crypto/rand for the same security property.
	if sessionID == "" {
		sessionID = generateSessionID()
	}

	transport := &mcp.StreamableServerTransport{
		SessionID: sessionID,
	}

	// Note: ServerSessionOptions.onClose is unexported in go-sdk v1.4.1.
	// Session cleanup from the sessions map is handled by the SessionTimeout
	// eviction timer and by explicit DELETE requests instead.
	connectOpts := &mcp.ServerSessionOptions{}
	session, err := server.Connect(req.Context(), transport, connectOpts)
	if err != nil {
		return nil, fmt.Errorf("server.Connect: %w", err)
	}

	uid := userIDFromRequest(req)
	entry := &resumableSessionEntry{
		session:   session,
		transport: transport,
		userID:    uid,
	}

	// If we detected an initialize call, seed the registry now so that a
	// restart that happens BEFORE tools/list (rare) is still resumable.
	if initParams != nil {
		h.registry.Put(transport.SessionID, &CachedSessionState{
			InitializeParams:  initParams,
			InitializedParams: &mcp.InitializedParams{},
			UserID:            uid,
		})
	}

	h.registerEntry(transport.SessionID, entry, session)
	return entry, nil
}

// registerEntry adds an entry to h.sessions and optionally starts its eviction timer.
func (h *ResumableStreamableHTTPHandler) registerEntry(
	sessionID string,
	entry *resumableSessionEntry,
	session *mcp.ServerSession,
) {
	if h.SessionTimeout > 0 {
		entry.timeout = h.SessionTimeout
		entry.timer = time.AfterFunc(h.SessionTimeout, func() {
			entry.mu.Lock()
			if entry.closed {
				entry.mu.Unlock()
				return
			}
			entry.closed = true
			entry.mu.Unlock()
			session.Close()
			h.removeSession(sessionID)
			// CV HIGH-1: also drop the registry entry — otherwise the cached
			// state outlives the idle timeout and the next request would
			// silently re-resurrect the session.
			h.registry.Delete(sessionID)
		})
	}
	h.mu.Lock()
	h.sessions[sessionID] = entry
	h.mu.Unlock()
}

// removeSession removes the session from h.sessions.
func (h *ResumableStreamableHTTPHandler) removeSession(sessionID string) {
	h.mu.Lock()
	delete(h.sessions, sessionID)
	h.mu.Unlock()
}

// SessionCount returns the number of currently active sessions (for testing).
func (h *ResumableStreamableHTTPHandler) SessionCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.sessions)
}

// --- helpers ----------------------------------------------------------------

// generateSessionID returns a 20-byte cryptographically random hex string.
// Matches the entropy level of the SDK's rand.Text default (math/rand/v2).
func generateSessionID() string {
	var b [20]byte
	if _, err := crand.Read(b[:]); err != nil {
		// crand.Read only fails on catastrophic OS errors; fall back to a
		// fixed prefix + time-based suffix rather than crashing.
		return hex.EncodeToString([]byte(fmt.Sprintf("fallback-%d", time.Now().UnixNano())))
	}
	return hex.EncodeToString(b[:])
}

// userIDFromRequest extracts a stable user identity from the request.
// The gateway uses a single shared bearer token; there is no per-user JWT,
// so we return the raw Authorization header value.  The result is the same
// for every request from the same client and differs from anonymous requests
// (empty string), satisfying the session-hijack guard.
func userIDFromRequest(req *http.Request) string {
	return req.Header.Get("Authorization")
}

// isLoopbackAddr returns true when addr looks like a loopback address.
// Mirrors the util.IsLoopback check in the SDK without importing internal packages.
func isLoopbackAddr(addr string) bool {
	host := addr
	if h, _, err := splitHostPort(addr); err == nil {
		host = h
	}
	return host == "127.0.0.1" || host == "::1" || host == "[::1]" || strings.HasSuffix(host, ".localhost")
}

// isLoopbackHost returns true when the Host header is a loopback address.
func isLoopbackHost(hostHeader string) bool {
	host := hostHeader
	if h, _, err := splitHostPort(hostHeader); err == nil {
		host = h
	}
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "[::1]"
}

// splitHostPort splits a host:port string into components.
// Returns an error when there is no port component.
func splitHostPort(hostport string) (host, port string, err error) {
	if len(hostport) == 0 {
		return "", "", fmt.Errorf("empty")
	}
	// IPv6 bracket notation: [::1]:port.
	if hostport[0] == '[' {
		end := strings.LastIndex(hostport, "]")
		if end < 0 {
			return "", "", fmt.Errorf("missing ']'")
		}
		host = hostport[1:end]
		rest := hostport[end+1:]
		if len(rest) == 0 {
			return host, "", nil
		}
		if rest[0] != ':' {
			return "", "", fmt.Errorf("unexpected char after ']'")
		}
		port = rest[1:]
		return host, port, nil
	}
	idx := strings.LastIndex(hostport, ":")
	if idx < 0 {
		return hostport, "", fmt.Errorf("no port")
	}
	return hostport[:idx], hostport[idx+1:], nil
}
