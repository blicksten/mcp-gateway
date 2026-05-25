# 07-verdict-check.ps1 - Enforce verdict.md triage workflow.
# Per INSTRUCTIONS.md section 10: every finding from gap-report.md MUST be
# triaged in verdict.md with a Decision (Fixed / Deferred / Escalated) and,
# for Deferred items, a non-empty Justification.
# Output: ../outputs/verdict-check.log
# Exit non-zero if triage is incomplete.

param([string]$AuditPath = (Split-Path $PSScriptRoot -Parent))

$ErrorActionPreference = "Stop"
$logPath     = Join-Path $AuditPath "outputs\verdict-check.log"
$verdictPath = Join-Path $AuditPath "outputs\verdict.md"
$gapPath     = Join-Path $AuditPath "outputs\gap-report.md"

New-Item -ItemType Directory -Force -Path (Split-Path $logPath) | Out-Null

function Write-Log($msg, $color = "White") {
    Write-Host $msg -ForegroundColor $color
    Add-Content -Path $logPath -Value $msg
}

# Reset log
"" | Out-File -Encoding utf8 -FilePath $logPath

if (-not (Test-Path $verdictPath)) {
    Write-Log "[BLOCK] verdict.md does not exist at $verdictPath" "Red"
    Write-Log "Operator must create verdict.md from the template in INSTRUCTIONS.md section 10." "Red"
    exit 1
}

if (-not (Test-Path $gapPath)) {
    Write-Log "[BLOCK] gap-report.md does not exist; 05-gap.ps1 must run before verdict-check." "Red"
    exit 1
}

$verdictRaw = Get-Content $verdictPath -Raw
$gapRaw     = Get-Content $gapPath     -Raw

# Skeleton-run guard: even if gap-report is empty (because 01-inventory was deferred),
# a skeleton run MUST NOT trivially pass verdict-check. The gap is real (we don't know
# what would have been found); operator must explicitly acknowledge with -AllowSkeleton.
# Per PAL re-review 2026-05-24: closes the new HIGH finding where empty gap-report
# led to vacuous PASS of verdict-check on a skeleton run.
$manifestPath = Join-Path $AuditPath "manifest.json"
if (Test-Path $manifestPath) {
    try {
        $manifest = Get-Content $manifestPath -Raw | ConvertFrom-Json
        if ($manifest.PSObject.Properties.Match("skeleton_run").Count -gt 0 -and $manifest.skeleton_run -eq $true) {
            Write-Log "[BLOCK] manifest.skeleton_run=true. Verdict-check refuses to pass a skeleton-baseline run." "Red"
            Write-Log "        Skeleton steps: $($manifest.skeleton_step_ids -join ', ')" "Red"
            Write-Log "        This packet is for protocol validation only, not a valid quarterly baseline." "Red"
            Write-Log "        Use run-all.ps1 -AllowSkeleton for training/dry-run; that flag bypasses run-all's banner exit but the packet is still not accepted as a real audit." "Red"
            exit 3
        }
    } catch {
        Write-Log "[WARN] could not parse manifest.json: $_" "Yellow"
    }
}

if ($gapRaw -match "SKELETON|TBD") {
    Write-Log "[BLOCK] gap-report.md contains SKELETON|TBD markers even though manifest.skeleton_run=false." "Red"
    Write-Log "        Internal inconsistency: fix 05-gap or set skeleton_run flag, then re-run." "Red"
    exit 4
}

# Count finding rows: look for lines matching "| F-N |" pattern in either file.
# Relaxed pattern (per PAL re-review 2026-05-24): accept any F-<digits> length,
# so F-1, F-99, F-001 all match (previously '^\|\s*F-\d{3,}\s*\|' silently skipped shorter IDs).
$findingPattern = '^\|\s*F-\d+\s*\|'
$gapFindings = ($gapRaw -split "`r?`n") | Where-Object { $_ -match $findingPattern }
$verdictFindings = ($verdictRaw -split "`r?`n") | Where-Object { $_ -match $findingPattern }

$gapCount = $gapFindings.Count
$verdictCount = $verdictFindings.Count

Write-Log "Gap-report findings: $gapCount"
Write-Log "Verdict triage rows: $verdictCount"

if ($gapCount -eq 0) {
    Write-Log "[OK] No findings in gap-report; verdict triage trivially satisfied." "Green"
    exit 0
}

if ($verdictCount -lt $gapCount) {
    Write-Log "[BLOCK] $gapCount findings in gap-report but only $verdictCount triage rows in verdict.md." "Red"
    Write-Log "        Operator must add a triage row for every finding." "Red"
    exit 1
}

# Validate each verdict row: Decision must be one of Fixed | Deferred | Escalated
$validDecisions = @("Fixed", "Deferred", "Escalated")
$invalidRows = @()
$deferredMissingJustification = @()

foreach ($row in $verdictFindings) {
    # Expected format: | F-NNN | Bucket | Decision | Owner | Due | Justification |
    $cols = $row -split '\|' | ForEach-Object { $_.Trim() } | Where-Object { $_ -ne "" }
    if ($cols.Count -lt 5) {
        $invalidRows += $row
        continue
    }
    $findingId  = $cols[0]
    $decision   = $cols[2]
    $justification = if ($cols.Count -ge 6) { $cols[5] } else { "" }

    if ($validDecisions -notcontains $decision) {
        Write-Log "[BLOCK] $findingId : Decision '$decision' is not in {$($validDecisions -join ', ')}" "Red"
        $invalidRows += $row
        continue
    }
    if ($decision -eq "Deferred" -and [string]::IsNullOrWhiteSpace($justification)) {
        Write-Log "[BLOCK] $findingId : Deferred decision requires non-empty Justification" "Red"
        $deferredMissingJustification += $findingId
    }
}

if ($invalidRows.Count -gt 0 -or $deferredMissingJustification.Count -gt 0) {
    Write-Log "[BLOCK] Verdict triage has $($invalidRows.Count) invalid row(s) and $($deferredMissingJustification.Count) Deferred items without justification." "Red"
    exit 1
}

# Verify signoff checkboxes
if ($verdictRaw -notmatch '\[x\]\s*\(?run-all\.ps1 step 7 verifies') {
    if ($verdictRaw -notmatch '(?im)^\s*-\s*All findings triaged:\s*\[x\]') {
        Write-Log "[BLOCK] Operator signoff checkbox 'All findings triaged: [x]' not found." "Red"
        exit 1
    }
}

Write-Log "[OK] Verdict triage complete: $verdictCount of $gapCount findings triaged, all decisions valid." "Green"
exit 0
