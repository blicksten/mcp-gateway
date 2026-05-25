# 02-reachability.ps1 — Static call-graph reachability analysis.
# Output: ../outputs/reachability.json
# Format: { "go": { "reachable": [...], "dead": [...] }, "ts": {...} }
#
# IMPLEMENTATION STATUS (2026-05-24): SKELETON.
# Full implementation: Phase 1 of docs/PLAN-verification-protocol.md.
# Will run:
#   - golang.org/x/tools/cmd/deadcode ./...
#   - staticcheck -checks U1000 ./...
#   - npx knip --reporter json (for TS)

param([string]$AuditPath = (Split-Path $PSScriptRoot -Parent))

$ErrorActionPreference = "Stop"
$outputPath = Join-Path $AuditPath "outputs\reachability.json"
New-Item -ItemType Directory -Force -Path (Split-Path $outputPath) | Out-Null

$projectRoot = $AuditPath
while ($projectRoot -and -not (Test-Path (Join-Path $projectRoot ".git"))) {
    $projectRoot = Split-Path $projectRoot -Parent
}

$result = @{
    meta = @{
        collected_at = (Get-Date -Format "yyyy-MM-ddTHH:mm:ssK")
        status       = "SKELETON — invoke deadcode + knip in Phase 1"
        phase        = "Phase 1 of docs/PLAN-verification-protocol.md"
    }
    go = @{ reachable = @(); dead = @(); deadcode_raw = $null; staticcheck_raw = $null }
    ts = @{ reachable = @(); dead = @(); knip_raw = $null }
}

# Attempt to run deadcode if installed (best-effort skeleton)
Push-Location $projectRoot
try {
    try {
        $deadOut = (deadcode ./... 2>&1) -join "`n"
        $result.go.deadcode_raw = $deadOut
    } catch {
        $result.go.deadcode_raw = "deadcode not installed or failed: $_"
    }
} finally {
    Pop-Location
}

$result | ConvertTo-Json -Depth 10 | Out-File -Encoding utf8 -FilePath $outputPath
Write-Host "Reachability skeleton written. Real impl in Phase 1."
