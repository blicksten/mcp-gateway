//go:build windows

package lifecycle

import (
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIsProcessLive_LiveAndDead verifies OpenProcess + GetExitCodeProcess
// liveness detection for both live and exited PIDs.
func TestIsProcessLive_LiveAndDead(t *testing.T) {
	// Spawn a long-running ping. cmd.exe wrapper ensures CREATE_NEW_PROCESS_
	// GROUP applies via configureSysProcAttr.
	cmd := exec.Command("cmd.exe", "/c", "ping -n 30 127.0.0.1 >nul")
	configureSysProcAttr(cmd)
	require.NoError(t, cmd.Start())
	pid := cmd.Process.Pid
	defer cmd.Wait()

	live, err := isProcessLive(pid)
	require.NoError(t, err)
	assert.True(t, live, "freshly-spawned PID %d must report live", pid)

	// Kill it and wait for reap.
	require.NoError(t, cmd.Process.Kill())
	_, _ = cmd.Process.Wait()

	// The Windows kernel keeps the PID entry alive long enough for
	// GetExitCodeProcess to return a non-STILL_ACTIVE code, which is what
	// isProcessLive uses to distinguish.
	live, err = isProcessLive(pid)
	require.NoError(t, err)
	assert.False(t, live, "killed PID %d must report not-live", pid)
}

// TestIsProcessLive_InvalidPID verifies the input guard.
func TestIsProcessLive_InvalidPID(t *testing.T) {
	_, err := isProcessLive(0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid pid")

	_, err = isProcessLive(-1)
	require.Error(t, err)
}

// TestGracefulKillByPID_TerminatesWithinBound spawns a `ping` child and
// verifies gracefulKillByPID terminates it within a bounded wall-clock
// window. The path actually taken (CTRL_BREAK graceful vs TerminateProcess
// escalation) depends on whether the test runner shares a console with the
// spawned cmd.exe — both are valid production behaviours. The honesty
// boundary of this test is "kill returned within window and child is dead",
// not "TerminateProcess was reached". A dedicated escalation-path proof
// would require spawning the child with CREATE_NO_WINDOW (deferred to a
// later integration suite).
func TestGracefulKillByPID_TerminatesWithinBound(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/c", "ping -n 30 127.0.0.1 >nul")
	configureSysProcAttr(cmd)
	require.NoError(t, cmd.Start())
	pid := cmd.Process.Pid
	defer cmd.Wait()

	const grace = 300 * time.Millisecond
	start := time.Now()
	err := gracefulKillByPID(pid, grace)
	elapsed := time.Since(start)
	require.NoError(t, err)
	assert.Less(t, elapsed, 5*time.Second, "must finish within bounded window")

	// Poll for exit; TerminateProcess delivery may take a tick on busy CI.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		live, err := isProcessLive(pid)
		require.NoError(t, err)
		if !live {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("child PID %d still live after gracefulKillByPID returned", pid)
}

// TestGracefulKillByPID_InvalidPID verifies the input guard.
func TestGracefulKillByPID_InvalidPID(t *testing.T) {
	err := gracefulKillByPID(0, time.Second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid pid")
}
