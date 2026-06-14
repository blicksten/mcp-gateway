# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased] — Stability: GET notification-stream 400 hot-loop storm

**Incident 2026-06-14.** With 13 parallel Claude Code sessions the gateway took a flat **~77 transport requests/second** (zero jitter, evenly spread across all 23 MCP surfaces), bloating `daemon.log` to **832 MB** and saturating the MCP router so new `initialize` handshakes timed out (PAL/orchestrator namespaces failed to register in-session). The daemon process itself was healthy — this was a request storm, not a respawn cascade.

**Root cause** (`internal/api/resumable_streamable.go:251-254`): a Claude Code MCP client whose notification GET stream loses its session reopens `GET /mcp/<backend>` with **no `Mcp-Session-Id`**. In stateful mode that is always a protocol error → HTTP 400. The client retries with **no backoff** (upstream anthropics/claude-code#57642) on a ~298ms timer; 13 sessions × 23 backends = the steady 77/s. Same 400→reconnect-storm *class* as "Bug A" below, different trigger (GET notification stream vs. POST-init on an error-state backend).

### Fixed

- **Early-reject GET with empty session id** (`internal/api/server.go`, `mcpTransportPolicy`): the pathological shape (`GET` + empty `Mcp-Session-Id`) is now rejected at the policy layer with a cheap 400, before the session-map lookup / protocol negotiation in the resumable handler. Keyed **strictly** on the empty-header shape — a GET carrying an unknown/stale session id is untouched so it still reaches `tryResurrect` for restart recovery (FM-3). Healthy GET streams always carry a session id post-`initialize`, so they are unaffected.
- **Happy-path transport logs dropped to DEBUG** (`internal/api/server.go`, `logMCPDecision`): `allow-loopback` / `allow-if-bearer` now log at `slog.LevelDebug` (daemon runs at `LevelInfo`, so they drop at the handler). `deny-*` decisions stay at INFO — rare and security-relevant. This removes the 832 MB/day log amplifier at the source.

**Verified live:** post-deploy the storm dropped from 77 req/s to **0 lines in 12s**; `POST initialize /mcp/pal` recovered to **HTTP 200 in 961ms** (was timing out); `GET` with no session id returns 400 in **41ms** (was ~298ms).

**Deferred to a follow-up pipeline:** per-path token-bucket throttle (429 + Retry-After) and a daemon-side size-capped log rotator (today the only rotation is the date-based VS Code `DaemonLogFile`, bypassed on CLI/Task-Scheduler launches).

---

## [Unreleased] — Stability: Claude Code reconnect storm + TCP fast-fail

Two bugs caused Claude Code to disconnect from mcp-gateway every 44 seconds whenever any configured HTTP backend was unreachable (e.g., VPN-dependent `pdap-docs` while VPN is off):

### Bug A — Empty backend stub keeps long-lived streams alive

`RebuildTools()` deleted `perBackendServer["pdap-docs"]` when the backend had 0 tools (StatusError). This caused `GET /mcp/pdap-docs` to return HTTP 400 "no server available". Claude Code treats any HTTP 400 during MCP initialize as a trigger to reinitialize ALL transports (exponential backoff starting at 8s), creating cascading reconnect cycles every ~44s in all active Claude sessions simultaneously.

**Fix** (`internal/proxy/gateway.go`): `RebuildTools()` now keeps an empty stub server for configured backends in error state. The empty stub returns HTTP 200 with 0 tools — Claude Code does not retry on 200. The stub is deleted only when the backend is removed from config entirely. A stale-tool cleanup pass clears any previously registered tools from the stub on error transition.

### Bug B — TCP connect hangs block health monitor for 42 seconds

`lifecycle/manager.go` `Start()` had no TCP connectivity check before calling `connectSafe()`. On Windows, an unreachable host blocked on `connectex` for ~42 seconds. With the health monitor retrying continuously (due to the CR-15 circuit-breaker reset-window bug), this accumulated hundreds of blocked goroutines per day.

**Fix** (`internal/lifecycle/transport.go` + `manager.go`): New `checkTCPReachable(ctx, rawURL, 3s)` helper does a quick TCP dial before the full MCP initialize handshake. On failure: returns `"host unreachable <addr>: ..."` in under 4 seconds instead of 42 seconds.

### Tests

- `TestRebuildTools_ErrorStateBackendKeepsStub`
- `TestStart_HTTPBackend_UnreachableHost_FastFail`
- 6 `TestCheckTCPReachable_*` unit tests
- `TestStart_StdioBackend_NoTCPCheck`
- **Total: 10 new tests, 130/130 passing**

---

## [Unreleased] — Fix: SSE 11-minute disconnect (WriteTimeout generalisation)

**Root cause:** `http.Server.WriteTimeout = 10 * time.Minute` fired on long-lived GET notification streams from Claude Code to the gateway (`/mcp`, `/mcp/*`, `/sse`, `/sse/*`). `ServerOptions.KeepAlive = 60s` pings do NOT reset Go's single-shot per-connection write deadline. Connections dropped after ~660s with "SSE stream disconnected: TimeoutError" → 2 retries → "HTTP connection closed after 692s with errors".

The prior F-8 fix (`handleServerLogs`, lines 1417-1422) already applied `SetWriteDeadline(time.Time{})` for the `/api/v1/servers/{name}/logs` SSE endpoint but the same pattern was missing from the streamable/SSE handlers on the MCP transport routes.

### Fixed

- **Per-connection deadline cleared for GET** (`internal/api/server.go`): Added `clearWriteDeadlineForGET` middleware wrapping the four MCP routes: `/mcp`, `/mcp/*`, `/sse`, `/sse/*`. The middleware calls `http.NewResponseController(w).SetWriteDeadline(time.Time{})` on GET requests only — POST requests retain `WriteTimeout` for slow-write DoS protection (H-001 invariant).

### Tests

- `TestClearWriteDeadlineForGET` (4 unit subtests)
- `TestMCPStreamWriteDeadlineCleared` (integration, WriteTimeout=2s + 4.5s hold)
- `TestMCPPostWriteTimeoutRetained` (H-001 regression guard)
- `TestMCPStreamWriteDeadline_LongRun` (`//go:build long` 720s real-server test)
- **Total: 10+ new tests across both suites**

---

## [Daemon 1.33.6] - 2026-05-13 — Unfreeze-Button Endpoints (Windows-only v1)

**Plan:** [docs/PLAN-unfreeze-button.md](../claude-team-control/docs/PLAN-unfreeze-button.md) (claude-team-control repo) — single-phase, operator-locked v3.

### Added — Gateway daemon

- **`POST /api/v1/claude-code/register-pid`** — accepts `{session_id, pid}` from `hooks/statusline.mjs`, stores in `patchstate.State.sessionPids` (in-memory, no disk persistence). Per-session rate limit 5/min. Rejects PID < 5 (Windows kernel reserves 0-4: System Idle, System, secure System).
- **`POST /api/v1/claude-code/unfreeze`** — accepts `{session_id}` from `patches/porfiry-taskbar.js` when the operator clicks the 🔄 button. Looks up the registered PID, runs `powershell.exe -NoProfile -NonInteractive -Command "Stop-Process -Id <pid> -Force"` with a 5 s timeout, drops the registration on both success and failure (stale PID after natural exit). Per-session rate limit 10/min. 404 when session is not registered.
- **`patchstate.SessionPid` + `RecordSessionPid` / `GetSessionPid` / `RemoveSessionPid`** — three concurrent-safe methods on `patchstate.State` for in-memory PID storage, modeled after the existing heartbeat APIs but without disk persistence (PIDs are transient).
- **`unfreezeExecFunc` injection point** — package-level function variable so tests override the real `Stop-Process` shell-out without spawning processes. Production default uses `exec.CommandContext` with PowerShell.

### Tests

- **8 new Go tests** in `internal/api/claude_code_handlers_test.go`: register happy path / PID=0 → 400 / PID=2 (kernel reserved) → 400 / unfreeze happy path with mocked exec / unfreeze unknown session → 404 / unfreeze exec failure → 500 with stale-registration drop / unfreeze rate limit at compressed-budget bucket / empty session_id → 400 on both endpoints.

### Security

- Webview cannot specify the target PID — daemon resolves session_id → pid via patchState lookup. Compromised webview can only kill its own claude.exe PID (the one registered for its session_id), not arbitrary system processes.
- PID < 5 rejected at registration time to fail fast against kernel-reserved PIDs that Stop-Process cannot kill anyway.
- Reuses existing `claudeCodeCORS` (vscode-webview:// origin echo) + Bearer auth chain; per-session rate limiter prevents budget exhaustion from one session affecting others.

## [Extension 1.33.5] - 2026-05-12 — Server Rename Feature

**Plan:** [docs/PLAN-server-rename.md](docs/PLAN-server-rename.md) — 4-phase plan (Go API → TS Extension Client → TS Extension UI → Documentation + manual E2E).

### Added — Gateway daemon

- **`PATCH /api/v1/servers/{name}` accepts `new_name`** — full rename support on the existing PATCH endpoint, transactional with env / header / disabled updates. `internal/models/types.go::ServerPatch.NewName *string` (pointer so empty string is distinguishable from "field absent"). Response on rename: `200 {"status":"patched","old_name":"{old}","new_name":"{new}"}`. No-op rename (`new_name == name`) preserves the existing `{"status":"updated"}` shape.
- **Plan A ordering** in `handlePatchServer`: `lm.AddServer({new})` → `lm.RemoveServer(r.Context(), {old})` with `context.Background()` rollback on failure → `cfgMu`-protected map swap → auto-start under new name (warn-only) → `RebuildTools` + `TriggerPluginRegen` (R-26 + spike 2026-05-08 routing-bypasses F1: `RebuildTools` is the single propagation channel for clients).
- **SAP refusal via `mcp-gateway/internal/sapname`** — the regex-free codegen package from `docs/grammar/sap-server-name.yaml` (R-21, sap-picker T-A.2) is imported by `internal/api/server.go`. `sapname.IsSAP(name) || sapname.IsSAP(*patch.NewName)` → 400 `"renaming SAP-named servers is not supported"`. No new file, no new regex (CLAUDE.md "Regex Discipline"). Existing env-only / disabled-only PATCHes against SAP-named servers continue to work (SAP non-goal is renaming, not all-mutation).
- **`lifecycle.Manager` test-only hooks**: `SetTestStopHook` + `SetTestRemoveHook` for error injection from the `api` package's rename tests (write-once-before-traffic invariant — production never calls these).
- **Bonus operator-approved fix**: `internal/proxy/gateway.go` adds `KeepAlive: 60 * time.Second` to both the aggregate `/mcp` server and per-backend `mcp.Server` instances. Mitigates Claude Code's 5-min idle MCP disconnect ("SSE stream disconnected: TimeoutError" → 3 strikes → "Closing transport"), empirically verified 2026-05-12 by curl probe (GET /mcp produced zero bytes over 5 min before this fix).

### Added — VSCode extension

- **`mcpGateway.renameServer` command** + `view/item/context` menu entry on the MCP Backends tree (`viewItem` regex whitelist of 7 lifecycle states `running|stopped|degraded|error|disabled|starting|restarting` — deliberately excludes SAP `contextValue`s).
- **`extension.ts` handler** — input box with `validateInput` rejecting empty / unchanged / format-invalid (`SERVER_NAME_RE`) / SAP-shaped names via the exported `parseSapServerName` helper (NOT regex literals — drift Go↔TS structurally impossible because both sides come from the same YAML grammar). Confirm modal showing **preserves summary** {env count, header count, secret count} computed via `credentialStore.listServerCredentials`. On confirm: gateway `patchServer` → on success, `credentialStore.renameServerCredentials` wrapped in try/catch → on throw, **warning toast**: *"Server renamed to '{new}' but {N} credential(s) could not be migrated. They remain under '{old}' in the keychain. Re-import KeePass or re-enter them manually."* `cache.refresh()` always fires on gateway success.
- **`credential-store.ts::renameServerCredentials(oldName, newName)`** — index-first ordering inside `_chainIndexMutation`: STEP 1 commit `newName` index entry FIRST → STEP 2 copy each secret from `mcpGateway/{old}/*` → `mcpGateway/{new}/*` → STEP 3 delete old secrets + remove `oldName` index entry. Crash-mid-rename leaves `{newName: entry-shape}` in the index — recoverable by `reconcile()`.
- **`credential-store.ts::listServerCredentials(server)`** — read-only `{env, headers}` shallow-copy helper (returns `{env:[], headers:[]}` for unknown server).
- **`gateway-client.ts::patchServer`** signature extended with `new_name?` + `add_env?` + `remove_env?` + `add_headers?` + `remove_headers?` (purely additive — existing callers compile + work unchanged).
- **`MockSecretStorage::failAfterNStores(n, error)` + `failAfterNGets(n, error)`** failure-injection knobs (default no-ops; existing call sites byte-identical). Required by Test 16b crash-mid-rename + reconcile recovery.

### Tests

- **25 new Go tests** in `internal/api/server_rename_test.go` covering happy path / collision (409) / invalid name (400) / not-found (404) / SAP refusal both directions (400) / SAP-beats-bad-env validation order / rollback / rollback-of-rollback ERROR log / start-fail warn-only / bad-env short-circuit / plugin-regen failure swallowed / stop-timed-out silent zombie regression guard (F-ARCH-4) / preserves env / combined rename+env atomic / disabled flag / no-op rename returns `{"status":"updated"}` / RebuildTools called and env-only PATCH does NOT call RebuildTools / case-strict invariants (`vsp-DEV` SAP, `random-server` proceed, `Vsp-DEV` proceed, `vsp-dev` proceed) / response shape / rollback ERROR-level log assertion / ValidateServerName on `*new_name`.
- **13 new TS tests** across `credential-store.test.ts`, `gateway-client.test.ts`, `commands.test.ts` covering migrate env+header / missing entry / patchServer with `new_name` / race + stranded-index (F-ARCH-2 option a) / crash-mid-rename + reconcile recoverable (uses `failAfterNStores(1)` knob) / `listServerCredentials` / UI happy path / SAP rejection / cancel input / cancel confirm / API failure / gateway success + creds failure / validateInput rejections.

### Security

- SAP-name detector source-of-truth lives in `docs/grammar/sap-server-name.yaml` (R-21). Both Go and TS sides are emitted from the same YAML — drift impossible. No new regex literals introduced.
- `ValidateServerName` guards `new_name` against injection: 1-64 chars, `[A-Za-z0-9_-]+`, no `__` separator (would collide with tool-namespace token).
- Index-first ordering in `renameServerCredentials` ensures secrets never live under an unindexed key — `reconcile()` can detect and prune partial-rename state on next extension activation.

### Known limitations

- **Orphan secrets after partial-migration failure** (LOW): if the gateway PATCH succeeds but the extension's credential migration throws mid-copy, secrets under `mcpGateway/{old}/*` remain in the keychain (the warning toast names them). Operator must re-import via KeePass or re-enter manually. Tracker: `v17-rename-orphan-audit` for a future `auditOrphanSecrets` command.
- **Stranded index entry after concurrent storeEnvVar**: if `storeEnvVar({old}, K3, v)` lands after `renameServerCredentials({old}, {new})` completes, the old-name index entry is resurrected with the new K3 secret. `reconcile()` cannot prune (K3 secret is genuinely present). Documented in `docs/REVIEW-server-rename.md` Phase 2 §P2-DOC-01.

### Operator action required

After installing this extension version, run **VSCode → Developer: Reload Window** so the new `mcpGateway.renameServer` command + context menu are activated.

---

## [Daemon 1.9.0 + Extension 1.32.0] - 2026-05-10 — Wave 2 (Import-from-Claude)

**Plan:** [docs/PLAN-sap-picker-and-import-mcp.md](docs/PLAN-sap-picker-and-import-mcp.md) Wave 2 (Phases D + E + F).

### Added — Daemon

- **Import-from-Claude REST endpoints** under FROZEN `/api/v1/claude-code/*`
  namespace (R-15 + ADR-0005 §Appendix A):
  - `GET /api/v1/claude-code/import-snapshot?source={cc_global|cc_project|desktop}&project_root=…` — returns rows + per-row gateway-state diff with `drift_fields` + provenance badge.
  - `POST /api/v1/claude-code/import-apply` — per-row copy/move with conflict policy (skip/overwrite), single end-of-batch `TriggerPluginRegen` (R-26 / X2 — closes the N×regen-storm under bulk import).
- **`internal/claudeconfig/`** — `cc_global` / `cc_project` / `desktop` readers with mtime-CAS retry (R-08), unrecognized-field preservation, and lockfile acquisition.
- **`internal/claudeconfig/rawroot.go`** — byte-level scanner that splices new `mcpServers` value into `~/.claude.json` while keeping every other top-level key (`oauthAccount`, `cachedGrowthBookFeatures`, `projects`, …) byte-identical (R-02). Zero regex; rejects duplicate `mcpServers`, non-object roots, and pathological string-escape inputs explicitly.
- **`internal/claudeimport/`** — apply / diff / commandresolve / provenance:
  - Refcounted per-file source-write mutex (`sourceLocks`) — entries deleted at zero waiters so the map is bounded by active concurrent paths, not total paths ever seen (F-02 audit fix).
  - `mutateSourceRemove` mtime-CAS catches concurrent external writers (TS-side reflector) and surfaces `Status=Applied, SourceUpdated=false, Reason="mtime"` (R-31 / X7 — see ANALYSIS §R-31 for the two-layer coordination contract).
  - `commandresolve.go` resolves `npx` / `uvx` / `node` to absolute paths via `os/exec.LookPath`; on Windows strips `.exe`/`.cmd`/`.bat` suffix for canonical name comparison.
  - `provenance.go` — atomic `CreateTemp` + `Rename` write of `~/.mcp-gateway/claude-imported.json`; in-process `sync.Mutex` serialises concurrent appenders; `OpResult.ProvenanceWarning` surfaces non-fatal write failures.
- **`internal/lifecycle/manager.go::RemoveServer`** signature change: now returns `(RemoveResult{Orphan bool, StopErr error}, error)`. `Orphan=true` surfaces when the OS Stop call fails — entry deletion remains unconditional (operator intent honoured even if OS process leaks). Closes R-28 / X4 (Stop-error swallow).

### Added — VSCode extension

- **Import-from-Claude webview** (`mcpGateway.openImportClaude` command):
  - Sources radio (`cc_global` / `cc_project` / `desktop`); refetches on switch.
  - Per-row checkbox + name + transport + command preview; provenance badge `◊ previously imported` + drift badge `⚠ drift: <fields>` + collision badge `◇ name in use`.
  - Action select (copy / move) × Conflict select (skip / overwrite). `move + overwrite` surfaces a red toolbar banner whenever any CHECKED row matches — visual cue is duplicated in the Preview / Apply modal (R-23).
  - Preview button: local-projection of final state per row — no destructive backend round-trip (spec evolution from TASKS T-E.3 — `dry_run` removed in favour of stateless host-side projection; closure record in PLAN-sap-picker-and-import-mcp.md Phase E section).
  - Apply button: 7-state row machine (`idle` / `pending` / `in_progress` / `applied` / `skipped` / `conflict` / `error`); retry-failed-rows captures pre-reset failed-key set so a fresh `idle+checked` row cannot slip through.
  - Host-side `coerceEdits` tamper guard: rejects payloads with `action='duplicate'` / `conflict='merge'` / unknown source / oversized rowKey before the daemon ever sees them.
- **`mcp-ctl install-claude-code`** — unchanged contract; the new endpoints are additive under the existing FROZEN namespace and require no installer flag.

### Documentation

- README — new "SAP Picker" + "Import-from-Claude" sections (Wave 1 + Wave 2 features) and a "Known limitations — webview file dialogs" subsection covering Q3.4 multi-monitor `showOpenDialog` quirk.
- `docs/ANALYSIS.md` — new section "Patterns introduced in Wave 1 + Wave 2" covering R-21 codegen, R-31 reflector hash-CAS coordination, R-03 provenance sidecar, R-02 raw-bytes-splice.
- `docs/ADR-0005-claude-code-integration.md` — Appendix A: additivity proof for the Import endpoints under the existing FROZEN `/api/v1/claude-code/*` namespace.
- `docs/SMOKE-2026-05-07.md` — 13-item manual smoke checklist for Windows + Linux.

### Breaking

None on the wire. `RemoveServer` Go signature change is structurally backward-compatible — new `Orphan` field; callers ignoring it preserve prior behaviour.

### Tag-history note

The daemon's git tag `v1.0.0` was a legacy stale tag from the initial public release; the next git tag jumps to **`v1.9.0`** to align with the ldflags-embedded version users see (`mcp-ctl version`). See [docs/PLAN-sap-picker-and-import-mcp.md](docs/PLAN-sap-picker-and-import-mcp.md) §C OpenQuestion 5 for the full rationale.

## [Daemon 1.8.0 + Extension 1.31.0] - 2026-05-09 — Wave 1 (SAP Picker + Settings)

**Plan:** [docs/PLAN-sap-picker-and-import-mcp.md](docs/PLAN-sap-picker-and-import-mcp.md) Wave 1 (Phases A + B + C).

### Added — Daemon

- **SAP Picker REST endpoints** under new `/api/v1/sap/*` namespace
  (additive under existing claudeCodeCORS + authMW middleware,
  ADR-0003 §csrf-scope precedent — comment in `internal/api/server.go`
  references it explicitly):
  - `GET /api/v1/sap/picker-snapshot` — joined landscape ∪ KeePass
    rows.
  - `POST /api/v1/sap/batch-begin` — opens a 5-minute batch window;
    returns `{batch_id}`.
  - `POST /api/v1/sap/batch-end` — closes the batch + fires single
    `TriggerPluginRegen` + `RebuildTools` (R-26 / X2 fix). 409 on
    nested batches.
- **`internal/saplandscape/parser.go`** — regex-free `encoding/xml`
  parser for `SAPUILandscape.xml` with `<Include>` cycle detection
  (visited map + max depth 8), URL normalisation
  (`%APPDATA%`/`%USERPROFILE%` expansion, `file:///C:/path` → backslash,
  `file://server/share/...` → UNC, `\\?\` long-path passthrough).
  Malformed XML / cycles / missing-include surface as
  `Landscape.Warnings`, never crashes the parser (R-05 / R-06).
- **`internal/sapcreds/keepass.go`** — production `ListEntries(kdbxPath, password, keyfile)` via
  `gokeepasslib/v3` (MIT-licensed, validated in T-A.0 PoC). Recycle-bin
  entries filtered. Locked-vault path returns typed
  `keepass.ErrNoCredentials`.
- **`internal/sapcreds/intersection.go`** — hybrid join: every landscape
  SID returned with `kpMissing: bool` flag; KP-only entries excluded
  (R-14 / R-30 backend).
- **`tools/grammar-gen/`** — codegen pipeline: single YAML SoT at
  `docs/grammar/sap-server-name.yaml` produces both Go
  (`internal/sapname/grammar_gen.go`) and TS
  (`vscode/mcp-gateway-dashboard/src/sap-name-grammar.gen.ts`) parsers.
  Both are regex-free; charcode comparisons only. Staleness check at
  `tools/grammar-gen/check`. CI job `grammar-staleness` in
  `.github/workflows/ci.yml` (NEW — repo's first GitHub Actions
  workflow file). 50 cross-language fixture cases at
  `testdata/sap-name-fixtures.json` shared by Go + TS test suites
  (R-21 / X1 fix).
- **`mcp-ctl credential list-structured`** — new cobra subcommand
  emitting `[{sid,client,user,kpMissing}]` JSON to stdout.
- **`internal/api/server.go`** — `addServerInProcess` /
  `removeServerInProcess` extracted from HTTP handlers; auto-suppress
  `TriggerPluginRegen` + `RebuildTools` when `s.sapBatchActive()` is
  true. Single end-of-batch regen verified by
  `TestSapBatch_SingleRegen` (5 servers added in one batch → exactly
  1 regen).

### Added — VSCode extension

- **SAP Picker webview** (`mcpGateway.openSapPicker` command): hybrid
  picker with virtualized rows (`content-visibility: auto`),
  3-toggle filter (registered/available/no-credentials) with
  degenerate-state guard, per-row VSP+GUI checkboxes (disabled +
  tooltip on `kpMissing` rows — R-30 UI), `[⋮]` expand whose state
  survives filter (R-18) + per-row override fields (vspCommand /
  guiCommand / guiUvProject), batch Apply (concurrency=4) with 9-state
  row lifecycle, retry-failed-rows preserves succeeded rows,
  force-kill button on `removed_with_orphan` rows surfaces
  `removeServerInProcess` Orphan via confirm-dialog (R-28 UI).
- **Settings webview** (`mcpGateway.openSettings`): sticky
  header + footer + scroll body fits 800 px viewport (R-10), Browse
  buttons with `defaultUri` fallback chain
  (`currentValue → parentDir → os.homedir()` — R-17), debounced (300 ms
  trailing) + LRU (TTL=10 s, max 64 entries) live validation (R-11),
  Save batches all changes atomically (any one error rejects entire
  batch, no partial writes), restart-required toast on
  `apiUrl`/`daemonPath`/`authTokenPath`/`claudeConfigSync.{enabled,namespacePrefix,path,aggregateEntryName}`
  with `[Restart Daemon]` action (R-29 / X5),
  `[Import paths from mcpDashboard]` button maps the four legacy
  `mcpDashboard.*` paths to `mcpGateway.*` equivalents (only fills
  empty targets — does not overwrite).
- **4 new `mcpGateway.*` settings** declared in `package.json`:
  `defaultVspCommand`, `defaultGuiUvProject`, `defaultGuiMode`
  (`exec` | `uv`), `uvPath`.
- **Regex-free server-name parsing** — `vscode/mcp-gateway-dashboard/src/sap-detector.ts`
  regex constants `VSP_RE` / `GUI_RE` DELETED; replaced by import
  from generated `sap-name-grammar.gen.ts`.

### Security

- SAP routes mount with `claudeCodeCORS + authMW` only — explicit code
  comment in `internal/api/server.go` references ADR-0003 §csrf-scope
  precedent. Picker is a VSCode-webview origin-restricted call; csrf
  stays off for the same reason as the existing `claude-code/*` group.
- `coerceEdits` tamper guard (Settings + SAP Picker + Import) — host
  validates every webview message envelope BEFORE invoking
  `vscode.workspace.getConfiguration().update()` or the daemon REST
  client. Tampered diffs (`disabled=false` on a `kpMissing` row,
  unknown action enum, oversized rowKey) are dropped at the host
  boundary.

### Breaking

None. All additions are backward-compatible — new REST endpoints under
new path, new settings have sensible defaults, generated parsers
mirror the regex behaviour they replaced.

## [1.9.1] - 2026-04-24

### Added — VSCode extension

- **Pin Claude Code Integration to view title bars** — the `mcpGateway.showClaudeCodeIntegration` command (`$(plug)` icon) is now in the `view/title` menu of all three sidebar views (Gateway daemon, Backends, SAP Systems) at `navigation@50`. Pure discoverability fix — the command itself was already there but only reachable from the command palette.

### Fixed — `mcp-ctl install-claude-code`

- **Marketplace JSON schema** updated for Claude Code CLI 2.1.x: `owner: {name, email?}` as a top-level field, `metadata.{version,description}` nested (not flat), and the file relocated to `installer/.claude-plugin/marketplace.json` so relative plugin `source` paths resolve against the marketplace root.
- **Plugin userConfig fields** in `installer/plugin/.claude-plugin/plugin.json` now carry `type` + `title` so the Claude Code installer renders the configuration prompt.
- **`mcp-ctl` resolves marketplace paths to absolute** (`resolveMarketplacePath()`) before passing to `claude plugin marketplace add`. Previously a relative arg was treated as a `github.com/<owner>/<repo>` shorthand and Claude attempted (and failed) to clone it over SSH.
- **409 ALREADY_INSTALLED** is now a non-fatal branch — the install flow no longer rolls the marketplace back when the plugin is already present.

## [1.9.0] - 2026-04-24

### Added — VSCode extension

- **`mcpGateway.sapSystemsEnabled`** setting (bool, default `false`, scope `window`). Hides the SAP Systems view by default — SAP integration is team-specific and most users of the published extension do not need it. The setting gates four runtime constructions in `activate()`: `SapTreeProvider`, `sapTreeView`, `SapStatusBar`, and the `SapDetailPanel.updateAll` cache-refresh listener. View visibility is driven by a `when: "mcpGateway.sapSystemsEnabled"` clause on both the view entry and its `viewsWelcome` entry, seeded via `executeCommand('setContext', ...)` before view registration so first paint is correct. SAP commands stay registered unconditionally so palette access remains an operator escape hatch.
- **Live-toggle handler** — `onDidChangeConfiguration` updates the context key immediately (view appears/disappears) and surfaces an informational toast with a one-click `Reload Window` action; full provider/status-bar lifecycle requires the reload to take effect.

### Documentation

- README — new `mcpGateway.sapSystemsEnabled` row in the Settings table.
- ROADMAP — new "UX toggles (post-v1.7.x)" section recording this entry.

### Build hygiene

- `.vscodeignore` — added `*.log` exclusion so stray build/test logs in the extension root never end up bundled in the VSIX.

### Breaking

- Users who had the SAP Systems view visible on v1.8.x will see it disappear on upgrade. Re-enable via Settings → `mcpGateway.sapSystemsEnabled: true` and reload the window. SAP commands remain available from the command palette regardless of the setting.

## [1.7.0] - 2026-04-24

### Added — Daemon lifecycle control

- **`POST /api/v1/shutdown`** — auth-gated graceful shutdown endpoint. Returns 202 + `{"status":"shutting_down"}`, flushes response via `http.Flusher` before triggering the root `context.CancelFunc`, idempotent under concurrent requests (returns `already_shutting_down` for re-entry). Wired to the same signal-handler path as `SIGTERM`, so the in-flight errgroup drain and the new 8-second bounded `context.WithTimeout` apply to both exit paths.
- **Extended `/api/v1/health`** — response now includes `started_at` (RFC3339 UTC), `pid`, `version`, and `uptime_seconds` alongside the existing `status`/`servers`/`running`/`auth` fields. All new fields `omitempty` — older clients decode unchanged.
- **`internal/pidfile` package** — atomic PID file acquisition (`O_CREAT|O_EXCL|O_WRONLY` + post-write `Lstat` non-symlink verification, `ErrAlreadyRunning` sentinel). Liveness probe is HTTP-based (`GET /api/v1/health` with 500ms timeout, TLS-aware with `InsecureSkipVerify` for self-signed loopback certs), so stale-reap works identically on Linux and Windows. `DefaultPath` prefers `$XDG_RUNTIME_DIR/mcp-gateway.pid` on Linux, falls back to `os.TempDir()` on other platforms.
- **`mcp-ctl daemon` CLI subcommands** — `start` (spawns detached via `DETACHED_PROCESS|CREATE_NEW_PROCESS_GROUP` on Windows, `Setpgid` on POSIX; polls `/health` for reachability), `stop` (REST `/shutdown` → PID-file-based OS kill fallback with SIGTERM → 2 s wait → SIGKILL escalation on POSIX), `restart` (composed stop + start with connection-error tolerance), `status` (tabwriter table: STATUS / PID / VERSION / STARTED / UPTIME / SERVERS / RUNNING). Uptime formatter handles `Ns` / `Nm Ss` / `Nh Mm Ss` / `Nd Hh Mm` ranges.

### Added — VSCode extension

- **`mcpGateway.restartDaemon` command** — REST-based (works for daemons started externally via `mcp-ctl daemon start`, not just extension-owned children). `DaemonManager.restart()` flow: `shutdown()` → poll `/health` unreachable → cleanup own child handle if any → spawn fresh. Serialised by a new `restarting` mutex with `start()`/`stop()` to prevent auto-start + user-restart races.
- **Gateway tree view** — new `mcpGatewayDaemon` view at the top of the MCP Gateway activity container. Root "Gateway" row with status icon + uptime description, expandable into `PID` / `Version` / `Started` / `Uptime` detail rows. Inline action buttons: start (when offline), stop + restart (when running). Fingerprint collapses uptime into 5-second buckets so the tree doesn't re-render every poll tick.
- **Status bar tooltip** now leads with `**Gateway**: 2h 3m · v1.7.3 · pid 12345` line when `/health` metadata is available. Missing fields are skipped rather than printed as `unknown`.
- **`ServerDataCache.gatewayHealth`** — cache fetches `/servers` and `/health` in parallel via `Promise.allSettled` on the same refresh cycle. `/health` failures don't mark the cache as offline (only `/servers` does); consumers get `gatewayHealth: null` and render "offline".

### Security

- `POST /api/v1/shutdown` is mounted inside the Bearer-auth-required router group alongside all other mutating endpoints. Rejected with 401 without a valid token.
- PID file mode `0600` with post-write `Lstat` check — world-writable `/tmp` symlink attacks rejected.
- `--no-auth` mode caveat documented: with auth disabled, any local process can POST `/shutdown`. Acceptable per existing `MCP_GATEWAY_I_UNDERSTAND_NO_AUTH=1` operator attestation (ADR-0003 §no-auth-escape-hatch).

### Documentation

- `README.md` — new "Managing the daemon" section covering CLI, extension UI, status bar tooltip, and graceful shutdown semantics.

### Breaking

None. All additions are backward-compatible — `HealthResponse` fields use JSON `omitempty` and TypeScript `?`; `DaemonManager.start()`/`stop()` signatures unchanged.

## [1.6.0] - 2026-04-22

### Added

- **Dual-mode gateway** — `/mcp` aggregate + `/mcp/{backend}` per-backend MCP surfaces from a single daemon. Unblocks Claude Code plugin packaging where each backend registers as its own `.mcp.json` entry without breaking clients that depend on the aggregate endpoint.
- **Claude Code Plugin packaging** — `installer/plugin/` ships an installable plugin with `.claude-plugin/plugin.json` (userConfig: `gateway_url` + `auth_token`) and `installer/marketplace.json` for one-command install. The plugin's `.mcp.json` is regenerated from the gateway's live backend list on every REST mutation (atomic tmp+rename, 0600 POSIX / DACL Windows).
- **`mcp-ctl install-claude-code`** — headless bootstrap CLI. Flags: `--mode|--scope|--no-patch|--dry-run|--refresh-token|--check-only`. LIFO rollback on partial failure. Exit codes 0/1/2/3/4 distinguishing usage / gateway-down / token-drift / rollback-executed.
- **Webview patch with native MCP reconnect (Alt-E pattern)** — opt-in. Walks Claude Code's React fiber tree to capture a reference to `session.reconnectMcpServer` (the same native method the `/mcp` panel's Reconnect button calls) and invokes it when the gateway enqueues a reconnect action. Closes the "tools/list caching" bug class (#13646) without patching `extension.js`.
- **`gateway.invoke` universal fallback tool** + `gateway.list_servers` / `gateway.list_tools` meta-tools on the aggregate endpoint. Callable even when the specific tool isn't in the client's current `tools/list` cache.
- **Supported-versions map** — `configs/supported_claude_code_versions.json` tracks `alt_e_verified_versions`. Served via `GET /api/v1/claude-code/compat-matrix`. Dashboard surfaces Mode C (yellow advisory) when the running CC version is unverified.
- **`/api/v1/claude-code/*` REST endpoints** — `patch-heartbeat`, `patch-status`, `pending-actions`, `pending-actions/{id}/ack`, `probe-trigger`, `probe-result`, `plugin-sync`, `compat-matrix`. FROZEN v1.6.0 contract in `docs/api/claude-code-endpoints.md`.
- **VSCode dashboard "Claude Code Integration" panel** — new command `mcpGateway.showClaudeCodeIntegration`. Displays plugin + patch + channel status with a 12-mode failure matrix (A-M, E obsoleted under Alt-E). Buttons: `[Activate for Claude Code]`, `[Probe reconnect]`, `[Copy diagnostics]`. Diagnostics report includes Alt-E metrics (p50/p95 reconnect latency, fiber depth history, dedup recent errors).
- **Slash-command disclaimer** — every auto-generated `.claude/commands/*.md` carries two disclaimer lines below the AUTO-GENERATED marker stating "this is a slash-command prompt template, NOT an MCP server registration" + pointer to the mcp-gateway plugin install path. Closes operator-confusion bug class (#16143). Regression-pinned by test.

### Security

- **CORS policy for `vscode-webview://`** narrowly scoped to `/api/v1/claude-code/*`; rest of `/api/v1` retains existing csrf-protected origin policy. OPTIONS preflight runs BEFORE bearer auth so browsers can preflight without `Authorization` (REVIEW-16 L-02). Unknown origins get 204 WITHOUT `Access-Control-Allow-Origin` — deny by omission.
- **Rate limits** — separate token-bucket limiters on `/patch-heartbeat` (5/min per session_id), `/pending-actions` (60/min per IP), `/patch-status` (60/min per IP). Amortized idle-bucket eviction.
- **Patch state durability (REVIEW-16 M-01)** — pending reconnect actions + recent heartbeats persist to `~/.mcp-gateway/patch-state.json` (0600, atomic tmp+rename) on every mutation. TTL-filtered on daemon startup. Graceful-shutdown path flushes in-flight persists before `lm.StopAll`.
- **Inlined auth token in patched index.js locked to 0600 on POSIX / DACL on Windows** (REVIEW-16 L-03). `mcp-ctl install-claude-code --refresh-token` re-registers plugin + re-applies patch after gateway token rotation (REVIEW-16 M-03).

### Documentation

- `docs/ADR-0005-claude-code-integration.md` — architectural decision record for the hybrid dual-mode + plugin + Alt-E webview-patch approach.
- `docs/api/claude-code-endpoints.md` — FROZEN v1.6.0 REST contract.
- `docs/TESTING-PHASE-16.md` — four-tier test documentation.
- README §"Connecting Claude Code to the Gateway" + §"Commands vs MCP servers".

### Breaking

None. All additions are backward-compatible.

### Known limitations

- **Webview patch is opt-in** and modifies Claude Code's own `webview/index.js`. Operators who decline still get full functionality via manual `/mcp` panel Reconnect.
- **CC version drift** mitigated via `configs/supported_claude_code_versions.json` + dashboard Mode C advisory — unverified versions are warnings, not errors.

## [1.5.0] - 2026-04-20

### Added
- **Server & command catalogs** — first-party JSON catalogs of popular MCP servers (context7, pdap-docs, orchestrator, pal-mcp, sap-gui-control) and matching slash-command templates. Versioned draft-07 JSON Schemas pinned by `$id` (`v1`). Catalogs ship bundled with the extension VSIX; never fetched from the network.
- **Add Server "Choose from catalog" dropdown** — `AddServerPanel` webview now exposes a catalog dropdown above the Name field. Selecting an entry pre-fills transport / url / command / args and renders one empty row per declared `env_keys` / `header_keys` so the operator fills only secret values. `(Custom server)` preserves the pre-catalog free-form flow.
- **Slash-command template enrichment** — `SlashCommandGenerator` injects the catalog's `template_md` body into `.claude/commands/<server>.md` on server transition to `running`. Allow-list substitution of `${server_name}` / `${server_url}`; unknown `${var}` tokens are left literal. Servers without a catalog entry keep the pre-v1.5 bare skeleton unchanged.
- **`mcpGateway.catalogPath` setting** (`type: string`, `default: ""`, `scope: machine`) — optional override path to a directory containing `servers.json` + `commands.json`. Operator path wins when non-empty and the directory exists; otherwise falls back to the bundled catalog under the extension's installation directory.
- **`npm run lint:catalog`** — ajv-cli validation of both seed files against their schemas plus a cross-reference check that every `command.server_name` resolves to a `server.name`. Added as a CI step alongside a VSIX-contents assertion ensuring the four catalog files plus ajv runtime dependencies are packaged.

### Security
- **Host-side re-validation of catalog selection** — `AddServerPanel.handleSubmit` re-loads the catalog and re-runs every field through `validation.ts` helpers before calling `client.addServer()`; forged `catalogId` payloads are rejected before they reach the daemon.
- **No catalog HTML interpolation** — every catalog string reaches the webview via `jsonForScript` and is rendered via `textContent` / `.value` (never `innerHTML`). `escapeHtml` neutralises `<script>`-laden catalog entries; verified by targeted test.
- **1 MiB catalog cap with TOCTOU-safe bounded read** — loader uses `fs.promises.open` + `fileHandle.stat` + bounded `fileHandle.read` on a single file handle, eliminating the swap window between stat and read. Oversized files produce a warning and an empty entry list; `readFile` is never invoked.
- **`scope: machine`** on `mcpGateway.catalogPath` prevents per-workspace catalog override (exfiltration-vector mitigation).
- **`$id` network refusal by design** — ajv is configured with bundled schema files via `addSchema`; catalog `$id`s are documentation-only and never trigger HTTP fetch.

### Breaking-config

- **Half-configured TLS now refuses to start** (T15B.3). Previously, setting
  exactly one of `gateway.tls_cert_path` / `gateway.tls_key_path` silently
  dropped back to plain HTTP — an operator who edited the config and forgot
  the second setting would see no error, assume TLS, and actually run
  cleartext. The daemon now refuses to start with an error message naming
  **both** paths. The wording is deliberately stable (grep target; future
  refactors must keep the string intact):

  > `TLS is half-configured: gateway.tls_cert_path is set but gateway.tls_key_path is empty — both must be set to enable TLS, or both must be empty for plain HTTP`

  Symmetric variant when only `tls_key_path` is set:

  > `TLS is half-configured: gateway.tls_key_path is set but gateway.tls_cert_path is empty — both must be set to enable TLS, or both must be empty for plain HTTP`

  Both variants are stable grep targets — future refactors must keep the
  strings intact. **No grace period** —
  silent plain-HTTP when the operator intended TLS is a security defect, not
  a feature. Installations running with half-finished TLS config from v1.4.0
  must either complete the pair or remove both settings before upgrading.

### Fixes

- **Scanner line-length cap raised from 64KB to 1MB** on both log paths
  (T15A.2a + T15A.2b — atomic pair, F-11 closed). `bufio.Scanner` defaults to
  a 64KB line limit, which silently truncated long lines both in
  `internal/ctlclient/client.go` (SSE client-side, `streamLogsOnce`) and in
  `internal/lifecycle/manager.go` (producer-side, `scanStderr`). The effective
  end-to-end cap is the minimum of the two sites, so fixing only one would
  still leave the user-visible ceiling at 64KB. Both sites now call
  `scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)` with a comment explaining
  the 64KB→1MB trade-off. Closes ROADMAP F-11.

### Hygiene

- **Bearer auth constant-time compare — pad-to-expected-length refactor**
  (T15A.1). `internal/auth/middleware.go` previously called
  `subtle.ConstantTimeCompare([]byte(received), expectedBytes)`, which the Go
  stdlib documents as returning 0 immediately on length mismatch. For the
  fixed 43-char token the practical leakage is 1 bit out of 256 — this is
  **not a security fix**. Landed anyway to remove the recurring PAL-review
  pattern and provide a clean reference for anyone copying the code to a
  variable-length secret: compare a pad-to-expected-length buffer, then do a
  separate `ConstantTimeEq` length check, combine both results
  unconditionally. Existing `TestMiddleware_ConstantTimeOnDifferentLengths`
  pins the coverage shape.

### Tests

- **TLS integration tier** (T15B.1 / T15B.2 / T15B.3). New
  `internal/api/tls_integration_test.go`: generates a CA → leaf cert chain in
  `t.TempDir()`, drives `ListenAndServeTLS`, probes with a custom `RootCAs`
  client pool — asserts 200 on `/api/v1/health` and 401 on an authed route
  without Bearer. Pins the previously-unexercised `ServeTLS` branch. Negative
  tests cover non-loopback + `authEnabled` + no TLS → startup refusal with
  pinned wording, and half-configured TLS refusal in both orderings
  (cert-only, key-only). Runs under the default `go test ./...` path — no
  external prereqs.
- **Windows DACL enforcement tier** (T15C.1). New
  `internal/auth/token_perms_integration_windows_test.go` under the
  `integration` build tag. Uses `LogonUserW` + `ImpersonateLoggedOnUser` via
  `advapi32.dll` to attempt `os.Open` on the token file as a second local
  account; expects `ACCESS_DENIED`. Confirms the token-file DACL is
  **OS-enforced**, not just structurally correct. Gated behind
  `make test-integration-windows` so the default `go test ./...` path is
  unaffected. `runtime.LockOSThread` pin + deferred `RevertToSelf` prevent
  impersonation from bleeding into other goroutines. Skips gracefully when
  `MCPGW_TEST_USER` / `MCPGW_TEST_PASSWORD` env vars are absent.
- **Manual-protocol branch for Windows enforcement** (T15C.2). The
  `windows-latest` GitHub-hosted runner spike
  (`docs/spikes/2026-04-19-windows-latest-impersonate.md`) was deferred — the
  branch cross-compiles clean but the repo's pre-push hook blocks leaking the
  spike branch to the remote. Scoped back to documented manual protocol:
  new `Makefile` target `test-integration-windows` (fail-fast env-var guard)
  plus a three-tier Testing section in the README with the elevated-PowerShell
  operator protocol. No `.github/workflows/ci.yml` change in v1.5.0.

### Documentation

- **README Testing tiers section** (T15D.2). Three-tier table separates what
  each test command proves and what it needs to run: default `go test ./...`
  covers unit + structural + TLS integration; `make test-integration-windows`
  covers the Windows DACL enforcement tier on a pre-provisioned local test
  account. Includes the elevated-PowerShell sequence (`net user /add` → env
  vars → make → `net user /delete`) and the behavior of the integration test
  when credentials are absent (`go test ./...` unaffected;
  `go test -tags integration ./...` skips with a pointer back to the README;
  `make test-integration-windows` fails fast).
- **README Catalogs section** (CD.1). New end-user-facing section documenting
  catalog layout (`servers.json` + `commands.json`), the `$id` version-pinning
  convention, the `mcpGateway.catalogPath` machine-scope override, hard limits
  (1 MiB cap, `v1.*` schema pin, fail-soft on malformed files), and the
  known-limitation note on slash-command edits below line 1 (regeneration
  overwrites edits unless the line-1 marker is removed). Paired with the
  feature entries in `### Added` / `### Security` above.

### ROADMAP

- **F-11 (bufio.Scanner 64KB stderr limit) — CLOSED** in Phase 15.A. Both
  scanner sites (SSE client + stderr producer) raised to 1MB atomically;
  regression tests pin the cap. End-to-end log-line ceiling is now 1MB.

## [1.0.0] - 2026-04-09

### Added
- **Go daemon** (`mcp-gateway`): MCP server lifecycle management for stdio and HTTP/SSE backends
- **CLI** (`mcp-ctl`): full server management, tool calls, log streaming, stdio compliance validation
- **VS Code extension** (`mcp-gateway-dashboard`): tree view, status bar, daemon lifecycle, webview detail panels
- **REST API** (v1): CRUD for servers, tool listing and calls, metrics, SSE log streaming
- Health monitoring with circuit breakers and configurable auto-restart
- Per-server tool budget with `ConsolidateExcess` meta-tool for budget overflow
- `compress_schemas` option: truncate tool descriptions, strip schema examples for token savings
- Environment variable expansion (`${VAR}`) in config with security-restricted fallback allowlist
- KeePass KDBX credential import via CLI (`mcp-ctl credential import-kdbx`)
- Windows Job Objects for automatic child process cleanup on daemon exit
- Installer scripts for Linux, macOS, and Windows with system service registration
- Binary signing with Sigstore cosign and SHA-256 checksum verification
- `GET /api/v1/metrics`: per-server crash counts, MTBF, uptime, token cost estimates
- `mcp-ctl validate`: black-box stdio compliance harness for MCP server onboarding
- API versioning with backward-compatible redirect (`/api/*` -> `/api/v1/*`)
- SAP system auto-detection and grouping by SID (opt-in via settings)

### Security
- CSRF protection via `Sec-Fetch-Site` header validation on mutating requests
- SSE connection limit (max 20 concurrent) to prevent resource exhaustion
- Non-loopback binding blocked without explicit `allow_remote` configuration
- Rate limiting (100 concurrent / 200 backlog) and 1 MB body size limit
- Dangerous environment key blocklist (25+ hijack vectors: `LD_PRELOAD`, `DYLD_INSERT_LIBRARIES`, etc.)
- Header injection prevention (CRLF/NUL validation)
- Atomic config writes (temp file + rename)
