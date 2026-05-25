# 99-bootstrap-next.ps1 — Clone this audit folder forward to the next quarterly date.
# Copies INSTRUCTIONS.md, scripts/, inputs/ forward.
# Does NOT copy outputs/ (those are this audit's results, not the next's inputs).
# Updates manifest.json with new date and clears verdict.

param(
    [string]$NextDate
)

$ErrorActionPreference = "Stop"
$AuditPath = Split-Path $PSScriptRoot -Parent
$AuditRoot = Split-Path $AuditPath -Parent  # docs/audits/

# Default next date = +3 months (quarterly cadence)
if (-not $NextDate) {
    $currentName = Split-Path $AuditPath -Leaf
    try {
        $currentDate = [DateTime]::ParseExact($currentName, "yyyy-MM-dd", $null)
        $NextDate = $currentDate.AddMonths(3).ToString("yyyy-MM-dd")
    } catch {
        $NextDate = (Get-Date).AddMonths(3).ToString("yyyy-MM-dd")
    }
}

$nextPath = Join-Path $AuditRoot $NextDate
if (Test-Path $nextPath) {
    Write-Host "[ABORT] Next audit folder already exists: $nextPath" -ForegroundColor Red
    Write-Host "Delete it manually if you want to regenerate." -ForegroundColor Yellow
    exit 1
}

Write-Host "Bootstrapping next audit at: $nextPath"
New-Item -ItemType Directory -Force -Path $nextPath | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $nextPath "outputs") | Out-Null

# Copy forward — full copies, not symlinks (the new folder must be independent)
Copy-Item -Path (Join-Path $AuditPath "INSTRUCTIONS.md") -Destination $nextPath
Copy-Item -Path (Join-Path $AuditPath "README.md")       -Destination $nextPath
Copy-Item -Path (Join-Path $AuditPath "scripts")         -Destination $nextPath -Recurse
Copy-Item -Path (Join-Path $AuditPath "inputs")          -Destination $nextPath -Recurse

# New manifest.json reflecting the new audit
$predecessor = Split-Path $AuditPath -Leaf
$predecessorDate = $predecessor
try {
    $nextDateObj = [DateTime]::ParseExact($NextDate, "yyyy-MM-dd", $null)
    $followingDate = $nextDateObj.AddMonths(3).ToString("yyyy-MM-dd")
} catch {
    $followingDate = $null
}

$manifest = @{
    audit_id          = "mcp-gateway-$NextDate"
    project           = "mcp-gateway"
    audit_date        = $NextDate
    audit_type        = "periodic-v-and-v-conformance"
    cadence           = "quarterly"
    next_audit_due    = $followingDate
    protocol_version  = "1.0"
    anchors           = @("IEEE 1012-2016", "ISO/IEC/IEEE 29148:2018", "ISO/IEC/IEEE 29119")
    git_head_at_start = $null
    git_head_at_end   = $null
    operator          = $null
    tool_versions     = @{}
    status            = "not_started"
    verdict           = $null
    predecessor_audit = $predecessorDate
}

$manifest | ConvertTo-Json -Depth 10 | Out-File -Encoding utf8 -FilePath (Join-Path $nextPath "manifest.json")

# Update docs/audits/latest pointer (text file on Windows where symlinks need admin)
$latestPointer = Join-Path $AuditRoot "latest.txt"
$NextDate | Out-File -Encoding utf8 -FilePath $latestPointer

Write-Host ""
Write-Host "[OK] Next audit bootstrapped at: $nextPath"
Write-Host ""
Write-Host "Next steps:"
Write-Host "  1. cd $nextPath"
Write-Host "  2. Review and update inputs/claims.yaml and inputs/scenarios.md as needed"
Write-Host "  3. Run scripts/run-all.ps1 when ready"
