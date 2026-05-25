# 04a-scenarios-lint.ps1 - Pre-flight: verify scenarios.md is not ritualistic.
# Per PAL re-review Q1 (2026-05-24 BLESSING round): the 4th axis (Correctness)
# only closes the gap if `assertions:` blocks are non-empty and `evidence_path:`
# points to a buildable location. This script enforces that BEFORE 04-scenarios
# runs anything.
# Output: ../outputs/scenarios-lint.log
# Exit non-zero if any scenario lacks substantive assertions or evidence_path.

param([string]$AuditPath = (Split-Path $PSScriptRoot -Parent))

$ErrorActionPreference = "Stop"
$scenariosPath = Join-Path $AuditPath "inputs\scenarios.md"
$logPath       = Join-Path $AuditPath "outputs\scenarios-lint.log"
New-Item -ItemType Directory -Force -Path (Split-Path $logPath) | Out-Null

if (-not (Test-Path $scenariosPath)) {
    Write-Host "[BLOCK] scenarios.md not found at $scenariosPath" -ForegroundColor Red
    exit 1
}

$lines = Get-Content $scenariosPath
$logLines = @("Scenarios lint - $(Get-Date -Format 'yyyy-MM-ddTHH:mm:ssK')", "")

# Parse: a scenario block starts at "## scn-..." header and ends at next "##" or "---"
$scenarios = @()
$current = $null
foreach ($line in $lines) {
    if ($line -match '^##\s+(scn-[a-zA-Z0-9_\-]+)\s*$') {
        if ($current) { $scenarios += $current }
        $current = [PSCustomObject]@{
            Id              = $Matches[1]
            HasAssertions   = $false
            HasEvidencePath = $false
            HasNonEmptyAssertion = $false
            HasNonEmptyEvidence  = $false
            Body            = @()
        }
        continue
    }
    if ($null -eq $current) { continue }
    if ($line -match '^##\s+' -or $line -match '^---\s*$') {
        $scenarios += $current
        $current = $null
        continue
    }
    $current.Body += $line

    if ($line -match '(?i)\*\*assertions[:\*]') {
        $current.HasAssertions = $true
    }
    # detect a bullet under assertions (lines that look like checks) - require at least
    # ONE non-comment, non-placeholder line within ~10 lines after the **assertions:** label
    if ($current.HasAssertions -and -not $current.HasNonEmptyAssertion) {
        if ($line -match '^\s*-\s+\S' -and $line -notmatch '^\s*-\s+(TODO|TBD|FIXME|XXX)') {
            $current.HasNonEmptyAssertion = $true
        }
    }

    if ($line -match '(?i)\*\*evidence_path[:\*]') {
        $current.HasEvidencePath = $true
        # capture the path after the label on the same line
        if ($line -match '(?i)\*\*evidence_path[:\*]+\*\s*(.+)$') {
            $candidate = $Matches[1].Trim('`', ' ', "`t")
            if ($candidate -and ($candidate -notmatch '^(TODO|TBD|null|N/A)')) {
                $current.HasNonEmptyEvidence = $true
            }
        }
    }
}
if ($current) { $scenarios += $current }

if ($scenarios.Count -eq 0) {
    $logLines += "[BLOCK] No '## scn-...' scenario headers parsed from scenarios.md"
    $logLines | Out-File -Encoding utf8 -FilePath $logPath
    Write-Host "[BLOCK] No scenario headers found in scenarios.md" -ForegroundColor Red
    exit 1
}

$violations = @()
foreach ($s in $scenarios) {
    if (-not $s.HasAssertions) {
        $violations += "$($s.Id): missing **assertions:** label"
        continue
    }
    if (-not $s.HasNonEmptyAssertion) {
        $violations += "$($s.Id): **assertions:** label present but no substantive bullet (only TODO/TBD or empty)"
    }
    if (-not $s.HasEvidencePath) {
        $violations += "$($s.Id): missing **evidence_path:** label"
        continue
    }
    if (-not $s.HasNonEmptyEvidence) {
        $violations += "$($s.Id): **evidence_path:** label present but value is empty/TODO/null"
    }
}

$logLines += "Total scenarios parsed: $($scenarios.Count)"
$logLines += "Violations: $($violations.Count)"
$logLines += ""

foreach ($v in $violations) {
    $logLines += "  [VIOLATION] $v"
}

$encoding = [System.Text.UTF8Encoding]::new($false)
[System.IO.File]::WriteAllText($logPath, ($logLines -join "`n") + "`n", $encoding)

if ($violations.Count -gt 0) {
    Write-Host "[BLOCK] $($violations.Count) scenario lint violation(s):" -ForegroundColor Red
    foreach ($v in $violations) { Write-Host "  - $v" -ForegroundColor Red }
    Write-Host "        Per PAL Q1 advisory: the 4th axis (Correctness) demands substantive assertions; empty/TODO assertions are ritualistic." -ForegroundColor Red
    Write-Host "        Fix scenarios.md and re-run." -ForegroundColor Red
    exit 1
}

Write-Host "[OK] All $($scenarios.Count) scenarios have non-empty assertions and evidence_path."
exit 0
