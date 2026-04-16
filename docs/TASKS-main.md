# Phase 11 (v1.1.0) — Implementation Tasks

**Session:** main
**Pipeline:** feature-ad5b2ffc
**Source plan:** docs/PLAN-main.md sections 11.A–11.E + architect appendix + user decisions
**Extension directory:** `vscode/mcp-gateway-dashboard/` (all paths below are relative to this unless noted otherwise)
**Created:** 2026-04-15

## Execution Order (critical path)

1. **Preparation (mock/test infra):** T11B.0, T11E.0
2. **Phase 11.A** — tree stability + inline buttons + `lastRefreshFailed` API (blocks 11.B and 11.E)
3. **Phase 11.B** — sidebar WebviewView detail + status bar tooltips + McpStatusBar cache migration
4. **Phase 11.C** — Add Server webview form (independent; can run in parallel with 11.B after 11.A)
5. **Phase 11.D** — SAP hierarchical grouping + Add SAP webview (independent; can run in parallel)
6. **Phase 11.E** — slash command auto-generation (blocked by 11.A `lastRefreshFailed`)
7. Each sub-phase ends with `GATE`: `npm test` + PAL codereview + PAL thinkdeep — zero MEDIUM+

## Dependency Graph

```
T11B.0 (mock MarkdownString) ─┐
T11E.0 (tmpdir test helper) ──┤
                              ▼
                         Phase 11.A  ──┬─► Phase 11.B
                                       │
                                       ├─► Phase 11.E
                                       │
                         Phase 11.C (independent)
                         Phase 11.D (independent)
```

`lastRefreshFailed` in ServerDataCache is introduced in 11.A (T11A.3) so both 11.B status bar migration and 11.E orphan cleanup consume a stable API.

---

## Phase 11.A — Tree Stability + Inline Buttons

**Goal:** Eliminate tree flicker via fingerprint-based refresh skip; expand inline start/stop/restart icons; add `lastRefreshFailed` signal to cache.

- [x] **T11A.1** — Compact fingerprint in `BackendTreeProvider`. Fields per server: `name|status|transport|restart_count|pid|last_error|tools.length`. `lastFingerprint: string | null` instance field; `refresh()` skips `fire()` when unchanged; reset to `null` in `dispose()`. REFINEMENT A-1, A-2.
- [x] **T11A.2** — Fingerprint pattern for `SapTreeProvider`. Per-system fields: `key|status|vsp.status|gui.status|vsp.restart_count|gui.restart_count|vsp.pid|gui.pid|vsp.last_error|gui.last_error`. Expanded beyond original spec per cross-review finding to cover silent process restarts.
- [x] **T11A.3** — `ServerDataCache.lastRefreshFailed` getter. `CacheRefreshPayload = { servers, lastRefreshFailed }` event type. `true` in catch block, `false` after success. REFINEMENT B-4.
- [x] **T11A.4** — Inline `startServer` menu entry: `viewItem == disabled` + `$(play)` icon. Kept existing context-menu entry. REFINEMENT A-3.
- [x] **T11A.5** — Inline `stopServer` menu entry: `viewItem =~ /^(running|degraded)$/` + `$(debug-stop)` icon. REFINEMENT A-3.
- [x] **T11A.6** — Inline `restartServer` expanded from `(error|degraded)` to `(running|degraded|error|stopped)`. REFINEMENT A-3.
- [x] **T11A.7** — Fingerprint tests: identical→skip, status→fire, restart_count→fire. In `backend-tree-provider.test.ts` and `sap-tree-provider.test.ts`.
- [x] **T11A.8** — `ServerDataCache.lastRefreshFailed` tests: getter mirrors outcome; flip true→false→true; payload exposes flag.
- [x] **T11A.9** — `dispose()` resets fingerprint field to `null` — tests in both tree providers.
- [x] **T11A.GATE** — 278 passing (+11 new tests). 31 pre-existing GatewayClient/LogViewer failures unchanged (need live daemon at localhost:8765, unrelated to 11.A). Sonnet cross-review (PAL MCP unavailable fallback) found 1 MEDIUM: SapTreeProvider fingerprint missed `pid` + `last_error` → fixed with 2 regression tests. Zero MEDIUM+ remaining.

---

## Phase 11.B — Sidebar Detail View + Status Bar Tooltips

**Goal:** Always-on sidebar `WebviewView` showing details of currently-selected server; MarkdownString tooltips on status bars; migrate `McpStatusBar` from timer polling to cache events.

**Note:** REWRITTEN from original plan. Uses `WebviewViewProvider` (always-on) instead of `WebviewPanel` (click-toggle). Existing `server-detail-panel.ts` is KEPT and still accessible via context-menu action — deletion deferred to future phase.

- [x] **T11B.0** — Extend `src/test/mock-vscode.ts`: add `MockMarkdownString` class with fields `value: string`, `isTrusted: boolean`, `supportHtml: boolean`, and method `appendMarkdown(value: string): this`. Export as `vscode.MarkdownString`. Widen `MockStatusBarItem.tooltip` and `MockTreeItem.tooltip` types to `string | MockMarkdownString | undefined`. Also added `MockWebviewView` + `createMockWebviewView` factory and stubs for `window.registerWebviewViewProvider` and `createTreeView().onDidChangeSelection`. REFINEMENT B-2. Blocks all 11.B tooltip tests.
  - **Files:** `src/test/mock-vscode.ts`
- [x] **T11B.1** — Registered new view `mcpGateway.serverDetail` in `package.json` under existing `mcp-gateway` view container (NOT `mcp-gateway-container` as originally planned — that id does not exist). Type `webview`, name `Server Detail`, placed below existing backend/SAP tree views.
  - **Files:** `package.json`
- [x] **T11B.2** — New file `src/webview/server-detail-view-provider.ts` implementing `vscode.WebviewViewProvider`. Constructor takes `(extensionUri, cache, credentialStore)`. Selection is tracked internally via `setMcpSelection(server|null)` / `setSapSelection(system|null)` / `clearSelection()` — extension.ts wires tree views' `onDidChangeSelection` to these methods so the provider stays testable without mocking full TreeView objects. `resolveWebviewView` sets webview options (enableScripts + localResourceRoots), CSP + nonce per existing pattern. Idempotent: a second resolve clears the prior view reference.
  - **Files:** new `src/webview/server-detail-view-provider.ts`
- [x] **T11B.3** — Provider subscribes to `cache.onDidRefresh` (in constructor); extension.ts wires `backendTreeView.onDidChangeSelection` → `provider.setMcpSelection(first.server | null)` and `sapTreeView.onDidChangeSelection` → `provider.setSapSelection(first.system | null)`. Placeholder HTML ("Select a server to view details") rendered via `buildDetailPlaceholderHtml` when no selection or selected item vanished from cache. Render is guarded with a `renderSeq` generation counter to prevent stale HTML writes after rapid selection changes (HIGH finding from cross-review fix).
  - **Files:** `src/webview/server-detail-view-provider.ts`, `src/extension.ts`
- [x] **T11B.4** — `buildMcpDetailHtml` and `buildSapDetailHtml` in `src/webview/html-builder.ts` were already pure (took `McpDetailData`/`SapDetailData` structs, no state). Added new `buildDetailPlaceholderHtml(nonce, cspSource)` for the empty-selection state. Legacy `ServerDetailPanel`/`SapDetailPanel` unchanged — they still use the same `buildMcpDetailHtml` / `buildSapDetailHtml` functions.
  - **Files:** `src/webview/html-builder.ts`
- [x] **T11B.5** — Wired `vscode.window.registerWebviewViewProvider(ServerDetailViewProvider.viewType, provider)` in `extension.ts activate()`. Provider + registration + both tree-view selection subscriptions pushed to `context.subscriptions`. Uses `BackendItem`/`SapSystemItem instanceof` checks to extract the selected model object.
  - **Files:** `src/extension.ts`
- [x] **T11B.6** — `McpStatusBar.buildTooltip` returns `vscode.MarkdownString` with summary line + per-status sections listing escaped server names. `isTrusted = false`, `supportHtml = false`. Per-status deterministic order: `running → degraded → error → restarting → starting → stopped → disabled` (skips empty buckets).
  - **Files:** `src/status-bar.ts`
- [x] **T11B.7** — `SapStatusBar.buildTooltip` returns `vscode.MarkdownString` with SAP systems count + per-system block showing VSP/GUI sub-statuses.
  - **Files:** `src/sap-status-bar.ts`
- [x] **T11B.8** — `McpStatusBar` rewritten for cache-driven operation: constructor now takes `ServerDataCache`, subscribes to `cache.onDidRefresh` directly (`private refresh()` method), no timer/`poll`/`startPolling`/`stopPolling`. `extension.ts activate()` passes `cache` instead of `client`, removed `statusBar.startPolling(pollInterval)` call. Counts `cache.getMcpServers()` (MCP only, excluding SAP) — intentional UX change: SAP servers now only count toward `SapStatusBar`. Daemon-offline state uses `cache.lastRefreshFailed` (no separate `/health` call). REFINEMENT B-3.
  - **Files:** `src/status-bar.ts`, `src/extension.ts`
- [x] **T11B.9** — `BackendItem.tooltip` and `SapItem.tooltip` now `MarkdownString` with status, transport, pid, restart_count, last_error, tool count (and, for SAP, per-component VSP/GUI breakdowns).
  - **Files:** `src/backend-item.ts`, `src/sap-item.ts`
- [x] **T11B.10** — Unit tests for `server-detail-view-provider`: placeholder on initial/no-selection state; webview options set on resolve; MCP detail HTML on MCP selection; SAP detail HTML on SAP selection; cache-refresh re-renders with fresh nonce; message routing (restart → `_webviewAction`); disallowed actions rejected; messages dropped when nothing selected; malformed `serverName` rejected; CSP forbids `'unsafe-inline'`; placeholder has CSP; dispose is no-throw. Plus one concurrent-render race test using a deferred credentialStore that proves newer beta selection is not clobbered by a stale alpha render still waiting on its credential promise (HIGH fix test).
  - **Files:** new `src/test/webview/server-detail-view-provider.test.ts`
- [x] **T11B.11** — Updated `src/test/backend-tree-provider.test.ts` (tooltip assertions cast to `MockMarkdownString.value`, new test asserting tooltip is a `MockMarkdownString` with `isTrusted=false` and `supportHtml=false`). `src/test/sap-status-bar.test.ts` had no tooltip assertions so it continues to pass unchanged under the MarkdownString change. `src/test/status-bar.test.ts` rewritten end-to-end for the cache-driven API: 20 tests covering initial state, cache-refresh in all running/partial/all-offline/no-servers/daemon-unreachable states, SAP-exclusion, dispose, state transitions, and a negative test asserting `poll`/`startPolling`/`stopPolling` are removed.
  - **Files:** `src/test/status-bar.test.ts`, `src/test/backend-tree-provider.test.ts`
- [x] **T11B.12** — Covered by `src/test/status-bar.test.ts` rewrite (see T11B.11). Dedicated "no legacy polling API" test asserts the methods are absent. Dedicated "dispose" test confirms the bar stops updating after `dispose()` even if subsequent `cache.refresh()` fires.
  - **Files:** `src/test/status-bar.test.ts`
- [x] **T11B.GATE** — `npm test` (276 passing, 0 failing in 513ms — excluding pre-existing daemon-dependent gateway-client.test.ts + log-viewer.test.ts which need live localhost:8765). PAL MCP unavailable; used Agent-tool fallback with Sonnet 4.6 for cross-model review (combined codereview + thinkdeep). First pass flagged 1 HIGH (concurrent render race) + 4 MEDIUM (apply payload vestigial, MarkdownString ctor inconsistency, onDidReceiveMessage disposable discarded, escapeMd duplicated without CLAUDE.md regex comment). All five fixed (renderSeq generation counter + guard at every HTML write, refresh() reads cache directly, consistent no-arg MarkdownString ctor, disposables list includes message + onDidDispose handlers, extracted `src/markdown-utils.ts` with justification comment). Second Sonnet pass: **APPROVE, zero MEDIUM+**. One LOW note on resolveWebviewView re-entry disposables growth — non-blocking, documented only. Also added `src/markdown-utils.ts` as a new shared util file.

---

## Phase 11.C — Add Server Webview Form

**Goal:** Replace sequential `InputBox`/`QuickPick` `addServer` flow with a single webview form; auto-detect transport from URL prefix; re-validate inputs on extension side.

- [x] **T11C.0** — Extract `validateServerName` and `validateUrl` regexes + helper functions to new shared module `src/validation.ts`. Import from `extension.ts` (currently defines them inline) and future `add-server-panel.ts`. Also added `validateStdioCommand`, `validateEnvEntry`, `validateHeaderEntry`, `parseEnvEntry`, `parseHeaderEntry`, `detectTransport`, and exported `SERVER_NAME_RE` / `ENV_KEY_RE` / `HEADER_NAME_RE`. `extension.ts` re-exports `validateServerName`/`validateUrl` for existing test imports. REFINEMENT C-3.
  - **Files:** new `src/validation.ts`, `src/extension.ts`
- [x] **T11C.1** — New file `src/webview/add-server-panel.ts` implementing an `AddServerPanel` class with `createOrShow(extensionUri, client, credentialStore, onCreated)` static factory. Uses existing webview pattern (CSP + nonce + postMessage). Singleton (one panel at a time). Receives `submit` / `cancel` messages; sends `nack` on validation or client failure. Disposes on success or explicit cancel. `retainContextWhenHidden=true` so switching tabs does not lose form state.
  - **Files:** new `src/webview/add-server-panel.ts`
- [x] **T11C.2** — Added `buildAddServerHtml(nonce, cspSource)` function to `src/webview/html-builder.ts`. Form fields: `name`, `target` (URL-or-command with live transport badge), `env` (textarea, KEY=VALUE per line), `headers` (textarea, Name: Value per line). Submit + Cancel buttons. Inline per-field error spans, banner for server-side nack errors. CSP forbids `'unsafe-inline'`; script only via nonce.
  - **Files:** `src/webview/html-builder.ts`
- [x] **T11C.3** — Client-side script inside the form: cross-platform `isAbsolutePath` (supports POSIX `/`, Windows drive letters `C:\\` / `C:/`, UNC `\\\\host`); auto-detect transport on every keystroke via `detectTransport`; validators mirror the extension-side ones (`SERVER_NAME_RE`, `ENV_KEY_RE`, `HEADER_NAME_RE`, URL constructor + scheme check). Validation is run both client-side (UX) AND extension-side (trust boundary). REFINEMENT C-1.
  - **Files:** `src/webview/html-builder.ts`
- [x] **T11C.4** — Replaced `addServer` command handler in `extension.ts` with a single `AddServerPanel.createOrShow(...)` call. Removed the `collectKeyValuePairs` helper (unused after the InputBox flow was deleted). Kept the original `errorMsg` helper for `guarded`. Kept `validateServerName`/`validateUrl`/`SERVER_NAME_RE` re-exports from `./validation` so existing tests continue to compile.
  - **Files:** `src/extension.ts`
- [x] **T11C.5** — `handleSubmit` in `add-server-panel.ts`: coerces the raw payload via `coercePayload` (rejects malformed shapes with a user-visible nack); re-validates name, target (URL for http, stdio command for stdio), every env entry, every header entry against `src/validation.ts`. Calls `client.addServer` with `{url: ...}` or `{command: ...}` + optional `env`/`headers`. Then iterates env/header entries with per-entry try/catch on `credentialStore.storeEnvVar` / `storeHeader` — partial credential failures surface as warning toasts but do NOT prevent server creation. On any validation or API error, posts `{type:'nack', error}` to the webview and keeps the panel open. On success: info toast, `onCreated()` callback (triggers `cache.refresh`), and panel dispose. REFINEMENT C-2, C-4.
  - **Files:** `src/webview/add-server-panel.ts`
- [x] **T11C.6** — New file `src/test/validation.test.ts` — 44 assertions covering `validateServerName` (valid, padded, empty, path traversal, leading separator, forbidden punctuation, length), `validateUrl` (http/https, empty, non-http schemes, malformed), `validateStdioCommand` (posix absolute, win32 absolute, empty, relative, npx-style), `validateEnvEntry` (valid, empty value, empty entry skip, missing equals, invalid key chars), `validateHeaderEntry` (valid, empty skip, missing colon, invalid name chars), `detectTransport` (http/https → http, everything else → stdio including `C:\\` paths), `parseEnvEntry` (first-equals split, missing equals, trim), `parseHeaderEntry` (first-colon split, missing colon, trim).
  - **Files:** new `src/test/validation.test.ts`
- [x] **T11C.7** — New file `src/test/webview/add-server-panel.test.ts` — 16 tests covering panel lifecycle (CSP/scripts/no-unsafe-inline, singleton, cancel disposal, malformed-message resilience), transport auto-detect (http://, https://, absolute stdio path), env + header flows (credential indexing, partial-failure warning preserves successful creation), server-side re-validation (rejects bad name, non-http URL, relative stdio, bad env key, bad header name, missing arrays → empty, non-object payload), and client failure path (nack + panel stays open).
  - **Files:** new `src/test/webview/add-server-panel.test.ts`
- [x] **T11C.8** — Replaced the old sequential-InputBox addServer tests in `src/test/commands.test.ts` with a single test asserting the command opens exactly one `mcpAddServer` webview panel (no client.addServer call at command time — end-to-end submit coverage lives in `add-server-panel.test.ts`). Added `mockWebviewPanels` import + singleton reset after the assertion.
  - **Files:** `src/test/commands.test.ts`
- [x] **T11C.GATE** — `npm test`: **352 passing, 31 pre-existing daemon-dependent failures** (same 31 as 11.A + 11.B GATE, unchanged — all in `gateway-client.test.ts` + `log-viewer.test.ts` which require live localhost:8765). +76 new passing tests in 11.C. PAL MCP unavailable → used Agent-tool fallback with Sonnet 4.6 for cross-model codereview + thinkdeep. **Round 1** found 3 HIGH + 5 MEDIUM: (HIGH) no in-flight submit guard allowing double addServer on crafted postMessage; (HIGH) stale `onCreated` callback on singleton re-reveal; (HIGH) `onCreated` called before `dispose()` without exception safety; (MEDIUM) `credential-store.ts` duplicated the SERVER_NAME/ENV_KEY/HEADER_NAME regexes locally; (MEDIUM) `validateStdioCommand` used Node `path.isAbsolute` producing cross-platform asymmetry; (MEDIUM) `coercePayload` respected the webview-supplied `transport` field which could disagree with target prefix; (MEDIUM) CSP missing `form-action 'none'`; (MEDIUM) webview in-script regex literals duplicated the canonical patterns with subtle backtick-escape hazard. **All eight fixed:** added `submitting` boolean guard with `try/finally` reset; made `onCreated` mutable and reassigned on re-reveal; reordered to dispose-first then callback-in-try/catch with error toast; `credential-store.ts` now imports regexes from `./validation`; new `isAbsolutePath` helper in `validation.ts` using string methods with POSIX / UNC / Windows drive-letter recognition; `coercePayload` always recomputes transport via `detectTransport(target)`; CSP extended with `form-action 'none'`; `buildAddServerHtml` injects the `.source` strings via `jsonForScript` into `new RegExp(...)` calls so the single validation.ts definitions drive the webview script. Added `concurrency and lifecycle` test block (3 tests for the HIGH fixes), `validation: isAbsolutePath` block (4 tests), `validation: webview regex parity` block (5 tests including CSP assertion). **Round 2 re-review:** both Sonnet codereview and Sonnet thinkdeep returned APPROVE with zero MEDIUM+; 5 new LOW observations (misleading parity test name for HEADER_NAME_RE, 3 untested `coercePayload` shape branches, UNC parity between TS/JS copies not independently end-to-end tested, double-escape maintainability note) — all non-blocking and tracked for future sweep.

---

## Phase 11.D — SAP Improvements (Hierarchical Grouping + Add SAP Webview)

**Goal:** Optional 2-level SAP tree (SapSystem → VSP/GUI children) behind a setting; Add SAP System webview form with SID/Client validation and duplicate detection.

**Note:** Interpretation 1 — SapSystem parents with VSP/GUI as direct children. Zero changes to `sap-detector.ts` — multi-client SIDs (e.g., A4H-000 and A4H-100) remain siblings. REFINEMENT D-1.

- [x] **T11D.1** — Added `mcpGateway.sapGroupBySid` configuration key (boolean, default `false`) to `package.json`.
  - **Files:** `package.json`
- [x] **T11D.2** — New class `SapComponentItem extends vscode.TreeItem` in `src/sap-item.ts`. Constructor: `(system, kind, server)`. `contextValue = sap-<kind>-<status>`, `collapsibleState: None`, icon matches server status. Tooltip is a `MarkdownString` with per-component status/transport/pid/restart/error details.
  - **Files:** `src/sap-item.ts`
- [x] **T11D.3** — Updated `SapSystemItem` constructor to accept a `hierarchical: boolean = false` flag. Hierarchical mode sets `collapsibleState: Collapsed` and `contextValue = sap-group-<status>`; flat mode preserves existing `sap-<status>` behavior.
  - **Files:** `src/sap-item.ts`
- [x] **T11D.4** — `SapTreeProvider.getChildren(element)` now returns collapsible `SapSystemItem` parents in hierarchical mode and `SapComponentItem` children (VSP/GUI) for each expanded parent. Constructor reads `mcpGateway.sapGroupBySid` and subscribes to `workspace.onDidChangeConfiguration('mcpGateway.sapGroupBySid')` — live toggle clears the fingerprint and fires `onDidChangeTreeData`. Fingerprint has an `H;`/`F;` prefix so mode flips rebuild the tree even when data is unchanged.
  - **Files:** `src/sap-tree-provider.ts`
- [x] **T11D.5** — Updated `package.json` view/item/context menu when-clauses. Each SAP action (restartSapVsp, restartSapGui, showSapVspLogs, showSapGuiLogs, showSapDetail) uses an alternation that explicitly enumerates flat-mode statuses (`sap-(running|stopped|error|degraded|disabled|starting|restarting)`) plus the hierarchical-mode markers (`sap-group-`, `sap-vsp-` or `sap-gui-`). This prevents the `sap-` prefix from bleeding onto `sap-vsp-*` / `sap-gui-*` child rows and producing duplicate menu entries (fixed by round-1 cross-review H-1). Added new view/title entry for `mcpGateway.addSapSystem` on the `mcpSapSystems` view.
  - **Files:** `package.json`
- [x] **T11D.6** — New file `src/webview/add-sap-panel.ts` — singleton `AddSapPanel` with `createOrShow(extensionUri, client, cache, onCreated)` factory. Uses the same trust-boundary + in-flight-guard + mutable-onCreated + dispose-first-callback-in-try-catch pattern as `AddServerPanel`. Server-side re-validates SID/Client via `validateSapSid`/`validateSapClient`, re-validates executable paths via `validateStdioCommand`, creates `vsp-<SID>[-<CLIENT>]` and/or `sap-gui-<SID>[-<CLIENT>]` servers via `client.addServer({ command: <user-supplied absolute path> })`. Duplicate detection: `warnedDuplicateKeys: Set<string>` remembers every key the user has been warned about so a resubmit on the same key confirms (skipping the existing servers and creating the fresh ones), and a submit on a different key does not lose the previous key's confirmation (fixed by round-1 cross-review M-1).
  - **Files:** new `src/webview/add-sap-panel.ts`
- [x] **T11D.7** — Added `buildAddSapHtml(nonce, cspSource)` function to `src/webview/html-builder.ts`. Form fields: `sid` (auto-uppercased, required), `client` (3 digits, optional), `vsp-cmd` + `gui-cmd` (absolute paths, required if the corresponding checkbox is checked — fixed by round-1 cross-review F2 so the created servers actually start), VSP/GUI component checkboxes (at least one required). CSP identical to `buildAddServerHtml`: `default-src 'none'; style-src cspSource 'nonce-...'; script-src 'nonce-...'; form-action 'none';`. Regex sources for `SAP_SID_RE` and `SAP_CLIENT_RE` are injected via `jsonForScript` so the webview cannot drift from `validation.ts`. The submit handler re-uppercases the SID before client-side validation so browser autofill that pastes lowercase is handled without a spurious nack (fixed by round-1 cross-review F7). `text-transform: uppercase` scoped to `#sid` only.
  - **Files:** `src/webview/html-builder.ts`
- [x] **T11D.8** — Registered new command `mcpGateway.addSapSystem` in `package.json` (with `$(add)` icon). Added view/title menu entry so the `+` button appears in the mcpSapSystems toolbar. Wired the command in `extension.ts` to open `AddSapPanel.createOrShow`. Also updated the existing SAP component commands (`restartSapVsp`, `restartSapGui`, `showSapVspLogs`, `showSapGuiLogs`, `showSapDetail`) to accept both `SapSystemItem` and `SapComponentItem` via a new `resolveSapServer(item, kind)` helper that reads the VSP/GUI server name from whichever container was clicked. `sapTreeView.onDidChangeSelection` now also propagates `SapComponentItem` parent-system selection to the sidebar detail view (fixed by round-1 cross-review H-2).
  - **Files:** `package.json`, `src/extension.ts`
- [x] **T11D.9** — Extended `src/test/sap-tree-provider.test.ts` with a `hierarchical mode (Phase 11.D)` describe block (9 tests): flat default, `reads sapGroupBySid=true from config at construction time`, collapsible parents with VSP/GUI children, omitted components, `H;`/`F;` fingerprint marker, live `onDidChangeConfiguration` fire with end-to-end tree refresh, ignores unrelated config changes, no-op on unchanged value, flat-mode children query. Tests exercise the real config watcher path by setting `mockConfigValues['mcpGateway.sapGroupBySid']` and calling `fireConfigChange(...)` (fixed by round-1 cross-review F-4).
  - **Files:** `src/test/sap-tree-provider.test.ts`
- [x] **T11D.10** — New file `src/test/webview/add-sap-panel.test.ts` — 27 tests across panel lifecycle (CSP, singleton, cancel, malformed), happy path submits (VSP+GUI, VSP only, GUI only, server-side uppercase SID, command config wiring), server-side validation (bad SID length, punctuation, bad client length, clientless, no components, malformed shape), duplicate detection (warn-then-confirm + Set-based cross-key remembering per M-1 fix), concurrency (in-flight drop, total failure nack, partial failure warning), and the new command validation block (missing VSP command, missing GUI command, relative VSP command, user-supplied absolute paths flow through to addServer config, ignored command for unchecked component).
  - **Files:** new `src/test/webview/add-sap-panel.test.ts`
- [x] **T11D.11** — New file `src/test/sap-item.test.ts` (13 tests): flat-mode `SapSystemItem` contextValue / collapsibleState / tooltip; hierarchical-mode `SapSystemItem` contextValue `sap-group-<status>` / `Collapsed` / tooltip parity with flat; `SapComponentItem` VSP/GUI contextValue (`sap-vsp-<status>` / `sap-gui-<status>`), label uppercase kind, description = server status, tooltip with details, empty-value sections omitted. Also new `validateSapSid`/`validateSapClient` + SAP webview regex parity + CSP tests in `src/test/validation.test.ts`. SAP commands added to `src/test/commands.test.ts` `expectedCommands` list + new `SAP commands (Phase 11.D dispatch)` describe block (7 tests) exercising `resolveSapServer` via both `SapSystemItem` and `SapComponentItem` inputs including the wrong-kind no-op path.
  - **Files:** new `src/test/sap-item.test.ts`, `src/test/validation.test.ts`, `src/test/commands.test.ts`, `src/test/mock-vscode.ts` (fire-able `onDidChangeConfiguration`, `mockConfigValues` registry, `fireConfigChange` helper)
- [x] **T11D.GATE** — `npm test`: **430 passing, 31 pre-existing daemon-dependent failures** (unchanged since 11.A/B/C GATE, all in `gateway-client.test.ts` + `log-viewer.test.ts`). **+78 new passing tests in 11.D.** PAL MCP unavailable → Sonnet 4.6 Agent-tool fallback. **Round 1** (3 HIGH + 5 MEDIUM + 3 LOW) — all 11 findings fixed: tightened package.json when-clauses (enumerated flat statuses), SapComponentItem branch in sapTreeView selection handler, required absolute-path VSP/GUI executable fields, Set-based warnedDuplicateKeys, #sid-scoped text-transform, new validateSapSid/validateSapClient test suites, fire-able onDidChangeConfiguration mock + end-to-end config watcher tests, SAP commands in commands.test.ts expectedCommands + dispatch tests, submit-time uppercase re-normalization, `_setHierarchicalForTest` removed, dead `successKey` removed. **Round 2 re-review:** one legitimate MEDIUM (N-1: `showSapDetail` when-clause missing `sap-vsp-`/`sap-gui-`, preventing child-row context menu from offering the detail view) **— fixed**. One MEDIUM flagged by both agents against the webview-JS `isAbsolutePath` UNC path check claiming a template-literal escape bug **— verified false positive** via direct `node -e` eval of the emitted function against UNC, Windows drive, and POSIX samples (all return true as expected). Added three new `webview isAbsolutePath runtime parity` tests that extract the emitted JS via `new Function(...)` and assert behavior matches the TS `isAbsolutePath` exactly — regression guard preventing future reviewers from repeating the escape-counting error. **Round 3 re-run after N-1 fix:** zero MEDIUM+, tests pass (430/31).

---

## Phase 11.E — Slash Command Auto-generation

**Goal:** Auto-generate `.claude/commands/<server>.md` files as servers transition to running/degraded; delete when stopped; orphan-cleanup; magic-header marker protects user-authored files.

- [x] **T11E.0** — Extend test harness with tmpdir filesystem helper. Add `src/test/helpers/tmpdir.ts` exporting `createTmpDir(): string` and `cleanupTmpDir(path: string): void` (uses `fs.mkdtempSync(os.tmpdir() + '/mcp-gw-test-')` + `fs.rmSync(..., { recursive: true, force: true })`). Call cleanup in test `afterEach`. Blocks all 11.E filesystem tests.
  - **Files:** new `src/test/helpers/tmpdir.ts`
- [x] **T11E.1** — Add two configuration keys to `package.json`:
  - `mcpGateway.slashCommandsEnabled`: boolean, default `false` (opt-in, REFINEMENT E-7)
  - `mcpGateway.slashCommandsPath`: string, default `"${workspaceFolder}/.claude/commands"` (REFINEMENT E-1)
  - **Files:** `package.json`
- [x] **T11E.2** — New file `src/slash-command-generator.ts`. Class `SlashCommandGenerator`. Constructor: `(private cache: ServerDataCache)`. Private fields: `previousStatus: Map<string, string>` (empty initially), `lastTask: Promise<void> = Promise.resolve()` (queue anchor), `loggedUnmarkedFiles: Set<string>` (session-scoped dedup for log spam).
  - **Files:** new `src/slash-command-generator.ts`
- [x] **T11E.3** — Implement `resolveCommandsDir(): string | null` method. Read `mcpGateway.slashCommandsPath` setting; expand `${workspaceFolder}` against `vscode.workspace.workspaceFolders?.[0]?.uri.fsPath`. Return `null` if no workspace folder and default value is used (graceful skip). Support absolute paths as-is. REFINEMENT E-1.
  - **Files:** `src/slash-command-generator.ts`
- [x] **T11E.4** — Define magic-header constant: `const MARKER = '<!-- AUTO-GENERATED by mcp-gateway extension. DO NOT EDIT — will be overwritten. -->';`. Implement private `isOwnedFile(path): Promise<boolean>`: if file does not exist → `true` (safe to write); read first line → return `true` iff matches marker exactly; else log once via `loggedUnmarkedFiles` guard and return `false`. REFINEMENT E-2.
  - **Files:** `src/slash-command-generator.ts`
- [x] **T11E.5** — Implement `generateCommand(server): Promise<void>`: check `isOwnedFile`; if owned or new, write content (T11E.9 template) with marker as line 1. Wrap `fs.writeFile` in try/catch; log failure without throwing. REFINEMENT E-2.
  - **Files:** `src/slash-command-generator.ts`
- [x] **T11E.6** — Implement `deleteCommand(name): Promise<void>`: compute path; check `isOwnedFile`; if owned, `fs.unlink`. If file missing → no-op (silent). Wrap in try/catch. REFINEMENT E-2.
  - **Files:** `src/slash-command-generator.ts`
- [x] **T11E.7** — Implement single-task async queue: private helper `enqueue(task: () => Promise<void>): void` that does `this.lastTask = this.lastTask.then(task).catch(err => log)`. All `generateCommand`/`deleteCommand` calls go through `enqueue`. Prevents interleaved writes and delete-before-write races. REFINEMENT E-4.
  - **Files:** `src/slash-command-generator.ts`
- [x] **T11E.8** — Implement transition handler: `onCacheRefresh(payload): void` subscribes to `cache.onDidRefresh`. Iterate new servers: compare `server.status` against `previousStatus.get(server.name)`. If transition to `running` or `degraded` → `enqueue(generateCommand)`. If transition to `stopped` / `disabled` or server removed (present in map but not in new list) → `enqueue(deleteCommand)`. Update `previousStatus` map. REFINEMENT E-3.
  - **Files:** `src/slash-command-generator.ts`
- [x] **T11E.9** — Implement orphan cleanup pass in `onCacheRefresh`: if `payload.lastRefreshFailed === true` → SKIP orphan cleanup (otherwise a transient daemon outage deletes all files). Else: `fs.readdir` resolved dir, filter `.md` files, for each filename not in current cache names → `enqueue(deleteCommand(name))`. REFINEMENT E-5.
  - **Files:** `src/slash-command-generator.ts`
- [x] **T11E.10** — Implement `contentTemplate(server): string` — minimal skeleton: marker line 1; `# <name>`; status line; transport line; `## Tools` with tool names list; `## Invocation` header referencing `mcp-gateway` routing. Rich enrichment deferred to Phase 14.7. REFINEMENT E-6.
  - **Files:** `src/slash-command-generator.ts`
- [x] **T11E.11** — Wire generator in `extension.ts activate()`: read `slashCommandsEnabled` setting; if `true`, instantiate `new SlashCommandGenerator(cache)`, subscribe to `cache.onDidRefresh` via the generator, push disposable to `context.subscriptions`. Watch `workspace.onDidChangeConfiguration('mcpGateway.slashCommandsEnabled')` to enable/disable at runtime.
  - **Files:** `src/extension.ts`
- [x] **T11E.12** — Unit test: `generateCommand` writes new file with marker as line 1, content matches template, first run produces file on disk.
  - **Files:** new `src/test/slash-command-generator.test.ts`
- [x] **T11E.13** — Unit test: `generateCommand` overwrites existing file when marker is present; `generateCommand` skips existing file when marker is absent; log-once dedup on repeated skip.
  - **Files:** `src/test/slash-command-generator.test.ts`
- [x] **T11E.14** — Unit test: `deleteCommand` deletes file with marker; `deleteCommand` skips file without marker; no throw on missing file.
  - **Files:** `src/test/slash-command-generator.test.ts`
- [x] **T11E.15** — Unit test: orphan cleanup deletes generator-authored files not in cache; orphan cleanup SKIPS when `lastRefreshFailed === true`; no-workspace scenario returns `null` from `resolveCommandsDir` and generator no-ops.
  - **Files:** `src/test/slash-command-generator.test.ts`
- [x] **T11E.16** — Unit test: transition detection — first refresh seeds map without emitting writes; subsequent `stopped → running` transition triggers `generateCommand`; `running → stopped` triggers `deleteCommand`.
  - **Files:** `src/test/slash-command-generator.test.ts`
- [x] **T11E.17** — Unit test: single-task queue serializes concurrent generate+delete calls (assert order via spy).
  - **Files:** `src/test/slash-command-generator.test.ts`
- [x] **T11E.GATE** — `npm test` + `mcp__pal__codereview` + `mcp__pal__thinkdeep`. Zero MEDIUM+.

---

## Global Verification (after all sub-phases complete)

- [ ] **GV.1** — Full `npm test` run in `vscode/mcp-gateway-dashboard/` — all tests pass with zero failures. Quote the final summary line in the commit message.
- [ ] **GV.2** — `npm run deploy` — verify compile + package + install chain succeeds. Confirm `mcp-gateway-dashboard-latest.vsix` is regenerated and installed via `code --install-extension ... --force`.
- [ ] **GV.3** — Manual smoke in VS Code (user-assisted): reload window; verify (a) tree stability (no flicker on idle polling), (b) inline start/stop/restart icons work on correct statuses, (c) sidebar detail view updates on tree selection, (d) status bar tooltips render as Markdown with per-server breakdown, (e) Add Server webview opens and creates a test server, (f) SAP `sapGroupBySid` toggle switches tree between flat and hierarchical, (g) Add SAP webview validates SID/Client and detects duplicates, (h) `slashCommandsEnabled` generates `.md` files on server start and deletes on stop, (i) user-authored `.md` files without marker are preserved.
- [ ] **GV.4** — Stage rebuilt `mcp-gateway-dashboard-latest.vsix` alongside source changes in the commit (per CLAUDE.md VSCode extension build discipline).
- [ ] **GV.5** — After merge: remind user to reload VSCode window (`Developer: Reload Window`).

---

## Task Count Summary

| Sub-phase | Tasks | GATE | Total |
|-----------|-------|------|-------|
| 11.A | 9 | 1 | 10 |
| 11.B | 12 (includes T11B.0 prep) | 1 | 13 |
| 11.C | 8 | 1 | 9 |
| 11.D | 11 | 1 | 12 |
| 11.E | 17 (includes T11E.0 prep) | 1 | 18 |
| Global verification | 5 | — | 5 |
| **Total** | **62** | **5** | **67** |

**Next agent:** backend-dev — begin with T11B.0 and T11E.0 (test-infra prep), then Phase 11.A.
