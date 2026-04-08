package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLogsCommand(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/servers/my-srv/logs" {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			fmt.Fprint(w, "data: hello world\n\n")
			fmt.Fprint(w, "data: second line\n\n")
			w.(http.Flusher).Flush()
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	out, err := executeCommand(ts, "logs", "--no-reconnect", "my-srv")
	require.NoError(t, err)
	assert.Contains(t, out, "hello world")
	assert.Contains(t, out, "second line")
}

func TestLogsCommand_NotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "server not found"})
	}))
	defer ts.Close()

	_, err := executeCommand(ts, "logs", "--no-reconnect", "missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "server not found")
}

func TestLogsCommand_MissingArg(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer ts.Close()

	_, err := executeCommand(ts, "logs")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg")
}

func TestLogsCommand_ContextCancellation(t *testing.T) {
	connected := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(connected)
		// Hold connection open until client disconnects.
		<-r.Context().Done()
	}))
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	buf := new(bytes.Buffer)
	cmd := newRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"--api-url", ts.URL, "logs", "--no-reconnect", "my-srv"})
	cmd.SetContext(ctx)

	done := make(chan error, 1)
	go func() {
		_, err := cmd.ExecuteC()
		done <- err
	}()

	// Wait for the connection to establish, then cancel.
	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for connection to establish")
	}
	cancel()

	select {
	case err := <-done:
		assert.NoError(t, err, "context cancellation should not produce an error")
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for logs command to exit")
	}
}

func TestLogsCommand_SSEFieldsIgnored(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		// Interleave non-data fields — should be ignored.
		fmt.Fprint(w, "event: message\n")
		fmt.Fprint(w, "id: 1\n")
		fmt.Fprint(w, "retry: 3000\n")
		fmt.Fprint(w, "data: actual log line\n\n")
		w.(http.Flusher).Flush()
	}))
	defer ts.Close()

	out, err := executeCommand(ts, "logs", "--no-reconnect", "my-srv")
	require.NoError(t, err)
	assert.Contains(t, out, "actual log line")
	assert.NotContains(t, out, "event:")
	assert.NotContains(t, out, "retry:")
}
