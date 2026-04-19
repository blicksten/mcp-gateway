package ctlclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var ctx = context.Background()

// newTestClient creates a Client pointing at the given httptest.Server.
func newTestClient(ts *httptest.Server) *Client {
	return New(ts.URL)
}

func TestHealth(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"status": "ok", "servers": 3, "running": 2,
		})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	h, err := c.Health(ctx)
	require.NoError(t, err)
	assert.Equal(t, "ok", h.Status)
	assert.Equal(t, 3, h.Servers)
	assert.Equal(t, 2, h.Running)
}

func TestListServers(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]ServerView{
			{Name: "alpha", Status: "running", Transport: "stdio", RestartCount: 0},
			{Name: "beta", Status: "stopped", Transport: "http", RestartCount: 1},
		})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	servers, err := c.ListServers(ctx)
	require.NoError(t, err)
	assert.Len(t, servers, 2)
	assert.Equal(t, "alpha", servers[0].Name)
	assert.Equal(t, "running", servers[0].Status)
}

func TestGetServer(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/servers/my-server", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ServerView{
			Name: "my-server", Status: "running", Transport: "stdio", PID: 1234,
		})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	sv, err := c.GetServer(ctx, "my-server")
	require.NoError(t, err)
	assert.Equal(t, "my-server", sv.Name)
	assert.Equal(t, 1234, sv.PID)
}

func TestGetServer_NotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "server not found"})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	_, err := c.GetServer(ctx, "missing")
	require.Error(t, err)
	var apiErr *APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, 404, apiErr.StatusCode)
	assert.Equal(t, "server not found", apiErr.Message)
}

func TestAddServer(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/api/v1/servers", r.URL.Path)

		var body struct {
			Name   string       `json:"name"`
			Config ServerConfig `json:"config"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "new-srv", body.Name)
		assert.Equal(t, "/usr/bin/srv", body.Config.Command)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"status": "added"})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	err := c.AddServer(ctx, "new-srv", ServerConfig{Command: "/usr/bin/srv"})
	require.NoError(t, err)
}

func TestRemoveServer(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "DELETE", r.Method)
		assert.Equal(t, "/api/v1/servers/old-srv", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "removed"})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	err := c.RemoveServer(ctx, "old-srv")
	require.NoError(t, err)
}

func TestPatchServer(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "PATCH", r.Method)
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, true, body["disabled"])
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "updated"})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	err := c.PatchServer(ctx, "srv", map[string]any{"disabled": true})
	require.NoError(t, err)
}

func TestRestartServer(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/api/v1/servers/srv/restart", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "restarted"})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	err := c.RestartServer(ctx, "srv")
	require.NoError(t, err)
}

func TestResetCircuit(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/api/v1/servers/srv/reset-circuit", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "circuit reset"})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	err := c.ResetCircuit(ctx, "srv")
	require.NoError(t, err)
}

func TestListTools(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]Tool{
			{Name: "read", Description: "Read a file", Server: "fs"},
		})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	tools, err := c.ListTools(ctx)
	require.NoError(t, err)
	assert.Len(t, tools, 1)
	assert.Equal(t, "read", tools[0].Name)
}

func TestCallTool(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/servers/fs/call", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(CallResult{
			Content: []ContentItem{{Type: "text", Text: "hello"}},
		})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	result, err := c.CallTool(ctx, "fs", "read", map[string]any{"path": "/tmp"})
	require.NoError(t, err)
	assert.Len(t, result.Content, 1)
	assert.Equal(t, "text", result.Content[0].Type)
	assert.Equal(t, "hello", result.Content[0].Text)
}

func TestStreamLogs(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		fmt.Fprint(w, "data: line one\n\n")
		fmt.Fprint(w, "data: line two\n\n")
		w.(http.Flusher).Flush()
	}))
	defer ts.Close()

	c := newTestClient(ts)
	var lines []string
	err := c.StreamLogs(ctx, "srv", func(line string) {
		lines = append(lines, line)
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"line one", "line two"}, lines)
}

// TestStreamLogs_AcceptsLineOver64KB pins the 1MB SSE scanner cap added
// in T15A.2a. Before v1.5.0 the default 64KB bufio.Scanner limit caused
// streamLogsOnce to exit with bufio.ErrTooLong on long MCP server log
// lines (tracebacks, JSON traces), truncating the stream. Regression
// would silently drop the remainder of any line over 64KB.
func TestStreamLogs_AcceptsLineOver64KB(t *testing.T) {
	// 250KB single log line — well above old 64KB cap, well under new 1MB ceiling.
	bigLine := strings.Repeat("abc ", 64*1024)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		fmt.Fprint(w, "data: "+bigLine+"\n\n")
		w.(http.Flusher).Flush()
	}))
	defer ts.Close()

	c := newTestClient(ts)
	var got []string
	err := c.StreamLogs(ctx, "srv", func(line string) {
		got = append(got, line)
	}, nil)
	require.NoError(t, err, "scanner must not error on 250KB line with raised 1MB cap")
	require.Len(t, got, 1, "big SSE line must deliver exactly one callback (no truncation)")
	assert.Equal(t, len(bigLine), len(got[0]), "delivered line must match sent length")
}

func TestStreamLogs_ContextCancellation(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		// Hold connection open until client disconnects.
		<-r.Context().Done()
	}))
	defer ts.Close()

	cancelCtx, cancel := context.WithCancel(context.Background())
	c := newTestClient(ts)

	done := make(chan error, 1)
	go func() {
		done <- c.StreamLogs(cancelCtx, "srv", func(_ string) {}, nil)
	}()

	cancel()
	err := <-done
	assert.NoError(t, err, "context cancellation should return nil, not an error")
}

func TestStreamLogs_NotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "server not found"})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	err := c.StreamLogs(ctx, "missing", func(_ string) {}, nil)
	require.Error(t, err)
	var apiErr *APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, 404, apiErr.StatusCode)
}

func TestStreamLogs_Reconnect(t *testing.T) {
	attempts := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		// First two attempts send data, then stop sending to exhaust retries.
		if attempts == 1 {
			fmt.Fprint(w, "data: first\n\n")
		} else if attempts == 2 {
			fmt.Fprint(w, "data: second\n\n")
		}
		// attempts >= 3: send nothing, just close — no data means retries count up.
		w.(http.Flusher).Flush()
	}))
	defer ts.Close()

	c := newTestClient(ts)
	var lines []string
	err := c.StreamLogs(ctx, "srv", func(line string) {
		lines = append(lines, line)
	}, &StreamLogsOptions{
		Reconnect:      true,
		MaxRetries:     2,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     50 * time.Millisecond,
	})
	// After attempt 2 sends data, retry counter resets. Attempts 3+ send no data,
	// so retries accumulate: 1, 2, then max retries exceeded.
	require.Error(t, err)
	assert.Contains(t, err.Error(), "max retries")
	assert.Contains(t, lines, "first")
	assert.Contains(t, lines, "second")
}

func TestStreamLogs_Reconnect_StopsOn404(t *testing.T) {
	attempts := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			fmt.Fprint(w, "data: first\n\n")
			w.(http.Flusher).Flush()
			return
		}
		// Second attempt: server removed.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "server not found"})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	var lines []string
	err := c.StreamLogs(ctx, "srv", func(line string) {
		lines = append(lines, line)
	}, &StreamLogsOptions{
		Reconnect:      true,
		MaxRetries:     5,
		InitialBackoff: 10 * time.Millisecond,
	})
	require.Error(t, err)
	var apiErr *APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, 404, apiErr.StatusCode)
	assert.Equal(t, []string{"first"}, lines)
}

func TestConnectionError(t *testing.T) {
	// Connect to a port that's not listening.
	c := New("http://127.0.0.1:1")
	_, err := c.Health(ctx)
	require.Error(t, err)
	var connErr *ConnectionError
	require.True(t, errors.As(err, &connErr))
	assert.Contains(t, connErr.Error(), "gateway not running at")
}

func TestURLEncodingSpecialChars(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Go auto-decodes percent-encoding in r.URL.Path; verify the decoded name arrives.
		assert.Equal(t, "/api/v1/servers/my server", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ServerView{Name: "my server", Status: "running"})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	sv, err := c.GetServer(ctx, "my server")
	require.NoError(t, err)
	assert.Equal(t, "my server", sv.Name)
}
