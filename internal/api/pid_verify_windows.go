//go:build windows

package api

import (
	"strings"

	"golang.org/x/sys/windows"
)

// verifyClaudeExePid returns true when pid refers to a process whose image
// name ends with "claude.exe" (case-insensitive). Returns false when:
//   - OpenProcess fails (PID doesn't exist or insufficient permissions)
//   - QueryFullProcessImageName fails
//   - The image name does not end with "claude.exe"
//
// This closes the /register-pid PID-spoofing vector (thinkdeep finding E-1):
// an on-host attacker with the Bearer token could register an arbitrary user-
// owned process's PID so the daemon kills it on the next operator unfreeze
// click. Requiring the PID to actually refer to claude.exe limits the damage
// surface to processes the attacker could already kill with their own creds.
//
// The check is intentionally narrow — it verifies only that the image name
// ends with "claude.exe", not the full path (which could be any location) and
// not that it's claude.exe's specific version. This is sufficient to close the
// spoofing vector without over-constraining future deployments.
func verifyClaudeExePid(pid uint32) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h) //nolint:errcheck

	var buf [windows.MAX_PATH]uint16
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return false
	}
	name := windows.UTF16ToString(buf[:size])
	return strings.HasSuffix(strings.ToLower(name), "claude.exe")
}
