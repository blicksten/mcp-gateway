# 04-scenarios.ps1 — Run instrumented binaries, execute scenarios, collect coverage.
# Output: ../outputs/coverage.json + ../outputs/scenario-results.md
#
# IMPLEMENTATION STATUS (2026-05-24): SKELETON.
# Full implementation: Phases 2, 3, 4 of docs/PLAN-verification-protocol.md.
#
# Workflow:
#   1. Build instrumented daemon: go build -cover -coverpkg=./... -o mcp-gateway-cov.exe
#   2. Start daemon with GOCOVERDIR=<audit>/outputs/cov-go
#   3. For each auto scenario in inputs/scenarios.md:
#        a. Reset coverage directory
#        b. Run scenario steps via PowerShell + curl + mcp-ctl
#        c. Stop daemon (so coverage is flushed)
#        d. Run `go tool covdata textfmt` to collapse to text
#        e. Tag coverage with scenario_id
#   4. For TS extension: launch VSCode with --inspect-extensions, attach CDP, collect Profiler.takePreciseCoverage per scenario
#   5. Pause for manual scenarios — display inputs/operator-manual-checklist.md, wait for "go" prompt
#   6. Merge all into outputs/coverage.json

param(
    [string]$AuditPath = (Split-Path $PSScriptRoot -Parent),
    [switch]$SkipManual
)

$ErrorActionPreference = "Stop"
$outputPath = Join-Path $AuditPath "outputs\coverage.json"
$resultsPath = Join-Path $AuditPath "outputs\scenario-results.md"
New-Item -ItemType Directory -Force -Path (Split-Path $outputPath) | Out-Null

$coverage = @{
    meta = @{
        collected_at = (Get-Date -Format "yyyy-MM-ddTHH:mm:ssK")
        status       = "SKELETON — instrumented runs not yet implemented"
        phase        = "Phases 2/3/4 of docs/PLAN-verification-protocol.md"
        skip_manual  = $SkipManual.IsPresent
    }
    scenarios_executed = @()
    scenarios_skipped  = @()
    function_coverage  = @{}
}

$coverage | ConvertTo-Json -Depth 10 | Out-File -Encoding utf8 -FilePath $outputPath

@"
# Scenario results — SKELETON

Status: not yet implemented.
Phase: 2/3/4 of docs/PLAN-verification-protocol.md.

Skip manual: $($SkipManual.IsPresent)

## Auto scenarios — planned
See inputs/scenarios.md.

## Manual scenarios — planned
See inputs/operator-manual-checklist.md.
"@ | Out-File -Encoding utf8 -FilePath $resultsPath

if (-not $SkipManual) {
    Write-Host "Manual scenarios pending — review inputs/operator-manual-checklist.md when running for real."
}

Write-Host "Scenario skeleton written. Real impl in Phases 2-4."
