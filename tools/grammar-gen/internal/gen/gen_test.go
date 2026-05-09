// Smoke tests for the codegen package. Asserts the generator runs
// against the real YAML at docs/grammar/sap-server-name.yaml without
// errors and that the rendered Go output passes go/format (i.e. is
// gofmt-clean — important because the staleness check compares the
// generator's output byte-by-byte against on-disk content).
//
// Plan reference: docs/PLAN-sap-picker-and-import-mcp.md task T-A.2.
package gen

import (
	"bytes"
	"go/format"
	"strings"
	"testing"
)

func TestRender_RealGrammar_Smokes(t *testing.T) {
	r, err := Render(DefaultPaths())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(r.Go) == 0 || len(r.TS) == 0 {
		t.Fatalf("Render returned empty payloads (Go=%d bytes, TS=%d bytes)", len(r.Go), len(r.TS))
	}
	// Go output must be gofmt-clean. We don't assert byte-for-byte
	// equality with format.Source again (Render already runs that), but
	// re-running gofmt as a fixed point is a cheap regression catch in
	// case Render's gofmt step is ever removed.
	formatted, err := format.Source(r.Go)
	if err != nil {
		t.Fatalf("Render Go output is not parseable: %v", err)
	}
	if !bytes.Equal(formatted, r.Go) {
		t.Fatalf("Render Go output is not idempotent under gofmt — Render.Go must already pass go/format")
	}
	// TS output is not auto-formatted (no equivalent to go/format on
	// the Go side); we just spot-check that key public symbols exist.
	tsStr := string(r.TS)
	for _, want := range []string{
		"export function parseServerName",
		"export function isVSP",
		"export function isSAPGUI",
		"export function isSAP",
		"export const KIND_VSP",
		"export const KIND_SAP_GUI",
	} {
		if !strings.Contains(tsStr, want) {
			t.Errorf("TS output missing expected symbol: %q", want)
		}
	}
}
