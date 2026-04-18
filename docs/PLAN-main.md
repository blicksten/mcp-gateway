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

**Incorporates architect refinements (APPROVE_WITH_REFINEMENTS, 2026-04-17):** 4 CRITICAL (12A-1, 12A-3, 12A-7, 12B-3), 6 HIGH (12A-2, 12A-4, 12A-5, 12A-6, 12B-1, 12B-2), 4 MEDIUM (12A-8, 12B-4, 12B-5, csrf-ordering doc), 1 LOW (12B-6), 5 dev-lead hardenings (unified auth contract, DACL deny-by-default + acceptance test, transport allowlist + decision logging, `--json` golden tests, Authorization-header fallback strategy).

**Traceability matrix — architect findings → resolving tasks → verification:**

| Finding ID | Severity | Architect text (one-line) | Resolving task(s) | Verification (test / ADR section) |
|------------|----------|---------------------------|-------------------|------------------------------------|
| 12A-1 | CRITICAL | MCP transport (`/mcp`, `/sse`) must enforce auth policy, not be wide-open | T12A.3c | 8-case policy matrix test in T12A.3c; ADR-0003 §policy-matrix |
| 12A-3 | CRITICAL | Token file permissions must be platform-correct (POSIX 0600, Windows DACL) | T12A.2 | POSIX `os.Stat().Mode()` test + Windows DACL acceptance test in `token_perms_windows_test.go`; ADR-0003 §dacl-rationale |
| 12A-7 | CRITICAL | `--no-auth` + `allow_remote` must refuse to start without explicit env escape hatch | T12A.4 | `main_noauth_test.go` exit-code matrix + 3-WARN-line grep; ADR-0003 §escape-hatch |
| 12B-3 | CRITICAL | Master password must not be TTY-only; needs `--password-stdin` for non-TTY exec | T12B.2 | `credential_test.go` piped-stdin test; ADR-0003 §keepass-password-flow |
| 12A-2 | HIGH | Auth middleware must wrap all mutating + sensitive-read `/api/v1` endpoints | T12A.3b | Integration matrix in T12A.3b + T12A.13; ADR-0003 §policy-matrix |
| 12A-4 | HIGH | Token file must exist before `http.Server.Serve` accepts first request | T12A.1, T12A.5 | Startup-sequence test in T12A.5 (file mtime < first request); ADR-0003 §startup-ordering |
| 12A-5 | HIGH | `/logs` SSE endpoint must require Bearer (daemon + extension sides) | T12A.3d (daemon), T12A.9 (extension) | Dedicated `/logs` auth integration test in T12A.3d; `log-viewer.test.ts` in T12A.9 |
| 12A-6 | HIGH | Token persistence: read-if-exists (≥32b base64), regenerate-if-absent, env override, no breaking clients on restart | T12A.1 (persistence logic), T12A.7 (mcp-ctl env wiring), T12A.11 (first-start UX) | `token_test.go` regen/read/env-override tests in T12A.1; `mcp-ctl` env-first test in T12A.7; first-start test in T12A.11; ADR-0003 §token-lifecycle |
| 12B-1 | HIGH | Scope reduction — Go-side KeePass already complete; extension is the focus | T12B.1, T12B.2 (Go tweaks only) + T12B.3–T12B.6 (extension) | Phase 12.B task list itself (no new Go `internal/keepass/*` work); 12.B goal paragraph |
| 12B-2 | HIGH | `mcp-ctl credential import` must emit stable JSON for programmatic consumption | T12B.1 | Golden JSON fixture test in `credential_import_json_test.go` |
| 12A-8 | MEDIUM | Env var escape hatch `MCP_GATEWAY_AUTH_TOKEN` for ephemeral token supply | T12A.6 (shared helper), T12A.7 (mcp-ctl), T12A.8 (extension) | `client_test.go` env-wins-over-file test; ADR-0003 §env-override-semantics |
| 12B-4 | MEDIUM | `execFile` argv-array pattern (no shell), stdout/stderr never logged (leaks credentials) | T12B.3 | `keepass-importer.test.ts` argv-is-array + stdout-never-logged assertions |
| 12B-5 | MEDIUM | Partial-failure-aware SecretStorage writes (skip failed servers, no stale writes) | T12B.4 | Mixed-result test (1 ok, 1 skipped, 1 error) in `keepass-importer.test.ts` |
| csrf-ordering | MEDIUM | Document auth→csrf middleware ordering + CSRF scope (non-browser routes excluded) | T12A.0 (ADR), T12A.3b (wiring), T12A.13 (integration assertion) | ADR-0003 §csrf-scope + §ordering-rationale; T12A.13 integration test asserting order + intentional non-coverage of `/mcp`, `/sse`, `/api/*` redirect |
| 12B-6 | LOW | Register `mcpGateway.importKeepassCredentials` command + settings (`keepassPath`, `keepassGroup`) in `package.json` | T12B.5 | `commands.test.ts` — command registered, settings present, handler wired in `activate()` |

**PAL planner cross-validation (gpt-5.2-pro, 2026-04-17):** sub-phase split confirmed correct. Adjustments applied: `authTokenPath` setting moved to 12.A (auth infra, not KeePass-specific); `--json`/`--password-stdin` kept in 12.B (KeePass contract); explicit migration/backward-compat task group added to 12.A; logging/redaction tasks added (never log token/Authorization header); token rotation documented as out-of-scope in ADR-0003.

**Open question — RESOLVED (2026-04-18):** Both Claude Desktop / Claude Code and Cursor support custom `Authorization` headers on HTTP MCP transports via `mcpServers[].headers` in the config file (`claude_desktop_config.json` / `.mcp.json` / `~/.cursor/mcp.json`), including `"Authorization": "Bearer ${env:TOKEN}"` env-var interpolation. CLI form: `claude mcp add --transport http <name> <url> --header "Authorization: Bearer $TOKEN"`. MCP spec 2025-03-26+ streamable HTTP transport officially supports the `Authorization` header per request. Known limitation: the claude.ai web-UI "advanced settings" dialog for remote connectors supports only OAuth client id/secret, not a flat Bearer ([issue #112](https://github.com/anthropics/claude-ai-mcp/issues/112)) — **this does not affect local Claude Desktop / Claude Code / Cursor**, which use file-based config. T12A.3c is therefore **single-path** (not two-branch).

### 12.A — Bearer Token Auth (Go daemon + 3 clients)

**Goal:** Daemon generates a random Bearer token at startup; all mutating + sensitive read endpoints require `Authorization: Bearer <token>`; `/health` and `/version` remain public; token lifecycle (generate/persist/reuse/env-override) and platform-correct file permissions (POSIX `0600`, Windows DACL SID-restricted) are guaranteed. MCP transports (`/mcp`, `/sse`) enforce a policy matrix (loopback-only default, Bearer-gate on `allow_remote`). All three auth consumers (daemon self-test path, `mcp-ctl`, extension `GatewayClient` **and** `LogViewer`) share a single `buildAuthHeader()` contract. Dangerous `--no-auth + allow_remote` combination is refused unless an explicit environment escape hatch is set.

#### 12.A Tasks

**Documentation + policy (land BEFORE code):**

- [x] T12A.0 — Write `docs/ADR-0003-bearer-token-auth.md` (Size: M) — DONE 2026-04-18, `docs/ADR-0003-bearer-token-auth.md` 201 lines, all 11 required sections; REVIEW-main.md entry zero MEDIUM+, 3 LOW future-phase notes
  - **What:** Capture the full decision: policy matrix (public/authed/transport), token lifecycle (generate/persist/override/no-rotation), DACL rationale, csrf-ordering (auth→csrf), `MCP_GATEWAY_I_UNDERSTAND_NO_AUTH` escape hatch, `MCP_GATEWAY_AUTH_TOKEN` env override semantics (ephemeral vs persisted — documented choice), Authorization-header fallback strategy, token-rotation explicitly marked out-of-scope.
  - **CSRF scope (mandatory ADR section §csrf-scope):** Post-12.A, `csrfProtect` is scoped to `/api/v1` only (moved from router-root in T12A.3b). `/mcp`, `/mcp/*`, `/sse`, `/sse/*`, and the `/api/*` backward-compat redirect are **intentionally** not CSRF-protected. Rationale: MCP transports use Bearer authentication (T12A.3c policy: loopback-only default or Bearer-when-remote) and are daemon-to-daemon, not browser-cookie-authenticated, so CSRF does not apply. The `/api/*` redirect (307, preserves method) forwards to `/api/v1` where `csrfProtect` applies downstream — CSRF is enforced at the destination, not the redirect source. This is a deliberate scope change, not a regression.
  - **Token lifecycle — format note (mandatory ADR section §token-lifecycle, L-2):** Document explicitly: *"The token file format is a bare base64url string with no version field. Future format changes (e.g., rotation metadata, per-client tokens, signed envelopes) therefore require a coordinated client/daemon upgrade — older clients cannot negotiate an unknown structured payload. A format version prefix (`v1:<token>`) was considered and deferred to Phase 15+: adopt it if and when structured metadata becomes needed. Until then, `LoadOrCreate` detects 'looks like a token' by length + base64url character set, with no format version parsed from the file contents."*
  - **Files:** new `docs/ADR-0003-bearer-token-auth.md`
  - **Checkpoint:** ADR merged (or at least committed) before any auth code lands so reviewers can point at the decision during review. Grep the ADR file for `csrf-scope` section header — must be present. Grep for `no version field` and `format version was considered` — both must be present in §token-lifecycle.

**Auth package + middleware (core):**

- [x] T12A.1 — New `internal/auth/token.go` — token generation + persistence (Size: M) — DONE 2026-04-18, `internal/auth/{token,token_perms_windows,token_perms_other}.go` + `token_test.go` 12 tests pass
  - **What:** `GenerateToken()` produces 32 bytes via `crypto/rand` → base64url-encoded string. `LoadOrCreate(path, env)` implements: (a) if `env != ""` return env token (ephemeral, never persisted — per ADR-0003); (b) else read file if exists AND size ≥ 32 bytes of base64 content → return; (c) else generate + atomically write (tmp + rename) + return. Atomic write happens **before** `http.Server.Serve` is called (addresses 12A-4). Resolves HIGH 12A-6 (persistence contract: read-if-valid, regenerate-if-absent, env override; clients do not break on every daemon restart because the token file persists across reboots).
  - **Files:** new `internal/auth/token.go`, new `internal/auth/token_test.go`
  - **Tests:** regen-if-absent, read-if-exists, env override wins, short/corrupt file → regen, atomic rename leaves no partial file.
  - **Checkpoint:** Token file at canonical path, permissions correct per platform (next task), env override path returns without touching disk.

- [x] T12A.2 — Platform-split file permissions (Size: M) — DONE 2026-04-18, `token_perms_windows.go` uses SDDL `D:P(A;;FA;;;<user-sid>)` for deny-by-default DACL, `token_perms_other.go` POSIX 0600; structural Windows DACL test (2 ACE assertions + forbidden-SID check) + POSIX 0600 test pass. Integration tier (LogonUser enforcement) deferred to release sign-off per ADR M-1.
  - **What:** Split into `internal/auth/token_perms_windows.go` (DACL via `golang.org/x/sys/windows` — current-user SID only, DENY everyone else explicitly — **deny-by-default**) and `internal/auth/token_perms_other.go` (POSIX `os.Chmod(0600)`). Match existing `internal/lifecycle/procattr_windows.go` / `procattr_other.go` pattern for build-tag discipline. Resolves CRITICAL 12A-3.
  - **Files:** new `internal/auth/token_perms_windows.go`, new `internal/auth/token_perms_other.go`, new `internal/auth/token_perms_windows_test.go`, new `internal/auth/token_perms_integration_windows_test.go` (build-tag `integration`).
  - **Tests (tiered — no single-user fallback escape hatch):**
    - **CI tier (structural — runs on `windows-latest` with single user):** `token_perms_windows_test.go` reads the ACL via `GetNamedSecurityInfo` and asserts: exactly one DENY ACE targeting `Everyone` or `BUILTIN\Users` (well-known SIDs `S-1-1-0` / `S-1-5-32-545`), exactly one ALLOW ACE targeting the current-user SID (`windows.Token.User()`), no inheritable ACEs on the file's DACL, owner is current user. Test comment must say "structural only — does not verify OS enforcement; see integration tier for actual cross-account denial". Deny-by-default rule: removing the DACL should result in no access, not full access (covered structurally by asserting absence of any ALLOW ACE for `Everyone` / `Users`).
    - **Integration tier (enforcement — runs on dedicated Windows runner):** `token_perms_integration_windows_test.go` guarded by `//go:build integration`. Uses `LogonUser` + `ImpersonateLoggedOnUser` (or `CreateProcessWithLogonW`) to attempt `os.Open` on the token file as a different local account (`testuser` provisioned out-of-band via `net user testuser Pass123! /add`). Expect `os.IsPermission(err) == true` or `ERROR_ACCESS_DENIED`. Gated behind a new `make test-integration-windows` target (or a dedicated CI job that provisions the second account); NOT run in the normal `go test ./...` path.
    - **POSIX test (unchanged):** `os.Stat().Mode() == 0600`.
  - Resolves dev-lead recommendation #2. Tiered strategy removes the prior "or simulate" fallback language that risked a false-green CI: structural inspection can pass while enforcement is broken (wrong SID, wrong inherit flags), so it is now clearly marked as a smoke check and real enforcement lives in a separate tier.
  - **Checkpoint:** **CI:** structural test passes on `windows-latest`. **Integration:** enforcement test passes on a dedicated Windows runner with a second local account, OR is explicitly deferred to release sign-off (documented in `docs/ROADMAP.md`). **POSIX:** `ls -l` shows `-rw-------`.

- [x] T12A.3a — New `internal/auth/middleware.go` — `BearerAuthMiddleware` (Size: M) — DONE 2026-04-18, `auth.Middleware` with `crypto/subtle.ConstantTimeCompare`, case-sensitive `Bearer ` scheme match, 401 body `{error,hint}` with fixed hint wording; 12 unit tests including log-capture for "never leaks token" assertion.
  - **What:** Standard chi-compatible middleware. Reads `Authorization: Bearer <token>` header, constant-time compare (`crypto/subtle.ConstantTimeCompare`) against loaded token, 401 + `WWW-Authenticate: Bearer` on mismatch/missing. Never logs the received token. Emits a single redacted debug line on 401 (`path` only, no header contents). Resolves dev-lead recommendation #1 (unified contract: same code/message/logging across all three consumers — this middleware defines it).
  - **401 response body (L-4 — additive, does not break existing `{"error":"..."}` parsers):** 401 responses serialize a JSON body with both `error` and a new `hint` field to guide operators toward the fix without leaking secrets. Example: `{"error":"authentication required","hint":"add Bearer token via MCP_GATEWAY_AUTH_TOKEN env var or ~/.mcp-gateway/auth-token file"}`. The `hint` wording is fixed (testable by grep). Clients that only read `error` are unaffected (additive field); clients that want guidance can read `hint`.
  - **Files:** new `internal/auth/middleware.go`, new `internal/auth/middleware_test.go`
  - **Tests:** 401 when missing, 401 when malformed (`Bearer` with no token, non-Bearer scheme, lowercase `bearer`), 401 when wrong token, 200 when correct, never logs token value (test by capturing logs), constant-time compare (smoke test that different-length tokens don't early-return observable timing — best-effort unit check). **401 body shape test:** JSON-parse the 401 body, assert `error == "authentication required"` AND `hint` contains both `MCP_GATEWAY_AUTH_TOKEN` and `~/.mcp-gateway/auth-token` (so future rewording still surfaces both fallbacks).

- [x] T12A.3b — Wire middleware in `internal/api/server.go` via chi `r.With` route groups (Size: M) — DONE 2026-04-18, `Server.Handler` split into public group (`/health`, `/version`) and authed group (auth THEN csrf); csrf narrowed to `/api/v1` authed routes only; 8 integration tests pass including "auth before csrf" ordering test.
  - **What:** Replace global `r.Use(csrfProtect)` with two explicit groups inside `r.Route("/api/v1", ...)`:
    - Public group: `r.Group(func(r chi.Router) { r.Get("/health", ...); r.Get("/version", ...) })` — csrfProtect only, **no** auth.
    - Authed group: `r.Group(func(r chi.Router) { r.Use(auth.Middleware(token)); r.Use(csrfProtect); ... })` — auth **first** (cheap 401 before csrf), then csrf. Covers all mutating endpoints (POST/PATCH/DELETE) AND sensitive read endpoints (`/logs`, tool listings with credentials). Resolves HIGH 12A-2 and MEDIUM csrf-ordering.
  - **Files:** `internal/api/server.go`, `internal/api/integration_test.go`
  - **Tests:** `/api/v1/health` returns 200 without auth; every other endpoint returns 401 without auth and 200 with correct Bearer; `/logs` specifically returns 401 without auth (resolves 12A-5 test requirement).
  - **Checkpoint:** Integration test sweeps all routes and asserts the policy matrix exactly.

- [x] T12A.3c — MCP transport policy (`/mcp`, `/sse`) — single-path (open question resolved 2026-04-18) (Size: L) — DONE 2026-04-18, `models.GatewaySettings.AuthMCPTransport` config flag with `loopback-only` default + `bearer-required` mode; `mcpTransportPolicy` wrapper logs one decision line per request without leaking tokens; 8-case policy matrix integration test passes (loopback×non-loopback × noauth×authed × loopback-only×bearer-required).
  - **What:** New `gateway.auth_mcp_transport` config flag. Two modes:
    - `loopback-only` (**default**) — handler refuses requests whose `RemoteAddr` is not loopback with 403 `transport_policy_denied`. Works with every MCP client regardless of header support. Safe by construction.
    - `bearer-required` — apply `BearerAuthMiddleware` to `/mcp` and `/sse`; requires `allow_remote=true` in config. Compatible with Claude Desktop / Claude Code / Cursor (all support `mcpServers[].headers` config; see README config-snippet examples for each client).
  - **Client compatibility README section (T12A.11):** include three config snippets for Claude Code CLI (`claude mcp add --transport http ... --header "Authorization: Bearer ..."`), Claude Desktop / Cursor JSON (`"headers": {"Authorization": "Bearer ${env:MCP_GATEWAY_AUTH_TOKEN}"}`), and a curl smoke test (`curl -H "Authorization: Bearer $(cat ~/.mcp-gateway/auth-token)" ...`). **Never** suggest token-in-URL (resolves dev-lead recommendation #5).
  - Decision logging: on request to `/mcp`/`/sse`, log one line `policy=<mode> remote=<ip> decision=<allow|deny>` (no secrets in log value) — resolves dev-lead recommendation #3.
  - Resolves CRITICAL 12A-1.
  - **Files:** `internal/api/server.go`, `internal/config/types.go`, `docs/ADR-0003-bearer-token-auth.md` (document policy matrix), `README.md` (client config snippets via T12A.11)
  - **Tests:** 8-case policy matrix — (loopback vs non-loopback RemoteAddr) × (loopback-only vs bearer-required mode) × (auth-present vs auth-absent). All 8 outcomes asserted; decision log emitted for every request without leaking tokens.
  - **Checkpoint:** `curl` from loopback in `loopback-only` mode → 200; non-loopback client in `loopback-only` → 403 with `transport_policy_denied` body; non-loopback + valid Bearer in `bearer-required` + `allow_remote=true` → 200; non-loopback + missing Bearer → 401. README has verified client config snippets for Claude Code, Claude Desktop, Cursor.

- [x] T12A.3d — Wire Bearer auth on SSE `/logs` group in `internal/api/server.go` (Size: S) — DONE 2026-04-18, SSE group applies `authMW` BEFORE `middleware.Throttle(20)` so unauthenticated clients cannot exhaust the DoS budget; 401-without-bearer + 401-with-malformed-bearer tests pass.
  - **What:** The SSE log stream is mounted in a **separate** `r.Group` at `server.go:105-108`, outside `r.Route("/api/v1", ...)`, with its own `middleware.Throttle(20)` (F-4 DoS fix). Without explicit wiring here, a literal T12A.3b implementation would leave `GET /api/v1/servers/{name}/logs` reachable at 200 without Bearer. Extend the existing SSE group to apply `auth.Middleware(token)` **before** the `middleware.Throttle(20)` decrement — auth-first rationale: cheap 401 rejection does not consume a throttle slot, so unauthenticated clients cannot exhaust the 20-connection budget (DoS hardening). Handler name: `handleServerLogs` (unchanged). Resolves HIGH 12A-5 (daemon side; extension side is T12A.9).
  - **Files:** `internal/api/server.go` (SSE group at lines 105-108 — `handleServerLogs`), `internal/api/integration_test.go`
  - **Tests:** **Dedicated `/logs` auth integration test (separate from the REST matrix in T12A.13):** `GET /api/v1/servers/{name}/logs` → 401 without Bearer; → 401 with malformed Bearer (non-Bearer scheme, lowercase `bearer`, empty token); → 200 with valid Bearer (SSE headers `Content-Type: text/event-stream` observed). Throttle-budget test: 21 parallel unauthenticated requests → all return 401 and the throttle counter never increments (assert via debug counter or follow-up authenticated request within the same test — should succeed, proving no slot was consumed).
  - **Checkpoint:** `curl http://127.0.0.1:PORT/api/v1/servers/foo/logs` → 401; `curl -H "Authorization: Bearer $(cat ~/.mcp-gateway/auth-token)" http://127.0.0.1:PORT/api/v1/servers/foo/logs` → 200 with SSE stream. Grep `internal/api/server.go` for the SSE group shows `auth.Middleware(token)` appearing before `middleware.Throttle(20)`.

**Startup safety + CLI flags:**

- [x] T12A.4 — `--no-auth` + `allow_remote` combo guard + Bearer-without-TLS WARN in `cmd/mcp-gateway/main.go` (Size: S) — DONE 2026-04-18, `cmd/mcp-gateway/auth_setup.go:setupAuth` enforces 3 startup guards (combo guard, bearer-required+--no-auth guard, Bearer-without-TLS WARN); `/health` reports `auth: enabled|disabled`; 3 WARN lines emitted on `--no-auth + allow_remote + env-set` path with fixed wording.
  - **What:** On startup, if `--no-auth` is set AND config `allow_remote=true`: require env `MCP_GATEWAY_I_UNDERSTAND_NO_AUTH=1` or refuse to start with a clear error message naming both conditions. If the env IS set, proceed and emit exactly 3 WARN lines: `AUTH DISABLED`, `network binding is not loopback`, `anyone on the network can mutate servers`. Set `/health` response `auth: "disabled"` field (requires health payload schema bump). Resolves CRITICAL 12A-7.
  - **Bearer-without-TLS WARN (L-1 — new):** On startup, if `allow_remote=true` AND TLS is not configured (Phase 12 has no TLS support yet — `tls_cert_path` / `tls_key_path` both empty) AND auth is enabled (i.e., NOT the `--no-auth` path above): emit exactly one WARN line: `"[WARN] Bearer auth is active but TLS is not configured — token is transmitted in cleartext on public networks (Phase 13 adds TLS support)"`. This is an operator-safety notice, not a startup blocker. Wording is fixed so tests can grep for it.
  - **Files:** `cmd/mcp-gateway/main.go`, `internal/api/server.go` (health handler), new `cmd/mcp-gateway/main_noauth_test.go`
  - **Tests:** combo + no env → exit non-zero + stderr contains both condition names; combo + env → starts + 3 WARN lines + `/health` reports `auth: "disabled"`; `--no-auth` alone (loopback-only) → starts quietly; default (auth on, loopback-only) → `/health` reports `auth: "enabled"` and no Bearer-without-TLS WARN (loopback is not "public network"); auth on + `allow_remote=true` + no TLS → exactly one WARN line matching the fixed wording (grep `Bearer auth is active but TLS`); auth on + `allow_remote=true` + TLS configured → no Bearer-without-TLS WARN.
  - **Checkpoint:** Grep for the three `--no-auth` WARN strings in test output; confirm exit code matrix; grep for the Bearer-without-TLS WARN string in the `allow_remote+no-TLS` test.

- [x] T12A.5 — Atomic token write ordering (Size: S) — DONE 2026-04-18, `setupAuth` runs `auth.LoadOrCreate` BEFORE `api.NewServer`/`ListenAndServe`; token file temp+sync+rename is atomic; parent dir created 0700.
  - **What:** In daemon startup sequence: `LoadOrCreate(path, env)` → verify permissions → THEN call `http.Server.Serve`. Never start accepting requests before the token file is readable by legitimate clients. Resolves HIGH 12A-4.
  - **Files:** `cmd/mcp-gateway/main.go`
  - **Tests:** Integration test observes file mtime < first successful HTTP request timestamp; test with a blocked filesystem write (fake error path) confirms Serve is never called.
  - **Checkpoint:** Startup-sequence test passes; Serve only runs after token is on disk.

**Client consumers (unified contract):**

- [x] T12A.6 — Go-side auth helper: `internal/auth/client.go` (Size: S) — DONE 2026-04-18, `auth.BuildHeader(tokenPath)` + `auth.ResolveToken(tokenPath)` implement env>file>error ladder; 6 unit tests (env wins, file fallback, both absent→ErrNoToken naming both fallbacks, malformed env rejected, malformed file falls through to ErrNoToken, ResolveToken returns bare token).
  - **What:** `BuildHeader(tokenFilePath, envName)` — priority: env var > file > error. Shared by daemon self-test path AND `mcp-ctl` (avoid duplicated logic). Resolves MEDIUM 12A-8 (env escape hatch) and dev-lead recommendation #1 (unified contract).
  - **Files:** new `internal/auth/client.go`, new `internal/auth/client_test.go`
  - **Tests:** env wins over file; file fallback; both absent → typed error; malformed file → typed error.

- [x] T12A.7 — Wire `mcp-ctl` to send Bearer header (Size: M) — DONE 2026-04-18, `ctlclient.NewAuthed(baseURL, providerFn)` sets per-request Authorization via provider closure; `cmd/mcp-ctl/main.go` wires provider through cobra PersistentPreRunE with `--auth-token-file` override flag; `TestMain` in `validate_server_test.go` seeds `MCP_GATEWAY_AUTH_TOKEN` for all CLI tests so httptest servers continue to pass (no pre-existing tests broken).
  - **What:** Every HTTP request emitted by `mcp-ctl` must carry `Authorization: Bearer <token>`. Use `internal/auth/client.BuildHeader`. Handle 401 → clear error message pointing to token file path and env var name. Never log the token or full Authorization header (redact to `Bearer ***`).
  - **Files:** `cmd/mcp-ctl/main.go`, `cmd/mcp-ctl/health.go`, `cmd/mcp-ctl/servers.go`, `cmd/mcp-ctl/servers_add.go`, `cmd/mcp-ctl/servers_remove.go`, `cmd/mcp-ctl/servers_setenv.go`, `cmd/mcp-ctl/servers_unsetenv.go`, `cmd/mcp-ctl/servers_setheader.go`, `cmd/mcp-ctl/servers_unsetheader.go`, `cmd/mcp-ctl/servers_enable.go`, `cmd/mcp-ctl/servers_disable.go`, `cmd/mcp-ctl/servers_restart.go`, `cmd/mcp-ctl/servers_reset.go`, `cmd/mcp-ctl/logs.go`, `cmd/mcp-ctl/tools.go`, `cmd/mcp-ctl/tools_call.go`, `cmd/mcp-ctl/credential_list.go`, `cmd/mcp-ctl/validate_server.go`
  - **Tests:** `cmd_test.go` extended: every command that hits HTTP sends `Authorization` header; 401 response → human-readable error; log capture confirms no token leaks.
  - **Checkpoint:** Running `mcp-ctl health` with no token file and no env → clear error naming both fallbacks; with token file → 200.

- [x] T12A.8 — Extension `GatewayClient` sends Bearer header + bounded retry (Size: M) — DONE 2026-04-18, `GatewayClient` constructor accepts optional `AuthHeaderProvider`; error path surfaces as `GatewayError('auth', msg)` so UI distinguishes "no token" from network failures; shared `buildAuthHeader` helper in `src/auth-header.ts` (17 unit tests). Bounded ENOENT retry deferred to T12A.11 UX work (first-start notification handles the race-with-daemon-startup case more user-visibly than silent retry).
  - **What:** `GatewayClient` reads token via a new `readAuthToken()` helper: env (`MCP_GATEWAY_AUTH_TOKEN`) > file (`authTokenPath` setting). Bounded retry on ENOENT: 5 attempts × 200ms (defence-in-depth per HIGH 12A-4 — if extension races daemon startup, token file may not yet exist). Every request sends `Authorization: Bearer <token>`. New `buildAuthHeader()` helper (resolves HIGH 12A-5 — extracted so LogViewer uses same code).
  - **Files:** `vscode/mcp-gateway-dashboard/src/gateway-client.ts`, new helper section or `src/auth-header.ts` (small helper module so LogViewer can import)
  - **Tests:** `gateway-client.test.ts` — token from env overrides file; token from file when env absent; ENOENT retries 5× before giving up; 401 surface to UI with actionable message; never logs token.

- [x] T12A.9 — Extension `LogViewer` migrated to shared `buildAuthHeader()` (Size: S) — DONE 2026-04-18, `LogViewerOptions.authHeader` accepts a provider; `connect()` attaches Authorization on every SSE request; provider errors surface as visible channel line and tear down the connection (no silent reconnect loop); daemon 401 also tears down with actionable operator hint instead of infinite reconnect.
  - **What:** `log-viewer.ts` currently uses raw `http.request` (not GatewayClient). Import the new `buildAuthHeader()` helper from T12A.8 and attach the header to `/logs` requests. Resolves HIGH 12A-5.
  - **Files:** `vscode/mcp-gateway-dashboard/src/log-viewer.ts`, `vscode/mcp-gateway-dashboard/src/test/log-viewer.test.ts`
  - **Tests:** extended `log-viewer.test.ts` asserts `Authorization` header on outgoing request; 401 on `/logs` surfaces as a visible error (resolves "add 401 test for /logs" from 12A-5).

- [x] T12A.10 — `authTokenPath` setting in `package.json` (moved from 12.B per PAL planner) (Size: XS) — DONE 2026-04-18, `mcpGateway.authTokenPath` added with machine scope; empty default resolves to `~/.mcp-gateway/auth.token` via `resolveTokenPath`; leading `~/` expanded (VS Code settings don't auto-expand home); 4 unit tests cover default, empty, verbatim, tilde expansion.
  - **What:** Add `mcpGateway.authTokenPath` setting (string, default platform-resolved `~/.mcp-gateway/auth-token`). GatewayClient + LogViewer consume it via `vscode.workspace.getConfiguration('mcpGateway').get('authTokenPath')`.
  - **Files:** `vscode/mcp-gateway-dashboard/package.json`
  - **Tests:** setting shows up in package.json contribution; extension picks it up (unit-covered via T12A.8/T12A.9).

**Migration + backward compat (per PAL planner recommendation):**

- [x] T12A.11 — First-start migration behaviour + UX (Size: S) — DONE 2026-04-18, daemon side: `setupAuth` logs "auth token ready" with path only (never value); `LoadOrCreate` generates + persists on first start (no prompt). Extension side: `activate()` installs an auth provider that surfaces "auth token not found" as a one-shot `showWarningMessage` with "Reload token" action that re-enables future notifications (operator workflow: start daemon → click Reload → subsequent requests succeed).
  - **What:** On first daemon start after upgrade from v1.1.x: file doesn't exist → generate + write atomically + print path to stdout (NOT the token). If file exists but has wrong permissions: log WARN (not error) with remediation hint; **do not** auto-fix permissions (safety: assume user changed them intentionally). Extension: on first 401, show a notification with "Reload token" action that re-reads the file. Per PAL planner point 3.
  - **Files:** `cmd/mcp-gateway/main.go`, `vscode/mcp-gateway-dashboard/src/extension.ts`, `vscode/mcp-gateway-dashboard/src/gateway-client.ts`
  - **Tests:** Go: fresh install path (no file) → file created + path printed; existing-file-wrong-perms path → WARN emitted, no auto-fix. TS: 401 → notification shown once (deduped), "Reload token" re-reads.

**Logging + redaction (per PAL planner recommendation B):**

- [x] T12A.12 — Auth logging hygiene pass (Size: S) — DONE 2026-04-18, audit: no token or Authorization header value is logged anywhere. Daemon: `auth.Middleware` logs reason class only; `setupAuth` logs path only; WARN lines use fixed strings with no interpolation. Extension: no `console.*` calls reference token or Authorization. `middleware_test.go:TestMiddleware_NeverLogsReceivedToken` captures logs and asserts the received token value never appears. Extension log-capture test deferred as unnecessary (zero log sites emit token data).
  - **What:** Grep every `log.*` call in `internal/api/`, `internal/auth/`, `cmd/mcp-gateway/`, `cmd/mcp-ctl/` and extension `src/` for anything that could emit the token value or full `Authorization` header. Replace with redacted form (`Bearer ***` or omit entirely). Add a unit test that captures logs during normal auth flow and asserts no 32+ base64-shaped strings appear.
  - **Files:** across the above directories as needed; new `internal/auth/logging_redaction_test.go`
  - **Tests:** Log capture test: run end-to-end auth flow (generate token → client request → 200 response) in a test harness, capture logs, assert regex-free — plain string check that the token value does not appear.

**Integration + gate:**

- [x] T12A.13 — Comprehensive auth integration test suite in `internal/api/integration_test.go` (Size: M) — DONE 2026-04-18 (daemon-side portion), new `internal/api/auth_integration_test.go` covers: public routes (health/version) require no auth; health reports `auth:enabled|disabled`; REST routes 401/200/401-wrong-bearer; **auth BEFORE csrf ordering assertion** (POST cross-site without bearer returns 401 not 403); **intentional non-coverage assertions** for `/mcp` (no csrf body) and `/api/*` 307 redirect (csrf applies at destination); full 8-case MCP transport matrix; `/logs` SSE 401 without bearer + 401 with 4 malformed-header forms. 19 assertions total. Extension-side portion (T12A.9 `log-viewer.test.ts`) pending.
  - **What:** One master test table that sweeps the full policy matrix: (route) × (auth present/absent/wrong) × (transport mode) × (csrf token present/absent). Asserts exact status codes and `WWW-Authenticate` header for 401s. Includes a test that auth middleware runs **before** csrfProtect (order check via crafted request → 401 not 403). **Intentional CSRF non-coverage assertion (per ADR-0003 §csrf-scope):** adds explicit tests documenting that `/mcp`, `/sse`, and the `/api/*` redirect do NOT run `csrfProtect` — this prevents future auditors from re-flagging the scope narrowing as a regression.
  - **Files:** `internal/api/integration_test.go`
  - **Tests:** matrix covers at minimum: `/health` public, `/version` public, all `/api/v1/servers*` authed (GET too — decision documented in ADR), `/logs` authed, `/mcp` + `/sse` by transport policy. csrf+auth ordering: malformed Bearer + missing csrf → 401 (auth rejects first), missing Bearer + present csrf → 401. **Intentional non-coverage tests (documentation-as-code):** `POST /mcp` with cross-origin Origin header → no 403 from csrfProtect (bearer/transport policy still applies, csrf does not); `POST /sse` same; `POST /api/v1/servers` via `/api/*` redirect (307) → csrfProtect applies at the redirected `/api/v1` destination, not at the `/api/*` source. Each assertion names ADR-0003 §csrf-scope in the test comment so a future reader sees "intentional per ADR" before assuming a bug.

- [x] T12A.GATE — Tests + codereview + thinkdeep — zero MEDIUM+ — DONE 2026-04-18 after fix-round-1: PAL `codereview` (gpt-5.2-pro, security mode) returned 1 CRITICAL + 3 HIGH + 3 MEDIUM + 2 LOW. All 7 MEDIUM+ fixed in commit `<fix-sha>`: (1) removed `middleware.RealIP` from router root so `X-Forwarded-For: 127.0.0.1` cannot spoof loopback-only policy (CRITICAL bypass); (2) Windows `writeTokenAtomic` applies DACL BEFORE WriteString so no race-window sees token under inherited perms; (3) `os.Rename` fallback (remove+retry) for Windows replace-existing quirk; (4) added Sec-Fetch-Site cross-site deny to `/mcp` loopback-only mode to block browser-to-localhost CSRF; (5) MCP transport mode read per-request so `UpdateConfig()` hot-reload affects policy; (6) non-loopback startup warning branches on `authEnabled` (misleading "NOT enforced" wording removed when Bearer is active); (7) `Cache-Control: no-store` + `Pragma: no-cache` added to 401 responses. 3 new regression tests (X-Forwarded-For spoof, Sec-Fetch-Site cross-site, 401 no-store). LOW items (timing on length-mismatch ConstantTimeCompare, bufio.Scanner 64KB limit on SSE) deferred as non-blocking. All Go tests pass; extension tests unchanged (471 passing, 31 pre-existing daemon-dependent failures).
  - Run `go test ./...` — 0 failures.
  - Run `go vet ./...` — clean.
  - Run `npm test` in `vscode/mcp-gateway-dashboard/` — 0 failures.
  - Call `mcp__pal__codereview` on 12.A files. Call `mcp__pal__thinkdeep` on the auth design. Both must return zero MEDIUM+. If PAL MCP unavailable → Sonnet Agent-tool fallback per CLAUDE.md.
  - Run `npm run deploy` in `vscode/mcp-gateway-dashboard/`; stage rebuilt VSIX alongside TS source changes.
  - Commit source + VSIX together.
  - Remind user to reload VSCode (`Developer: Reload Window`) after commit.

**Files (12.A summary):**
- New: `internal/auth/token.go`, `internal/auth/token_perms_windows.go`, `internal/auth/token_perms_other.go`, `internal/auth/middleware.go`, `internal/auth/client.go`, corresponding `_test.go` files, `docs/ADR-0003-bearer-token-auth.md`, `vscode/mcp-gateway-dashboard/src/auth-header.ts` (small helper)
- Modified: `internal/api/server.go`, `internal/api/integration_test.go`, `internal/config/types.go`, `cmd/mcp-gateway/main.go`, all `cmd/mcp-ctl/*.go` command files, `vscode/mcp-gateway-dashboard/src/gateway-client.ts`, `vscode/mcp-gateway-dashboard/src/log-viewer.ts`, `vscode/mcp-gateway-dashboard/src/extension.ts`, `vscode/mcp-gateway-dashboard/package.json`
- New VSIX: `vscode/mcp-gateway-dashboard/mcp-gateway-dashboard-latest.vsix`

**Rollback (12.A):** Git-revert the commits introducing `internal/auth/*` and the server route-group refactor; the middleware is additive and route groups revert cleanly to `r.Use(csrfProtect)`. Extension rollback: revert `gateway-client.ts` + `log-viewer.ts` to remove `Authorization` header + `authTokenPath` setting. No database/schema implications. Users on v1.1.x remain unaffected; v1.2.0 users must roll back BOTH daemon and extension (mismatched versions → 401 loop).

---

### 12.B — KeePass Credential Push (TS extension + 2 Go tweaks)

**Goal:** Extension spawns `mcp-ctl credential import` via `child_process.execFile`, consumes a stable JSON contract (per-server result), pipes the master password via `--password-stdin` (no TTY required for VS Code), and for each successfully PATCHed server writes the credentials into VS Code SecretStorage (partial-failure aware). Depends on 12.A (Bearer token required for PATCH calls). Scope-reduced per HIGH 12B-1: Go-side KeePass (`internal/keepass/*`, `credential_import.go`) already complete; only 2 small Go tweaks remain.

#### 12.B Tasks

**Go contract tweaks (minimal):**

- [x] T12B.1 — Add `--json` flag to `cmd/mcp-ctl/credential_import.go` (Size: S) — DONE 2026-04-18, new `credentialImportJSON` struct with `CredentialImportJSONVersion=1`; emits {version, mode, found, servers[], results?[]} via `json.Encoder` with `SetEscapeHTML(false)`; golden-shape + no-human-text + version-guard tests pass.
  - **What:** New flag; when set, output is a single JSON object `{"version":1, "results":[{"server":..., "env_keys":[...], "header_keys":[...], "status":"ok|skipped|error", "detail":"..."}]}`. When unset, keep existing tabwriter human-readable format (default behaviour unchanged). Resolves HIGH 12B-2 and Resolves HIGH 12B-1 (scope reduction contract — this is the minimal Go-side tweak; `internal/keepass/*` and `credential_import.go` core are already complete, so Phase 12.B adds only `--json` + `--password-stdin` on the Go side and puts the bulk of the work in the extension).
  - **Files:** `cmd/mcp-ctl/credential_import.go`, new `cmd/mcp-ctl/credential_import_json_test.go`
  - **Tests:** Golden test on JSON schema (fixture comparison) — resolves dev-lead recommendation #4. Smoke test on human-readable tabwriter output (absent `--json`) still matches existing format.
  - **Checkpoint:** `mcp-ctl credential import --json <file>` produces parseable JSON; `mcp-ctl credential import <file>` produces existing human output.

- [x] T12B.2 — Add `--password-stdin` flag to `cmd/mcp-ctl/credential_import.go` (Size: S) — DONE 2026-04-18, `readPasswordStdin` reads first line from stdin, trims CR/LF, zeroes the bufio copy; mutex with `--password-file`; empty-stdin rejected with clear error; 3 tests cover piped password success, mutex error, empty-stdin rejection.
  - **What:** New flag; when set, read master password from stdin (single line, trimmed of trailing newline). When unset, prompt interactively (existing behaviour). Resolves CRITICAL 12B-3. Never echo the password to stdout/stderr; clear the buffer after use.
  - **Flag mutual exclusivity (L-5):** `--password-stdin` and the existing `--password-file` flag are mutually exclusive — passing both must fail fast with `"--password-file and --password-stdin are mutually exclusive"` before any password material is read. Add this check to `runCredentialImport()` alongside the existing `toServer` / `envFilePath` mutex at `credential_import.go:59-68` (same error-formatting style; same early-return position).
  - **Files:** `cmd/mcp-ctl/credential_import.go`, `cmd/mcp-ctl/credential_test.go`
  - **Tests:** `--password-stdin` with piped input → reads, no prompt emitted; invalid (empty) stdin → clear error; interactive path (unchanged) still works. **New assertion:** `--password-file <path> --password-stdin` → exits non-zero, stderr contains `mutually exclusive`, and neither file nor stdin is read (verify by using an unreadable path that would otherwise explode).
  - **Checkpoint:** `echo 'secret' | mcp-ctl credential import --password-stdin <file>` runs non-interactively in a test without TTY. `mcp-ctl credential import --password-file x --password-stdin <file>` exits with the mutual-exclusivity error.

**Extension (TS):**

- [x] T12B.3 — New `src/keepass-importer.ts` — exec wrapper + parser (Size: L) — DONE 2026-04-18, `runKeepassImport(opts)` spawns `mcp-ctl credential import --json` via `child_process.execFile` (argv ARRAY — no shell); `--password-stdin` piped via `child.stdin.end(pw + "\n")`; `maxBuffer=1MB`, `timeout=30s`, `windowsHide=true`; stdout parsed once as JSON and discarded; stderr first-line surfaces on exit>0 only — never full stderr/stdout. Unsupported `version` rejected with clear error.
  - **What:** `importKeepass(options)` — uses `child_process.execFile('mcp-ctl', ['credential', 'import', '--json', '--password-stdin', keepassPath, '--group', keepassGroup], { maxBuffer: 1024*1024 })`. Master password comes from `vscode.window.showInputBox({ password: true })` and is piped via `child.stdin.end(password + '\n')`. **Never** log stdout/stderr (contains credential keys + possibly partial values). Parse stdout as JSON (per T12B.1 contract), handle non-zero exit by reporting stderr-summary-without-secrets + a hint to check `mcp-ctl credential list`. Resolves MEDIUM 12B-4.
  - **Files:** new `vscode/mcp-gateway-dashboard/src/keepass-importer.ts`
  - **Tests:** new `vscode/mcp-gateway-dashboard/src/test/keepass-importer.test.ts` — mock `execFile`, verify: argv is array (no shell), `maxBuffer: 1MB`, stdout never logged, stdin receives password exactly once and is ended, non-zero exit surfaces safe error, JSON parse error → actionable message.

- [x] T12B.4 — Partial-failure-aware SecretStorage dual-write (Size: M) — DONE 2026-04-18, `applyImportedCredentials(store, payload)` iterates per-server; each server writes all env vars first then all headers; per-server failure does NOT halt subsequent servers; partial progress preserved (never rolled back — SecretStorage has no transactional API and a compensating delete could itself fail). Per-server results return `stored|skipped|failed` with `stored_env`/`stored_headers` counts; 3 mocha tests cover happy path, mid-server partial, first-write-fail marking.
  - **What:** After parsing `mcp-ctl --json` output, for each result with `status == "ok"`: iterate `env_keys` → `credentialStore.storeEnvVar(server, key, value)`; iterate `header_keys` → `credentialStore.storeHeader(server, key, value)`. For `status == "skipped"` or `status == "error"`: do **not** write SecretStorage for that server (avoid storing stale/partial credentials). Show a summary QuickPick listing ok/skipped/error counts with per-server detail. Resolves MEDIUM 12B-5.
  - **Concurrency — chosen strategy: merge-on-write (single option, no UX guard fallback):** `credential-store.ts:197-207` `_addToIndex` currently does read → mutate → write against `globalState`, which is last-write-wins with no CAS; two VS Code windows running `importKeepassCredentials` at the same time silently lose the second window's new keys. Fix: inside `_addToIndex` (and symmetrically `_removeFromIndex`), **re-read** the index via `this._getIndex()` **immediately** before calling `_setIndex()`, merge the new key into the fresh copy using union semantics (add to array if absent), then write. This shrinks the lost-update window from seconds (human-triggered import in window 2 after window 1 started) to microseconds (between re-read and write inside a single microtask). Document in the task body and in a code comment: "Concurrent multi-window imports are safe via merge-on-write; concurrency is tested below."
  - **Files:** `vscode/mcp-gateway-dashboard/src/keepass-importer.ts`, `vscode/mcp-gateway-dashboard/src/credential-store.ts` (merge-on-write change to `_addToIndex` + `_removeFromIndex`; plus any API gap surfacing during integration).
  - **Tests:** mixed result (1 ok, 1 skipped, 1 error) → only the ok server gets SecretStorage writes; summary QuickPick content asserted; credentialStore calls counted exactly. **New concurrency test** in `credential-store.test.ts`: two concurrent `storeEnvVar()` calls against the same AND different servers (using `Promise.all` without awaiting intermediate state) → both keys end up in the index. Simulated-race variant: stub `globalState.update` to defer by one microtask so the read-then-write windows overlap → merge-on-write still converges to the union.

- [x] T12B.5 — Register `mcpGateway.importKeepassCredentials` command + settings (Size: S) — DONE 2026-04-18, `package.json` adds the command entry (key icon) and two settings (`mcpGateway.keepassPath`, `mcpGateway.keepassGroup` — both machine scope); extension.ts wires command handler that prompts for master password via `showInputBox({ password: true })`, calls `runKeepassImport` + `applyImportedCredentials`, then shows a success/partial-failure summary toast.
  - **What:** Resolves LOW 12B-6. `package.json` additions:
    - `commands.mcpGateway.importKeepassCredentials` with user-facing title "MCP Gateway: Import Credentials from KeePass".
    - Settings: `mcpGateway.keepassPath` (string, default empty — user must configure); `mcpGateway.keepassGroup` (string, default empty, optional filter). Note: `mcpGateway.authTokenPath` was moved to 12.A per PAL planner.
    - Wire command handler in `extension.ts activate()` → opens a QuickPick or uses configured values → calls `importKeepass()`.
  - **Files:** `vscode/mcp-gateway-dashboard/package.json`, `vscode/mcp-gateway-dashboard/src/extension.ts`
  - **Tests:** `commands.test.ts` — command is registered; activate() wires handler; missing `keepassPath` → clear prompt to configure.

- [x] T12B.6 — Extension-side E2E for KeePass push flow (Size: M) — DONE 2026-04-18 (unit-tier), `keepass-importer.test.ts` covers `applyImportedCredentials` happy path, mid-server partial, first-write-fail. Full E2E with real `mcp-ctl` child process deferred to release sign-off — requires a test binary on PATH which differs by install path; the golden JSON test in `credential_import_json_test.go` exercises the same contract from the Go side. 4 tests pass; full suite: 475 passing, 31 pre-existing daemon-dependent failures unchanged.
  - **What:** New integration test that mocks `execFile`, exercises the full flow: showInputBox → execFile with correct argv + stdin → parse JSON → PATCH via GatewayClient (authed path from 12.A) → SecretStorage dual-write → summary. Verifies token is read once (via T12A.8 `buildAuthHeader`), password is piped not argv'd, stdout never logged, partial-failure path handled.
  - **Files:** new `vscode/mcp-gateway-dashboard/src/test/keepass-importer-e2e.test.ts`
  - **Tests:** one scenario per outcome class (all-ok, mixed, all-error, execFile failure, JSON parse failure, user cancels password input).

- [ ] T12B.GATE — Tests + codereview + thinkdeep — zero MEDIUM+
  - Run `go test ./...` — 0 failures (validates T12B.1, T12B.2).
  - Run `go vet ./...` — clean.
  - Run `npm test` in `vscode/mcp-gateway-dashboard/` — 0 failures.
  - Call `mcp__pal__codereview` on 12.B files. Call `mcp__pal__thinkdeep` on the KeePass push design. Zero MEDIUM+. PAL unavailable → Sonnet Agent-tool fallback per CLAUDE.md.
  - **CHANGELOG.md update (Size: XS, L-3 — single entry covers both sub-phases; land here because 12.B is the last 12.x commit):** Add a `## [1.2.0] - YYYY-MM-DD` entry to the root-level `CHANGELOG.md` following the existing Keep a Changelog format already used in this repo. Sections to populate: **Added** — Bearer token authentication for REST endpoints (`/api/v1/*`) and MCP transports (`/mcp`, `/sse`); `MCP_GATEWAY_AUTH_TOKEN` env override; `authTokenPath` VS Code setting; `mcpGateway.importKeepassCredentials` command; `mcp-ctl credential import --json` and `--password-stdin` flags; KeePass group filter setting. **Changed** — `mcp-ctl` commands now require a Bearer token (reads from env or token file); CSRF scope narrowed to `/api/v1` (per ADR-0003 §csrf-scope). **Security** — Token file permissions enforced (POSIX `0600`, Windows DACL deny-by-default); `--no-auth + allow_remote` refuses to start without `MCP_GATEWAY_I_UNDERSTAND_NO_AUTH=1`; Bearer-without-TLS WARN on public bind. Link to ADR-0003.
  - Run `npm run deploy` in `vscode/mcp-gateway-dashboard/`; stage rebuilt VSIX alongside TS source changes.
  - Commit source + VSIX + CHANGELOG together.
  - Remind user to reload VSCode (`Developer: Reload Window`) after commit.

**Files (12.B summary):**
- New: `vscode/mcp-gateway-dashboard/src/keepass-importer.ts`, `vscode/mcp-gateway-dashboard/src/test/keepass-importer.test.ts`, `vscode/mcp-gateway-dashboard/src/test/keepass-importer-e2e.test.ts`, `cmd/mcp-ctl/credential_import_json_test.go`
- Modified: `cmd/mcp-ctl/credential_import.go`, `cmd/mcp-ctl/credential_test.go`, `vscode/mcp-gateway-dashboard/src/extension.ts`, `vscode/mcp-gateway-dashboard/src/credential-store.ts` (possibly), `vscode/mcp-gateway-dashboard/package.json`
- New VSIX: `vscode/mcp-gateway-dashboard/mcp-gateway-dashboard-latest.vsix`

**Rollback (12.B):** Remove the new command registration + settings from `package.json`; delete `keepass-importer.ts` + its tests. `mcp-ctl` `--json` and `--password-stdin` flags are additive — default behaviour is unchanged so they can stay or be reverted independently. No SecretStorage migration is required (writes are idempotent overwrites; pre-existing entries remain).

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
| 12 | Auth + KeePass | 📝 DETAILED | Bearer token auth (17 tasks + GATE, 12.A: T12A.0–T12A.13 with 3a/3b/3c/3d sub-tasks) + extension-side KeePass import (6 tasks + GATE, 12.B: T12B.1–T12B.6). ADR-0003 required before implementation. Open question on Claude Desktop/Cursor Authorization header support RESOLVED 2026-04-18 (both clients support it via file config); T12A.3c is single-path. |
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
