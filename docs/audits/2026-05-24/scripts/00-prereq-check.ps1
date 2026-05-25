# 00-prereq-check.ps1 - Verify all tools needed for the audit are installed.
# Output: ../outputs/prereq.log + populates ../inputs/tool-versions.txt
# Also updates manifest.json.tool_versions and manifest.json.git_head_at_start.
# Exit non-zero if a required tool is missing.

param(
    [string]$AuditPath = (Split-Path $PSScriptRoot -Parent)
)

$ErrorActionPreference = "Stop"
$outputDir = Join-Path $AuditPath "outputs"
$inputsDir = Join-Path $AuditPath "inputs"
$logPath = Join-Path $outputDir "prereq.log"
$versionsPath = Join-Path $inputsDir "tool-versions.txt"
$manifestPath = Join-Path $AuditPath "manifest.json"

New-Item -ItemType Directory -Force -Path $outputDir | Out-Null

# Per PAL review 2026-05-24 [H2]: Invoke-Expression replaced with Get-Command
# probe + & operator. Each tool defines:
#   - Executable: the command to look up (Get-Command)
#   - VersionArgs: args to fetch version (passed via call operator, not eval)
#   - Required: whether prereq fails when tool is missing
#   - VersionExitOK: array of acceptable exit codes when probing version
#     (some tools exit non-zero on version flags; ignored when listed)
$tools = @(
    @{ Name = "go";          Executable = "go";          VersionArgs = @("version");      Required = $true;  VersionExitOK = @(0) }
    @{ Name = "node";        Executable = "node";        VersionArgs = @("--version");    Required = $true;  VersionExitOK = @(0) }
    @{ Name = "npm";         Executable = "npm";         VersionArgs = @("--version");    Required = $true;  VersionExitOK = @(0) }
    # knip probe: try --version; if package not installed, Get-Command for npx is enough
    @{ Name = "knip";        Executable = "npx";         VersionArgs = @("--no-install", "knip", "--version"); Required = $false; VersionExitOK = @(0) }
    # deadcode: Phase 0 dependency. Probe via Get-Command only (deadcode -version is unreliable cross-versions).
    @{ Name = "deadcode";    Executable = "deadcode";    VersionArgs = @();               Required = $false; VersionExitOK = @() ;  ProbeMode = "presence" }
    @{ Name = "staticcheck"; Executable = "staticcheck"; VersionArgs = @("-version");     Required = $false; VersionExitOK = @(0) }
    @{ Name = "git";         Executable = "git";         VersionArgs = @("--version");    Required = $true;  VersionExitOK = @(0) }
)

$results = @()
$missing = @()
$toolVersionsObj = @{}

foreach ($t in $tools) {
    $cmdInfo = Get-Command -Name $t.Executable -ErrorAction SilentlyContinue
    if (-not $cmdInfo) {
        $results += "$($t.Name)=NOT_INSTALLED"
        $toolVersionsObj[$t.Name] = "NOT_INSTALLED"
        Write-Host "[MISSING] $($t.Name) (`'$($t.Executable)`' not found in PATH)" -ForegroundColor Yellow
        if ($t.Required) { $missing += $t.Name }
        continue
    }

    if ($t.ProbeMode -eq "presence") {
        $results += "$($t.Name)=PRESENT@$($cmdInfo.Source)"
        $toolVersionsObj[$t.Name] = "PRESENT"
        Write-Host "[OK] $($t.Name): present at $($cmdInfo.Source) (version probe skipped)"
        continue
    }

    # Run version probe via call operator with argument array (no Invoke-Expression)
    try {
        $versionOutput = & $t.Executable @($t.VersionArgs) 2>&1 | Out-String
        $exit = $LASTEXITCODE
        if ($t.VersionExitOK -notcontains $exit) {
            $results += "$($t.Name)=VERSION_PROBE_NON_ZERO_EXIT_$exit"
            $toolVersionsObj[$t.Name] = "VERSION_PROBE_FAILED"
            Write-Host "[WARN] $($t.Name): version probe exited $exit (tool may still be functional)" -ForegroundColor Yellow
            continue
        }
        $version = ($versionOutput -split "`r?`n" | Where-Object { $_.Trim() })[0]
        if (-not $version) { $version = "PRESENT_NO_VERSION_OUTPUT" }
        $results += "$($t.Name)=$version"
        $toolVersionsObj[$t.Name] = $version
        Write-Host "[OK] $($t.Name): $version"
    } catch {
        $results += "$($t.Name)=PROBE_ERROR: $($_.Exception.Message)"
        $toolVersionsObj[$t.Name] = "PROBE_ERROR"
        Write-Host "[ERR] $($t.Name): $($_.Exception.Message)" -ForegroundColor Yellow
        if ($t.Required) { $missing += $t.Name }
    }
}

# Capture git HEAD and root
$gitHead = $null
$gitRoot = $null
try {
    $here = $AuditPath
    while ($here -and -not (Test-Path (Join-Path $here ".git"))) {
        $here = Split-Path $here -Parent
    }
    if ($here -and (Test-Path (Join-Path $here ".git"))) {
        $gitRoot = $here
        Push-Location $gitRoot
        try {
            $gitHead = (git rev-parse HEAD 2>&1).Trim()
            if ($LASTEXITCODE -ne 0) { $gitHead = $null }
        } finally {
            Pop-Location
        }
    }
} catch {
    Write-Host "[WARN] git HEAD probe failed: $_" -ForegroundColor Yellow
}

if ($gitHead) {
    $results += "git_head=$gitHead"
    $toolVersionsObj["git_head"] = $gitHead
}
if ($gitRoot) {
    $results += "git_root=$gitRoot"
    $toolVersionsObj["git_root"] = $gitRoot
}

# Write inputs/tool-versions.txt — UTF-8 no BOM for cross-shell safety
$encoding = [System.Text.UTF8Encoding]::new($false)
[System.IO.File]::WriteAllText($versionsPath, ($results -join "`n") + "`n", $encoding)
[System.IO.File]::WriteAllText($logPath,      ($results -join "`n") + "`n", $encoding)

# Update manifest.json.tool_versions and git_head_at_start
if (Test-Path $manifestPath) {
    try {
        $manifest = Get-Content $manifestPath -Raw | ConvertFrom-Json
        $manifest | Add-Member -NotePropertyName tool_versions     -NotePropertyValue $toolVersionsObj -Force
        if ($gitHead) {
            $manifest | Add-Member -NotePropertyName git_head_at_start -NotePropertyValue $gitHead -Force
        }
        $manifest | ConvertTo-Json -Depth 10 | Out-File -Encoding utf8 -FilePath $manifestPath
    } catch {
        Write-Host "[WARN] could not update manifest.json: $_" -ForegroundColor Yellow
    }
}

if ($missing.Count -gt 0) {
    Write-Host "REQUIRED tools missing: $($missing -join ', ')" -ForegroundColor Red
    exit 1
}

Write-Host "Prereq OK. Versions written to inputs/tool-versions.txt and manifest.json."
exit 0
