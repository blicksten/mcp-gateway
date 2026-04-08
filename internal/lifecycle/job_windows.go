//go:build windows

package lifecycle

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// jobHandle wraps windows.Handle for cross-platform Manager compatibility.
type jobHandle = windows.Handle

// newJobObject creates a Windows Job Object configured to kill all assigned
// processes when the last handle is closed (i.e., when the gateway daemon exits).
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
