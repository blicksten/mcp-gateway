// Tests for subprocess registry (spike 2026-05-11 FM 1).

package lifecycle

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// quietLogger discards all output so tests stay readable.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{}))
}

// TestRegistry_AddRemoveClose covers the happy path: open a registry, add
// two entries, remove one, close. File contents must match expectations at
// every step.
func TestRegistry_AddRemoveClose(t *testing.T) {
	dir := t.TempDir()
	r, err := OpenRegistry(dir, 12345)
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}

	// File should exist with empty subprocess list.
	expectedPath := filepath.Join(dir, "12345.subprocesses.json")
	if r.Path() != expectedPath {
		t.Fatalf("Path() = %q, want %q", r.Path(), expectedPath)
	}
	if _, err := os.Stat(expectedPath); err != nil {
		t.Fatalf("registry file not created: %v", err)
	}
	doc := readRegistryFile(t, expectedPath)
	if doc.OwnerPID != 12345 {
		t.Fatalf("owner pid = %d, want 12345", doc.OwnerPID)
	}
	if len(doc.Subprocesses) != 0 {
		t.Fatalf("empty registry expected, got %d entries", len(doc.Subprocesses))
	}

	// Add two.
	if err := r.Add("orchestrator", 100, "python server.py"); err != nil {
		t.Fatalf("Add 1: %v", err)
	}
	if err := r.Add("pal", 101, "python pal/server.py"); err != nil {
		t.Fatalf("Add 2: %v", err)
	}
	doc = readRegistryFile(t, expectedPath)
	if len(doc.Subprocesses) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(doc.Subprocesses))
	}

	// Remove one.
	if err := r.Remove(100); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	doc = readRegistryFile(t, expectedPath)
	if len(doc.Subprocesses) != 1 || doc.Subprocesses[0].PID != 101 {
		t.Fatalf("after Remove, expected only pid 101; got %+v", doc.Subprocesses)
	}

	// Close deletes the file. Second Close is a no-op.
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(expectedPath); !os.IsNotExist(err) {
		t.Fatalf("file should be gone after Close, stat err = %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("second Close should be no-op: %v", err)
	}

	// Add after Close errors.
	if err := r.Add("late", 200, "x"); err == nil {
		t.Fatalf("Add after Close should error")
	}
}

// TestRegistry_AddIdempotentOnPID confirms re-adding the same PID overwrites
// the older entry rather than creating duplicates.
func TestRegistry_AddIdempotentOnPID(t *testing.T) {
	dir := t.TempDir()
	r, err := OpenRegistry(dir, 9000)
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	defer r.Close()

	if err := r.Add("old-name", 500, "old cmd"); err != nil {
		t.Fatalf("Add 1: %v", err)
	}
	if err := r.Add("new-name", 500, "new cmd"); err != nil {
		t.Fatalf("Add 2: %v", err)
	}
	entries := r.Entries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Name != "new-name" || entries[0].Command != "new cmd" {
		t.Fatalf("expected overwrite by latest, got %+v", entries[0])
	}
}

// TestScanAndReap_NoOpWhenNothingToReap covers the dominant path: only the
// current gateway's registry file exists, and it owns no orphans.
func TestScanAndReap_NoOpWhenNothingToReap(t *testing.T) {
	dir := t.TempDir()
	r, err := OpenRegistry(dir, os.Getpid())
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	defer r.Close()

	reaped := ScanAndReap(dir, os.Getpid(), quietLogger())
	if reaped != 0 {
		t.Fatalf("expected 0 reaped, got %d", reaped)
	}
	// Our own file must still be there.
	if _, err := os.Stat(r.Path()); err != nil {
		t.Fatalf("own registry file should remain: %v", err)
	}
}

// TestScanAndReap_SkipsLiveOwner asserts ScanAndReap leaves alone any file
// whose owner PID is still alive (matches the "another gateway is up"
// scenario).
func TestScanAndReap_SkipsLiveOwner(t *testing.T) {
	dir := t.TempDir()
	// Use a long-lived helper process as the live owner.
	// `sleep 60` on Unix, `timeout 60` on Windows.
	helper := startSleepHelper(t)
	defer helper.kill()

	// Write a fake registry file as if helper owned subprocess pid 1.
	fakePath := filepath.Join(dir, strconv.Itoa(helper.pid)+".subprocesses.json")
	doc := registryFile{
		OwnerPID:       helper.pid,
		OwnerStartedAt: time.Now().UTC().Format(time.RFC3339),
		Subprocesses: []SubprocessEntry{
			{Name: "fake-sub", PID: 99998, Command: "n/a", AddedAt: time.Now().Format(time.RFC3339)},
		},
	}
	writeJSON(t, fakePath, doc)

	reaped := ScanAndReap(dir, os.Getpid(), quietLogger())
	if reaped != 0 {
		t.Fatalf("expected 0 reaped (owner live), got %d", reaped)
	}
	if _, err := os.Stat(fakePath); err != nil {
		t.Fatalf("file should remain when owner is live: %v", err)
	}
}

// TestScanAndReap_ReapsOrphanFromDeadOwner exercises the FM 1 happy path:
// a registry file lists a live subprocess whose owner gateway is dead.
// ScanAndReap must (a) kill the subprocess, (b) delete the registry file,
// and (c) return reaped count = 1.
func TestScanAndReap_ReapsOrphanFromDeadOwner(t *testing.T) {
	dir := t.TempDir()
	// Spawn an orphan helper that will be reaped.
	orphan := startSleepHelper(t)
	defer orphan.kill() // safety net; ScanAndReap should kill first

	// Write a registry file whose owner_pid is guaranteed not-alive.
	// Pick a PID far above typical max — Linux pid_max default is ~32768,
	// Windows allows up to ~67M but recycles quickly; 999_999_998 is a
	// safe synthetic "not alive" value for tests.
	deadOwner := 999_999_998
	fakePath := filepath.Join(dir, strconv.Itoa(deadOwner)+".subprocesses.json")
	doc := registryFile{
		OwnerPID:       deadOwner,
		OwnerStartedAt: time.Now().UTC().Format(time.RFC3339),
		Subprocesses: []SubprocessEntry{
			{Name: "orphan-helper", PID: orphan.pid, Command: "helper", AddedAt: time.Now().Format(time.RFC3339)},
		},
	}
	writeJSON(t, fakePath, doc)

	reaped := ScanAndReap(dir, os.Getpid(), quietLogger())
	if reaped != 1 {
		t.Fatalf("expected 1 reaped, got %d", reaped)
	}
	// Orphan must actually be dead.
	// Give the OS a beat to reap on Windows (TerminateProcess is async wrt
	// the process's appearance in OpenProcess).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		live, err := isProcessLive(orphan.pid)
		if err == nil && !live {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	live, err := isProcessLive(orphan.pid)
	if err != nil {
		t.Fatalf("isProcessLive: %v", err)
	}
	if live {
		t.Fatalf("orphan pid %d still alive after reap", orphan.pid)
	}
	// File must be removed.
	if _, err := os.Stat(fakePath); !os.IsNotExist(err) {
		t.Fatalf("file should be removed after reap, stat err = %v", err)
	}
}

// TestScanAndReap_SkipsOwnFile asserts the scanner never reaps subprocesses
// listed under the caller's own gateway PID — even if the file's contents
// would otherwise qualify.
func TestScanAndReap_SkipsOwnFile(t *testing.T) {
	dir := t.TempDir()
	r, err := OpenRegistry(dir, os.Getpid())
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	defer r.Close()
	// Inject a fake entry with a PID we'd otherwise reap.
	if err := r.Add("fake", 1, "init"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	reaped := ScanAndReap(dir, os.Getpid(), quietLogger())
	if reaped != 0 {
		t.Fatalf("scanner must not touch its own file; got reaped=%d", reaped)
	}
}

// TestScanAndReap_IgnoresCorruptFile verifies a broken JSON file does not
// abort the scan or corrupt accounting — it is logged + skipped.
func TestScanAndReap_IgnoresCorruptFile(t *testing.T) {
	dir := t.TempDir()
	corrupt := filepath.Join(dir, "999999.subprocesses.json")
	if err := os.WriteFile(corrupt, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}
	reaped := ScanAndReap(dir, os.Getpid(), quietLogger())
	if reaped != 0 {
		t.Fatalf("expected 0 reaped with only corrupt file, got %d", reaped)
	}
	// Corrupt file is left in place — manual inspection allowed.
	if _, err := os.Stat(corrupt); err != nil {
		t.Fatalf("corrupt file should remain for manual inspection, stat err = %v", err)
	}
}

// TestDefaultRegistryDir_NonEmpty smoke-tests path resolution. The exact
// directory is platform/env-dependent; we only assert it is non-empty and
// ends with "mcp-gateway".
func TestDefaultRegistryDir_NonEmpty(t *testing.T) {
	dir := DefaultRegistryDir()
	if dir == "" {
		t.Fatalf("DefaultRegistryDir returned empty string")
	}
	if filepath.Base(dir) != "mcp-gateway" {
		t.Fatalf("expected basename mcp-gateway, got %q", dir)
	}
}

// --- helpers ---

func readRegistryFile(t *testing.T, path string) registryFile {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc registryFile
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return doc
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

type sleepHelper struct {
	cmd *exec.Cmd
	pid int
}

func (h *sleepHelper) kill() {
	if h == nil || h.cmd == nil || h.cmd.Process == nil {
		return
	}
	_ = h.cmd.Process.Kill()
	_, _ = h.cmd.Process.Wait()
}

// startSleepHelper spawns a long-lived child process and returns its PID.
// Used to simulate live owners + live orphan subprocesses in tests. The
// child runs "sleep 60" on Unix and "timeout /t 60 /nobreak" on Windows.
func startSleepHelper(t *testing.T) *sleepHelper {
	t.Helper()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		// timeout.exe writes a tick to stdout; redirect to /dev/null analog.
		cmd = exec.Command("timeout", "/t", "60", "/nobreak")
	} else {
		cmd = exec.Command("sleep", "60")
	}
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Skipf("could not spawn sleep helper: %v", err)
	}
	return &sleepHelper{cmd: cmd, pid: cmd.Process.Pid}
}

// TestIsProcessLive_OnSelf trivially asserts os.Getpid() is "live".
func TestIsProcessLive_OnSelf(t *testing.T) {
	live, err := isProcessLive(os.Getpid())
	if err != nil {
		t.Fatalf("isProcessLive(self): %v", err)
	}
	if !live {
		t.Fatalf("isProcessLive(self) = false; want true")
	}
}

// TestIsProcessLive_OnDeadPID asserts an obviously-not-running PID is "not
// live". 999_999_999 is above typical max PID on Linux + Windows.
func TestIsProcessLive_OnDeadPID(t *testing.T) {
	live, err := isProcessLive(999_999_999)
	if err != nil {
		t.Skipf("isProcessLive returned err on synthetic PID (env-dependent): %v", err)
	}
	if live {
		t.Fatalf("isProcessLive(999999999) = true; want false")
	}
}

// TestGracefulKillByPID_KillsLiveHelper smoke-tests the kill wrapper by
// spawning a sleeper, sending the graceful kill, and confirming exit.
func TestGracefulKillByPID_KillsLiveHelper(t *testing.T) {
	helper := startSleepHelper(t)
	defer helper.kill() // safety net

	// Use a short grace so the SIGKILL/TerminateProcess path is exercised
	// (the sleep helper ignores SIGTERM on some Windows builds).
	if err := gracefulKillByPID(helper.pid, 500*time.Millisecond); err != nil {
		t.Fatalf("gracefulKillByPID: %v", err)
	}
	// Allow the OS to finalize.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		live, err := isProcessLive(helper.pid)
		if err == nil && !live {
			return // success
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("helper pid %d still alive after gracefulKillByPID", helper.pid)
}
