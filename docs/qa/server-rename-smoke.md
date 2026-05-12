# SMOKE — Server Rename Feature

**Companion to:** [docs/PLAN-server-rename.md](../PLAN-server-rename.md)
**Source plan:** Phase 4 T4.1 (9-item manual E2E checklist, expanded from 6 to 9 per F-ARCH-3)
**Created:** 2026-05-12 by Porfiry [Opus 4.7]
**Target version:** Extension 1.33.5 (or later); Gateway daemon HEAD includes commits 5d21509 (Phase 1), a634b5c (Phase 2), d5d6157 (Phase 3).

---

## Pre-requisites

1. VSCode running with the installed `mcp-gateway-dashboard-latest.vsix`.
2. Operator has run `Developer: Reload Window` after install.
3. Gateway daemon running on `http://localhost:8765` (default).
4. At least one non-SAP backend MCP server registered in the gateway (e.g. `ctx7` from the catalog).
5. Optional: a SAP-named server (`vsp-DEV` or similar) for items 5 and the SAP-refusal cases.

---

## Checklist (9 items per F-ARCH-3)

### 1. Rename a server with no credentials

- [ ] Right-click `ctx7` in the **MCP Backends** tree view → **Rename Server**.
- [ ] Enter `context7-prod` in the input box.
- [ ] Confirm modal shows: "Rename ctx7 → context7-prod? Preserves: 0 env vars, 0 headers, 0 secrets." → click **Rename**.
- [ ] Verify:
  - Tree view updates to `context7-prod`.
  - `~/.claude/commands/ctx7.md` is deleted, `context7-prod.md` is created with the same slash-command content.
  - `curl http://localhost:8765/api/v1/servers/context7-prod` returns 200.
  - `curl http://localhost:8765/api/v1/servers/ctx7` returns 404.
  - Info toast: "Server renamed: ctx7 → context7-prod"

### 2. Rename a server WITH credentials

- [ ] Register a server (`ctx7`) and store an env-var secret via the detail panel (e.g. `BEARER_TOKEN=...`).
- [ ] Rename `ctx7` → `context7-prod`.
- [ ] Verify:
  - The detail panel for `context7-prod` shows the same env keys as `ctx7` had.
  - "Restart server" works under the new name (server starts cleanly).
  - VSCode SecretStorage no longer exposes secrets under `mcpGateway/ctx7/env/*`; they live under `mcpGateway/context7-prod/env/*`.
  - `listServerCredentials('ctx7')` returns `{env:[], headers:[]}` (verified via the detail panel or by inspecting the extension state).

### 3. Rename → ESC at confirm modal

- [ ] Right-click `ctx7` → **Rename Server** → enter `context7-prod` → press **ESC** on the confirm modal.
- [ ] Verify:
  - No rename happens (tree still shows `ctx7`).
  - No toast appears.
  - No HTTP request to `/api/v1/servers/*` was made (check daemon log).

### 4. Rename to a name that already exists (409)

- [ ] Register two servers: `ctx7` and `ctx8`.
- [ ] Rename `ctx7` → `ctx8`.
- [ ] Verify:
  - Error toast: "Rename failed: 409 already exists" (or matching wording).
  - Both `ctx7` and `ctx8` still exist in the tree.
  - Daemon log shows the 409 response from `PATCH /api/v1/servers/ctx7` body `{"new_name":"ctx8"}`.

### 5. Rename a SAP server (`vsp-DEV`) — both paths must refuse

- [ ] Verify the **Rename Server** menu item is **NOT** visible in the context menu when right-clicking a `vsp-DEV` row in the **SAP Systems** tree view (the `viewItem` regex excludes SAP `contextValue`s).
- [ ] Open the command palette (`Ctrl+Shift+P`) → run `MCP Gateway: Rename Server` with `vsp-DEV` as the implicit target (or via test harness):
  - Error toast: "Renaming SAP servers is not supported."
  - No `PATCH` request fires.
- [ ] Direct API call: `curl -X PATCH http://localhost:8765/api/v1/servers/vsp-DEV -H "Content-Type: application/json" -d '{"new_name":"foo"}'`:
  - Returns 400 with body `"renaming SAP-named servers is not supported"`.
- [ ] Cross-direction: `curl -X PATCH http://localhost:8765/api/v1/servers/ctx7 -H "Content-Type: application/json" -d '{"new_name":"vsp-XYZ"}'`:
  - Returns 400 with the same body.

### 6. Combined rename + env update via API

- [ ] `curl -X PATCH http://localhost:8765/api/v1/servers/ctx7 -H "Content-Type: application/json" -d '{"new_name":"context7-prod","add_env":["TIMEOUT=30s"]}'`.
- [ ] Verify:
  - Returns 200 `{"status":"patched","old_name":"ctx7","new_name":"context7-prod"}`.
  - On next start of `context7-prod`, `TIMEOUT=30s` is set in the child process environment.
  - Tree shows `context7-prod`.

### 7. **NEW** — Plan A rollback UX (F-ARCH-3)

- [ ] Register `ctx7` (any non-SAP server).
- [ ] Start rename `ctx7` → `context7-prod` via API: `curl -X PATCH http://localhost:8765/api/v1/servers/ctx7 -H "Content-Type: application/json" -d '{"new_name":"context7-prod"}'`.
- [ ] During the rename window (Plan A Step 2 — bounded by `lm.Stop` duration, up to ~9s stdio / ~2s HTTP), forcibly kill the daemon: `taskkill /F /IM mcp-gateway.exe` on Windows or `pkill -9 mcp-gateway` on POSIX.
- [ ] Restart the daemon: `mcp-ctl daemon start` (or VS Code Reload Window if autoStart is on).
- [ ] Verify:
  - Gateway tree shows the **OLD** name `ctx7` (cfg was not yet written to disk when the kill happened).
  - No zombie child process for `context7-prod` (the spawned process either auto-exited on parent loss or was reaped by the OS).
  - Daemon log (if available) contains an error or warning about an incomplete rename — exact wording may vary depending on which step the kill landed at.
- Note: This is a destructive test path. Restart the daemon afterwards; if the operator's `~/.claude.json` contains a stale `mcp-gateway:context7-prod` entry, run `claude mcp list` to confirm it cleared on next reflector tick.

### 8. **NEW** — Credential-migration failure UX (F-ARCH-3)

- [ ] Register `ctx7` with at least 2 env-var secrets.
- [ ] Put VSCode's SecretStorage into a degraded state (one platform-specific option below):
  - macOS: lock the user's keychain (System Settings → Passwords → Local Keychains → Lock).
  - Windows: revoke DPAPI for the test user (`certutil -decrypt ...` workaround) or temporarily delete the user's DPAPI master key.
  - Linux: revoke libsecret access (block `D-Bus` socket).
- [ ] Trigger rename `ctx7` → `context7-prod` from the UI.
- [ ] Verify:
  - Tree updates to `context7-prod` (gateway-side rename succeeded).
  - Warning toast: *"Server renamed to 'context7-prod' but {N} credential(s) could not be migrated. They remain under 'ctx7' in the keychain. Re-import KeePass or re-enter them manually."*
  - `mcp-ctl credential list` (if available) — or inspect the extension's credential-store index — shows secrets remain under `mcpGateway/ctx7/env/*`.
  - Subsequent restart of `context7-prod` logs "missing credentials" warnings (env keys reference `mcpGateway/context7-prod/env/*` per `cfg.Servers`, but those secrets don't exist yet).

### 9. **NEW** — `~/.claude.json` propagation (F-ARCH-3)

- [ ] Before rename: `cat ~/.claude.json | jq '.mcpServers'` (POSIX) or `Get-Content $env:USERPROFILE\.claude.json | jq ".mcpServers"` (PowerShell). Confirm `mcp-gateway:ctx7` HTTP entry exists.
- [ ] Trigger rename `ctx7` → `context7-prod` from the UI.
- [ ] Within the `cache.refresh()` + `claude-config-sync` polling window (~1–2 s):
  - `mcp-gateway:ctx7` is removed from `~/.claude.json::mcpServers`.
  - `mcp-gateway:context7-prod` is added with the same Bearer header reference.
- [ ] Verify Claude Code 2.x picks up the change without restart (the FS watcher on `~/.claude.json` notices the mtime bump). `claude mcp list` after a few seconds reflects `mcp-gateway:context7-prod`.
- [ ] The aggregate `/mcp` URL is unchanged so existing Claude Code MCP connections stay valid (the rename is transparent on the HTTP transport side).

---

## Pass/fail report template

When reporting back to the dev team after running the checklist, use this format:

```
Operator: <name>
Date: 2026-05-NN
Platform: macOS 14 / Windows 11 / Ubuntu 22.04
Extension version: 1.33.5
Daemon version: <output of mcp-ctl version>

Results:
  1. <PASS|FAIL>  no-creds rename
  2. <PASS|FAIL>  creds-preserved rename
  3. <PASS|FAIL>  ESC cancels rename
  4. <PASS|FAIL>  409 collision toast
  5. <PASS|FAIL>  SAP refusal (both menu + palette + API)
  6. <PASS|FAIL>  combined rename + env atomic
  7. <PASS|FAIL>  Plan A rollback UX
  8. <PASS|FAIL>  credential-migration failure UX
  9. <PASS|FAIL>  ~/.claude.json propagation

Notes:
  <any unexpected behaviour, exact toast wording observed, log excerpts>
```

Failures route to a follow-up cycle: revert Phase 3 (commit `d5d6157`) if the issue is UI-side, or Phase 1 (commit `5d21509`) if the gateway-side rename branch has a fundamental flaw.
