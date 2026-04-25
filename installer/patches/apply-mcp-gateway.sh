#!/bin/bash
# Apply MCP Gateway patch to Claude Code VSCode extension webview.
# Idempotent — safe to run on every session start.
#
# Modes:
#   bash apply-mcp-gateway.sh             Interactive — verbose output, re-patches if needed
#   bash apply-mcp-gateway.sh --auto      Hook mode — silent if already patched, exits 0 if extension not found
#   bash apply-mcp-gateway.sh --uninstall Restore original index.js from .bak
#
# After first apply: Reload VSCode window (Developer: Reload Window)
#
# Canonical env vars (v1.10+):
#   MCP_GATEWAY_URL        — gateway base URL (preferred)
#   MCP_GATEWAY_TOKEN_FILE — filesystem path to the auth-token file (NEVER raw bytes)
# Legacy env vars (compat window v1.10..v2.0.0 — will be removed in v2.0.0):
#   GATEWAY_URL            — deprecated alias for MCP_GATEWAY_URL

set -euo pipefail

AUTO=false
UNINSTALL=false
for arg in "${@:-}"; do
  [ "$arg" = "--auto" ]      && AUTO=true
  [ "$arg" = "--uninstall" ] && UNINSTALL=true
done

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
JS_PATCH="$SCRIPT_DIR/porfiry-mcp.js"

# Find Claude Code extension directory (latest version by semantic sort)
EXT_DIR=""
if command -v sort >/dev/null 2>&1 && echo "1" | sort -V >/dev/null 2>&1; then
  # GNU sort -V available — use semantic version sorting
  EXT_DIR=$(find "$HOME/.vscode/extensions" -maxdepth 1 -name "anthropic.claude-code-*" -type d 2>/dev/null \
    | sort -V | tail -1)
  [ -d "${EXT_DIR:-}/webview" ] && EXT_DIR="$EXT_DIR/webview" || EXT_DIR=""
else
  # Fallback: lexicographic
  for d in "$HOME/.vscode/extensions"/anthropic.claude-code-*; do
    [ -d "$d/webview" ] && EXT_DIR="$d/webview"
  done
fi

if [ -z "$EXT_DIR" ]; then
  if $AUTO; then
    exit 0
  else
    echo "ERROR: Claude Code extension webview not found under $HOME/.vscode/extensions/anthropic.claude-code-*" >&2
    exit 1
  fi
fi

INDEX_JS="$EXT_DIR/index.js"

# --- Uninstall mode ---
if $UNINSTALL; then
  if [ -f "$INDEX_JS.bak" ]; then
    if grep -q "MCP Gateway Patch v" "$INDEX_JS" 2>/dev/null; then
      cp "$INDEX_JS.bak" "$INDEX_JS"
      echo "[MCP-GATEWAY] Uninstalled: restored $INDEX_JS from .bak"
    else
      echo "[MCP-GATEWAY] Not patched — nothing to uninstall"
    fi
  else
    echo "[MCP-GATEWAY] No .bak found — cannot uninstall"
  fi
  exit 0
fi

# --- Re-patch if already patched: restore .bak first ---
if grep -q "MCP Gateway Patch v" "$INDEX_JS" 2>/dev/null; then
  if $AUTO; then
    exit 0
  fi
  echo "Already patched. Removing old patch first..."
  if [ ! -f "$INDEX_JS.bak" ]; then
    echo "ERROR: .bak file missing — cannot safely re-patch." >&2
    exit 1
  fi
  cp "$INDEX_JS.bak" "$INDEX_JS"
fi

# Backup original (only if .bak doesn't exist — preserve first clean backup)
[ ! -f "$INDEX_JS.bak" ] && cp "$INDEX_JS" "$INDEX_JS.bak"

# --- Read and validate gateway URL (S16.4-H1/H2 fix — prevents JS injection via string-literal break) ---
# Prefer MCP_GATEWAY_URL (canonical v1.10+); fall back to legacy GATEWAY_URL during compat window.
if [ -n "${MCP_GATEWAY_URL:-}" ]; then
  RESOLVED_URL="$MCP_GATEWAY_URL"
elif [ -n "${GATEWAY_URL:-}" ]; then
  echo "WARN: GATEWAY_URL is deprecated, use MCP_GATEWAY_URL (will be removed in v2.0.0)" >&2
  RESOLVED_URL="$GATEWAY_URL"
else
  RESOLVED_URL="http://127.0.0.1:8765"
fi
GATEWAY_URL="$RESOLVED_URL"
# Strict allowlist: http(s)://, hostname chars, optional :port, optional path with safe chars.
# Rejects quotes, backslash, backtick, $, &, |, ;, <, >, @, spaces, newlines, unicode.
# Use bash native =~ so the regex is evaluated without awk's trailing-newline quirk
# (printf without trailing \n yields zero awk records, which wrongly exits 0).
URL_RE='^https?://[A-Za-z0-9.-]+(:[0-9]+)?(/[A-Za-z0-9._~/%-]*)?$'
if ! [[ "$GATEWAY_URL" =~ $URL_RE ]]; then
  echo "ERROR: invalid MCP_GATEWAY_URL — must match http(s)://<host>[:<port>][/<path>] with no metachars" >&2
  exit 1
fi

# --- Read and validate auth token ---
TOKEN_FILE="${MCP_GATEWAY_TOKEN_FILE:-$HOME/.mcp-gateway/auth.token}"
if [ ! -r "$TOKEN_FILE" ]; then
  if $AUTO; then
    exit 0
  else
    echo "ERROR: Token file not readable: $TOKEN_FILE" >&2
    exit 1
  fi
fi

# Read + robust trim (S16.4-L2 fix: strip all trailing whitespace, not just one LF/CR)
AUTH_TOKEN="$(cat "$TOKEN_FILE" | tr -d '\r' | awk 'NR==1 { sub(/[[:space:]]+$/, ""); sub(/^[[:space:]]+/, ""); print }')"

# Validate: only allow [A-Za-z0-9_.-] — reject any shell metachar or whitespace
case "$AUTH_TOKEN" in
  *[!A-Za-z0-9_.\-]*)
    echo "ERROR: invalid token format — token must match ^[A-Za-z0-9_\-\.]+$" >&2
    # Preserve .bak intact
    exit 1
    ;;
esac

# Guard against empty token
if [ -z "$AUTH_TOKEN" ]; then
  echo "ERROR: empty token in $TOKEN_FILE" >&2
  exit 1
fi

# --- Extract patch version from JS patch file first line ---
PATCH_VERSION="$(sed -n 's/.*MCP Gateway Patch \(v[0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*\).*/\1/p' "$JS_PATCH" | head -1)"
if [ -z "$PATCH_VERSION" ]; then
  echo "ERROR: could not extract version from $JS_PATCH first line" >&2
  exit 1
fi

# --- Apply patch via awk placeholder substitution (SP4-cross: never shell-interpolate token) ---
# S16.4-M1 fix: set restrictive umask BEFORE writing so newly-created files are 600 from birth
# (closes the race window where a token-bearing file was briefly world-readable between cp and chmod)
OLD_UMASK="$(umask)"
umask 077
cp "$INDEX_JS.bak" "$INDEX_JS"
# Use awk -v for all three substitutions — avoids any shell metachar injection risk
awk -v token="$AUTH_TOKEN" -v url="$GATEWAY_URL" -v ver="$PATCH_VERSION" \
  '{gsub("__GATEWAY_AUTH_TOKEN__", token); gsub("__GATEWAY_URL__", url); gsub("__PATCH_VERSION__", ver); print}' \
  "$JS_PATCH" >> "$INDEX_JS"
umask "$OLD_UMASK"

# Placeholder survival guard — if any placeholder survived, the patch file is corrupted
if grep -qE "__GATEWAY_URL__|__GATEWAY_AUTH_TOKEN__|__PATCH_VERSION__" "$INDEX_JS"; then
  echo "ERROR: placeholder(s) still present in $INDEX_JS after substitution — patch file may be corrupted" >&2
  cp "$INDEX_JS.bak" "$INDEX_JS"
  exit 1
fi

# Belt-and-suspenders: enforce 0600 even if umask was somehow bypassed (T16.4.1.a)
chmod 600 "$INDEX_JS"

if $AUTO; then
  echo "[MCP-GATEWAY] Applied MCP Gateway patch $PATCH_VERSION to $(basename "$(dirname "$EXT_DIR")")"
  echo "[MCP-GATEWAY] Reload VSCode to activate: Developer: Reload Window"
else
  echo "Found: $EXT_DIR"
  echo "Done. Reload VSCode: Developer: Reload Window"
  echo "Patch version: $PATCH_VERSION"
  echo "Gateway URL: $GATEWAY_URL"
fi
