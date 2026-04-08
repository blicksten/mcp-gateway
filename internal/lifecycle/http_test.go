package lifecycle

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"mcp-gateway/internal/models"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startHTTPMockServer builds the mock server binary, spawns it with the given
// transport ("http" or "sse"), and waits for the "READY port=<N>" signal.
// Extra args are passed to the mock server binary (e.g., "--auth-token=secret").
// Registers cleanup via t.Cleanup to ensure the process is always killed.
func startHTTPMockServer(t *testing.T, transport string, extraArgs ...string) string {
	t.Helper()

	binary := buildMockServer(t)
	args := append([]string{"--transport=" + transport, "--port=0"}, extraArgs...)
	cmd := exec.Command(binary, args...)
	cmd.Stderr = os.Stderr

	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)

	require.NoError(t, cmd.Start())

	// Ensure cleanup runs even if the test panics.
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = stdout.Close()
		_ = cmd.Wait()
	})

	// Read the readiness signal with a timeout.
	portCh := make(chan int, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if portStr, ok := strings.CutPrefix(scanner.Text(), "READY port="); ok {
				if p, err := strconv.Atoi(portStr); err == nil {
					portCh <- p
					return
				}
			}
		}
		// Drain remaining output so cmd.Wait() doesn't block.
		_, _ = io.Copy(io.Discard, stdout)
	}()

	var port int
	select {
	case port = <-portCh:
	case <-time.After(30 * time.Second):
		t.Fatal("mock server did not print READY within 30s")
	}

	suffix := "/mcp"
	if transport == "sse" {
		suffix = "/sse"
	}
	url := fmt.Sprintf("http://127.0.0.1:%d%s", port, suffix)

	t.Logf("Mock %s server ready at %s (PID=%d)", transport, url, cmd.Process.Pid)
	return url
}

func TestStart_HTTPBackend(t *testing.T) {
	url := startHTTPMockServer(t, "http")

	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"http-backend": {URL: url},
		},
	}
	cfg.ApplyDefaults()

	m := NewManager(cfg, "test", testLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := m.Start(ctx, "http-backend")
	require.NoError(t, err)

	e, ok := m.Entry("http-backend")
	require.True(t, ok)
	assert.Equal(t, models.StatusRunning, e.Status)
	assert.Equal(t, "http", e.Config.TransportType())
	assert.Equal(t, 0, e.PID, "HTTP backends have no PID")
	assert.NotEmpty(t, e.Tools, "expected tools from HTTP mock server")

	// Verify tool names.
	toolNames := make([]string, len(e.Tools))
	for i, tool := range e.Tools {
		toolNames[i] = tool.Name
	}
	assert.Contains(t, toolNames, "echo")
	assert.Contains(t, toolNames, "add")
	assert.Contains(t, toolNames, "fail")
	t.Logf("HTTP backend tools: %v", toolNames)

	// Session should be accessible.
	session, ok := m.Session("http-backend")
	require.True(t, ok)
	require.NotNil(t, session)

	// Call a tool via session.
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "echo",
		Arguments: map[string]any{"message": "hello-http"},
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.Content)
	t.Logf("Tool call result: %+v", result.Content)

	require.NoError(t, m.Stop(ctx, "http-backend"))
	e, _ = m.Entry("http-backend")
	assert.Equal(t, models.StatusStopped, e.Status)
}

func TestStart_SSEBackend(t *testing.T) {
	url := startHTTPMockServer(t, "sse")

	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"sse-backend": {URL: url},
		},
	}
	cfg.ApplyDefaults()

	m := NewManager(cfg, "test", testLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := m.Start(ctx, "sse-backend")
	require.NoError(t, err)

	e, ok := m.Entry("sse-backend")
	require.True(t, ok)
	assert.Equal(t, models.StatusRunning, e.Status)
	assert.Equal(t, "sse", e.Config.TransportType())
	assert.Equal(t, 0, e.PID, "SSE backends have no PID")
	assert.NotEmpty(t, e.Tools, "expected tools from SSE mock server")

	// Verify all tool names.
	toolNames := make([]string, len(e.Tools))
	for i, tool := range e.Tools {
		toolNames[i] = tool.Name
	}
	assert.Contains(t, toolNames, "echo")
	assert.Contains(t, toolNames, "add")
	assert.Contains(t, toolNames, "fail")
	t.Logf("SSE backend tools: %v", toolNames)

	// Call a tool.
	session, ok := m.Session("sse-backend")
	require.True(t, ok)
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "add",
		Arguments: map[string]any{"a": 3.0, "b": 4.0},
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.Content)
	t.Logf("Tool call result: %+v", result.Content)

	require.NoError(t, m.Stop(ctx, "sse-backend"))
}

func TestHTTPBackend_StopAndRestart(t *testing.T) {
	url := startHTTPMockServer(t, "http")

	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"http-restart": {URL: url},
		},
	}
	cfg.ApplyDefaults()

	m := NewManager(cfg, "test", testLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Start.
	require.NoError(t, m.Start(ctx, "http-restart"))
	e, _ := m.Entry("http-restart")
	assert.Equal(t, models.StatusRunning, e.Status)
	assert.NotEmpty(t, e.Tools)

	// Stop.
	require.NoError(t, m.Stop(ctx, "http-restart"))
	e, _ = m.Entry("http-restart")
	assert.Equal(t, models.StatusStopped, e.Status)
	assert.Empty(t, e.Tools)

	// Re-start — should reconnect and re-fetch tools.
	require.NoError(t, m.Start(ctx, "http-restart"))
	e, _ = m.Entry("http-restart")
	assert.Equal(t, models.StatusRunning, e.Status)
	assert.NotEmpty(t, e.Tools, "tools should be re-fetched after reconnect")
	t.Logf("Stop/restart round-trip: tools=%d", len(e.Tools))

	// Verify tool call works after reconnect.
	session, ok := m.Session("http-restart")
	require.True(t, ok)
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "echo",
		Arguments: map[string]any{"message": "after-restart"},
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.Content)

	require.NoError(t, m.Stop(ctx, "http-restart"))
}

func TestReconcile_HTTPBackend(t *testing.T) {
	url := startHTTPMockServer(t, "http")

	// Start with no servers.
	oldCfg := &models.Config{
		Servers: map[string]*models.ServerConfig{},
	}
	oldCfg.ApplyDefaults()

	m := NewManager(oldCfg, "test", testLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Reconcile: add HTTP backend.
	newCfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"http-added": {URL: url},
		},
	}
	newCfg.ApplyDefaults()

	require.NoError(t, m.Reconcile(ctx, newCfg))

	e, ok := m.Entry("http-added")
	require.True(t, ok)
	assert.Equal(t, models.StatusRunning, e.Status)
	assert.NotEmpty(t, e.Tools)
	t.Logf("Reconcile added HTTP backend, tools: %d", len(e.Tools))

	// Reconcile: remove HTTP backend.
	emptyCfg := &models.Config{
		Servers: map[string]*models.ServerConfig{},
	}
	emptyCfg.ApplyDefaults()

	require.NoError(t, m.Reconcile(ctx, emptyCfg))

	_, ok = m.Entry("http-added")
	assert.False(t, ok, "removed HTTP backend should not exist")
	assert.Empty(t, m.Entries(), "no entries should remain after removal")
	t.Log("Reconcile removed HTTP backend")
}

func TestHTTPBackend_AuthHeaders(t *testing.T) {
	const token = "test-secret-token-42"
	url := startHTTPMockServer(t, "http", "--auth-token="+token)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Without headers — should fail to connect (401 from mock).
	t.Run("without_header", func(t *testing.T) {
		cfg := &models.Config{
			Servers: map[string]*models.ServerConfig{
				"no-auth": {URL: url},
			},
		}
		cfg.ApplyDefaults()

		m := NewManager(cfg, "test", testLogger())
		err := m.Start(ctx, "no-auth")
		assert.Error(t, err, "connection without auth header should fail")
		t.Logf("Expected error: %v", err)

		e, _ := m.Entry("no-auth")
		assert.Equal(t, models.StatusError, e.Status)
	})

	// With correct header — should succeed.
	t.Run("with_header", func(t *testing.T) {
		cfg := &models.Config{
			Servers: map[string]*models.ServerConfig{
				"with-auth": {
					URL:     url,
					Headers: map[string]string{"Authorization": "Bearer " + token},
				},
			},
		}
		cfg.ApplyDefaults()

		m := NewManager(cfg, "test", testLogger())
		err := m.Start(ctx, "with-auth")
		require.NoError(t, err)

		e, ok := m.Entry("with-auth")
		require.True(t, ok)
		assert.Equal(t, models.StatusRunning, e.Status)
		assert.NotEmpty(t, e.Tools)
		t.Logf("Auth headers: connected, tools=%d", len(e.Tools))

		// Tool call should work.
		session, ok := m.Session("with-auth")
		require.True(t, ok)
		result, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name:      "echo",
			Arguments: map[string]any{"message": "authenticated"},
		})
		require.NoError(t, err)
		require.NotEmpty(t, result.Content)

		require.NoError(t, m.Stop(ctx, "with-auth"))
	})
}

func TestReconcile_HeaderChange(t *testing.T) {
	const token = "test-secret-token-42"
	url := startHTTPMockServer(t, "http", "--auth-token="+token)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Start with correct headers.
	cfg1 := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"auth-srv": {
				URL:     url,
				Headers: map[string]string{"Authorization": "Bearer " + token},
			},
		},
	}
	cfg1.ApplyDefaults()

	m := NewManager(cfg1, "test", testLogger())
	require.NoError(t, m.Start(ctx, "auth-srv"))

	e, _ := m.Entry("auth-srv")
	assert.Equal(t, models.StatusRunning, e.Status)

	// Reconcile with changed header value — should trigger restart.
	cfg2 := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"auth-srv": {
				URL:     url,
				Headers: map[string]string{"Authorization": "Bearer " + token, "X-Extra": "value"},
			},
		},
	}
	cfg2.ApplyDefaults()

	require.NoError(t, m.Reconcile(ctx, cfg2))

	e, _ = m.Entry("auth-srv")
	assert.Equal(t, models.StatusRunning, e.Status)
	// Verify the config was updated.
	assert.Equal(t, "value", e.Config.Headers["X-Extra"])
	t.Log("Header change triggered Reconcile restart")

	m.StopAll(ctx)
}
