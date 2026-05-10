package claudeimport

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"mcp-gateway/internal/claudeconfig"
	"mcp-gateway/internal/models"
)

// fakeGateway captures the calls Apply makes into the api package's
// addServerInProcess / removeServerInProcess via the Adder/Remover
// callbacks.
type fakeGateway struct {
	added   map[string]*models.ServerConfig
	removed []string
	addErr  error
	rmErr   error
}

func newFakeGateway() *fakeGateway {
	return &fakeGateway{added: map[string]*models.ServerConfig{}}
}

func (f *fakeGateway) Adder(_ context.Context, name string, sc *models.ServerConfig, _ AddOpts) error {
	if f.addErr != nil {
		return f.addErr
	}
	f.added[name] = sc
	return nil
}

func (f *fakeGateway) Remover(_ context.Context, name string, _ RemoveOpts) (RemoveResult, error) {
	if f.rmErr != nil {
		return RemoveResult{}, f.rmErr
	}
	f.removed = append(f.removed, name)
	return RemoveResult{}, nil
}

// writeCCGlobal writes a fake ~/.claude.json with the given mcpServers
// payload to a tempdir and points HOME / USERPROFILE at the dir.
func writeCCGlobal(t *testing.T, mcpServers string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	body := `{
  "verbose": false,
  "mcpServers": ` + mcpServers + `,
  "projects": {}
}`
	path := filepath.Join(dir, ".claude.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Use a separate dir for sidecar so we don't pollute the
	// "home" with claude-imported.json that affects subsequent
	// snapshot reads (they don't read the sidecar but defence-in-depth).
	return path
}

func TestApply_Copy_Success(t *testing.T) {
	writeCCGlobal(t, `{"pal":{"type":"stdio","command":"go","args":["pal-mcp"]}}`)
	gw := newFakeGateway()
	sidecar := filepath.Join(t.TempDir(), "imp.json")

	res := Apply(context.Background(), []Op{
		{
			Source:   claudeconfig.SourceCCGlobal,
			Name:     "pal",
			Action:   ActionCopy,
			Conflict: ConflictSkip,
		},
	}, Dependencies{
		Adder:           gw.Adder,
		Remover:         gw.Remover,
		GatewaySnapshot: GatewaySnapshot{Entries: map[string]json.RawMessage{}},
		SidecarPath:     sidecar,
	})

	if len(res) != 1 {
		t.Fatalf("results count = %d, want 1", len(res))
	}
	if res[0].Status != StatusApplied {
		t.Errorf("Status = %v reason=%q, want applied", res[0].Status, res[0].Reason)
	}
	if _, ok := gw.added["pal"]; !ok {
		t.Errorf("Adder was not called for 'pal'")
	}

	// Sidecar must contain the record.
	body, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	if !strings.Contains(string(body), `"name": "pal"`) {
		t.Errorf("sidecar missing pal record: %s", body)
	}
}

func TestApply_ConflictSkip_DoesNotAdd(t *testing.T) {
	writeCCGlobal(t, `{"pal":{"type":"stdio","command":"go"}}`)
	gw := newFakeGateway()

	res := Apply(context.Background(), []Op{
		{
			Source:   claudeconfig.SourceCCGlobal,
			Name:     "pal",
			Action:   ActionCopy,
			Conflict: ConflictSkip,
		},
	}, Dependencies{
		Adder:   gw.Adder,
		Remover: gw.Remover,
		GatewaySnapshot: GatewaySnapshot{
			Entries: map[string]json.RawMessage{
				"pal": json.RawMessage(`{"type":"stdio","command":"go"}`),
			},
		},
		SidecarPath: filepath.Join(t.TempDir(), "imp.json"),
	})
	if res[0].Status != StatusSkipped {
		t.Errorf("Status = %v, want skipped", res[0].Status)
	}
	if len(gw.added) != 0 {
		t.Errorf("Adder must NOT have been called on skip, got %v", gw.added)
	}
}

func TestApply_ConflictOverwrite_RemovesThenAdds(t *testing.T) {
	writeCCGlobal(t, `{"pal":{"type":"stdio","command":"go","args":["new"]}}`)
	gw := newFakeGateway()

	res := Apply(context.Background(), []Op{
		{
			Source:   claudeconfig.SourceCCGlobal,
			Name:     "pal",
			Action:   ActionCopy,
			Conflict: ConflictOverwrite,
		},
	}, Dependencies{
		Adder:   gw.Adder,
		Remover: gw.Remover,
		GatewaySnapshot: GatewaySnapshot{
			Entries: map[string]json.RawMessage{
				"pal": json.RawMessage(`{"type":"stdio","command":"go","args":["old"]}`),
			},
		},
		SidecarPath: filepath.Join(t.TempDir(), "imp.json"),
	})
	if res[0].Status != StatusApplied {
		t.Errorf("Status = %v reason=%q, want applied", res[0].Status, res[0].Reason)
	}
	if len(gw.removed) != 1 || gw.removed[0] != "pal" {
		t.Errorf("Remover not called: %v", gw.removed)
	}
	if _, ok := gw.added["pal"]; !ok {
		t.Errorf("Adder not called after remove")
	}
	want := []string{"args"}
	if len(res[0].DriftFields) != 1 || res[0].DriftFields[0] != want[0] {
		t.Errorf("DriftFields = %v, want %v", res[0].DriftFields, want)
	}
}

func TestApply_Move_DeletesFromSource(t *testing.T) {
	path := writeCCGlobal(t, `{"pal":{"type":"stdio","command":"go"},"other":{"type":"stdio","command":"go"}}`)
	gw := newFakeGateway()

	beforeCalled := atomic.Int32{}
	afterCalled := atomic.Int32{}

	res := Apply(context.Background(), []Op{
		{
			Source:   claudeconfig.SourceCCGlobal,
			Name:     "pal",
			Action:   ActionMove,
			Conflict: ConflictSkip,
		},
	}, Dependencies{
		Adder:           gw.Adder,
		Remover:         gw.Remover,
		GatewaySnapshot: GatewaySnapshot{Entries: map[string]json.RawMessage{}},
		SidecarPath:     filepath.Join(t.TempDir(), "imp.json"),
		BeforeSourceWrite: func(_ string) { beforeCalled.Add(1) },
		AfterSourceWrite:  func(_ string, _ bool) { afterCalled.Add(1) },
	})
	if res[0].Status != StatusApplied {
		t.Errorf("Status = %v, want applied", res[0].Status)
	}
	if !res[0].SourceUpdated {
		t.Errorf("SourceUpdated = false on move")
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// "pal" must be gone, "other" must remain.
	if strings.Contains(string(body), `"pal"`) {
		t.Errorf("source still contains pal: %s", body)
	}
	if !strings.Contains(string(body), `"other"`) {
		t.Errorf("source lost other: %s", body)
	}
	// Top-level non-mcpServers fields preserved.
	if !strings.Contains(string(body), `"verbose": false`) {
		t.Errorf("source lost verbose key: %s", body)
	}
	if beforeCalled.Load() != 1 || afterCalled.Load() != 1 {
		t.Errorf("hooks not invoked: before=%d after=%d", beforeCalled.Load(), afterCalled.Load())
	}
}

// TestApply_Move_CCProject_ThreadsProjectRoot is the F-01 regression
// test. Before the fix, mutateSourceRemove hardcoded projectRoot=""
// for the re-read, which always failed for SourceCCProject with
// ErrEmptyProjectRoot. This test asserts the fix: a cc_project move
// can complete (entry deleted from <workspace>/.mcp.json).
func TestApply_Move_CCProject_ThreadsProjectRoot(t *testing.T) {
	proj := t.TempDir()
	body := `{"mcpServers":{"pal":{"type":"stdio","command":"go"},"keep":{"type":"stdio","command":"go"}}}`
	if err := os.WriteFile(filepath.Join(proj, ".mcp.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	gw := newFakeGateway()

	res := Apply(context.Background(), []Op{
		{
			Source:      claudeconfig.SourceCCProject,
			ProjectRoot: proj,
			Name:        "pal",
			Action:      ActionMove,
			Conflict:    ConflictSkip,
		},
	}, Dependencies{
		Adder:           gw.Adder,
		Remover:         gw.Remover,
		GatewaySnapshot: GatewaySnapshot{Entries: map[string]json.RawMessage{}},
		SidecarPath:     filepath.Join(t.TempDir(), "imp.json"),
	})
	if res[0].Status != StatusApplied {
		t.Fatalf("Status = %v reason=%q, want applied", res[0].Status, res[0].Reason)
	}
	if !res[0].SourceUpdated {
		t.Errorf("SourceUpdated = false; F-01 regression — projectRoot not threaded into mutateSourceRemove")
	}
	post, err := os.ReadFile(filepath.Join(proj, ".mcp.json"))
	if err != nil {
		t.Fatalf("read post: %v", err)
	}
	if strings.Contains(string(post), `"pal"`) {
		t.Errorf("source still contains pal: %s", post)
	}
	if !strings.Contains(string(post), `"keep"`) {
		t.Errorf("source lost keep: %s", post)
	}
}

func TestApply_Move_OnAlreadyAbsentSource_Idempotent(t *testing.T) {
	// Source has no "pal" entry — mutateSourceRemove should be a no-op.
	writeCCGlobal(t, `{"other":{"type":"stdio","command":"go"}}`)
	gw := newFakeGateway()

	res := Apply(context.Background(), []Op{
		{
			Source:   claudeconfig.SourceCCGlobal,
			Name:     "pal",
			Action:   ActionMove,
			Conflict: ConflictSkip,
		},
	}, Dependencies{
		Adder:           gw.Adder,
		Remover:         gw.Remover,
		GatewaySnapshot: GatewaySnapshot{Entries: map[string]json.RawMessage{}},
		SidecarPath:     filepath.Join(t.TempDir(), "imp.json"),
	})
	// Apply errored on read because "pal" is not in source.
	if res[0].Status != StatusError {
		t.Errorf("Status = %v, want error (entry not found)", res[0].Status)
	}
	if !strings.Contains(res[0].Reason, "not found") {
		t.Errorf("Reason should mention not found: %q", res[0].Reason)
	}
}

// TestSourceLocks_ReleasedAfterUse is the F-02 regression test.
// Before the fix, sourceLocks map entries persisted forever. This
// test does N moves and asserts the map is empty afterward.
func TestSourceLocks_ReleasedAfterUse(t *testing.T) {
	// Snapshot pre-state.
	sourceLocksMu.Lock()
	preCount := len(sourceLocks)
	sourceLocksMu.Unlock()

	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	body := `{"mcpServers":{"a":{"type":"stdio","command":"go"},"b":{"type":"stdio","command":"go"},"c":{"type":"stdio","command":"go"}}}`
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	gw := newFakeGateway()
	for _, n := range []string{"a", "b", "c"} {
		Apply(context.Background(), []Op{
			{Source: claudeconfig.SourceCCGlobal, Name: n, Action: ActionMove, Conflict: ConflictSkip},
		}, Dependencies{
			Adder:           gw.Adder,
			Remover:         gw.Remover,
			GatewaySnapshot: GatewaySnapshot{Entries: map[string]json.RawMessage{}},
			SidecarPath:     filepath.Join(t.TempDir(), "imp.json"),
		})
	}

	sourceLocksMu.Lock()
	postCount := len(sourceLocks)
	sourceLocksMu.Unlock()

	if postCount != preCount {
		t.Errorf("sourceLocks map grew (%d -> %d); F-02 regression — entries not released", preCount, postCount)
	}
}

// TestTranslateEntry_Override_NotAliased is the F-04 regression test.
// Before the fix, translateEntry returned the override pointer
// directly. A caller mutating the struct after the call would
// corrupt the value handed to Adder.
func TestTranslateEntry_Override_NotAliased(t *testing.T) {
	override := &models.ServerConfig{Command: "/usr/bin/orig"}
	entry := claudeconfig.EntryRaw{Name: "x", Raw: json.RawMessage(`{"type":"stdio","command":"go"}`)}
	sc, _, err := translateEntry(entry, override)
	if err != nil {
		t.Fatalf("translateEntry: %v", err)
	}
	if sc == override {
		t.Errorf("returned pointer aliases override; F-04 regression — caller mutation can corrupt daemon state")
	}
	// Mutating override must not leak into sc.
	override.Command = "/usr/bin/mutated"
	if sc.Command != "/usr/bin/orig" {
		t.Errorf("sc.Command leaked override mutation: %q", sc.Command)
	}
}

func TestApply_InvalidAction_Errors(t *testing.T) {
	writeCCGlobal(t, `{"x":{"type":"stdio","command":"go"}}`)
	gw := newFakeGateway()

	res := Apply(context.Background(), []Op{
		{
			Source:   claudeconfig.SourceCCGlobal,
			Name:     "x",
			Action:   "duplicate",
			Conflict: ConflictSkip,
		},
	}, Dependencies{
		Adder:           gw.Adder,
		Remover:         gw.Remover,
		GatewaySnapshot: GatewaySnapshot{Entries: map[string]json.RawMessage{}},
		SidecarPath:     filepath.Join(t.TempDir(), "imp.json"),
	})
	if res[0].Status != StatusError {
		t.Errorf("invalid action should yield error, got %v", res[0].Status)
	}
}

func TestApply_DestNameOverride(t *testing.T) {
	writeCCGlobal(t, `{"original":{"type":"stdio","command":"go"}}`)
	gw := newFakeGateway()

	res := Apply(context.Background(), []Op{
		{
			Source:   claudeconfig.SourceCCGlobal,
			Name:     "original",
			DestName: "renamed",
			Action:   ActionCopy,
			Conflict: ConflictSkip,
		},
	}, Dependencies{
		Adder:           gw.Adder,
		Remover:         gw.Remover,
		GatewaySnapshot: GatewaySnapshot{Entries: map[string]json.RawMessage{}},
		SidecarPath:     filepath.Join(t.TempDir(), "imp.json"),
	})
	if res[0].Status != StatusApplied {
		t.Errorf("Status = %v reason=%q", res[0].Status, res[0].Reason)
	}
	if _, ok := gw.added["renamed"]; !ok {
		t.Errorf("Adder called with wrong name; have %v", gw.added)
	}
	if _, ok := gw.added["original"]; ok {
		t.Errorf("Adder should NOT have been called with original name")
	}
}

func TestTranslateEntry_StdioWithEnvMap(t *testing.T) {
	entry := claudeconfig.EntryRaw{
		Name: "x",
		Raw: json.RawMessage(`{"type":"stdio","command":"go","args":["pal-mcp"],"env":{"FOO":"1","BAR":"2"}}`),
	}
	sc, _, err := translateEntry(entry, nil)
	if err != nil {
		t.Fatalf("translateEntry: %v", err)
	}
	// Env must be sorted: BAR before FOO.
	if len(sc.Env) != 2 || sc.Env[0] != "BAR=2" || sc.Env[1] != "FOO=1" {
		t.Errorf("Env wrong: %v", sc.Env)
	}
}

func TestTranslateEntry_HTTP(t *testing.T) {
	entry := claudeconfig.EntryRaw{
		Name: "x",
		Raw: json.RawMessage(`{"type":"http","url":"http://localhost:80","headers":{"X":"y"}}`),
	}
	sc, _, err := translateEntry(entry, nil)
	if err != nil {
		t.Fatalf("translateEntry: %v", err)
	}
	if sc.URL != "http://localhost:80" {
		t.Errorf("URL = %q", sc.URL)
	}
	if sc.Headers["X"] != "y" {
		t.Errorf("Headers = %v", sc.Headers)
	}
	if sc.Command != "" {
		t.Errorf("Command should be empty for http entry, got %q", sc.Command)
	}
}

func TestTranslateEntry_OverrideUsesOverride(t *testing.T) {
	entry := claudeconfig.EntryRaw{
		Name: "x",
		Raw: json.RawMessage(`{"type":"stdio","command":"go"}`),
	}
	override := &models.ServerConfig{Command: "/usr/bin/explicit"}
	sc, _, err := translateEntry(entry, override)
	if err != nil {
		t.Fatalf("translateEntry: %v", err)
	}
	if sc.Command != "/usr/bin/explicit" {
		t.Errorf("override Command not applied: %q", sc.Command)
	}
}

func TestTranslateEntry_EmptyEntry_Errors(t *testing.T) {
	entry := claudeconfig.EntryRaw{
		Name: "x",
		Raw:  json.RawMessage(`{"type":"unknown"}`),
	}
	_, _, err := translateEntry(entry, nil)
	if err == nil {
		t.Errorf("expected error on entry with neither command nor url")
	}
}

func TestApply_AdderError_PropagatesToReason(t *testing.T) {
	writeCCGlobal(t, `{"x":{"type":"stdio","command":"go"}}`)
	gw := newFakeGateway()
	gw.addErr = errInjected

	res := Apply(context.Background(), []Op{
		{
			Source:   claudeconfig.SourceCCGlobal,
			Name:     "x",
			Action:   ActionCopy,
			Conflict: ConflictSkip,
		},
	}, Dependencies{
		Adder:           gw.Adder,
		Remover:         gw.Remover,
		GatewaySnapshot: GatewaySnapshot{Entries: map[string]json.RawMessage{}},
		SidecarPath:     filepath.Join(t.TempDir(), "imp.json"),
	})
	if res[0].Status != StatusError {
		t.Errorf("Status = %v reason=%q, want error", res[0].Status, res[0].Reason)
	}
	if !strings.Contains(res[0].Reason, errInjected.Error()) {
		t.Errorf("Reason should contain injected error: %q", res[0].Reason)
	}
}

var errInjected = newErr("injected")

type stringErr string

func (e stringErr) Error() string { return string(e) }

func newErr(s string) error { return stringErr(s) }
