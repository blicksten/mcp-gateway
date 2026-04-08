package router

import (
	"context"
	"testing"

	"mcp-gateway/internal/models"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockSP implements SessionProvider for testing.
type mockSP struct {
	entries  map[string]models.ServerEntry
	sessions map[string]*mcp.ClientSession
}

func (m *mockSP) Session(name string) (*mcp.ClientSession, bool) {
	s, ok := m.sessions[name]
	return s, ok
}

func (m *mockSP) Entry(name string) (models.ServerEntry, bool) {
	e, ok := m.entries[name]
	return e, ok
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
