package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"mcp-gateway/internal/pidfile"
)

// daemonExeBasenames lists the basenames killProcessByPID will accept as
// "this is the mcp-gateway daemon". Includes the .exe variant for Windows
// builds; the platform-specific verifyExeBasename returns the OS-native
// basename (e.g. "mcp-gateway.exe" on Windows, "mcp-gateway" on Linux).
//
// Comparison is case-insensitive on Windows because filesystems there are
// case-insensitive — a daemon installed as "MCP-Gateway.exe" by an
// installer is still the daemon. Linux comparison is case-sensitive per
// POSIX semantics.
var daemonExeBasenames = []string{"mcp-gateway", "mcp-gateway.exe"}

// killByPIDFile is the shared logic: read the PID file, verify the process
// at that PID is the mcp-gateway daemon, then invoke killFn(pid). On
// success the PID file is removed so the next daemon-status probe sees a
// clean slate.
//
// AUDIT B-NEW-21 (Phase 9): the verification step guards against PID
// recycling. On Windows in particular, PIDs are recycled aggressively —
// the time between daemon crash, mcp-ctl reading the pidfile, and mcp-ctl
// invoking Kill is enough that the recycled PID may belong to a
// completely unrelated process (a build step, a browser tab worker).
// verifyExeBasename returning anything other than "mcp-gateway[.exe]"
// causes a refuse-to-kill — preserving the stale pidfile so the operator
// notices.
// The `_` deadline param is preserved in the signature for callers in
// daemon_spawn_other.go and daemon_spawn_windows.go that thread the kill
// deadline through to their own `killFn` closures (POSIX uses it for the
// SIGTERM-then-SIGKILL escalation window). killByPIDFile itself does not
// consume it directly.
func killByPIDFile(_ time.Time, killFn func(int) error) error { //nolint:unparam — see comment above
	pidPath := pidfile.DefaultPath()
	pid, err := pidfile.Read(pidPath)
	if err != nil {
		// PID file missing or unreadable — daemon may already be gone.
		return fmt.Errorf("cannot find daemon PID: %w", err)
	}
	basename, verifyErr := verifyExeBasenameFunc(pid)
	if verifyErr != nil {
		// Refuse to kill on lookup failure (PID gone, access denied,
		// macOS unsupported, etc.). Safer than guessing.
		fmt.Fprintf(os.Stderr,
			"mcp-ctl: refusing to kill PID %d — could not verify executable: %v\n",
			pid, verifyErr,
		)
		return fmt.Errorf("verify daemon (pid %d): %w", pid, verifyErr)
	}
	if !isDaemonBasename(basename) {
		fmt.Fprintf(os.Stderr,
			"mcp-ctl: refusing to kill PID %d — process is %q, not mcp-gateway. "+
				"Stale pidfile at %s may need manual cleanup.\n",
			pid, basename, pidPath,
		)
		return fmt.Errorf("pid %d belongs to %q, not mcp-gateway", pid, basename)
	}
	if err := killFn(pid); err != nil {
		return fmt.Errorf("kill daemon (pid %d): %w", pid, err)
	}
	// Successful kill — remove the pidfile so daemon-status doesn't
	// see a stale entry. Best-effort: pidfile.Remove tolerates ENOENT.
	if rmErr := pidfile.Remove(pidPath); rmErr != nil {
		fmt.Fprintf(os.Stderr,
			"mcp-ctl: warning — could not remove pidfile after kill: %v\n",
			rmErr,
		)
	}
	return nil
}

// killProcessByPID forcefully terminates the process with the given PID.
// Identity verification happens upstream in killByPIDFile so this remains
// a thin wrapper around os.Process.Kill — keeping the platform-specific
// SIGTERM-then-SIGKILL escalation logic in daemon_spawn_other.go
// undisturbed.
func killProcessByPID(pid int) error {
	proc, err := findProcess(pid)
	if err != nil {
		return err
	}
	return proc.Kill()
}

// isDaemonBasename reports whether basename matches one of the accepted
// mcp-gateway executable names. Case-insensitive — Windows filesystems
// are case-insensitive, and Linux daemons might be packaged with varying
// casing without semantic difference.
func isDaemonBasename(basename string) bool {
	if basename == "" {
		return false
	}
	// Strip any directory prefix that might have leaked through (defensive).
	basename = filepath.Base(basename)
	lower := strings.ToLower(basename)
	return slices.Contains(daemonExeBasenames, lower)
}

// findProcess wraps os.FindProcess. Exposed as a `var` so tests can
// inject a fake process whose Kill() is observable.
var findProcess = func(pid int) (*os.Process, error) {
	return os.FindProcess(pid)
}

// verifyExeBasenameFunc indirects through `var` so tests can replace the
// platform-specific verifier without spawning real processes. Production
// callers get the OS-native implementation (Windows: QueryFullProcessImageName,
// Linux: /proc/<pid>/exe, other: refuse).
var verifyExeBasenameFunc = verifyExeBasename
