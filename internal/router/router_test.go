package router

import (
	"context"
	"errors"
	"testing"

	"mcp-gateway/internal/lifecycle"
	"mcp-gateway/internal/models"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockSP implements SessionProvider for testing.
// ensureStartedFn is optional — when nil, EnsureStarted returns (StatusRunning, nil).
type mockSP struct {
	entries          map[string]models.ServerEntry
	sessions         map[string]*mcp.ClientSession
	lazyPending      map[string]bool
	ensureStartedFn  func(ctx context.Context, name string) (models.ServerStatus, error)
}

func (m *mockSP) Session(name string) (*mcp.ClientSession, bool) {
	s, ok := m.sessions[name]
	return s, ok
}

func (m *mockSP) Entry(name string) (models.ServerEntry, bool) {
	e, ok := m.entries[name]
	return e, ok
}

func (m *mockSP) EnsureStarted(ctx context.Context, name string) (models.ServerStatus, error) {
	if m.ensureStartedFn != nil {
		return m.ensureStartedFn(ctx, name)
	}
	return models.StatusRunning, nil
}

func (m *mockSP) IsLazyPending(name string) bool {
	return m.lazyPending[name]
}

func TestSplitToolName(t *testing.T) {
	r := New(&mockSP{})

	tests := []struct {
		input          string
		wantServer     string
		wantTool       string
		wantOK         bool
	}{
		{"pal__thinkdeep", "pal", "thinkdeep", true},
		{"ctx7__resolve-library-id", "ctx7", "resolve-library-id", true},
		{"server__tool__extra", "server", "tool__extra", true}, // only first split
		{"notool", "", "", false},
		{"", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			server, tool, ok := r.SplitToolName(tt.input)
			assert.Equal(t, tt.wantOK, ok)
			if ok {
				assert.Equal(t, tt.wantServer, server)
				assert.Equal(t, tt.wantTool, tool)
			}
		})
	}
}

func TestNamespacedTool(t *testing.T) {
	r := New(&mockSP{})
	assert.Equal(t, "pal__thinkdeep", r.NamespacedTool("pal", "thinkdeep"))
}

func TestCall_ServerNotFound(t *testing.T) {
	sp := &mockSP{
		entries: map[string]models.ServerEntry{},
	}
	r := New(sp)

	_, err := r.Call(context.Background(), "pal__thinkdeep", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestCall_InvalidToolName(t *testing.T) {
	r := New(&mockSP{})

	_, err := r.Call(context.Background(), "notool", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid tool name")
}

func TestCall_ServerNotRunning(t *testing.T) {
	sp := &mockSP{
		entries: map[string]models.ServerEntry{
			"pal": {Name: "pal", Status: models.StatusStopped},
		},
	}
	r := New(sp)

	_, err := r.Call(context.Background(), "pal__thinkdeep", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not running")
}

func TestCall_NoSession(t *testing.T) {
	sp := &mockSP{
		entries: map[string]models.ServerEntry{
			"pal": {Name: "pal", Status: models.StatusRunning},
		},
		sessions: map[string]*mcp.ClientSession{},
	}
	r := New(sp)

	_, err := r.Call(context.Background(), "pal__thinkdeep", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no active session")
}

func TestCall_DegradedServerAllowed(t *testing.T) {
	sp := &mockSP{
		entries: map[string]models.ServerEntry{
			"pal": {Name: "pal", Status: models.StatusDegraded},
		},
		sessions: map[string]*mcp.ClientSession{},
	}
	r := New(sp)

	// Should pass status check but fail on no session (not on status).
	_, err := r.Call(context.Background(), "pal__thinkdeep", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no active session")
}

// ----- Fix 7: flag-ON lazy-spawn router tests ---------------------------------

// TestCall_LazySpawn_IdleToRunning verifies that with the flag ON, a StatusIdle
// backend whose EnsureStarted returns StatusRunning proceeds past the spawn gate
// to the session lookup. Reaching "no active session" proves the gate was crossed.
func TestCall_LazySpawn_IdleToRunning(t *testing.T) {
	t.Setenv("MCP_GATEWAY_LAZY_SPAWN", "1")
	require.True(t, lifecycle.LazySpawnEnabled(), "precondition: flag must be ON")

	sp := &mockSP{
		entries: map[string]models.ServerEntry{
			"vsp": {Name: "vsp", Status: models.StatusIdle},
		},
		sessions: map[string]*mcp.ClientSession{},
		ensureStartedFn: func(_ context.Context, name string) (models.ServerStatus, error) {
			return models.StatusRunning, nil
		},
	}
	r := New(sp)

	_, err := r.Call(context.Background(), "vsp__sap_ping", nil)
	require.Error(t, err)
	// Must reach session lookup — not the spawn-gate rejection.
	assert.Contains(t, err.Error(), "no active session",
		"idle→running path must reach session lookup, not be rejected at spawn gate")
}

// TestCall_LazySpawn_ErrLazyWarming verifies that when EnsureStarted returns
// ErrLazyWarming, Call propagates an error wrapping the warming message.
func TestCall_LazySpawn_ErrLazyWarming(t *testing.T) {
	t.Setenv("MCP_GATEWAY_LAZY_SPAWN", "1")

	sp := &mockSP{
		entries: map[string]models.ServerEntry{
			"vsp": {Name: "vsp", Status: models.StatusIdle},
		},
		ensureStartedFn: func(_ context.Context, name string) (models.ServerStatus, error) {
			return models.StatusIdle, lifecycle.ErrLazyWarming
		},
	}
	r := New(sp)

	_, err := r.Call(context.Background(), "vsp__sap_ping", nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, lifecycle.ErrLazyWarming,
		"ErrLazyWarming must be preserved via wrapping so callers can inspect it")
}

// TestCall_LazySpawn_StatusErrorAfterSpawn verifies that when EnsureStarted
// returns a non-Running/non-Degraded status with no error (e.g. StatusError,nil),
// Call returns the "not running after spawn" error.
func TestCall_LazySpawn_StatusErrorAfterSpawn(t *testing.T) {
	t.Setenv("MCP_GATEWAY_LAZY_SPAWN", "1")

	sp := &mockSP{
		entries: map[string]models.ServerEntry{
			"vsp": {Name: "vsp", Status: models.StatusIdle},
		},
		ensureStartedFn: func(_ context.Context, name string) (models.ServerStatus, error) {
			// Return StatusError with nil error — exercises the post-spawn status check.
			return models.StatusError, nil
		},
	}
	r := New(sp)

	_, err := r.Call(context.Background(), "vsp__sap_ping", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not running after spawn",
		"non-Running status with nil error must produce the 'not running after spawn' message")
}

// TestCall_LazySpawn_EnsureStartedErrorWrapped verifies that when EnsureStarted
// returns a real spawn error (non-nil), Call wraps it with the server name.
func TestCall_LazySpawn_EnsureStartedErrorWrapped(t *testing.T) {
	t.Setenv("MCP_GATEWAY_LAZY_SPAWN", "1")

	spawnErr := errors.New("binary not found")
	sp := &mockSP{
		entries: map[string]models.ServerEntry{
			"vsp": {Name: "vsp", Status: models.StatusIdle},
		},
		ensureStartedFn: func(_ context.Context, name string) (models.ServerStatus, error) {
			return models.StatusError, spawnErr
		},
	}
	r := New(sp)

	_, err := r.Call(context.Background(), "vsp__sap_ping", nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, spawnErr, "spawn error must be reachable via errors.Is")
	assert.Contains(t, err.Error(), "vsp", "error must include the server name")
}

// TestCall_LazySpawn_IsLazyPendingTriggersEnsureStarted verifies that a backend
// with IsLazyPending==true (StatusStarting, not Idle) also routes into EnsureStarted.
// This covers the race window where the backend is between Idle and Running.
func TestCall_LazySpawn_IsLazyPendingTriggersEnsureStarted(t *testing.T) {
	t.Setenv("MCP_GATEWAY_LAZY_SPAWN", "1")

	ensureStartedCalled := false
	sp := &mockSP{
		entries: map[string]models.ServerEntry{
			// StatusStarting — not Idle, but IsLazyPending is true.
			"vsp": {Name: "vsp", Status: models.StatusStarting},
		},
		lazyPending: map[string]bool{"vsp": true},
		sessions:    map[string]*mcp.ClientSession{},
		ensureStartedFn: func(_ context.Context, name string) (models.ServerStatus, error) {
			ensureStartedCalled = true
			return models.StatusRunning, nil
		},
	}
	r := New(sp)

	_, err := r.Call(context.Background(), "vsp__sap_ping", nil)
	require.Error(t, err)
	// Must reach session lookup — not a "not running" rejection.
	assert.True(t, ensureStartedCalled, "IsLazyPending==true must route into EnsureStarted")
	assert.Contains(t, err.Error(), "no active session",
		"after spawn gate, call must proceed to session lookup")
}
