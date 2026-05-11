//go:build !windows

package lifecycle

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// isProcessLive returns true when pid refers to a live OS process.
//
// Uses kill(pid, 0): signal 0 performs the permission/existence check
// without actually delivering a signal. ESRCH = no such process; EPERM =
// process exists but we cannot signal it (still counts as live for FM 1
// purposes — we just cannot reap it).
func isProcessLive(pid int) (bool, error) {
	if pid <= 0 {
		return false, fmt.Errorf("invalid pid: %d", pid)
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	if errors.Is(err, syscall.EPERM) {
		return true, nil // exists but not signalable by us
	}
	return false, fmt.Errorf("kill(%d, 0): %w", pid, err)
}

// gracefulKillByPID sends SIGTERM to the process group, waits up to grace
// for the process to exit, then escalates to SIGKILL if still alive.
//
// FM 1 Q3 = I: gracefulPolicy. Lets orchestrator/PAL children flush SQLite
// WAL and close sockets cleanly when the previous gateway crashed.
func gracefulKillByPID(pid int, grace time.Duration) error {
	if pid <= 0 {
		return fmt.Errorf("invalid pid: %d", pid)
	}
	// Try the process group first (matches lifecycle.terminateProcessGroup
	// semantics — children spawn with Setpgid=true on POSIX). If the
	// group signal fails (e.g. child was reaped or not in a group),
	// fall back to the leader.
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil // already gone
		}
		// Fall back to signalling just the leader.
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EINVAL) {
			if err2 := syscall.Kill(pid, syscall.SIGTERM); err2 != nil {
				if errors.Is(err2, syscall.ESRCH) {
					return nil
				}
				return fmt.Errorf("kill SIGTERM %d: %w", pid, err2)
			}
		} else {
			return fmt.Errorf("kill SIGTERM -%d: %w", pid, err)
		}
	}
	// Poll for exit up to grace.
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		live, err := isProcessLive(pid)
		if err != nil {
			return err
		}
		if !live {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Escalate: SIGKILL the group, then the leader as fallback.
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EINVAL) {
			if err2 := syscall.Kill(pid, syscall.SIGKILL); err2 != nil && !errors.Is(err2, syscall.ESRCH) {
				return fmt.Errorf("kill SIGKILL %d: %w", pid, err2)
			}
		} else {
			return fmt.Errorf("kill SIGKILL -%d: %w", pid, err)
		}
	}
	_ = os.Getpid() // keep os imported when build tags trim everything else
	return nil
}
