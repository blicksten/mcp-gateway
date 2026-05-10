// Targeted unit tests for previously-uncovered branches in claudeimport,
// pushing package coverage from 77.4% past the Phase F T-F.1 ≥80% gate.
//
// These tests exercise helper-function error paths that the larger
// end-to-end Apply tests routed around: DefaultSidecarPath, validateOp
// argument validation, and Apply's pre-I/O validation rejections.
//
// Plan reference: docs/PLAN-sap-picker-and-import-mcp.md task T-F.1.
package claudeimport

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"mcp-gateway/internal/claudeconfig"
)

func TestDefaultSidecarPath_ReturnsHomeMcpGatewayJSON(t *testing.T) {
	path, err := DefaultSidecarPath()
	if err != nil {
		t.Fatalf("DefaultSidecarPath: %v", err)
	}
	// Compare with native separators on the actual return value — earlier
	// implementation double-converted both sides via filepath.ToSlash and
	// would have accepted a unix-style path injected via test override
	// even on Windows. T-F.5 finding LOW-2, 2026-05-10.
	want := filepath.Join(".mcp-gateway", "claude-imported.json")
	if !strings.HasSuffix(path, want) {
		t.Fatalf("DefaultSidecarPath = %q, want suffix %q (native separators)", path, want)
	}
	if !filepath.IsAbs(path) {
		t.Fatalf("DefaultSidecarPath returned non-absolute path: %q", path)
	}
}

func TestValidateOp_AllErrorBranches(t *testing.T) {
	cases := []struct {
		name      string
		op        Op
		wantError string
	}{
		{
			name:      "empty source",
			op:        Op{Name: "x", Action: ActionCopy, Conflict: ConflictSkip},
			wantError: "source is required",
		},
		{
			name:      "empty name",
			op:        Op{Source: claudeconfig.SourceCCGlobal, Action: ActionCopy, Conflict: ConflictSkip},
			wantError: "name is required",
		},
		{
			name:      "invalid action",
			op:        Op{Source: claudeconfig.SourceCCGlobal, Name: "x", Action: "duplicate", Conflict: ConflictSkip},
			wantError: `invalid action "duplicate"`,
		},
		{
			name:      "invalid conflict",
			op:        Op{Source: claudeconfig.SourceCCGlobal, Name: "x", Action: ActionCopy, Conflict: "merge"},
			wantError: `invalid conflict policy "merge"`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateOp(&c.op)
			if err == nil {
				t.Fatalf("validateOp(%+v) returned nil, want %q", c.op, c.wantError)
			}
			if !strings.Contains(err.Error(), c.wantError) {
				t.Fatalf("validateOp error = %q, want contains %q", err.Error(), c.wantError)
			}
		})
	}
}

func TestValidateOp_AcceptsAllValidCombinations(t *testing.T) {
	for _, action := range []Action{ActionCopy, ActionMove} {
		for _, conflict := range []ConflictPolicy{ConflictSkip, ConflictOverwrite} {
			op := Op{Source: claudeconfig.SourceCCGlobal, Name: "x", Action: action, Conflict: conflict}
			if err := validateOp(&op); err != nil {
				t.Errorf("validateOp(%v/%v) = %v, want nil", action, conflict, err)
			}
		}
	}
}

func TestApply_RejectsEmptySource_AsValidationError(t *testing.T) {
	gw := newFakeGateway()
	res := Apply(context.Background(), []Op{
		{Source: "", Name: "x", Action: ActionCopy, Conflict: ConflictSkip},
	}, Dependencies{
		Adder:           gw.Adder,
		Remover:         gw.Remover,
		GatewaySnapshot: GatewaySnapshot{Entries: map[string]json.RawMessage{}},
		SidecarPath:     filepath.Join(t.TempDir(), "imp.json"),
	})
	if len(res) != 1 {
		t.Fatalf("results length = %d, want 1", len(res))
	}
	if res[0].Status != StatusError {
		t.Fatalf("status = %q, want %q", res[0].Status, StatusError)
	}
	if !strings.Contains(res[0].Reason, "source is required") {
		t.Fatalf("reason = %q, want contains 'source is required'", res[0].Reason)
	}
}

func TestApply_RejectsEmptyName_AsValidationError(t *testing.T) {
	gw := newFakeGateway()
	res := Apply(context.Background(), []Op{
		{Source: claudeconfig.SourceCCGlobal, Name: "", Action: ActionCopy, Conflict: ConflictSkip},
	}, Dependencies{
		Adder:           gw.Adder,
		Remover:         gw.Remover,
		GatewaySnapshot: GatewaySnapshot{Entries: map[string]json.RawMessage{}},
		SidecarPath:     filepath.Join(t.TempDir(), "imp.json"),
	})
	if len(res) != 1 {
		t.Fatalf("results length = %d, want 1", len(res))
	}
	if res[0].Status != StatusError {
		t.Fatalf("status = %q, want %q", res[0].Status, StatusError)
	}
	if !strings.Contains(res[0].Reason, "name is required") {
		t.Fatalf("reason = %q, want contains 'name is required'", res[0].Reason)
	}
}

func TestDriftFields_NonObjectInputs_ReturnNil(t *testing.T) {
	cases := []struct {
		name      string
		candidate json.RawMessage
		gateway   json.RawMessage
	}{
		{"empty candidate", json.RawMessage(``), json.RawMessage(`{"command":"go"}`)},
		{"null candidate", json.RawMessage(`null`), json.RawMessage(`{"command":"go"}`)},
		{"array candidate", json.RawMessage(`[1,2,3]`), json.RawMessage(`{"command":"go"}`)},
		{"scalar candidate", json.RawMessage(`42`), json.RawMessage(`{"command":"go"}`)},
		{"empty gateway", json.RawMessage(`{"command":"go"}`), json.RawMessage(``)},
		{"null gateway", json.RawMessage(`{"command":"go"}`), json.RawMessage(`null`)},
		{"array gateway", json.RawMessage(`{"command":"go"}`), json.RawMessage(`[1]`)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			state := GatewayState{Entries: map[string]json.RawMessage{"x": c.gateway}}
			got := DriftFields(c.candidate, state, "x")
			if got != nil {
				t.Fatalf("DriftFields = %v, want nil for non-object input", got)
			}
		})
	}
}

func TestDriftFields_FieldOrderInsensitive(t *testing.T) {
	candidate := json.RawMessage(`{"command":"go","args":["a","b"]}`)
	gateway := json.RawMessage(`{"args":["a","b"],"command":"go"}`)
	state := GatewayState{Entries: map[string]json.RawMessage{"x": gateway}}
	got := DriftFields(candidate, state, "x")
	if len(got) != 0 {
		t.Fatalf("DriftFields = %v, want empty (field-order insensitive)", got)
	}
}

func TestDriftFields_DetectsAddedAndRemovedKeys(t *testing.T) {
	candidate := json.RawMessage(`{"command":"go","args":["new"]}`)
	gateway := json.RawMessage(`{"command":"go","cwd":"/tmp"}`)
	state := GatewayState{Entries: map[string]json.RawMessage{"x": gateway}}
	got := DriftFields(candidate, state, "x")
	wantSet := map[string]bool{"args": true, "cwd": true}
	if len(got) != len(wantSet) {
		t.Fatalf("DriftFields = %v, want exactly %v", got, wantSet)
	}
	for _, k := range got {
		if !wantSet[k] {
			t.Fatalf("DriftFields = %v contains unexpected key %q", got, k)
		}
	}
}

func TestDriftFields_NoEntryReturnsNil(t *testing.T) {
	state := GatewayState{Entries: map[string]json.RawMessage{}}
	got := DriftFields(json.RawMessage(`{"command":"go"}`), state, "missing")
	if got != nil {
		t.Fatalf("DriftFields = %v, want nil for missing entry", got)
	}
}

func TestApply_RejectsInvalidConflictPolicy_AsValidationError(t *testing.T) {
	gw := newFakeGateway()
	res := Apply(context.Background(), []Op{
		{Source: claudeconfig.SourceCCGlobal, Name: "x", Action: ActionCopy, Conflict: "merge"},
	}, Dependencies{
		Adder:           gw.Adder,
		Remover:         gw.Remover,
		GatewaySnapshot: GatewaySnapshot{Entries: map[string]json.RawMessage{}},
		SidecarPath:     filepath.Join(t.TempDir(), "imp.json"),
	})
	if len(res) != 1 {
		t.Fatalf("results length = %d, want 1", len(res))
	}
	if res[0].Status != StatusError {
		t.Fatalf("status = %q, want %q", res[0].Status, StatusError)
	}
	if !strings.Contains(res[0].Reason, "invalid conflict policy") {
		t.Fatalf("reason = %q, want contains 'invalid conflict policy'", res[0].Reason)
	}
}
