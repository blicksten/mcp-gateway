// Tests for `mcp-ctl marketplace cleanup` (spike 2026-05-11 FM 6).

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestScanStaleBak_HappyPath: a 30-day-old .bak is selected, a fresh .bak
// is left alone, and a non-.bak directory is ignored entirely.
func TestScanStaleBak_HappyPath(t *testing.T) {
	root := t.TempDir()
	makeDir(t, root, "live")            // not .bak — never a candidate
	stale := makeDir(t, root, "live.bak")
	fresh := makeDir(t, root, "other.bak")
	makeFile(t, root, "stray.bak.txt") // file with .bak in name — must be ignored
	thirtyDaysAgo := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(stale, thirtyDaysAgo, thirtyDaysAgo); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	// fresh dir keeps its default mtime (now-ish) -> not stale at 7-day cutoff

	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	report, err := scanStaleBak(root, cutoff)
	if err != nil {
		t.Fatalf("scanStaleBak: %v", err)
	}
	if len(report) != 1 {
		t.Fatalf("expected 1 stale entry, got %d: %+v", len(report), report)
	}
	if report[0].path != stale {
		t.Fatalf("expected %q, got %q", stale, report[0].path)
	}
	// Confirm the fresh dir is intact on disk — scan must not modify state.
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh dir should still exist after scan: %v", err)
	}
}

// TestScanStaleBak_MissingRoot returns no error so the cleanup command can
// run on a host that has never installed any Claude marketplace.
func TestScanStaleBak_MissingRoot(t *testing.T) {
	bogus := filepath.Join(t.TempDir(), "definitely-not-here")
	report, err := scanStaleBak(bogus, time.Now())
	if err != nil {
		t.Fatalf("missing root must be a no-op: %v", err)
	}
	if len(report) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(report))
	}
}

// TestScanStaleBak_StrictCutoff: a directory with mtime exactly at cutoff
// is selected (cutoff is inclusive — `After` is strict).
func TestScanStaleBak_StrictCutoff(t *testing.T) {
	root := t.TempDir()
	exact := makeDir(t, root, "exact.bak")
	cutoff := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(exact, cutoff, cutoff); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	report, err := scanStaleBak(root, cutoff)
	if err != nil {
		t.Fatalf("scanStaleBak: %v", err)
	}
	if len(report) != 1 {
		t.Fatalf("dir at exactly cutoff time must be eligible; got %d entries", len(report))
	}
}

// TestScanStaleBak_DeterministicSort: multiple candidates come back sorted
// by path so dry-run output is stable across runs.
func TestScanStaleBak_DeterministicSort(t *testing.T) {
	root := t.TempDir()
	names := []string{"zeta.bak", "alpha.bak", "midd.bak"}
	mt := time.Now().Add(-30 * 24 * time.Hour)
	for _, n := range names {
		p := makeDir(t, root, n)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatalf("Chtimes: %v", err)
		}
	}
	report, err := scanStaleBak(root, time.Now())
	if err != nil {
		t.Fatalf("scanStaleBak: %v", err)
	}
	if len(report) != 3 {
		t.Fatalf("expected 3, got %d", len(report))
	}
	for i := 1; i < len(report); i++ {
		if report[i-1].path > report[i].path {
			t.Fatalf("not sorted: %q !< %q", report[i-1].path, report[i].path)
		}
	}
}

// TestMarketplaceCleanupCmd_DryRun: end-to-end through cobra exercises the
// command tree wiring + flag parsing + no-side-effect dry-run path.
func TestMarketplaceCleanupCmd_DryRun(t *testing.T) {
	root := t.TempDir()
	stale := makeDir(t, root, "old.bak")
	mt := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(stale, mt, mt); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	rootCmd := newRootCmd()
	out := &strings.Builder{}
	rootCmd.SetOut(out)
	rootCmd.SetErr(out)
	rootCmd.SetArgs([]string{
		"marketplace", "cleanup",
		"--dry-run",
		"--max-age=7",
		"--root", root,
	})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "Would remove 1 stale .bak directory") {
		t.Fatalf("dry-run header missing in output: %s", got)
	}
	if !strings.Contains(got, "Dry run — no files were touched") {
		t.Fatalf("dry-run footer missing in output: %s", got)
	}
	// Confirm the directory is still on disk.
	if _, err := os.Stat(stale); err != nil {
		t.Fatalf("dry-run must not delete; stat err = %v", err)
	}
}

// TestMarketplaceCleanupCmd_RemovesStale: real removal path.
func TestMarketplaceCleanupCmd_RemovesStale(t *testing.T) {
	root := t.TempDir()
	stale := makeDir(t, root, "purge.bak")
	mt := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(stale, mt, mt); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	// Drop a file inside so we exercise the recursive remove path.
	makeFile(t, stale, "leftover.json")

	rootCmd := newRootCmd()
	out := &strings.Builder{}
	rootCmd.SetOut(out)
	rootCmd.SetErr(out)
	rootCmd.SetArgs([]string{
		"marketplace", "cleanup",
		"--max-age=7",
		"--root", root,
	})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale dir should be gone; stat err = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "Removed 1 of 1.") {
		t.Fatalf("removal summary missing: %s", got)
	}
}

// TestMarketplaceCleanupCmd_NoOpOnEmpty exercises the dominant happy path:
// the root has no .bak directories at all.
func TestMarketplaceCleanupCmd_NoOpOnEmpty(t *testing.T) {
	root := t.TempDir()
	makeDir(t, root, "claude-plugins-official") // live tree, not a .bak

	rootCmd := newRootCmd()
	out := &strings.Builder{}
	rootCmd.SetOut(out)
	rootCmd.SetErr(out)
	rootCmd.SetArgs([]string{"marketplace", "cleanup", "--root", root})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out.String(), "No stale .bak directories") {
		t.Fatalf("expected no-op message; got %s", out.String())
	}
}

// --- helpers ---

func makeDir(t *testing.T, parent, name string) string {
	t.Helper()
	p := filepath.Join(parent, name)
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", p, err)
	}
	return p
}

func makeFile(t *testing.T, parent, name string) string {
	t.Helper()
	p := filepath.Join(parent, name)
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}
