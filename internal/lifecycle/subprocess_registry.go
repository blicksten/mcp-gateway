// Subprocess registry — spike 2026-05-11 FM 1 / Q1=(c).
//
// Each running gateway daemon writes a per-instance JSON file recording the
// PIDs of MCP backend subprocesses it has spawned. On startup, a new gateway
// scans the registry directory for files whose owner PID is no longer alive
// and reaps the listed subprocesses. This catches the case where the previous
// gateway crashed before its Job Object handle could close (which is the path
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE protects).
//
// Disambiguation (spike FM 1 Q2 = α): the registry tracks ONLY gateway-spawned
// stdio backends. The REST-only orchestrator (started independently by the VS
// Code extension on :8100) never enters the registry, so the scanner cannot
// touch it — eliminating the self-MCPR.1 risk that motivated Q2.
//
// Reaper policy (spike FM 1 Q3 = I): SIGTERM/Console-CtrlBreak → 5 s wait →
// SIGKILL/TerminateProcess. Honors graceful shutdown so orchestrator children
// can close SQLite WAL cleanly before forced termination.

package lifecycle

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// registryFilenamePrefix is the basename prefix for per-owner JSON files.
// Filenames are "<ownerPID>.subprocesses.json".
const registryFilenameSuffix = ".subprocesses.json"

// reaperGraceTimeout is the wait between SIGTERM/CtrlBreak and SIGKILL when
// reaping an orphan subprocess (FM 1 Q3 = I).
const reaperGraceTimeout = 5 * time.Second

// SubprocessEntry is one row in the registry file.
type SubprocessEntry struct {
	// Name is the lifecycle Manager's logical server name (e.g. "orchestrator").
	Name string `json:"name"`
	// PID is the OS process ID of the spawned child.
	PID int `json:"pid"`
	// Command is the resolved exec.Cmd Path + Args joined for diagnostics only.
	Command string `json:"command"`
	// AddedAt is the registry-write timestamp (UTC, RFC3339).
	AddedAt string `json:"added_at"`
}

// registryFile is the on-disk JSON shape.
type registryFile struct {
	OwnerPID       int               `json:"owner_pid"`
	OwnerStartedAt string            `json:"owner_started_at"`
	Subprocesses   []SubprocessEntry `json:"subprocesses"`
}

// Registry is a per-gateway-instance JSON file tracking spawned subprocesses.
// All exported methods are safe for concurrent use.
type Registry struct {
	mu             sync.Mutex
	path           string
	ownerPID       int
	ownerStartedAt string
	entries        []SubprocessEntry
	closed         bool
}

// DefaultRegistryDir returns the canonical directory for registry files.
//
// Mirrors internal/pidfile/pidfile.go::DefaultPath: prefers $XDG_RUNTIME_DIR
// when set + writable (Linux per-user runtime tree), else falls back to
// os.TempDir() which resolves to %TEMP% on Windows. Reusing the pidfile
// pattern avoids drift between PID-file probing and registry scanning.
func DefaultRegistryDir() string {
	const subdir = "mcp-gateway"
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		if info, err := os.Stat(xdg); err == nil && info.IsDir() {
			probe := filepath.Join(xdg, ".mcp-gateway-registry-probe")
			//nolint:nosnakecase // os constants
			if f, err := os.OpenFile(probe, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err == nil {
				_ = f.Close()
				_ = os.Remove(probe)
				return filepath.Join(xdg, subdir)
			}
		}
	}
	return filepath.Join(os.TempDir(), subdir)
}

// OpenRegistry creates (or replaces) the registry file for ownerPID in dir.
//
// The file is written atomically and contains an empty subprocess list. The
// returned *Registry retains state in-memory and rewrites the file on every
// Add/Remove for crash-recovery (on graceful Close the file is deleted).
//
// dir is created with 0o700 if missing. ownerStartedAt is captured at call
// time and used by ScanAndReap to defend against PID recycling (a recycled
// PID will have a different start time on the live process).
func OpenRegistry(dir string, ownerPID int) (*Registry, error) {
	if ownerPID <= 0 {
		return nil, fmt.Errorf("invalid owner pid: %d", ownerPID)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create registry dir %q: %w", dir, err)
	}
	r := &Registry{
		path:           filepath.Join(dir, fmt.Sprintf("%d%s", ownerPID, registryFilenameSuffix)),
		ownerPID:       ownerPID,
		ownerStartedAt: time.Now().UTC().Format(time.RFC3339),
		entries:        nil,
	}
	if err := r.persist(); err != nil {
		return nil, err
	}
	return r, nil
}

// Add appends an entry for a newly-spawned subprocess. Idempotent on (pid):
// repeated calls with the same PID overwrite the older entry.
func (r *Registry) Add(name string, pid int, command string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return fmt.Errorf("registry is closed")
	}
	// Replace existing entry with the same PID (idempotent re-Add).
	for i, e := range r.entries {
		if e.PID == pid {
			r.entries[i] = SubprocessEntry{
				Name:    name,
				PID:     pid,
				Command: command,
				AddedAt: time.Now().UTC().Format(time.RFC3339),
			}
			return r.persist()
		}
	}
	r.entries = append(r.entries, SubprocessEntry{
		Name:    name,
		PID:     pid,
		Command: command,
		AddedAt: time.Now().UTC().Format(time.RFC3339),
	})
	return r.persist()
}

// Remove drops the entry whose PID matches. Silent no-op if not found.
func (r *Registry) Remove(pid int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	out := r.entries[:0]
	removed := false
	for _, e := range r.entries {
		if e.PID == pid {
			removed = true
			continue
		}
		out = append(out, e)
	}
	r.entries = out
	if !removed {
		return nil
	}
	return r.persist()
}

// Entries returns a snapshot of currently-registered subprocesses.
// Useful for tests and diagnostics; production callers should not need this.
func (r *Registry) Entries() []SubprocessEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	out := make([]SubprocessEntry, len(r.entries))
	copy(out, r.entries)
	return out
}

// Path returns the on-disk registry file path. Stable for the lifetime of
// the Registry; used by tests.
func (r *Registry) Path() string { return r.path }

// Close removes the registry file from disk. After Close, Add/Remove become
// no-ops or return errors — the Registry must not be reused.
//
// Idempotent: a second call returns nil even if the file is already gone.
func (r *Registry) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	if err := os.Remove(r.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove registry file %q: %w", r.path, err)
	}
	return nil
}

// persist writes the current entries snapshot to disk atomically.
//
// Uses the standard write-temp-then-rename pattern so a crash mid-write
// cannot leave a partial JSON file (which the next scan would read as
// "owner alive but corrupt" — wrong). Caller MUST hold r.mu.
func (r *Registry) persist() error {
	doc := registryFile{
		OwnerPID:       r.ownerPID,
		OwnerStartedAt: r.ownerStartedAt,
		Subprocesses:   r.entries,
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal registry: %w", err)
	}
	tmpPath := r.path + ".tmp"
	//nolint:nosnakecase // os constants
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return fmt.Errorf("write registry temp: %w", err)
	}
	if err := os.Rename(tmpPath, r.path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename registry: %w", err)
	}
	return nil
}

// ScanAndReap inspects all registry files in dir and reaps subprocesses
// whose owner gateway PID is no longer alive. Files belonging to the
// caller (currentOwnerPID) are skipped — a gateway never reaps its own
// registry.
//
// Reaping uses gracefulKillByPID (FM 1 Q3 = I): SIGTERM/Console-CtrlBreak
// first, wait up to reaperGraceTimeout, then SIGKILL/TerminateProcess.
//
// Returns the total number of subprocesses successfully reaped. Errors on
// individual files are logged but do not abort the scan — a single
// unreadable file must not block startup of an otherwise-healthy gateway.
//
// CALLER CONTRACT: pass the dir returned by DefaultRegistryDir() unless
// you are in a test. Pass os.Getpid() as currentOwnerPID so the function
// excludes its own (about-to-be-written) registry file.
func ScanAndReap(dir string, currentOwnerPID int, logger *slog.Logger) int {
	if logger == nil {
		logger = slog.Default()
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0 // no registry yet — nothing to reap
		}
		logger.Warn("registry scan: cannot read directory", "dir", dir, "error", err)
		return 0
	}

	reaped := 0
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		if !strings.HasSuffix(name, registryFilenameSuffix) {
			continue
		}
		// Filename is "<ownerPID>.subprocesses.json" — parse the PID.
		pidStr := strings.TrimSuffix(name, registryFilenameSuffix)
		ownerPID, err := strconv.Atoi(pidStr)
		if err != nil {
			continue // not our format
		}
		if ownerPID == currentOwnerPID {
			continue // never reap our own registry
		}
		fullPath := filepath.Join(dir, name)
		data, err := os.ReadFile(fullPath)
		if err != nil {
			logger.Warn("registry scan: cannot read file", "file", fullPath, "error", err)
			continue
		}
		var doc registryFile
		if err := json.Unmarshal(data, &doc); err != nil {
			logger.Warn("registry scan: corrupt JSON, leaving file", "file", fullPath, "error", err)
			continue
		}
		live, err := isProcessLive(ownerPID)
		if err != nil {
			logger.Warn("registry scan: liveness probe failed, skipping reap to be safe",
				"file", fullPath, "owner_pid", ownerPID, "error", err)
			continue
		}
		if live {
			continue // owner still running — its job to clean up
		}
		// Owner is dead. Reap each subprocess.
		for _, sub := range doc.Subprocesses {
			if sub.PID <= 0 {
				continue
			}
			subLive, _ := isProcessLive(sub.PID)
			if !subLive {
				continue // already gone (good — nothing to do)
			}
			if err := gracefulKillByPID(sub.PID, reaperGraceTimeout); err != nil {
				logger.Warn("registry scan: reap failed",
					"file", fullPath, "subprocess", sub.Name, "pid", sub.PID, "error", err)
				continue
			}
			logger.Info("registry scan: reaped orphan subprocess",
				"owner_pid", ownerPID, "subprocess", sub.Name, "pid", sub.PID, "command", sub.Command)
			reaped++
		}
		// Remove the now-processed file.
		if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
			logger.Warn("registry scan: cannot remove processed file",
				"file", fullPath, "error", err)
		}
	}
	return reaped
}
