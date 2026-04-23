package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"
	"time"

	"mcp-gateway/internal/ctlclient"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- formatUptime tests ---

func TestFormatUptime(t *testing.T) {
	cases := []struct {
		seconds  int64
		expected string
	}{
		{0, "0s"},
		{1, "1s"},
		{59, "59s"},
		{60, "1m 0s"},
		{61, "1m 1s"},
		{3599, "59m 59s"},
		{3600, "1h 0m 0s"},
		{3661, "1h 1m 1s"},
		{86399, "23h 59m 59s"},
		{86400, "1d 0h 0m"},
		{86401, "1d 0h 0m"},
		{90061, "1d 1h 1m"},
		{-1, "0s"}, // negative clamped to 0
	}
	for _, tc := range cases {
		t.Run(tc.expected, func(t *testing.T) {
			got := formatUptime(tc.seconds)
			assert.Equal(t, tc.expected, got)
		})
	}
}

// --- daemon status command tests ---

func TestDaemonStatusCommand_Running(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/health" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"status":         "ok",
				"servers":        7,
				"running":        7,
				"pid":            12345,
				"version":        "v1.7.0",
				"started_at":     "2026-04-23T14:02:17Z",
				"uptime_seconds": int64(8057),
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	out, err := executeCommand(ts, "daemon", "status")
	require.NoError(t, err)
	assert.Contains(t, out, "ok")
	assert.Contains(t, out, "12345")
	assert.Contains(t, out, "v1.7.0")
	assert.Contains(t, out, "2026-04-23T14:02:17Z")
	assert.Contains(t, out, "UPTIME")
	assert.Contains(t, out, "7")
}

func TestDaemonStatusCommand_InfoAlias(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"status": "ok", "servers": 0, "running": 0,
		})
	}))
	defer ts.Close()

	out, err := executeCommand(ts, "daemon", "info")
	require.NoError(t, err)
	assert.Contains(t, out, "ok")
}

func TestDaemonStatusCommand_OlderDaemon_NoExtendedFields(t *testing.T) {
	// Daemon responds but without extended fields — should still print STATUS row.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"status": "ok", "servers": 2, "running": 2,
		})
	}))
	defer ts.Close()

	out, err := executeCommand(ts, "daemon", "status")
	require.NoError(t, err)
	assert.Contains(t, out, "STATUS")
	assert.Contains(t, out, "ok")
	// PID/VERSION/STARTED/UPTIME rows absent when fields are zero.
	assert.NotContains(t, out, "PID")
	assert.NotContains(t, out, "VERSION")
	assert.NotContains(t, out, "UPTIME")
}

func TestDaemonStatusCommand_Offline(t *testing.T) {
	// Point at a port that's not listening.
	buf := new(bytes.Buffer)
	cmd := newRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"--api-url", "http://127.0.0.1:1", "daemon", "status"})
	_, err := cmd.ExecuteC()
	require.Error(t, err)
	var connErr *ctlclient.ConnectionError
	assert.ErrorAs(t, err, &connErr)
	assert.Contains(t, buf.String(), "offline")
}

// --- daemon stop command tests ---

func TestDaemonStopCommand_Success(t *testing.T) {
	callCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/shutdown":
			assert.Equal(t, "POST", r.Method)
			callCount++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(map[string]string{"status": "shutting_down"})
		case "/api/v1/health":
			// Simulate daemon stopping after first shutdown call.
			if callCount > 0 {
				http.Error(w, "gone", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"status": "ok", "servers": 1, "running": 1})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	out, err := executeCommand(ts, "daemon", "stop")
	require.NoError(t, err)
	assert.Contains(t, out, "stopped")
}

func TestDaemonStopCommand_AlreadyDown(t *testing.T) {
	// Daemon not reachable — /shutdown returns connection error but that means
	// daemon is already down, so stop should succeed.
	// We test via mocking at the client level by using a stopped server.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	ts.Close() // close immediately so all requests fail with connection refused

	// Since health endpoint is also unreachable, pollUntilUnreachable returns true immediately.
	// But the stop command gets client from context which will also fail to connect.
	// The executeCommand path sets up a client that will get a ConnectionError on Shutdown.
	// stopDaemon returns nil on ConnectionError from Shutdown.
	// Then pollUntilUnreachable returns true since server is down.
	// daemon_stop.go calls stopDaemon and then pollUntilUnreachable separately — but
	// stopDaemon already polls internally, so let's just verify no panic and result.
	out, err := executeCommand(ts, "daemon", "stop")
	require.NoError(t, err)
	assert.Contains(t, out, "stopped")
}

// --- daemon start command tests ---

func TestDaemonStartCommand_AlreadyRunning(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"status": "ok", "servers": 1, "running": 1,
			"pid": 9999, "version": "v1.7.0",
		})
	}))
	defer ts.Close()

	out, err := executeCommand(ts, "daemon", "start")
	require.NoError(t, err)
	assert.Contains(t, out, "already running")
	assert.Contains(t, out, "9999")
}

func TestDaemonStartCommand_MissingBinary(t *testing.T) {
	// Daemon not reachable + binary doesn't exist → should fail.
	// Point to a closed server so /health probe returns false.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	ts.Close()

	_, err := executeCommand(ts, "daemon", "start", "--daemon-path", "/nonexistent/binary")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "daemon-path")
}

func TestDaemonStartCommand_SpawnHook(t *testing.T) {
	// Replace spawnFunc to capture the spawn call.
	spawned := false

	// Daemon not running initially, then becomes running after spawn.
	callCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callCount++
		if callCount <= 1 {
			// First call (pre-spawn liveness check) → daemon not running.
			http.Error(w, "not found", http.StatusServiceUnavailable)
			return
		}
		// Subsequent calls (post-spawn polling) → daemon is running.
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"status": "ok", "servers": 0, "running": 0,
			"pid": 1234, "version": "v1.7.0",
		})
	}))
	defer ts.Close()

	// Create a real (no-op) binary path for validation.
	bin, err := exec.LookPath("go")
	require.NoError(t, err, "need 'go' in PATH for test binary")

	restore := setSpawnFunc(func(cmd *exec.Cmd) error {
		spawned = true
		// Don't actually start the process — health mock handles it.
		return nil
	})
	t.Cleanup(restore)

	out, err := executeCommand(ts, "daemon", "start",
		"--daemon-path", bin,
		"--wait", "2s",
	)
	require.NoError(t, err)
	assert.True(t, spawned, "spawn hook must be called")
	assert.Contains(t, out, "started")
}

// --- daemon restart command tests ---

func TestDaemonRestartCommand_StopsAndStarts(t *testing.T) {
	shutdownCalled := false
	callCount := 0

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/shutdown":
			shutdownCalled = true
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(map[string]string{"status": "shutting_down"})
		case "/api/v1/health":
			callCount++
			// Calls 1-2: daemon running. Calls 3-4: daemon stopped. Calls 5+: daemon restarted.
			if callCount <= 2 {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{"status": "ok", "servers": 0, "running": 0})
			} else if callCount <= 4 {
				http.Error(w, "gone", http.StatusServiceUnavailable)
			} else {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{
					"status": "ok", "servers": 0, "running": 0,
					"pid": 5678, "version": "v1.7.1",
				})
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	bin, err := exec.LookPath("go")
	require.NoError(t, err)

	t.Cleanup(setSpawnFunc(func(_ *exec.Cmd) error { return nil }))

	out, err := executeCommand(ts, "daemon", "restart",
		"--daemon-path", bin,
		"--timeout", "5s",
		"--wait", "5s",
	)
	require.NoError(t, err)
	assert.True(t, shutdownCalled)
	assert.Contains(t, out, "restarted")
}

// --- isConnectionError tests ---

func TestIsConnectionError_WithConnectionError(t *testing.T) {
	err := &ctlclient.ConnectionError{URL: "http://x", Err: nil}
	var target *ctlclient.ConnectionError
	assert.True(t, isConnectionError(err, &target))
	assert.NotNil(t, target)
}

func TestIsConnectionError_WithOtherError(t *testing.T) {
	err := &ctlclient.APIError{StatusCode: 401, Message: "unauth"}
	assert.False(t, isConnectionError(err, nil))
}

func TestIsConnectionError_NilError(t *testing.T) {
	assert.False(t, isConnectionError(nil, nil))
}

// --- resolveDaemonBin tests ---

func TestResolveDaemonBin_ExplicitPath(t *testing.T) {
	bin, err := exec.LookPath("go")
	require.NoError(t, err)
	got, err := resolveDaemonBin(bin)
	require.NoError(t, err)
	assert.Equal(t, bin, got)
}

func TestResolveDaemonBin_NonExistent(t *testing.T) {
	_, err := resolveDaemonBin("/nonexistent/path/binary")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "daemon-path")
}

func TestResolveDaemonBin_Directory(t *testing.T) {
	_, err := resolveDaemonBin(t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "directory")
}

// --- pollUntilUnreachable / pollUntilLive tests ---

func TestPollUntilUnreachable_AlreadyDown(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	ts.Close()

	deadline := time.Now().Add(1 * time.Second)
	got := pollUntilUnreachable(t.Context(), ts.URL+"/api/v1/health", deadline)
	assert.True(t, got)
}

func TestPollUntilLive_AlreadyUp(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
	}))
	defer ts.Close()

	deadline := time.Now().Add(1 * time.Second)
	got := pollUntilLive(t.Context(), ts.URL+"/api/v1/health", deadline)
	assert.True(t, got)
}

func TestPollUntilLive_Timeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	// 100ms deadline — should fail fast.
	deadline := time.Now().Add(100 * time.Millisecond)
	got := pollUntilLive(t.Context(), ts.URL+"/api/v1/health", deadline)
	assert.False(t, got)
}
