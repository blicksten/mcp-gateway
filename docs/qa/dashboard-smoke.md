# Dashboard Smoke Checklist

> **Purpose:** Manual QA pass for the `mcp-gateway-dashboard` VSCode extension. Catches the failure modes that automated tests miss — primarily zombie-DOM regressions, contract drift between extension and external tools (Claude Code, mcp-ctl, daemon), and UX behaviors that only surface against a real four-state matrix (gateway up / down / 401 / externally-started).
>
> **Scope:** This is a snapshot at extension `v1.16.0` covering Phases 0–5 of the audit-dashboard plan. Run on every release that touches `vscode/mcp-gateway-dashboard/` or the Claude Code integration path (`installer/`, `cmd/mcp-ctl/install_claude_code.go`, daemon shutdown / health endpoints).
>
> **Time budget:** ≈ 30 minutes for the full pass.

## Preconditions

Run once at the start, document anything red here before starting the matrix:

- [ ] Build is reproducible: `npm run deploy` from `vscode/mcp-gateway-dashboard/` produces `mcp-gateway-dashboard-latest.vsix` and installs it into the local VSCode without errors.
- [ ] Reload window after install (`Developer: Reload Window`) — extension activates without errors in the Output → "MCP Gateway" channel.
- [ ] `mcp-ctl version` from a terminal returns the same minor version as the extension's `package.json`.
- [ ] `mcp-gateway --version` (the daemon binary) is on PATH and version-skew check passes (Phase 4 guardrail).

## Matrix axes

Each numbered item below is run in **all four columns**:

- **A. Gateway up:** daemon running on the configured `mcpGateway.apiUrl`, valid auth.token, ≥ 1 backend healthy.
- **B. Gateway down:** daemon stopped (`mcp-ctl daemon stop`), nothing listening on the port.
- **C. Invalid auth (401):** daemon up, but auth.token has been rotated so the extension's saved token is rejected.
- **D. Externally-started daemon:** daemon started outside VSCode (`mcp-ctl daemon start` from a terminal), so the extension's `daemon.running` is false even though a daemon IS reachable.

Mark `✓ PASS`, `✗ FAIL` (with bug ID), or `n/a` if the case does not apply (some commands only exist in some states — e.g. Stop is only meaningful in A/D).

> **Reset between rows:** click the gateway tree's "Refresh" action between cases to flush caches.

## Smoke matrix (30 items)

### Activation / poller (5 items)

| # | Check | A | B | C | D |
|---|-------|---|---|---|---|
| 1 | Extension activates within 2 s of `Reload Window`. Status bar shows `MCP: <state>` (not blank). | | | | |
| 2 | Status bar reflects state correctly: `MCP: ready` (A), `MCP: offline` (B), one-shot toast about token rotation (C), `MCP: ready` once Refresh hits (D). | | | | |
| 3 | Tree view "Gateway" node updates within one poll cycle (≤ 10 s) — no `Connecting to gateway…` placeholder lingering after first successful poll. | | | | |
| 4 | "Backends" tree shows the configured backends from daemon health (A) / "No MCP servers configured" placeholder (B/C) / populated after first refresh (D). | | | | |
| 5 | OutputChannel "MCP Gateway" contains a structured log line (severity + source) for each cache refresh and command — no silent `.catch(() => {})` swallowing (B-08, B-09 regression check). | | | | |

### Claude Code Integration panel (8 items)

| # | Check | A | B | C | D |
|---|-------|---|---|---|---|
| 6 | `MCP Gateway: Show Claude Code Integration` opens the webview. Banner is `Checking gateway…` initially, replaced within 10 s. | | | | |
| 7 | `#pluginStatus` resolves to `✔ Installed (vN, marketplace=…)` or `✘ Not installed` — never sticks at `● Checking…` (B-01 regression check). | | | | |
| 8 | `#patchStatus` resolves to `✔ Applied (vN)` / `✘ Not applied` / `⚠ Stale (vOld → vNew)` — never sticks at `—` (B-02 regression check). | | | | |
| 9 | `#channelStatus` shows the auto-reload channel state when applicable — never sticks at `—` (B-03 regression check). | | | | |
| 10 | `Polling gateway…` banner replaced by an actionable banner: red "daemon not running" (B), or yellow with restart-Claude-Code hint when sessions=0 but plugin installed (B-04). | | | | |
| 11 | "Activate for Claude Code" button is enabled, runs `mcp-ctl install-claude-code --api-url <cfg.apiUrl>` (B-NEW-23: argv carries `--api-url`), and streams the install log into `#activate-log`. | | | | |
| 12 | "Auto-reload plugins" toggle on → patch installer is invoked with `MCP_GATEWAY_URL=<cfg.apiUrl>` and `MCP_GATEWAY_TOKEN_FILE=<token path>` (NEW canonical env names; legacy `GATEWAY_URL`/`GATEWAY_AUTH_TOKEN` only emitted with deprecation warning, B-NEW-18 + B-NEW-31 regression check). | | | | |
| 13 | "Probe reconnect" posts the probe and toasts `Probe sent (nonce …)` within 1 s — does not freeze the panel for 15 s (B-11 regression check). | | | | |

### Daemon lifecycle commands (6 items)

| # | Check | A | B | C | D |
|---|-------|---|---|---|---|
| 14 | `MCP Gateway: Start Daemon` — succeeds (A: idempotent, no-op; B: starts; C: starts a fresh daemon; D: idempotent), all errors surface a toast with details (B-07 regression check). | | | | |
| 15 | `MCP Gateway: Stop Daemon` — calls REST `/shutdown` first regardless of who owns the process; succeeds in A and D, no-ops with `"No daemon process to stop"` in B, surfaces 401 in C (B-06 regression check). | | | | |
| 16 | `MCP Gateway: Restart Daemon` — REST shutdown + start; UI reports correct lifecycle. After restart, `daemon.running` is consistent with extension-owned vs externally-owned process (B-NEW-30 regression check). | | | | |
| 17 | Tree-view inline actions (▶ Start, ◼ Stop, ⟳ Restart) on the "Gateway" node trigger the same handlers as the command palette and produce identical results. | | | | |
| 18 | `mcp-ctl daemon stop` from a terminal followed by extension's "Refresh" — extension transitions to "offline" within one poll cycle and recovers when the daemon is restarted externally. | | | | |
| 19 | `Copy diagnostics` — clipboard contains the running daemon's actual `gatewayVersion` (Phase 4: pulled from `cache.gatewayHealth.version`, never `unknown`) (B-10 regression check). | | | | |

### Server / SAP / KeePass (6 items)

| # | Check | A | B | C | D |
|---|-------|---|---|---|---|
| 20 | `Add Server` webview opens, browse list is populated from the catalog, adding an entry persists to daemon and tree updates within one poll cycle. | | | | |
| 21 | Right-click on a backend → `Show Server Details` opens the detail panel. Removing the backend from another window (`mcp-ctl servers remove`) renders a `Server "X" was removed` banner; action buttons disable; panel auto-closes within 5 s (B-NEW-20 — Phase 8 ticket; expected to FAIL until Phase 8). | | | | |
| 22 | `Show Server Logs` opens the log viewer. 401 from the log endpoint produces a single `[log-viewer] HTTP 401 — auth token rejected` line and no silent reconnect spam. | | | | |
| 23 | SAP Systems view (when `mcpGateway.sapSystemsEnabled = true`) groups detected SAP backends by SID. Backends like `vsp-DEV` + `sap-gui-DEV-100` should merge into one row when both are present (B-NEW-27 — Phase 10 ticket; expected to FAIL until Phase 10). | | | | |
| 24 | `Import KeePass Credentials` runs to completion, imported entries appear as "stopped" in the SAP tree, no orphan SECRETS in `CredentialStore.reconcile()` after a second concurrent import (B-NEW-24 — Phase 10 ticket; expected to FAIL until Phase 10). | | | | |
| 25 | Slash-command files written to `mcpGateway.slashCommandsPath` are atomic (no truncated `.md` files after VSCode kill mid-write) — Phase 10 ticket (B-NEW-26); expected to FAIL until Phase 10. | | | | |

### Settings + auth (5 items)

| # | Check | A | B | C | D |
|---|-------|---|---|---|---|
| 26 | Editing `mcpGateway.apiUrl`, `pollInterval`, `autoStart`, `daemonPath`, `authTokenPath`, or `mcpCtlPath` in Settings prompts a `Reload Window` toast (B-NEW-22 — Phase 8 ticket; expected to FAIL until Phase 8). | | | | |
| 27 | Activate-for-Claude-Code from a VSCode window whose workspace is NOT the mcp-gateway repo: works when `mcpGateway.marketplaceJsonPath` is set or auto-detect succeeds; clear error toast otherwise (B-12 regression check). | | | | |
| 28 | `mcpGateway.verboseLogging = true` — every REST call to the daemon shows up in OutputChannel "MCP Gateway" with URL, status, elapsed ms (B-14 regression check). | | | | |
| 29 | Rotating `~/.mcp-gateway/auth.token` while the extension is running surfaces a one-shot toast with the `mcp-ctl install-claude-code --refresh-token` hint (B-NEW-19 regression check). The status bar must NOT silently say `MCP: offline` indefinitely. | | | | |
| 30 | Force-killing the daemon from Task Manager (Windows) → `mcp-ctl daemon start` after a few seconds → extension restart command does not mistake the successor daemon for "already running" (B-NEW-30 — Phase 10 ticket; expected to FAIL until Phase 10). | | | | |

## Reporting

After running the matrix:

1. Attach this file (rendered or filled in) to the release PR.
2. For every `✗ FAIL` row that is NOT a known phase ticket already on the plan, file a new bug under `docs/PLAN-audit-dashboard.md` with severity, repro steps, and the matrix cell that surfaced it.
3. Cross-check `npm test` output: zombie-DOM CI (`scripts/check-zombie-dom.js`, wired via `pretest`) must report `OK — claude-code-panel.ts (N ids, all wired …)`. Any `check-zombie-dom: ...` error blocks the release until either the wire is added or the id moves into `ZOMBIE_ALLOWLIST` with a justification.

## Acceptance for Phase 6 GATE

Phase 6's gate accepts this checklist when:
- The 30-item matrix has been run end-to-end against the current build at least once.
- Every `✗ FAIL` row maps to a known plan ticket OR a newly-filed bug; no orphan failures.
- The zombie-DOM CI passes with zero unjustified findings on the current `claude-code-panel.ts`.

## Future-proofing

When a new webview, command, or external-tool integration lands, add the corresponding row to this matrix. The 4-column matrix is intentionally fixed: it is the smallest set that surfaces contract-drift bugs (the audit's largest defect class) and any new integration must be exercisable against all four columns or an explicit `n/a` justification.
