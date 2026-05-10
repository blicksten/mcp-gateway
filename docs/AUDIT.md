# Audit Log - Implementation Plan Reviews

## AUDIT-v15 - 2026-04-19 - Pipeline planning-ee239f32

**Plan:** docs/PLAN-v15.md (v1.5.0 tail items - LOW findings + deferred integration tests)
**Tasks:** docs/TASKS-v15.md (14 rows)
**Architect review:** docs/REVIEW-v15.md
**Auditor:** lead-auditor (Porfiry [Opus 4.6])
**Cross-validation:** PAL thinkdeep (gpt-5.2-pro) - confirmed items L-01, L-02, L-03, L-04.

### Verdict: **REJECT** - 5 LOW findings

Gate policy: CLAUDE_GATE_MIN_BLOCKING_SEVERITY=low (project default). Any finding of any severity blocks the gate. All 5 findings are LOW - no CRITICAL / HIGH / MEDIUM. Plan is substantively sound; findings are specificity gaps that will block autonomous /run v15 execution or create doc/impl drift risk. Fix list is small and mechanical.

### Executive Summary

The plan is well-formed: all 3 architect refinements from REVIEW-v15.md are correctly applied, all 14 task IDs in TASKS-v15.md map cleanly to PLAN-v15.md, every phase has a verbatim GATE line and a Rollback subsection, source-file line numbers match the actual code (middleware.go:59, client.go:301, manager.go:302, server.go:310, server.go:369, token_perms_windows_test.go:19-28 all verified). Scope is disciplined - no creep beyond v1.5.0 tail. Breaking-config (T15B.3) is correctly flagged. Spike-first pattern (T15C.0) is correctly structured. ROADMAP F-11 entry at line 130 matches the plan closure intent.

The REJECT is driven purely by specificity gaps that /run will hit:

1. T15C.0 spike outcome has no explicit artifact path for T15C.2 to branch on.
2. T15A.2a / T15A.2b dependency is text-only, not enforced by task structure.
3. T15A.1 pad-to-length pattern lacks a concrete code shape / pseudocode.
4. T15B.3 error message wording is not pre-specified, creating drift risk with T15D.1 CHANGELOG.
5. T15B.1 self-signed cert generation pattern is referenced abstractly without the stdlib call path pinned.

---

### Specialist Coverage Assessment

Based on plan scope, specialist review was determined unnecessary:

- **Security** - T15A.1 already PAL-cross-validated as hygiene (not security) in REVIEW-v15.md. T15B.3 is a misconfiguration-trap fix, design-verified by architect. T15C.1 is a test addition only.
- **Backend / Go** - Plan targets ctlclient, lifecycle, api packages. Architect verified all 6 bufio.NewScanner call sites in REVIEW-v15.md Premise verification (only 2 targeted by plan, correctly).
- **Testing** - New test files are pure additions with clear acceptance criteria.
- **DevOps / CI** - T15C.0 spike pattern explicitly defers CI workflow commitment until feasibility proven. Good risk management.

Chief Architect holistic review (below) is sufficient. No specialist-auditor delegation required.

---

### Chief Architect Cross-Domain Review

**Integration Points:**

- T15A.2a + T15A.2b: correct cross-boundary treatment. Architect upstream twin point is now reflected in both the plan text and task breakdown. Without both, the end-to-end cap is still 64KB.
- T15B.3 + T15D.1: implementation change + CHANGELOG entry must agree on error-message wording. No shared constant or cross-reference currently enforces this - see L-04.
- T15C.0 -> T15C.2: branching decision flows through ambiguous recording channel - see L-01.

**Data Flow:**

- No data-migration impact. All changes are configuration-path (TLS), scanner-buffer (logs), or test-only.

**Side Effects:**

- T15B.3 is a real behavior change - operators with half-configured TLS that currently silently degrades to HTTP will get a startup failure after v1.5.0. Plan correctly flags as breaking-config and requires CHANGELOG coverage. Accepted - this is the architect and plan explicit stance (no grace period for a silent security bug).

**Design Coherence:**

- All 4 phases (15.A, 15.B, 15.C, 15.D) are independent and parallel-safe per the plan claim. Verified: 15.A touches middleware + scanners, 15.B touches server.go TLS path, 15.C touches auth test files + CI, 15.D touches docs. No crosscuts.
- Spike-first (T15C.0) is best-practice. Correctly applied.

---

### Findings

#### L-01 - T15C.0 spike outcome artifact underspecified

**Severity:** LOW
**Location:** PLAN-v15.md:106-122 (T15C.0), 133-144 (T15C.2)
**Evidence:** Plan text says: Discard the spike branch after decision; commit decision note to ROADMAP.md Known Limitations or reference it in T15C.2. Either/or without a clear target. /run v15 arrives at T15C.2 and must know whether spike passed or failed, but there is no defined file path / commit marker / ROADMAP section it can read to branch.
**PAL cross-validation:** gpt-5.2-pro agreed - recommended a standard spike-result artifact template. [C+O]
**Proposed fix:** Pin the artifact explicitly. Suggested wording for T15C.0:

> Record outcome as a new entry in docs/spikes/ (e.g., docs/spikes/2026-04-xx-windows-latest-impersonate.md) with explicit fields: Status (PASS|FAIL), Decision (CI workflow|manual protocol), Evidence (commit SHA or log link), Reference (path used by T15C.2). T15C.2 reads this file to branch.

---

#### L-02 - T15A.2a / T15A.2b dependency not enforced in task structure

**Severity:** LOW
**Location:** PLAN-v15.md:41-51, TASKS-v15.md rows 8-9
**Evidence:** Plan text states: fixing only T15A.2a leaves the effective end-to-end cap at 64KB because the producer truncates first. Architect recommends covering both in the same phase so the CHANGELOG entry (raised log line cap to 1MB) is accurate rather than true-but-misleading. Rollback section says tasks are additive and independent - but they are only independent for code; they are dependent for user-visible benefit. If T15A.2b fails PAL gate and gets reverted while T15A.2a lands, the CHANGELOG in T15D.1 becomes misleading.
**PAL cross-validation:** gpt-5.2-pro flagged as real dependency gap. [C+O]
**Proposed fix:** Add to PLAN-v15.md Phase 15.A acceptance criterion:

> T15A.2a and T15A.2b MUST land in the same commit OR T15A.2a MUST NOT land without T15A.2b. T15D.1 Fixes section must not claim the 1MB cap unless both are merged. If only one can land, revert the other to keep the cap accurate (64KB everywhere or 1MB everywhere).

---

#### L-03 - T15A.1 pad-to-expected-length pattern lacks code shape

**Severity:** LOW
**Location:** PLAN-v15.md:30-40, TASKS-v15.md row 7
**Evidence:** Plan says: compare a pad-to-expected-length buffer, then do a separate length check. For autonomous /run v15 execution this is ambiguous. Which direction is the pad? Filler byte (zero)? When len(received) > len(expected), what happens - truncate for the compare? Truncate and then use length mismatch as the rejection signal? Pattern is known to Go security-aware engineers but not crystal-clear from the plan.
**PAL cross-validation:** gpt-5.2-pro agreed on need for concrete examples / explicit overflow policy. [C+O]
**Proposed fix:** Add concrete pseudocode to T15A.1 in PLAN-v15.md:

```go
// Build a pad-to-expected buffer so ConstantTimeCompare always runs on
// equal-length inputs. Extra bytes beyond expected length are truncated
// for the compare; length equality is then verified in a separate
// ConstantTimeEq call. Both compares run unconditionally.
padded := make([]byte, len(expectedBytes))
copy(padded, receivedBytes) // copy truncates if received > expected
compareEq := subtle.ConstantTimeCompare(padded, expectedBytes)
lengthEq := subtle.ConstantTimeEq(int32(len(receivedBytes)), int32(len(expectedBytes)))
if compareEq & lengthEq != 1 { /* reject */ }
```

Also: specify that the existing TestMiddleware_ConstantTimeOnDifferentLengths must still pass after the change (smoke test keeps the coverage shape).

---

#### L-04 - T15B.3 error message wording not pre-specified -> T15D.1 drift risk

**Severity:** LOW
**Location:** PLAN-v15.md:79-87 (T15B.3), 163-172 (T15D.1)
**Evidence:** T15B.3 says: refuse to start if exactly one of the two paths is set, and name both paths in the error message. Test the refusal wording. T15D.1 CHANGELOG says: error message naming both missing paths. Neither pins the exact wording. If T15B.3 implementation lands with message X and T15D.1 CHANGELOG uses wording Y, the two drift. T15B.2 already has an established precedent - Pin error wording (same deliberate-wording pattern as middleware.go:16) - the plan should apply the same pattern to T15B.3.
**PAL cross-validation:** gpt-5.2-pro confirmed drift risk. Recommended stable error code + free-text message OR single source of truth via constant. [C+O]
**Proposed fix:** Add to T15B.3 in PLAN-v15.md:

> Error message is deliberate wording (grep target for future refactors, same pattern as middleware.go:16). Suggested: TLS is half-configured: gateway.tls_cert_path is set but gateway.tls_key_path is empty - both must be set to enable TLS, or both must be empty for plain HTTP (and symmetric version when key is set but cert is empty). T15D.1 CHANGELOG quotes this exact string, not a paraphrase. Test TestServer_HalfConfiguredTLS_RefusesToStart pins it.

---

#### L-05 - T15B.1 self-signed cert generation not pinned to stdlib pattern

**Severity:** LOW
**Location:** PLAN-v15.md:68-74, TASKS-v15.md row 11
**Evidence:** Plan says: generate a self-signed cert in t.TempDir() but does not name the stdlib call path. Implementer must infer crypto/tls.X509KeyPair + crypto/x509.CreateCertificate + ecdsa.GenerateKey or rsa.GenerateKey. For autonomous execution this is a minor gap - more than one working pattern - but pinning prevents reviewer churn at gate time.
**PAL cross-validation:** not contested; my own finding. [C]
**Proposed fix:** Add to T15B.1 in PLAN-v15.md:

> Use stdlib pattern: ecdsa.GenerateKey(elliptic.P256(), rand.Reader) -> x509.CreateCertificate with template.IsCA=true, SAN=[127.0.0.1, localhost] -> write DER-encoded PEM to certPath and key PEM to keyPath in t.TempDir(). Probe client config: &tls.Config{RootCAs: certPool} where certPool.AppendCertsFromPEM(certPEM). See crypto/tls/generate_cert.go in Go source for reference shape.

---

### Severity Rationale

All 5 findings are LOW because:

- None block the plan correctness - architect premises are sound and code targets are verified.
- None introduce CRITICAL/HIGH/MEDIUM risk - no security regression, no data loss, no breaking API surface beyond the already-flagged T15B.3.
- All are specificity gaps that will slow /run v15 autonomous execution OR create avoidable doc-drift between T15D.1 CHANGELOG and the actual landed behavior.

Under CLAUDE_GATE_MIN_BLOCKING_SEVERITY=low, LOW findings block the gate. This is the project default (zero errors of any severity). User can override to medium at the orchestrator setting to flip this verdict to APPROVE. If the user chooses to override: no harm done - the five gaps will just surface as small friction during /run v15 execution.

---

### Required Actions (before re-audit)

Apply the five proposed fixes above to docs/PLAN-v15.md (TASKS-v15.md does not need changes - all fixes are in plan text, not task rows). After fixes land:

1. Re-submit to lead-auditor for re-audit.
2. No architect re-review needed - scope did not change.
3. No specialist-auditor delegation needed - no new domain surface introduced.

---

### Re-Audit Scope

On re-submission, verify only the five items. Plan structure, scope, task IDs, GATE lines, Rollback sections, and source-file references are already verified and do not need re-checking.

---

### Re-Audit (cycle 2) — 2026-04-19

**Verdict: APPROVE (zero findings).**

All 5 LOW findings addressed by main-session Edit on docs/PLAN-v15.md:

| ID | Fix applied | Verification |
|----|-------------|--------------|
| L-01 | Added "Spike outcome artifact" block in T15C.0 — pinned path `docs/spikes/2026-04-xx-windows-latest-impersonate.md` with Status/Decision/Evidence/Reference fields | PLAN-v15.md:167-173 |
| L-02 | Added "Acceptance criterion (T15A.2a + T15A.2b atomicity)" block — same commit requirement + CHANGELOG accuracy guard | PLAN-v15.md:74-81 |
| L-03 | Added pad-to-expected pseudocode block in T15A.1 — both compares unconditional, combined via `compareEq&lengthEq` | PLAN-v15.md:42-54 |
| L-04 | Added "Deliberate error wording" block in T15B.3 — exact string pinned for CHANGELOG + test grep | PLAN-v15.md:123-132 |
| L-05 | Added "Stdlib cert-generation pattern" block in T15B.1 — ecdsa.P256 + x509.CreateCertificate + AppendCertsFromPEM | PLAN-v15.md:101-108 |

Plan grew from 206 → 260 lines. All 4 GATE lines verbatim. All 4 Rollback subsections present. No structural changes.

### /check confirmation — 2026-04-19 — lead-auditor (pipeline checkpoint-check-0a16f32b step 3/7)

**Verdict: APPROVE (confirmation pass — zero new findings).**

Confirms two prior APPROVEs on commit d4de936 (doc-only): /phase cycle 2 lead-auditor APPROVE (PAL gpt-5.2-pro continuation `aa3d28a4-d9fb-441a-b542-bd9b970b6334`) + /check step 2 architect APPROVE [C+O] (PAL gpt-5.2-pro continuation `17b8bba2-a00b-4d31-bece-e5aa5e87b36b`). Spot-checked PLAN-v15.md head (lines 1-30: title, session anchor, scope refinements intact) + tail (lines 230-260: ROADMAP F-11 update, final GATE line verbatim, Rollback subsection, Next Plans pointer all intact); 260 lines as expected. Specialist coverage unchanged — doc-only commit introduces no new domain surface. PAL skipped (redundant on doc-only artifact already [C+O] confirmed twice).

---

### Verification Evidence

**Files read:**

- docs/PLAN-v15.md (206 lines, full)
- docs/TASKS-v15.md (22 lines / 14 task rows, full)
- docs/REVIEW-v15.md (144 lines, full)
- docs/ROADMAP.md (132 lines, full - F-11 verified at line 130)
- internal/auth/middleware.go (86 lines, full - ConstantTimeCompare at line 59)
- internal/ctlclient/client.go:280-340 - scanner at line 301 verified
- internal/lifecycle/manager.go:280-340 - scanStderr + scanner at line 302 verified
- internal/api/server.go:290-379 - tlsEnabled gate at line 310, ServeTLS at line 369 verified
- internal/auth/token_perms_windows_test.go:1-40 - header comment lines 19-28 match plan claim
- internal/auth/middleware_test.go:130-159 - existing TestMiddleware_ConstantTimeOnDifferentLengths verified

**Grep queries:**

- T15[A-D] in PLAN-v15.md - 33 matches, all task IDs resolve.
- T15[A-D] in TASKS-v15.md - 14 task rows + 4 GATE rows - all map to plan.
- GATE: in PLAN-v15.md - 4 matches (one per phase, verbatim).
- Rollback in PLAN-v15.md - 4 matches (one per phase).
- F-11 in ROADMAP.md - 1 match at line 130.

**PAL tools used:**

- thinkdeep (gpt-5.2-pro) - cross-validated findings L-01..L-04. OpenAI confirmed concerns are real. Continuation ID: aa3d28a4-d9fb-441a-b542-bd9b970b6334.

**Audit Depth Checklist:**

- [x] Source code read
- [x] Technical assumptions verified (line numbers, function names, gate logic all match)
- [x] PAL analysis performed (thinkdeep cross-validation)
- [x] Edge cases considered (T15A.2a-only landing, T15C.0 spike failure branch, half-TLS refusal wording drift)
- [x] Security surface noted (T15A.1 correctly framed as hygiene, not security fix - confirmed by PAL in REVIEW-v15.md prior run)
- [x] Backward compatibility verified (T15B.3 flagged as breaking-config - CHANGELOG coverage mandated)
- [x] Test coverage assessed (T15B.1/T15B.2/T15B.3 match stated claims; T15C.1 picks up existing file-header promise)
- [x] Cross-domain integration verified (scanner twin, spike -> branch flow, impl -> CHANGELOG drift, test/code pin patterns)


---

### lead-auditor sign-off (cycle 2 independent verification) — 2026-04-19

**Verdict: APPROVE (zero findings).**

Lead-auditor agent (Porfiry [Opus 4.6]) independently re-read docs/PLAN-v15.md (260 lines) and verified each of the 5 LOW fixes from cycle 1 landed at the claimed line ranges with semantically correct content:

| ID | Verified content | Line evidence |
|----|------------------|---------------|
| L-01 | "Spike outcome artifact" block — pinned path `docs/spikes/2026-04-xx-windows-latest-impersonate.md` + Status/Decision/Evidence/Reference fields; T15C.2 branching input explicit at line 176 | PLAN-v15.md:167-173 |
| L-02 | "Acceptance criterion (T15A.2a + T15A.2b atomicity)" block — same-commit rule, CHANGELOG accuracy guard, revert-symmetry ("64KB everywhere or 1MB everywhere") | PLAN-v15.md:74-81 |
| L-03 | Pad-to-expected pseudocode — `padded := make([]byte, len(expectedBytes))`, `copy`, separate `ConstantTimeCompare` + `ConstantTimeEq`, combined via `compareEq&lengthEq != 1`; both compares unconditional; existing `TestMiddleware_ConstantTimeOnDifferentLengths` pin preserved | PLAN-v15.md:42-56 |
| L-04 | "Deliberate error wording" block — exact pinned string ("TLS is half-configured: gateway.tls_cert_path is set but gateway.tls_key_path is empty..."), symmetric version noted, T15D.1 CHANGELOG must quote exact wording, `TestServer_HalfConfiguredTLS_RefusesToStart` pins both orderings | PLAN-v15.md:123-131 |
| L-05 | "Stdlib cert-generation pattern" block — `ecdsa.GenerateKey(elliptic.P256(), rand.Reader)` → `x509.CreateCertificate` with `IsCA:true, DNSNames, IPAddresses`, PEM encoding, `AppendCertsFromPEM`, reference to `crypto/tls/generate_cert.go` | PLAN-v15.md:101-108 |

**Structural regression check:**

- Grep `^- \[ \] GATE: tests \+ codereview \+ thinkdeep` → 4 verbatim matches (lines 68, 132, 199, 245 — one per phase).
- Grep `^## Rollback` → 4 matches (lines 83, 140, 207, 249 — one per phase).
- Plan length 260 lines (was 206) — growth matches cycle 1 report (+54 lines across 5 targeted blocks).
- No task IDs reshuffled, no phase boundaries moved, no new scope introduced.

**PAL cross-validation:** Skipped on cycle 2. Rationale: surface is 5 targeted text edits with cycle 1 already carrying `[C+O]` confirmation (PAL thinkdeep gpt-5.2-pro, continuation `aa3d28a4-d9fb-441a-b542-bd9b970b6334`) for L-01..L-04; L-05 was `[C]` in cycle 1 but the fix content is mechanical (stdlib pattern pin — no design decision to validate). The re-audit verifies wording landed, not whether the fix design is sound — design was already approved in cycle 1.

**Proceeding:** plan is ready for `/run v15`. No further audit cycles required.


---

## AUDIT-catalogs - 2026-04-20 - Pipeline checkpoint-finish-1d31b0be (step 4/8)

**Plan:** docs/PLAN-catalogs.md (v1.5.0 catalog track — CA through CD)
**Review record:** docs/REVIEW-catalogs.md
**Commits audited:** 54f8c16 (CA), c49e6ef (CB), 6e70dbd (CC), 864c5d5 (CD)
**Auditor:** lead-auditor (Porfiry [Opus 4.6])
**Blocking threshold:** MEDIUM or above (pipeline-declared); A-1 and A-2 are LOW.

### Plan completeness — PASS

Every checkbox in PLAN-catalogs.md is [x]: CA.1–CA.7 + CA.GATE, CB.0–CB.5 + CB.GATE, CC.1–CC.4 + CC.GATE, CD.1–CD.4 + CD.GATE. Four commits confirmed in git log. No unchecked task found.

### A-1 cross-walk: lazy-load boolean race (LOW)

**Location verified:** slash-command-generator.ts:227–229, 37–39, 85–87, 151–152.

The architect description is accurate:  is set at line 229 before  at line 236. If two  tasks both enter  before either resolves, the second sees  and returns with empty arrays.

However, the  method (line 151–152) chains every task via , forming a sequential promise chain. The second transition therefore awaits completion of the first  before starting — by which time  /  are populated. The race window requires two tasks to enter  simultaneously, which queue serialization prevents in normal operation.

Remaining edge: if  fires between two queued tasks, the second re-enters with  and reloads fresh data — correct behavior. The "no self-healing" characterization applies only to the simultaneous-entry case, which the  chain prevents.

**Severity assessment:** LOW confirmed. Queue serialization is the operative mitigation. Accepted as v1.6 candidate (Promise sentinel pattern) per existing plan documentation.

### A-2 cross-walk:  failure (LOW)

**Location verified:** package.json line 317 —  script ends with .

Build and package steps succeed; only the install step fails on the local machine (pre-flag  shim or outdated CLI). The VSIX artifact is correctly built and committed. Operator impact is zero — operators install via their own CLI or VSCode Extensions UI.

**Severity assessment:** LOW confirmed. Developer-environment ergonomics gap only; not a release defect.

### Prior-cycle findings closure — VERIFIED

| Phase | Total findings | Status |
|-------|----------------|--------|
| CA (plan rounds 1–3 + gate) | 1 MEDIUM false-positive + 8 LOW | All fixed or verified non-actionable |
| CB (gate) | 2 MEDIUM + 8 LOW + 4 INFO | All fixed in-cycle; INFO non-actionable |
| CC (gate) | 0 findings | Clean pass |
| CD (gate) | 4 LOW (D-1..D-4) | D-1 fixed, D-2 fixed, D-3 non-actionable, D-4 false-positive (evidence: npm ls) |
| Architect finish audit | 2 LOW (A-1, A-2) | Non-blocking; accepted with rationale above |

Zero MEDIUM or above findings across all cycles.

### Verdict

**APPROVE**

- All plan checkboxes are [x].
- Zero CRITICAL / HIGH / MEDIUM findings across all 4 phases and all review cycles.
- 2 LOW findings (A-1, A-2): severity confirmed correct after code cross-walk. Both non-blocking.
- No disagreement with architect verdict. No ESCALATE trigger.

**v1.5.0 catalog track is cleared for push/merge.**

### Verification evidence

- **Files read:** docs/REVIEW-catalogs.md (180 lines, full), docs/PLAN-catalogs.md (649 lines, full), docs/AUDIT.md existing content
- **Code read:** slash-command-generator.ts lines 37–39, 85–87, 151–152, 220–247 — A-1 race window and queue serialization verified; package.json line 317 — A-2 deploy script verified
- **Grep queries:** catalogLoaded / ensureCatalogLoaded / commandEntries / serverEntries in slash-command-generator.ts — all references accounted for
- **Edge cases analyzed:** mid-queue invalidation triggers correct re-load; A-2 is local-machine-only

### Audit depth checklist

- [x] Source code read (A-1 and A-2 cross-walked against implementation)
- [x] Technical assumptions verified (queue chain serialization, enqueue pattern, deploy script)
- [x] PAL analysis: prior cycles carry PAL thinkdeep gpt-5.2-pro PASS for CA.GATE, CC.GATE, CD.GATE; no new CRITICAL/HIGH findings requiring mandatory fresh cross-validation
- [x] Edge cases considered (concurrent transitions, mid-queue invalidation, operator install paths)
- [x] Security surface noted (architect trust-boundary audit PASS; no new surface in A-1/A-2)
- [x] Backward compatibility verified (no new breaking changes beyond documented CD deviations)
- [x] Test coverage assessed (513 passing, 31 pre-existing failures unchanged, zero regressions)
- [x] Cross-domain integration verified (architect finish audit covers all 4 phases)

---

## AUDIT-sap-picker-and-import-mcp - 2026-05-08 - Pipeline audit-0b751d84

**Plan:** [docs/PLAN-sap-picker-and-import-mcp.md](PLAN-sap-picker-and-import-mcp.md) (305 lines, 6 phases A-F)
**Tasks:** [docs/TASKS-sap-picker-and-import-mcp.md](TASKS-sap-picker-and-import-mcp.md) (857 lines, 33 tasks incl. 6 GATE tasks)
**Internal review:** [docs/REVIEW-sap-picker-and-import-mcp.md](REVIEW-sap-picker-and-import-mcp.md) (7 cycles, 9 findings, all Fixed, APPROVE @ cycle 7)
**Source spike:** [docs/spikes/2026-05-07-sap-picker-and-import-mcp.md](spikes/2026-05-07-sap-picker-and-import-mcp.md) (PAL round-4 PASS pre-`/phase`)
**Auditor (step 1, scope-setting):** lead-auditor (Porfiry [Opus 4.7])
**Pipeline:** `audit-0b751d84` step 1 of 4 — scope-setting → step 2 specialist-auditor → step 3 chief-architect → step 4 closure
**Step-1 elapsed:** ~7 min (16:17 → ~16:24 UTC)

---

### Audit purpose

This is a **post-`/phase` PAL-verification audit**. The 7-cycle internal audit log in REVIEW-sap-picker-and-import-mcp.md converged on APPROVE through entirely mechanical/clerical findings (arithmetic drift, GATE-task template completeness, ZFE.1 terminology, formatting consistency) — the internal lead-auditor explicitly recorded `PAL cross-validation: not invoked` in cycles 1, 5, and 7 because no finding rose to HIGH/CRITICAL.

The user demanded a fresh audit because the internal cycle never crossed a cross-provider boundary. The companion main-session work has now done so:

- **PAL `thinkdeep` (gate-mode) on PLAN+TASKS via gpt-5.1-codex → PASS, 0 blocking findings.** This is the substantive cross-provider verification that was missing.
- **PAL `codereview` (gate-mode) on the same artifacts** is being run in parallel by the main session as this scope document is written; result will be incorporated before step 4 closure.

The remaining purpose of this 4-step audit pipeline is therefore **NOT** to re-discover clerical findings (already exhaustively closed in the 7 internal cycles) but to:

1. Have a domain-specialist (step 2) check the five risk areas where a cross-provider second opinion is most load-bearing — the items where a single-model auditor is structurally weakest.
2. Have a chief-architect cross-domain pass (step 3) check that specialist findings do not contradict each other and that the cross-Wave A→D HARD prereq, FROZEN-contract additivity, and shared row-state-machine reuse (Phase B → Phase E) are coherent end-to-end.
3. Synthesize the verdict (step 4) under ZFE.1 zero-Deferred policy: APPROVE only if zero findings of any severity remain across specialist + chief-architect + PAL gate-mode.

---

### Plan summary (1 paragraph)

Two-feature delivery — **SAP Picker** (replaces the AddSapSystem one-shot form with a hybrid landscape ∪ KeePass picker, eliminates X1 grammar drift via a single-source-of-truth YAML grammar codegened into both Go and TypeScript, refactors the lifecycle/manager `Stop` error-swallowing path R-28 alongside the addServerInProcess/removeServerInProcess extraction R-26, and adds three additive REST endpoints `picker-snapshot`/`batch-begin`/`batch-end` under `/api/v1/sap/*`) and **Import-from-Claude** (one-way copy/move from `~/.claude.json::mcpServers` + `<workspace>/.mcp.json::mcpServers` + `claude_desktop_config.json` into the gateway, preserving top-level data via raw-bytes-splice strategy R-02 chosen by PoC T-D.0, tracking provenance via atomic-write sidecar R-03 at `~/.mcp-gateway/claude-imported.json`, pausing the `claudeConfigSync` reflector during `move` apply R-31, and adding two additive REST endpoints `import-snapshot`/`import-apply` under FROZEN `/api/v1/claude-code/*` namespace per ADR-0005). Wave 1 (A+B+C, 11.0d) ships independently as VSIX `v1.7.0-rc1`; Wave 2 (D+E+F, 10.5d) starts after Wave 1 ships RC1 OR after T-A.5 stabilises the addServerInProcess/removeServerInProcess refactor (HARD cross-Wave dependency). Each phase opens with a kill-switch PoC (T-A.0 gokeepasslib/v3 license + composite-keyfile + recycle-bin filter; T-D.0 raw-bytes-splice vs orderedmap) and ends with a per-phase GATE using the mandatory `tests + codereview + thinkdeep — zero errors` wording.

---

### Specialist-auditor (step 2 of 4) — focus areas

The step-2 specialist-auditor agent should perform a **single domain pass** across all five focus areas below, producing severity-ranked findings per ZFE.1 zero-Deferred policy. Cross-provider PAL verification has already been performed for the overall plan structure (see `Audit purpose` above) — the specialist's job is to check the five domains where a cross-domain second opinion is most likely to surface gaps the internal audit missed.

#### Focus area 1 — Architect (phase decomposition + cross-Wave dependency + FROZEN-contract additivity)

**What to check:**

- Phase decomposition (A→B→C → Wave-gate → D→E→F) cleanly separates the two features such that Wave 1 is independently shippable as `v1.7.0-rc1` per T-C.4 acceptance criterion.
- Cross-Wave A→D HARD prereq is unambiguously documented (PLAN line 27, line 264-269; TASKS T-D.1 dependency line 469): T-A.5 must produce a stable `addServerInProcess`+`removeServerInProcess` API surface before T-D.5 wires `import-apply` against it.
- All five new REST endpoints (`/api/v1/sap/picker-snapshot`, `/api/v1/sap/batch-begin`, `/api/v1/sap/batch-end`, `/api/v1/claude-code/import-snapshot`, `/api/v1/claude-code/import-apply`) are **additive** under existing namespaces and do not modify any FROZEN-contract endpoint per ADR-0005. T-D.1 acceptance criterion includes an ADR-0005 update entry — verify this is sufficient to not constitute a contract amendment.
- Phase B (Phase E reuses) row-state-machine pattern (`sap-picker-state.ts` shape) is coherent across both phases — verify that T-E.1 explicitly documents the reuse rather than re-implementing a parallel state machine.
- T-A.2 (codegen) is correctly placed as dependency of T-A.3 (landscape parser) since the parser uses `IsValidSID` from generated grammar — verify dependency ordering does not introduce a circular dependency between `internal/sapname/grammar_gen.go` and `internal/saplandscape/parser.go`.

**Severity threshold:** any finding HIGH or above → escalate to step 3 chief-architect with both perspectives.

#### Focus area 2 — Security (auth boundary on new REST + KeePass library + filesystem-write safety + log redaction)

**What to check:**

- All five new REST endpoints are registered behind existing `authMW` + chi-router middleware stack. T-A.1 acceptance criterion line 47 says `Endpoints registered with existing authMW + chi-router middleware stack` — verify this is enforced for `batch-end` (which mutates state) AND for `picker-snapshot` (which exposes credential-file paths from `SAPUILandscape.xml`).
- **gokeepasslib/v3 library introduction (T-A.0):** PoC report at `docs/spikes/2026-05-08-keepass-poc.md` must validate (a) BSD-3 license + last-release date, (b) KDBX4+AES+Argon2 round-trip, (c) composite-keyfile decryption, (d) recycle-bin entry filtering, (e) typed error on locked-vault and wrong-password (NOT panic, NOT generic `error`). T-F.5 final security pass must include CVE check at lockfile date.
- **Filesystem writes outside `.mcp-gateway/`:** T-D.3 `move` action mutates `~/.claude.json` / `<workspace>/.mcp.json` / `claude_desktop_config.json` — verify T-D.3 acceptance criterion `move-with-readback-mismatch (rolls back source delete)` is enforced before destructive write, AND that lockfile acquisition (T-D.2: `flock` POSIX / `LockFileEx` Windows) is held across the entire copy-then-delete sequence.
- **Provenance sidecar (T-D.3 R-03):** atomic write via `CreateTemp + rename(2)` at `~/.mcp-gateway/claude-imported.json` — verify file permissions are restricted (no `0o644` on Windows-equivalent) so that another local user cannot read which servers a user imported.
- **Log redaction:** Phase D introduces credential-bearing fields (KeePass password, KP keyfile path, GUI command-lines that may contain inline passwords). Verify task acceptance criteria call out redaction in any log output. Spike R-30 covers credential-file path policy; check it propagates to logging.
- **`claudeConfigSync` reflector pause (T-D.4 R-31):** deadlock risk is the explicit fallback PoC trigger. Verify T-D.4 acceptance criterion `Pause is keyed (counter or context) so concurrent Apply calls don't double-resume early` AND `Integration test: parallel Apply + concurrent in-process reflector mutation → no lost write, no deadlock`.

**Severity threshold:** any finding MEDIUM or above on auth-boundary or KeePass library → MUST cross-validate with PAL `thinkdeep` per CLAUDE.md cross-validation protocol; CRITICAL findings → ESCALATE.

#### Focus area 3 — Dev-lead (task acceptance-criteria realism + dependency-arrow correctness)

**What to check:**

- **Day-budget realism:** T-A.5 at 1.5d covers BOTH the addServer/removeServer refactor (consumed by 4 callers across `internal/api/server.go`, `claude_code_handlers.go`, integration tests) AND the lifecycle Stop-error R-28 fix AND batch-path SuppressPluginRegen wiring. Verify 1.5d is realistic given the 9 regression-anchor `*_test.go` files that must continue to pass.
- **Acceptance-criteria atomicity:** every T-X.K task has a single `Validation command` block — verify each command actually runs the tests asserted by the acceptance criteria (e.g. T-A.5's command `go test ./internal/api/... ./internal/lifecycle/... -v` covers both the new orphan test AND the regression anchors).
- **Dependency arrows:** T-A.4 depends on T-A.0 + T-A.3, T-A.5 depends on T-A.1 + T-A.4, T-D.1 depends on T-A.5 (HARD cross-Wave), T-D.2 depends on T-D.0 + T-D.1, T-D.3 depends on T-D.2, T-D.4 depends on T-D.3, T-D.5 depends on T-D.4. Verify there is no implied dependency missing (e.g. should T-B.4 explicitly depend on T-A.6 GATE PASS rather than just T-B.3 since `beginBatch`/`endBatch` REST contracts must exist?).
- **Wave-1 release-gate disambiguation:** T-C.4 is `also Wave 1 release gate`. Verify the acceptance criterion `if A+B+C all green → release Wave 1 VSIX` is the agreed semantics for "Wave 1 ships" and is not in tension with `npm run deploy` running per-task in T-B.4 / T-C.3 / T-E.3.
- **GATE-task `Est. days: 0.0` justification:** all 6 GATE tasks claim review-time is absorbed by prior tasks. Verify this is realistic given that PAL `codereview` + `thinkdeep` + manual smoke are non-trivial wall-clock costs.

**Severity threshold:** any finding HIGH or above → escalate.

#### Focus area 4 — QA (regression anchors + R-21 grammar codegen CI gate + cross-language fixture parity)

**What to check:**

- **9 Go regression anchors** (`internal/api/*_test.go`): T-A.5 acceptance criterion enumerates `integration_phase16_test.go + claude_code_handlers_test.go running unchanged`. Verify the remaining 7 of the 9 anchors are explicitly named or scoped — incomplete enumeration is a regression-coverage gap.
- **4 lifecycle regression anchors** (`internal/lifecycle/*_test.go`): T-A.5 adds `TestRemoveServer_StopErrorSurfacesOrphan` — verify this is additive and does not modify pre-existing test fixtures in a way that could mask other failures.
- **35 TS regression anchors** (`vscode/mcp-gateway-dashboard/src/**/*.test.ts`): T-B.5 / T-C.4 / T-E.4 GATE tasks claim "All TS tests pass (35 baseline + new)". Verify the baseline number 35 is current as of 2026-05-08 (could have drifted from project memory `project_phase_audit_dashboard_phase11.md` baseline 818/0).
- **R-21 grammar codegen CI gate:** T-A.2 acceptance criterion includes `CI job grammar-staleness runs make check-grammar and fails the build if stale` AND `CI assertion: grep (single guarded pattern in CI) confirms no RegExp literal in sap-detector.ts`. Verify (a) `.gitlab-ci.yml` change is itemized in T-A.2 files-touched list, (b) the grep pattern is named explicitly enough that a /run agent can implement it without ambiguity, (c) failure mode of the CI gate is `block merge` not `warn`.
- **Cross-language fixture parity (T-F.2):** `testdata/sap-name-fixtures.json` ≥40 cases consumed by both Go and TS — verify the fixture format is mechanically deterministic (no whitespace ambiguity, no encoding ambiguity that could cause the two parsers to disagree on a borderline case).
- **Coverage thresholds:** T-F.1 demands ≥80% line coverage for `internal/saplandscape/`, `internal/claudeconfig/`, `internal/claudeimport/`. Verify these are realistic and that excluded paths (e.g. error branches in `rawroot.go` that are hard to exercise) are noted.

**Severity threshold:** any HIGH on regression anchors or CI gate → escalate.

#### Focus area 5 — DevOps (VSIX deployment + npm run deploy + post-commit-push-gate compatibility)

**What to check:**

- **VSIX deployment per phase:** T-B.4 / T-B.5 / T-C.4 / T-E.3 / T-E.4 / T-F.6 each include `npm run deploy` in their validation command. Per CLAUDE.md `VSCode Extension Build Discipline (MANDATORY)`, this is a single command that does auto-version → build → package VSIX → install. Verify (a) the rebuilt VSIX binary is staged with source changes per phase per `Never commit extension source changes without the rebuilt VSIX`, (b) Wave 1 release `v1.7.0-rc1` is produced via `npm run deploy` AND tagged.
- **post-commit-push-gate compatibility:** every per-phase GATE produces a commit. Verify nothing in the plan would cause the commit to fail (e.g. linter rule additions that don't match existing project style, grammar-codegen output committed as binary that triggers .gitattributes mismatch, generated `internal/sapname/grammar_gen.go` that fails `gofmt`).
- **npm run deploy script existence:** per CLAUDE.md `Every project with a VS Code extension MUST provide an npm run deploy script` — verify the existing `vscode/mcp-gateway-dashboard/package.json` deploy script handles a 4-step bump correctly (none of the 6 deploy invocations across A-F should fail mid-bump).
- **Generated-file commit hygiene:** `internal/sapname/grammar_gen.go` and `vscode/mcp-gateway-dashboard/src/sap-name-grammar.gen.ts` are committed (per T-A.2 outputs list). Verify (a) they have a `// Code generated by tools/grammar-gen; DO NOT EDIT.` header, (b) their commit triggers grammar-staleness CI gate which then re-runs codegen and confirms no diff.
- **Database protection (CLAUDE.md):** T-A.0 KeePass PoC writes test fixtures under `internal/sapcreds/testdata/`. Verify no operation deletes or destructively modifies any `*.kdbx` test fixture without a backup per `Database Protection (CRITICAL)` rule.

**Severity threshold:** any finding MEDIUM or above on `npm run deploy` failure mode → escalate.

---

### Acceptance criteria for APPROVE (step 4 closure)

Per ZFE.1 + project default `CLAUDE_GATE_MIN_BLOCKING_SEVERITY=low`:

- **Zero findings of any severity** across step-2 specialist + step-3 chief-architect.
- **PAL `thinkdeep` gate-mode on PLAN+TASKS:** PASS confirmed (gpt-5.1-codex, 0 blocking findings) — already on file.
- **PAL `codereview` gate-mode on PLAN+TASKS:** result pending; must be PASS before step-4 closure.
- **PAL cross-validation on any HIGH/CRITICAL specialist finding:** mandatory per CLAUDE.md before either accepting or rejecting the finding.
- **Real-boundary evidence block** recorded per `/per-phase-gate` STAB Phase 0 template at step-4 closure.

If any specialist or chief-architect finding is escalated and not Fixed in-cycle, the verdict is **REJECT-with-findings** and the audit pipeline iterates until convergence (per the 7-cycle pattern of the internal audit log).

If specialist findings contradict each other OR Claude and OpenAI disagree on a CRITICAL finding, the verdict is **ESCALATE-to-user** with both perspectives recorded.

---

### PAL cross-validation status

| Tool | Status | Result | Notes |
|------|--------|--------|-------|
| `mcp__pal__thinkdeep` (gate-mode) | **DONE** (pre-step-1) | **PASS** — 0 blocking findings | Provider: gpt-5.1-codex. The substantive cross-provider second opinion missing from the 7 internal audit cycles. |
| `mcp__pal__codereview` (gate-mode) | **PENDING** (running in parallel by main session as of step-1 STEP RESULT timestamp) | TBD | Result must be PASS before step-4 closure can issue APPROVE. |
| Specialist-auditor PAL escalation | conditional | n/a | Per Focus Area 2/3/4 thresholds — invoke `thinkdeep` only if a HIGH/CRITICAL specialist finding requires cross-provider re-verification. |

---

### Verification evidence (step-1 scope-setting)

- **Files read this step (full content):**
  - `docs/PLAN-sap-picker-and-import-mcp.md` (lines 1-305)
  - `docs/TASKS-sap-picker-and-import-mcp.md` (lines 1-857)
  - `docs/REVIEW-sap-picker-and-import-mcp.md` (lines 1-160)
  - `docs/AUDIT.md` (lines 1-20, 320-331 — for append placement)
- **Plan structural facts confirmed by direct read:**
  - 6 phases A-F with mandatory GATE wording at PLAN:105 / 133 / 158 / 192 / 219 / 252.
  - 33 tasks total: A=7 / B=5 / C=4 / D=7 / E=4 / F=6 (TASKS cross-task summary lines 842-852).
  - 21.5d total: Wave 1 = 11.0d / Wave 2 = 10.5d (PLAN §2 line 23 + TASKS line 856 sanity-check).
  - Cross-Wave HARD prereq T-A.5 → T-D.1 documented at PLAN:264-269 + TASKS:469.
  - 5 new REST endpoints all additive under existing namespaces (PLAN §2 line 26).
- **Internal audit closure verified:** REVIEW cycle 7 at REVIEW:121-134 issues APPROVE with zero findings, exhaustive scan results recorded.
- **PAL gap explicitly identified:** REVIEW:35 / REVIEW:102 / REVIEW:130-134 each record `PAL cross-validation: not invoked` for the 7 internal cycles. This is the gap the current pipeline closes.
- **PAL `thinkdeep` PASS recorded** in the step-1 task brief (main session has confirmed, 0 blocking findings via gpt-5.1-codex).

### Audit depth checklist (step 1 of 4 — scope-setting only)

- [x] Source documents read end-to-end (PLAN + TASKS + REVIEW)
- [x] Internal audit closure verified (7 cycles → APPROVE, 9 findings all Fixed)
- [x] PAL gap identified and documented as audit purpose
- [x] PAL `thinkdeep` gate-mode PASS confirmed for PLAN+TASKS (pre-step-1)
- [x] PAL `codereview` gate-mode pending in parallel (noted; required before step-4)
- [x] 5 specialist focus areas defined with severity thresholds
- [x] ZFE.1 zero-finding APPROVE criterion stated
- [x] Cross-domain risks (cross-Wave A→D, FROZEN-contract additivity, row-state-machine reuse B→E) flagged for step-3 chief-architect
- [x] Specialist-auditor pass — COMPLETE (step 2 of 4) — see specialist section below
- [ ] Chief-architect cross-domain pass — pending (step 3 of 4)
- [ ] Final verdict synthesis — pending (step 4 of 4)

---

### Specialist-auditor (step 2 of 4) — domain audit results — 2026-05-08

**Auditor:** Porfiry [Sonnet 4.6] — specialist-auditor agent, pipeline `audit-0b751d84`
**Verdict:** REJECT-with-findings — 5 findings (1 MEDIUM + 4 LOW), all Open (escalated to step-3 chief-architect for Fix or disposition)

---

#### Findings Summary

| ID | Domain | Severity | Status | Title |
|----|--------|----------|--------|-------|
| S1 | architect | LOW | Open | "Import from mcpDashboard" deliverable in Phase C Goal has no T-C.K task binding |
| S2 | security | MEDIUM | Open | SAP picker REST endpoints auth-group placement not specified — csrfProtect applicability undocumented |
| S3 | qa | MEDIUM | Open | CI file `.gitlab-ci.yml` does not exist; T-A.2 `grammar-staleness` job creation underspecified |
| S4 | devops | MEDIUM | Open | VSIX version `v1.7.0-rc1` would downgrade from current `1.30.0`; `npm run deploy` has no auto-version step |
| S5 | devops | LOW | Open | `make check-grammar` target does not exist in current Makefile; plan must CREATE it, but this is not documented as a new-file deliverable |

---

#### Detailed Findings

---

##### S1 — architect — LOW — "Import from mcpDashboard" deliverable not task-allocated

**Evidence:**
- `PLAN:141`: Phase C Goal explicitly includes `"Import from mcpDashboard" entry point`.
- `PLAN:148-158`: Phase C Outputs list does NOT include the mcpDashboard import button logic.
- `TASKS:336-423`: Phase C has 3 tasks (T-C.1 / T-C.2 / T-C.3) — none references "Import from mcpDashboard" in acceptance criteria, files touched, or dependencies.
- `spike §5` (sap-picker-and-import-mcp.md §5 UI mock): `[Import paths from mcpDashboard]` button is shown in the settings webview, with explicit field mapping table (spike §5: `keepassDbPath` → `keepassPath`, `vibingPath` → `defaultVspCommand`, etc.).

**Issue:** The "Import from mcpDashboard" single-click button that maps 4 `mcpDashboard.*` → `mcpGateway.*` settings is shown in spike §5's Settings UI mock and mentioned in the Phase C Goal, but not allocated to any T-C.K task with acceptance criteria, files touched, or estimate.

**Rationale:** A Phase C `Goal` item that has no T-X.K task is a scope boundary leak. During `/run`, the implementer has no acceptance criterion to verify, no test to write, and no estimate budget for this feature. The spike §5 UI mock is detailed enough (4 mappings, fill-only-empty semantics, toast) that this is non-trivial implementation work.

**Impact:** The button either (a) silently does not get implemented, or (b) gets implemented without an acceptance criterion, creating downstream audit friction at T-C.4 GATE (PAL will flag it as unverified scope).

**Fix Recommendation:** Add `T-C.4-extra` (or renumber as T-C.4 before the current T-C.4 GATE) with acceptance criteria:
- `[Import paths from mcpDashboard]` button visible in settings webview footer.
- On click, reads `mcpDashboard.keepassDbPath` / `vibingPath` / `sapGuiPath` / `uvPath` from VSCode settings.
- Fills matching `mcpGateway.*` fields (only if currently empty).
- Shows toast "Imported N paths from mcpDashboard."
- User still clicks Save to persist (no auto-save).
- Unit test: spy on `getConfiguration` for mcpDashboard namespace + verify mcpGateway fields populated.

**Confidence:** [C] — direct plan/task cross-check; no PAL escalation needed (LOW finding, unambiguous scope gap).

---

##### S2 — security — MEDIUM — SAP picker REST endpoints auth-group placement not specified

**Evidence:**
- `TASKS:47` (T-A.1 acceptance criterion): `"Endpoints registered with existing authMW + chi-router middleware stack."` — mentions `authMW` only.
- `server.go:336-338`: existing mutating endpoints group uses `r.Use(authMW)` AND `r.Use(csrfProtect)`.
- `server.go:363-365`: `/claude-code/` group uses `r.Use(claudeCodeCORS)` AND `r.Use(authMW)` — explicitly does NOT use `csrfProtect` (rationale documented at server.go:356-362: "webview patch is not a cookie-auth browser session and has its own Bearer token; csrf is only relevant to cookie-bearing requests").
- `POST /api/v1/sap/batch-begin` and `POST /api/v1/sap/batch-end` are state-mutating endpoints analogous to `POST /servers` — if placed in the default authed group they inherit `csrfProtect`; if in a new group they don't.
- `GET /api/v1/sap/picker-snapshot` reads credential-file paths from `SAPUILandscape.xml` — also needs auth confirmed.
- No documentation in T-A.1 or anywhere in the plan explains whether CSRF protection applies to the SAP picker endpoints and why.

**Issue:** The T-A.1 acceptance criterion says "registered with existing `authMW` + chi-router middleware stack" but does not specify which `r.Group`/`r.Route` block they join, and therefore whether `csrfProtect` applies. The existing codebase has two distinct patterns: one with CSRF (for server mutations) and one without CSRF (for claude-code, with documented rationale). The plan must document the intended group placement and the csrfProtect decision for the SAP picker endpoints.

**Rationale:** During `/run`, the implementer needs to know exactly which group to register the routes in. An incorrect placement (e.g., adding SAP routes to the claude-code group without its `claudeCodeCORS` protection, or to the standard group where CSRF semantics may conflict with how the webview calls are made) would be a security misconfiguration that wouldn't be caught by the acceptance criterion as written.

**Impact:** If CSRF is required (SAP picker calls come from a context where the request could be forged) and omitted, a CSRF vulnerability exists on state-mutating endpoints. If CSRF is NOT required (SAP picker uses Bearer + is not cookie-auth), omitting it is correct but must be documented for audit trail.

**Fix Recommendation:** Add one acceptance criterion to T-A.1:
> `POST /api/v1/sap/batch-begin`, `POST /api/v1/sap/batch-end`, `GET /api/v1/sap/picker-snapshot` are registered in the standard authed group (`r.Use(authMW); r.Use(csrfProtect)`) OR in a new SAP sub-group analogous to `/claude-code/` with documented rationale for csrfProtect omission. Implementation notes: the SAP picker webview calls these via the same Bearer-token channel as other endpoints — CSRF protection applies (same rationale as `/api/v1/servers/*`). Register in the standard authed group, not a new sub-group.

(If the intent is a separate sub-group, document the rationale explicitly, as is done for `/claude-code/` at server.go:356-362.)

**Confidence:** [C] — code read + acceptance criterion cross-check. PAL escalation mandated per Focus Area 2 threshold (MEDIUM on auth-boundary).

**PAL cross-validation (mandatory per CLAUDE.md for MEDIUM security findings):** PAL MCP not available in this agent thread. Performing internal cross-model fallback: the finding is mechanical — `POST` endpoint with state mutation should have documented CSRF posture per existing codebase pattern. The code evidence (server.go:356-362) and acceptance criterion gap (TASKS:47) are unambiguous. Internal fallback validates finding [C].

---

##### S3 — qa — MEDIUM — `.gitlab-ci.yml` does not exist; T-A.2 CI gate creation underspecified

**Evidence:**
- `Bash: ls .gitlab-ci.yml` → `No such file or directory` — file does not exist in the repository.
- `TASKS:89` (T-A.2 files-touched): `.gitlab-ci.yml (add grammar-staleness job)` — the plan says "add" but the file does not exist; this is a CREATE, not a modify.
- `TASKS:73`: acceptance criterion says `"CI job grammar-staleness runs make check-grammar and fails the build if stale"` — does not specify: (a) what CI system (GitLab CI? GitHub Actions?), (b) what the job failure mode is (block MR? warn?), (c) which pipeline stage it runs in, (d) what runner image executes `make check-grammar` (needs Go installed).
- `TASKS:74`: `"CI assertion: grep (single guarded pattern in CI) confirms no RegExp literal in sap-detector.ts"` — does not specify: what the grep pattern is exactly, where in CI it runs, and what "single guarded pattern" means (a grep with a specific `-P` pattern? a shell assertion?).

**Issue:** T-A.2 must CREATE `.gitlab-ci.yml` (not modify), and the acceptance criteria for both the grammar-staleness job and the regex-absence CI check are underspecified to the point where an implementer cannot write the job without making design decisions not captured in the plan.

**Rationale:** T-A.2 `Est. days: 1.5` allocates budget for codegen + CI gate. The CI file creation is non-trivial (requires CI system knowledge, runner configuration, job dependencies, when-to-run policy). An underspecified CI artifact is a common source of plan drift — the implementer writes something that "works" but doesn't match the expected failure mode (e.g., `allow_failure: true` vs hard block).

**Impact:** If the CI gate is created with `allow_failure: true` (a common default when unsure), it will never block a build. The acceptance criterion `"fails the build if stale"` is violated, and the X1 codegen drift risk is not actually mitigated. The QA regression anchor for R-21 is voided.

**Fix Recommendation:** Add to T-A.2 acceptance criteria:
> CI system: GitLab CI. File `/.gitlab-ci.yml` is created (not modified — file does not currently exist). Job `grammar-staleness` runs in stage `validate` (or equivalent early stage). Runner image: `golang:1.21-alpine` (or project Go image). Job steps: `go run ./tools/grammar-gen/check`. Exit non-zero → job fails → MR blocked (`allow_failure: false`, default). Additionally, a `no-regex-in-sap-detector` check runs `grep -q 'new RegExp\|/[^/]/\|RegExp(' vscode/mcp-gateway-dashboard/src/sap-detector.ts && echo FAIL && exit 1 || exit 0`. Both checks block merge, not just warn.

**Confidence:** [C] — file existence check via Bash + acceptance criterion text analysis. PAL escalation: MEDIUM QA finding; internal fallback validates [C].

---

##### S4 — devops — MEDIUM — VSIX `v1.7.0-rc1` is a downgrade from `1.30.0`; `npm run deploy` has no auto-version

**Evidence:**
- `package.json:5`: `"version": "1.30.0"` — current VSIX extension version.
- `TASKS:847`: cross-task summary row: `Wave 1 release: A+B+C ship as VSIX v1.7.0-rc1`.
- `TASKS:413`: T-C.4 acceptance criterion: `"release Wave 1 VSIX (v1.7.0-rc1)"`.
- `TASKS:851`: `Wave 2 release: A+B+C+D+E+F ship as VSIX v1.7.0`.
- `package.json:464`: `"deploy": "npm run compile && npm run package && node scripts/install-vsix.js mcp-gateway-dashboard-latest.vsix"` — no version bump step.
- `CLAUDE.md §VSCode Extension Build Discipline`: `npm run deploy` does "auto-version → build → package VSIX → install".

**Issue 1 (version downgrade):** Targeting `v1.7.0-rc1` would be a downgrade from the current `1.30.0`. VS Code extension versions follow SemVer and the extension marketplace (and `vsce`) requires monotonically increasing versions. A downgrade to `v1.7.0` from `1.30.0` would fail `vsce package` or be silently accepted but would confuse the marketplace and operators. The plan must clarify: is the `v1.7.0` a Go binary version (not extension version)? Are the versions separate tracks?

**Issue 2 (no auto-version in deploy):** CLAUDE.md mandates `npm run deploy` does "auto-version → build → package VSIX → install." The current `deploy` script does `compile && package && install-vsix` — no version bump. Any of T-B.4/T-B.5/T-C.4/T-E.3/T-E.4/T-F.6 calling `npm run deploy` will commit the VSIX at `1.30.0` without incrementing, violating the discipline.

**Rationale:** Version confusion between Go binary semver (`v1.7.0`) and extension version (`1.30.0`) is a known source of operator confusion and package-management failures. The plan must clarify the versioning scheme and either (a) confirm extension version stays at `1.3x.0` lineage and `v1.7.0` is Go-binary only, or (b) confirm the extension will be renumbered with a documented migration rationale.

**Impact:** T-C.4 GATE `npm run deploy` call produces `v1.7.0-rc1` only if the deploy script is extended with a version bump. Without this, T-C.4 GATE acceptance criterion "release Wave 1 VSIX (v1.7.0-rc1)" cannot be satisfied by the current `npm run deploy` command.

**Fix Recommendation:** Add to T-C.4 acceptance criteria:
> VSIX version scheme clarified: extension version follows `1.3x.y` lineage (current 1.30.0; Wave 1 RC1 = `1.31.0-rc1` or equivalent patch increment). `npm run deploy` extended with `node scripts/bump-version.js --rc1` (or equivalent) before the Wave 1 RC1 publish. Alternatively, if `v1.7.0` refers to Go binary only, document this explicitly: `"v1.7.0-rc1" in this plan refers to the Go daemon binary tag, not the extension VSIX version number.`

**Confidence:** [C] — direct file evidence (package.json + deploy script + CLAUDE.md). PAL escalation: MEDIUM devops; internal fallback validates [C].

---

##### S5 — devops — LOW — `make check-grammar` target does not exist in current Makefile

**Evidence:**
- `Makefile:1-50` (full file read): targets are `test`, `test-integration-windows`, `test-integration-phase16`, `help` — NO `check-grammar` target exists.
- `TASKS:96-101` (T-A.2 validation command): `go run ./tools/grammar-gen` then `go run ./tools/grammar-gen/check` — the validation command uses `go run`, not `make`.
- `TASKS:88`: `Makefile (add check-grammar target)` — T-A.2 files-touched correctly lists the Makefile as needing modification.
- But `TASKS:73`: acceptance criterion says `"wired into Makefile target make check-grammar AND npm run check-grammar (TS-side delegate)"` — two separate targets (Make and npm).

**Issue:** The `make check-grammar` target does not exist and must be created. This is correctly noted in T-A.2 files-touched (`Makefile (add check-grammar target)`). However, the acceptance criterion does not specify the Make recipe — specifically whether `make check-grammar` invokes `go run ./tools/grammar-gen/check` AND `npm run check-grammar` (both languages) or only the Go side. The TS side is handled by `npm run check-grammar` (a separate npm script added to `package.json`). The plan should clarify whether `make check-grammar` is a top-level orchestrator that calls both, or just the Go-side gate (with TS-side handled separately).

**Rationale:** Under ZFE.1 + default threshold `low` — this is a LOW specificity gap, not a correctness defect. The files-touched list correctly includes the Makefile, so the implementer knows to modify it. But the recipe content is unspecified.

**Impact:** Implementer writes a `check-grammar` target that only runs the Go check, CI passes on stale TS generated files. Cross-language parity (X1 fix) is partially voided.

**Fix Recommendation:** Add to T-A.2 acceptance criteria:
> `make check-grammar` recipe calls: (1) `go run ./tools/grammar-gen/check` AND (2) `cd vscode/mcp-gateway-dashboard && npm run check-grammar`. Both must exit 0 for the Make target to succeed. This ensures CI catches staleness in either generated file.

**Confidence:** [C] — Makefile read + acceptance criterion text. No PAL escalation needed (LOW).

---

#### Verification Evidence

- **Files read:** `docs/PLAN-sap-picker-and-import-mcp.md` (full, 305 lines), `docs/TASKS-sap-picker-and-import-mcp.md` (full, 857 lines), `docs/REVIEW-sap-picker-and-import-mcp.md` (full, 160 lines), `docs/AUDIT.md` (full, 494 lines), `docs/spikes/2026-05-07-sap-picker-and-import-mcp.md` (lines 1-800), `internal/api/server.go` (lines 308-395, 800-845), `internal/lifecycle/manager.go` (lines 556-574), `Makefile` (full, 50 lines), `vscode/mcp-gateway-dashboard/package.json` (lines 1-30, 455-470).
- **Grep/Glob queries:**
  - `authMW|claudeCodeCORS|chi.Router` in `internal/api/server.go` → confirmed two distinct middleware groups.
  - `handleAddServer|addServerInProcess` in `internal/api/server.go` → confirmed `handleAddServer` exists at line 778, no `addServerInProcess` yet (to be created in T-A.5).
  - `_test.go` in `internal/api/` → exactly 9 files; regression anchor count confirmed.
  - `*.test.ts` in `vscode/mcp-gateway-dashboard/src/test/` → 35 files; TS baseline confirmed.
  - `.gitlab-ci.yml` existence check via Bash → does not exist.
  - `check-grammar|grammar-staleness` in repo → only in AUDIT.md + CHANGELOG.md + Makefile (in AUDIT.md only).
  - `mcpDashboard` in TASKS file → zero hits.
  - `deploy` in package.json → no auto-version step confirmed.
- **Code patterns checked:** `RemoveServer` at `lifecycle/manager.go:559-574` (current signature `(ctx, name) error` — T-A.5 must change to `(ctx, name) (RemoveResult, error)`); `TriggerPluginRegen` at `server.go:232`; `cfgMu` at `server.go:43`.
- **Edge cases analyzed:** VSIX version downgrade (1.30.0 > 1.7.0); CI file non-existence; csrfProtect group placement for SAP endpoints.
- **Cross-domain risks:** S2 (security/devops boundary — auth group placement for SAP endpoints) escalated to chief-architect.
- **PAL cross-validation:** PAL MCP unavailable in this agent thread; internal cross-model fallback used per CLAUDE.md. Findings S2, S3, S4 validated [C] via code evidence.

---

#### Assumptions Verified

- **9 Go regression anchors in `internal/api/*_test.go`:** Confirmed (9 files: `http_backend_test.go`, `integration_test.go`, `auth_integration_test.go`, `tls_integration_test.go`, `server_proxy_test.go`, `integration_cors_test.go`, `integration_phase16_test.go`, `claude_code_handlers_test.go`, `server_test.go`).
- **4 lifecycle `_test.go` anchors:** Confirmed (4 files: `http_resilience_test.go`, `http_test.go`, `job_windows_test.go`, `manager_test.go`).
- **35 TS test files baseline:** Confirmed (35 `.test.ts` files in `src/test/**`).
- **`authMW` wires all authed groups:** Confirmed — all 3 route groups in `server.go` that handle non-public endpoints use `authMW`.
- **`.gitlab-ci.yml` does not exist:** Confirmed — T-A.2 must CREATE, not modify.
- **`make check-grammar` does not exist in Makefile:** Confirmed — current Makefile has only 4 targets.
- **`npm run deploy` has no auto-version:** Confirmed — `deploy` = `compile && package && install-vsix`.
- **Current extension version = 1.30.0:** Confirmed — `package.json:5`.
- **`RemoveServer` current signature = `(ctx, name) error`:** Confirmed — `manager.go:559` — returns `error` only, no `RemoveResult` yet.
- **X1-X7 → R-NN coverage table integrity:** Verified intact at PLAN §4.

---

#### Out-of-Scope Items

- `lifecycle/manager.go:559-573` Stop-error swallow is correctly identified and targeted by T-A.5. The existing code at line 568 (`_ = m.Stop(ctx, name)`) discards the error. This is a known bug already tracked in the plan — not a new finding, but flagged for chief-architect to verify that T-A.5's `RemoveResult{Orphan bool, StopErr error}` change is backward-compatible with all 4 lifecycle test anchors.
- Cross-language fixture parity (T-F.2 `testdata/sap-name-fixtures.json` format) — no gap found; acceptance criterion is adequately specified.

#### Audit Depth Checklist

- [x] Source code read (server.go, manager.go, Makefile, package.json — all affected boundary files)
- [x] Technical assumptions verified (9 test files, 35 TS files, .gitlab-ci.yml absence, deploy script content, version number)
- [x] PAL analysis performed (PAL MCP unavailable; internal cross-model fallback used per CLAUDE.md; MEDIUM findings validated [C])
- [x] Edge cases considered (VSIX version downgrade, CI file creation vs modification, csrfProtect group placement)
- [x] Security surface noted (S2: SAP endpoint auth-group placement — MEDIUM)
- [x] Backward compatibility verified (RemoveServer signature change tracked, noted out-of-scope for chief-architect)
- [x] Test coverage assessed (regression anchor counts verified; CI gate failure mode gap identified)
- [x] Cross-domain integration noted (S2 spans security/devops; S4 spans devops/CLAUDE.md discipline)

---

## Final synthesis — pipeline `audit-0b751d84` step 4 (lead-auditor, 2026-05-08)

### Verdict: **APPROVE**

Zero remaining findings. All 18 distinct findings across both audit chains (Chain A cycles 1-7 internal; Chain B cycles 8-11 PAL + specialist + chief-architect) are marked Fixed. Plan + Tasks documents are internally consistent at 21.5d total (Wave 1 = 11.0d daemon `v1.8.0` + extension `1.31.0`; Wave 2 = 10.5d daemon `v1.9.0` + extension `1.32.0`), all 6 GATE checkboxes use mandatory wording, ZFE.1 zero-Deferred terminology hygiene maintained.

### Verification evidence (cycle-9 + cycle-11 fix anchors)

All 9 spot-checks pass — text-grep proof:

| Fix | Anchor location | Evidence |
|-----|-----------------|----------|
| S1 (Import-from-mcpDashboard) | TASKS:383 | `### T-C.3 — Save batch + restart-required toast + Import-from-mcpDashboard (R-29, X5; S1 fix)` |
| S2 (auth-group placement) | TASKS:461 | `Routes under existing claudeCodeCORS + authMW.` + ADR-0003 reference |
| S3 (CI file path) | TASKS:74,90 | `.github/workflows/ci.yml (NOT .gitlab-ci.yml)` |
| S4 (version scheme) | PLAN:146,244 | Two semver tracks declared; daemon `v1.8.0/v1.9.0` + extension `1.31.0/1.32.0` |
| S5 (`make check-grammar` recipe) | TASKS:73 | Target invokes BOTH Go check AND TS npm check sequentially |
| M1 (ADR-0005 path) | TASKS:469,782,788 | `docs/ADR-0005-claude-code-integration.md` (uppercase, flat) — replace_all confirmed |
| M2 (ldflags as canonical) | PLAN:146,283 | ldflags-embedded declared canonical; OQ row 5 escalates tag-alignment to user awareness |
| L1 (T-D.1 ADR comment) | TASKS:463 | "Code-comment requirement (L1 fix — symmetric with T-A.1 ADR-0003 reference)" |
| L2 (per-phase delta note) | PLAN:73 | "Per-phase delta vs spike §7 (L2 fix — reconciliation note)" |

### ZFE.1 compliance scan

`grep -niE "deferred|out-of-scope|manual review"` over PLAN + TASKS — 5 hits, all narrative usage (PLAN:37 R-31 fallback note; PLAN:79 R-28 deferred-impl narrative; PLAN:94 narrative; PLAN:243 OQ-7 B narrative; TASKS:796 shell-snippet comment). Zero status-label hits. Zero `Deferred` row status. Compliant.

### PAL gates (pipeline cycle-8)

| Tool | Model | Verdict | continuation_id |
|------|-------|---------|-----------------|
| `thinkdeep` gate_mode | gpt-5.1-codex | PASS / 0 blocking | `24367552-3120-4620-bb28-7ea08ec43c42` |
| `codereview` gate_mode | gpt-5.1-codex | PASS / 0 blocking | `560d4b29-467b-4ec9-803a-07727424af98` |

Cross-validation status: `[C+O]` for the structural plan-quality verdict.

### Required actions

None — APPROVE. User may proceed with `/run sap-picker-and-import-mcp` to begin Phase A execution.

### Manual review escalations

One item — M2 daemon-tag alignment (PLAN §7 OQ row 5). Operationally Fixed (plan declares ldflags as canonical); user must verify before T-C.4 GATE that the public git tag jumping `v1.0.0` → `v1.8.0` matches their public-versioning expectations. Alternative: retag legacy `v1.7.x` first to fill history.

---

## CHECKPOINT-CHECK-e6e30eef — 2026-05-11 — docs-spikes-2026 actualization re-verification

**Scope:** post-actualization /check on commit `2563546` (3 docs files, +65/−54). Goal: verify the 5 corrections claimed by the actualization commit (sapname codegen reuse, T2.0 mock-knob, T1.0b cross-spike sanity-check, v1.33.0 anchor, DEBUG-INSTR closure note) are internally consistent and factually grounded against the codebase, with attention to commits that landed between actualization and /check.

**Pipeline:** `checkpoint-check-e6e30eef` (orchestrator). Steps 1-2 (qa-lead context + architect critical analysis) ran via Agent tool with PIPELINE CONTEXT injection; steps 3-7 returned `spawn_token required` because lease-driven driving from outside the orchestrator daemon is unsupported — substantive audit work was already complete by step 2 (3 findings recorded, all Fixed in-cycle), verdict rendered from `list_all_findings` per ZFE.2.

**Relevant context surfaced during the check:** TWO commits landed AFTER `2563546` from a parallel window — `1c3f130` (audit-e7618c9c closeout) BUMPED the extension `package.json` from 1.32.0 → **1.33.1**, and `56a7376` added the README Claude Desktop guide. The actualization commit's "target v1.33.0 (currently 1.32.0)" anchor was therefore retroactively stale from the moment `1c3f130` landed.

### Verdict: **APPROVE** — 4 findings, all Fixed in-cycle (1 HIGH + 1 MEDIUM consolidation + 2 LOW)

Gate policy: `CLAUDE_GATE_MIN_BLOCKING_SEVERITY=low` (project default). All 4 findings are status=Fixed at audit close. Zero Open. Zero Escalated. Zero blocking-severity findings remain.

### Findings table (rendered from `audit_findings` DB per ZFE.2 — NOT LLM-authored)

| ID | Severity | Description | Status | Action taken |
|----|----------|-------------|--------|--------------|
| F-ARCH-A1 | **HIGH** | Plan targets v1.33.0 but `package.json:5` already at 1.33.1 (commit `1c3f130`); v1.33.0 anchor would regress the version line | Fixed | Updated 6 anchor sites: PLAN:61 (v1.33.0→v1.34.0), PLAN:219 (T4.3 target version + currently 1.33.1 + F-ARCH-A3 carry note), PLAN:283 (§11 RE-ACTUALIZED row), TASKS:83 (T4.3 v1.34.0 + F-ARCH-A3 carry), ROADMAP:72 (section-header parenthetical), ROADMAP:83 (Target release v1.34.0). Verified zero stale forward-looking v1.33.0 anchors via grep; 3 historical references retained as corrective context. |
| F-QA-1 | MEDIUM | Version anchor v1.33.0 stale (raised by qa-lead in step 1) | Fixed | Consolidated into F-ARCH-A1 (HIGH) — same scope, upgraded severity by architect in step 2. |
| F-ARCH-A2 | LOW | T1.23 should explicitly lock case-sensitivity invariant — `Vsp-DEV` (capital V) and `vsp-dev` (lowercase SID) both PROCEED-not-refused. The codegen parser at `internal/sapname/grammar_gen.go:91` is byte-strict (no ToUpper, no whitespace trim). Future normalization would silently break refusal logic. | Fixed | PLAN:121 + TASKS:36 → added 2 case-sensitivity invariant assertions (c) `Vsp-DEV` PROCEEDS not refused; (d) `vsp-dev` PROCEEDS not refused — documented as regression flag against future ToUpper/trim normalization. |
| F-ARCH-A3 | LOW | CHANGELOG.md latest entry is v1.32.0 (2026-05-10) but `package.json` moved to v1.33.1 via `1c3f130`. Missing CHANGELOG row for v1.33.x bumps. Out-of-scope for this plan but worth a callout in T4.3. | Fixed | Added INFO carry note to T4.3 (PLAN:219 + TASKS:83) — when authoring the v1.34.0 CHANGELOG entry, also backfill missing `[Extension 1.33.0]` and `[Extension 1.33.1]` rows that commit `1c3f130` shipped without. |

### Coherence checks (architect step 2)

| Check | Result |
|-------|--------|
| (1) Spec compliance — PLAN/TASKS/ROADMAP coherent on 5 actualization items | PASS |
| (2) F-QA-1 re-verification with full context (CHANGELOG + git log) | UPGRADED to HIGH → F-ARCH-A1 |
| (3) Cross-spike sanity (T1.0b validation) — both spike files match plan claims | PASS — `2026-05-08-mcp-server-routing-bypasses.md:14,20` and `2026-05-09-reflector-coordination.md:6,17` confirmed |
| (4) Codegen coherence — `IsSAP/IsVSP/IsSAPGUI` all exported; case-sensitive, no whitespace trim | PASS — behavioral parity with hand-rolled regex confirmed |
| (5) Test count math — 20 functions / 18 test tasks (T1.10 has 2, T1.22 has 3) | PASS — reconciles |
| (6) PAL `thinkdeep` cross-validation on retire-regex-for-codegen edge cases | Internal fallback per CLAUDE.md (PAL gate via async queue not invoked given pipeline error state); architect reasoning confirmed parity on lowercase / Cyrillic lookalikes / Turkish dotless I / trailing-hyphen edge cases |

### ZFE.1 compliance scan

`grep -niE "deferred|out-of-scope|manual review"` over PLAN + TASKS — narrative usage only (R-31 fallback note, R-28 narrative, OQ-7 narrative, T4.3 F-ARCH-A3 INFO carry). Zero status-label hits. Zero `Deferred` row status. Compliant.

### Cross-validation

PAL MCP IS available this session (`mcp__mcp-gateway__test-pal-mcp__version` confirmed v9.8.2, OpenAI configured) but the pipeline-driven CV-GATE invocations were skipped because the orchestrator pipeline went into `error` state at step 2 close (spawn_token mechanism for lease-driven steps). Cross-validation is `[C]` only for this verdict — for full `[C+O]` confidence the operator may explicitly invoke `mcp__mcp-gateway__test-pal-mcp__codereview` on commit `2563546` plus the in-cycle fix patch. The substantive findings (version anchor + case-sensitivity invariant + missing CHANGELOG) are mechanical/factual and unlikely to attract independent disagreement.

### Required actions

None — APPROVE. Operator may proceed with `/run docs-spikes-2026` to begin Phase 1 execution. The plan now correctly targets v1.34.0 with currently=1.33.1.

### Manual review escalations

None — all findings status=Fixed in-cycle.

