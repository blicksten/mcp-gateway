// check is the staleness gate for grammar-gen. Exits non-zero when the
// on-disk generated files differ from what the current YAML grammar
// would produce. Wired into `make check-grammar` and the GitHub Actions
// `grammar-staleness` job.
//
// Usage:
//
//	go run ./tools/grammar-gen/check
//
// On failure the exit message tells the developer what to do (run
// `go run ./tools/grammar-gen` and commit the result).
package main

import (
	"bytes"
	"fmt"
	"os"

	gen "mcp-gateway/tools/grammar-gen/internal/gen"
)

func main() {
	p := gen.DefaultPaths()
	rendered, err := gen.Render(p)
	if err != nil {
		fmt.Fprintf(os.Stderr, "check-grammar: render failed: %v\n", err)
		os.Exit(2)
	}

	stale := false
	if !sameOnDisk(p.GoOut, rendered.Go) {
		fmt.Fprintf(os.Stderr, "check-grammar: STALE %s\n", p.GoOut)
		stale = true
	}
	if !sameOnDisk(p.TSOut, rendered.TS) {
		fmt.Fprintf(os.Stderr, "check-grammar: STALE %s\n", p.TSOut)
		stale = true
	}
	if stale {
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Generated SAP server-name parsers are out of sync with docs/grammar/sap-server-name.yaml.")
		fmt.Fprintln(os.Stderr, "Fix: run `go run ./tools/grammar-gen` and commit the regenerated files.")
		os.Exit(1)
	}
}

// sameOnDisk reports true when path exists and its contents are
// byte-identical to want. Treats a missing file as "different" so a
// fresh checkout without the generated files surfaces clearly.
func sameOnDisk(path string, want []byte) bool {
	got, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return bytes.Equal(got, want)
}
