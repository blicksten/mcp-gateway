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
// HISTORY: this began as a D-5 viability spike proving the resurrection
// primitive in isolation (given a server, a sessionID, and cached
// InitializeParams, construct a working ServerSession using ONLY public SDK
// API). The primitive proved out and was PROMOTED TO PRODUCTION.
//
// STATUS (2026-06): WIRED INTO PRODUCTION — not a spike anymore. The types
// here (SessionStateRegistry, CaptureInitializeFromRequest, ResurrectSession)
// back the live handler: api.Server constructs sessionRegistry in NewServer
// and passes it to NewResumableStreamableHTTPHandler (resumable_streamable.go),
// which is mounted at /mcp in Server.Handler(). Do NOT delete this file as
// "dead spike code" — removing it breaks the /mcp session-resurrection path.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// DefaultSessionRegistryDiskPath returns the cross-platform default on-disk
// path for persisted session state — ~/.mcp-gateway/sessions.json. Returns
// "" if the home directory cannot be resolved (e.g. no HOME/USERPROFILE),
// which makes the registry behave as in-memory-only — the same fall-back
// posture as before this fix.
//
// 2026-05-23 closure of the T0.7.1 disk-persistence gap: the spike header
// said "the full D-5 implementation will add file-backed persistence at
// ~/.mcp-gateway/sessions.json" — that work was planned but never landed.
// The in-memory-only registry meant T0.7.1 silently degraded to a TCP-blip-
// only fix; the most common FM-3 trigger (daemon restart) was uncovered.
// This function and NewSessionStateRegistryWithPath close that gap.
//
// Honest scope: recovers POST tool-call path after restart (T0.7.1 stated
// goal) — does NOT recover the SSE GET notification stream, which dies
// independently when the closed-source Claude Code MCP client gives up
// after exponential backoff (FM-32 upstream issue #57642, closed as stale).
// Notification stream recovery is handled separately by the porfiry-mcp.js
// webview patch's reconnect-action flow (Phase MCPR.4, 2026-05-08).
func DefaultSessionRegistryDiskPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".mcp-gateway", "sessions.json")
}

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

// SessionStateRegistry is a thread-safe cache of session states keyed by
// Mcp-Session-Id. When constructed with a non-empty diskPath, every Put
// and Delete is mirrored to disk via atomic rename so the cache survives
// daemon restart (T0.7.1 disk-persistence — 2026-05-23). When diskPath
// is empty (NewSessionStateRegistry()), behaviour is in-memory-only and
// matches the pre-2026-05-23 posture.
type SessionStateRegistry struct {
	mu       sync.Mutex
	states   map[string]*CachedSessionState
	diskPath string // "" disables persistence
}

// NewSessionStateRegistry constructs an in-memory-only registry. Kept for
// backward compatibility with existing tests that don't exercise the
// restart-recovery path; production code should use
// NewSessionStateRegistryWithPath(DefaultSessionRegistryDiskPath()).
func NewSessionStateRegistry() *SessionStateRegistry {
	return &SessionStateRegistry{states: make(map[string]*CachedSessionState)}
}

// NewSessionStateRegistryWithPath constructs a registry backed by an
// on-disk JSON file. On startup, any existing file is loaded; subsequent
// Put/Delete mutations are atomically written back via temp-file rename.
//
// Returns an empty registry (no error) when the file does not exist —
// that is the cold-start case. Corrupted JSON is logged-and-skipped, with
// the registry continuing empty so the daemon does not refuse to start
// over a malformed cache.
//
// When diskPath is "" or the parent directory cannot be created, the
// registry falls back to in-memory-only behaviour and is functionally
// indistinguishable from NewSessionStateRegistry. This keeps the
// "best-effort, never block startup" contract used elsewhere in the
// gateway.
func NewSessionStateRegistryWithPath(diskPath string) *SessionStateRegistry {
	r := &SessionStateRegistry{
		states:   make(map[string]*CachedSessionState),
		diskPath: diskPath,
	}
	if diskPath == "" {
		return r
	}
	// Best-effort: create parent dir but don't fail registry construction
	// if mkdir errors (most likely a permissions surprise on Windows; the
	// gateway should still start with in-memory cache).
	if err := os.MkdirAll(filepath.Dir(diskPath), 0o700); err != nil {
		r.diskPath = "" // fall back to in-memory
		return r
	}
	if err := r.loadFromDisk(); err != nil && !errors.Is(err, os.ErrNotExist) {
		// Corrupted file: log via panic-recover-style noop (no logger
		// dependency here to keep this package import-light). The cache
		// starts empty; the daemon proceeds. A truncated file is no worse
		// than a missing one — both manifest as no-resurrection on next
		// restart.
		r.states = make(map[string]*CachedSessionState)
	}
	return r
}

// loadFromDisk reads the persisted JSON into r.states. Caller must hold
// r.mu OR be calling from the constructor (single-goroutine). Returns
// os.ErrNotExist when the file does not exist (cold start, not an error).
func (r *SessionStateRegistry) loadFromDisk() error {
	data, err := os.ReadFile(r.diskPath)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	var loaded map[string]*CachedSessionState
	if err := json.Unmarshal(data, &loaded); err != nil {
		return fmt.Errorf("unmarshal %s: %w", r.diskPath, err)
	}
	if loaded == nil {
		return nil
	}
	r.states = loaded
	return nil
}

// flushToDiskLocked atomically writes the current registry state to
// r.diskPath via a temp file in the same directory + rename. No-op when
// diskPath is "". Caller MUST hold r.mu.
//
// Errors are intentionally swallowed (logged-only would be the right
// shape with a logger; this file is logger-free by design). The next
// successful flush overwrites; a single failed flush merely loses one
// generation of disk durability while in-memory state remains correct.
// This matches the "best effort persistence" contract per the function
// doc on NewSessionStateRegistryWithPath.
func (r *SessionStateRegistry) flushToDiskLocked() {
	if r.diskPath == "" {
		return
	}
	data, err := json.MarshalIndent(r.states, "", "  ")
	if err != nil {
		return
	}
	dir := filepath.Dir(r.diskPath)
	// Same-directory temp guarantees os.Rename is atomic (within one
	// filesystem). CreateTemp creates with mode 0600 by default which
	// matches our auth-token convention.
	tmp, err := os.CreateTemp(dir, "sessions-*.json.tmp")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return
	}
	if err := os.Rename(tmpName, r.diskPath); err != nil {
		// Windows rename may fail across-volume; on same dir it is safe.
		// Best-effort cleanup of the temp file.
		_ = os.Remove(tmpName)
		return
	}
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

// Put records or replaces the cached state for a session ID. When
// disk-backed, the new state is atomically flushed before Put returns —
// callers can trust that a successful daemon shutdown after a Put
// preserves the session for next-startup resurrection.
func (r *SessionStateRegistry) Put(id string, s *CachedSessionState) {
	if s == nil {
		return
	}
	s.LastSeen = time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.states[id] = s
	r.flushToDiskLocked()
}

// Delete removes the cached state for a session ID (e.g. on DELETE /mcp).
// When disk-backed, the deletion is atomically flushed before Delete
// returns.
func (r *SessionStateRegistry) Delete(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.states, id)
	r.flushToDiskLocked()
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
