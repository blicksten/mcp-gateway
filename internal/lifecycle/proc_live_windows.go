//go:build windows

package lifecycle

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// stillActiveExitCode is STILL_ACTIVE (259) — Windows process exit code
// returned by GetExitCodeProcess for a process that has not yet terminated.
const stillActiveExitCode = 259

// isProcessLive returns true when pid refers to a live OS process.
//
// Uses OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION) + GetExitCodeProcess.
// The LIMITED_INFORMATION access right exists on every Win Vista+ kernel
// and does not require elevated privileges for processes owned by the same
// user, which is the only case FM 1 needs (gateway-spawned subprocesses).
func isProcessLive(pid int) (bool, error) {
	if pid <= 0 {
		return false, fmt.Errorf("invalid pid: %d", pid)
	}
	const access = windows.PROCESS_QUERY_LIMITED_INFORMATION
	h, err := windows.OpenProcess(access, false, uint32(pid))
	if err != nil {
		// ERROR_INVALID_PARAMETER (87) is what Windows returns when no
		// process with that PID exists at all. Treat as "not live".
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return false, nil
		}
		// ERROR_ACCESS_DENIED (5): process exists, owned by a different
		// user / has stronger ACLs. Live, but unreachable for reaping.
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return true, nil
		}
		return false, fmt.Errorf("OpenProcess(%d): %w", pid, err)
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false, fmt.Errorf("GetExitCodeProcess(%d): %w", pid, err)
	}
	return code == stillActiveExitCode, nil
}

// gracefulKillByPID sends Ctrl-Break to the process group, waits up to
// grace for exit, then escalates to TerminateProcess if still alive.
//
// FM 1 Q3 = I: graceful policy. Children spawned by lifecycle.Manager are
// configured with CREATE_NEW_PROCESS_GROUP (see procattr_windows.go), so
// GenerateConsoleCtrlEvent with their PID-as-group-id triggers a clean
// CTRL_BREAK_EVENT delivery. Console subsystem children handle this as a
// shutdown signal; non-console children are unaffected and proceed to the
// hard-kill fallback.
func gracefulKillByPID(pid int, grace time.Duration) error {
	if pid <= 0 {
		return fmt.Errorf("invalid pid: %d", pid)
	}

	// Step 1 — try Ctrl-Break to the process group.
	// GenerateConsoleCtrlEvent on the child's PGID (which equals the child
	// PID when CREATE_NEW_PROCESS_GROUP was used) sends CTRL_BREAK_EVENT
	// to every process in that group. Errors here are best-effort — we
	// fall through to the hard-kill path regardless.
	_ = windows.GenerateConsoleCtrlEvent(syscall.CTRL_BREAK_EVENT, uint32(pid))

	// Step 2 — poll for exit up to grace.
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

	// Step 3 — TerminateProcess as last resort.
	const access = windows.PROCESS_TERMINATE
	h, err := windows.OpenProcess(access, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return nil // already gone between deadline check and OpenProcess
		}
		return fmt.Errorf("OpenProcess(PROCESS_TERMINATE, %d): %w", pid, err)
	}
	defer windows.CloseHandle(h)
	if err := windows.TerminateProcess(h, 1); err != nil {
		return fmt.Errorf("TerminateProcess(%d): %w", pid, err)
	}
	_ = os.Getpid() // keep os imported when build tags trim everything else
	return nil
}
