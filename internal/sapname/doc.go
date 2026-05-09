// Package sapname holds the generated parser for SAP server-name strings
// of the form `vsp-<SID>(-<Client>)?` and `sap-gui-<SID>(-<Client>)?`.
//
// The parser source-of-truth is docs/grammar/sap-server-name.yaml; the
// Go file in this package (grammar_gen.go) is regenerated from there by
// running `go run ./tools/grammar-gen`. CI's grammar-staleness job
// rejects any commit that lets the generated file drift away from the
// YAML.
//
// Mirror file: vscode/mcp-gateway-dashboard/src/sap-name-grammar.gen.ts
// — both are emitted from the same YAML so Go-side daemon code and
// TS-side dashboard code agree on every server name (cross-domain risk
// X1 in the SAP Picker spike, mitigated by R-21).
//
// Plan reference: docs/PLAN-sap-picker-and-import-mcp.md task T-A.2.
package sapname
