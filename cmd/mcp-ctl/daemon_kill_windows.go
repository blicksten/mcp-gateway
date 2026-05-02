//go:build windows

package main

import (
	"fmt"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// verifyExeBasename returns the basename of the executable image of the
// process at pid. Used by killProcessByPID to confirm the process is the
// mcp-gateway daemon before killing — guards against PID recycling on
// Windows where a recycled PID could belong to an unrelated process by
// the time mcp-ctl tries to kill it (B-NEW-21).
//
// Uses PROCESS_QUERY_LIMITED_INFORMATION (lower privilege than
// PROCESS_QUERY_INFORMATION) so the query works for processes the current
// user owns without requiring elevation.
//
// On lookup failure (process exited, access denied, system race) returns
// a non-nil error. Caller policy: refuse-to-kill on error — preserving a
// stale pidfile is strictly better than killing the wrong process.
func verifyExeBasename(pid int) (string, error) {
	const desiredAccess = windows.PROCESS_QUERY_LIMITED_INFORMATION
	handle, err := windows.OpenProcess(desiredAccess, false, uint32(pid))
	if err != nil {
		return "", fmt.Errorf("OpenProcess(%d): %w", pid, err)
	}
	defer windows.CloseHandle(handle) //nolint:errcheck — close is best-effort

	// Buffer must hold a full path (Windows MAX_PATH=260, but long-path-aware
	// processes can return up to ~32k. Use a generous 1024 to balance memory
	// and tail-truncation risk; if a daemon is installed at a 1k-char path
	// the verification just returns ERROR_INSUFFICIENT_BUFFER and we refuse
	// the kill, which is safer than guessing).
	buf := make([]uint16, 1024)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(handle, 0, &buf[0], &size); err != nil {
		return "", fmt.Errorf("QueryFullProcessImageName(%d): %w", pid, err)
	}
	fullPath := windows.UTF16ToString(buf[:size])
	if fullPath == "" {
		return "", fmt.Errorf("QueryFullProcessImageName(%d): empty path", pid)
	}
	return filepath.Base(fullPath), nil
}
