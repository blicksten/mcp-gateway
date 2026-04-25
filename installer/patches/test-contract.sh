#!/bin/bash
# Smoke-test for apply-mcp-gateway.sh env-var contract (Phase 0f).
#
# Tests:
#   A) New-only env (MCP_GATEWAY_URL) — no deprecation warning.
#   B) Legacy-only env (GATEWAY_URL) — deprecation warning on stderr.
# Both produce byte-identical output (same URL substituted).
#
# Does NOT touch any real Claude Code install — uses a temp dir with a
# fake index.js as the substitution target.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
APPLY="$SCRIPT_DIR/apply-mcp-gateway.sh"

# --- Create a minimal fake JS_PATCH so the script can extract a version ---
TMPDIR_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMPDIR_ROOT"' EXIT

FAKE_EXT_DIR="$TMPDIR_ROOT/.vscode/extensions/anthropic.claude-code-9.9.9/webview"
mkdir -p "$FAKE_EXT_DIR"
FAKE_INDEX="$FAKE_EXT_DIR/index.js"
echo "/* base */" > "$FAKE_INDEX"
# Provide a writable .bak so the script can operate without needing real file
cp "$FAKE_INDEX" "$FAKE_INDEX.bak"

FAKE_PATCH="$TMPDIR_ROOT/porfiry-mcp.js"
echo "/* MCP Gateway Patch v1.0.0 */ __GATEWAY_URL__ __GATEWAY_AUTH_TOKEN__ __PATCH_VERSION__" > "$FAKE_PATCH"

FAKE_TOKEN_FILE="$TMPDIR_ROOT/auth.token"
echo "testtoken123" > "$FAKE_TOKEN_FILE"

# Patch the script to use our fake home and patch dirs
# We override HOME so the script's find() resolves to our fake ext dir.
export HOME="$TMPDIR_ROOT"

# Override the patch location via symlink so the script finds porfiry-mcp.js
ln -sf "$FAKE_PATCH" "$SCRIPT_DIR/porfiry-mcp.js.testlink" 2>/dev/null || true
# The script uses SCRIPT_DIR/porfiry-mcp.js — we need to temporarily place
# a fake there, or create a wrapper that overrides the var. Since we cannot
# easily stub SCRIPT_DIR in a sourced script, we run the script in a subshell
# with a temp copy of the script directory.
FAKE_SCRIPT_DIR="$TMPDIR_ROOT/patches"
mkdir -p "$FAKE_SCRIPT_DIR"
cp "$APPLY" "$FAKE_SCRIPT_DIR/apply-mcp-gateway.sh"
cp "$FAKE_PATCH" "$FAKE_SCRIPT_DIR/porfiry-mcp.js"
APPLY_UNDER_TEST="$FAKE_SCRIPT_DIR/apply-mcp-gateway.sh"

PASS=0
FAIL=0

run_test() {
  local name="$1"; shift
  local expect_deprecation="$1"; shift
  # Restore fake index before each test run
  cp "$FAKE_INDEX.bak" "$FAKE_INDEX"
  cp "$FAKE_INDEX.bak" "$FAKE_EXT_DIR/index.js.bak"

  stderr_out="$TMPDIR_ROOT/stderr_${name}.txt"
  stdout_out="$TMPDIR_ROOT/stdout_${name}.txt"

  set +e
  env -i HOME="$TMPDIR_ROOT" PATH="$PATH" \
    "$@" \
    bash "$APPLY_UNDER_TEST" --auto \
    >"$stdout_out" 2>"$stderr_out"
  exit_code=$?
  set -e

  if [ "$exit_code" -ne 0 ]; then
    echo "FAIL [$name]: script exited $exit_code" >&2
    cat "$stderr_out" >&2
    FAIL=$((FAIL + 1))
    return
  fi

  if "$expect_deprecation"; then
    if grep -q "deprecated" "$stderr_out"; then
      : # expected
    else
      echo "FAIL [$name]: expected deprecation warning on stderr but got none" >&2
      FAIL=$((FAIL + 1))
      return
    fi
  else
    if grep -q "deprecated" "$stderr_out"; then
      echo "FAIL [$name]: unexpected deprecation warning: $(cat "$stderr_out")" >&2
      FAIL=$((FAIL + 1))
      return
    fi
  fi

  patched="$TMPDIR_ROOT/patched_${name}.txt"
  cat "$FAKE_INDEX" > "$patched"
  echo "PASS [$name]"
  PASS=$((PASS + 1))
}

# Case A: new-only env — no deprecation expected
run_test "new_env" false \
  MCP_GATEWAY_URL="http://localhost:9001" \
  MCP_GATEWAY_TOKEN_FILE="$FAKE_TOKEN_FILE"

# Restore for case B
cp "$FAKE_INDEX.bak" "$FAKE_INDEX"
cp "$FAKE_INDEX.bak" "$FAKE_EXT_DIR/index.js.bak"

# Case B: legacy-only env — deprecation expected
run_test "legacy_env" true \
  GATEWAY_URL="http://localhost:9001" \
  MCP_GATEWAY_TOKEN_FILE="$FAKE_TOKEN_FILE"

# Both patched files should contain the URL
A_OUT="$TMPDIR_ROOT/patched_new_env.txt"
B_OUT="$TMPDIR_ROOT/patched_legacy_env.txt"

if [ -f "$A_OUT" ] && [ -f "$B_OUT" ]; then
  if ! grep -q "http://localhost:9001" "$A_OUT"; then
    echo "FAIL [new_env]: URL not found in patched output" >&2
    FAIL=$((FAIL + 1))
  fi
  if ! grep -q "http://localhost:9001" "$B_OUT"; then
    echo "FAIL [legacy_env]: URL not found in patched output" >&2
    FAIL=$((FAIL + 1))
  fi

  # Both outputs (sans deprecation lines) should be byte-identical
  # Strip stderr deprecation lines — compare stdout patched results only
  if diff <(grep -v "deprecated" "$A_OUT" 2>/dev/null || cat "$A_OUT") \
          <(grep -v "deprecated" "$B_OUT" 2>/dev/null || cat "$B_OUT") >/dev/null 2>&1; then
    echo "PASS [byte-identical]: new_env and legacy_env outputs match"
    PASS=$((PASS + 1))
  else
    echo "FAIL [byte-identical]: new_env and legacy_env outputs differ" >&2
    FAIL=$((FAIL + 1))
  fi
fi

echo ""
echo "Results: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
