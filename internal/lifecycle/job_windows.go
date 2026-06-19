//go:build windows

package lifecycle

import (
	"fmt"
	"log/slog"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// jobHandle wraps windows.Handle for cross-platform Manager compatibility.
type jobHandle = windows.Handle

// newJobObject creates a Windows Job Object configured to kill all assigned
// processes when the last handle is closed (i.e., when the gateway daemon exits).
//
// L4 non-inheritable assertion: CreateJobObject is called with nil
// SECURITY_ATTRIBUTES (bInheritHandles=false, no SA_INHERIT flag) and a nil
// name, so:
//   - the handle is not inheritable by child processes;
//   - the job has no kernel name, so a foreign process cannot open it via
//     OpenJobObject(name) — it would need to inherit or duplicate our handle.
//
// Therefore L4 (foreign OpenJobObject leaking the job handle) is already
// mitigated by construction. No ACL restriction is required.
func newJobObject() (jobHandle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, fmt.Errorf("CreateJobObject: %w", err)
	}

	// Set JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE so all assigned processes are
	// terminated when the job handle is closed (including abnormal exit).
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	_, err = windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	if err != nil {
		windows.CloseHandle(job)
		return 0, fmt.Errorf("SetInformationJobObject: %w", err)
	}
	return job, nil
}

// retryAssignProcess attempts assignProcess with exponential backoff.
// Tries up to maxAssignAttempts times with 50/100/200ms sleeps between
// attempts (~0.35s total worst case) so a transient race (e.g. kernel
// object not yet fully initialised) is tolerated without blocking startup
// noticeably.
// Returns nil on success, or the last error after all retries are exhausted.
func retryAssignProcess(job jobHandle, pid uint32, logger *slog.Logger, name string) error {
	const maxAssignAttempts = 4
	delay := 50 * time.Millisecond
	var lastErr error
	for attempt := 1; attempt <= maxAssignAttempts; attempt++ {
		if err := assignProcess(job, pid); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt < maxAssignAttempts {
			logger.Warn("assign process to job object failed, retrying",
				"server", name, "pid", pid, "attempt", attempt, "backoff", delay)
			time.Sleep(delay)
			delay *= 2
		}
	}
	logger.Error("assign process to job object failed after retries; killing unassigned backend",
		"server", name, "pid", pid, "error", lastErr)
	return fmt.Errorf("AssignProcessToJobObject after %d attempts: %w", maxAssignAttempts, lastErr)
}

// assignProcess assigns a running process to the Job Object.
// The process handle must have PROCESS_SET_QUOTA and PROCESS_TERMINATE access.
//
// TOCTOU note: there is a small race window between CreateProcess (inside
// go-sdk's CommandTransport.Start()) and this call. A grandchild spawned
// during this window may escape the Job Object. This is an accepted limitation;
// the primary goal is cleanup-on-daemon-exit.
func assignProcess(job jobHandle, pid uint32) error {
	const desiredAccess = windows.PROCESS_SET_QUOTA | windows.PROCESS_TERMINATE
	proc, err := windows.OpenProcess(desiredAccess, false, pid)
	if err != nil {
		return fmt.Errorf("OpenProcess(%d): %w", pid, err)
	}
	defer windows.CloseHandle(proc)

	if err := windows.AssignProcessToJobObject(job, proc); err != nil {
		return fmt.Errorf("AssignProcessToJobObject(%d): %w", pid, err)
	}
	return nil
}

// closeJobObject closes the Job Object handle, which triggers termination
// of all assigned processes (due to JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE).
func closeJobObject(job jobHandle) error {
	return windows.CloseHandle(job)
}
