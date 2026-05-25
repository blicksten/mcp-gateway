# Periodic V&V Conformance Audit — Instructions

**Project:** mcp-gateway
**Audit date:** 2026-05-24
**Protocol version:** 1.0
**Cadence:** quarterly (next: 2026-08-24)

This document is **frozen at the date of this audit**. Future protocol revisions will appear in future audit folders. This folder always documents what was considered correct on 2026-05-24.

---

## 1. Why this audit exists

Unit tests prove code **compiles** and isolated functions return correct values **for mocked inputs**. They do NOT prove:

- That production code paths are reached by real flows (wired correctly)
- That every function in the deployed binary is exercised (no dead code disguised as covered)
- That documentation reflects what the code actually does (no drift between docs and reality)

This audit closes those three gaps on a quarterly cadence. It is **not a replacement** for unit tests — it is an orthogonal verification layer.

## 2. Methodology — what this audit checks (4 axes)

| Axis | Question | Method |
|---|---|---|
| **1. Reachability** | Is every function in the codebase reachable from an entry point? | Static call-graph analysis (Go `deadcode`, TS `knip`) |
| **2. Runtime exercise** | Is every reachable function executed by ≥1 named real-operator scenario? | Instrumented binary (Go `-cover`, V8 PreciseCoverage) + scenario runs |
| **3. Doc-code conformance** | Does every documented claim have an implementation, and vice versa? | RTM (`inputs/claims.yaml`) + mechanical extraction of endpoints/env/config + diff |
| **4. Correctness** | Does each scenario assert the documented behavior, not just that the function was called? | Every scenario in `inputs/scenarios.md` MUST have an `assertions:` block; manual scenarios MUST attach a captured artifact (log, snapshot, screenshot) in `inputs/operator-manual-checklist.md` |

A function/endpoint/claim is **PROVEN** only when all four axes confirm it.

- **Axis 1 only** → reachable but latent (no scenario)
- **Axis 1+2** → executed but un-asserted (don't know if it did the right thing)
- **Axis 1+2+3** → executed + documented but correctness un-asserted (the executed code may still return wrong values)
- **Axis 1+2+3+4** → PROVEN

Anything failing one or more axes falls into one of: dead code, untested code, undocumented code, documentation lie, or **assertion-deficient scenario**.

**Why axis 4 is structurally necessary** (per PAL review 2026-05-24): coverage proves a function was called; it does NOT prove the function returned a correct value. For persistence claims (T0.7.1), auth claims (MCPR.3), and data-transformation claims (server-rename), a corrupted-but-non-crashing result would pass axes 1-3 while silently losing data. Axis 4 closes that gap by requiring scenarios to assert the documented behavior.

## 3. Methodology anchors (industry standards)

This protocol is grounded on the following standards. Citations are kept here so future operators can verify our methodology has not drifted from the field.

- **IEEE 1012-2016** — *Standard for System, Software, and Hardware Verification and Validation*. Defines V&V processes including in-service audits. <https://standards.ieee.org/ieee/1012/7324/>
- **ISO/IEC/IEEE 29148:2018** — *Requirements engineering*. Defines the Requirements Traceability Matrix (RTM) used in `inputs/claims.yaml`.
- **ISO/IEC/IEEE 29119** (multi-part, 2022+) — *Software testing — General concepts, processes, documentation, techniques, keyword-driven*.
- **ISO/IEC/IEEE 26511-26515** — *Information for users / software documentation* (testing & reviewing user docs included).
- **Living Documentation** — Cyrille Martraire, Addison-Wesley 2019. Patterns for self-describing code and evergreen artifacts.
- **Specification by Example** — Gojko Adzic, Manning 2011. Foundation for executable documentation via examples.
- **Software Reflexion Model** — Murphy & Notkin, FSE 1995. Academic foundation for code↔model conformance checking.
- **arc42** — open-source architecture documentation template. <https://arc42.org>
- **C4 model** — Simon Brown's 4-level visual notation. <https://c4model.com>

### Frontier disclaimer (be honest about this)

The **"every function in the deployed binary must be exercised by a named real-operator scenario"** discipline is not yet a canonical industry standard. Components are standardized (Go `-cover` for built binaries since Go 1.20, V8 PreciseCoverage via Chrome DevTools Protocol). The integration discipline — sewing function execution to scenario IDs — is ours. The closest named practice is *"observability-driven development"* (Honeycomb / Charity Majors), which measures business outcomes rather than function reach.

Re-evaluate at each audit whether the field has caught up.

## 4. Project profile (mcp-gateway specific)

- **Language stack:** Go (daemon, mcp-ctl) + TypeScript (VSCode extension + webview) + JavaScript (webview vanilla JS)
- **Entry points:**
  - Go: `cmd/mcp-gateway/main.go` (daemon), `cmd/mcp-ctl/main.go` (CLI)
  - TS: `vscode/mcp-gateway-dashboard/src/extension.ts` (VSCode activate)
- **Critical paths:** auth (two-tier admin/user), MCP stdio proxy, session orphan reaping, disk persistence (SessionStateRegistry), plugin reannounce, daemon lifecycle (cold start, crash, restart)
- **External dependencies of concern:** Claude Code VSCode plugin, VSCode 1.119 McpGatewayService (third-party shutdown attacker — see CLAUDE.md MCPR.3)

## 5. Tool selection (mcp-gateway specific, frozen for this audit)

### Go (daemon + mcp-ctl)

- Inventory: `go list ./...` + AST traversal via `go/parser`
- Static reachability: [`golang.org/x/tools/cmd/deadcode`](https://pkg.go.dev/golang.org/x/tools/cmd/deadcode) + `staticcheck -checks U1000`
- Call graph: [`golang.org/x/tools/cmd/callgraph -algo=cha`](https://pkg.go.dev/golang.org/x/tools/cmd/callgraph)
- Runtime coverage: [`go build -cover -coverpkg=./...`](https://go.dev/doc/build-cover) (Go 1.20+) → `GOCOVERDIR` → `go tool covdata`

### TypeScript (extension + webview)

- Inventory: ts-morph AST traversal
- Static reachability: [`knip`](https://knip.dev) (supersedes legacy `ts-prune`)
- Runtime coverage: V8 `Profiler.startPreciseCoverage` / `takePreciseCoverage` via [Chrome DevTools Protocol](https://chromedevtools.github.io/devtools-protocol/tot/Profiler/)
- Architecture conformance: [`dependency-cruiser`](https://github.com/sverweij/dependency-cruiser)

### Doc-code diff

- Mechanical (endpoints, env vars, config keys, CLI subcommands): regex-based, see `scripts/03-doc-diff.ps1`
- OpenAPI drift (if/when applicable): [`oasdiff`](https://www.oasdiff.com)
- Semantic claims: RTM in `inputs/claims.yaml` (ISO/IEC/IEEE 29148 anchored)

## 6. RTM format — `inputs/claims.yaml`

Each entry binds a documented semantic claim about mcp-gateway to its implementation and the named scenario that PROVES it:

```yaml
- claim_id: <short-kebab-case-id>
  source_doc: <relative-path>
  source_section: <heading-or-line-range>
  asserts: <one-sentence-claim — MUST be testable via a scenario assertion>
  implementation_file: <relative-path — MUST exist (validated by 03-doc-diff)>
  implementation_line: <int>
  implementation_symbol: <function/type name>
  scenario_id: <id from inputs/scenarios.md>
  verification_status: proven | unproven | drifted | doc_lie | needs_audit
  last_verified: <YYYY-MM-DD or null>
```

Status semantics:

- `proven` — impl + scenario found AND scenario asserted the behavior in latest run (axis 4 satisfied)
- `unproven` — impl found but no scenario covers it
- `drifted` — was proven; latest scenario run shows behavior changed
- `doc_lie` — no implementation found at impl_file:impl_line
- `needs_audit` — added or modified since last_verified

**Scope rule:** this file holds claims about mcp-gateway code ONLY. Cross-project claims (e.g. claude-team-control hooks like `mcp-rehydrate.sh` or `anti-passive-stop.py`) are tracked in those projects' own audit packets, per project isolation discipline (CLAUDE.md § Project & Pipeline Isolation).

`scripts/03-doc-diff.ps1` (Phase 1.5 implementation) does three things:

1. Mechanically extracts endpoints/env/config/CLI from docs and code; flags 3 buckets (docs-only / matched / code-only).
2. Validates `implementation_file:implementation_line` exists for every entry in `claims.yaml`. Missing path → `doc_lie`.
3. Validates each claim's `scenario_id` exists in `scenarios.md` AND that scenario has an `assertions:` block matching the `asserts:` text.

**RTM staleness check** (per PAL review B): claims with `last_verified` older than the predecessor audit's date are flagged in `outputs/gap-report.md` as `RTM_STALE` and require operator triage in `outputs/verdict.md`.

## 7. Scenarios — `inputs/scenarios.md`

Catalog of named operator scenarios. Each scenario has:

- `id` — stable identifier (referenced from `claims.yaml`)
- `type` — `auto` (runnable by `04-scenarios.ps1`) or `manual` (operator runs by hand, checks off in `operator-manual-checklist.md`)
- `name` — human-readable
- `steps` — reproducible operator instructions
- `expected_coverage` — files/functions this scenario is expected to touch
- `last_verified` — date

## 8. Step-by-step run

| Step | Script | What it does | Output |
|---|---|---|---|
| 0 | `00-prereq-check.ps1` | Verify go, node, knip, deadcode are installed | `outputs/prereq.log` |
| 1 | `01-inventory.ps1` | Enumerate every function in the codebase | `outputs/inventory.json` |
| 2 | `02-reachability.ps1` | Static dead-code analysis (Go deadcode + knip) | `outputs/reachability.json` |
| 3 | `03-doc-diff.ps1` | Mechanical endpoints/env/config/CLI extraction + diff against docs | `outputs/doc-code-diff.md` |
| 4 | `04-scenarios.ps1` | Build instrumented binaries, run auto scenarios, prompt for manual ones, collect coverage | `outputs/coverage.json` |
| 5 | `05-gap.ps1` | Merge inventory + reachability + coverage + RTM into 3-axis matrix | `outputs/gap-report.md` |
| 6 | `06-drift.ps1` | Diff vs the predecessor audit folder | `outputs/drift-vs-prior.md` |
| 7 | `07-verdict-check.ps1` | Verify `verdict.md` is fully triaged (one Decision per finding, signoff complete) | exit code only |
| 99 | `99-bootstrap-next.ps1` | (Run AFTER this audit is closed) clone forward to next quarterly | `docs/audits/<next-date>/` |

`run-all.ps1` sequences 00→07. Step 4 pauses for operator on manual scenarios. Step 7 blocks completion until operator has triaged every finding in `verdict.md` per the enforced workflow in §10. A run that bypasses verdict triage exits non-zero, leaving `manifest.status="incomplete"`.

**Skeleton run protection.** Each of 01–06 writes its `meta.status` field into its output JSON. `run-all.ps1` reads these after every step. If ANY step's `status` contains the substring `SKELETON`, `manifest.skeleton_run` is set to `true` and a prominent `[SKELETON RUN — NOT A VALID BASELINE]` banner is printed at the end. The verdict.md generated will not be accepted as a baseline by future drift comparison (`06-drift.ps1` refuses to compare against a `skeleton_run=true` predecessor).

## 9. Outputs — how to interpret

- `outputs/inventory.json` — full function inventory (file:line:name for every Go/TS/JS function)
- `outputs/reachability.json` — REACHABLE set (subset of inventory)
- `outputs/coverage.json` — EXECUTED set (subset of reachability) tagged by scenario_id
- `outputs/doc-code-diff.md` — 3 columns: docs-only (lies), matched, code-only (undocumented)
- `outputs/gap-report.md` — 3-axis matrix per function: reachable? executed? documented? → verdict bucket
- `outputs/drift-vs-prior.md` — what changed since predecessor audit
- `outputs/verdict.md` — bottom line: count of PROVEN / UNDOCUMENTED / UNTESTED / DEAD / DOC-LIE entries + ESCALATED items requiring operator decision

A function appearing in `outputs/gap-report.md` as anything other than PROVEN means one of:

- **UNDOCUMENTED** → write a doc entry, optionally add to `inputs/claims.yaml`
- **UNTESTED** → add or extend a scenario in `inputs/scenarios.md`
- **DEAD** → delete the code (after confirming no external consumer)
- **DOC LIE** → either fix the doc or fix the wiring

## 10. Verdict — `outputs/verdict.md` — ENFORCED TRIAGE WORKFLOW

**`verdict.md` is NOT a free-form template.** It is a structured triage document produced by `scripts/05-gap.ps1` and required to be fully triaged by the operator before `run-all.ps1` will mark the audit complete.

**PROVEN ≠ correctness alone.** A finding marked PROVEN means all four axes (reachability, runtime exercise, doc-code conformance, **correctness assertion satisfied in scenario**) were confirmed. PROVEN does NOT mean the function is correct under all inputs — it means the scenario's documented assertion was met. Reviewers reading `verdict.md` in isolation MUST keep this caveat in mind (and the template embeds it at the top).

### Required structure (machine-checked by run-all.ps1 step 7)

```markdown
# Audit verdict — 2026-05-24

> **PROVEN caveat:** PROVEN = reachable + executed + documented + scenario-asserted.
> NOT a proof of correctness under all inputs. NOT a proof of absence of races,
> performance regressions, or security gaps outside the assertion text.

## Summary counts
- Total functions inventoried: <N>
- PROVEN: <N>
- UNDOCUMENTED: <N>
- UNTESTED (reachable but no scenario): <N>
- ASSERTION_DEFICIENT (executed but scenario lacks assertion): <N>
- REACHABLE_BUT_LATENT: <N>
- DEAD: <N>
- DOC_LIE: <N>
- RTM_STALE: <N>

## Drift vs predecessor
- New functions: <N>, of which PROVEN <N>
- Removed functions: <N>
- Status regressions: <N> (e.g. previously PROVEN, now UNTESTED)

## Triage table (MANDATORY — one row per non-PROVEN finding from gap-report.md)

| Finding ID | Bucket | Decision | Owner | Due | Justification (required for Deferred) |
|---|---|---|---|---|---|
| F-001 | UNDOCUMENTED | Fixed | <name> | this cycle | <e.g. "added entry to docs/ANALYSIS.md §X"> |
| F-002 | UNTESTED | Deferred | <name> | next audit | <e.g. "scenario requires Windows CI runner not yet available"> |
| F-003 | DEAD | Fixed | <name> | this cycle | <e.g. "deleted in commit <sha>"> |
| F-004 | DOC_LIE | Escalated | <name> | open question | <e.g. "doc claims X, impl does Y; need product decision"> |

Decision values: `Fixed` | `Deferred` | `Escalated`. **NO findings may remain untriaged.**

## Operator signoff

- Date: <YYYY-MM-DD>
- Operator: <name>
- All findings triaged: [ ] (run-all.ps1 step 7 verifies this checkbox is `[x]`)
- Captured artifacts for all manual scenarios attached: [ ] (verified against inputs/operator-manual-checklist.md)

## Next audit due
2026-08-24
```

### Enforcement in run-all.ps1

After `06-drift.ps1` produces `outputs/gap-report.md`, `run-all.ps1` step 7 runs `07-verdict-check.ps1` which:

1. Parses `gap-report.md` to count non-PROVEN findings (N).
2. Reads `verdict.md` triage table; counts rows with non-empty Decision column (T).
3. If N ≠ T: exit non-zero with `[BLOCK] N findings, T triaged — verdict.md is incomplete`.
4. If "All findings triaged" checkbox is not `[x]`: exit non-zero with `[BLOCK] operator signoff missing`.
5. If any `Decision=Deferred` row has empty Justification: exit non-zero with `[BLOCK] Deferred finding F-XXX requires justification`.

This prevents the quarterly cadence from becoming a false-comfort ritual (per PAL review finding [A]). An audit is not closed until the operator has made an explicit Fixed/Deferred/Escalated call on every finding, with justification for deferrals.

## 11. Manual scenarios

Some scenarios cannot be automated (VSCode window-close cascade across 3 windows, real Claude Code session lifecycle, etc.). Operator runs these by hand from `inputs/operator-manual-checklist.md` and ticks them off. `04-scenarios.ps1` pauses and prompts for completion.

This is a **legitimate gap** in automation. Each audit may reduce manual count by automating one more scenario. Track this metric in `manifest.json.tool_versions` or extend `manifest.json` with `manual_scenario_count`.

## 12. Three execution modes

| Mode | Trigger | When to use |
|---|---|---|
| **Standalone** | `pwsh ./scripts/run-all.ps1` | Default. Local prerelease, manual quarterly run. |
| **CI** | `.github/workflows/verification-audit.yml` cron | Automated quarterly baseline capture. |
| **Orchestrator** | `mcp__orchestrator__start_pipeline(pipeline_type="verification-audit", audit_path="...")` | When you want PAL CV gate on `verdict.md` and per-step state tracking. |

**Critical design rule:** the scripts in `scripts/` do NOT depend on the orchestrator. The orchestrator pipeline is a thin wrapper that calls the same scripts and records `complete_step` after each. If orchestrator is broken, standalone mode still works.

This is intentional: the audit must not depend on a system it audits. If mcp-gateway and orchestrator both rely on shared infrastructure (Claude Code MCP plugin), a single failure cannot block both the operation and the verification.

## 13. Cadence and bootstrap

- **Cadence:** quarterly (every 3 months).
- **Next audit:** 2026-08-24.
- **Bootstrap next:** `pwsh ./scripts/99-bootstrap-next.ps1` (run AFTER this audit is closed). Creates `docs/audits/<next-date>/` by copying this folder's `INSTRUCTIONS.md`, `scripts/`, and `inputs/` forward. New folder is then independently runnable.
- **Update `docs/audits/latest`** symlink (or in Windows, a `latest.txt` file with the path) after bootstrap.

## 14. CI gate (continuous between audits) — STATUS: NOT YET IMPLEMENTED

> **Status:** `not_yet_implemented` as of 2026-05-24. Workflow file `.github/workflows/verification-audit.yml` and the parent plan `docs/PLAN-verification-protocol.md` are future deliverables (Phase 8 of the verification-protocol plan, which has not yet been authored). The text below describes the INTENDED design once Phase 8 lands.

Once implemented, the following CI checks run on every PR (separate from the quarterly cycle):

- New endpoint registered → CI fails if no entry in any doc or in `inputs/claims.yaml`
- New env var read → CI fails if not in README or `docs/`
- New ADR added → CI fails if no entry in `inputs/claims.yaml`
- Memory closure file mentions commit SHA → CI fails if `git cat-file -e <sha>` returns non-zero

These are the inverse of the quarterly audit: cheap, continuous, point-of-change gates that catch drift early so the quarterly audit has less work.

## 15. Adoption checklist (one-time per project, already done if this folder exists)

- [x] Tools installed (will be verified by `00-prereq-check.ps1`)
- [x] Initial inventory script available (`01-inventory.ps1`)
- [x] Initial scenarios drafted (`inputs/scenarios.md`)
- [x] Initial claims registry drafted (`inputs/claims.yaml`)
- [ ] First baseline captured (run `scripts/run-all.ps1` to produce)
- [ ] CI workflow added (Phase 8)
- [ ] Operator-manual scenarios verified by ≥1 hand-run (`inputs/operator-manual-checklist.md`)

---

*Frozen 2026-05-24. Future protocol revisions appear in future audit folders.*
