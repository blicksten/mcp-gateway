# Plan: mcp-gateway Phases 11-14 (v1.1.0–v1.4.0)

## Context

v1.0.0 published to GitHub on 2026-04-09 (Go daemon + CLI + VS Code extension). Phases 1-10.5 complete. This plan covers four milestones: Extension UX (11), Auth + KeePass (12), Security Hardening (13), Community/CI (14). Based on comprehensive codebase exploration of both the TypeScript extension and Go daemon.

## Dependency Graph

```
Phase 11.A (tree stability) → 11.B (detail panel, tooltips)
Phase 11.C (Add Server webview) — independent
Phase 11.D (SAP improvements) — independent
Phase 11.A-D → 11.E (settings + slash commands)
Phase 12.A (bearer auth, Go) — standalone
Phase 12.A → 12.B (KeePass push, extension)
Phase 12.A → 13.B (TLS + redaction)
Phase 13.A (process groups + watcher) — standalone
Phase 14 (community/CI) — after 11.C, 11.E, 12.A
```

---

## Phase 11 — Extension UX (v1.1.0)

### 11.0 — Status Bar Color Refinement (COMPLETE — `7fbd4c0`)
**Goal:** Replace aggressive background colors with subtle foreground tinting.

- [x] T11.0.1: Replace `statusBarItem.errorBackground` / `warningBackground` with foreground ThemeColors (`testing.iconPassed`, `testing.iconFailed`, `notificationsWarningIcon.foreground`)
- [x] T11.0.2: "No servers" state shows neutral `$(circle-slash)` instead of `0/0`
- [x] T11.0.3: "Daemon offline" shows no color (not red) — it's not an error, just not running
- [x] T11.0.4: Same foreground approach for SAP status bar
- [x] T11.0.5: Tests updated (29 passing)

**Files:** `status-bar.ts`, `sap-status-bar.ts`, `mock-vscode.ts`, `status-bar.test.ts`, `sap-status-bar.test.ts`

### 11.A — Tree Stability + Inline Buttons (COMPLETE)
**Goal:** Eliminate tree flicker; add inline start/stop/restart icons.

- [x] T11A.1: Compact fingerprint in `BackendTreeProvider` (name|status|transport|restart_count|pid|last_error|tools.length per server)
- [x] T11A.2: Same fingerprint pattern for `SapTreeProvider` (per-system + vsp/gui status/restart_count/pid/last_error)
- [x] T11A.3: `ServerDataCache.lastRefreshFailed` getter + `CacheRefreshPayload` event payload
- [x] T11A.4-6: Inline `startServer` (disabled), `stopServer` (running/degraded), `restartServer` (running/degraded/error/stopped) in `package.json` with icons
- [x] T11A.7-9: Fingerprint tests (identical→no fire, status→fire, restart_count→fire, pid→fire, last_error→fire, dispose→null)
- [x] T11A.GATE: 278 passing (+11 new tests); 31 pre-existing failures unchanged (GatewayClient + LogViewer need live daemon); sonnet cross-review found 1 MEDIUM (SapTreeProvider fingerprint missing pid/last_error), fixed + regression tests added

**Files:** `backend-tree-provider.ts`, `sap-tree-provider.ts`, `package.json`
**Rollback:** Revert snapshot comparison; remove inline menu entries.

### 11.B — Sidebar Detail View + Rich Tooltips (COMPLETE)
**Goal:** Always-on sidebar `WebviewView` showing details of currently-selected tree item; `MarkdownString` tooltips on status bars and tree items; migrate `McpStatusBar` from timer polling to cache events.

- [x] T11B.0: `MockMarkdownString` + `MockWebviewView` + `registerWebviewViewProvider` stub + `createTreeView().onDidChangeSelection` stub in test harness.
- [x] T11B.1: Registered new view `mcpGateway.serverDetail` in `package.json` inside existing `mcp-gateway` view container (type `webview`).
- [x] T11B.2: New `src/webview/server-detail-view-provider.ts` implementing `vscode.WebviewViewProvider`. Selection API: `setMcpSelection`, `setSapSelection`, `clearSelection`. Renders `buildMcpDetailHtml` / `buildSapDetailHtml` / `buildDetailPlaceholderHtml` depending on current state. `renderSeq` generation counter guards every HTML write to prevent stale async renders from clobbering newer selections.
- [x] T11B.3: Provider subscribes to `cache.onDidRefresh` (constructor); extension wires `backendTreeView.onDidChangeSelection` + `sapTreeView.onDidChangeSelection` → provider selection setters. `BackendItem instanceof` + `SapSystemItem instanceof` guards in selection handlers.
- [x] T11B.4: `buildMcpDetailHtml` / `buildSapDetailHtml` already pure (data-in / HTML-out); added new `buildDetailPlaceholderHtml(nonce, cspSource)` for empty state.
- [x] T11B.5: `registerWebviewViewProvider` wired in `extension.ts activate()`. Provider + registration + both tree-view selection subscriptions pushed to `context.subscriptions`.
- [x] T11B.6: `McpStatusBar.buildTooltip` → `MarkdownString` with summary + per-status server lists (`running`, `degraded`, `error`, `restarting`, `starting`, `stopped`, `disabled`).
- [x] T11B.7: `SapStatusBar.buildTooltip` → `MarkdownString` with per-system VSP/GUI breakdown.
- [x] T11B.8: `McpStatusBar` rewritten: takes `ServerDataCache`, subscribes to `onDidRefresh`, no timer/`poll`/`startPolling`/`stopPolling`. Counts `cache.getMcpServers()` (excludes SAP — intentional UX change since SAP has its own `SapStatusBar`). Uses `cache.lastRefreshFailed` for daemon-offline detection (no separate `/health` call).
- [x] T11B.9: `BackendItem.tooltip` + `SapItem.tooltip` → `MarkdownString` with status, transport, pid, restart_count, last_error, tool count (SAP items include per-component VSP/GUI breakdown).
- [x] T11B.10: 14 new tests in `src/test/webview/server-detail-view-provider.test.ts` covering placeholder, MCP/SAP selection rendering, cache-refresh re-render, message routing + rejection, CSP, dispose, and a dedicated concurrent-render race test using a deferred `credentialStore`.
- [x] T11B.11: `src/test/status-bar.test.ts` rewritten (20 tests on cache-driven API); `src/test/backend-tree-provider.test.ts` tooltip assertions updated for `MockMarkdownString.value`.
- [x] T11B.12: `no legacy polling API` negative test + dispose idempotency test in `src/test/status-bar.test.ts`.
- [x] T11B.GATE: **276 passing, 0 failures in 513ms** (excluding pre-existing daemon-dependent `gateway-client.test.ts` + `log-viewer.test.ts` which need live localhost:8765; unchanged since 11.A). PAL MCP unavailable → Sonnet 4.6 Agent-tool fallback for cross-model codereview + thinkdeep. First pass: 1 HIGH (concurrent render race) + 4 MEDIUM (apply-payload hygiene, MarkdownString ctor inconsistency, discarded message disposable, escapeMd duplicated without regex comment). All five fixed; second Sonnet pass: **APPROVE, zero MEDIUM+**.

**Files:** `package.json`, `src/webview/server-detail-view-provider.ts` (new), `src/webview/html-builder.ts`, `src/status-bar.ts`, `src/sap-status-bar.ts`, `src/backend-item.ts`, `src/sap-item.ts`, `src/extension.ts`, `src/markdown-utils.ts` (new), `src/test/mock-vscode.ts`, `src/test/webview/server-detail-view-provider.test.ts` (new), `src/test/status-bar.test.ts`, `src/test/backend-tree-provider.test.ts`
**Rollback:** Revert ServerDetailViewProvider registration and tree-view selection wiring in `extension.ts`; restore `McpStatusBar` constructor to accept `IGatewayClient`; revert tooltip fields to plain strings. Legacy `ServerDetailPanel` / `SapDetailPanel` remain functional via context-menu Show Details action.

### 11.C — Add Server Webview Form (COMPLETE)
**Goal:** Replace sequential InputBox with single webview form, auto-detect transport.

- [x] T11C.0: New `src/validation.ts` — single source of truth for SERVER_NAME_RE / ENV_KEY_RE / HEADER_NAME_RE + platform-agnostic `isAbsolutePath` (string methods, no Node `path` dependency), plus helper validators. `credential-store.ts` now imports regexes from there (replacing 3 local copies).
- [x] T11C.1: New `src/webview/add-server-panel.ts` — singleton `AddServerPanel` class with `createOrShow(extensionUri, client, credentialStore, onCreated)` factory. Includes in-flight submit guard, mutable `onCreated` (refreshed on re-reveal), and dispose-first / callback-in-try-catch ordering.
- [x] T11C.2: Added `buildAddServerHtml(nonce, cspSource)` to `html-builder.ts`. CSP forbids `'unsafe-inline'` and includes `form-action 'none'`. Embedded script builds `SERVER_NAME_RE`/`ENV_KEY_RE`/`HEADER_NAME_RE` via `new RegExp(jsonForScript(canonical.source))` — single source of truth for the regex patterns.
- [x] T11C.3: Client-side validation in the embedded script: `detectTransport` auto-detects on every keystroke; `isAbsolutePath` mirrors the string-based TS helper; per-field inline error spans; form-level error banner with `role="alert"`.
- [x] T11C.4: `extension.ts addServer` command now invokes `AddServerPanel.createOrShow`. Removed the `collectKeyValuePairs` helper + sequential `InputBox`/`QuickPick` flow. Re-exports `validateServerName`/`validateUrl`/`SERVER_NAME_RE` from `./validation` for backward compatibility.
- [x] T11C.5: `handleSubmit` re-validates every field via shared helpers (never trusts webview), ignores any submitted `transport` field and always recomputes via `detectTransport(target)` to prevent crafted-payload nack confusion, then calls `client.addServer` followed by partial-failure-tolerant credential indexing.
- [x] T11C.6-8: Tests — `src/test/validation.test.ts` (platform-agnostic absolute path, regex parity with webview HTML, CSP assertion) + `src/test/webview/add-server-panel.test.ts` (lifecycle, transport auto-detect, credentials, server-side re-validation, client failure, concurrency/lifecycle for the 3 HIGH fixes) + `src/test/commands.test.ts` updated to assert the command opens `mcpAddServer` panel rather than the old InputBox flow.
- [x] T11C.GATE — `npm test`: **352 passing, 31 pre-existing daemon-dependent failures** (unchanged since 11.A/11.B; all in gateway-client.test.ts + log-viewer.test.ts). +76 new passing tests in 11.C. PAL MCP unavailable → Sonnet 4.6 Agent-tool fallback. First pass: 3 HIGH (concurrent submit, stale onCreated on re-reveal, onCreated before dispose exception-safety) + 5 MEDIUM (credential-store regex duplication, Node `path.isAbsolute` cross-platform asymmetry, crafted transport-field mismatch, CSP missing `form-action 'none'`, webview-script regex duplication with escape hazard). **All 8 fixed.** Second Sonnet pass: **APPROVE, zero MEDIUM+**. Five LOW observations documented only (test-name clarity, 3 untested `coercePayload` shape branches, UNC parity between TS/JS copies, double-escape maintainability comment).

**Files:** new `src/validation.ts`, new `src/webview/add-server-panel.ts`, new `src/test/validation.test.ts`, new `src/test/webview/add-server-panel.test.ts`, `src/webview/html-builder.ts`, `src/extension.ts`, `src/credential-store.ts`, `src/test/commands.test.ts`
**Rollback:** Revert addServer command to InputBox flow; delete add-server-panel.ts + validation.ts; restore local regex copies in credential-store.ts.

### 11.D — SAP Improvements (COMPLETE)
**Goal:** Hierarchical SID grouping toggle; Add SAP System webview.

- [x] T11D.1: `mcpGateway.sapGroupBySid` setting (boolean, default false) — added to package.json configuration.
- [x] T11D.2-3: New `SapComponentItem` TreeItem (`sap-vsp-<status>` / `sap-gui-<status>` contextValue), hierarchical `SapSystemItem` flag (`sap-group-<status>` + Collapsed), hierarchical `SapTreeProvider.getChildren()` with parent → VSP/GUI children. Fingerprint gains `H;`/`F;` prefix so mode toggle rebuilds tree.
- [x] T11D.4: Live config watcher in SapTreeProvider constructor — `workspace.onDidChangeConfiguration('mcpGateway.sapGroupBySid')` triggers a forced refresh.
- [x] T11D.5: Tightened package.json when-clauses — flat-mode arm explicitly enumerates statuses `sap-(running|stopped|error|degraded|disabled|starting|restarting)` so it does NOT match `sap-vsp-*`/`sap-gui-*`/`sap-group-*` and cause duplicate menu entries on child rows (round-1 cross-review H-1 fix).
- [x] T11D.6-7: New singleton `AddSapPanel` (`src/webview/add-sap-panel.ts`) with `buildAddSapHtml` (new function in `html-builder.ts`). Form fields: SID (auto-uppercased), Client (optional, 3 digits), VSP/GUI component checkboxes, VSP executable path, GUI executable path (absolute paths required when the corresponding component is checked — round-1 F2 fix). In-flight submit guard, mutable `onCreated`, dispose-first callback-in-try-catch ordering, server-side re-validation of SID/Client/executable paths, `Set<string>`-based `warnedDuplicateKeys` for cross-SID-remembering duplicate-detection confirmation (round-1 M-1 fix).
- [x] T11D.8: `mcpGateway.addSapSystem` command registered + `view/title` `+` icon on `mcpSapSystems`. Extension-side command handlers (`restartSapVsp`, `restartSapGui`, `showSapVspLogs`, `showSapGuiLogs`, `showSapDetail`) now accept either `SapSystemItem` or `SapComponentItem` via a new `resolveSapServer(item, kind)` helper. `sapTreeView.onDidChangeSelection` propagates `SapComponentItem` selections to the sidebar detail view via `item.system` (round-1 H-2 fix).
- [x] T11D.9-11: Tests — new `src/test/sap-item.test.ts` (13 tests for flat/hierarchical SapSystemItem + SapComponentItem contextValue/tooltip/labels), extended `src/test/sap-tree-provider.test.ts` with 9 hierarchical-mode tests (including live `onDidChangeConfiguration` fire via new `fireConfigChange` mock helper), new `src/test/webview/add-sap-panel.test.ts` (27 tests across lifecycle / happy-path submit / server-side validation / duplicate detection with 3-submit Set remembering / concurrency / command validation), SAP commands added to `src/test/commands.test.ts` expectedCommands list, new dispatch tests exercising `resolveSapServer` via both input types, new `validateSapSid`/`validateSapClient` + `buildAddSapHtml` regex parity tests in `src/test/validation.test.ts`. `mock-vscode.ts` extended with `mockConfigValues` registry, fire-able `onDidChangeConfiguration`, and `fireConfigChange(key)` helper.
- [x] T11D.GATE — `npm test`: final round after all fixes — details in final TASKS-main.md summary. PAL MCP unavailable → Sonnet 4.6 Agent-tool fallback. **Round 1:** 3 HIGH + 5 MEDIUM + 3 LOW — all 11 fixed. **Round 2:** 1 MEDIUM (showSapDetail when-clause missing `sap-vsp-|sap-gui-` — fixed) + 1 MEDIUM FALSE POSITIVE claimed by both agents against the webview-JS UNC path check but verified correct by extracting the emitted function via `node -e` and executing it against UNC/Windows/POSIX samples (both return true as expected). Added 3 new parity tests (`webview isAbsolutePath runtime parity`) that extract the webview JS via `new Function(...)` and assert behavior matches the TS `isAbsolutePath` exactly — prevents future reviewers from repeating the escape-counting error. **Round 3 pass:** zero MEDIUM+ after N-1 fix.

**Files:** `src/sap-tree-provider.ts`, `src/sap-item.ts`, new `src/webview/add-sap-panel.ts`, `src/webview/html-builder.ts`, `src/validation.ts`, `src/extension.ts`, `package.json`, new `src/test/sap-item.test.ts`, new `src/test/webview/add-sap-panel.test.ts`, `src/test/sap-tree-provider.test.ts`, `src/test/validation.test.ts`, `src/test/commands.test.ts`, `src/test/mock-vscode.ts`
**Rollback:** Revert to flat mode; delete AddSapPanel; remove setting; revert validation.ts additions.

### 11.E — Settings + Slash Commands (COMPLETE)
**Goal:** Auto-generate `.claude/commands/<server>.md` on server start/stop.

- [x] T11E.0: Extend test harness with tmpdir filesystem helper (`src/test/helpers/tmpdir.ts`)
- [x] T11E.1: Add `slashCommandsEnabled` (boolean, default false) + `slashCommandsPath` (string, default `${workspaceFolder}/.claude/commands`) settings
- [x] T11E.2-10: New `slash-command-generator.ts` — `SlashCommandGenerator` class with promise queue, magic-header marker, transition detection, orphan cleanup, `SERVER_NAME_RE` path traversal guard
- [x] T11E.11: Wire generator in `extension.ts activate()` with live `onDidChangeConfiguration` listener
- [x] T11E.12-17: 22 unit tests covering resolveCommandsDir, generate/overwrite/skip, delete with marker guard, orphan cleanup, transition detection, queue serialization, enable/disable lifecycle
- [x] T11E.GATE: 439 passing, 0 failures (503ms). PAL codereview PASS (1 LOW — silent error swallowing, by-design). PAL thinkdeep PASS (0 findings). Expert codereview raised 1 HIGH (seeding) + 1 MEDIUM (multi-root) — both by-design per REFINEMENT E-3 and E-1.

**Files:** new `src/slash-command-generator.ts`, new `src/test/helpers/tmpdir.ts`, new `src/test/slash-command-generator.test.ts`, `src/extension.ts`, `package.json`
**Rollback:** Remove generator; delete settings.

---

## Phase 12 — Auth + KeePass (v1.2.0)

### 12.A — Bearer Token Auth (Go)
**Goal:** Daemon generates random token; all mutating endpoints require `Authorization: Bearer <token>`.

- T12A.1-2: New `internal/api/auth.go` — `GenerateToken()` (32 bytes crypto/rand), `BearerAuthMiddleware`
- T12A.3-4: Exempt GET read-only endpoints; mount middleware on POST/PATCH/DELETE
- T12A.5-6: `--no-auth` flag; write token to `~/.mcp-gateway/auth-token` (0600)
- T12A.7-9: Extension `GatewayClient` + CLI `mcp-ctl` read token file, pass in headers
- T12A.10-12: Tests (Go + TS)
- GATE: tests + codereview + thinkdeep — zero MEDIUM+

**Files:** new `internal/api/auth.go`, `server.go`, `main.go`, `gateway-client.ts`, `extension.ts`, `cmd/mcp-ctl/main.go`
**Rollback:** Remove middleware; revert clients. Backward-compatible.

### 12.B — KeePass Credential Push (Extension)
**Goal:** Extension shells out to `mcp-ctl credential import` and pushes credentials via PATCH.

- T12B.1-4: New `keepass-importer.ts` — exec mcp-ctl, parse output, PATCH servers, store in SecretStorage
- T12B.5-6: Register command + tests
- GATE: tests + codereview + thinkdeep — zero MEDIUM+

**Files:** new `keepass-importer.ts`, `extension.ts`, `package.json`
**Rollback:** Remove command; delete importer.

---

## Phase 13 — Security Hardening (v1.3.0)

### 13.A — POSIX Process Groups + Config Watcher Fix
**Goal:** Prevent orphan child processes on Linux/macOS; fix concurrent onChange race.

- T13A.1-2: `procattr_other.go` — `Setpgid: true`; `Stop()` sends SIGTERM to `-pid` (process group)
- T13A.3-4: `watcher.go` — `sync.Mutex` around `onChange` in AfterFunc callback + ctx.Err() check
- T13A.5-6: Tests
- GATE: tests + codereview + thinkdeep — zero MEDIUM+

**Files:** `procattr_other.go`, `manager.go`, `watcher.go`
**Rollback:** Revert procattr to no-op; remove mutex.

### 13.B — TLS + Log Redaction
**Goal:** Optional TLS for REST API; mask secrets in stderr logs.

- T13B.1-4: `GatewaySettings.TLSCert/TLSKey`; `ServeTLS()` branch; required for non-loopback
- T13B.5-7: New `internal/logbuf/redact.go` — pattern-match Bearer tokens, API keys, base64 blobs → `***REDACTED***`; apply in `Ring.Write()`
- T13B.8-10: Wiring + tests (self-signed cert integration test)
- GATE: tests + codereview + thinkdeep — zero MEDIUM+

**Files:** `types.go`, `server.go`, new `logbuf/redact.go`, `ring.go`, `main.go`
**Rollback:** Remove TLS branch; remove redaction. Both additive.

---

## Phase 14 — Community & CI (v1.4.0)

Single phase — documentation + CI + catalogs.

- T14.1: `SECURITY.md` — responsible disclosure policy
- T14.2-3: `.gitleaks.toml` + `gitleaks` job in `ci.yml`
- T14.4-5: Server catalog + command catalog JSON files
- T14.6-7: Catalog browse in Add Server webview; template enrichment in slash command generator
- T14.8: README update (auth, TLS, slash commands, catalogs)
- GATE: tests + codereview + thinkdeep — zero MEDIUM+

**Rollback:** Delete new files; revert CI.

---

## Verification

Each GATE step runs:
1. `go test ./...` — 0 failures (Go phases)
2. `npm test` in vscode/mcp-gateway-dashboard — 0 failures (TS phases)
3. `go vet ./...` — clean
4. PAL codereview + thinkdeep — zero MEDIUM+ findings
5. Manual verification where applicable (webview forms, tooltip rendering)

## Next Plans

| Phase | Title | Status | Goal |
|-------|-------|--------|------|
| 11 | Extension UX | ✅ COMPLETE | Eliminate flicker, inline buttons, webview forms, slash commands |
| 12 | Auth + KeePass | 📋 PLANNED | Bearer token auth, extension-side KeePass import |
| 13 | Security Hardening | 📋 PLANNED | POSIX process groups, TLS, log redaction |
| 14 | Community & CI | 📋 PLANNED | SECURITY.md, gitleaks, server/command catalogs |


---

## Phase 11 Design Validation (architect, 2026-04-15)

**Verdict:** **APPROVE_WITH_REFINEMENTS**

The overall design is sound and aligns with existing patterns in the codebase. All five sub-phases (11.A through 11.E) are implementable without architectural upheaval. However, several unstated assumptions, missing details, and mock/test-infra gaps must be addressed before implementation. None rise to CRITICAL/HIGH severity -- all are MEDIUM-at-most and can be resolved during dev-lead task breakdown.

### Verification Evidence (files read + line ranges)

- vscode/mcp-gateway-dashboard/src/backend-tree-provider.ts (1-39) -- getChildren() maps cache.getMcpServers() to BackendItem, no diff today; refresh() unconditionally fires event emitter.
- vscode/mcp-gateway-dashboard/src/sap-tree-provider.ts (1-38) -- same pattern; getChildren(element) returns [] when element supplied (flat list).
- vscode/mcp-gateway-dashboard/src/server-data-cache.ts (1-78) -- ServerDataCache exists with onDidRefresh, getMcpServers(), getSapSystems(), getAllServers(). Fail-clear on refresh errors (lines 29-34): exceptions clear cachedServers to empty array.
- vscode/mcp-gateway-dashboard/src/status-bar.ts (1-93) -- McpStatusBar polls client.getHealth() on its own timer (not cache-driven). Migration target confirmed.
- vscode/mcp-gateway-dashboard/src/sap-status-bar.ts (1-73) -- already consumes ServerDataCache via onDidRefresh. Tooltip is plain string.
- vscode/mcp-gateway-dashboard/src/backend-item.ts (1-56) -- contextValue = server.status. No command set. buildTooltip returns plain string.
- vscode/mcp-gateway-dashboard/src/sap-item.ts (1-56) -- collapsibleState: None, contextValue = sap-<status>. No command set.
- vscode/mcp-gateway-dashboard/src/webview/server-detail-panel.ts (1-124) -- EXISTS (Phase 8.4). createOrShow reveals existing panel; static panels map keyed by server name.
- vscode/mcp-gateway-dashboard/src/webview/html-builder.ts (1-203) -- established webview pattern: CSP with nonce, escapeHtml, jsonForScript, postMessage. buildAddServerHtml / buildAddSapHtml fit naturally.
- vscode/mcp-gateway-dashboard/src/extension.ts (1-457) -- startServer / stopServer / restartServer commands already registered. showServerDetail command exists. Current addServer (lines 206-324) is the sequential InputBox/QuickPick flow to be replaced.
- vscode/mcp-gateway-dashboard/package.json (1-249) -- menus: restartServer already inline for (error|degraded); startServer/stopServer only in 1_lifecycle context; SAP entries unconditional on /^sap-/. Configuration has apiUrl, autoStart, daemonPath, pollInterval only.
- vscode/mcp-gateway-dashboard/src/sap-detector.ts (1-89) -- groupSapSystems keys by SID-Client. A4H-000 and A4H-100 are separate SapSystem instances. Hierarchical grouping must address this.
- vscode/mcp-gateway-dashboard/src/test/mock-vscode.ts (1-60) -- no MockMarkdownString; MockStatusBarItem.tooltip is string|undefined. Must extend for 11.B.

### Design Assumption Validation -- per sub-phase

#### 11.A -- Tree Stability + Inline Buttons

- OK: ServerDataCache exists and emits onDidRefresh; tree providers already subscribe. Diff-refresh is well-scoped.
- **REFINEMENT A-1 (MEDIUM):** Plan says JSON.stringify of previous list. This includes the full tools array per server (potentially large) and runs every pollInterval. Prefer a compact fingerprint of render-affecting fields only: for each server emit name|status|transport|restart_count|pid|last_error|tools.length and join with semicolons. Lower CPU cost at scale; explicit contract about which fields trigger refresh. Same pattern for SapTreeProvider using (key, status, vsp?.status, gui?.status, vsp?.restart_count, gui?.restart_count).
- **REFINEMENT A-2 (LOW):** Store fingerprint as instance field; reset to null in dispose().
- **REFINEMENT A-3 (MEDIUM):** Inline menu expansion -- current package.json has inline restart for (error|degraded) only; expand to (running|degraded|error|stopped). Add inline startServer (viewItem == disabled) and stopServer (viewItem =~ /^(running|degraded)$/). Keep context-menu versions for discoverability.
- **REFINEMENT A-4 (LOW):** Same command in both inline and 1_lifecycle groups is fine -- precedent is existing restartServer. No dedup concern.

#### 11.B -- Detail Panel + Rich Tooltips

- OK: ServerDetailPanel already exists and wired into cache.onDidRefresh via updateAll (extension.ts:87-90). Click-to-show reuses existing showServerDetail command.
- **REFINEMENT B-1 (MEDIUM) -- click-toggle UX pitfall:** VS Code tree command fires on selection change. Clicking an already-selected item does not reliably re-fire. Toggle-via-dispose-if-visible will feel inconsistent. Options: (1) Abandon toggle; click always reveals; add a Close button to the detail panel toolbar. (2) Subscribe to treeView.onDidChangeSelection and fire toggle there. (3) Use keybinding or inline icon instead of click. Recommendation: Option 1. Change T11B.1-4 from dispose-if-visible to click-reveals + panel Close action.
- **REFINEMENT B-2 (MEDIUM) -- mock-vscode must gain MockMarkdownString:** Add class with value/isTrusted/supportHtml fields and appendMarkdown method. Export as vscode.MarkdownString. Widen MockStatusBarItem.tooltip and MockTreeItem.tooltip to string|MockMarkdownString|undefined.
- **REFINEMENT B-3 (LOW):** After McpStatusBar migration to cache, remove timer/startPolling/stopPolling. Update extension.ts wiring (currently calls statusBar.startPolling(pollInterval)).
- **REFINEMENT B-4 (MEDIUM) -- daemon-offline signal:** Cache fail-clears to [] on refresh error, losing the distinction between daemon-offline and no-servers. Add lastRefreshFailed: boolean to ServerDataCache; expose via getter; include in onDidRefresh payload. Land in 11.A (earliest) so both 11.B status bar and 11.E orphan cleanup consume a stable API.

#### 11.C -- Add Server Webview Form

- OK: Pattern is consistent with server-detail-panel.ts + html-builder.ts.
- **REFINEMENT C-1 (MEDIUM) -- auto-detect rule:** If input starts with http:// or https:// then http/url; else stdio/command. Edge cases like npx foo-mcp or node /abs/path.js with args -- current validator requires absolute path, rejecting npx. Keep absolute-path requirement to match current behavior. Document in plan.
- **REFINEMENT C-2 (MEDIUM) -- credential flow:** Panel validates client-side, postMessage submit payload, extension handler calls client.addServer() then iterates credentialStore.storeEnvVar/storeHeader with per-entry warnings on failure. Preserve extension.ts:301-321 partial-failure behavior.
- **REFINEMENT C-3 (LOW) -- validation DRY:** Extract validateServerName/validateUrl regexes to a shared validation.ts module; import in both extension.ts and add-server-panel.ts.
- **REFINEMENT C-4 (LOW) -- webview trust boundary:** Re-validate postMessage values on extension side before client.addServer(). Do not trust the webview.

#### 11.D -- SAP Improvements

- **REFINEMENT D-1 (MEDIUM) -- hierarchical grouping spec is ambiguous.** Current groupSapSystems keys by SID-Client so A4H-000 and A4H-100 are separate systems. Two interpretations: (1) Each existing SapSystem becomes collapsible with VSP/GUI as children (2 levels, multi-client SIDs remain siblings, simple); (2) Bare SID becomes parent with clients as grandchildren (3 levels, requires re-keying sap-detector, complex). Recommendation: Interpretation 1. Zero changes to sap-detector.ts. Update plan text to say: hierarchical means each SAP system becomes collapsible, with VSP and GUI as direct children.
- **REFINEMENT D-2 (MEDIUM) -- new child class:** Introduce SapComponentItem extending TreeItem for VSP/GUI child rows. Exposes server name so existing restartSapVsp/restartSapGui commands work. Cleaner than reusing BackendItem.
- **REFINEMENT D-3 (MEDIUM) -- contextValue and menu rewrites:** Hierarchical parent gets distinct contextValue (sap-group-<status>); children get sap-vsp-<status>/sap-gui-<status>. package.json when-clauses must be updated so VSP/GUI actions do not show on the parent.
- **REFINEMENT D-4 (LOW):** sapGroupBySid default false is correct -- avoids breaking existing users.
- **REFINEMENT D-5 (LOW):** Expand state is ephemeral across toggles. Acceptable.
- **REFINEMENT D-6 (MEDIUM) -- Add SAP webview pre-check:** Validate SID ^[A-Z0-9]{3}$ (matches SAP_VSP_RE in sap-detector.ts:18); Client ^\d{3}$. Use cache.getAllServers() to warn on duplicate vsp-SID-Client.

#### 11.E -- Settings + Slash Commands

- **REFINEMENT E-1 (HIGH) -- workspace root resolution:** .claude/commands/<server>.md target directory must be resolvable. slashCommandsPath setting should accept absolute paths or a workspaceFolder variable. Default: ${workspaceFolder}/.claude/commands. On no-workspace or multi-root: use first workspace folder or skip gracefully.
- **REFINEMENT E-2 (HIGH) -- auto-file clobber protection:** User may hand-author .md files. Generator must NOT overwrite or delete user files. Adopt magic-header marker: first line is an HTML comment identifying the file as auto-generated and DO NOT EDIT. On overwrite or delete, read existing file first and proceed only if marker is present.
- **REFINEMENT E-3 (MEDIUM) -- transition detection state:** Subscribe to ServerDataCache.onDidRefresh; maintain in-memory Map<name, status>. Seed on first refresh so initial load does not emit spurious transitions.
- **REFINEMENT E-4 (MEDIUM) -- write-order race:** Chain async writes through a single-task queue (this.lastTask = this.lastTask.then(...)). Prevents interleaved writes and delete-before-write races. Wrap fs.writeFile/fs.unlink in try/catch; log failures without throwing.
- **REFINEMENT E-5 (MEDIUM) -- orphan cleanup guard:** Stale files from rename/remove must be cleaned. Run cleanup only when cache.lastRefreshFailed === false (see B-4) -- otherwise a transient daemon outage (cache cleared to []) would delete every generated file.
- **REFINEMENT E-6 (LOW) -- content template:** 11.E generates a minimal skeleton (name, status, transport, tools list, mcp-gateway invocation header). Rich enrichment deferred to Phase 14.7.
- **REFINEMENT E-7 (LOW):** slashCommandsEnabled default false -- opt-in to avoid surprising existing users.

### Cross-cutting Risks

1. **Test infrastructure debt (MEDIUM):** mock-vscode.ts needs extensions before 11.B-D: MockMarkdownString, widened tooltip typing, possibly mock filesystem for 11.E. Add T11B.0 and T11E.0 to prepare mock infra.
2. **Settings schema drift (LOW):** Phases 11.D, 11.E, 12, 13 all add mcpGateway.* configuration keys. Add atomically in owning phase.
3. **Tree/webview refresh coupling (MEDIUM):** cache.onDidRefresh already drives multiple consumers. 11.E generator must return quickly (queue writes). Existing fire-and-forget pattern (extension.ts:87-90) is acceptable.
4. **Dependency ordering 11.A -> 11.B -> 11.E (MEDIUM):** lastRefreshFailed flag and fingerprint snapshot are reused downstream. Land them in 11.A so both 11.B status bar and 11.E generator consume stable APIs.
5. **Deploy discipline (LOW):** CLAUDE.md references vscode-dashboard/ but repo uses vscode/mcp-gateway-dashboard/. Deploy rule still applies. Current package.json has a package script but no deploy script -- dev-lead should add or confirm.

### Summary of Required Plan Additions (for dev-lead)

1. **T11B.0 (new): Extend mock-vscode.ts with MockMarkdownString + widened tooltip types.**
2. **T11A.1/T11A.2: Use compact fingerprint, not JSON.stringify of full list. Store as instance field; reset in dispose().**
3. **T11B.1-4 (revise): Replace click-toggle with click-reveal + Close button in panel toolbar.**
4. **T11A.9 or T11B.6b (new): Add lastRefreshFailed: boolean to ServerDataCache; expose via getter; include in onDidRefresh payload. Land in 11.A preferred.**
5. **T11B.8 (expand): After migration, remove McpStatusBar.timer/startPolling/stopPolling and update extension.ts.**
6. **T11C.3 (clarify): startsWith http:// or https:// means http, else stdio; keep absolute-path requirement for stdio.**
7. **T11C.2b (new): Extract validateServerName/validateUrl to shared validation.ts; import in both extension.ts and add-server-panel.ts.**
8. **T11D (spec): Adopt Interpretation 1 -- 2-level hierarchy (SapSystem -> VSP/GUI), no changes to sap-detector.ts.**
9. **T11D.2b (new): New class SapComponentItem; distinct contextValue for group parent vs child; update package.json menu when-clauses.**
10. **T11D.4 (pre-check): Validate SID and Client regexes; warn on duplicate via cache.getAllServers().**
11. **T11E.0 (new): Extend mock-vscode / test harness for filesystem (tmpdir + cleanup per test).**
12. **T11E.2: Workspace root resolution with workspaceFolder variable; default in-workspace .claude/commands; graceful no-workspace skip.**
13. **T11E.3: Magic-header write protection; never overwrite or delete files without marker.**
14. **T11E.4: Single-task async queue for writes/deletes.**
15. **T11E.5 (new): Orphan cleanup pass guarded by lastRefreshFailed === false.**
16. **T11E.1: slashCommandsEnabled default false.**
17. **Per-phase GATE: Add explicit npm run deploy + stage rebuilt VSIX step; verify actual deploy script path in vscode/mcp-gateway-dashboard/package.json.**

### Cross-Validation Status

- **PAL MCP:** not available in this architect subagent session (mcp__pal__* tools not exposed).
- **Agent tool (fallback internal cross-model review):** also not available in this subagent.
- **No automated cross-validation could be performed.** Validation relies on direct-code inspection evidence only.
- **Recommendation:** dev-lead (next pipeline step) should run mcp__pal__thinkdeep on REFINEMENTS B-1, D-1, and E-2 (the MEDIUM+/HIGH items most likely to benefit from a second opinion), and mcp__pal__consensus on the overall plan. If those PAL calls disagree materially, escalate to user before starting implementation.

### Final Verdict

**APPROVE_WITH_REFINEMENTS** -- the 11.A-11.E design is implementable and consistent with existing patterns. All identified issues are resolvable via the 17 refinements above without re-scoping the phase. No CRITICAL or HIGH architectural flaws (the two items marked HIGH -- E-1 and E-2 -- are design gaps with clear resolutions, not flaws). Proceed to dev-lead task breakdown, incorporating these refinements.

-- Porfiry [Opus 4.6], 2026-04-15

---

## Phase 11 Design Decisions (user-confirmed, 2026-04-15)

### B-1 resolution: Sidebar WebviewView (Option 4)

**Decision:** Phase 11.B pivots from `vscode.WebviewPanel` (floating tab) to `vscode.window.registerWebviewViewProvider` (always-on sidebar view). The click-toggle UX pitfall is eliminated by removing the toggle entirely — the detail view is always visible and re-renders based on current tree selection.

**Cross-validation:** `[C+S AGREE]` on B-1 diagnosis (Opus architect + Sonnet independent review). `[C vs S DISAGREE]` on fix — Sonnet recommended Option 4, Opus architect recommended Option 1. User chose Option 4 for better monitoring UX (always-on, no dismissed-tab problem). VS Code issues #34130, #51536, #85636, #77418, #105256 confirm re-click toggle is a longstanding API constraint with no official fix since 2019.

**Revised T11B task list:**

- T11B.0: Extend mock-vscode.ts with MockMarkdownString (per REFINEMENT B-2) — land early, blocks 11.B tests
- T11B.1: Register new view `mcpGateway.serverDetail` in package.json inside existing `mcp-gateway-container` (views.mcp-gateway-container entry, type: `webview`)
- T11B.2: New `src/webview/server-detail-view-provider.ts` implementing `vscode.WebviewViewProvider`:
  - `resolveWebviewView(view)` wires CSP + nonce + HTML via existing `buildMcpDetailHtml()` from html-builder.ts (reuse, do not duplicate)
  - Subscribes to `backendTreeView.onDidChangeSelection` + `sapTreeView.onDidChangeSelection`
  - On selection change → post message to webview to re-render for new server
  - Empty placeholder when nothing selected: "Select a server to view details"
- T11B.3: Wire `registerWebviewViewProvider` in extension.ts; pass `ServerDataCache` so view can fetch current server state
- T11B.4: Migrate HTML generation: ensure `buildMcpDetailHtml(server, cache)` is pure and reusable between WebviewPanel (legacy, keep for now) and WebviewView (new)
- T11B.5-7: MarkdownString tooltips on `McpStatusBar` and `SapStatusBar` with per-server breakdown (unchanged from original plan)
- T11B.6/8: Migrate `McpStatusBar` from timer-based polling to `ServerDataCache.onDidRefresh` (unchanged from original plan, per REFINEMENT B-3)
- T11B.9: Add `lastRefreshFailed: boolean` getter to `ServerDataCache`; include in `onDidRefresh` payload (per REFINEMENT B-4 — land in 11.A preferred, fallback 11.B)
- T11B.10-12: Tests (new `server-detail-view-provider.test.ts`, status-bar tooltip tests, extended mock)
- GATE: tests + codereview + thinkdeep — zero MEDIUM+

**Existing `server-detail-panel.ts` (WebviewPanel):** KEEP for now. Still accessible via context-menu action. Do not delete in 11.B — deletion decision deferred to Phase 11.F review after real-world usage.

**Scope impact:** Phase 11.B grows by ~1 new file (`server-detail-view-provider.ts`), ~1 package.json view registration, ~1 test file. Other 11.A, 11.C, 11.D, 11.E scopes unchanged.

### E-2 resolution: magic-header marker (user-ACK'd)

**Decision:** slash command generator writes files with a magic-header as the first line:

```md
<!-- AUTO-GENERATED by mcp-gateway extension. DO NOT EDIT — will be overwritten. -->
```

On any write/delete, the generator first reads the existing file (if present). It proceeds ONLY if the first line matches the marker exactly. Without the marker → file is treated as user-authored → generator skips with a debug log ("skipped user-authored file: <path>"). No telemetry, no warning to user (not an error — just a deliberate no-op).

**Edge cases:**
- File does not exist → write unconditionally (no file to respect).
- File exists, marker present → safe to overwrite/delete.
- File exists, no marker → leave alone. Log once per file per session (avoid log spam).
- Race: file created between read-check and write → second write goes through same marker check.

**Tests required:**
- T11E.test.1: generates new file with marker as line 1
- T11E.test.2: overwrites existing file when marker present
- T11E.test.3: skips file without marker (user-authored)
- T11E.test.4: deletes only files with marker on orphan cleanup

### Release-please status (investigated, 2026-04-15)

**Finding:** commits `216e258` and `216f571` are NOT on `main`. They live on two release-please PR branches:
- `origin/release-please--branches--main--components--mcp-gateway-dashboard` → 216e258 (dashboard v0.2.0 bump)
- `origin/release-please--branches--main--components--mcp-gateway` → 216f571 (daemon v0.2.0 bump)

HEAD = origin/main = b38447d (security hardening) — untouched. The two commits are automated PRs from release-please bot waiting for human merge. They do NOT affect the working tree, do NOT block Phase 11 implementation, and can be dealt with independently.

**Version drift warning:** release-please manifest says next release is `0.2.0`, but current `package.json` is at `1.0.0` (from the v1.0.0 release event on 2026-04-09). The 0.2.0 is a misconfiguration — release-please was seeded with a stale baseline. When we're ready to release v1.1.0 (after Phase 11), we'll need to either:
- Close the two stale PRs and let release-please regenerate from the current 1.0.0 baseline, or
- Manually update `.release-please-manifest.json` to `1.0.0` so the next PR bumps correctly to `1.1.0`.

**Action deferred:** not a Phase 11 blocker. Recorded here for reference.

### CLAUDE.md path fix (applied, 2026-04-15)

**Action:** rewrote `base/CLAUDE.md` section "VSCode Dashboard Build Discipline" to be path-agnostic. Renamed to "VSCode Extension Build Discipline". New rule lists extension locations per project:
- claude-team-control: `vscode-dashboard/`
- mcp-gateway: `vscode/mcp-gateway-dashboard/`

Rule now mandates `npm run deploy` as the single deploy command and requires every project with a VS Code extension to provide the script. Claude-team-control's deploy flow is unaffected (its package.json already has `deploy`). mcp-gateway's package.json gets `deploy` added below.

**Sync:** requires running `/sync` in claude-team-control to propagate to all projects. Not done yet — commit + sync will happen after Phase 11 completion or on next user-initiated sync.

### npm run deploy script (added, 2026-04-15)

**Action:** added to `vscode/mcp-gateway-dashboard/package.json`:

```json
"package": "vsce package --allow-missing-repository --out mcp-gateway-dashboard-latest.vsix",
"deploy": "npm run compile && npm run package && code --install-extension mcp-gateway-dashboard-latest.vsix --force",
"build": "npm run compile"
```

- `package` now outputs stable filename `mcp-gateway-dashboard-latest.vsix`
- `deploy` chains compile → package → install via VS Code CLI with `--force` (ensures reinstall even on same version)
- `build` added as alias for `compile` (satisfies CLAUDE.md rule vocabulary)

No version auto-bump on deploy — version is owned by release-please, not the local deploy flow. `--force` handles same-version reinstall.

**Prerequisite:** `code` CLI must be on PATH. Standard on Windows/Linux/Mac VS Code installs.

-- Porfiry [Opus 4.6], 2026-04-15 (post-user-confirmation, all 4 decisions resolved)
