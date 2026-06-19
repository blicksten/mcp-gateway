package api

// Tests for TASK A: idle-exit monitor wiring in server.go.
//
// Coverage:
//  1. sessionCountAdapter.ClientCount() returns 0 when h is nil (nil-safe guard).
//  2. sessionCountAdapter.ClientCount() tracks ResumableStreamableHTTPHandler.SessionCount().
//  3. After Handler() is called on a Server, s.streamableHandler is non-nil and
//     the adapter reflects SessionCount correctly (ordering proof).
//  4. The adapter satisfies the proxy.ClientCounter interface at compile time
//     (static assertion — no runtime test needed beyond compilation).

import (
	"net/http"
	"testing"

	"mcp-gateway/internal/proxy"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// staticClientCounterCheck is a compile-time assertion: sessionCountAdapter must
// implement proxy.ClientCounter.
var _ proxy.ClientCounter = sessionCountAdapter{}

// TestSessionCountAdapter_NilHandler verifies that ClientCount returns 0 when
// the adapter holds a nil handler (nil-safe guard in sessionCountAdapter).
func TestSessionCountAdapter_NilHandler(t *testing.T) {
	a := sessionCountAdapter{h: nil}
	assert.Equal(t, 0, a.ClientCount(), "nil handler should report 0 clients")
}

// TestSessionCountAdapter_TracksSessionCount verifies that ClientCount reflects
// the underlying ResumableStreamableHTTPHandler.SessionCount().
//
// We directly manipulate h.sessions (same package) rather than going through
// registerEntry (which requires a live *mcp.ServerSession from the SDK).
func TestSessionCountAdapter_TracksSessionCount(t *testing.T) {
	registry := NewSessionStateRegistry()
	h := NewResumableStreamableHTTPHandler(
		func(_ *http.Request) *mcp.Server { return nil },
		registry,
	)

	a := sessionCountAdapter{h: h}

	// Initially zero sessions.
	assert.Equal(t, 0, a.ClientCount(), "expect 0 clients initially")

	// Add sessions directly via the internal map (same package).
	h.mu.Lock()
	h.sessions["sess-1"] = &resumableSessionEntry{}
	h.mu.Unlock()
	assert.Equal(t, 1, a.ClientCount(), "expect 1 client after inserting sess-1")

	h.mu.Lock()
	h.sessions["sess-2"] = &resumableSessionEntry{}
	h.mu.Unlock()
	assert.Equal(t, 2, a.ClientCount(), "expect 2 clients after inserting sess-2")

	h.removeSession("sess-1")
	assert.Equal(t, 1, a.ClientCount(), "expect 1 client after removing sess-1")

	h.removeSession("sess-2")
	assert.Equal(t, 0, a.ClientCount(), "expect 0 clients after all sessions removed")
}

// TestServer_HandlerSetsStreamableHandler verifies that calling s.Handler()
// populates s.streamableHandler, proving the ordering invariant relied upon
// by ListenAndServe: ConfigureIdleExit is called AFTER s.Handler() returns
// so s.streamableHandler is always non-nil at wiring time.
func TestServer_HandlerSetsStreamableHandler(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Before Handler() is called the field must be nil.
	assert.Nil(t, srv.streamableHandler, "streamableHandler must be nil before Handler() is called")

	_ = srv.Handler()

	// After Handler() the field must be populated.
	require.NotNil(t, srv.streamableHandler, "streamableHandler must be non-nil after Handler() is called")

	// The adapter must report 0 sessions on a freshly created handler.
	a := sessionCountAdapter{srv.streamableHandler}
	assert.Equal(t, 0, a.ClientCount(), "freshly built handler has 0 sessions")
}
