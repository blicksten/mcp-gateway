# REVIEW — Phase 16 Plan Audit

**Scope:** `docs/PLAN-16.md` + `docs/TASKS-16.md`
**Auditor:** self-audit (lead-auditor + specialist-auditor roles combined due to PAL MCP unavailability during this session — PAL threw `AttributeError` on CV-gate; fallback to rigorous internal critical analysis per CLAUDE.md fallback protocol)
**Audit date:** 2026-04-20
**Prior audit findings addressed:** 1 HIGH (bootstrap gap), 2 MEDIUM (tools/list caching #13646, .md semantic confusion)

## Audit Pass 1 — Findings

### MEDIUM

**M-01 — Pending-action queue loss on gateway restart** (T16.3.2)

In-memory `patchstate.State` holds heartbeats, actions, probes. If gateway daemon restarts between "user adds MCP" and "patch polls `/pending-actions`", the action is lost. Patch still triggers heartbeat on restart-after-gap but never gets the `reload-plugins` command → Claude Code never refreshes → new MCP invisible until user noticed.

**Fix applied:** Add durability requirement to T16.3.2 — persist queue to `~/.mcp-gateway/patch-state.json` on mutation, reload on startup. Include TTL filter (drop actions >10min old on reload).

**M-02 — Concurrent regen race in `RegenerateMCPJSON`** (T16.2.4)

Multiple concurrent `POST /api/v1/servers` calls trigger simultaneous regen. POSIX `rename()` is atomic per-call, so no file corruption, but race on "which writer wins" → observable tool list inconsistent with actual backend set.

**Fix applied:** Add mutex requirement to T16.2.3 (`regen.go` must serialize via internal `sync.Mutex`). Call order = arrival order at gateway, deterministic post-mutex.

**M-03 — Auth token drift between `auth.token` file and plugin userConfig** (T16.2.2 / T16.8.2)

`~/.mcp-gateway/auth.token` is the canonical source. Claude Code plugin stores `auth_token` via userConfig (keychain). On token rotation (gateway regenerates), plugin keychain stale → 401 on every MCP call until user re-enters.

**Fix applied:** Add T16.8.6 (new task) — `mcp-ctl install-claude-code` re-run detects token mismatch and re-registers plugin with fresh token. Document rotation procedure in README §"Rotating the gateway auth token". Add dashboard warning indicator for mismatch (T16.5.5 new failure mode K).

**M-04 — Cross-platform activation-hook dispatch ambiguity** (T16.4.5)

Extension activation hook must run `apply-mcp-gateway.sh` on Unix and `.ps1` on Windows. Task text says "extension runs it" but doesn't specify the exact dispatch logic. On Windows without Git Bash, `.sh` silently fails; extension has no way to know.

**Fix applied:** Extend T16.4.5 with explicit platform dispatch: `process.platform === "win32"` → spawn `powershell.exe -NoProfile -ExecutionPolicy Bypass -File apply-mcp-gateway.ps1`; else → spawn `/bin/sh apply-mcp-gateway.sh`. Add `--auto` flag to both scripts. Fail loudly (write error to dashboard status) if neither works.

### LOW

**L-01 — Streamable sub-path collision analysis missing** (T16.1.5)

Plan says "/mcp/{anything-else} → aggregate". MCP Streamable HTTP protocol is a single POST endpoint by spec (JSON-RPC body). But go-sdk may use sub-paths internally (e.g., for session resumption). Collision with backend name `initialize` or `tools` would break things.

**Fix applied:** Add T16.1.5.a — verify SDK's path usage by reading streamable.go fully before implementation; reject backend names matching a denylist (`initialize`, `tools`, `resources`, `prompts`, etc.) in `SERVER_NAME_RE` if SDK uses them.

**L-02 — CORS preflight OPTIONS handling unspecified** (T16.3.4)

Webview `fetch()` triggers preflight `OPTIONS` request before POST. Must respond 200 with `Access-Control-Allow-*` headers. Gateway's existing chi middleware may not handle OPTIONS on `/api/v1/claude-code/*` routes.

**Fix applied:** Add explicit OPTIONS handler registration to T16.3.4; add test case in T16.3.6 `TestClaudeCodeCORSPreflight`.

**L-03 — Patched `index.js` file-mode not pinned** (T16.4.1)

After `apply-mcp-gateway.sh` writes index.js with inline auth token, file mode is whatever `cp` default produces (likely 644). Any local user can read the token from `~/.vscode/extensions/anthropic.claude-code-*/webview/index.js`.

**Fix applied:** Add T16.4.1.a — `apply-mcp-gateway.sh` `chmod 600` on index.js after write (POSIX); equivalent DACL restrict on Windows via `icacls`. Document in security section.

**L-04 — Dogfood change lacks smoke test** (T16.9.4)

Replacing repo `.mcp.json` affects every developer cloning the repo. No automated check that the gateway actually boots and registers backends from the new config.

**Fix applied:** Add T16.9.4.a — CI job `dogfood-smoke` starts gateway, curl `/api/v1/servers`, asserts all three backends `running`. Runs on PRs that touch `.mcp.json` or `config.json`.

**L-05 — Over-engineered "never touch non-matching entries" in regen** (T16.2.3)

Plugin cache dir is ours by contract. "Defensive coding against user hand-edits" is scope creep — we don't own hand-edits to `~/.claude/plugins/cache/`. Simpler: overwrite whole file, add LOUD banner comment in generated file.

**Fix applied:** Clarify T16.2.3 — regen OWNS the `.mcp.json` file; unconditional atomic overwrite with generated banner `// GENERATED BY mcp-gateway — DO NOT EDIT`. Users editing cache files is not supported.

**L-06 — Token rotation detection UX absent** (T16.5.4)

Dashboard polls patch-status every 10s but no "token file mtime changed since patch applied" check. User rotates token manually → dashboard silent → 401s until user notices.

**Fix applied:** Add T16.5.5.K — new failure mode "Token rotated since patch install" triggered when `auth.token` mtime > patched index.js mtime. Banner: "Click [Reinstall patch] to pick up new token."

### Verification evidence (internal cross-review)

- Read PLAN-16.md lines 1-601 in full (file has 601 lines per audit verification).
- Read TASKS-16.md lines 1-230 for task-ownership alignment with PLAN-16.md.
- Cross-checked each sub-phase (16.0–16.9) for: Rollback subsection (present in all 10), GATE step (present in all 10 incl. with zero-errors phrasing), task numbering (T16.N.X format consistent).
- Verified style consistency with PLAN-v15.md (table-bullet hybrid, Files subsection, Rollback subsection, GATE trailing task).
- Verified dependency graph in TASKS-16.md matches PLAN-16.md phase ordering.

## Audit Pass 1 Verdict

**REJECT** — 4 MEDIUM + 6 LOW findings. Apply fixes, re-audit.

## Audit Pass 2 — Fixes applied

All 10 findings addressed in PLAN-16.md revision:
- M-01 → T16.3.2 extended with durable queue (disk persistence)
- M-02 → T16.2.3 extended with mutex serialization
- M-03 → T16.8.6 new task (token-drift detection) + T16.5.5.K (new failure mode)
- M-04 → T16.4.5 explicit platform dispatch
- L-01 → T16.1.5.a SDK path verification + backend name denylist
- L-02 → T16.3.4 OPTIONS handler + T16.3.6 preflight test
- L-03 → T16.4.1.a chmod 600 / icacls
- L-04 → T16.9.4.a CI dogfood smoke job
- L-05 → T16.2.3 clarified (OWNS file, no partial-merge)
- L-06 → T16.5.5.K token-rotation detection

## Audit Pass 2 Verdict

**APPROVE** — all 10 findings fixed in-cycle. PLAN-16.md ready for execution.

## Session Summary

**What was done:** Phase 16 plan authored by architect with 10 sub-phases, 74 tasks, explicit Rollback + GATE per sub-phase. Lead-auditor + specialist-auditor combined pass (PAL MCP unavailable, internal critical analysis per CLAUDE.md fallback) found 10 findings (4 MEDIUM + 6 LOW). All fixed in-cycle by updating PLAN-16.md and TASKS-16.md. Final verdict APPROVE.

### Findings table

| ID | Severity | Description | Status | Action taken |
|----|----------|-------------|--------|--------------|
| M-01 | MEDIUM | Pending-action queue lost on gateway restart | Fixed | T16.3.2 adds disk-persistence requirement |
| M-02 | MEDIUM | Concurrent regen race (race on "last writer wins") | Fixed | T16.2.3 mutex serialization |
| M-03 | MEDIUM | Auth token drift between file and plugin keychain | Fixed | T16.8.6 drift detection + T16.5.5.K failure mode |
| M-04 | MEDIUM | Activation-hook platform dispatch ambiguous | Fixed | T16.4.5 explicit win32/unix branching |
| L-01 | LOW | SDK streamable sub-path collision risk | Fixed | T16.1.5.a verification + backend-name denylist |
| L-02 | LOW | CORS preflight OPTIONS unspecified | Fixed | T16.3.4 explicit OPTIONS handler + test |
| L-03 | LOW | Patched index.js permissions unpinned | Fixed | T16.4.1.a chmod 600 / icacls |
| L-04 | LOW | Dogfood change lacks smoke test | Fixed | T16.9.4.a CI smoke job |
| L-05 | LOW | Over-engineered partial-merge in regen | Fixed | T16.2.3 OWNS file, full overwrite |
| L-06 | LOW | Token rotation detection UX absent | Fixed | T16.5.5.K new failure mode |

### Manual review table

| Item | Why manual verification needed | Risk if skipped |
|------|-------------------------------|-----------------|
| T16.0 SPIKE result | Fiber-walk pattern depends on Claude Code version — must verify on actual current installation before 16.4 work starts | High (entire phase may rescope) |
| Cross-platform patch smoke test | `apply-mcp-gateway.{sh,ps1}` invoked on actual macOS + Windows machines; no CI matrix for GUI VSCode test | Medium |
| Claude Code marketplace install flow | `claude plugin marketplace add` + `claude plugin install` tested with real CLI (not mocked) | Medium |
| Fresh-clone end-to-end test (acceptance) | From empty VSCode, clone repo, run installer, verify /mcp panel shows gateway entries | Medium |

### Deviation from standard audit protocol

PAL MCP threw `AttributeError: 'dict' object has no attribute 'strip'` on CV-gate invocation during step 1. Per CLAUDE.md "PAL unavailable" fallback, sub-agent cross-model review was attempted but the architect sub-agent hit an Anthropic quota cap mid-execution. Lead-auditor + specialist-auditor roles were therefore consolidated into a single rigorous self-audit with explicit finding documentation (this file). Session memory for Phase 16 captures this fallback decision.

Future re-audit with PAL available (next session) SHOULD cross-check the 10 findings above against `pal__codereview` on PLAN-16.md and TASKS-16.md; any new findings surfaced post-hoc become cycle-3 inputs.

---

## Audit Pass 3 — `/check phase 16` re-verification (2026-04-20)

**Trigger:** User invoked `/check phase 16` after initial /phase commit `bb4dbe4` landed. PAL MCP still unavailable in this session — continuing internal fallback protocol.

### Verification of prior-cycle fixes

All 10 findings from Pass 1 verified present in committed artifacts:

- `[REVIEW-16 M-01]` at PLAN-16:343 (T16.3.2 persistence) ✓
- `[REVIEW-16 M-02]` at PLAN-16:241,255 (T16.2.3 mutex) ✓
- `[REVIEW-16 M-03]` at PLAN-16:682 (T16.8.6) + PLAN-16:506 (failure mode K) ✓
- `[REVIEW-16 M-04]` at PLAN-16:416 (T16.4.5 platform dispatch) ✓
- `[REVIEW-16 L-01]` at PLAN-16:175 (T16.1.5.a SDK path verify) ✓
- `[REVIEW-16 L-02]` at PLAN-16:340,351 (T16.3.4 OPTIONS + test) ✓
- `[REVIEW-16 L-03]` at PLAN-16:392 (T16.4.1.a permissions) ✓
- `[REVIEW-16 L-04]` at PLAN-16:731 (T16.9.4.a dogfood smoke) ✓
- `[REVIEW-16 L-05]` at PLAN-16:248 (T16.2.3 OWNS file) ✓
- `[REVIEW-16 L-06]` at PLAN-16:506 (failure mode K mtime check) ✓

TASKS-16.md has new-task rows for L-01 (T16.1.5.a), L-03 (T16.4.1.a), M-03 (T16.8.6), L-04 (T16.9.4.a). Rollback sections present in all 10 sub-phases (10 matches).

### New findings from /check pass

**N-01 — GATE phrasing non-canonical** (LOW)

PLAN-v15.md (style reference) uses the canonical GATE format from `/phase` skill:
```
- [x] GATE: tests + codereview + thinkdeep — zero errors (any finding at or above CLAUDE_GATE_MIN_BLOCKING_SEVERITY; default: any finding)
```

PLAN-16.md GATE lines are tool-specific (e.g., `T16.1.GATE: go test ./... PASS + go vet ./... clean + mcp__pal__codereview (go files changed) zero errors ...`). MORE specific content is good (operationally useful), but the tool-specific form doesn't start with the standardized `- [ ] GATE: tests + codereview + thinkdeep — zero errors ...` prefix that downstream automation may grep for.

**Decision:** accept with note rather than fix. Rationale: the tool-specific lines ARE the authoritative GATE content; retrofitting a generic prefix would create two parallel statements per sub-phase, inviting drift. The `- [ ]` checkbox + `GATE:` keyword + "zero errors (any finding at or above CLAUDE_GATE_MIN_BLOCKING_SEVERITY; default: any finding)" substring are all present in every GATE line — greppable for the same automation use cases. Phase 17 should confirm this convention works for the tooling; if automation breaks, restructure in a fix-phase.

**Status:** Accepted — not fixed.

**N-02 — Partial traceability in TASKS-16.md** (LOW)

Only findings that ADDED new tasks (L-01, L-03, M-03, L-04) have `[REVIEW-16 <id>]` markers in TASKS-16.md. Findings that EXTENDED existing tasks (M-01 in T16.3.2, M-02 in T16.2.3, L-02 in T16.3.4, L-05 in T16.2.3, L-06 in T16.5.5, M-04 in T16.4.5) have no such markers in the Notes column. A reader auditing from TASKS-16.md alone cannot tell which existing task carries which fix.

**Decision:** fix. Cheap one-liner annotation in Notes column for 6 rows.

**Status:** Fix applied below.

**N-03 — `max_tested` in supported-versions map will rot** (LOW)

T16.4.7 pins `max_tested: "2.5.8"`. If Phase 16 takes 4-6 weeks, Claude Code will ship 2-3 new versions. By the time 16.4 lands, the pinned value is already stale even though compatibility may be fine.

**Decision:** defer to 16.4 implementation. Add task note: "maintainer re-runs spike T16.0 against latest Claude Code before each 16.4 patch release and bumps `max_tested` in one-commit PRs."

**Status:** Will fold into T16.4.7 when phase starts; not a blocker today.

### /check Pass 3 verdict

**APPROVE** — 1 new LOW finding (N-02) fixed in-cycle; 1 accepted with rationale (N-01); 1 deferred to implementation phase with explicit tracking (N-03). No blocking-severity issues.

Committed plan artifacts (`bb4dbe4`) stand as-is for N-01 and N-03. N-02 fix applied as a follow-up amend to TASKS-16.md below.

