# MCP Gateway — Roadmap

## Released

| Version | Date | Description |
|---------|------|-------------|
| v1.0.0 | 2026-04-09 | Go daemon + CLI (mcp-ctl) + VS Code extension + REST API + installers + cosign signing |
| v1.0.1 | 2026-04-14 | Security hardening: env key blocklist bypass fix, CRLF validation, URL validation, config permissions, MaxServers enforcement, disable-stops-process |

Phases 1–10.5 implemented. Full history preserved locally in `full-history-backup` branch.

---

## Backlog

### Phase 11 — Extension UX (v1.1.0)

11.0 COMPLETE (`7fbd4c0` — status bar foreground tinting). 11.A COMPLETE (`8650505`, 2026-04-15 — fingerprint-based diff refresh, inline start/stop/restart icons, `ServerDataCache.lastRefreshFailed`). 11.B COMPLETE (`6d059e2`, 2026-04-15 — sidebar `WebviewView` detail, MarkdownString tooltips, `McpStatusBar` cache migration). 11.C COMPLETE (`ec3aae7`, 2026-04-15 — `AddServerPanel` webview form replacing sequential InputBox flow, shared `src/validation.ts` module, platform-agnostic `isAbsolutePath`, CSP `form-action 'none'`, in-flight submit guard, exception-safe dispose ordering). 11.D COMPLETE (`44f0d7a`, 2026-04-15 — SAP hierarchical tree via new `SapComponentItem` + `sapGroupBySid` setting + live config watcher, new `AddSapPanel` webview form with absolute-path VSP/GUI executable fields + Set-based duplicate-detection). 11.E COMPLETE (`991121f`, 2026-04-16 — `SlashCommandGenerator` with promise queue, magic-header marker, transition detection, orphan cleanup, daemon outage protection; opt-in via `slashCommandsEnabled` setting; 24 tests).

| # | Task | Description |
|---|------|-------------|
| 11.1 | ✅ Diff-based tree refresh | Only fire TreeView update when data actually changed — eliminate flicker and scroll jumps. |
| 11.2 | ✅ Inline start/stop/restart buttons | Show control icons directly on each server row, no context menu needed. |
| 11.3 | ✅ Sidebar detail webview | Always-on `WebviewView` beneath tree views; re-renders on selection + cache refresh. |
| 11.4 | ✅ Status bar + tree item MarkdownString tooltips | Per-server breakdown with per-status sections; McpStatusBar migrated from `getHealth` polling to `ServerDataCache.onDidRefresh`. |
| 11.5 | ✅ Add Server webview form | Replace sequential InputBox prompts with a single form, auto-detect transport type. |
| 11.6 | ✅ SAP grouping toggle | Group SAP systems by SID with colored VSP/GUI icons, opt-in via settings. |
| 11.7 | ✅ Add SAP System flow | Webview form: SID + component checkboxes + absolute-path VSP/GUI executables. |
| 11.8 | ✅ Settings consolidation | All 7 settings already under `mcpGateway.*` (apiUrl, autoStart, daemonPath, pollInterval, sapGroupBySid, slashCommandsEnabled, slashCommandsPath); all `getConfiguration('mcpGateway')` calls, all commands, and storage key `mcpGateway.credentialIndex` verified namespace-consistent (audit 2026-04-16, no code changes). |
| 11.9 | ✅ Slash command generation | Auto-generate `.claude/commands/<server>.md` on server start, delete on stop. |

### Phase 12 — Auth + KeePass (v1.2.0)

| # | Task | Description |
|---|------|-------------|
| 12.1 | Bearer token auth | Daemon generates token on start, all mutating API requests require it. |
| 12.2 | Extension-side KeePass unlock | Unlock KeePass in extension via SecretStorage, never send master password to daemon. |
| 12.3 | Credential push via PATCH | Extension fetches credentials from KeePass, sends to daemon via existing PATCH API. |

### Phase 13 — Security Hardening (v1.3.0)

| # | Task | Description |
|---|------|-------------|
| 13.1 | POSIX process groups (F-5) | Create process groups for child processes on Linux/macOS to prevent orphans on daemon crash. |
| 13.2 | Config watcher race (F-6) | Guard AfterFunc in watcher against concurrent invocations — no duplicate reconciles. |
| 13.3 | TLS support (F-7) | Optional TLS for REST API on non-loopback binding to prevent cleartext traffic. |
| 13.4 | Log redaction (F-9) | Mask secrets (API keys, passwords, tokens) in child process stderr before streaming. |

### Phase 14 — Community & CI (v1.4.0)

| # | Task | Description |
|---|------|-------------|
| 14.1 | SECURITY.md | Responsible disclosure policy for contributors. |
| 14.2 | gitleaks in CI | Automated secret scanning in GitHub Actions on every push. |
| 14.3 | Server catalog | Built-in catalog of popular MCP servers (context7, pal, etc.) with auto-fill in Add Server. |
| 14.4 | Slash command catalog | Library of ready-made slash commands for common MCP servers. |

Detailed plan: `docs/PLAN-main.md` (audited [C+O] 2026-04-10, 0 MEDIUM+).
Full codebase audit: `docs/REVIEW-main.md` (audited [C+O] 2026-04-14, 13 findings fixed, 11 deferred to planned phases).

### Phase 11 Completion Summary (2026-04-16)

- **Status:** Phase 11 (Extension UX v1.1.0) — ALL sub-phases COMPLETE. Final commits: `8650505` (11.A) → `6d059e2` (11.B) → `ec3aae7` (11.C) → `44f0d7a` (11.D) → `991121f` (11.E) → `64f6057` (ROADMAP/REVIEW update). 11.8 audit 2026-04-16.
- **Unpushed commits on `main`:** `8650505`, `2a10e8b`, `6d059e2`, `ec3aae7`, `44f0d7a`, `991121f`, `64f6057` — push to GitLab (`git push origin main`) to publish v1.1.0 work.
- **Next plan:** Phase 12 (Auth + KeePass) — run `/phase 12` to create detailed task breakdown, then `/run main`.
- **PAL MCP status:** unavailable in this project; all cross-validation runs via sonnet sub-agent fallback per CLAUDE.md.
- **Known test-infra debt:** 31 pre-existing `GatewayClient` + `LogViewer` test failures need a local mock HTTP server — tracked as out-of-phase cleanup, not a Phase 11 blocker. The rebuilt VSIX (`vscode/mcp-gateway-dashboard/mcp-gateway-dashboard-latest.vsix`) must be installed manually via VSCode UI because `code --install-extension` fails on this machine's PATH.

---

## Known Limitations (LOW, documented)

| ID | Description |
|----|-------------|
| F-10 | KeePass master password in Go string cannot be zeroed due to GC — acceptable for localhost. |
| F-11 | bufio.Scanner 64KB line limit for stderr — sufficient for any realistic log output. |
| RF-3 | Gateway crash = all MCP servers lost — HA/clustering not planned (localhost daemon). |
