//go:build !windows && !linux

package main

import "errors"

// errVerifyUnsupported is returned by verifyExeBasename on platforms
// where the audit-dashboard B-NEW-21 verification has no implementation
// (currently macOS, BSD, and Solaris). Callers MUST refuse the kill on
// this error — refusing to kill a recycled-PID stranger is strictly
// safer than guessing.
var errVerifyUnsupported = errors.New(
	"daemon-kill: PID-owner verification not implemented on this platform — refusing kill",
)

// verifyExeBasename is a conservative fallback for non-Windows, non-Linux
// systems. Production deployment targets are Linux and Windows; macOS
// developer workstations get a clear error and the operator has to
// remove the stale pidfile by hand. This is documented per
// docs/PLAN-audit-dashboard.md Phase 9 ("On lookup failure, refuse + log").
func verifyExeBasename(_ int) (string, error) {
	return "", errVerifyUnsupported
}
