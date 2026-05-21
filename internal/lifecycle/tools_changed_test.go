package lifecycle

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"mcp-gateway/internal/logbuf"
	"mcp-gateway/internal/models"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// noInput is a zero-field struct used as the input type for test tools so
// mcp.AddTool can infer a valid {"type":"object"} schema without extra setup.
type noInput struct{}

// startInProcessMCPSessionWithTool creates an in-process MCP server/client pair
// with one pre-registered tool. Returns the session and a function to add more
// tools to the server (to simulate tools/list_changed).
func startInProcessMCPSessionWithTool(ctx context.Context, t *testing.T) (*mcp.ClientSession, func(name string)) {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "test-srv-tools", Version: "1"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "t1", Description: "initial tool"},
		func(_ context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, noInput, error) {
			return nil, noInput{}, nil
		},
	)
	cliTrans, srvTrans := mcp.NewInMemoryTransports()
	go func() { _ = srv.Run(ctx, srvTrans) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-cli-tools", Version: "1"}, nil)
	sess, err := client.Connect(ctx, cliTrans, nil)
	require.NoError(t, err)
	addTool := func(name string) {
		mcp.AddTool(srv, &mcp.Tool{Name: name, Description: "added tool"},
			func(_ context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, noInput, error) {
				return nil, noInput{}, nil
			},
		)
	}
	return sess, addTool
}

// newMinimalManager builds a Manager with a single pre-populated entry and
// a live session injected directly (bypassing connect / subprocess machinery).
// Used by F1 unit tests so we exercise handleToolsChanged without spawning
// real processes.
func newMinimalManager(t *testing.T, name string, sess *mcp.ClientSession) *Manager {
	t.Helper()
	m := &Manager{
		entries: map[string]*entry{
			name: {
				ServerEntry: models.ServerEntry{
					Name:   name,
					Status: models.StatusRunning,
				},
				session: sess,
				logs:    logbuf.New(logbuf.DefaultCapacity),
			},
		},
		impl:   &mcp.Implementation{Name: "mcp-gateway", Version: "test"},
		logger: slog.Default(),
	}
	return m
}

// TestManager_ToolListChangedHandler_RefreshesEntryAndFiresCallback verifies
// the F1 fix: after a backend signals tools/list_changed, handleToolsChanged
// re-fetches the tool list from the live session, updates entry.Tools, and
// invokes the registered callback with the correct backend name.
//
// The test does NOT send a real notification over the wire — it calls
// handleToolsChanged directly. This is equivalent because connect() registers
// exactly that call as the ToolListChangedHandler; the SDK guarantees it is
// invoked for every notification/tools/list_changed received.
func TestManager_ToolListChangedHandler_RefreshesEntryAndFiresCallback(t *testing.T) {
	const backendName = "test-backend"

	srvCtx := t.Context()

	sess, addTool := startInProcessMCPSessionWithTool(srvCtx, t)
	m := newMinimalManager(t, backendName, sess)

	// Add a second tool on the server side BEFORE calling handleToolsChanged
	// so the re-fetch returns 2 tools instead of 1.
	addTool("t2")

	// Record callback invocations via a buffered channel to avoid blocking.
	callbackFired := make(chan string, 1)
	m.SetToolsChangedCallback(func(name string) {
		callbackFired <- name
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	m.handleToolsChanged(ctx, backendName)

	// Verify entry.Tools was refreshed.
	e, ok := m.Entry(backendName)
	require.True(t, ok)
	assert.Len(t, e.Tools, 2, "entry.Tools must reflect both t1 and t2 after handleToolsChanged")
	names := make([]string, len(e.Tools))
	for i, tool := range e.Tools {
		names[i] = tool.Name
	}
	assert.ElementsMatch(t, []string{"t1", "t2"}, names)

	// Verify callback fired with the correct backend name.
	select {
	case fired := <-callbackFired:
		assert.Equal(t, backendName, fired, "callback must fire with backend name")
	case <-ctx.Done():
		t.Fatal("callback was not called within timeout")
	}
}

// TestManager_ToolListChangedHandler_NilCallbackSafe verifies that
// handleToolsChanged does not panic when no callback is registered
// (nil-safe path — toolsChangedCb was never set).
func TestManager_ToolListChangedHandler_NilCallbackSafe(t *testing.T) {
	const backendName = "no-cb-backend"

	srvCtx := t.Context()

	sess, _ := startInProcessMCPSessionWithTool(srvCtx, t)
	m := newMinimalManager(t, backendName, sess)
	// Intentionally do NOT call SetToolsChangedCallback.

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Must not panic.
	assert.NotPanics(t, func() {
		m.handleToolsChanged(ctx, backendName)
	})

	// entry.Tools should still be refreshed (t1 was registered on the server).
	e, ok := m.Entry(backendName)
	require.True(t, ok)
	assert.Len(t, e.Tools, 1)
}

// TestManager_ToolListChangedHandler_SessionGone verifies that
// handleToolsChanged returns early without error when the session
// has been removed (race between notification arrival and Stop).
func TestManager_ToolListChangedHandler_SessionGone(t *testing.T) {
	const backendName = "gone-backend"

	m := &Manager{
		entries: map[string]*entry{
			backendName: {
				ServerEntry: models.ServerEntry{
					Name:   backendName,
					Status: models.StatusStopped,
				},
				session: nil, // session already cleared by Stop()
				logs:    logbuf.New(logbuf.DefaultCapacity),
			},
		},
		impl:   &mcp.Implementation{Name: "mcp-gateway", Version: "test"},
		logger: slog.Default(),
	}

	callbackFired := make(chan struct{}, 1)
	m.SetToolsChangedCallback(func(_ string) { callbackFired <- struct{}{} })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Must not panic; callback must NOT fire (no session, nothing to fetch).
	assert.NotPanics(t, func() {
		m.handleToolsChanged(ctx, backendName)
	})

	select {
	case <-callbackFired:
		t.Fatal("callback must not fire when session is gone")
	default:
		// expected: no callback fired
	}
}
