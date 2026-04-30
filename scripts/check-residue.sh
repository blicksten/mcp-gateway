#!/bin/bash
# check-residue.sh — verify a removed MCP server leaves no residue.
# Usage: bash scripts/check-residue.sh <server-name>
# Exits 0 if clean, non-zero summary count if residue detected.
#
# Per spike §6.7 (docs/spikes/2026-04-25-mcp-lifecycle-functional-test.md).
# Run AFTER `mcp-ctl servers remove <name>` to verify clean removal.

set -u

NAME="${1:-}"
if [ -z "$NAME" ]; then
    echo "usage: $0 <server-name>"
    exit 2
fi

TOK="$(cat ~/.mcp-gateway/auth.token 2>/dev/null)"
PLUGIN_ROOT="$HOME/.claude/plugins/cache/mcp-gateway-local/mcp-gateway"
WORKSPACE="${WORKSPACE:-$PWD}"

residue=0

echo "=== check-residue $NAME @ $(date -u +%Y-%m-%dT%H:%M:%SZ) ==="

# 1. config.json — server entry must be absent
echo "--- 1. config.json server entry ---"
val=$(jq -r ".servers.\"$NAME\" // \"absent\"" ~/.mcp-gateway/config.json)
if [ "$val" = "absent" ] || [ "$val" = "null" ]; then
    echo "  PASS (absent)"
else
    echo "  FAIL: still present in config.json"
    echo "  $val"
    residue=$((residue + 1))
fi

# 2. plugin .mcp.json — server entry must be absent
echo "--- 2. plugin .mcp.json server entry ---"
mcp_json="$(ls "$PLUGIN_ROOT"/*/.mcp.json 2>/dev/null | head -1)"
if [ -n "$mcp_json" ] && [ -f "$mcp_json" ]; then
    pval=$(jq -r ".mcpServers.\"$NAME\" // \"absent\"" "$mcp_json")
    if [ "$pval" = "absent" ] || [ "$pval" = "null" ]; then
        echo "  PASS (absent in $mcp_json)"
    else
        echo "  FAIL: still present in plugin .mcp.json"
        residue=$((residue + 1))
    fi
else
    echo "  SKIP (plugin .mcp.json not found at $PLUGIN_ROOT)"
fi

# 3. REST view — must 404
echo "--- 3. REST /api/v1/servers/$NAME ---"
http=$(curl -s -o /dev/null -w "%{http_code}" -H "Authorization: Bearer $TOK" "http://127.0.0.1:8765/api/v1/servers/$NAME")
if [ "$http" = "404" ]; then
    echo "  PASS (HTTP 404)"
else
    echo "  FAIL: HTTP $http (expected 404)"
    residue=$((residue + 1))
fi

# 4. SSE logs endpoint — must 404
echo "--- 4. REST /api/v1/servers/$NAME/logs ---"
http_logs=$(curl -s -o /dev/null -w "%{http_code}" -H "Authorization: Bearer $TOK" "http://127.0.0.1:8765/api/v1/servers/$NAME/logs")
if [ "$http_logs" = "404" ]; then
    echo "  PASS (HTTP 404)"
else
    echo "  FAIL: HTTP $http_logs (expected 404)"
    residue=$((residue + 1))
fi

# 5. tools cache — namespaced tools must be 0
echo "--- 5. tools cache (mcp-ctl tools list) ---"
count=$(mcp-ctl tools list --json 2>/dev/null | jq "[.[] | select(.name | startswith(\"$NAME\" + \"__\"))] | length")
if [ "$count" = "0" ]; then
    echo "  PASS (0 namespaced tools)"
else
    echo "  FAIL: $count namespaced tools still in cache"
    residue=$((residue + 1))
fi

# 6. slash command file — must be absent (only relevant if slashCommandsEnabled was on)
echo "--- 6. workspace slash command file ---"
slash="$WORKSPACE/.claude/commands/$NAME.md"
if [ ! -f "$slash" ]; then
    echo "  PASS (no slash command file at $slash)"
else
    echo "  INFO: slash file present at $slash (may be expected if slashCommandsEnabled=true)"
fi

# 7. child processes (Windows) — best-effort tasklist match by name
echo "--- 7. child processes matching $NAME ---"
if command -v powershell.exe >/dev/null 2>&1; then
    procs=$(powershell.exe -NoProfile -Command "Get-Process | Where-Object {\$_.MainWindowTitle -match '$NAME' -or \$_.ProcessName -match '$NAME'} | Measure-Object | ForEach-Object {\$_.Count}" 2>/dev/null | tr -d '\r\n')
    if [ "$procs" = "0" ] || [ -z "$procs" ]; then
        echo "  PASS (no processes matching name)"
    else
        echo "  INFO: $procs process(es) matched (may be unrelated; manual verification recommended)"
    fi
else
    echo "  SKIP (powershell.exe not available)"
fi

echo "=== $NAME residue check: $residue issues ==="
exit $residue
