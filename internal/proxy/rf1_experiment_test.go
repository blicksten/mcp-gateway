package proxy_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// RF-1 Experiment: Validate go-sdk Client + Server coexistence in one process.

func buildMockServer(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	binary := filepath.Join(tmpDir, "mock-server")
	if os.PathSeparator == '\\' {
		binary += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", binary, "mcp-gateway/internal/testutil")
	cmd.Dir = filepath.Join("..", "..") // module root — go test always runs from package dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "Failed to build mock server: %s", string(out))
	return binary
}

// T1.5(a): stdio Client → child process Server
func TestRF1a_StdioClientServer(t *testing.T) {
	binary := buildMockServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	client := mcp.NewClient(&mcp.Implementation{Name: "rf1-test", Version: "1.0"}, nil)
	transport := &mcp.CommandTransport{Command: exec.Command(binary)}

	session, err := client.Connect(ctx, transport, nil)
	require.NoError(t, err, "Client.Connect failed")
	defer session.Close()

	// ListTools
	toolsResult, err := session.ListTools(ctx, nil)
	require.NoError(t, err, "ListTools failed")
	require.NotEmpty(t, toolsResult.Tools, "Expected tools")

	toolNames := make([]string, len(toolsResult.Tools))
	for i, tool := range toolsResult.Tools {
		toolNames[i] = tool.Name
	}
	assert.Contains(t, toolNames, "echo")
	assert.Contains(t, toolNames, "add")
	t.Logf("RF-1a: stdio → %d tools: %v", len(toolsResult.Tools), toolNames)

	// CallTool
	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "echo",
		Arguments: map[string]any{"message": "hello rf1"},
	})
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.NotEmpty(t, res.Content)
	t.Logf("RF-1a: CallTool OK — content type: %T", res.Content[0])
}

// T1.5(b): Streamable HTTP handler
func TestRF1b_StreamableHTTPHandler(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "rf1-http", Version: "1.0"}, nil)

	type Empty struct{}
	mcp.AddTool(server, &mcp.Tool{Name: "ping", Description: "pong"}, func(ctx context.Context, req *mcp.CallToolRequest, _ Empty) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "pong"}},
		}, nil, nil
	})

	handler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server { return server }, nil)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := mcp.NewClient(&mcp.Implementation{Name: "rf1-http-client", Version: "1.0"}, nil)
	transport := &mcp.StreamableClientTransport{Endpoint: ts.URL}

	session, err := client.Connect(ctx, transport, nil)
	require.NoError(t, err)
	defer session.Close()

	tools, err := session.ListTools(ctx, nil)
	require.NoError(t, err)
	require.Len(t, tools.Tools, 1)
	assert.Equal(t, "ping", tools.Tools[0].Name)

	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "ping"})
	require.NoError(t, err)
	tc, ok := res.Content[0].(*mcp.TextContent)
	require.True(t, ok, "expected *mcp.TextContent, got %T", res.Content[0])
	assert.Equal(t, "pong", tc.Text)
	t.Logf("RF-1b: StreamableHTTP → ListTools + CallTool OK")
}

// T1.5(c): SSE handler
func TestRF1c_SSEHandler(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "rf1-sse", Version: "1.0"}, nil)
	type Empty2 struct{}
	mcp.AddTool(server, &mcp.Tool{Name: "sse-ping", Description: "sse-pong"}, func(ctx context.Context, req *mcp.CallToolRequest, _ Empty2) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "sse-pong"}},
		}, nil, nil
	})

	handler := mcp.NewSSEHandler(func(r *http.Request) *mcp.Server { return server }, nil)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := mcp.NewClient(&mcp.Implementation{Name: "rf1-sse-client", Version: "1.0"}, nil)
	transport := &mcp.SSEClientTransport{Endpoint: ts.URL}

	session, err := client.Connect(ctx, transport, nil)
	require.NoError(t, err)
	defer session.Close()

	tools, err := session.ListTools(ctx, nil)
	require.NoError(t, err)
	require.Len(t, tools.Tools, 1)
	assert.Equal(t, "sse-ping", tools.Tools[0].Name)
	t.Logf("RF-1c: SSE → ListTools OK")
}

// T1.5(d): AddTool dynamic registration
func TestRF1d_AddTool(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "rf1-addtool", Version: "1.0"}, nil)
	type E struct{}
	noop := func(ctx context.Context, req *mcp.CallToolRequest, _ E) (*mcp.CallToolResult, any, error) {
		return nil, nil, nil
	}

	mcp.AddTool(server, &mcp.Tool{Name: "t1", Description: "first"}, noop)
	mcp.AddTool(server, &mcp.Tool{Name: "t2", Description: "second"}, noop)
	mcp.AddTool(server, &mcp.Tool{Name: "t1", Description: "replaced"}, noop) // replace

	t.Logf("RF-1d: AddTool × 3 (2 add + 1 replace), no panic")
}

// T1.5(e): Verify Ping exists on session
func TestRF1e_Ping(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "rf1-ping", Version: "1.0"}, nil)
	handler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server { return server }, nil)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := mcp.NewClient(&mcp.Implementation{Name: "rf1-ping-client", Version: "1.0"}, nil)
	transport := &mcp.StreamableClientTransport{Endpoint: ts.URL}

	session, err := client.Connect(ctx, transport, nil)
	require.NoError(t, err)
	defer session.Close()

	err = session.Ping(ctx, nil)
	require.NoError(t, err)
	t.Logf("RF-1e: session.Ping() exists and works")
}

// T1.5(f): AddTool concurrency safety
// NOTE: Full race detection requires CGO_ENABLED=1 (go test -race).
// On Windows without CGO this test only verifies no panics.
func TestRF1f_AddToolConcurrency(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "rf1-concurrent", Version: "1.0"}, nil)
	type E3 struct{}
	done := make(chan struct{})
	for i := range 10 {
		go func() {
			defer func() { done <- struct{}{} }()
			mcp.AddTool(server,
				&mcp.Tool{Name: fmt.Sprintf("tool-%d", i), Description: "concurrent"},
				func(ctx context.Context, req *mcp.CallToolRequest, _ E3) (*mcp.CallToolResult, any, error) {
					return nil, nil, nil
				},
			)
		}()
	}
	for range 10 {
		<-done
	}
	t.Logf("RF-1f: 10 concurrent AddTool, no race/panic")
}
