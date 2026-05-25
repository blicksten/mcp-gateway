# 05-gap.ps1 — Merge inventory + reachability + coverage + claims into 3-axis matrix.
# Output: ../outputs/gap-report.md
#
# IMPLEMENTATION STATUS (2026-05-24): SKELETON.
# Full implementation: Phase 5 of docs/PLAN-verification-protocol.md.
#
# For each function in outputs/inventory.json:
#   reachable = function in outputs/reachability.json[].reachable
#   executed  = function in outputs/coverage.json.function_coverage
#   documented = function name appears in outputs/doc-code-diff.md matched OR in claims.yaml impl_file:line
#
# Bucket per (reachable, executed, documented):
#   (Y,Y,Y) → PROVEN
#   (Y,Y,N) → UNDOCUMENTED
#   (Y,N,Y) → UNTESTED
#   (Y,N,N) → REACHABLE_BUT_LATENT
#   (N,*,Y) → DOC_LIE
#   (N,*,N) → DEAD

param([string]$AuditPath = (Split-Path $PSScriptRoot -Parent))

$ErrorActionPreference = "Stop"
$outputPath = Join-Path $AuditPath "outputs\gap-report.md"
New-Item -ItemType Directory -Force -Path (Split-Path $outputPath) | Out-Null

# Load inputs (skeleton — real merge logic in Phase 5)
$inventory   = Get-Content (Join-Path $AuditPath "outputs\inventory.json")    -Raw | ConvertFrom-Json
$reachable   = Get-Content (Join-Path $AuditPath "outputs\reachability.json") -Raw | ConvertFrom-Json
$coverage    = Get-Content (Join-Path $AuditPath "outputs\coverage.json")     -Raw | ConvertFrom-Json
$docDiffPath = Join-Path $AuditPath "outputs\doc-code-diff.md"

$invCount = ($inventory.go + $inventory.ts + $inventory.js).Count

@"
# Gap report — SKELETON

Status: not yet implemented.
Phase: 5 of docs/PLAN-verification-protocol.md.

## Inputs read
- inventory.json: $invCount functions inventoried
- reachability.json: status=$($reachable.meta.status)
- coverage.json: status=$($coverage.meta.status)
- doc-code-diff.md: $(if (Test-Path $docDiffPath) { 'present' } else { 'MISSING' })

## 3-axis bucket counts (planned)
| Bucket | Count | Notes |
|---|---|---|
| PROVEN | TBD | reachable + executed + documented |
| UNDOCUMENTED | TBD | reachable + executed + not documented |
| UNTESTED | TBD | reachable + not executed + documented |
| REACHABLE_BUT_LATENT | TBD | reachable + not executed + not documented |
| DOC_LIE | TBD | not reachable + documented |
| DEAD | TBD | not reachable + not documented |

## ESCALATED items (planned)
Items requiring operator decision before next audit:
- TODO
"@ | Out-File -Encoding utf8 -FilePath $outputPath

Write-Host "Gap report skeleton written. Real impl in Phase 5."
