# 01-inventory.ps1 - Enumerate every function in the codebase.
# Output: ../outputs/inventory.json
#
# IMPLEMENTATION STATUS (2026-05-24): SKELETON_DEFERRED.
# Full implementation: Phase 0 of docs/PLAN-verification-protocol.md.
#
# Per PAL review 2026-05-24 [D-HIGH]: this script previously used a fragile
# regex that misses multi-line method receivers, generic functions
# `func (s *Foo[T])`, and does not distinguish `func init()`. The skeleton
# regex was producing silently incomplete inventory that contaminated 05-gap.
#
# The honest fix at skeleton level is: DO NOT produce a wrong-looking inventory.
# Emit an empty inventory with status=SKELETON_DEFERRED so downstream 05-gap
# refuses to compute a gap matrix and run-all.ps1 detects the skeleton state.
#
# Phase 0 will replace this with a proper `go/ast` walker (likely a small
# Go program `audit-inventory` invoked from here) for Go, and a `ts-morph`
# pass for TypeScript.

param([string]$AuditPath = (Split-Path $PSScriptRoot -Parent))

$ErrorActionPreference = "Stop"
$outputPath = Join-Path $AuditPath "outputs\inventory.json"
New-Item -ItemType Directory -Force -Path (Split-Path $outputPath) | Out-Null

$projectRoot = $AuditPath
while ($projectRoot -and -not (Test-Path (Join-Path $projectRoot ".git"))) {
    $projectRoot = Split-Path $projectRoot -Parent
}

# Best-effort package enumeration via `go list` (no AST yet, no function-level data)
$goPackages = @()
if (Get-Command go -ErrorAction SilentlyContinue) {
    Push-Location $projectRoot
    try {
        $pkgList = & go list ./... 2>&1
        if ($LASTEXITCODE -eq 0) {
            $goPackages = $pkgList | Where-Object { $_ -and ($_ -notmatch '^err|^warn') }
        }
    } finally {
        Pop-Location
    }
}

$inventory = @{
    meta = @{
        collected_at = (Get-Date -Format "yyyy-MM-ddTHH:mm:ssK")
        status       = "SKELETON_DEFERRED - inventory at function granularity intentionally not produced at skeleton level"
        reason       = "Per PAL review 2026-05-24 [D-HIGH], the regex-based skeleton produced silently incomplete inventory. Honest behavior: emit empty function list + Phase 0 marker so downstream 05-gap halts gracefully."
        phase        = "Phase 0 of docs/PLAN-verification-protocol.md"
        replacement  = "go/ast walker (Go) + ts-morph (TS) + plain-JS scanner (webview)"
    }
    go_packages_discovered = $goPackages
    go = @()  # intentionally empty until Phase 0
    ts = @()
    js = @()
}

$inventory | ConvertTo-Json -Depth 10 | Out-File -Encoding utf8 -FilePath $outputPath
Write-Host "Inventory: SKELETON_DEFERRED. Discovered $($goPackages.Count) Go packages but did not enumerate functions."
Write-Host "05-gap.ps1 will detect this state and emit gap_status=blocked_on_inventory."
exit 0
