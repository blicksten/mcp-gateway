// Package api: P0.7 D-5 spike — viability proof for MCP session resurrection
// across gateway restart using public SDK primitives only.
//
// Background. The SDK's StreamableHTTPHandler keeps sessions in an UNEXPORTED
// in-memory map keyed by Mcp-Session-Id. On daemon restart the map is empty,
// and every request from a previously-connected client gets a 404 "session
// not found" until the client calls /clear. We cannot inject into the SDK's
// map. The agent's D-5 recommendation is a custom handler that owns its own
// map AND, on unknown-session POSTs, calls Server.Connect(...) with a cached
// ServerSessionState supplying the original client's InitializeParams.
//
// This spike is NOT the final handler. It proves the resurrection primitive
// in isolation: given a server, a sessionID, and cached InitializeParams, can
// we use ONLY public SDK API to construct a working ServerSession? If yes,
// the larger ServeHTTP fork is purely mechanical.
//
// Wired into: nothing yet. Existing /mcp dispatcher unchanged.

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// CachedSessionState is the minimum state needed to recreate an MCP session
// after the daemon process restart. Only fields that ServerSessionState
// requires are captured; resource subscriptions / sampling state are out of
// scope for the gateway-proxy use case (no client→server requests originate
// here — verified by grep in P0.7 audit).
type CachedSessionState struct {
	InitializeParams  *mcp.InitializeParams  `json:"initializeParams"`
	InitializedParams *mcp.InitializedParams `json:"initializedParams"`
	LogLevel          mcp.LoggingLevel       `json:"logLevel"`
	UserID            string                 `json:"userID,omitempty"`
	LastSeen          time.Time              `json:"lastSeen"`
}

// SessionStateRegistry is an in-memory, thread-safe cache of session states
// keyed by Mcp-Session-Id. The full D-5 implementation will add file-backed
// persistence at ~/.mcp-gateway/sessions.json so the cache survives restarts.
// This spike keeps it in-memory only to isolate the resurrection mechanic.
type SessionStateRegistry struct {
	mu     sync.Mutex
	states map[string]*CachedSessionState
}

// NewSessionStateRegistry constructs an empty registry.
func NewSessionStateRegistry() *SessionStateRegistry {
	return &SessionStateRegistry{states: make(map[string]*CachedSessionState)}
}

// Get returns the cached state for a session ID, or nil if absent.
// The returned pointer is to a fresh copy — safe to mutate without lock.
func (r *SessionStateRegistry) Get(id string) *CachedSessionState {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.states[id]
	if !ok {
		return nil
	}
	cp := *s
	return &cp
}

// Put records or replaces the cached state for a session ID.
func (r *SessionStateRegistry) Put(id string, s *CachedSessionState) {
	if s == nil {
		return
	}
	s.LastSeen = time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.states[id] = s
}

// Delete removes the cached state for a session ID (e.g. on DELETE /mcp).
func (r *SessionStateRegistry) Delete(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.states, id)
}

// Size returns the number of cached states (for metrics/tests).
func (r *SessionStateRegistry) Size() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.states)
}

// CaptureInitializeFromRequest scans an incoming POST body for an `initialize`
// JSON-RPC request. If found, returns the parsed InitializeParams. The body
// is rewound so downstream handlers can re-read it.
//
// Returns (nil, nil) when the body contains no initialize call.
func CaptureInitializeFromRequest(req *http.Request) (*mcp.InitializeParams, error) {
	if req.Method != http.MethodPost || req.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	_ = req.Body.Close()
	// Rewind for downstream.
	req.Body = io.NopCloser(newBytesReader(body))

	// JSON-RPC payload can be a single request or a batch. Sniff for either.
	trimmed := skipWhitespace(body)
	if len(trimmed) == 0 {
		return nil, nil
	}

	// Single request: {"method":"initialize",...}.
	if trimmed[0] == '{' {
		var msg struct {
			Method string                `json:"method"`
			Params *mcp.InitializeParams `json:"params"`
		}
		if err := json.Unmarshal(trimmed, &msg); err == nil && msg.Method == "initialize" {
			return msg.Params, nil
		}
		return nil, nil
	}
	// Batch: [ {...}, {...} ].
	if trimmed[0] == '[' {
		var batch []struct {
			Method string                `json:"method"`
			Params *mcp.InitializeParams `json:"params"`
		}
		if err := json.Unmarshal(trimmed, &batch); err == nil {
			for _, m := range batch {
				if m.Method == "initialize" {
					return m.Params, nil
				}
			}
		}
	}
	return nil, nil
}

// ResurrectSession is the load-bearing primitive of P0.7 D-5. Given a server,
// a (previously-known) session ID, and a cached state, it constructs a fresh
// StreamableServerTransport bound to that ID, opens a new ServerSession on
// the server using Server.Connect with the cached state, and returns both.
//
// The caller is responsible for serving subsequent HTTP requests via
// transport.ServeHTTP(w, req) and for closing session when done.
//
// VIABILITY of D-5 hinges on this function. If Server.Connect with a state
// containing InitializeParams + InitializedParams produces a fully-functional
// session that accepts tool calls without a re-initialize handshake, the
// custom-handler approach is sound. The companion test exercises exactly
// this path.
func ResurrectSession(
	ctx context.Context,
	server *mcp.Server,
	sessionID string,
	cached *CachedSessionState,
) (*mcp.ServerSession, *mcp.StreamableServerTransport, error) {
	if server == nil {
		return nil, nil, fmt.Errorf("nil server")
	}
	if sessionID == "" {
		return nil, nil, fmt.Errorf("empty sessionID")
	}
	if cached == nil || cached.InitializeParams == nil {
		return nil, nil, fmt.Errorf("nil cached state or InitializeParams")
	}

	transport := &mcp.StreamableServerTransport{
		SessionID: sessionID,
	}

	state := &mcp.ServerSessionState{
		InitializeParams:  cached.InitializeParams,
		InitializedParams: cached.InitializedParams,
		LogLevel:          cached.LogLevel,
	}
	if state.InitializedParams == nil {
		// SDK treats nil InitializedParams as "not yet initialized" which makes
		// the session reject non-initialize requests. Default to empty to mark
		// the session as ready for normal traffic.
		state.InitializedParams = &mcp.InitializedParams{}
	}

	session, err := server.Connect(ctx, transport, &mcp.ServerSessionOptions{
		State: state,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("server.Connect: %w", err)
	}
	return session, transport, nil
}

// --- small local helpers (kept here to keep the spike self-contained) ---

type bytesReader struct {
	b   []byte
	pos int
}

func newBytesReader(b []byte) *bytesReader { return &bytesReader{b: b} }

func (r *bytesReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.pos:])
	r.pos += n
	return n, nil
}

func skipWhitespace(b []byte) []byte {
	for i, c := range b {
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			return b[i:]
		}
	}
	return nil
}
