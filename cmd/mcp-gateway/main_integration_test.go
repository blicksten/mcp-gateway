//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIntegration_ShutdownEndpoint spawns a real daemon, calls POST /shutdown,
// and asserts:
//   - 202 received within 1 second
//   - daemon process exits with code 0 within 10 seconds
//   - PID file is removed after exit
func TestIntegration_ShutdownEndpoint(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}

	// Build the daemon binary into a temp dir.
	tmpDir := t.TempDir()
	binary := filepath.Join(tmpDir, "mcp-gateway")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, "mcp-gateway/cmd/mcp-gateway")
	build.Dir = moduleRoot(t)
	out, err := build.CombinedOutput()
	require.NoError(t, err, "build failed: %s", string(out))

	// Write a minimal config.
	cfgDir := filepath.Join(tmpDir, "cfg")
	require.NoError(t, os.MkdirAll(cfgDir, 0700))
	cfgPath := filepath.Join(cfgDir, "config.json")
	cfgContent := `{"gateway":{"http_port":0,"allow_remote":false},"servers":{}}`
	require.NoError(t, os.WriteFile(cfgPath, []byte(cfgContent), 0600))

	// Write a Bearer token file (auth.token convention — ~/.mcp-gateway/auth.token).
	// In integration tests we use a known token so we can POST /shutdown.
	// The daemon reads the token from cfg dir / auth.token.
	authTokenPath := filepath.Join(cfgDir, "auth.token")
	const testToken = "integration-test-token-abc123"
	require.NoError(t, os.WriteFile(authTokenPath, []byte(testToken+"\n"), 0600))

	// Spawn the daemon.
	// Use a custom XDG_RUNTIME_DIR pointing into tmpDir so the PID file
	// goes somewhere predictable that we can check.
	pidDir := filepath.Join(tmpDir, "xdg-run")
	require.NoError(t, os.MkdirAll(pidDir, 0700))
	expectedPID := filepath.Join(pidDir, "mcp-gateway.pid")

	cmd := exec.CommandContext(context.Background(), binary,
		"--config", cfgPath,
	)
	cmd.Env = append(os.Environ(),
		"XDG_RUNTIME_DIR="+pidDir,
		"HOME="+cfgDir, // auth.token lookup uses HOME
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		// Best-effort kill if the test fails before graceful exit.
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})

	// Poll /health until the daemon is ready (up to 10s).
	// We need the actual port; since the daemon uses port 0 we read the
	// bound port from its stdout log — or use a fixed port. For simplicity
	// in this test, use a fixed port that is unlikely to conflict.
	port := findListenPort(t, &stderr, 10*time.Second)
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)

	pollHealth(t, baseURL, 10*time.Second)

	// PID file must exist.
	_, err = os.Stat(expectedPID)
	assert.NoError(t, err, "PID file should exist after daemon start")

	// POST /shutdown with valid Bearer token.
	shutURL := baseURL + "/api/v1/shutdown"
	req, err := http.NewRequest(http.MethodPost, shutURL, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+testToken)

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err, "POST /shutdown should not fail")
	defer resp.Body.Close()

	assert.Equal(t, http.StatusAccepted, resp.StatusCode,
		"expected 202 from /shutdown")

	var body map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)
	assert.Equal(t, "shutting_down", body["status"])

	// Daemon must exit within 10 seconds.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		// Process exited — check exit code.
		if err != nil {
			// Allow signal exits on Unix (killed by our own SIGTERM via REST).
			t.Logf("daemon stderr: %s", stderr.String())
		}
		// Exit code 0 expected for clean shutdown.
		if exitErr, ok := err.(*exec.ExitError); ok {
			assert.Equal(t, 0, exitErr.ExitCode(),
				"daemon should exit with code 0 after /shutdown\nstderr: %s", stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("daemon did not exit within 10s after /shutdown\nstderr: %s", stderr.String())
	}

	// PID file must be removed after clean exit.
	_, err = os.Stat(expectedPID)
	assert.True(t, os.IsNotExist(err), "PID file should be removed after clean exit")
}

// moduleRoot returns the Go module root by walking up from the test binary.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	// Walk up until we find go.mod.
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod")
		}
		dir = parent
	}
}

// pollHealth blocks until GET /health returns 200 or deadline is exceeded.
func pollHealth(t *testing.T, baseURL string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 500 * time.Millisecond}
	for time.Now().Before(deadline) {
		resp, err := client.Get(baseURL + "/api/v1/health")
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			return
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("daemon did not become healthy within %s", timeout)
}

// findListenPort reads daemon stderr logs looking for the "mcp-gateway started"
// line and extracts the port. Falls back to a heuristic scan.
func findListenPort(t *testing.T, buf *bytes.Buffer, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		// Scan for a log line containing "addr=" or "port="
		content := buf.String()
		for _, line := range strings.Split(content, "\n") {
			if port := extractPort(line); port > 0 {
				return port
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Fallback: if we cannot parse, fail.
	t.Fatalf("could not determine daemon listen port from logs:\n%s", buf.String())
	return 0
}

// extractPort parses a port number from a log line containing "addr=:PORT".
func extractPort(line string) int {
	// Look for patterns like "addr=:8080" or "http_port=8080".
	for _, prefix := range []string{"addr=:", "port="} {
		idx := strings.Index(line, prefix)
		if idx < 0 {
			continue
		}
		rest := line[idx+len(prefix):]
		var port int
		fmt.Sscanf(rest, "%d", &port)
		if port > 0 {
			return port
		}
	}
	return 0
}
