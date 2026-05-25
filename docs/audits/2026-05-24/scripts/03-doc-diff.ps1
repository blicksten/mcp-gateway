# 03-doc-diff.ps1 — Mechanical doc <-> code reconciliation.
# Output: ../outputs/doc-code-diff.md
#
# IMPLEMENTATION STATUS (2026-05-24): SKELETON.
# Full implementation: Phase 1.5 of docs/PLAN-verification-protocol.md.
#
# Will extract:
#   FROM DOCS: regex-scan README.md, docs/**/*.md, CLAUDE.md sections for:
#     - HTTP endpoints (e.g. "POST /api/v1/foo")
#     - Env vars (CLAUDE_*, MCP_*)
#     - VSCode config keys (mcpGateway.*)
#     - CLI subcommands (mcp-ctl <word>)
#     - Commit SHA refs (validate via `git cat-file -e`)
#
#   FROM CODE:
#     - Go: AST scan for http.HandleFunc / mux.Handle, os.Getenv("...")
#     - TS: ts-morph for vscode.commands.registerCommand, getConfiguration().get
#     - package.json: contributes.configuration.properties
#
# Diff produces 3 buckets:
#   - docs-only (claim with no impl — lies)
#   - matched (claim with impl)
#   - code-only (impl without doc — undocumented)
#
# Plus: cross-reference inputs/claims.yaml for semantic claims.

param([string]$AuditPath = (Split-Path $PSScriptRoot -Parent))

$ErrorActionPreference = "Stop"
$outputPath = Join-Path $AuditPath "outputs\doc-code-diff.md"
New-Item -ItemType Directory -Force -Path (Split-Path $outputPath) | Out-Null

@"
# Doc-code diff — SKELETON

Status: not yet implemented.
Phase: 1.5 of docs/PLAN-verification-protocol.md.

## Planned extraction sources
- README.md, docs/**/*.md, CLAUDE.md
- Go AST for http.HandleFunc, os.Getenv, viper/config
- TS AST via ts-morph for vscode.commands, getConfiguration
- package.json contributes.configuration

## Planned outputs
### docs-only (potential lies)
TODO

### matched
TODO

### code-only (undocumented)
TODO

### claims.yaml cross-reference
TODO — load inputs/claims.yaml, validate impl_file:line exists and matches.

### commit-ref validation
TODO — extract SHA mentions from docs/spikes/*.md, memory closure files; verify via git cat-file -e.
"@ | Out-File -Encoding utf8 -FilePath $outputPath

Write-Host "Doc-diff skeleton written. Real impl in Phase 1.5."
