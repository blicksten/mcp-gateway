# run-all.ps1 - Sequencer for the audit. Calls 00..07 in order.
# Reads each step's outputs/<n>.json .meta.status field; if ANY contains SKELETON,
# sets manifest.skeleton_run=true and prints a prominent warning banner.
# Step 7 (07-verdict-check) refuses to pass until operator triages verdict.md.

param(
    [switch]$SkipManual,
    [switch]$DryRun,
    [switch]$AllowSkeleton    # operator explicit ack of skeleton-only training run
)

$ErrorActionPreference = "Stop"
$AuditPath = Split-Path $PSScriptRoot -Parent

$steps = @(
    @{ Id = "00-prereq";        Script = "00-prereq-check.ps1";  OutputFile = "outputs\prereq.log" }
    @{ Id = "01-inventory";     Script = "01-inventory.ps1";     OutputFile = "outputs\inventory.json" }
    @{ Id = "02-reachability";  Script = "02-reachability.ps1";  OutputFile = "outputs\reachability.json" }
    @{ Id = "03-doc-diff";      Script = "03-doc-diff.ps1";      OutputFile = "outputs\doc-code-diff.md" }
    @{ Id = "04a-scenarios-lint"; Script = "04a-scenarios-lint.ps1"; OutputFile = "outputs\scenarios-lint.log" }
    @{ Id = "04-scenarios";     Script = "04-scenarios.ps1";     OutputFile = "outputs\coverage.json" }
    @{ Id = "05-gap";           Script = "05-gap.ps1";           OutputFile = "outputs\gap-report.md" }
    @{ Id = "06-drift";         Script = "06-drift.ps1";         OutputFile = "outputs\drift-vs-prior.md" }
    @{ Id = "07-verdict-check"; Script = "07-verdict-check.ps1"; OutputFile = "outputs\verdict-check.log" }
)

$startTime = Get-Date
Write-Host "Audit packet: $AuditPath"
Write-Host "Start: $startTime"
Write-Host ""

# Capture git HEAD into manifest
$manifestPath = Join-Path $AuditPath "manifest.json"
if (Test-Path $manifestPath) {
    try {
        $manifest = Get-Content $manifestPath -Raw | ConvertFrom-Json
        $projectRoot = $AuditPath
        while ($projectRoot -and -not (Test-Path (Join-Path $projectRoot ".git"))) {
            $projectRoot = Split-Path $projectRoot -Parent
        }
        if ($projectRoot -and (Test-Path (Join-Path $projectRoot ".git"))) {
            Push-Location $projectRoot
            try { $manifest.git_head_at_start = (git rev-parse HEAD).Trim() } finally { Pop-Location }
        }
        $manifest | ConvertTo-Json -Depth 10 | Out-File -Encoding utf8 -FilePath $manifestPath
    } catch {
        Write-Host "[WARN] could not update manifest.git_head_at_start: $_" -ForegroundColor Yellow
    }
}

$skeletonStepIds = @()

foreach ($step in $steps) {
    $stepStart = Get-Date
    $scriptPath = Join-Path $PSScriptRoot $step.Script
    Write-Host ">>> [$($step.Id)] starting"

    if ($DryRun) {
        Write-Host "    (dry run - would execute $scriptPath)"
        continue
    }

    if (-not (Test-Path $scriptPath)) {
        Write-Host "    [SKIP] $($step.Id) script not yet implemented: $scriptPath" -ForegroundColor Yellow
        continue
    }

    $stepArgs = @()
    if ($step.Id -eq "04-scenarios" -and $SkipManual) {
        $stepArgs += "-SkipManual"
    }

    try {
        & pwsh -NoProfile -File $scriptPath @stepArgs -AuditPath $AuditPath
        if ($LASTEXITCODE -ne 0) {
            Write-Host "    [FAIL] $($step.Id) exited with $LASTEXITCODE" -ForegroundColor Red
            exit $LASTEXITCODE
        }
    } catch {
        Write-Host "    [ERROR] $($step.Id): $_" -ForegroundColor Red
        exit 1
    }

    # Skeleton-run detection: read output JSON .meta.status if available
    $outputPath = Join-Path $AuditPath $step.OutputFile
    if ((Test-Path $outputPath) -and ($outputPath -like "*.json")) {
        try {
            $outputJson = Get-Content $outputPath -Raw | ConvertFrom-Json
            $status = $outputJson.meta.status
            # Match literals actually emitted by step scripts: 'SKELETON' (generic), 'SKELETON_DEFERRED' (01-inventory honest path), 'DEFERRED' (any step that intentionally defers).
            if ($status -and ($status -match "SKELETON|DEFERRED")) {
                $skeletonStepIds += $step.Id
                Write-Host "    [SKELETON DETECTED] $($step.Id): status='$status'" -ForegroundColor Yellow
            }
        } catch {
            # Output isn't JSON or lacks .meta.status — ignore
        }
    }

    $elapsed = (Get-Date) - $stepStart
    Write-Host "<<< [$($step.Id)] done in $($elapsed.TotalSeconds.ToString('F1'))s"
    Write-Host ""
}

# Update manifest with skeleton_run + git_head_at_end
$skeletonRun = ($skeletonStepIds.Count -gt 0)
if (Test-Path $manifestPath) {
    try {
        $manifest = Get-Content $manifestPath -Raw | ConvertFrom-Json
        $manifest | Add-Member -NotePropertyName skeleton_run        -NotePropertyValue $skeletonRun           -Force
        $manifest | Add-Member -NotePropertyName skeleton_step_ids   -NotePropertyValue $skeletonStepIds       -Force
        $projectRoot = $AuditPath
        while ($projectRoot -and -not (Test-Path (Join-Path $projectRoot ".git"))) {
            $projectRoot = Split-Path $projectRoot -Parent
        }
        if ($projectRoot -and (Test-Path (Join-Path $projectRoot ".git"))) {
            Push-Location $projectRoot
            try { $manifest.git_head_at_end = (git rev-parse HEAD).Trim() } finally { Pop-Location }
        }
        $manifest.status = if ($skeletonRun) { "skeleton_run" } else { "complete" }
        $manifest | ConvertTo-Json -Depth 10 | Out-File -Encoding utf8 -FilePath $manifestPath
    } catch {
        Write-Host "[WARN] could not finalize manifest: $_" -ForegroundColor Yellow
    }
}

$totalElapsed = (Get-Date) - $startTime
Write-Host "==========================================================="
Write-Host "All steps complete in $($totalElapsed.TotalMinutes.ToString('F1'))m"
Write-Host "==========================================================="

if ($skeletonRun) {
    Write-Host ""
    Write-Host "###########################################################"  -ForegroundColor Yellow
    Write-Host "#  [SKELETON RUN - NOT A VALID BASELINE]                  #"  -ForegroundColor Yellow
    Write-Host "###########################################################"  -ForegroundColor Yellow
    Write-Host "Steps with skeleton output:"                                   -ForegroundColor Yellow
    foreach ($id in $skeletonStepIds) { Write-Host "  - $id"                  -ForegroundColor Yellow }
    Write-Host ""                                                              -ForegroundColor Yellow
    Write-Host "manifest.skeleton_run=true. 06-drift.ps1 of the NEXT audit"   -ForegroundColor Yellow
    Write-Host "will refuse to compare against this packet as a baseline."    -ForegroundColor Yellow
    Write-Host "This run is for protocol validation only, NOT for baseline."  -ForegroundColor Yellow
    Write-Host "###########################################################"  -ForegroundColor Yellow
    if (-not $AllowSkeleton) {
        exit 2
    }
}

Write-Host ""
Write-Host "Next: review outputs/verdict.md and complete the triage table per INSTRUCTIONS.md section 10."
Write-Host "      Then run scripts/99-bootstrap-next.ps1 when ready for the next quarterly audit."
