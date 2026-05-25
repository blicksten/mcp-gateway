# PAL re-review round 2 — 2026-05-24 (post-fix verification)

**Tool route:** `pal-mcp__chat` via `mcp__mcp-gateway__gateway_invoke`
**Note:** backend renamed from `test-pal-mcp` → `pal-mcp` mid-cycle (daemon restart at 16:48; new uptime 114s); prior continuation_id `c4760c0b-...` was lost. Round 2 done via `chat` (single-shot) instead of multi-step `codereview` (round 1 codereview timed out under sync 300s limit).
**Expert model:** gpt-5.1-codex (OpenAI)
**Mode:** thinking_mode=medium
**Continuation ID (round 2):** 35b82b1d-47d8-4c58-9813-a4b54ed310bc

## Verdict: **BLESSING**

All 4 HIGH + 6 MEDIUM + 5 LOW from round 1 resolved, plus 1 NEW HIGH (self-identified during round 2 prep — "07-verdict-check trivially passes a skeleton run") fixed in-cycle. PAL confirms no further HALT-level findings; 3 actionable advisories returned, all non-blocking.

## Round-1 finding closure status

| ID | Finding | Resolution |
|---|---|---|
| **HIGH-A** | Ritualization | `INSTRUCTIONS.md` §10 = enforced triage table; `07-verdict-check.ps1` exits non-zero if any finding lacks Decision/Justification |
| **HIGH-H1** | Fabricated claims paths | `claims.yaml` rewritten — grep-verified paths (`internal/auth/admin.go:39`, `internal/api/resumable_session_spike.go:82`, `internal/api/claude_code_handlers.go:252+`, `internal/proxy/gateway.go:172/245/296/339`, `vscode/.../credential-store.ts:148`); cross-project (MCPR.0, FM-29) removed |
| **HIGH-H3** | No SKELETON guard | `run-all.ps1` reads each step's `meta.status`; matches `SKELETON|DEFERRED`; sets `manifest.skeleton_run=true`; exits 2 unless `-AllowSkeleton` |
| **HIGH-D** | 01-inventory regex provably wrong | `01-inventory.ps1` no longer produces wrong inventory; emits `SKELETON_DEFERRED` with empty function lists |
| **HIGH-NEW** | 07-verdict-check trivially passes skeleton run | `07-verdict-check.ps1` reads `manifest.skeleton_run`; exits 3 when true |
| MED-B | RTM staleness | `INSTRUCTIONS.md` §6 documents `RTM_STALE` flag; 06-drift impl in Phase 0 |
| MED-C | Observability axis | `evidence_path` mandatory per scenario; `07-verdict-check` verifies presence |
| MED-E | Tool-version drift | `00-prereq-check.ps1` writes `manifest.tool_versions`; UTF-8 no BOM |
| MED-F | Manual artifact capture | `operator-manual-checklist.md` has explicit Evidence requirement blocks |
| MED-H2 | `Invoke-Expression` | Replaced with `Get-Command` + `&` operator; presence-only deadcode probe |
| MED-M5 | Missing crash/restart scenarios | 4 new: `scn-backend-crash-mid-session`, `scn-daemon-restart-during-invoke`, `scn-orphan-cleanup-during-active-session`, `scn-patchstate-concurrent-enforce` |
| LOW | PROVEN caveat | Embedded inline in verdict.md template |
| LOW | UTF-8 BOM | `[System.IO.File]::WriteAllText` with `UTF8Encoding(false)` |
| LOW | `git_head_*` not populated | `00-prereq-check` + `run-all.ps1` populate manifest |
| LOW | §14 references missing CI | Marked `not_yet_implemented` inline |
| LOW | `06-drift` exits 1 on missing predecessor | Now exits 0 with warning |
| LOW | dead `NOT_IMPLEMENTED` regex | Removed; only `SKELETON|DEFERRED` literals |
| **NEW-MED** | regex `F-\d{3,}` too strict | Relaxed to `F-\d+` |
| **NEW-MED** | claims.yaml `impl_line=null` for 4 of 10 | Documented; 03-doc-diff Phase 1.5 will require operator to populate |

## PAL Q&A round 2 (gpt-5.1-codex, in original Russian)

**Q1.** Does the 4th axis (Correctness) genuinely close the gap or just rename it onto possibly-empty operator-prose assertions? Single added check?

> "Добавленный «Correctness»-контур закрывает главный пробел только если «assertions» реально проверяются. Сейчас сценарий может формально пройти с пустым блоком (`assertions: []`) или бессодержательным `expect: ???`. Минимальный доп.контроль: lint-проход, который парсит каждый сценарий, убеждается, что `assertions` содержит ≥1 записи и что каждая запись указывает и на `expect`/`method` (или аналог) и на валидный `evidence_path`."

**Action applied this cycle:** new `scripts/04a-scenarios-lint.ps1` sequenced between `03-doc-diff` and `04-scenarios`. Parses scenarios.md, verifies each `## scn-*` block has both substantive `**assertions:**` (≥1 non-TODO bullet) and non-empty `**evidence_path:**`. Exits non-zero on violation.

**Q2.** Move triage from markdown table to structured YAML for v1.1?

> "Для v1.1 markdown-таблица приемлема... Главное — держать регексы и колоноконсистентность под тестами. Если ожидания по масштабу аудитов вырастут, перейти на `outputs/verdict.yaml` (источник истины) + автогенерацию markdown сводки действительно снизит хрупкость... Но это можно оставить в бэклоге до 1.2; нынешний risk surface локализован, и новый скрипт его покрывает."

**Action:** deferred to v1.2 backlog. Documented in this artifact.

**Q3.** Anything load-bearing missed that would HALT Phase 0?

> "(a) нет явной проверки, что `manifest.axes` включает новую Correctness-ось и что каждый шаг заполняет оценку по всем осям; (b) `run-all.ps1` skeleton-флаг не блокирует downstream шаги, если оператор вручную пропустит guard (намеренно или по ошибке). Стоит добавить финальный агрегатор, который сверяет `manifest.skeleton_run` и отказывает любому `outputs/*` постфактум. Остальное (claims, triage, prereq hardening) выглядит консистентным; явных HALT-поводов не увидел."

**Status:**
- (a) Q3a — `manifest.axes` field added in this cycle; per-step axis tagging deferred to Phase 0 (currently `05-gap.ps1` will emit per-function 4-axis matrix once implemented).
- (b) Q3b — `07-verdict-check.ps1` already directly reads `manifest.skeleton_run` (does NOT rely on run-all's exit code). If operator manually flips manifest field, that's social-engineering against the tool which is out of scope for v1.1. v1.2 backlog: add manifest signature/hash to detect post-hoc mutation.

## v1.2 backlog (PAL-flagged, deferred)

- Move triage from markdown table to `outputs/verdict.yaml` with auto-generated markdown summary
- Add per-step axis tagging in `manifest.json` (axis_status per step)
- Add manifest hash/signature to detect post-hoc mutation
- Consider migrating `scenarios.md` and `claims.yaml` to structured YAML with JSON Schema validation
- Implement `03-doc-diff.ps1` flag for `impl_line=null` requiring operator population

## Process note (PAL access through this cycle)

- Round 1 (initial review): `test-pal-mcp__codereview` via `gateway_invoke` — succeeded, returned HALT verdict with 15 findings
- Round 2 step 1 (codereview re-call with continuation): succeeded, paused for code reading
- Round 2 step 2 (codereview with full findings): timed out at 300s (sync MCP limit; orchestrator async wrapper unavailable in this session)
- Round 2 retry (chat with corrected backend `pal-mcp` post-restart): succeeded, returned BLESSING

**Lesson:** for future PAL gates in this packet, prefer `chat` for blessing-style queries (single-shot, sub-300s) and reserve full `codereview` workflow for first-pass deep audits. Or use the orchestrator async wrapper (`queue_review`) when orchestrator MCP is available.
