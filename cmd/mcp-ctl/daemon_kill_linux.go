//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// verifyExeBasename returns the basename of the executable image of the
// process at pid by reading the /proc/<pid>/exe symlink. Used by
// killProcessByPID to confirm the process is the mcp-gateway daemon
// before killing — symmetric protection to the symlink-attack guard
// pidfile.Write already has on the create side (M-1 from D.1 audit).
//
// On lookup failure (process exited between PID-file read and verify,
// access denied for processes in another user's namespace) returns a
// non-nil error. Caller policy: refuse-to-kill on error — preserving a
// stale pidfile is strictly better than killing the wrong process.
func verifyExeBasename(pid int) (string, error) {
	exePath := fmt.Sprintf("/proc/%d/exe", pid)
	target, err := os.Readlink(exePath)
	if err != nil {
		return "", fmt.Errorf("readlink %s: %w", exePath, err)
	}
	if target == "" {
		return "", fmt.Errorf("readlink %s: empty target", exePath)
	}
	// /proc/<pid>/exe target may include a " (deleted)" suffix when the
	// daemon binary was removed/replaced after process start. Strip it
	// so basename matches across upgrade scenarios.
	const deletedSuffix = " (deleted)"
	if l := len(target); l > len(deletedSuffix) && target[l-len(deletedSuffix):] == deletedSuffix {
		target = target[:l-len(deletedSuffix)]
	}
	return filepath.Base(target), nil
}
