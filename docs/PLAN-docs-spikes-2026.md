# PLAN — Server Rename Feature — Implementation Plan

**Session label:** `docs-spikes-2026`
**Companion files:** [docs/TASKS-docs-spikes-2026.md](TASKS-docs-spikes-2026.md), [docs/REVIEW-docs-spikes-2026.md](REVIEW-docs-spikes-2026.md)
**Source spike:** [docs/spikes/2026-05-05-server-rename.md](spikes/2026-05-05-server-rename.md) (v4 — fully validated; revision history confirms all 13 findings F-1..F-13 from prior audit `checkpoint-check-d1c32725` were addressed)
**Created:** 2026-05-06 by Porfiry [Opus 4.7]
**Status:** Drafted by dev-lead in pipeline `planning-19b7b15b`; awaits operator approval before `/run docs-spikes-2026`

---

## 1. Goal

Add the ability to **rename a registered backend MCP server** (e.g. `ctx7` → `context7-prod`) with full propagation across three atomic regions: gateway in-memory `s.cfg.Servers` map, gateway lifecycle manager (`lm.entries`), and extension-side credential index + secrets. Rename is bundled with the existing `PATCH /api/v1/servers/{name}` endpoint via a new optional `new_name` field — rename + env/header/disabled changes commit transactionally; partial-completion windows are bounded and logged.

The feature blocks at API and UI on **SAP-named** servers (`vsp-XXX`, `sap-gui-XXX`) because their names encode SID/client and renaming would break the SAP detector.

## 2. Plan-level facts

| Item | Value |
|------|-------|
| Total LOC | ~990 (Go ~540, TS ~450) |
| Total tests | **34** (21 Go + 13 TS) plus a 9-item manual E2E checklist — spike's 31 base + Test 7f (F-ARCH-4) + Test 19b (T3.9) |
| Expected duration | ~16 hours of focused work, gated across 4 phases |
| Phases | **4** (Go API → TS client → TS UI → Documentation/E2E/VSIX) |
| Risk level | MEDIUM — touches lifecycle manager, secret store, and Claude Code config sync; mitigated by Plan A rollback + index-first credential migration |

## 3. Architect-finding traceability

The architect (Step 1 of pipeline `planning-19b7b15b`) raised 9 findings (2 HIGH, 2 MEDIUM, 5 LOW), all with status `Open` and `escalation_to: dev-lead`. Each is addressed in a specific task or phase note below.

| Finding | Severity | Addressed by | Mechanism |
|---------|----------|--------------|-----------|
| F-ARCH-1 | HIGH | T1.0 (Phase 1 prerequisite) + Phase 1 description note | Spike line 27 atomicity claim (`Between region (a) and (b)... Bounded to ~1ms in practice`) corrected to `Bounded to lm.Stop duration (up to ~9s for stdio, ~2s for HTTP/SSE)` BEFORE any code work |
| F-ARCH-2 | HIGH | T2.4 (Phase 2 expanded test) — option (a) chosen | Expanded Test 17 in Phase 2 to assert orphan-detection logic for the storeEnvVar/rename race; reasoning recorded in T2.4 description (cheaper than auditing all `_chainIndexMutation`-external secret writes site-by-site, and the orphan-detection contract is already exercised by reconcile()) |
| F-ARCH-3 | MEDIUM | T4.1 (Phase 4 manual E2E) | Phase 4 manual E2E checklist expanded from 6 to **9 items** explicitly covering Plan A rollback UX, credential migration failure UX, and `~/.claude.json` propagation |
| F-ARCH-4 | MEDIUM | T1.17 (Phase 1 new test = Test 7f) | Test 7f added to Phase 1 with assertion shape: `lm.RemoveServer=nil but Stop timed out → silent zombie child` |
| F-ARCH-5 | LOW | Plan structure decision | **4-phase** kept (rationale in §4) — fine-grained PAL gates per concern outweigh the ~200 LOC collapse benefit |
| F-ARCH-6 | LOW | T1.0 (Phase 1 prerequisite) | Verification step "lm-error-injection helpers + SecretStorage mock helpers exist" added; if missing, +2-3h documented in plan-level estimate |
| F-ARCH-7 | LOW | T1.5 (Phase 1 task description) | Go regex updated to `(?:-\d{3})?` non-capturing group for parity with TS |
| F-ARCH-8 | LOW | T1.21 (Phase 1 Test 11 task description) | Test 11 (no-op rename) response body assertion: `{'status':'updated'}` |
| F-ARCH-9 | LOW | Phase 1 description notes | `context.Background()` rollback choice + rate-limit follow-up notes included verbatim in Phase 1 description below |

## 4. Phase-structure decision (F-ARCH-5)

**Decision: 4 phases (not 3).**

**Rationale:** F-ARCH-5 noted Phase 2 + Phase 3 are both pure TS (~200 LOC total) and could collapse. We keep them separate because:

1. Phase 2 (TS client + credential-store) is **logic-only / unit-tested** — PAL `codereview` + `thinkdeep` gates focus on concurrency, ordering, atomicity.
2. Phase 3 (TS UI: package.json + extension.ts handler + UI tests + VSIX) is **integration-heavy** — VSIX rebuild, command/menu wiring, modal-dialog UX. Different review surface.
3. Collapsing them would create a single PAL gate that has to triage credential-store atomicity bugs against UI-handler bugs in one pass — slower fix-loops, larger blast radius per cycle.
4. The 4-phase split matches the proven pattern from PLAN-mcp-lifecycle.md (per-concern PAL gating).

## 5. Phase Breakdown

```
Phase 1 — Go API                  (≈ 540 LOC, 21 tests, ~7.5 h)   ← rename branch + isSAPName + Plan A rollback
Phase 2 — TS Extension Client     (≈ 75 LOC,  6 tests,  ~1.3 h)   ← gateway-client.patchServer + credential-store
Phase 3 — TS Extension UI         (≈ 125 LOC, 7 tests,  ~2.2 h)   ← package.json + extension.ts handler + UI tests
Phase 4 — Documentation + E2E     (≈ — LOC,   9 manual, ~2.0 h)   ← README + manual E2E + VSIX deploy + commit
PAL gate cycles + buffer          (—,  —, ~3.0 h)                 ← codereview/thinkdeep/rollback fix-loops across 4 phases
                                       ─────────────────
                                       ≈ 16 h
```

### Phase 1 — Go API

**Goal:** Implement the gateway-side rename branch in `handlePatchServer` with Plan A ordering (lm.AddServer first → lm.RemoveServer second → cfg-mutation third), the `isSAPName` helper, model change, and 19 Go tests including 6 new failure-path cases.

**F-ARCH-1 / F-ARCH-9 notes (carry into commit message):**

- **Spike line 22 must be corrected before code work begins.** Original wording "Bounded to ~1ms in practice" contradicts actual `lm.Stop` budget for graceful child-process shutdown (up to ~9 s for stdio per `lifecycle/manager.go:568`, ~2 s for HTTP/SSE). T1.0 corrects to `Bounded to lm.Stop duration (up to ~9s for stdio, ~2s for HTTP/SSE)`.
- **Rollback context choice (F-ARCH-9 verbatim):** When `lm.RemoveServer(name)` fails after the new name has already been registered in lm, the rollback `lm.RemoveServer(newName)` is invoked with `context.Background()` (NOT the request context). The request context may already be cancelled (the very reason `RemoveServer(name)` failed), and using it would skip rollback entirely. `context.Background()` ensures the just-added new name is removed before the handler returns.
- **Rate-limit follow-up (F-ARCH-9 verbatim):** No request-rate limiter is added in this PR. The PATCH endpoint inherits the existing chi-router middleware stack (auth → CSRF → throttle 20). If rename traffic ever spikes (which is unlikely — operators rename servers manually, not in loops), tracker `v16-rename-rate-limit` covers a follow-up to add a per-IP token bucket dedicated to rename calls.

**Tasks:**

- [ ] **T1.0 (F-ARCH-1, F-ARCH-6, F-SPEC-1 — prerequisites; THREE edit targets in the spike):** Update [docs/spikes/2026-05-05-server-rename.md](spikes/2026-05-05-server-rename.md) on the following targets:
    1. **Line 27** — the inconsistency-window bullet starting `Between region (a) and (b)` — to read `Bounded to lm.Stop duration (up to ~9s for stdio, ~2s for HTTP/SSE)` instead of the current `Bounded to ~1ms in practice`. Locate by content (`Between region (a) and (b)`) rather than by raw line number to be resilient against unrelated edits. **Note: line 22 is the harmless `(b)`-row of the atomic-regions table — do NOT edit it.**
    2. **Lines 263-264 (F-SPEC-1)** — the `sap.go` code block must use **non-capturing** groups for parity with PLAN T1.5 and the F-ARCH-7 resolution. Change:
       ```go
       sapVSPRe = regexp.MustCompile(`^vsp-[A-Z0-9]{3}(-\d{3})?$`)
       sapGUIRe = regexp.MustCompile(`^sap-gui-[A-Z0-9]{3}(-\d{3})?$`)
       ```
       to:
       ```go
       sapVSPRe = regexp.MustCompile(`^vsp-[A-Z0-9]{3}(?:-\d{3})?$`)
       sapGUIRe = regexp.MustCompile(`^sap-gui-[A-Z0-9]{3}(?:-\d{3})?$`)
       ```
    3. ALSO: verify `internal/api/server_test.go` already exposes `lm-error-injection helpers` (used by Tests 7/7b/7c) and that `vscode/mcp-gateway-dashboard/src/test/helpers/` has SecretStorage mock helpers (used by Test 16b). If either set is missing, log +2–3 h to the plan-level estimate.
- [ ] **T1.1:** Add `NewName *string` field to `models.ServerPatch` in [internal/models/types.go](../internal/models/types.go) — pointer so empty string is distinguishable from "field absent". JSON tag `json:"new_name,omitempty"`.
- [ ] **T1.2:** Add `ValidateServerName` invocation path: confirm existing `models.ValidateServerName` works on `*patch.NewName` and produces a 400 with the existing error wording. No new validator function needed.
- [ ] **T1.3:** Implement `handlePatchServer` rename branch in [internal/api/server.go:824](../internal/api/server.go#L824). Follow the validation order pinned in spike §"Validation order (pinned)" — JSON decode → SAP refusal (only when `patch.NewName != nil`) → ValidateServerName → existing env/header validation → cfgMu.Lock → 404 lookup → scCopy build/merge → scCopy.Validate → branch on rename. Rename branch implements Plan A:
  - **Step 1** `s.lm.AddServer(newName, &scCopy)` — 409 on collision.
  - **Step 2** `s.lm.RemoveServer(r.Context(), name)` — on error, rollback via `s.lm.RemoveServer(context.Background(), newName)` (F-ARCH-9 carry); if rollback also fails, log at ERROR level with both error fields and return 500 with `"rename failed at remove stage (rolled back): ..."` body.
  - **Step 3** `s.cfgMu.Lock()` → `s.cfg.Servers[newName] = &scCopy; delete(s.cfg.Servers, name)` → `s.marshalConfig()` → `s.cfgMu.Unlock()` → `s.flushConfig(data)`.
  - **Step 4** `if !scCopy.Disabled { s.lm.Start(r.Context(), newName) }` — **warn-only**, do NOT roll back (parity with `handleAddServer:787-789`).
  - **Step 5** `if s.gw != nil { s.gw.RebuildTools() }`; `s.TriggerPluginRegen()`.
  - **Response:** 200 with `{"status":"patched","old_name":name,"new_name":newName}`.
- [ ] **T1.4:** Add SAP refusal pre-check at top of handler — fires only when `patch.NewName != nil`; if `isSAPName(name) || isSAPName(*patch.NewName)` → 400 with body message exactly `"renaming SAP-named servers is not supported"`. Confirms the codebase 400-for-validation convention. **Existing env-only / disabled-only PATCHes against SAP-named servers must still work**; SAP non-goal is renaming, not all-mutation.
- [ ] **T1.5 (F-ARCH-7):** Create new file `internal/api/sap.go` with two regexes using **non-capturing** groups for parity with the TS detector at [vscode/mcp-gateway-dashboard/src/sap-detector.ts:37-38](../vscode/mcp-gateway-dashboard/src/sap-detector.ts#L37):

  ```go
  var (
      sapVSPRe = regexp.MustCompile(`^vsp-[A-Z0-9]{3}(?:-\d{3})?$`)
      sapGUIRe = regexp.MustCompile(`^sap-gui-[A-Z0-9]{3}(?:-\d{3})?$`)
  )

  func isSAPName(name string) bool {
      return sapVSPRe.MatchString(name) || sapGUIRe.MatchString(name)
  }
  ```

  No capturing groups (we only test membership, not extract SID/client) — parity with TS regex without leaking match-group identifiers.
- [ ] **T1.6:** Test 1 — `TestPatchServer_Rename_Success`: name swap in cfg + lm; env/headers preserved; auto-start under new name; old name absent from `cfg.Servers`.
- [ ] **T1.7:** Test 2 — `TestPatchServer_Rename_NameCollision`: pre-populate cfg with both `ctx7` and `ctx8`; PATCH ctx7 with `new_name=ctx8` → 409; cfg + lm unchanged.
- [ ] **T1.8:** Test 3 — `TestPatchServer_Rename_InvalidName`: `new_name=""` and `new_name="bad name with spaces"` both → 400 with `ValidateServerName` error wording.
- [ ] **T1.9:** Test 4 — `TestPatchServer_Rename_NotFound`: PATCH non-existent server → 404; cfg + lm unchanged.
- [ ] **T1.10:** Test 5 + Test 6 — `TestPatchServer_Rename_SAPRefused_Old` (`name=vsp-DEV`) and `TestPatchServer_Rename_SAPRefused_New` (`new_name=vsp-XYZ`) both → 400 with exact `"renaming SAP-named servers is not supported"` body.
- [ ] **T1.11:** Test 6b — `TestPatchServer_Rename_SAPBeatsBadEnv`: PATCH `{new_name:"vsp-XYZ", add_env:["bad=val"]}` → 400 SAP refusal (NOT 400 bad env). Proves validation order step 2 short-circuits step 4.
- [ ] **T1.12:** Test 7 — `TestPatchServer_Rename_RollbackOnRemoveFailure`: inject `lm.RemoveServer(name)` error → assert rollback `lm.RemoveServer(newName)` fires; final lm state has only `name`; cfg untouched.
- [ ] **T1.13:** Test 7b — `TestPatchServer_Rename_RollbackOfRollbackErrorLogged`: both `lm.RemoveServer(name)` AND its rollback `lm.RemoveServer(newName)` fail → ERROR-level log entry with both error fields; HTTP 500 body shape verified.
- [ ] **T1.14:** Test 7c — `TestPatchServer_Rename_StartFailWarnsNotRollback`: `lm.Start(newName)` returns error → 200 OK still returned; warn-level log entry; cfg + lm both reflect new name (no rollback — parity with `handleAddServer:787-789`).
- [ ] **T1.15:** Test 7d — `TestPatchServer_Rename_BadEnvShortCircuits`: PATCH `{new_name:"ok", add_env:["INVALID="]}` → 400 BEFORE any state mutation; cfg + lm unchanged.
- [ ] **T1.16:** Test 7e — `TestPatchServer_Rename_PluginRegenFailureSwallowed`: construct server with a faulty `pluginRegen` callback that records failure to a captured logger buffer; rename returns 200 OK; rename completes; assert failure log entry exists in captured buffer (TriggerPluginRegen returns no error — verify via captured logger output, not error-return).
- [ ] **T1.17 (F-ARCH-4):** Test 7f — `TestPatchServer_Rename_StopTimedOutSilentZombie`: assertion shape — inject scenario where `lm.RemoveServer(name)` returns `nil` BUT the underlying `Stop(ctx, name)` timed out (from `lifecycle/manager.go:568` — error swallowed via `_ = m.Stop(...)`). Test asserts handler treats this as success (200 OK), final cfg + lm both reflect new name, AND a separate observability assertion: log entry at WARN level documents that `Stop` swallowing happened (if/when such logging is added). **Note**: this test currently locks down inherited pre-existing behavior (silent zombie child is a known LOW-severity risk per spike §13.2 row 2). Test 7f is a regression guard so future fixes to upstream `lm.RemoveServer` propagate cleanly into rename.
- [ ] **T1.18:** Test 8 — `TestPatchServer_Rename_PreservesEnv`: env values intact after rename (no PATCH-level env op); read SecretStorage equivalent (Go side has no SecretStorage; assert `scCopy.Env == originalEnv`).
- [ ] **T1.19:** Test 9 — `TestPatchServer_Rename_CombinedWithEnvDelta`: combined `{new_name, add_env, remove_env}` — sub-cases (a) all-success → both applied, (b) Step-2-fail → both unchanged in cfg + lm. Atomicity proof: env merges happen inside `scCopy` before Step 3, and Step 3 is the single commit point.
- [ ] **T1.20:** Test 10 — `TestPatchServer_Rename_DisabledFlag`: rename of disabled server: no auto-start under new name (Step 4 guard `if !scCopy.Disabled`).
- [ ] **T1.21 (F-ARCH-8):** Test 11 — `TestPatchServer_RenameNoOp_SameName`: PATCH `{new_name: "ctx7", add_env: [...]}` with `name == new_name` → rename branch SKIPPED, env/headers patch executes; **assertion: response body == `{"status":"updated"}`** (NOT `{"status":"patched","old_name":...,"new_name":...}` — that latter shape only fires when an actual rename happens). Env/headers actually applied to cfg.
- [ ] **T1.22:** Test 12 + 12b + 12c — `TestPatchServer_Rename_RebuildToolsCalled` (per-backend mcp.Server cleaned up for old, created for new on `s.gw != nil` path), `TestPatchServer_Rename_NilGateway_NoPanic` (server constructed without `gw` — rename succeeds without RebuildTools call), `TestPatchServer_PatchEnvOnly_NoRebuildTools` regression guard (in-place env-only PATCH does NOT call RebuildTools — wire spy `proxy.Gateway` with a counter; assert `count == 0`).
- [ ] **T1.23:** Test 13 — `TestIsSAPName`: positive cases `vsp-DEV`, `vsp-DEV-100`, `sap-gui-DEV`, `sap-gui-DEV-100`; negative cases `vsp-dev` (lowercase), `vsp-DE` (too short), `vspDEV` (no hyphen), `vsp-DEV-1000` (4-digit suffix), `vsp-DEV-`, `random-server`.
- [ ] **T1.24:** Run `go test ./...` + `go vet ./...` + `go build ./...` — must all pass with zero failures. Quote test count + failures into the GATE evidence block.

**Rollback (Phase 1):**

If Phase 1 lands a regression, revert via `git revert <commit-hash>` of the Phase 1 commit. The change is contained in three files (`internal/models/types.go`, `internal/api/server.go`, `internal/api/sap.go`) plus tests. No DB migrations, no config-file format change, no external service contract change. Existing PATCH callers (env-only, headers-only, disabled-only) are byte-identical because the rename branch is only entered when `patch.NewName != nil && *patch.NewName != name`. Daemon restart picks up reverted binary cleanly.

- [ ] GATE: tests + codereview + thinkdeep — zero errors (any finding at or above CLAUDE_GATE_MIN_BLOCKING_SEVERITY; default: any finding)

### Phase 2 — TS Extension Client

**Goal:** Implement the extension-side gateway client signature update (`patchServer` accepts `new_name`), the credential-store rename helper with index-first ordering, and the credentials listing helper. 5 unit tests including the load-bearing crash-recovery test (T2.5 / Test 16b).

**Tasks:**

- [ ] **T2.1:** Update `patchServer` signature in [vscode/mcp-gateway-dashboard/src/gateway-client.ts](../vscode/mcp-gateway-dashboard/src/gateway-client.ts) to accept the new shape:

  ```ts
  async patchServer(name: string, patch: {
      new_name?: string;
      disabled?: boolean;
      add_env?: string[];
      remove_env?: string[];
      add_headers?: Record<string, string>;
      remove_headers?: string[];
  }): Promise<StatusResponse>
  ```

  Backward-compat preserved: existing callers passing `{disabled:true}` or `{add_env:[...]}` continue to compile and work unchanged.
- [ ] **T2.2:** Implement `listServerCredentials(server: string): { env: string[]; headers: string[] }` in [vscode/mcp-gateway-dashboard/src/credential-store.ts](../vscode/mcp-gateway-dashboard/src/credential-store.ts). Validates server name; returns shallow copies of `entry.env` / `entry.headers`; returns `{env: [], headers: []}` for unknown server (no throw). NOT under `_chainIndexMutation` — read-only on the index snapshot.
- [ ] **T2.3:** Implement `renameServerCredentials(oldName: string, newName: string): Promise<void>` in `credential-store.ts` with **index-first ordering** (matches `storeEnvVar` ordering at [credential-store.ts:50-51](../vscode/mcp-gateway-dashboard/src/credential-store.ts#L50)):
  - Validate both names.
  - `await this._chainIndexMutation(async () => { ... })` — three steps inside:
    - **STEP 1** Index points at newName FIRST: `index.servers[newName] = { env: [...entry.env], headers: [...entry.headers] }; await this._setIndex(index);`
    - **STEP 2** Copy each secret from old key to new key (one `await secrets.get` + `await secrets.store` per key, undefined-safe).
    - **STEP 3** Delete each old secret + remove old index entry: `delete index.servers[oldName]; await this._setIndex(index);`.
  - Crash-mid-rename leaves `{newName: entry-shape}` in index — `reconcile()` can prune incomplete entries on next call (verified by Test 16b).
- [ ] **T2.4 (F-ARCH-2 — option (a) chosen; F-SPEC-2 corrected assertion):** Test 17 — `credential-store.test.ts — renameServerCredentials race + stranded-index-detection`. Setup: pre-populate index with `{ctx7: {env:[K1,K2], headers:[]}}`. Spawn two concurrent operations: (a) `storeEnvVar('ctx7', 'K3', 'v')`, (b) `renameServerCredentials('ctx7', 'ctx8')`. Force ordering via mock-call-order recorder so the `storeEnvVar` chain task runs AFTER the `renameServerCredentials` chain task completes (post-rename `_addToIndex('ctx7','env','K3')` resurrects the `ctx7` index entry per [credential-store.ts:232-234](../vscode/mcp-gateway-dashboard/src/credential-store.ts#L232)). Assert final state: index = `{ctx8: {env:[K1,K2], headers:[]}, ctx7: {env:[K3], headers:[]}}` (ctx7 resurrected by post-rename storeEnvVar); secrets exist for `mcpGateway/ctx8/env/K1`, `mcpGateway/ctx8/env/K2`, `mcpGateway/ctx7/env/K3`. **Then call `reconcile()` and assert the ctx7 index entry is NOT pruned** — secret K3 is still present, so reconcile (`_reconcileLocked`, [credential-store.ts:134-179](../vscode/mcp-gateway-dashboard/src/credential-store.ts#L134)) cannot identify it as logically orphaned (it only prunes index entries whose secrets are missing, not entries whose secrets are stale-but-present). Document in REVIEW: stranded ctx7 index entry persists after rename if a concurrent `storeEnvVar` resurrects it — manual cleanup via a future `auditOrphanSecrets` command is the documented mitigation path. **Do NOT assert reconcile prunes the entry — that assertion would be incorrect.**

  **Decision rationale (F-ARCH-2):** Option (a) chosen over option (b) because (1) the orphan-detection assertion makes the existing race observable as a test-recorded behavior rather than waiting for a future audit, (2) the alternative (audit storeEnvVar/storeHeader to move secrets.store inside _chainIndexMutation) is a wider refactor that touches the public contract of credential-store and could destabilize Phase 11 of audit-dashboard track which already shipped async token caching changes, (3) keeping the test ensures any future move of secrets.store inside _chainIndexMutation is validated against this race semantics.
- [ ] **T2.5:** Test 16b — `credential-store.test.ts — crash mid-rename → reconcile recoverable`. Force `secrets.store` to throw after first key copied (mock SecretStorage rejects every call after the first `store()`); the `_chainIndexMutation` callback throws mid-Step-2; assert: index has `{newName: entry-shape}` (Step 1 committed), one secret partially migrated, `_setIndex` for cleanup not invoked. Then call `reconcile()`; assert recovery — orphan-secret keys remain (reconcile doesn't auto-prune secrets without index entries — documented limitation), but index is consistent and no double-entry exists.
- [ ] **T2.6:** Test 14 — `gateway-client.test.ts — patchServer with new_name`: http stub records `PATCH /api/v1/servers/ctx7` with body `{new_name: "ctx8"}` and `Authorization` header from `buildAuthHeader()`; response shape `{status, old_name, new_name}` parsed correctly.
- [ ] **T2.7:** Test 15 — `credential-store.test.ts — renameServerCredentials migrates env+header`: pre-populated index + secrets; rename ctx7 → ctx8; assert all secrets moved from old to new key, old keys deleted, index updated; **assert index updated to point at newName BEFORE first `secrets.store` call** (verifies STEP 1 ordering — captured via mock-call-order recorder).
- [ ] **T2.8:** Test 16 — `credential-store.test.ts — renameServerCredentials handles missing entry`: rename a server not in index → early return, no error, no secret operations recorded.
- [ ] **T2.9:** Test 18 — `credential-store.test.ts — listServerCredentials`: returns env+header arrays for known server; empty `{env:[], headers:[]}` for unknown server.
- [ ] **T2.10:** Run `npm run compile && npm test -- --grep "credential-store|gateway-client"` in `vscode/mcp-gateway-dashboard/` — zero failures in scoped suite; baseline-flaky failures (LogViewer/GatewayClient timeouts) untouched.

**Rollback (Phase 2):**

`git revert` of the Phase 2 commit. Two files changed (`gateway-client.ts`, `credential-store.ts`); `gateway-client.patchServer` signature change is **additive** (new optional fields), so existing callers compile and run unchanged. `credential-store.renameServerCredentials` and `listServerCredentials` are net-new methods; revert deletes them. No SecretStorage data migration is run during Phase 2 — the rename method is only called from Phase 3 UI handler. Therefore reverting Phase 2 in isolation does not strand any extension secret state.

- [ ] GATE: tests + codereview + thinkdeep — zero errors (any finding at or above CLAUDE_GATE_MIN_BLOCKING_SEVERITY; default: any finding)

### Phase 3 — TS Extension UI

**Goal:** Wire the rename command + context menu, implement the `extension.ts` handler with input box + modal confirm + credentials-failure UX, and add 7 UI/command tests.

**Tasks:**

- [ ] **T3.1:** Update [vscode/mcp-gateway-dashboard/package.json](../vscode/mcp-gateway-dashboard/package.json):
  - Add command entry `{"command":"mcpGateway.renameServer","title":"Rename Server","icon":"$(edit)"}`.
  - Add `view/item/context` menu entry under `menus`: `{"command":"mcpGateway.renameServer","when":"view == mcpBackends && viewItem =~ /^(running|stopped|degraded|error|disabled|starting|restarting)$/","group":"1_modification@1"}`. The `viewItem` regex deliberately excludes SAP server contextValues (those use `sap-component`, `sap-system`, etc. naming, not the lifecycle status set).
- [ ] **T3.2:** Implement `mcpGateway.renameServer` handler in [vscode/mcp-gateway-dashboard/src/extension.ts](../vscode/mcp-gateway-dashboard/src/extension.ts) per spike §"TypeScript extension changes" §4. Use the **already-exported** `parseSapServerName` from [vscode/mcp-gateway-dashboard/src/sap-detector.ts:40](../vscode/mcp-gateway-dashboard/src/sap-detector.ts#L40) (NOT module-private regex constants). Input box with `validateInput` rejecting empty, unchanged, format-invalid (`SERVER_NAME_RE`), or SAP-shaped values. After confirm modal with "preserves" summary (env count + header count + secret count via `listServerCredentials`), call `client.patchServer(oldName, {new_name})`. On gateway success, call `credentialStore.renameServerCredentials(oldName, newName)`. On `renameServerCredentials` failure: warning toast `Server renamed to "{newName}" but {N} credential(s) could not be migrated. They remain under "{oldName}" in the keychain. Re-import KeePass or re-enter them manually.`. Always `await cache.refresh()` afterwards.
- [ ] **T3.3:** Test 19 — `commands.test.ts — mcpGateway.renameServer happy path`: stub `BackendItem` with `server.name='ctx7'`; input box returns `'ctx8'`; modal returns `'Rename'`; assert `client.patchServer('ctx7', {new_name:'ctx8'})` + `credentialStore.renameServerCredentials('ctx7','ctx8')` + `cache.refresh()` all fired in order; success info toast shown.
- [ ] **T3.4:** Test 20 — `commands.test.ts — mcpGateway.renameServer rejects SAP name`: stub `BackendItem` with `server.name='vsp-DEV'`; assert error toast `'Renaming SAP servers is not supported.'`; no `patchServer` call.
- [ ] **T3.5:** Test 21 — `commands.test.ts — mcpGateway.renameServer cancel input`: input box returns `undefined` (ESC); no `patchServer` call; no toast.
- [ ] **T3.6:** Test 22 — `commands.test.ts — mcpGateway.renameServer cancel confirm`: input box returns `'ctx8'`; modal returns `undefined` (ESC); no `patchServer` call.
- [ ] **T3.7:** Test 23 — `commands.test.ts — mcpGateway.renameServer API failure`: `client.patchServer` rejects with `GatewayError({kind:'http',message:'409 already exists'})`; assert error toast `'Rename failed: 409 already exists'`; `cache.refresh()` NOT fired (since rename did not happen).
- [ ] **T3.8:** Test 24 — `commands.test.ts — gateway PATCH succeeds, credential migration fails`: `client.patchServer` resolves; `credentialStore.renameServerCredentials` rejects with `Error('SecretStorage unavailable')`; assert warning toast wording matches T3.2 spec exactly (count from `item.server.env_keys + header_keys`); assert `cache.refresh()` fired (server-side rename succeeded); rename NOT rolled back gateway-side.
- [ ] **T3.9:** Test 19b — `commands.test.ts — input box validateInput rejects bad name`: simulate `validateInput('bad name with spaces')` → returns `'Invalid name (1-64 chars: a-z, A-Z, 0-9, -, _)'`. Simulate `validateInput('vsp-XYZ')` → returns SAP rejection wording. Simulate `validateInput(oldName)` → returns `null` (unchanged passes through to early-return).
- [ ] **T3.10:** Run `npm run compile && npm test` in `vscode/mcp-gateway-dashboard/` — full extension suite passes; **zero new regressions**; baseline-flaky failures unchanged.
- [ ] **T3.11 (F-SPEC-3):** `npm run deploy` — auto-version bump → build → package VSIX → install to local VSCode. Stage rebuilt VSIX binary alongside source changes. Single commit must bundle source + VSIX (per CLAUDE.md VSCode Extension Build Discipline). **After commit succeeds, instruct the operator to run `Developer: Reload Window` in VSCode** so the newly installed VSIX activates — without this, the rename context-menu item will not appear despite a successful install.

**Rollback (Phase 3):**

`git revert` of the Phase 3 commit reverts package.json (command + menu removed → command palette no longer exposes Rename Server), extension.ts handler (the `registerCommand` block deleted), and the bundled VSIX. After revert, operator runs `Developer: Reload Window` to deactivate the old VSIX. The credential-store and gateway-client methods from Phase 2 remain on disk but unreachable from the UI — operators cannot trigger rename via the extension. CLI / REST callers can still call `PATCH new_name` directly (Phase 1 not reverted), so a second revert of Phase 1 is required if the entire feature must be withdrawn from the gateway too.

- [ ] GATE: tests + codereview + thinkdeep — zero errors (any finding at or above CLAUDE_GATE_MIN_BLOCKING_SEVERITY; default: any finding)

### Phase 4 — Documentation + manual E2E + VSIX deploy + commit + push

**Goal:** Update README + CHANGELOG + ROADMAP, run the manual E2E checklist (9 items per F-ARCH-3), final security pass, push to origin/main.

**Tasks:**

- [ ] **T4.1 (F-ARCH-3 — expanded from 6 to 9 items):** Manual E2E checklist at [docs/qa/server-rename-smoke.md](qa/server-rename-smoke.md):
  1. Rename `ctx7` (no creds) → `context7-prod`. Verify: tree shows new name, `~/.claude/commands/ctx7.md` deleted, `context7-prod.md` created with same content.
  2. Rename server WITH creds. Verify: `restartServer` works under new name (creds applied); secrets-list under old name returns empty.
  3. Rename via input box → ESC at confirm. Verify: no rename happens.
  4. Rename to a name that already exists. Verify: 409 error toast `'Rename failed: ...409...'`.
  5. Rename SAP server `vsp-DEV`. Verify: button hidden in context menu (per `viewItem` regex) AND, if invoked via command palette, error toast `'Renaming SAP servers is not supported.'`.
  6. Rename + simultaneous env update via API call (combined PATCH). Verify: both applied; env reflected on next start.
  7. **(NEW per F-ARCH-3)** Plan A rollback UX. Force a rollback path by killing the gateway daemon between Step 1 and Step 2 of the rename branch (e.g. send SIGKILL during `lm.RemoveServer` window — observed via long stdio shutdown). Operator restarts daemon. Verify: error toast surfaces "rename failed at remove stage (rolled back)"; cfg map state on disk shows OLD name; lm-side has no orphan; tree shows OLD name on next refresh; no zombie child process for newName.
  8. **(NEW per F-ARCH-3)** Credential-migration failure UX. Pre-arrange VSCode SecretStorage to be in a degraded state (e.g. lock the user's keychain on macOS / revoke DPAPI on Windows for the test machine — alternatively, mock via the test harness). Trigger rename. Verify: gateway shows new name; warning toast appears with exact wording from T3.2; secrets remain queryable under old name via `mcp-ctl credential list`; tree shows new name; subsequent restart of the new-name server logs missing-credentials warnings.
  9. **(NEW per F-ARCH-3)** `~/.claude.json` propagation. Before rename: confirm `mcp-gateway:ctx7` entry exists in `~/.claude.json::mcpServers` (claude-config-sync wrote it). Trigger rename ctx7 → ctx8. Within `cache.refresh + claude-config-sync` window (~1–2s of polling), confirm `mcp-gateway:ctx7` is removed and `mcp-gateway:ctx8` is added in `~/.claude.json::mcpServers` with the same Bearer header reference. Verify Claude Code 2.x picks up the change without restart (via FS watcher); aggregate `/mcp` URL unchanged so existing connections stay valid; `claude mcp list` after a few seconds reflects the new namespaced name.
- [ ] **T4.2:** README "Renaming a server" section in `README.md`: explain the UI flow (right-click server → Rename Server → enter new name → confirm), the SAP-name limitation, the credential preservation behavior, and the `~/.claude.json` automatic propagation. Add a callout noting Plan A rollback semantics (atomic rollback on lm-removal failure) and the credential-migration failure path with explicit "remains under old name in keychain" wording.
- [ ] **T4.3:** CHANGELOG.md entry under Unreleased / next version (match existing CHANGELOG semver cadence — `vNN.NN.0` increments by minor for new feature). Sections: **Added** (rename via PATCH new_name + extension Rename Server command), **Security** (SAP refusal both sides + index-first credential migration), **Known limitations** (orphan secrets after credential-migration failure require manual cleanup; documented via Tracker reference).
- [ ] **T4.4:** ROADMAP.md update — add a new track section "Server Rename track" with one-line Phase summary, link to plan + spike, mark all 4 phases complete with commit hashes after Phase 4 commits.
- [ ] **T4.5:** Final security cross-validation pass via PAL `mcp__pal__codereview` (model: `gpt-5.2-pro`, gate_mode=true) on all changed files across Phases 1–3. Findings at any severity → fix in-cycle. If PAL MCP unavailable, fall back to internal cross-model review per CLAUDE.md (Agent tool, different model tier).
- [ ] **T4.6:** Commit + push: stage VSIX + source + docs in a single commit per CLAUDE.md VSCode Extension Build Discipline. Push to `origin/main`. Inspect commit and push output for hook failures per Post-Commit/Push Discipline. Notify operator (per Git & GitLab section) and offer to push if not auto-pushed.
- [ ] **T4.7:** Post-push smoke: verify GitLab CI pipeline green (gitleaks, dogfood-smoke, go test, npm test). If any CI step fails, fix in-cycle (do not weaken rules per CLAUDE.md).
- [ ] **T4.8:** Operator hand-off: announce in the run summary that the manual E2E checklist (T4.1, 9 items) is the operator's portion. Operator runs items 1–9; reports pass/fail per item; failures route to a follow-up cycle (revert Phase 3 if blocking, revert Phase 1 if cgi-side fundamental flaw).

**Rollback (Phase 4):**

Phase 4 is documentation-only on the code side (T4.2/T4.3/T4.4) plus the operator E2E sweep (T4.1) and the security cross-validation (T4.5). If T4.5 surfaces a critical security finding, revert Phases 1–3 in reverse order (3 → 2 → 1). If E2E surfaces a UX defect (e.g. dialog wording wrong), patch in-cycle without full revert. README + CHANGELOG + ROADMAP edits revert via `git revert`. The CI smoke step has no rollback because it is observation-only.

- [ ] GATE: tests + codereview + thinkdeep — zero errors (any finding at or above CLAUDE_GATE_MIN_BLOCKING_SEVERITY; default: any finding)

## 6. Per-Phase Gate Standards

Per CLAUDE.md "Per-Phase Gate (MANDATORY)":
- Default threshold: any finding at any severity blocks. Configurable via `CLAUDE_GATE_MIN_BLOCKING_SEVERITY`.
- TDD advisory: write failing test first (most Phase 1 / Phase 2 / Phase 3 tests are written before the implementation tasks they cover).
- Real-boundary evidence per HGL.1 / Inv-5: every GATE PASS commit message must include the `## Real-boundary evidence` block listing invariant claimed (Inv-2 cancellation ownership for the rollback path is the strongest claim; Inv-4A correctness for index-first ordering CAS is also relevant), test name, file:line, boundary type, and why-this-test-catches-the-failure-mode paragraph.

## 7. Plan Artifacts

- **PLAN-docs-spikes-2026.md** (this file) — phase + gate definitions
- **TASKS-docs-spikes-2026.md** — flat task checklist (companion file)
- **REVIEW-docs-spikes-2026.md** — accumulating findings with severity, action_taken, and cycle metadata (created by lead-auditor and code-reviewer agents during `/run`)
- **docs/qa/server-rename-smoke.md** — created in T4.1; manual E2E checklist (9 items)
- **docs/spikes/2026-05-05-server-rename.md** — source spike (edited in T1.0 to correct line 22)

## 8. Execution Mode

After operator approval:

```
/run docs-spikes-2026          # runs Phase 1, halts at GATE 1
/run docs-spikes-2026 2        # runs 2 phases
/run docs-spikes-2026 all      # runs everything to completion
```

## 9. Risks (carried from spike §13 — recap, accurate to v4)

| # | Risk | Severity | Mitigation |
|---|------|----------|------------|
| R1 | Race: `/health` reader observes lm with extra entry while cfg shows fewer — bounded by `lm.Stop` duration (up to ~9 s for stdio). | LOW | Direction is benign (lm-extra-vs-cfg-fewer); daemon restart in this window reloads lm from cfg → only OLD name survives. T1.16 + T1.17 cover the failure-path observability. |
| R2 | `lm.Stop` timeout under r.Context() cancellation → silent zombie child. Pre-existing in `handleRemoveServer`; inherited by rename. | LOW | T1.17 (Test 7f) regression-guards the inherited behavior. Future fix to `lm.RemoveServer` propagates cleanly into rename. |
| R3 | Credential migration partial failure (orphan secrets under old name). | LOW | Index-first ordering; T2.5 (Test 16b) verifies recoverability. T3.8 (Test 24) verifies UX. Documented manual-cleanup follow-up per CHANGELOG Known limitations entry. |
| R4 | Operator renames to SAP-shaped name. | MEDIUM | Block at API (400) AND in UI (validateInput + handler check). T1.10 + T1.11 + T3.4 + T3.9 cover both layers. |
| R5 | Auto-start after rename fails. | MEDIUM | Don't auto-rollback at start step (config + lm membership already applied). T1.14 (Test 7c) verifies warn-only. Matches `handleAddServer:787-789`. |
| R6 | Rollback-of-rollback failure. | MEDIUM | T1.13 (Test 7b) verifies ERROR-level log fires. Operator must manually reconcile. Probability extremely low. |

## 10. Open Questions

1. **Should T2.4 / Test 17 audit option (b) be promoted to a follow-up tracker?** Decision in §3 chose option (a) with the orphan-detection assertion. If `reconcile()` cannot detect orphan-secret-without-index in the test, log a follow-up tracker `v17-rename-orphan-audit` to either extend reconcile or move secrets.store inside `_chainIndexMutation`.
2. **Should compile time of Phase 4 T4.5 PAL pass include all Phase 1–3 commits as a single review?** Recommended yes — the surface area of ~990 LOC is reviewable in one PAL pass and catches cross-phase contract drift (e.g. response body shape mismatch between Go and TS).
3. **Should the manual E2E `~/.claude.json` propagation timing (item 9) be tightened?** Currently `~1–2s of polling`; this depends on `cache.startAutoRefresh` interval. If E2E reveals propagation lag exceeds operator tolerance, raise to a follow-up tracker; do not block GATE 4.

---

## Next Plans

Sourced from [docs/ROADMAP.md](ROADMAP.md):

1. **Audit-dashboard track Phase 12 (final phase)** — closing v1.9.1 audit-dashboard track. Phases 9, 10, 11 are landed (per `project_phase_audit_dashboard_phase{9,10,11}.md` memories); Phase 12 is the final closure phase per `docs/PLAN-audit-dashboard.md`. Natural successor because rename feature is independent of audit-dashboard remediation but shares the same VSCode extension testing infrastructure (which Phase 2/3 of this plan exercises).

2. **Debug-flicker.3 + debug-flicker.4B + debug-flicker.Final** (Phase 4-equivalent of `docs/PLAN-debug-flicker.md`) — install Claude Code plugin end-to-end + wire Activate-for-CC button to `mcp-ctl install-claude-code` with webview log streaming + final commit bundling. Phase 4B already landed per `project_debug_flicker_phase4B.md`; only `.3` (operator steps) and `.Final` (deploy commit) remain.

3. **v16-1: Hash-augmented slash-command marker** (post-v1.5.0 candidate). Tolerate operator edits below line 1 of generated `.claude/commands/<server>.md` files via per-entry content hash on the magic-header marker. Currently the marker is regenerated in full on each server `running` transition; below-line-1 edits are silently overwritten. Natural follow-up because rename feature exercises slash-command file rename (old `ctx7.md` deleted, new `context7-prod.md` created) which is the same surface area.

