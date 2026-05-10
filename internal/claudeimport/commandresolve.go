// Package claudeimport implements the Import-from-Claude apply path.
//
// Three concerns live here:
//
//   - Apply (apply.go) — copy/move semantics with conflict policy
//     (skip/overwrite) and a `move`-time mtime-CAS write to the source
//     file when the entry must be deleted from origin.
//   - Diff (diff.go) — compare a candidate import row against the
//     daemon's current state and surface drift_fields when local
//     edits would be discarded by an overwrite.
//   - Command resolution (commandresolve.go) — turn a relative
//     command name like `npx`/`uvx`/`node` into an absolute path so the
//     daemon's stdio launcher does not depend on the user's PATH at
//     run-time. The user's PATH at install time wins.
//   - Provenance (provenance.go) — append/read a sidecar
//     ~/.mcp-gateway/claude-imported.json so the snapshot endpoint can
//     surface a "previously imported" badge on rows the user has
//     already imported once. Atomic writes via CreateTemp + rename.
package claudeimport

import (
	"os/exec"
	"runtime"
	"strings"
)

// CommandResolution is the result of resolveCommand: the canonical
// command name (lowercased basename) plus the absolute path the daemon
// should launch.
type CommandResolution struct {
	// Name is the canonical lowercased basename used by callers to
	// route into per-binary policy (e.g. "uvx" vs "npx").
	Name string
	// AbsPath is the resolved absolute filesystem path. Empty when
	// resolution failed; the caller's apply path falls back to the
	// raw command string and lets the launcher's own PATH lookup
	// produce a runtime error.
	AbsPath string
	// Resolved indicates whether AbsPath is populated.
	Resolved bool
}

// resolveCommand turns a command string from a Claude Code config
// (typically just "npx", "uvx", "node", or a path "C:\\Program Files\\…")
// into an absolute path the daemon can spawn directly.
//
// Resolution order:
//  1. If command is already absolute and exists → use as-is.
//  2. If command is relative, scan PATH via os/exec.LookPath.
//  3. If neither succeeds → mark Resolved=false; AbsPath empty.
//
// Per spike §4.3: the user's PATH at the time of import is the
// authoritative reference. The daemon does NOT re-resolve at run time
// because PATH may differ when the daemon runs as a service vs the
// user's interactive shell.
func ResolveCommand(command string) CommandResolution {
	command = strings.TrimSpace(command)
	if command == "" {
		return CommandResolution{}
	}

	// On Windows the user may have written `node.exe` or just `node`.
	// LookPath canonicalises this for us.
	abs, err := exec.LookPath(command)
	if err != nil {
		return CommandResolution{
			Name:     baseName(command),
			Resolved: false,
		}
	}
	return CommandResolution{
		Name:     baseName(abs),
		AbsPath:  abs,
		Resolved: true,
	}
}

// baseName extracts the lowercased base name without an .exe suffix
// for canonical comparison ("npx" matches both "/usr/local/bin/npx" and
// "C:\\Program Files\\nodejs\\npx.cmd").
func baseName(command string) string {
	name := command
	// Strip directory prefix.
	if idx := lastPathSep(name); idx >= 0 {
		name = name[idx+1:]
	}
	// Strip extension on Windows.
	if runtime.GOOS == "windows" {
		for _, ext := range []string{".exe", ".cmd", ".bat"} {
			if strings.HasSuffix(strings.ToLower(name), ext) {
				name = name[:len(name)-len(ext)]
				break
			}
		}
	}
	return strings.ToLower(name)
}

// lastPathSep returns the index of the last path separator (/ or \) in
// s, or -1 if none. Both are checked because Windows paths may use
// either depending on origin.
func lastPathSep(s string) int {
	idx := -1
	for i := len(s) - 1; i >= 0; i-- {
		c := s[i]
		if c == '/' || c == '\\' {
			idx = i
			break
		}
	}
	return idx
}
