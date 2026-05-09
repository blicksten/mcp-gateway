// grammar-gen reads docs/grammar/sap-server-name.yaml and writes the Go +
// TS parsers to their committed locations. Idempotent: running multiple
// times is a no-op when the YAML has not changed.
//
// Usage:
//
//	go run ./tools/grammar-gen           # regenerate parsers in place
//	go run ./tools/grammar-gen -dry-run  # print what would change to stderr
//
// The companion staleness check at ./tools/grammar-gen/check exits non-zero
// when the on-disk parsers differ from what would be generated. CI wires
// it via `make check-grammar`.
//
// Plan reference: docs/PLAN-sap-picker-and-import-mcp.md task T-A.2.
package main

import (
	"flag"
	"fmt"
	"os"

	gen "mcp-gateway/tools/grammar-gen/internal/gen"
)

func main() {
	dryRun := flag.Bool("dry-run", false, "print actions to stderr instead of writing files")
	flag.Parse()

	if err := gen.Generate(gen.DefaultPaths(), *dryRun); err != nil {
		fmt.Fprintf(os.Stderr, "grammar-gen: %v\n", err)
		os.Exit(1)
	}
}
