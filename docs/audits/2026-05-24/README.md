# Audit packet — 2026-05-24

This folder is a **self-contained, reproducible Periodic V&V Conformance Audit** packet for mcp-gateway.

Everything needed to reproduce, understand, or audit-the-audit is in this folder. No external dependencies.

## Quick start (standalone mode)

```powershell
pwsh ./scripts/run-all.ps1
# Read outputs/verdict.md after completion
```

## Other execution modes

- **CI:** `.github/workflows/verification-audit.yml` (added in Phase 8 of plan) calls the same `scripts/*.ps1`.
- **Orchestrator (opt-in):** `mcp__orchestrator__start_pipeline(pipeline_type="verification-audit", audit_path="docs/audits/2026-05-24")`.

## Reading order

1. `INSTRUCTIONS.md` — the protocol (frozen at this audit's date). Read this first.
2. `inputs/` — what was given to the scripts (RTM, scenarios, manual checklist).
3. `scripts/` — what was run.
4. `outputs/` — what came back.
5. `outputs/verdict.md` — bottom-line result.

## Next audit

After this audit completes, run:

```powershell
pwsh ./scripts/99-bootstrap-next.ps1
```

This creates the next audit folder (e.g. `docs/audits/2026-08-24/`) by copying THIS folder's `INSTRUCTIONS.md`, `scripts/`, and `inputs/` forward. The new folder is then independently runnable; this folder remains frozen as a historical record.

## Anchors

- IEEE 1012-2016 (V&V)
- ISO/IEC/IEEE 29148 (Requirements Traceability Matrix)
- ISO/IEC/IEEE 29119 (Software testing)
- Living Documentation (Martraire, 2019)
- Specification by Example (Adzic, 2011)

See `INSTRUCTIONS.md` § Methodology for full citations.
