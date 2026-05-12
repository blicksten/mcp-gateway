# PLAN — Server Rename Feature — Implementation Plan

**Session label:** `server-rename` (renamed from `docs-spikes-2026` on 2026-05-11 — the auto-derived path-based label had no semantic content; renamed to match the spike topic)
**Companion files:** [docs/TASKS-server-rename.md](TASKS-server-rename.md), [docs/REVIEW-server-rename.md](REVIEW-server-rename.md)
**Source spike:** [docs/spikes/2026-05-05-server-rename.md](spikes/2026-05-05-server-rename.md) (v4 — fully validated; revision history confirms all 13 findings F-1..F-13 from prior audit `checkpoint-check-d1c32725` were addressed)
**Created:** 2026-05-06 by Porfiry [Opus 4.7]
**Actualized:** 2026-05-11 by Porfiry [Opus 4.7] — see §11 below for the diff vs. the 2026-05-06 draft.
**Status:** Drafted by dev-lead in pipeline `planning-19b7b15b`; actualized in-session 2026-05-11; awaits operator approval before `/run server-rename`

---

## 1. Goal

Add the ability to **rename a registered backend MCP server** (e.g. `ctx7` → `context7-prod`) with full propagation across three atomic regions: gateway in-memory `s.cfg.Servers` map, gateway lifecycle manager (`lm.entries`), and extension-side credential index + secrets. Rename is bundled with the existing `PATCH /api/v1/servers/{name}` endpoint via a new optional `new_name` field — rename + env/header/disabled changes commit transactionally; partial-completion windows are bounded and logged.

The feature blocks at API and UI on **SAP-named** servers (`vsp-XXX`, `sap-gui-XXX`) because their names encode SID/client and renaming would break the SAP detector.

## 2. Plan-level facts

| Item | Value |
|------|-------|
| Total LOC | **~940** (Go ~490, TS ~450) — **down ~50** vs. 2026-05-06 draft (no `internal/api/sap.go` because `mcp-gateway/internal/sapname.IsSAP` already exists from sap-picker T-A.2 codegen) |
| Total tests | **33** (20 Go + 13 TS) plus a 9-item manual E2E checklist — spike's 31 base + Test 7f (F-ARCH-4) + Test 19b (T3.9) − Test 13 (TestIsSAPName moved to existing `internal/sapname/grammar_gen_test.go`); **+1 trivial extension to `MockSecretStorage` failure-injection** (Phase 2 prereq T2.0) |
| Expected duration | **~14.5 hours** of focused work, gated across 4 phases (down from 16h: −1h sap.go authoring, −0.5h test 13 retire, +0.5h spike sanity-check + mock extension) |
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
| F-ARCH-7 | LOW | T1.5 (Phase 1 task description) — **OBSOLETED by 2026-05-11 actualization** | No regex is rolled at all. Plan now imports `mcp-gateway/internal/sapname` (regex-free, codegen from `docs/grammar/sap-server-name.yaml`, R-21) and calls `sapname.IsSAP(name)`. Drift between Go and TS detectors is structurally impossible because both sides are emitted from the same YAML. |
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
Phase 1 — Go API                  (≈ 490 LOC, 20 tests, ~6.5 h)   ← rename branch + sapname.IsSAP import + Plan A rollback (no new sap.go)
Phase 2 — TS Extension Client     (≈ 85 LOC,  6 tests,  ~1.5 h)   ← +T2.0 mock-knob (~10 LOC) + gateway-client.patchServer + credential-store
Phase 3 — TS Extension UI         (≈ 125 LOC, 7 tests,  ~2.2 h)   ← package.json + extension.ts handler + UI tests
Phase 4 — Documentation + E2E     (≈ — LOC,   9 manual, ~2.0 h)   ← README + manual E2E + VSIX deploy + commit (target: extension v1.34.0)
PAL gate cycles + buffer          (—,  —, ~2.3 h)                 ← codereview/thinkdeep/rollback fix-loops across 4 phases
                                       ─────────────────
                                       ≈ 14.5 h  (was 16 h on 2026-05-06; −1.5 h via sapname reuse + helper-existence verify)
```

### Phase 1 — Go API

**Goal:** Implement the gateway-side rename branch in `handlePatchServer` with Plan A ordering (lm.AddServer first → lm.RemoveServer second → cfg-mutation third), the `sapname.IsSAP` import (from existing `internal/sapname` codegen — no new file, no regex; see T1.5 for rationale), model change, and **20 Go test functions across 18 test tasks** (T1.22 contains 3 sub-functions: `_RebuildToolsCalled`, `_NilGateway_NoPanic`, `_PatchEnvOnly_NoRebuildTools`) — including 6 new failure-path cases (T1.12..T1.17).

**F-ARCH-1 / F-ARCH-9 notes (carry into commit message):**

- **Spike line 22 must be corrected before code work begins.** Original wording "Bounded to ~1ms in practice" contradicts actual `lm.Stop` budget for graceful child-process shutdown (up to ~9 s for stdio per `lifecycle/manager.go:568`, ~2 s for HTTP/SSE). T1.0 corrects to `Bounded to lm.Stop duration (up to ~9s for stdio, ~2s for HTTP/SSE)`.
- **Rollback context choice (F-ARCH-9 verbatim):** When `lm.RemoveServer(name)` fails after the new name has already been registered in lm, the rollback `lm.RemoveServer(newName)` is invoked with `context.Background()` (NOT the request context). The request context may already be cancelled (the very reason `RemoveServer(name)` failed), and using it would skip rollback entirely. `context.Background()` ensures the just-added new name is removed before the handler returns.
- **Rate-limit follow-up (F-ARCH-9 verbatim):** No request-rate limiter is added in this PR. The PATCH endpoint inherits the existing chi-router middleware stack (auth → CSRF → throttle 20). If rename traffic ever spikes (which is unlikely — operators rename servers manually, not in loops), tracker `v16-rename-rate-limit` covers a follow-up to add a per-IP token bucket dedicated to rename calls.

**Tasks:**

- [x] **T1.0 (F-ARCH-1, F-ARCH-6, F-SPEC-1 — prerequisites; ACTUALIZED 2026-05-11):** Update [docs/spikes/2026-05-05-server-rename.md](spikes/2026-05-05-server-rename.md) on the following targets:
    1. **Line 27** — the inconsistency-window bullet starting `Between region (a) and (b)` — to read `Bounded to lm.Stop duration (up to ~9s for stdio, ~2s for HTTP/SSE)` instead of the current `Bounded to ~1ms in practice`. Locate by content (`Between region (a) and (b)`) rather than by raw line number to be resilient against unrelated edits. **Note: line 22 is the harmless `(b)`-row of the atomic-regions table — do NOT edit it.**
    2. **Lines 257-270 (F-SPEC-1, REWRITTEN 2026-05-11):** the §"3. SAP name helper" block currently proposes a new `internal/api/sap.go` with hand-rolled regex. **REWRITE the block** to read: *"3. SAP name helper — gateway-side enforcement uses the existing regex-free codegen package `mcp-gateway/internal/sapname` (emitted by `tools/grammar-gen` from `docs/grammar/sap-server-name.yaml`, R-21). Import the package in `internal/api/server.go` and call `sapname.IsSAP(name)` for the rename refusal. No new file. No regex (CLAUDE.md "Regex Discipline (MANDATORY)" rule). Drift between Go and TS detectors is structurally impossible because both sides are emitted from the same YAML."* The hand-rolled regex code block must be deleted (the comparison sample for `(?:-\d{3})?` non-capturing group becomes moot — there is no Go regex to compare to TS regex).
    3. **Verify pre-conditions (already satisfied as of 2026-05-11; record in REVIEW):**
       - `lm-error-injection` helper: ✅ present (`testStopHook` in [internal/lifecycle/manager.go:55](../internal/lifecycle/manager.go#L55), gated to tests).
       - `MockSecretStorage`: ✅ present (`vscode/mcp-gateway-dashboard/src/test/mock-vscode.ts:242`) and already imported by `credential-store.test.ts`. **Limitation:** the existing mock has no failure-injection knob — addressed by net-new T2.0 below (~10 LOC extension), NOT by a separate helper file.
       - **No estimate inflation needed** — both helpers exist; the +2–3h overrun warned in the 2026-05-06 draft does not apply.
- [x] **T1.0b (NEW 2026-05-11 — cross-spike sanity check before T1.3):** Read both [docs/spikes/2026-05-08-mcp-server-routing-bypasses.md](spikes/2026-05-08-mcp-server-routing-bypasses.md) and [docs/spikes/2026-05-09-reflector-coordination.md](spikes/2026-05-09-reflector-coordination.md) and record in REVIEW one paragraph each:
    1. **2026-05-08 (routing bypasses)** — confirm that since F1 cleanup landed, **all** MCP routing flows through gateway (`.mcp.json` stdio entries removed; `~/.claude.json::mcpServers` namespaced under `mcp-gateway:*`). Therefore `RebuildTools` after rename (T1.3 step 5) is the single channel through which clients learn the new name; no additional cross-window broadcast is needed. F3 (zombie children on Stop) is being addressed in `sap-picker-and-import-mcp` T-A.5 and is independent of rename.
    2. **2026-05-09 (reflector coordination)** — confirm that the `~/.claude.json` propagation path used by manual E2E item 9 (T4.1) is the **TS-side** reflector `vscode/mcp-gateway-dashboard/src/claude-config-sync.ts`, with its own CAS-style content-fingerprint retry. There is **no** Go-side `internal/api/claude_config_sync.go` despite what some older plan text in the wider repo may suggest. Rename does not need a new daemon hook — the next reflector tick after `cache.refresh()` carries the new name into `~/.claude.json`.
- [x] **T1.1:** Add `NewName *string` field to `models.ServerPatch` in [internal/models/types.go](../internal/models/types.go) — pointer so empty string is distinguishable from "field absent". JSON tag `json:"new_name,omitempty"`.
- [x] **T1.2:** Add `ValidateServerName` invocation path: confirm existing `models.ValidateServerName` works on `*patch.NewName` and produces a 400 with the existing error wording. No new validator function needed.
- [x] **T1.3:** Implement `handlePatchServer` rename branch in [internal/api/server.go:824](../internal/api/server.go#L824). Follow the validation order pinned in spike §"Validation order (pinned)" — JSON decode → SAP refusal (only when `patch.NewName != nil`) → ValidateServerName → existing env/header validation → cfgMu.Lock → 404 lookup → scCopy build/merge → scCopy.Validate → branch on rename. Rename branch implements Plan A:
  - **Step 1** `s.lm.AddServer(newName, &scCopy)` — 409 on collision.
  - **Step 2** `s.lm.RemoveServer(r.Context(), name)` — on error, rollback via `s.lm.RemoveServer(context.Background(), newName)` (F-ARCH-9 carry); if rollback also fails, log at ERROR level with both error fields and return 500 with `"rename failed at remove stage (rolled back): ..."` body.
  - **Step 3** `s.cfgMu.Lock()` → `s.cfg.Servers[newName] = &scCopy; delete(s.cfg.Servers, name)` → `s.marshalConfig()` → `s.cfgMu.Unlock()` → `s.flushConfig(data)`.
  - **Step 4** `if !scCopy.Disabled { s.lm.Start(r.Context(), newName) }` — **warn-only**, do NOT roll back (parity with `handleAddServer:787-789`).
  - **Step 5** `if s.gw != nil { s.gw.RebuildTools() }`; `s.TriggerPluginRegen()`.
  - **Response:** 200 with `{"status":"patched","old_name":name,"new_name":newName}`.
- [x] **T1.4 (ACTUALIZED 2026-05-11 — call-site renamed):** Add SAP refusal pre-check at top of handler — fires only when `patch.NewName != nil`; if `sapname.IsSAP(name) || sapname.IsSAP(*patch.NewName)` → 400 with body message exactly `"renaming SAP-named servers is not supported"`. Confirms the codebase 400-for-validation convention. **Existing env-only / disabled-only PATCHes against SAP-named servers must still work**; SAP non-goal is renaming, not all-mutation.
- [x] **T1.5 (F-ARCH-7 — REWRITTEN 2026-05-11):** Use the existing regex-free codegen helper `mcp-gateway/internal/sapname.IsSAP(name string) bool` ([internal/sapname/grammar_gen.go:127](../internal/sapname/grammar_gen.go#L127)). It is emitted by `tools/grammar-gen` from [docs/grammar/sap-server-name.yaml](../docs/grammar/sap-server-name.yaml) (R-21, sap-picker T-A.2, commit `85cbebc`) and uses only string-prefix and char-code-range checks per CLAUDE.md "Regex Discipline (MANDATORY)".

  **No new file.** Add the import line `"mcp-gateway/internal/sapname"` to [internal/api/server.go](../internal/api/server.go) (the rename refusal site in T1.4 is the only call site) and replace the proposed `isSAPName(name)` calls with `sapname.IsSAP(name)`.

  **Drift impossibility:** because both Go and TS detectors are emitted from the same YAML grammar, F-ARCH-7's original concern (capturing-group / parity drift between hand-rolled Go regex and the TS regex literal) is structurally eliminated — there are no regex literals on either side anymore, just YAML-derived grammar.
- [x] **T1.6:** Test 1 — `TestPatchServer_Rename_Success`: name swap in cfg + lm; env/headers preserved; auto-start under new name; old name absent from `cfg.Servers`.
- [x] **T1.7:** Test 2 — `TestPatchServer_Rename_NameCollision`: pre-populate cfg with both `ctx7` and `ctx8`; PATCH ctx7 with `new_name=ctx8` → 409; cfg + lm unchanged.
- [x] **T1.8:** Test 3 — `TestPatchServer_Rename_InvalidName`: `new_name=""` and `new_name="bad name with spaces"` both → 400 with `ValidateServerName` error wording.
- [x] **T1.9:** Test 4 — `TestPatchServer_Rename_NotFound`: PATCH non-existent server → 404; cfg + lm unchanged.
- [x] **T1.10:** Test 5 + Test 6 — `TestPatchServer_Rename_SAPRefused_Old` (`name=vsp-DEV`) and `TestPatchServer_Rename_SAPRefused_New` (`new_name=vsp-XYZ`) both → 400 with exact `"renaming SAP-named servers is not supported"` body.
- [x] **T1.11:** Test 6b — `TestPatchServer_Rename_SAPBeatsBadEnv`: PATCH `{new_name:"vsp-XYZ", add_env:["bad=val"]}` → 400 SAP refusal (NOT 400 bad env). Proves validation order step 2 short-circuits step 4.
- [x] **T1.12:** Test 7 — `TestPatchServer_Rename_RollbackOnRemoveFailure`: inject `lm.RemoveServer(name)` error → assert rollback `lm.RemoveServer(newName)` fires; final lm state has only `name`; cfg untouched.
- [x] **T1.13:** Test 7b — `TestPatchServer_Rename_RollbackOfRollbackErrorLogged`: both `lm.RemoveServer(name)` AND its rollback `lm.RemoveServer(newName)` fail → ERROR-level log entry with both error fields; HTTP 500 body shape verified.
- [x] **T1.14:** Test 7c — `TestPatchServer_Rename_StartFailWarnsNotRollback`: `lm.Start(newName)` returns error → 200 OK still returned; warn-level log entry; cfg + lm both reflect new name (no rollback — parity with `handleAddServer:787-789`).
- [x] **T1.15:** Test 7d — `TestPatchServer_Rename_BadEnvShortCircuits`: PATCH `{new_name:"ok", add_env:["INVALID="]}` → 400 BEFORE any state mutation; cfg + lm unchanged.
- [x] **T1.16:** Test 7e — `TestPatchServer_Rename_PluginRegenFailureSwallowed`: construct server with a faulty `pluginRegen` callback that records failure to a captured logger buffer; rename returns 200 OK; rename completes; assert failure log entry exists in captured buffer (TriggerPluginRegen returns no error — verify via captured logger output, not error-return).
- [x] **T1.17 (F-ARCH-4):** Test 7f — `TestPatchServer_Rename_StopTimedOutSilentZombie`: assertion shape — inject scenario where `lm.RemoveServer(name)` returns `nil` BUT the underlying `Stop(ctx, name)` timed out (from `lifecycle/manager.go:568` — error swallowed via `_ = m.Stop(...)`). Test asserts handler treats this as success (200 OK), final cfg + lm both reflect new name, AND a separate observability assertion: log entry at WARN level documents that `Stop` swallowing happened (if/when such logging is added). **Note**: this test currently locks down inherited pre-existing behavior (silent zombie child is a known LOW-severity risk per spike §13.2 row 2). Test 7f is a regression guard so future fixes to upstream `lm.RemoveServer` propagate cleanly into rename.
- [x] **T1.18:** Test 8 — `TestPatchServer_Rename_PreservesEnv`: env values intact after rename (no PATCH-level env op); read SecretStorage equivalent (Go side has no SecretStorage; assert `scCopy.Env == originalEnv`).
- [x] **T1.19:** Test 9 — `TestPatchServer_Rename_CombinedWithEnvDelta`: combined `{new_name, add_env, remove_env}` — sub-cases (a) all-success → both applied, (b) Step-2-fail → both unchanged in cfg + lm. Atomicity proof: env merges happen inside `scCopy` before Step 3, and Step 3 is the single commit point.
- [x] **T1.20:** Test 10 — `TestPatchServer_Rename_DisabledFlag`: rename of disabled server: no auto-start under new name (Step 4 guard `if !scCopy.Disabled`).
- [x] **T1.21 (F-ARCH-8):** Test 11 — `TestPatchServer_RenameNoOp_SameName`: PATCH `{new_name: "ctx7", add_env: [...]}` with `name == new_name` → rename branch SKIPPED, env/headers patch executes; **assertion: response body == `{"status":"updated"}`** (NOT `{"status":"patched","old_name":...,"new_name":...}` — that latter shape only fires when an actual rename happens). Env/headers actually applied to cfg.
- [x] **T1.22:** Test 12 + 12b + 12c — `TestPatchServer_Rename_RebuildToolsCalled` (per-backend mcp.Server cleaned up for old, created for new on `s.gw != nil` path), `TestPatchServer_Rename_NilGateway_NoPanic` (server constructed without `gw` — rename succeeds without RebuildTools call), `TestPatchServer_PatchEnvOnly_NoRebuildTools` regression guard (in-place env-only PATCH does NOT call RebuildTools — wire spy `proxy.Gateway` with a counter; assert `count == 0`).
- [x] **T1.23 (REWRITTEN 2026-05-11; expanded by /check `checkpoint-check-e6e30eef` F-ARCH-A2 LOW):** ~~Test 13 `TestIsSAPName`~~ **DROPPED** — the codegen helper has its own grammar tests at [internal/sapname/grammar_gen_test.go](../internal/sapname/grammar_gen_test.go); duplicating them in `internal/api/sap_test.go` would violate DRY and tie us to a generated symbol surface. **Replacement:** Test 13 — `TestPatchServer_RenameRefusal_UsesSapnamePackage`: assert that the rename branch refuses (a) `vsp-DEV` (positive case), (b) `random-server` (negative — must NOT 400-for-SAP; rename proceeds normally). Lock down that the rename refusal call site actually invokes `sapname.IsSAP` rather than a regression to a hand-rolled check (covered indirectly by tests 5/6 already, but a fresh test with `random-server` makes the negative path explicit). **F-ARCH-A2 LOW invariant assertions (added 2026-05-11):** (c) `Vsp-DEV` (capital `V`) MUST NOT be treated as SAP — proves byte-strict prefix check; rename proceeds. (d) `vsp-dev` (lowercase SID) MUST NOT be treated as SAP — proves byte-strict charset check (codegen `c >= 'A' && c <= 'Z'` at [internal/sapname/grammar_gen.go:91](../internal/sapname/grammar_gen.go#L91)); rename proceeds. These two assertions are a regression flag — if a future change adds `strings.ToUpper` or whitespace-trim normalization to either the parser or the rename call site, these two cases will start failing 400-SAP and surface the contract change immediately.
- [x] **T1.24:** Run `go test ./...` + `go vet ./...` + `go build ./...` — must all pass with zero failures. Quote test count + failures into the GATE evidence block.

**Rollback (Phase 1):**

If Phase 1 lands a regression, revert via `git revert <commit-hash>` of the Phase 1 commit. The change is contained in **two files** (`internal/models/types.go` for the `NewName *string` field, `internal/api/server.go` for the rename branch + `sapname` import) plus tests. No new file (`internal/api/sap.go` was retired in the 2026-05-11 actualization in favor of importing the existing `mcp-gateway/internal/sapname` package). No DB migrations, no config-file format change, no external service contract change. Existing PATCH callers (env-only, headers-only, disabled-only) are byte-identical because the rename branch is only entered when `patch.NewName != nil && *patch.NewName != name`. Daemon restart picks up reverted binary cleanly.

- [x] GATE: tests + codereview + thinkdeep — zero errors (any finding at or above CLAUDE_GATE_MIN_BLOCKING_SEVERITY; default: any finding) — **PASSED 2026-05-12** via PAL gpt-5.1-codex gate_mode=true (codereview + thinkdeep both verdict=PASS 0 findings, plus 25 rename tests / 21 packages pass, build+vet clean). Real-boundary evidence recorded in `docs/REVIEW-server-rename.md` (Inv-2 cancellation ownership: rollback uses `context.Background()` per F-ARCH-9). Pipeline `execute-29bd533a` cancelled at step 5 due to spawn_token lease mechanism after long PAL calls; work and gates fully validated outside pipeline.

### Phase 2 — TS Extension Client

**Goal:** Implement the extension-side gateway client signature update (`patchServer` accepts `new_name`), the credential-store rename helper with index-first ordering, and the credentials listing helper. 5 unit tests including the load-bearing crash-recovery test (T2.5 / Test 16b).

**Tasks:**

- [x] **T2.0 (NEW 2026-05-11 — Phase 2 prerequisite, ~10 LOC):** Extend `MockSecretStorage` in [vscode/mcp-gateway-dashboard/src/test/mock-vscode.ts:242](../vscode/mcp-gateway-dashboard/src/test/mock-vscode.ts#L242) with a failure-injection knob — a `failAfterNStores(n: number, error: Error)` method that arms the mock to throw `error` on the (n+1)-th `store()` call, leaving the first n calls passing through normally. Also a matching `failAfterNGets(n, error)` for symmetry. **Both are no-ops by default** — existing call sites (`commands.test.ts:37`, `credential-store.test.ts:8`, `add-server-panel.test.ts:48`, `sap-detail-panel.test.ts:26`, `server-detail-panel.test.ts:26`) continue to compile and pass byte-identically. Required by T2.5 / Test 16b "crash mid-rename → reconcile recoverable" which needs `secrets.store` to throw after the first key copied. **Why not a separate helper file:** keeping the failure-injection in the existing mock means there is one MockSecretStorage type for the whole extension test suite; a separate "FailingMockSecretStorage" subclass would create a second hierarchy and a new import path for tests that need both behaviors.

- [x] **T2.1:** Update `patchServer` signature in [vscode/mcp-gateway-dashboard/src/gateway-client.ts](../vscode/mcp-gateway-dashboard/src/gateway-client.ts) to accept the new shape:

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
- [x] **T2.2:** Implement `listServerCredentials(server: string): { env: string[]; headers: string[] }` in [vscode/mcp-gateway-dashboard/src/credential-store.ts](../vscode/mcp-gateway-dashboard/src/credential-store.ts). Validates server name; returns shallow copies of `entry.env` / `entry.headers`; returns `{env: [], headers: []}` for unknown server (no throw). NOT under `_chainIndexMutation` — read-only on the index snapshot.
- [x] **T2.3:** Implement `renameServerCredentials(oldName: string, newName: string): Promise<void>` in `credential-store.ts` with **index-first ordering** (matches `storeEnvVar` ordering at [credential-store.ts:50-51](../vscode/mcp-gateway-dashboard/src/credential-store.ts#L50)):
  - Validate both names.
  - `await this._chainIndexMutation(async () => { ... })` — three steps inside:
    - **STEP 1** Index points at newName FIRST: `index.servers[newName] = { env: [...entry.env], headers: [...entry.headers] }; await this._setIndex(index);`
    - **STEP 2** Copy each secret from old key to new key (one `await secrets.get` + `await secrets.store` per key, undefined-safe).
    - **STEP 3** Delete each old secret + remove old index entry: `delete index.servers[oldName]; await this._setIndex(index);`.
  - Crash-mid-rename leaves `{newName: entry-shape}` in index — `reconcile()` can prune incomplete entries on next call (verified by Test 16b).
- [x] **T2.4 (F-ARCH-2 — option (a) chosen; F-SPEC-2 corrected assertion):** Test 17 — `credential-store.test.ts — renameServerCredentials race + stranded-index-detection`. Setup: pre-populate index with `{ctx7: {env:[K1,K2], headers:[]}}`. Spawn two concurrent operations: (a) `storeEnvVar('ctx7', 'K3', 'v')`, (b) `renameServerCredentials('ctx7', 'ctx8')`. Force ordering via mock-call-order recorder so the `storeEnvVar` chain task runs AFTER the `renameServerCredentials` chain task completes (post-rename `_addToIndex('ctx7','env','K3')` resurrects the `ctx7` index entry per [credential-store.ts:232-234](../vscode/mcp-gateway-dashboard/src/credential-store.ts#L232)). Assert final state: index = `{ctx8: {env:[K1,K2], headers:[]}, ctx7: {env:[K3], headers:[]}}` (ctx7 resurrected by post-rename storeEnvVar); secrets exist for `mcpGateway/ctx8/env/K1`, `mcpGateway/ctx8/env/K2`, `mcpGateway/ctx7/env/K3`. **Then call `reconcile()` and assert the ctx7 index entry is NOT pruned** — secret K3 is still present, so reconcile (`_reconcileLocked`, [credential-store.ts:134-179](../vscode/mcp-gateway-dashboard/src/credential-store.ts#L134)) cannot identify it as logically orphaned (it only prunes index entries whose secrets are missing, not entries whose secrets are stale-but-present). Document in REVIEW: stranded ctx7 index entry persists after rename if a concurrent `storeEnvVar` resurrects it — manual cleanup via a future `auditOrphanSecrets` command is the documented mitigation path. **Do NOT assert reconcile prunes the entry — that assertion would be incorrect.**

  **Decision rationale (F-ARCH-2):** Option (a) chosen over option (b) because (1) the orphan-detection assertion makes the existing race observable as a test-recorded behavior rather than waiting for a future audit, (2) the alternative (audit storeEnvVar/storeHeader to move secrets.store inside _chainIndexMutation) is a wider refactor that touches the public contract of credential-store and could destabilize Phase 11 of audit-dashboard track which already shipped async token caching changes, (3) keeping the test ensures any future move of secrets.store inside _chainIndexMutation is validated against this race semantics.
- [x] **T2.5 (ACTUALIZED 2026-05-11 — uses T2.0 knob):** Test 16b — `credential-store.test.ts — crash mid-rename → reconcile recoverable`. Use the T2.0 `failAfterNStores(1, new Error('SecretStorage unavailable'))` knob to make `secrets.store` throw after the first key copied; the `_chainIndexMutation` callback throws mid-Step-2; assert: index has `{newName: entry-shape}` (Step 1 committed), one secret partially migrated, `_setIndex` for cleanup not invoked. Then call `reconcile()`; assert recovery — orphan-secret keys remain (reconcile doesn't auto-prune secrets without index entries — documented limitation), but index is consistent and no double-entry exists.
- [x] **T2.6:** Test 14 — `gateway-client.test.ts — patchServer with new_name`: http stub records `PATCH /api/v1/servers/ctx7` with body `{new_name: "ctx8"}` and `Authorization` header from `buildAuthHeader()`; response shape `{status, old_name, new_name}` parsed correctly.
- [x] **T2.7:** Test 15 — `credential-store.test.ts — renameServerCredentials migrates env+header`: pre-populated index + secrets; rename ctx7 → ctx8; assert all secrets moved from old to new key, old keys deleted, index updated; **assert index updated to point at newName BEFORE first `secrets.store` call** (verifies STEP 1 ordering — captured via mock-call-order recorder).
- [x] **T2.8:** Test 16 — `credential-store.test.ts — renameServerCredentials handles missing entry`: rename a server not in index → early return, no error, no secret operations recorded.
- [x] **T2.9:** Test 18 — `credential-store.test.ts — listServerCredentials`: returns env+header arrays for known server; empty `{env:[], headers:[]}` for unknown server.
- [x] **T2.10:** Run `npm run compile && npm test -- --grep "credential-store|gateway-client"` in `vscode/mcp-gateway-dashboard/` — zero failures in scoped suite; baseline-flaky failures (LogViewer/GatewayClient timeouts) untouched.

**Rollback (Phase 2):**

`git revert` of the Phase 2 commit. Two files changed (`gateway-client.ts`, `credential-store.ts`); `gateway-client.patchServer` signature change is **additive** (new optional fields), so existing callers compile and run unchanged. `credential-store.renameServerCredentials` and `listServerCredentials` are net-new methods; revert deletes them. No SecretStorage data migration is run during Phase 2 — the rename method is only called from Phase 3 UI handler. Therefore reverting Phase 2 in isolation does not strand any extension secret state.

- [x] GATE: tests + codereview + thinkdeep — zero errors (any finding at or above CLAUDE_GATE_MIN_BLOCKING_SEVERITY; default: any finding) — **PASSED 2026-05-12** PAL gpt-5.1-codex gate_mode=true (codereview + thinkdeep both PASS 0 findings); 55/55 scoped tests + 1089/1 full suite (1 pre-existing daemon.test.ts:808 live-daemon unrelated). Real-boundary evidence (Inv-4A index-first ordering CAS) in `docs/REVIEW-server-rename.md`. Pipeline `execute-5a8e2d1d` cancelled at step 5 (async CV-gate redundant with direct PAL).

### Phase 3 — TS Extension UI

**Goal:** Wire the rename command + context menu, implement the `extension.ts` handler with input box + modal confirm + credentials-failure UX, and add 7 UI/command tests.

**Tasks:**

- [x] **T3.1:** Update [vscode/mcp-gateway-dashboard/package.json](../vscode/mcp-gateway-dashboard/package.json):
  - Add command entry `{"command":"mcpGateway.renameServer","title":"Rename Server","icon":"$(edit)"}`.
  - Add `view/item/context` menu entry under `menus`: `{"command":"mcpGateway.renameServer","when":"view == mcpBackends && viewItem =~ /^(running|stopped|degraded|error|disabled|starting|restarting)$/","group":"1_modification@1"}`. The `viewItem` regex deliberately excludes SAP server contextValues (those use `sap-component`, `sap-system`, etc. naming, not the lifecycle status set).
- [x] **T3.2:** Implement `mcpGateway.renameServer` handler in [vscode/mcp-gateway-dashboard/src/extension.ts](../vscode/mcp-gateway-dashboard/src/extension.ts) per spike §"TypeScript extension changes" §4. Use the **already-exported** `parseSapServerName` from [vscode/mcp-gateway-dashboard/src/sap-detector.ts:40](../vscode/mcp-gateway-dashboard/src/sap-detector.ts#L40) (NOT module-private regex constants). Input box with `validateInput` rejecting empty, unchanged, format-invalid (`SERVER_NAME_RE`), or SAP-shaped values. After confirm modal with "preserves" summary (env count + header count + secret count via `listServerCredentials`), call `client.patchServer(oldName, {new_name})`. On gateway success, call `credentialStore.renameServerCredentials(oldName, newName)`. On `renameServerCredentials` failure: warning toast `Server renamed to "{newName}" but {N} credential(s) could not be migrated. They remain under "{oldName}" in the keychain. Re-import KeePass or re-enter them manually.`. Always `await cache.refresh()` afterwards.
- [x] **T3.3:** Test 19 — `commands.test.ts — mcpGateway.renameServer happy path`: stub `BackendItem` with `server.name='ctx7'`; input box returns `'ctx8'`; modal returns `'Rename'`; assert `client.patchServer('ctx7', {new_name:'ctx8'})` + `credentialStore.renameServerCredentials('ctx7','ctx8')` + `cache.refresh()` all fired in order; success info toast shown.
- [x] **T3.4:** Test 20 — `commands.test.ts — mcpGateway.renameServer rejects SAP name`: stub `BackendItem` with `server.name='vsp-DEV'`; assert error toast `'Renaming SAP servers is not supported.'`; no `patchServer` call.
- [x] **T3.5:** Test 21 — `commands.test.ts — mcpGateway.renameServer cancel input`: input box returns `undefined` (ESC); no `patchServer` call; no toast.
- [x] **T3.6:** Test 22 — `commands.test.ts — mcpGateway.renameServer cancel confirm`: input box returns `'ctx8'`; modal returns `undefined` (ESC); no `patchServer` call.
- [x] **T3.7:** Test 23 — `commands.test.ts — mcpGateway.renameServer API failure`: `client.patchServer` rejects with `GatewayError({kind:'http',message:'409 already exists'})`; assert error toast `'Rename failed: 409 already exists'`; `cache.refresh()` NOT fired (since rename did not happen).
- [x] **T3.8:** Test 24 — `commands.test.ts — gateway PATCH succeeds, credential migration fails`: `client.patchServer` resolves; `credentialStore.renameServerCredentials` rejects with `Error('SecretStorage unavailable')`; assert warning toast wording matches T3.2 spec exactly (count from `item.server.env_keys + header_keys`); assert `cache.refresh()` fired (server-side rename succeeded); rename NOT rolled back gateway-side.
- [x] **T3.9:** Test 19b — `commands.test.ts — input box validateInput rejects bad name`: simulate `validateInput('bad name with spaces')` → returns `'Invalid name (1-64 chars: a-z, A-Z, 0-9, -, _)'`. Simulate `validateInput('vsp-XYZ')` → returns SAP rejection wording. Simulate `validateInput(oldName)` → returns `null` (unchanged passes through to early-return).
- [x] **T3.10:** Run `npm run compile && npm test` in `vscode/mcp-gateway-dashboard/` — full extension suite passes; **zero new regressions**; baseline-flaky failures unchanged.
- [x] **T3.11 (F-SPEC-3):** `npm run deploy` — auto-version bump → build → package VSIX → install to local VSCode. Stage rebuilt VSIX binary alongside source changes. Single commit must bundle source + VSIX (per CLAUDE.md VSCode Extension Build Discipline). **After commit succeeds, instruct the operator to run `Developer: Reload Window` in VSCode** so the newly installed VSIX activates — without this, the rename context-menu item will not appear despite a successful install.

**Rollback (Phase 3):**

`git revert` of the Phase 3 commit reverts package.json (command + menu removed → command palette no longer exposes Rename Server), extension.ts handler (the `registerCommand` block deleted), and the bundled VSIX. After revert, operator runs `Developer: Reload Window` to deactivate the old VSIX. The credential-store and gateway-client methods from Phase 2 remain on disk but unreachable from the UI — operators cannot trigger rename via the extension. CLI / REST callers can still call `PATCH new_name` directly (Phase 1 not reverted), so a second revert of Phase 1 is required if the entire feature must be withdrawn from the gateway too.

- [x] GATE: tests + codereview + thinkdeep — zero errors (any finding at or above CLAUDE_GATE_MIN_BLOCKING_SEVERITY; default: any finding) — **PASSED 2026-05-12** PAL gpt-5.1-codex gate_mode=true (codereview + thinkdeep both PASS 0 findings); 92 tests pass in commands.test.js compiled scoped suite (T3.3..T3.9 verified); VSIX rebuilt + installed via `npm run deploy`. Real-boundary evidence in `docs/REVIEW-server-rename.md`. Operator must run `Developer: Reload Window` in VSCode after commit to activate the new VSIX.

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
- [ ] **T4.3 (RE-ACTUALIZED 2026-05-11 by /check pipeline `checkpoint-check-e6e30eef` — F-ARCH-A1 HIGH):** CHANGELOG.md entry under the next minor — **target version `v1.34.0`** (extension currently `1.33.1` per [vscode/mcp-gateway-dashboard/package.json:5](../vscode/mcp-gateway-dashboard/package.json#L5); previous anchor `v1.33.0` was set when extension was at 1.32.0, but commit `1c3f130` audit-e7618c9c closeout bumped to 1.33.1 between actualization and /check). Earlier plan-internal references to "v1.6.0" / "v1.7.0" are stale (those were the gateway-side numbers from Phase 16-17, not the post-Wave-2 extension semver). **F-ARCH-A3 INFO carry:** also add a missing `[Extension 1.33.0]` and `[Extension 1.33.1]` CHANGELOG row when authoring the v1.34.0 entry — neither was added when commit `1c3f130` shipped. Sections: **Added** (rename via PATCH new_name + extension Rename Server command), **Security** (SAP refusal via `sapname.IsSAP` codegen on both sides + index-first credential migration), **Known limitations** (orphan secrets after credential-migration failure require manual cleanup; documented via tracker `v17-rename-orphan-audit` reference).
- [ ] **T4.4 (ACTUALIZED 2026-05-11):** ROADMAP.md update — promote the existing "Server Rename Feature Track (Drafted)" section from `Drafted` to `Released` with all 4 phases marked complete with commit hashes after Phase 4 commits. Note in the section header that the actualization on 2026-05-11 retired the hand-rolled `internal/api/sap.go` in favor of `mcp-gateway/internal/sapname` codegen reuse (per R-21).
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

- **PLAN-server-rename.md** (this file) — phase + gate definitions
- **TASKS-server-rename.md** — flat task checklist (companion file)
- **REVIEW-server-rename.md** — accumulating findings with severity, action_taken, and cycle metadata (created by lead-auditor and code-reviewer agents during `/run`)
- **docs/qa/server-rename-smoke.md** — created in T4.1; manual E2E checklist (9 items)
- **docs/spikes/2026-05-05-server-rename.md** — source spike (edited in T1.0 to correct line 22)

## 8. Execution Mode

After operator approval:

```
/run server-rename          # runs Phase 1, halts at GATE 1
/run server-rename 2        # runs 2 phases
/run server-rename all      # runs everything to completion
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

## 11. Actualization log

**2026-05-11 — drift sweep before first `/run`** (this session, by Porfiry [Opus 4.7]):

| # | Change | Rationale | Affected items |
|---|---|---|---|
| 1 | T1.5 dropped hand-rolled `internal/api/sap.go` regex helper. Plan now imports `mcp-gateway/internal/sapname.IsSAP` from existing YAML-grammar codegen ([internal/sapname/grammar_gen.go](../internal/sapname/grammar_gen.go), shipped in commit `85cbebc` as part of sap-picker T-A.2). | Between 2026-05-06 (plan draft) and 2026-05-11, the SAP server-name detector source-of-truth moved to `docs/grammar/sap-server-name.yaml` with Go + TS codegen. R-21 ("never re-introduce a regex literal") + CLAUDE.md "Regex Discipline (MANDATORY)" make a hand-rolled regex a deliberate violation. Codegen is regex-free (string-prefix + char-code-range only). | T1.5 rewritten; T1.4 call-site renamed `isSAPName` → `sapname.IsSAP`; T1.0 spike-edit target #2 reframed (delete §3 SAP helper code block, replace with reuse note); T1.23 dropped `TestIsSAPName` (covered by `internal/sapname/grammar_gen_test.go`), replaced with thin "rename refusal uses sapname package" assertion |
| 2 | T1.0 verify-step (3) — `MockSecretStorage` confirmed present ([vscode/mcp-gateway-dashboard/src/test/mock-vscode.ts:242](../vscode/mcp-gateway-dashboard/src/test/mock-vscode.ts#L242)), no helper file needed. Limitation: no failure-injection knob → addressed by net-new T2.0 (~10 LOC extension). | The 2026-05-06 draft warned of +2-3h overrun if SecretStorage mock helpers were missing. They aren't missing — the search path was wrong (looked in `src/test/helpers/`, mock lives in `src/test/mock-vscode.ts`). | T1.0 verify-step (3) rewritten; T2.0 added; T2.5 updated to use the T2.0 knob |
| 3 | T1.0b (NEW) — cross-spike sanity-check task before T1.3 RebuildTools wiring, referencing two spikes that landed 2026-05-08 and 2026-05-09. | The 2026-05-06 draft was unaware of (a) the routing-bypass cleanup in `2026-05-08-mcp-server-routing-bypasses.md` (now all MCP traffic flows through the gateway, so RebuildTools is the single propagation channel); (b) the reflector-coordination clarification in `2026-05-09-reflector-coordination.md` (the `~/.claude.json` propagation channel is the TS-side `claude-config-sync.ts` reflector, not a phantom Go-side reflector). | T1.0b inserted between T1.0 and T1.1; manual E2E item 9 (T4.1) confirmed correct as written |
| 4 | T4.3 CHANGELOG version anchor pinned to **v1.33.0** (extension was at 1.32.0 per package.json, shipped via Wave 2 commit `54cd911`). Earlier plan-internal references to "v1.6.0/v1.7.0" era retired. **RE-ACTUALIZED same day** by /check pipeline `checkpoint-check-e6e30eef` finding F-ARCH-A1 HIGH: between this actualization and the /check, commit `1c3f130` (audit-e7618c9c closeout) bumped the extension to **v1.33.1** — so the v1.33.0 anchor was already retroactively stale. Final target is **v1.34.0** with currently=1.33.1. | The plan was drafted before Wave 1 + Wave 2 of the sap-picker-and-import-mcp track shipped (commits `4656a6d..1f2a650`). The CHANGELOG / VSIX target version moved on. | T4.3 updated; T4.4 ROADMAP-update task references v1.34.0 |
| 5 | DEBUG-INSTR investigation closure noted explicitly: spike `2026-05-09-dashboard-kills-gateway-closure.md` confirms root cause = MCPR.3 two-tier auth, safety net = B-NEW-32 daemon supervisor (v1.30.0). Working tree was polluted with DEBUG-INSTR markers on 2026-05-06 (blocked the first `/run` attempt) — that pollution was resolved between sessions and is no longer a precondition. | Pre-flight scope check from earlier session is now a no-op. Plan does not need any shutdown-related changes. | Pre-flight section in `/run` invocation no longer blocks |
| — | **Plan-level facts updated:** Total LOC 990 → ~940; Total tests 34 → 33 (kept the +1 from T1.23 reframing as one positive + one negative case); Expected duration 16h → ~14.5h. | Direct consequence of items 1 + 2 above. | §2 plan-level facts table; §5 phase-breakdown table |

**Underlying surfaces NOT changed since 2026-05-06** (verified 2026-05-11): `models.ServerPatch` shape, `handlePatchServer` body and validation order, `gateway-client.ts::patchServer({ disabled?: boolean })` signature, `credential-store.ts::_chainIndexMutation` pattern. Phases 1/2/3/4 task bodies remain valid as written, modulo the 5 corrections above.

**No re-audit triggered** — corrections (1)-(5) are scope-tightening (replace stale code spec with reuse of newer canonical helpers) and version-anchor freshening, not behavior changes. The 17 architect/lead-audit/specialist findings closed in pipeline `planning-19b7b15b` (2026-05-06) remain Fixed; F-ARCH-7 + F-SPEC-1 are *more thoroughly* addressed by item (1) than by the 2026-05-06 resolution.

---

## Next Plans

Sourced from [docs/ROADMAP.md](ROADMAP.md):

1. **Audit-dashboard track Phase 12 (final phase)** — closing v1.9.1 audit-dashboard track. Phases 9, 10, 11 are landed (per `project_phase_audit_dashboard_phase{9,10,11}.md` memories); Phase 12 is the final closure phase per `docs/PLAN-audit-dashboard.md`. Natural successor because rename feature is independent of audit-dashboard remediation but shares the same VSCode extension testing infrastructure (which Phase 2/3 of this plan exercises).

2. **Debug-flicker.3 + debug-flicker.4B + debug-flicker.Final** (Phase 4-equivalent of `docs/PLAN-debug-flicker.md`) — install Claude Code plugin end-to-end + wire Activate-for-CC button to `mcp-ctl install-claude-code` with webview log streaming + final commit bundling. Phase 4B already landed per `project_debug_flicker_phase4B.md`; only `.3` (operator steps) and `.Final` (deploy commit) remain.

3. **v16-1: Hash-augmented slash-command marker** (post-v1.5.0 candidate). Tolerate operator edits below line 1 of generated `.claude/commands/<server>.md` files via per-entry content hash on the magic-header marker. Currently the marker is regenerated in full on each server `running` transition; below-line-1 edits are silently overwritten. Natural follow-up because rename feature exercises slash-command file rename (old `ctx7.md` deleted, new `context7-prod.md` created) which is the same surface area.

