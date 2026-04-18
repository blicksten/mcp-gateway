# Plan: Server & Command Catalogs (v1.5.0)

## Session: catalogs → `docs/PLAN-catalogs.md`

## Context

Phase 14 (Community & CI) of `PLAN-main.md` shipped the hardening half
(SECURITY.md, gitleaks, README, CI job) in commit `29e6fc2` on
2026-04-18. The catalog half — server catalog, command catalog, browse
UI in `AddServerPanel`, slash-command template enrichment — was
deliberately deferred out of `PLAN-main.md` because it is substantial
UX + schema design that warrants its own plan rather than being
squeezed into a hardening release.

This plan picks up that deferred work for v1.5.0.

## Dependency Graph

```
catalog.A (schema + files) → catalog.B (Add Server browse) → catalog.D (GATE)
catalog.A                 → catalog.C (slash-command enrichment) → catalog.D
```

---

## Phase catalog.A — Catalog format + seed files

**Goal:** Define a stable JSON schema for server and command catalogs and ship a first-party seed.

- [ ] CA.1 — `docs/catalog/schema.server.json` — JSON Schema for one server catalog entry (name, display_name, transport, url/command, env_keys, header_keys, homepage, tags, description, default_config).
- [ ] CA.2 — `docs/catalog/schema.command.json` — JSON Schema for one command catalog entry (server_name, command_name, description, template (Markdown), required_vars, suggested_vars).
- [ ] CA.3 — `docs/catalog/servers.json` — Seed with the common MCP servers (context7, pdap-docs, orchestrator, pal-mcp, sap-gui-control).
- [ ] CA.4 — `docs/catalog/commands.json` — Seed slash-command templates pairing 1-N commands to each catalog server.
- [ ] CA.5 — Go-side loader in new `internal/catalog/` package (ReadServers, ReadCommands, Validate against the schemas).
- [ ] CA.6 — Unit tests: schema validation, round-trip load, malformed file rejection, empty-catalog handling.
- [ ] CA.GATE — Tests + codereview + thinkdeep — zero MEDIUM+.

**Files:** new `docs/catalog/`, new `internal/catalog/`.

---

## Phase catalog.B — Catalog browse in Add Server webview

**Goal:** Let operators pick a catalog server from a dropdown in the existing `AddServerPanel` instead of re-entering URL/command/env keys from memory.

- [ ] CB.1 — Extension-side catalog loader `src/catalog.ts` — fetch from `docs/catalog/*.json` at workspace root OR a `mcpGateway.catalogPath` setting; refuse network fetches by default (principle: supply chain hygiene).
- [ ] CB.2 — `AddServerPanel` gains a "Choose from catalog" dropdown that pre-fills the form fields from the selected entry.
- [ ] CB.3 — Per-entry env_keys / header_keys listed as empty inputs so the operator fills only the secrets, not the structure.
- [ ] CB.4 — Mocha tests: catalog parse, pre-fill behaviour, malformed catalog surfaces as a warning without breaking the form, in-flight submit guard still holds under catalog re-selection.
- [ ] CB.GATE — Tests + codereview + thinkdeep — zero MEDIUM+.

**Files:** new `vscode/mcp-gateway-dashboard/src/catalog.ts`, `vscode/mcp-gateway-dashboard/src/webview/add-server-panel.ts`, `vscode/mcp-gateway-dashboard/src/webview/html-builder.ts`, tests.

---

## Phase catalog.C — Slash-command template enrichment

**Goal:** `SlashCommandGenerator` currently writes a bare skeleton. With a catalog, it can inject per-server command templates the moment a server starts, matching the slash-command UX of other MCP tooling.

- [ ] CC.1 — Extend `SlashCommandGenerator` to read the commands catalog (best-effort; missing catalog → fall back to skeleton).
- [ ] CC.2 — Template variable resolution: `${server_name}`, `${server_url}` substitutions resolved at write time.
- [ ] CC.3 — Existing magic-header marker still gates overwrite/delete so user-hand-edited files are not clobbered.
- [ ] CC.4 — Mocha tests: catalog-enriched template write, no catalog (skeleton fallback), user-edited file preserved, template variable substitution.
- [ ] CC.GATE — Tests + codereview + thinkdeep — zero MEDIUM+.

**Files:** `vscode/mcp-gateway-dashboard/src/slash-command-generator.ts`, tests.

---

## Phase catalog.D — Integration + release

- [ ] CD.1 — README update: "Catalogs" section with example catalog fragment and the Add Server browse screenshot.
- [ ] CD.2 — CHANGELOG.md v1.5.0 entry.
- [ ] CD.3 — VSIX rebuild + commit.
- [ ] CD.GATE — Full PAL codereview across `internal/catalog/` + extension changes — zero MEDIUM+.

---

## Open questions

- Should the catalog be hosted in-repo only, or also published to a separate `mcp-gateway-catalog` repository so the community can PR entries without touching the daemon repo? (Recommend: in-repo for v1.5; split repo only if PR volume demands it.)
- Should the extension ever hit a remote catalog URL, or strictly a local file? (Recommend: local only, with a `mcpGateway.catalogPath` setting — operators who want a remote catalog can pre-fetch it.)
- How does catalog evolution interact with `$schema` version bumps? (Recommend: JSON Schema `$id` carries a semantic version; the Go loader rejects unknown major versions.)

## Rollback

Each phase is additive — pure additions to a new package and new extension module. Rollback = revert the commit + remove the catalog files. No existing behaviour changes if the catalog is absent.
