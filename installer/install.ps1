# MCP Gateway installer for Windows.
# Downloads pre-built binaries from GitHub Releases with checksum verification,
# installs to %USERPROFILE%\.local\bin, and registers a Scheduled Task.
#
# Security note: checksums provide integrity verification (download corruption).
# Release checksums are signed with Sigstore cosign (keyless). To verify:
#   cosign verify-blob --bundle checksums.txt.bundle `
#     --certificate-identity-regexp 'https://github.com/blicksten/mcp-gateway/.github/workflows/release.yml@refs/tags/v.*' `
#     --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' checksums.txt
# For production deployments, pin a version: .\install.ps1 -Version v1.0.0
#Requires -Version 5.1
[CmdletBinding()]
param(
    [string]$Version = "latest"
)

$ErrorActionPreference = "Stop"

# Ensure TLS 1.2 for GitHub API (PS 5.1 may default to TLS 1.0)
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

$Repo = "blicksten/mcp-gateway"
$InstallDir = Join-Path $env:USERPROFILE ".local\bin"
$ConfigDir = Join-Path $env:USERPROFILE ".mcp-gateway"

# --- Detect architecture ---
$RawArch = $env:PROCESSOR_ARCHITECTURE
# Handle 32-bit PowerShell on 64-bit Windows
if ($RawArch -eq "x86" -and $env:PROCESSOR_ARCHITEW6432 -eq "AMD64") {
    $RawArch = "AMD64"
}
switch ($RawArch) {
    "AMD64"  { $Arch = "amd64" }
    "ARM64"  { $Arch = "arm64" }
    default  { Write-Error "Unsupported architecture: $RawArch (only amd64 and arm64 are supported)"; return }
}

# --- Determine latest version ---
if ($Version -eq "latest") {
    $Release = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest" -UseBasicParsing
    $Version = $Release.tag_name
    if (-not $Version) {
        Write-Error "Could not determine latest version."
        return
    }
}
Write-Host "Installing mcp-gateway $Version for windows/$Arch..."

# --- Download binary archive and checksums ---
$ArchiveName = "mcp-gateway_$($Version.TrimStart('v'))_windows_${Arch}.zip"
$BaseUrl = "https://github.com/$Repo/releases/download/$Version"
$TmpDir = Join-Path ([System.IO.Path]::GetTempPath()) "mcp-gateway-install-$(Get-Random)"
New-Item -ItemType Directory -Path $TmpDir -Force | Out-Null

try {
    Write-Host "Downloading $ArchiveName..."
    Invoke-WebRequest -Uri "$BaseUrl/$ArchiveName" -OutFile (Join-Path $TmpDir $ArchiveName) -UseBasicParsing
    Invoke-WebRequest -Uri "$BaseUrl/checksums.txt" -OutFile (Join-Path $TmpDir "checksums.txt") -UseBasicParsing

    # --- Checksum verification (MANDATORY) ---
    Write-Host "Verifying checksum..."
    $EscapedName = [regex]::Escape($ArchiveName)
    $ChecksumLine = Get-Content (Join-Path $TmpDir "checksums.txt") | Where-Object { $_ -match $EscapedName }
    if (-not $ChecksumLine) {
        Write-Error "Archive not found in checksums.txt"
        return
    }
    $ExpectedHash = ($ChecksumLine -split '\s+')[0].ToLower()
    $ActualHash = (Get-FileHash -Path (Join-Path $TmpDir $ArchiveName) -Algorithm SHA256).Hash.ToLower()

    if ($ExpectedHash -ne $ActualHash) {
        Write-Error "Checksum mismatch!`n  Expected: $ExpectedHash`n  Actual:   $ActualHash"
        return
    }
    Write-Host "Checksum OK."

    # --- Extract and install ---
    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
    Expand-Archive -Path (Join-Path $TmpDir $ArchiveName) -DestinationPath $TmpDir -Force
    Copy-Item -Path (Join-Path $TmpDir "mcp-gateway.exe") -Destination (Join-Path $InstallDir "mcp-gateway.exe") -Force
    Copy-Item -Path (Join-Path $TmpDir "mcp-ctl.exe") -Destination (Join-Path $InstallDir "mcp-ctl.exe") -Force

    # --- PATH idempotency ---
    $UserPath = [Environment]::GetEnvironmentVariable("PATH", "User")
    if ($UserPath -notlike "*$InstallDir*") {
        [Environment]::SetEnvironmentVariable("PATH", "$InstallDir;$UserPath", "User")
        Write-Host "Added $InstallDir to user PATH."
    }

    # --- Default config ---
    $ConfigFile = Join-Path $ConfigDir "config.json"
    if (-not (Test-Path $ConfigFile)) {
        New-Item -ItemType Directory -Path $ConfigDir -Force | Out-Null
        @'
{
  "backends": [],
  "listen": "127.0.0.1:8100"
}
'@ | Set-Content -Path $ConfigFile -Encoding UTF8
        Write-Host "Created default config at $ConfigFile"
    }

    # --- Scheduled Task registration ---
    # Privilege guard: warn if running as admin
    $IsAdmin = ([Security.Principal.WindowsPrincipal] [Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole(
        [Security.Principal.WindowsBuiltInRole]::Administrator)
    if ($IsAdmin) {
        Write-Warning "Running as Administrator. The scheduled task will be registered at user level (RunLevel = Limited). Consider running without elevation."
    }

    $TaskName = "MCP Gateway"
    $ExePath = Join-Path $InstallDir "mcp-gateway.exe"
    $Action = New-ScheduledTaskAction -Execute $ExePath -Argument "-config `"$ConfigFile`"" -WorkingDirectory $ConfigDir
    $Trigger = New-ScheduledTaskTrigger -AtLogOn
    $Settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1)
    $Principal = New-ScheduledTaskPrincipal -UserId ([Security.Principal.WindowsIdentity]::GetCurrent().Name) -LogonType Interactive -RunLevel Limited

    # Remove existing task if present (idempotency)
    if (Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue) {
        Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false
    }
    Register-ScheduledTask -TaskName $TaskName -Action $Action -Trigger $Trigger -Settings $Settings -Principal $Principal | Out-Null
    Write-Host "Registered scheduled task: $TaskName (RunLevel = Limited)"

    # --- Summary ---
    Write-Host ""
    Write-Host "=== MCP Gateway $Version installed ==="
    Write-Host "  Binaries: $InstallDir\mcp-gateway.exe, $InstallDir\mcp-ctl.exe"
    Write-Host "  Config:   $ConfigFile"
    Write-Host "  Task:     $TaskName (starts at logon)"
    Write-Host ""
    Write-Host "To start now: Start-ScheduledTask -TaskName '$TaskName'"
}
finally {
    Remove-Item -Path $TmpDir -Recurse -Force -ErrorAction SilentlyContinue
}
