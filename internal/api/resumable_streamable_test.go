package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- test helpers -----------------------------------------------------------

func newTestServer(t *testing.T) *mcp.Server {
	t.Helper()
	s := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	mcp.AddTool(s,
		&mcp.Tool{Name: "ping", Description: "ping tool"},
		func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, struct{}, error) {
			return nil, struct{}{}, nil
		},
	)
	return s
}

func testGetServer(s *mcp.Server) func(*http.Request) *mcp.Server {
	return func(*http.Request) *mcp.Server { return s }
}

func initBody() []byte {
	return []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","clientInfo":{"name":"test-client","version":"0.0.1"},"capabilities":{}}}`)
}

func toolsListBody() []byte {
	return []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
}

// postMCP issues a POST to a handler with the standard MCP headers.
func postMCP(t *testing.T, handler http.Handler, path, sessionID string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", "2025-06-18")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(rec, req)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler timed out")
	}
	return rec
}

// sessionIDFrom extracts the Mcp-Session-Id from a response header.
func sessionIDFrom(rec *httptest.ResponseRecorder) string {
	return rec.Header().Get("Mcp-Session-Id")
}

// --- Test 1: Known-session happy path ----------------------------------------

// TestResumable_KnownSession verifies that an initialized session can be
// re-used for subsequent tool calls within the same handler lifetime.
func TestResumable_KnownSession(t *testing.T) {
	server := newTestServer(t)
	registry := NewSessionStateRegistry()
	h := NewResumableStreamableHTTPHandler(testGetServer(server), registry)
	h.DisableLocalhostProtection = true

	// First request: initialize (no session ID in request).
	rec := postMCP(t, h, "/mcp", "", initBody())
	require.Equal(t, http.StatusOK, rec.Code, "initialize must succeed; body=%s", rec.Body)
	sid := sessionIDFrom(rec)
	require.NotEmpty(t, sid, "handler must assign a session ID on initialize")

	// Second request: tools/list on the same session ID.
	rec2 := postMCP(t, h, "/mcp", sid, toolsListBody())
	require.Equal(t, http.StatusOK, rec2.Code, "tools/list must succeed on known session; body=%s", rec2.Body)
	assert.True(t,
		strings.Contains(rec2.Body.String(), "ping"),
		"tools/list response must mention the registered tool; got %s", rec2.Body)
}

// --- Test 2: Resurrection from cached state ----------------------------------

// TestResumable_ResurrectionFromCache is the load-bearing real-boundary test
// for P0.7 D-5.  It simulates a daemon restart by creating a new handler
// (with the same registry) and verifying that a tools/list call with the
// original session ID succeeds without re-initialize.
//
// Real boundary: real HTTP over httptest.NewServer, real mcp.Server.Connect.
func TestResumable_ResurrectionFromCache(t *testing.T) {
	server := newTestServer(t)
	registry := NewSessionStateRegistry()

	const sessionID = "test-resurrect-session-abc123"

	// Pre-populate the registry as if a daemon restart just wiped the
	// in-memory sessions map.
	registry.Put(sessionID, &CachedSessionState{
		InitializeParams: &mcp.InitializeParams{
			ProtocolVersion: "2025-06-18",
			ClientInfo:      &mcp.Implementation{Name: "test-client", Version: "0.0.1"},
			Capabilities:    &mcp.ClientCapabilities{},
		},
		InitializedParams: &mcp.InitializedParams{},
		LogLevel:          "info",
	})

	// New handler with the same registry — simulates daemon restart.
	h := NewResumableStreamableHTTPHandler(testGetServer(server), registry)
	h.DisableLocalhostProtection = true

	// tools/list without re-initialize.
	rec := postMCP(t, h, "/mcp", sessionID, toolsListBody())
	require.Equal(t, http.StatusOK, rec.Code,
		"resurrected session must accept tools/list without re-init; body=%s", rec.Body)
	assert.True(t,
		strings.Contains(rec.Body.String(), "ping"),
		"tools/list response must mention the registered tool; got %s", rec.Body)
}

// --- Test 3: Unknown session with no cached state → 404 ----------------------

func TestResumable_UnknownSession_NoCache_Returns404(t *testing.T) {
	server := newTestServer(t)
	registry := NewSessionStateRegistry()
	h := NewResumableStreamableHTTPHandler(testGetServer(server), registry)
	h.DisableLocalhostProtection = true

	rec := postMCP(t, h, "/mcp", "completely-unknown-session-xyz", toolsListBody())
	assert.Equal(t, http.StatusNotFound, rec.Code,
		"unknown session with no cached state must return 404")
	assert.Contains(t, rec.Body.String(), "session not found")
}

// --- Test 4: DELETE removes from sessions and registry -----------------------

func TestResumable_DELETE_RemovesSessionAndRegistry(t *testing.T) {
	server := newTestServer(t)
	registry := NewSessionStateRegistry()
	h := NewResumableStreamableHTTPHandler(testGetServer(server), registry)
	h.DisableLocalhostProtection = true

	// Seed registry directly.
	const sid = "delete-test-session"
	registry.Put(sid, &CachedSessionState{
		InitializeParams: &mcp.InitializeParams{
			ProtocolVersion: "2025-06-18",
			ClientInfo:      &mcp.Implementation{Name: "c", Version: "0"},
			Capabilities:    &mcp.ClientCapabilities{},
		},
		InitializedParams: &mcp.InitializedParams{},
	})

	// Resurrect it first (so it lives in the sessions map too).
	toolsRec := postMCP(t, h, "/mcp", sid, toolsListBody())
	require.Equal(t, http.StatusOK, toolsRec.Code, "resurrection must succeed before DELETE")
	assert.Equal(t, 1, h.SessionCount(), "session must be in map after resurrection")

	// DELETE.
	req := httptest.NewRequest(http.MethodDelete, "/mcp", nil)
	req.Header.Set("Mcp-Session-Id", sid)
	req.Header.Set("Accept", "application/json, text/event-stream")
	delRec := httptest.NewRecorder()
	h.ServeHTTP(delRec, req)
	assert.Equal(t, http.StatusNoContent, delRec.Code, "DELETE must return 204")

	// After DELETE: both maps must be empty.
	assert.Equal(t, 0, h.SessionCount(), "session must be removed from map after DELETE")
	assert.Nil(t, registry.Get(sid), "registry must be cleared after DELETE")

	// Subsequent tool call must return 404.
	rec2 := postMCP(t, h, "/mcp", sid, toolsListBody())
	assert.Equal(t, http.StatusNotFound, rec2.Code, "must 404 after DELETE")
}

// --- Test 5: Session hijack guard --------------------------------------------

// TestResumable_HijackGuard verifies that a session created with userID "user-A"
// rejects requests carrying a different userID "user-B".
func TestResumable_HijackGuard(t *testing.T) {
	server := newTestServer(t)
	registry := NewSessionStateRegistry()

	const sid = "hijack-guard-session"
	registry.Put(sid, &CachedSessionState{
		InitializeParams: &mcp.InitializeParams{
			ProtocolVersion: "2025-06-18",
			ClientInfo:      &mcp.Implementation{Name: "c", Version: "0"},
			Capabilities:    &mcp.ClientCapabilities{},
		},
		InitializedParams: &mcp.InitializedParams{},
		UserID:            "Bearer token-user-A",
	})

	h := NewResumableStreamableHTTPHandler(testGetServer(server), registry)
	h.DisableLocalhostProtection = true

	// Request carrying a different Authorization header.
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(toolsListBody()))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", "2025-06-18")
	req.Header.Set("Mcp-Session-Id", sid)
	req.Header.Set("Authorization", "Bearer token-user-B") // different user
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { h.ServeHTTP(rec, req); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler timed out")
	}
	assert.Equal(t, http.StatusForbidden, rec.Code, "mismatched userID must return 403")
}

// --- Test 6: Protocol-version negotiation parity with SDK --------------------

func TestResumable_ProtocolVersionNegotiation(t *testing.T) {
	server := newTestServer(t)
	registry := NewSessionStateRegistry()
	h := NewResumableStreamableHTTPHandler(testGetServer(server), registry)
	h.DisableLocalhostProtection = true

	unsupported := "1999-01-01"
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(initBody()))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", unsupported)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code,
		"unsupported protocol version must return 400")
	assert.Contains(t, rec.Body.String(), "Unsupported protocol version")

	// Supported version must succeed.
	rec2 := postMCP(t, h, "/mcp", "", initBody())
	assert.Equal(t, http.StatusOK, rec2.Code, "supported protocol version must succeed")
}

// --- Test 7: Handler-restart with shared registry simulates daemon restart ---

// TestResumable_HandlerRestartSharedRegistry is the real-boundary daemon-restart
// simulation.  It:
//  1. Creates handler H1, initializes a session, records the session ID.
//  2. Creates handler H2 with the SAME registry (H1 has no knowledge of H2).
//  3. Issues a tools/list POST to H2 with the original session ID.
//  4. Asserts the request succeeds WITHOUT re-initialize.
//
// This is the real-boundary test for §2.6 R5 (client-visible transport
// survives a daemon restart) and for the P0.7 GATE acceptance criteria.
func TestResumable_HandlerRestartSharedRegistry(t *testing.T) {
	server := newTestServer(t)
	registry := NewSessionStateRegistry()

	// Handler 1 — the "pre-restart" handler.
	h1 := NewResumableStreamableHTTPHandler(testGetServer(server), registry)
	h1.DisableLocalhostProtection = true

	// Step 1: initialize via H1.
	initRec := postMCP(t, h1, "/mcp", "", initBody())
	require.Equal(t, http.StatusOK, initRec.Code, "H1 initialize must succeed; body=%s", initRec.Body)
	sid := sessionIDFrom(initRec)
	require.NotEmpty(t, sid, "session ID must be set")

	// Step 2: make a tools/list call via H1 to ensure the registry is populated
	// (createSession only seeds the registry on initialize).
	toolsRec1 := postMCP(t, h1, "/mcp", sid, toolsListBody())
	require.Equal(t, http.StatusOK, toolsRec1.Code, "H1 tools/list must succeed")

	// Step 3: "daemon restart" — create H2, H1 is abandoned.
	h2 := NewResumableStreamableHTTPHandler(testGetServer(server), registry)
	h2.DisableLocalhostProtection = true
	assert.Equal(t, 0, h2.SessionCount(), "H2 starts with empty session map")

	// Step 4: tools/list on H2 with the original session ID, without re-init.
	toolsRec2 := postMCP(t, h2, "/mcp", sid, toolsListBody())
	require.Equal(t, http.StatusOK, toolsRec2.Code,
		"resurrected session on H2 must accept tools/list without re-init; body=%s", toolsRec2.Body)
	assert.True(t,
		strings.Contains(toolsRec2.Body.String(), "ping"),
		"tools/list response must list the tool; got %s", toolsRec2.Body)
	assert.Equal(t, 1, h2.SessionCount(), "H2 must have the resurrected session in its map")
}

// --- Test 8: Content-Type and Accept validation ------------------------------

func TestResumable_ValidationErrors(t *testing.T) {
	server := newTestServer(t)
	registry := NewSessionStateRegistry()
	h := NewResumableStreamableHTTPHandler(testGetServer(server), registry)
	h.DisableLocalhostProtection = true

	// Wrong Content-Type on POST.
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(initBody()))
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", "2025-06-18")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnsupportedMediaType, rec.Code,
		"wrong Content-Type must return 415")

	// Missing Accept on POST.
	req2 := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(initBody()))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Accept", "application/json") // missing text/event-stream
	req2.Header.Set("Mcp-Protocol-Version", "2025-06-18")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	assert.Equal(t, http.StatusBadRequest, rec2.Code,
		"missing text/event-stream Accept must return 400")

	// Unknown HTTP method.
	req3 := httptest.NewRequest(http.MethodPatch, "/mcp", nil)
	req3.Header.Set("Accept", "application/json, text/event-stream")
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, req3)
	assert.Equal(t, http.StatusMethodNotAllowed, rec3.Code,
		"PATCH must return 405")
}

// --- Test 9: SessionTimeout eviction -----------------------------------------

func TestResumable_SessionTimeout(t *testing.T) {
	server := newTestServer(t)
	registry := NewSessionStateRegistry()

	const sid = "timeout-test-session"
	registry.Put(sid, &CachedSessionState{
		InitializeParams: &mcp.InitializeParams{
			ProtocolVersion: "2025-06-18",
			ClientInfo:      &mcp.Implementation{Name: "c", Version: "0"},
			Capabilities:    &mcp.ClientCapabilities{},
		},
		InitializedParams: &mcp.InitializedParams{},
	})

	h := NewResumableStreamableHTTPHandler(testGetServer(server), registry)
	h.DisableLocalhostProtection = true
	h.SessionTimeout = 50 * time.Millisecond

	// Resurrect session (starts the timer).
	rec := postMCP(t, h, "/mcp", sid, toolsListBody())
	require.Equal(t, http.StatusOK, rec.Code, "resurrection must succeed")
	assert.Equal(t, 1, h.SessionCount())
	assert.NotNil(t, registry.Get(sid), "registry must still hold the cached state pre-eviction")

	// Poll for eviction (avoids fixed-sleep flake under load).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if h.SessionCount() == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	assert.Equal(t, 0, h.SessionCount(), "session must be evicted after timeout")
	// CV HIGH-1: eviction must also drop the cached state — otherwise the
	// next incoming request would silently re-resurrect the session and
	// nullify the idle timeout.
	assert.Nil(t, registry.Get(sid), "registry entry must also be deleted on idle eviction")
}

