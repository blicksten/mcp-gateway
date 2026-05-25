# 06-drift.ps1 — Compare this audit against the predecessor audit folder.
# Output: ../outputs/drift-vs-prior.md
#
# IMPLEMENTATION STATUS (2026-05-24): SKELETON.
# Full implementation: Phase 7 of docs/PLAN-verification-protocol.md.
#
# Reads manifest.json.predecessor_audit; loads previous outputs/gap-report.md
# and diffs the bucket counts and per-function status transitions.
#
# Drift categories:
#   - New functions added: were they immediately PROVEN?
#   - Removed functions: was removal intentional?
#   - Status regressions: was PROVEN, now UNTESTED — critical
#   - Status improvements: was UNDOCUMENTED, now PROVEN — celebrate
#   - New claims added: any without scenarios?
#   - Removed claims: was deletion authorized?

param([string]$AuditPath = (Split-Path $PSScriptRoot -Parent))

$ErrorActionPreference = "Stop"
$outputPath = Join-Path $AuditPath "outputs\drift-vs-prior.md"
New-Item -ItemType Directory -Force -Path (Split-Path $outputPath) | Out-Null

$manifest = Get-Content (Join-Path $AuditPath "manifest.json") -Raw | ConvertFrom-Json
$predecessor = $manifest.predecessor_audit

if (-not $predecessor) {
    @"
# Drift report — first audit, no predecessor

This is the baseline audit (manifest.predecessor_audit = null).
No drift comparison possible. Future audits will compare against this one.
"@ | Out-File -Encoding utf8 -FilePath $outputPath
    Write-Host "First audit — no predecessor for drift comparison."
    exit 0
}

$predecessorPath = Join-Path (Split-Path $AuditPath -Parent) $predecessor
if (-not (Test-Path $predecessorPath)) {
    @"
# Drift report — predecessor MISSING

manifest.predecessor_audit = $predecessor
Expected folder: $predecessorPath
Folder NOT FOUND.

This is a data integrity warning. Either the predecessor was deleted
(should never happen — audit folders are append-only history)
or manifest.json was edited incorrectly.

Escalate to operator.
"@ | Out-File -Encoding utf8 -FilePath $outputPath
    exit 1
}

@"
# Drift report — SKELETON

Status: not yet implemented.
Phase: 7 of docs/PLAN-verification-protocol.md.

Predecessor: $predecessor
Predecessor path: $predecessorPath

## Planned drift metrics
- New functions: TBD
- Removed functions: TBD
- Status regressions (PROVEN → other): TBD (critical)
- Status improvements: TBD
- New claims: TBD
- Removed claims: TBD

## Predecessor bucket counts (for comparison)
TODO — read predecessor outputs/gap-report.md
"@ | Out-File -Encoding utf8 -FilePath $outputPath

Write-Host "Drift report skeleton written. Real impl in Phase 7."
