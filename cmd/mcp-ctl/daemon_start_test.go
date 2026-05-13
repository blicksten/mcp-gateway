package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunDaemonStart_OpensDaemonLog verifies that runDaemonStart opens the
// daemon log file and assigns it to child.Stdout and child.Stderr before
// calling the spawn hook.
func TestRunDaemonStart_OpensDaemonLog(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "daemon.log")

	var capturedChild *exec.Cmd

	// Override openDaemonLog to write to a temp file so we can inspect it.
	t.Cleanup(setOpenDaemonLog(func() (*os.File, error) {
		return os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	}))

	// Override spawnFunc to capture the child and avoid real process spawn.
	t.Cleanup(setSpawnFunc(func(cmd *exec.Cmd) error {
		capturedChild = cmd
		return nil
	}))

	// Resolve a real executable for path validation.
	bin, err := exec.LookPath("go")
	require.NoError(t, err, "need 'go' in PATH for test binary")

	// Use a fake health URL (never polled because spawn succeeds immediately
	// and pollUntilLive will time out — but we override wait to be very short).
	errBuf := new(bytes.Buffer)
	rootCmd := newRootCmd()
	rootCmd.SetOut(new(bytes.Buffer))
	rootCmd.SetErr(errBuf)
	rootCmd.SetArgs([]string{
		"--api-url", "http://127.0.0.1:1",
		"daemon", "start",
		"--daemon-path", bin,
		"--wait", "50ms",
	})
	// We expect a "daemon did not become reachable" error (health poll fails
	// on 127.0.0.1:1) but the log + spawn path must have run.
	rootCmd.ExecuteC() //nolint:errcheck — error expected due to health timeout

	require.NotNil(t, capturedChild, "spawn hook must have been called")

	// child.Stdout and child.Stderr must both point at the log file.
	// After runDaemonStart returns (even on error), the parent handle is closed,
	// but the assignment was made to the cmd before spawn.
	logFile, err := os.OpenFile(logPath, os.O_RDONLY, 0)
	require.NoError(t, err, "daemon.log must have been created by openDaemonLog")
	_ = logFile.Close()

	// Both I/O slots were the same *os.File (same underlying fd at assign time).
	assert.Equal(t, capturedChild.Stdout, capturedChild.Stderr,
		"child.Stdout and child.Stderr must reference the same log file")
}

// TestRunDaemonStart_LogFileOpenFailDoesNotBlockSpawn verifies that when
// openDaemonLog returns an error, runDaemonStart still calls the spawn hook
// and prints a warning to stderr — it does NOT abort the start sequence.
func TestRunDaemonStart_LogFileOpenFailDoesNotBlockSpawn(t *testing.T) {
	spawnCalled := false

	// openDaemonLog always fails.
	t.Cleanup(setOpenDaemonLog(func() (*os.File, error) {
		return nil, fmt.Errorf("injected open failure")
	}))

	// Capture whether spawn was called.
	t.Cleanup(setSpawnFunc(func(cmd *exec.Cmd) error {
		spawnCalled = true
		return nil
	}))

	bin, err := exec.LookPath("go")
	require.NoError(t, err)

	errBuf := new(bytes.Buffer)
	rootCmd := newRootCmd()
	rootCmd.SetOut(new(bytes.Buffer))
	rootCmd.SetErr(errBuf)
	rootCmd.SetArgs([]string{
		"--api-url", "http://127.0.0.1:1",
		"daemon", "start",
		"--daemon-path", bin,
		"--wait", "50ms",
	})
	rootCmd.ExecuteC() //nolint:errcheck — health timeout expected

	assert.True(t, spawnCalled, "spawn must still be called when log open fails")
	assert.Contains(t, errBuf.String(), "warning: cannot open daemon log file",
		"a warning about the log failure must appear on stderr")
}
