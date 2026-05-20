//go:build !windows

package lifecycle

import (
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIsProcessLive_LiveAndDead verifies the kill(pid, 0) liveness probe for
// both live and reaped PIDs.
func TestIsProcessLive_LiveAndDead(t *testing.T) {
	// Spawn a short sleep so the process is definitely alive.
	cmd := exec.Command("sleep", "30")
	configureSysProcAttr(cmd)
	require.NoError(t, cmd.Start())
	pid := cmd.Process.Pid

	live, err := isProcessLive(pid)
	require.NoError(t, err)
	assert.True(t, live, "freshly-spawned PID %d must report live", pid)

	// Kill via the process group and wait for reap.
	require.NoError(t, syscall.Kill(-pid, syscall.SIGKILL))
	_, _ = cmd.Process.Wait()

	live, err = isProcessLive(pid)
	require.NoError(t, err)
	assert.False(t, live, "reaped PID %d must report not-live", pid)
}

// TestIsProcessLive_InvalidPID verifies the input guard.
func TestIsProcessLive_InvalidPID(t *testing.T) {
	_, err := isProcessLive(0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid pid")

	_, err = isProcessLive(-1)
	require.Error(t, err)
}

// TestGracefulKillByPID_HappyPath spawns a sleep that honours SIGTERM and
// verifies gracefulKillByPID returns inside the grace window (i.e. the
// SIGKILL escalation does NOT fire).
func TestGracefulKillByPID_HappyPath(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	configureSysProcAttr(cmd)
	require.NoError(t, cmd.Start())
	pid := cmd.Process.Pid
	defer cmd.Wait()

	start := time.Now()
	err := gracefulKillByPID(pid, 3*time.Second)
	elapsed := time.Since(start)
	require.NoError(t, err)
	assert.Less(t, elapsed, 3*time.Second,
		"sleep responds to SIGTERM so gracefulKillByPID must finish well inside grace")

	// Process should be gone.
	live, err := isProcessLive(pid)
	require.NoError(t, err)
	assert.False(t, live)
}

// TestGracefulKillByPID_EscalationOnHung spawns a shell that genuinely
// survives SIGTERM and verifies gracefulKillByPID still terminates the
// process by escalating to SIGKILL after the grace window.
//
// Test design rationale (CV finding fix):
//   - `trap : TERM` installs `:` (no-op) as the SIGTERM handler. Critically
//     this is NOT the same as `trap "" TERM` (truly ignore): a `:` handler
//     means the shell receives the signal, runs `:`, and stays alive. Dash
//     (Ubuntu CI's `/bin/sh`) and bash both honour this consistently.
//   - The `while true; do sleep 1; done` loop ensures dash cannot apply the
//     single-command exec optimisation that would replace the shell with
//     `sleep` (which would NOT inherit the trap). Multiple statements +
//     a loop force the shell to remain as the group leader.
//   - We poll the SHELL's PID, which is the process that absorbs SIGTERM
//     and remains alive — proving the escalation path is the only way to
//     reach termination within the wider deadline.
func TestGracefulKillByPID_EscalationOnHung(t *testing.T) {
	cmd := exec.Command("sh", "-c", `trap : TERM; while true; do sleep 1; done`)
	configureSysProcAttr(cmd)
	require.NoError(t, cmd.Start())
	pid := cmd.Process.Pid
	defer cmd.Wait()

	const grace = 300 * time.Millisecond
	start := time.Now()
	err := gracefulKillByPID(pid, grace)
	elapsed := time.Since(start)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, elapsed, grace,
		"escalation path must wait out the grace window first; got elapsed=%s grace=%s", elapsed, grace)

	// Poll for exit (Wait reaps; SIGKILL delivery may take a tick).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		live, err := isProcessLive(pid)
		require.NoError(t, err)
		if !live {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("hung child PID %d still live after SIGKILL escalation", pid)
}

// TestGracefulKillByPID_InvalidPID verifies the input guard.
func TestGracefulKillByPID_InvalidPID(t *testing.T) {
	err := gracefulKillByPID(0, time.Second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid pid")
}
