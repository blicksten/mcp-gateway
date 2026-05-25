# Pre-run methodology review — 2026-05-24

**Reviewer:** Sonnet 4.6 sub-agent (code-reviewer)
**Mode:** PAL-equivalent critical review (codereview + thinkdeep)
**Why fallback:** PAL MCP unavailable in parent session (mcp__pal__* not in deferred tools list); orchestrator MCP also unavailable. Per CLAUDE.md fallback rule: Opus session → Sonnet sub-agent.

## Verdict

**APPROVE WITH FINDINGS** — 0 CRITICAL, 3 HIGH, 6 MEDIUM, 5 LOW, 3 INFORMATIONAL.

The packet is structurally sound and the 3-axis framework is the right first-cut decomposition. None of the findings are fatal; several would cause silent misfires on the first real run if not fixed before Phase 0 of the implementation plan.

## Blocking before Phase 0 implementation

- **H1** — `inputs/claims.yaml` `implementation_file` paths are wrong (`api/auth.go` does not exist; the actual auth package is `internal/auth/`; `api/patchstate.go` should be `internal/patchstate/state.go` + `internal/api/claude_code_handlers.go`; meta-tools live at `internal/proxy/gateway.go`). When `03-doc-diff.ps1` validates paths, every claim will flag as DOC_LIE against a non-existent path, contaminating the first baseline.
- **H2** — `scripts/00-prereq-check.ps1` uses `Invoke-Expression` on tool-probe strings (code-smell + future injection vector) AND probes `deadcode` with `-help` flag which exits non-zero, so `deadcode` will ALWAYS appear as NOT_INSTALLED even when present, silently degrading the reachability step.
- **H3** — `scripts/05-gap.ps1` reads outputs of skeleton steps and `run-all.ps1` exits 0 on every successful skeleton step. An operator could mistakenly record a skeleton run as the real 2026-05-24 baseline. No `SKELETON_RUN=true` guard exists.

## Fix-soon (Medium)

- **M1** — `99-bootstrap-next.ps1` copies `claims.yaml` forward; stale `proven` status carries to next quarter with no validation that impl_file:line still exists.
- **M2** — `tool-versions.txt` is copied forward by bootstrap, masquerading as a version-pin but unenforced. `manifest.json.tool_versions` is never populated.
- **M3** — Missing 4th axis: **correctness**. Axis 2 (runtime exercise) only proves a function was called, not that it returned correct output. Critical for persistence/auth/data-transformation claims.
- **M4** — `scn-daemon-restart-state-replay` doesn't verify loaded-state equals saved-state. Will declare T0.7.1 PROVEN even if disk persistence is silently lossy.
- **M5** — All 13 scenarios are happy-path. Zero fault-injection (backend crash, corrupted sessions.json, KeePass locked, duplicate-name registration, concurrent renames).
- **M6** — RTM only validates docs→code direction. Load-bearing internal invariants (patchstate TOCTOU atomicity, supervisor crash-restart contract) have no claims.

## Lesser issues (Low + Info)

- **L1** `run-all.ps1` shadows automatic `$args` — rename to `$stepArgs`
- **L2** `06-drift.ps1` exits 1 on missing predecessor — blocks subsequent audits unfairly; should warn and exit 0
- **L3** `manifest.json.git_head_at_start/end` never populated by any script
- **L4** `Out-File -Encoding utf8` adds BOM on PS 5.1 — use `utf8NoBOM` or `[IO.File]::WriteAllText`
- **L5** `INSTRUCTIONS.md` §14 references `.github/workflows/verification-audit.yml` and `docs/PLAN-verification-protocol.md` as if active; both are future deliverables
- **I1** "PROVEN" label in `verdict.md` template needs inline reminder that PROVEN ≠ correctness
- **I2** `scn-scalability-3x16` has no quantitative pass/fail for the 30s connection window
- **I3** `scn-mcp-rehydrate-recovery` automation goal stated in §13 may be infeasible without Claude Code telemetry endpoint

## Closing assessment

The methodology is a genuine attempt to close a real gap — not cargo-culting. The 3-axis framework directly addresses the user's stated frustration: "unit tests prove code exists, not that it is wired and exercised." The frontier disclaimer is honest. The skeleton labelling is clear. The historical-record-via-copy-forward design is sound.

What will burn the user in 6 months is the combination of [H1] + [H3]: the first end-to-end run of `run-all.ps1` (once Phase 0 is implemented) will produce a flood of false DOC_LIE findings due to wrong `implementation_file` paths, and if skeleton outputs are mistakenly committed as a "baseline" audit result, the drift report will flag the entire TS inventory as newly-appeared functions. Both outcomes erode trust in the audit faster than not having it at all. Fix [H1] and add the skeleton-run guard from [H3] before implementing Phase 0.
