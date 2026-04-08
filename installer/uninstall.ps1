# MCP Gateway uninstaller for Windows.
# Stops the scheduled task, removes binaries and task registration.
# Config directory is preserved.
#Requires -Version 5.1
[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"

$InstallDir = Join-Path $env:USERPROFILE ".local\bin"
$ConfigDir = Join-Path $env:USERPROFILE ".mcp-gateway"
$TaskName = "MCP Gateway"

# --- Stop and remove scheduled task ---
$Task = Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
if ($Task) {
    if ($Task.State -eq "Running") {
        Stop-ScheduledTask -TaskName $TaskName
        Write-Host "Stopped scheduled task: $TaskName"
    }
    Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false
    Write-Host "Removed scheduled task: $TaskName"
}

# --- Remove binaries ---
foreach ($Binary in @("mcp-gateway.exe", "mcp-ctl.exe")) {
    $Path = Join-Path $InstallDir $Binary
    if (Test-Path $Path) {
        Remove-Item -Path $Path -Force
        Write-Host "Removed $Path"
    }
}

# --- Summary ---
Write-Host ""
Write-Host "=== MCP Gateway uninstalled ==="
Write-Host "Config preserved at $ConfigDir\"
Write-Host "To remove config: Remove-Item -Recurse -Force '$ConfigDir'"
