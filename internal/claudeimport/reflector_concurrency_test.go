package claudeimport

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mcp-gateway/internal/claudeconfig"
)

// TestApply_Move_ConcurrentExternalWriter_MTimeCASCatches encodes the
// T-D.4 / R-31 invariant: when a `move` apply races a concurrent
// external writer (e.g. Claude Code itself, or the TS-side
// claudeConfigSync reflector tick), the mtime-CAS check inside
// mutateSourceRemove aborts the daemon's write rather than clobbering
// the external mutation.
//
// Architectural reality (Phase D vs spike §4.7):
//
//   - The plan presupposed a Go-side `internal/api/claude_config_sync.go`
//     with `Pause()` / `Resume()` primitives. That file does NOT exist;
//     the reflector lives entirely on the TypeScript side at
//     `vscode/mcp-gateway-dashboard/src/claude-config-sync.ts`.
//   - The TS reflector already implements CAS-style sha256 retry over
//     the raw text (see CAS_RETRY_BUDGET=5 in claude-config-sync.ts).
//     Concurrent writes from the daemon's import-apply path are caught
//     by the reflector's own retry logic — when the daemon writes
//     between the reflector's read and rename, the reflector's hash
//     mismatch triggers a retry.
//   - The Go-side equivalent is THIS test: prove that mutateSourceRemove
//     itself does not silently lose work when an external writer
//     mutates the source file between the daemon's re-read and rename.
//
// Per spike §4.7 fallback note: "if pause introduces deadlock with the
// daemon's existing watcher, switch to content-fingerprint over
// mcpServers value (NOT file mtime)". This test pins the file-mtime
// path which is the simpler and currently-shipping check; if the
// fingerprint upgrade lands later, this test continues to assert the
// abort-on-concurrent-write property.
func TestApply_Move_ConcurrentExternalWriter_MTimeCASCatches(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	path := filepath.Join(dir, ".claude.json")
	body := `{"verbose":false,"mcpServers":{"pal":{"type":"stdio","command":"go"},"keep":{"type":"stdio","command":"go"}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	gw := newFakeGateway()

	// BeforeSourceWrite hook simulates a concurrent external writer
	// touching the file at the worst possible moment — between
	// mutateSourceRemove's re-read and the atomic rename. We bump
	// the file's mtime via a one-byte rewrite of the same body.
	hookFired := false
	deps := Dependencies{
		Adder:           gw.Adder,
		Remover:         gw.Remover,
		GatewaySnapshot: GatewaySnapshot{Entries: map[string]json.RawMessage{}},
		SidecarPath:     filepath.Join(t.TempDir(), "imp.json"),
		BeforeSourceWrite: func(p string) {
			hookFired = true
			// Wait one filesystem-mtime-tick so our touch is
			// observably newer than the daemon's read mtime.
			// On many filesystems the mtime resolution is 1 ms
			// or coarser; we sleep 50 ms to be safe.
			time.Sleep(50 * time.Millisecond)
			// Rewrite the file with the same bytes — preserves
			// content but advances mtime.
			_ = os.WriteFile(p, []byte(body), 0o600)
		},
	}
	res := Apply(context.Background(), []Op{
		{
			Source:   claudeconfig.SourceCCGlobal,
			Name:     "pal",
			Action:   ActionMove,
			Conflict: ConflictSkip,
		},
	}, deps)

	if !hookFired {
		t.Fatal("BeforeSourceWrite hook did not fire")
	}
	if res[0].Status != StatusApplied {
		t.Fatalf("apply status = %v reason=%q", res[0].Status, res[0].Reason)
	}
	// CRITICAL: SourceUpdated must be FALSE because the mtime-CAS
	// check aborted the source write. The gateway has the entry
	// but the source file is unchanged — and operator must repeat
	// the move to clear the source side.
	if res[0].SourceUpdated {
		t.Errorf("SourceUpdated = true; expected false because mtime-CAS should have aborted the write")
	}
	if !strings.Contains(res[0].Reason, "mtime") {
		t.Errorf("Reason should mention mtime; got %q", res[0].Reason)
	}

	// Verify the source file STILL contains pal — the daemon did not
	// clobber the external writer's version.
	post, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read post: %v", err)
	}
	if !strings.Contains(string(post), `"pal"`) {
		t.Errorf("source lost pal even though mtime-CAS should have aborted write: %s", post)
	}
}

// TestApply_Move_InProcessSerialisation_NoLostUpdate covers the in-
// process race: two concurrent Apply calls on the same source file
// must not lose either entry. The sourceLocks map serialises them.
func TestApply_Move_InProcessSerialisation_NoLostUpdate(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	path := filepath.Join(dir, ".claude.json")
	body := `{"mcpServers":{"a":{"type":"stdio","command":"go"},"b":{"type":"stdio","command":"go"},"c":{"type":"stdio","command":"go"}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	gw := newFakeGateway()
	deps := Dependencies{
		Adder:           gw.Adder,
		Remover:         gw.Remover,
		GatewaySnapshot: GatewaySnapshot{Entries: map[string]json.RawMessage{}},
		SidecarPath:     filepath.Join(t.TempDir(), "imp.json"),
	}

	var wg sync.WaitGroup
	results := make([][]OpResult, 2)
	for i, name := range []string{"a", "b"} {
		wg.Add(1)
		go func(idx int, n string) {
			defer wg.Done()
			results[idx] = Apply(context.Background(), []Op{
				{
					Source:   claudeconfig.SourceCCGlobal,
					Name:     n,
					Action:   ActionMove,
					Conflict: ConflictSkip,
				},
			}, deps)
		}(i, name)
	}
	wg.Wait()

	// At least one of the two must have aborted — sequential
	// guarantee says both can't update mtime independently because
	// the second's re-read sees the first's mtime change. (One may
	// abort with mtime-changed reason.) The other must succeed.
	post, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read post: %v", err)
	}
	// 'c' must remain intact regardless.
	if !strings.Contains(string(post), `"c"`) {
		t.Errorf("source lost c (collateral damage): %s", post)
	}
	// At least one of a/b is gone in the resulting source (the one
	// whose move's mtime-CAS won the race).
	hasA := strings.Contains(string(post), `"a"`)
	hasB := strings.Contains(string(post), `"b"`)
	if hasA && hasB {
		t.Errorf("both a and b still present; concurrent moves did not run: %s", post)
	}
}
