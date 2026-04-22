#Requires -Version 5.1
<#
.SYNOPSIS
    Apply MCP Gateway patch to Claude Code VSCode extension webview.

.DESCRIPTION
    Idempotent — safe to run on every session start.
    Mirrors apply-mcp-gateway.sh semantics for Windows without Git Bash.

.PARAMETER Auto
    Hook mode — silent if already patched; exits 0 if extension not found.

.PARAMETER Uninstall
    Restore original index.js from .bak and remove patch marker.

.EXAMPLE
    .\apply-mcp-gateway.ps1
    .\apply-mcp-gateway.ps1 -Auto
    .\apply-mcp-gateway.ps1 -Uninstall
#>
param(
    [switch]$Auto,
    [switch]$Uninstall
)

$ErrorActionPreference = 'Stop'

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$JsPatch   = Join-Path $ScriptDir "porfiry-mcp.js"

# --- Find latest Claude Code extension (semantic version sort, PS5.1 compatible) ---
$ExtDir = $null
$ExtBase = Join-Path $env:USERPROFILE ".vscode\extensions"

$candidates = Get-ChildItem $ExtBase -Directory -Filter "anthropic.claude-code-*" -ErrorAction SilentlyContinue

if ($candidates -and $candidates.Count -gt 0) {
    try {
        # Attempt semantic version sort
        $sorted = $candidates | Sort-Object {
            [version]($_.Name -replace '^anthropic\.claude-code-', '')
        }
        $best = $sorted | Select-Object -Last 1
    } catch {
        # Fallback: lexicographic sort
        $best = $candidates | Sort-Object Name | Select-Object -Last 1
    }
    $webview = Join-Path $best.FullName "webview"
    if (Test-Path $webview -PathType Container) {
        $ExtDir = $webview
    }
}

if (-not $ExtDir) {
    if ($Auto) { exit 0 }
    Write-Error "Claude Code extension webview not found under $ExtBase\anthropic.claude-code-*"
    exit 1
}

$IndexJs = Join-Path $ExtDir "index.js"
$IndexBak = "$IndexJs.bak"

# --- Uninstall mode ---
if ($Uninstall) {
    if (Test-Path $IndexBak) {
        $content = Get-Content $IndexJs -Raw -Encoding UTF8 -ErrorAction SilentlyContinue
        if ($content -match 'MCP Gateway Patch v') {
            Copy-Item $IndexBak $IndexJs -Force
            Write-Host ('[MCP-GATEWAY] Uninstalled: restored ' + $IndexJs + ' from .bak')
        } else {
            Write-Host '[MCP-GATEWAY] Not patched -- nothing to uninstall'
        }
    } else {
        Write-Host '[MCP-GATEWAY] No .bak found -- cannot uninstall'
    }
    exit 0
}

# --- Re-patch if already patched: restore .bak first ---
$currentContent = Get-Content $IndexJs -Raw -Encoding UTF8 -ErrorAction SilentlyContinue
if ($currentContent -match 'MCP Gateway Patch v') {
    if ($Auto) { exit 0 }
    Write-Host "Already patched. Removing old patch first..."
    if (-not (Test-Path $IndexBak)) {
        Write-Error ".bak file missing -- cannot safely re-patch."
        exit 1
    }
    Copy-Item $IndexBak $IndexJs -Force
    $currentContent = Get-Content $IndexJs -Raw -Encoding UTF8
}

# Backup original (only if .bak does not exist -- preserve first clean backup)
if (-not (Test-Path $IndexBak)) {
    Copy-Item $IndexJs $IndexBak
}

# --- Read and validate gateway URL (S16.4-H1/H2 fix — prevents JS injection via string-literal break) ---
$GatewayUrl = $env:MCP_GATEWAY_URL
if ([string]::IsNullOrWhiteSpace($GatewayUrl)) {
    $GatewayUrl = "http://127.0.0.1:8765"
}
# Strict allowlist: http(s)://, hostname chars, optional :port, optional path with safe chars.
# Rejects quotes, backslash, backtick, $, &, |, ;, <, >, @, spaces, unicode.
if ($GatewayUrl -notmatch '^https?://[A-Za-z0-9.\-]+(:[0-9]+)?(/[A-Za-z0-9._~/%\-]*)?$') {
    Write-Error 'Invalid MCP_GATEWAY_URL -- must match http(s)://<host>[:<port>][/<path>] with no metachars'
    exit 1
}

# --- Read and validate auth token ---
$TokenFilePath = $env:MCP_GATEWAY_TOKEN_FILE
if ([string]::IsNullOrWhiteSpace($TokenFilePath)) {
    $TokenFilePath = Join-Path $env:USERPROFILE ".mcp-gateway\auth.token"
}

if (-not (Test-Path $TokenFilePath -PathType Leaf)) {
    if ($Auto) { exit 0 }
    Write-Error "Token file not found: $TokenFilePath"
    exit 1
}

$token = Get-Content $TokenFilePath -Raw -Encoding UTF8
$token = $token.Trim()

# Validate: only [A-Za-z0-9_.-] allowed (SP4-cross)
if ($token -notmatch '^[A-Za-z0-9_\-\.]+$') {
    Write-Error 'Invalid token format -- token must match ^[A-Za-z0-9_\-\.]+$'
    exit 1
}

if ($token.Length -eq 0) {
    Write-Error "Empty token in $TokenFilePath"
    exit 1
}

# --- Extract patch version from JS patch file first line ---
$jsPatchFirstLine = (Get-Content $JsPatch -TotalCount 1 -Encoding UTF8)
$versionMatch = [regex]::Match($jsPatchFirstLine, 'MCP Gateway Patch (v\d+\.\d+\.\d+)')
if (-not $versionMatch.Success) {
    Write-Error "Could not extract version from $JsPatch first line"
    exit 1
}
$PatchVersion = $versionMatch.Groups[1].Value

# --- Apply patch: byte-safe substitution via string.Replace (no cmd.exe path) ---
$patchContent = Get-Content $JsPatch -Raw -Encoding UTF8
$patchContent = $patchContent.Replace('__GATEWAY_URL__', $GatewayUrl)
$patchContent = $patchContent.Replace('__GATEWAY_AUTH_TOKEN__', $token)
$patchContent = $patchContent.Replace('__PATCH_VERSION__', $PatchVersion)

# Restore clean base before appending patch
Copy-Item $IndexBak $IndexJs -Force
$baseContent = Get-Content $IndexJs -Raw -Encoding UTF8

$combined = $baseContent + "`n" + $patchContent

# Write byte-safe (no BOM, no cmd.exe interpolation)
[System.IO.File]::WriteAllBytes($IndexJs, [System.Text.Encoding]::UTF8.GetBytes($combined))

# Placeholder survival guard
$written = Get-Content $IndexJs -Raw -Encoding UTF8
if ($written -match '__GATEWAY_URL__|__GATEWAY_AUTH_TOKEN__|__PATCH_VERSION__') {
    Copy-Item $IndexBak $IndexJs -Force
    Write-Error "Placeholder(s) still present in $IndexJs after substitution -- patch file may be corrupted"
    exit 1
}

# --- Lock down permissions (T16.4.1.a): current-user-only Protected DACL ---
# Mirrors internal/auth/token_perms_windows.go DACL pattern D:P(A;;FA;;;<SID>).
# S16.4-L1 fix: grant by SID (not env:USERNAME) — aligns with Go implementation which uses
# the current token's user SID, immune to rename/domain-prefix ambiguity.
# S16.4-M2 fix: if icacls fails, the file is left with inherited DACL (token readable by
# any local user) — roll back to .bak and exit 1 rather than fail-open.
$currentSid = [System.Security.Principal.WindowsIdentity]::GetCurrent().User.Value
$icaclsResult = & icacls "$IndexJs" /inheritance:r /grant:r "*${currentSid}:(F)" 2>&1
if ($LASTEXITCODE -ne 0) {
    Write-Warning ('[MCP-GATEWAY] icacls DACL lockdown failed (exit ' + $LASTEXITCODE + ') -- ' + ($icaclsResult -join '; ') + ' -- rolling back to .bak')
    Copy-Item $IndexBak $IndexJs -Force
    Write-Error 'icacls DACL lockdown failed; token-bearing file would be world-readable with inherited DACL. Aborting.'
    exit 1
}

if ($Auto) {
    $extName = Split-Path (Split-Path $ExtDir -Parent) -Leaf
    Write-Host ('[MCP-GATEWAY] Applied MCP Gateway patch ' + $PatchVersion + ' to ' + $extName)
    Write-Host '[MCP-GATEWAY] Reload VSCode to activate: Developer: Reload Window'
} else {
    Write-Host "Found: $ExtDir"
    Write-Host "Done. Reload VSCode: Developer: Reload Window"
    Write-Host "Patch version: $PatchVersion"
    Write-Host "Gateway URL: $GatewayUrl"
}
