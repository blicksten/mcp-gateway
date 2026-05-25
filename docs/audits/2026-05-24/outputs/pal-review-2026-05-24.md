# PAL codereview — 2026-05-24

**Tool:** `test-pal-mcp__codereview` via `mcp__mcp-gateway__gateway_invoke` (workaround route)
**Expert model:** gpt-5.1-codex (OpenAI)
**Mode:** external validation, gate_mode=true, thinking_mode=high, review_type=full
**Continuation ID:** c4760c0b-3e93-41dd-8d99-68499bdc33e8

## Gate verdict: **HALT**

10 blocking findings — `{'high': 4, 'medium': 6, 'low': 5}`

## HIGH (4 — blocking)

1. **[A] Ritualization risk** — `verdict.md` is a template, not an enforced workflow. Nothing forces operator triage between audits. Quarterly cadence without forced triage = false-comfort ritual. PAL upgraded this from Sonnet's "medium structural concern" to HIGH.
2. **[H1-confirmed] claims.yaml fabricated paths** — `api/auth.go`, `api/patchstate.go` do not exist; real paths are `internal/auth/admin.go`, `internal/patchstate/state.go`. First run of `03-doc-diff` produces flood of false DOC_LIE.
3. **[H3-confirmed + extended] run-all.ps1 exits 0 on every skeleton step** — no `SKELETON_RUN=true` guard. Combined with [A], FIRST baseline could be permanently wrong.
4. **[D-upgraded] 01-inventory.ps1 regex provably wrong even at skeleton level** — pattern `^func\s+(\([^)]+\)\s+)?(\w+)` misses multi-line method receivers, generic functions `func (s *Foo[T])`, doesn't distinguish `func init()`. Silently incomplete inventory contaminates `05-gap`. PAL upgraded this from Sonnet's LOW skeleton-level note to HIGH structural.

## MEDIUM (6)

- **[B]** RTM drift — `claims.yaml` itself decays; protocol has no "claim with `last_verified=null` after 2+ audits" detector
- **[C]** Observability axis missing — coverage records function ran but no trace/log artifact for diagnosis
- **[E]** "Frozen at point of run" weaker than claimed — external tool binaries (deadcode, knip, Go) PATH-resolved not pinned
- **[F]** Manual scenarios lack mandatory captured artifacts — operators can disagree with no audit trail
- **[H2-confirmed]** `Invoke-Expression` + `deadcode -help` returns exit 2 → false NOT_INSTALLED
- **[M5-confirmed + new]** Missing lifecycle scenarios: backend-MCP-crash-mid-session, daemon-restart-mid-PAL-call, orphaned-marker-cleanup-during-active-session

## LOW (5)

- PROVEN label propagates to `verdict.md` without inline qualification
- `Out-File -Encoding utf8` BOM in `99-bootstrap-next.ps1:74`
- `manifest.json.git_head_*` never populated
- INSTRUCTIONS.md §14 references CI YAML + plan file as if active
- `06-drift.ps1` exits 1 on missing predecessor (should warn + exit 0)

## PAL expert recommendations (raw)

> Top 3 Priority Fixes:
> - Ввести дополнительную ось проверки корректности/инвариантов и собираемые артефакты, иначе «выполненный» код может возвращать мусор.
> - Устранить ложные RTM пути и добавить в run-all стопор для SKELETON-скриптов, чтобы baseline вообще имел смысл.
> - Переписать 01-inventory на AST‑уровень и закрыть coverage за реальные сценарии (включая краш/рестарт), иначе 05-gap не может классифицировать функции верно.

## Comparison Sonnet vs PAL

| Finding | Sonnet 4.6 (fallback) | PAL gpt-5.1-codex |
|---|---|---|
| Ritualization [A] | not separately flagged | **HIGH** (new + blocking) |
| Claims.yaml paths [H1] | HIGH | HIGH (confirmed) |
| SKELETON guard [H3] | HIGH | HIGH (confirmed) |
| 01-inventory regex [D] | LOW (skeleton-level OK) | **HIGH** (upgraded — structural) |
| Correctness axis [M3/C] | MEDIUM (M3, nice-to-have) | MEDIUM (C, structural — must add 4th axis) |

PAL adds rigor on two points: ritualization is **blocking**, and the inventory grep bug is **structural** (skeleton-level "good enough" labelling is wrong because incomplete inventory contaminates downstream).

## Process note

Direct deferred tools `mcp__test-pal-mcp__*` are not surface'd in this Claude Code session despite `.claude.json` containing all 5 gateway-mediated backends. The `claude-config-sync` extension wrote the entries mid-session at 16:16; Claude Code does not re-read `~/.claude.json` mid-session. Workaround used: `mcp__mcp-gateway__gateway_invoke(backend="test-pal-mcp", tool="codereview", args={...})` — this is the exact use case the meta-tool was designed for. Permanent fix (for direct access): VSCode "Developer: Reload Window" (cheaper than `/clear`).
