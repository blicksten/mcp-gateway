// Cross-language parity test for the SAP server-name grammar.
//
// Reads testdata/sap-name-fixtures.json (the same file the TS test in
// vscode/mcp-gateway-dashboard/src/test/sap-name-grammar.gen.test.ts
// reads). Both languages must agree on every fixture; new fixtures
// added to the JSON flow into both tests automatically — no code change
// required.
//
// Plan reference: docs/PLAN-sap-picker-and-import-mcp.md task T-A.2 (the
// codegen pipeline that eliminates Go-vs-TS regex drift, X1 / R-21).
package sapname_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"mcp-gateway/internal/sapname"
)

// fixtureCase mirrors testdata/sap-name-fixtures.json. Expected==nil
// signals a "should reject" case (parser must return ok=false).
type fixtureCase struct {
	Name     string         `json:"name"`
	Expected *fixtureExpect `json:"expected"`
	Reason   string         `json:"$reason,omitempty"`
}

type fixtureExpect struct {
	Kind   string `json:"kind"`
	SID    string `json:"sid"`
	Client string `json:"client"`
}

type fixtureFile struct {
	Version int           `json:"version"`
	Cases   []fixtureCase `json:"cases"`
}

func loadFixtures(t *testing.T) fixtureFile {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	// thisFile = <repo>/internal/sapname/grammar_gen_test.go → repo root is two up.
	repoRoot := filepath.Clean(filepath.Join(thisFile, "..", "..", ".."))
	path := filepath.Join(repoRoot, "testdata", "sap-name-fixtures.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	var f fixtureFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("parse fixtures: %v", err)
	}
	if f.Version != 1 {
		t.Fatalf("unsupported fixtures version %d (this test understands 1)", f.Version)
	}
	if len(f.Cases) < 40 {
		t.Fatalf("fixtures too small: got %d, plan T-A.2 requires ≥40", len(f.Cases))
	}
	return f
}

func TestGrammar_FixtureParity(t *testing.T) {
	f := loadFixtures(t)
	for _, c := range f.Cases {
		t.Run(fixtureSubtest(c.Name), func(t *testing.T) {
			got, ok := sapname.ParseServerName(c.Name)
			if c.Expected == nil {
				if ok {
					t.Fatalf("expected reject, got %+v (reason: %s)", got, c.Reason)
				}
				return
			}
			if !ok {
				t.Fatalf("expected accept %+v, got reject (reason: %s)", *c.Expected, c.Reason)
			}
			if got.Kind != c.Expected.Kind || got.SID != c.Expected.SID || got.Client != c.Expected.Client {
				t.Fatalf("mismatch: got {kind=%s sid=%s client=%s}, want {kind=%s sid=%s client=%s}",
					got.Kind, got.SID, got.Client,
					c.Expected.Kind, c.Expected.SID, c.Expected.Client)
			}
		})
	}
}

// fixtureSubtest renders an empty input as a placeholder so go test's
// subtest naming doesn't print the unhelpful "" test name.
func fixtureSubtest(name string) string {
	if name == "" {
		return "<empty>"
	}
	return name
}

// TestKindHelpers spot-checks the Is{VSP,SAPGUI,SAP} helpers — fixture
// parity above already exercises ParseServerName end-to-end.
func TestKindHelpers(t *testing.T) {
	cases := []struct {
		name   string
		isVSP  bool
		isGUI  bool
		isAny  bool
	}{
		{"vsp-DEV", true, false, true},
		{"vsp-DEV-100", true, false, true},
		{"sap-gui-DEV", false, true, true},
		{"sap-gui-DEV-100", false, true, true},
		{"my-server", false, false, false},
		{"vsp-dev", false, false, false}, // lowercase SID rejected
		{"", false, false, false},
	}
	for _, c := range cases {
		t.Run(fixtureSubtest(c.name), func(t *testing.T) {
			if got := sapname.IsVSP(c.name); got != c.isVSP {
				t.Errorf("IsVSP(%q) = %v, want %v", c.name, got, c.isVSP)
			}
			if got := sapname.IsSAPGUI(c.name); got != c.isGUI {
				t.Errorf("IsSAPGUI(%q) = %v, want %v", c.name, got, c.isGUI)
			}
			if got := sapname.IsSAP(c.name); got != c.isAny {
				t.Errorf("IsSAP(%q) = %v, want %v", c.name, got, c.isAny)
			}
		})
	}
}
