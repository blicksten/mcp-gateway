# TASKS — Server Rename Feature

**Companion to:** [docs/PLAN-docs-spikes-2026.md](PLAN-docs-spikes-2026.md)
**Source spike:** [docs/spikes/2026-05-05-server-rename.md](spikes/2026-05-05-server-rename.md)
**Created:** 2026-05-06 by Porfiry [Opus 4.7]

---

## Phase 1 — Go API

- [ ] T1.0: (F-ARCH-1, F-ARCH-6, F-SPEC-1 prerequisite) Update spike `docs/spikes/2026-05-05-server-rename.md` THREE targets: (1) **line 27** (`Between region (a) and (b)` bullet — NOT line 22) atomicity claim → `Bounded to lm.Stop duration (up to ~9s for stdio, ~2s for HTTP/SSE)`; (2) **lines 263-264** sap.go regex code block → use non-capturing groups `(?:-\d{3})?` (parity with PLAN T1.5 + F-ARCH-7 resolution); (3) verify lm-error-injection helpers in `internal/api/server_test.go` + SecretStorage mock helpers in `vscode/mcp-gateway-dashboard/src/test/helpers/` exist (else +2-3h)
- [ ] T1.1: Add `NewName *string \`json:"new_name,omitempty"\`` to `models.ServerPatch` in `internal/models/types.go`
- [ ] T1.2: Confirm existing `models.ValidateServerName` invocation produces 400 with existing wording on `*patch.NewName`
- [ ] T1.3: Implement `handlePatchServer` rename branch (Plan A: lm.AddServer → lm.RemoveServer → cfg-mutation → auto-start warn-only → RebuildTools + TriggerPluginRegen → 200 with `{status,old_name,new_name}`)
- [ ] T1.4: Add SAP refusal pre-check (only when `patch.NewName != nil`) → 400 with body `"renaming SAP-named servers is not supported"`
- [ ] T1.5: (F-ARCH-7) Create `internal/api/sap.go` with `sapVSPRe` and `sapGUIRe` using non-capturing `(?:-\d{3})?` group + `isSAPName(name)` helper
- [ ] T1.6: Test 1 `TestPatchServer_Rename_Success` — name swap in cfg + lm; env/headers preserved; auto-start under new name
- [ ] T1.7: Test 2 `TestPatchServer_Rename_NameCollision` — 409 if new_name exists in cfg
- [ ] T1.8: Test 3 `TestPatchServer_Rename_InvalidName` — 400 if new_name fails ValidateServerName
- [ ] T1.9: Test 4 `TestPatchServer_Rename_NotFound` — 404 if old name absent
- [ ] T1.10: Test 5 + Test 6 `TestPatchServer_Rename_SAPRefused_Old/_New` — 400 SAP refusal both directions
- [ ] T1.11: Test 6b `TestPatchServer_Rename_SAPBeatsBadEnv` — proves validation order step 2 short-circuits step 4
- [ ] T1.12: Test 7 `TestPatchServer_Rename_RollbackOnRemoveFailure` — rollback fires; final lm has only `name`
- [ ] T1.13: Test 7b `TestPatchServer_Rename_RollbackOfRollbackErrorLogged` — both errors logged; HTTP 500
- [ ] T1.14: Test 7c `TestPatchServer_Rename_StartFailWarnsNotRollback` — 200 OK + warn log; no rollback (parity with handleAddServer)
- [ ] T1.15: Test 7d `TestPatchServer_Rename_BadEnvShortCircuits` — 400 BEFORE state mutation
- [ ] T1.16: Test 7e `TestPatchServer_Rename_PluginRegenFailureSwallowed` — 200 OK + captured logger has failure entry
- [ ] T1.17: (F-ARCH-4) Test 7f `TestPatchServer_Rename_StopTimedOutSilentZombie` — handler treats `lm.RemoveServer=nil + Stop timeout` as success (regression guard for inherited LOW risk)
- [ ] T1.18: Test 8 `TestPatchServer_Rename_PreservesEnv` — env values intact after rename
- [ ] T1.19: Test 9 `TestPatchServer_Rename_CombinedWithEnvDelta` — combined rename+env atomic; both sub-cases (success + step-2-fail)
- [ ] T1.20: Test 10 `TestPatchServer_Rename_DisabledFlag` — disabled server: no auto-start
- [ ] T1.21: (F-ARCH-8) Test 11 `TestPatchServer_RenameNoOp_SameName` — response body == `{"status":"updated"}` when name == new_name
- [ ] T1.22: Tests 12 + 12b + 12c — RebuildTools called; nil-gateway no panic; env-only PATCH does NOT call RebuildTools
- [ ] T1.23: Test 13 `TestIsSAPName` — positive + negative cases (incl. `vsp-DEV-1000` 4-digit suffix rejection)
- [ ] T1.24: Run `go test ./...` + `go vet ./...` + `go build ./...` — zero failures; quote test count into GATE evidence
- [ ] GATE: tests + codereview + thinkdeep — zero errors (any finding at or above CLAUDE_GATE_MIN_BLOCKING_SEVERITY; default: any finding)

## Phase 2 — TS Extension Client

- [ ] T2.1: Update `gateway-client.ts::patchServer` signature to accept new optional `new_name?: string` field; backward-compatible
- [ ] T2.2: Implement `credential-store.ts::listServerCredentials(server)` returning `{env, headers}` shallow copies; empty for unknown
- [ ] T2.3: Implement `credential-store.ts::renameServerCredentials(oldName, newName)` with index-first ordering inside `_chainIndexMutation`
- [ ] T2.4: (F-ARCH-2 option a; F-SPEC-2 corrected) Test 17 — concurrent storeEnvVar(old) + renameServerCredentials race; assert ctx7 entry IS resurrected by post-rename storeEnvVar AND that reconcile() does NOT prune it (stranded-index, not orphan-secret); cleanup deferred to v17-rename-orphan-audit
- [ ] T2.5: Test 16b `crash mid-rename → reconcile recoverable` — secrets.store throws after first key copied; reconcile leaves consistent index
- [ ] T2.6: Test 14 `gateway-client.test.ts patchServer with new_name` — http stub records body shape + auth header; response parses `{status,old_name,new_name}`
- [ ] T2.7: Test 15 `renameServerCredentials migrates env+header` — assert index points at newName BEFORE first secrets.store call (mock-call-order recorder)
- [ ] T2.8: Test 16 `renameServerCredentials handles missing entry` — early return, no error
- [ ] T2.9: Test 18 `listServerCredentials` — known + unknown server cases
- [ ] T2.10: `npm run compile && npm test -- --grep "credential-store|gateway-client"` — zero failures in scoped suite
- [ ] GATE: tests + codereview + thinkdeep — zero errors (any finding at or above CLAUDE_GATE_MIN_BLOCKING_SEVERITY; default: any finding)

## Phase 3 — TS Extension UI

- [ ] T3.1: Update `package.json` — add `mcpGateway.renameServer` command + `view/item/context` menu entry gated on lifecycle-status `viewItem` regex (excludes SAP)
- [ ] T3.2: Implement `mcpGateway.renameServer` handler in `extension.ts` — input box + parseSapServerName guards + modal confirm with preserves summary + patchServer + renameServerCredentials + warning toast on creds-failure
- [ ] T3.3: Test 19 `renameServer happy path` — patchServer + renameServerCredentials + cache.refresh fired in order
- [ ] T3.4: Test 20 `renameServer rejects SAP name` — error toast, no API call
- [ ] T3.5: Test 21 `renameServer cancel input` — no API call, no toast
- [ ] T3.6: Test 22 `renameServer cancel confirm` — no API call
- [ ] T3.7: Test 23 `renameServer API failure` — error toast; cache.refresh NOT fired
- [ ] T3.8: Test 24 `gateway success + creds-failure` — exact warning toast wording; cache.refresh fired; no rollback
- [ ] T3.9: Test 19b `validateInput rejects bad name + SAP-shaped + unchanged passes through`
- [ ] T3.10: `npm run compile && npm test` in `vscode/mcp-gateway-dashboard/` — full suite passes; zero new regressions
- [ ] T3.11: (F-SPEC-3) `npm run deploy` — version bump + build + VSIX + install; stage VSIX + source in single commit; **after commit, instruct operator to run `Developer: Reload Window`**
- [ ] GATE: tests + codereview + thinkdeep — zero errors (any finding at or above CLAUDE_GATE_MIN_BLOCKING_SEVERITY; default: any finding)

## Phase 4 — Documentation + manual E2E + VSIX deploy + commit + push

- [ ] T4.1: (F-ARCH-3 — 9 items) Manual E2E checklist at `docs/qa/server-rename-smoke.md`:
  - [ ] (1) Rename ctx7 (no creds) → context7-prod; tree updates; commands/*.md renamed
  - [ ] (2) Rename WITH creds; restartServer works under new name
  - [ ] (3) Rename → ESC at confirm; no rename
  - [ ] (4) Rename to existing name; 409 error toast
  - [ ] (5) Rename SAP `vsp-DEV`; button hidden + palette-invoked error toast
  - [ ] (6) Combined rename + env update via API; both applied
  - [ ] (7) **NEW** Plan A rollback UX — kill daemon mid-Step-2; toast surfaces "rolled back"; cfg/lm consistent on restart
  - [ ] (8) **NEW** Credential-migration failure UX — degraded SecretStorage; warning toast wording verified; secrets queryable under old name
  - [ ] (9) **NEW** `~/.claude.json` propagation — old `mcp-gateway:ctx7` removed, `mcp-gateway:ctx8` added; Claude Code FS-watcher picks up; `claude mcp list` reflects new name
- [ ] T4.2: README "Renaming a server" section + Plan A rollback callout + creds-failure callout
- [ ] T4.3: CHANGELOG.md entry — Added (rename) + Security (SAP refusal both sides + index-first migration) + Known limitations (orphan secrets cleanup)
- [ ] T4.4: ROADMAP.md update — new "Server Rename track" with all 4 phases marked complete with commit hashes
- [ ] T4.5: Final security cross-validation — PAL `mcp__pal__codereview` (gpt-5.2-pro, gate_mode=true) on Phases 1–3 changed files; findings at any severity → fix in-cycle
- [ ] T4.6: Commit + push to `origin/main`; bundle source + VSIX + docs in single commit; inspect output per Post-Commit/Push Discipline
- [ ] T4.7: Post-push CI smoke verification (gitleaks + dogfood-smoke + go test + npm test); fix in-cycle on any failure
- [ ] T4.8: Operator hand-off — announce manual E2E (T4.1, 9 items) is operator's portion; failures route to follow-up cycle
- [ ] GATE: tests + codereview + thinkdeep — zero errors (any finding at or above CLAUDE_GATE_MIN_BLOCKING_SEVERITY; default: any finding)

## Follow-up trackers (promoted from architect findings)

- [ ] `v17-rename-orphan-audit` — if T2.4 reconcile() cannot detect orphan-secret-without-index, audit `storeEnvVar` / `storeHeader` to move `secrets.store` inside `_chainIndexMutation` (F-ARCH-2 option (b) deferred from main plan).
- [ ] `v16-rename-rate-limit` — if rename traffic ever spikes (operator scripting), add a per-IP token bucket dedicated to rename calls (F-ARCH-9 carry).
