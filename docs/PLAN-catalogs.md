# Plan: Server & Command Catalogs (v1.5.0)

> Session: catalogs
> Pipeline: planning-7e15e160
> Created: 2026-04-19

## Overview

Phase 14 (Community & CI) shipped the hardening half of v1.4.0 in commit `29e6fc2`
(see `docs/ROADMAP.md:86-92`). The catalog half — first-party server catalog,
command catalog, browse-and-pre-fill UX in `AddServerPanel`, and slash-command
template enrichment — was deferred into this dedicated v1.5.0 plan because it is
substantial UX + schema work that would have inflated the hardening release.

**v1.5.0 delivers:** versioned JSON schemas for server and command catalogs, a
first-party seed (`docs/catalog/{servers,commands}.json`) covering five common
MCP servers, an extension-side loader that ships inside the VSIX with an optional
operator override, a "Choose from catalog" dropdown in the existing
`AddServerPanel` webview that pre-fills form fields, and slash-command template
enrichment that injects per-server templates the moment a server starts.

**Out of v1.5.0:** the previously planned Go-side `internal/catalog/` loader is
deferred — there is no daemon endpoint that serves catalog data and no roadmap
item that requires one. The extension is the sole consumer for v1.5.0. Hash-
augmented slash-command markers, remote catalog fetch, and a daemon
`GET /api/v1/catalog` endpoint are all listed in **Deferred Work** below.

This plan replaces the v1.4.0 backlog rows 14.3 and 14.4 (`docs/ROADMAP.md:109-110`).

## Audit Log

- **Round 1 (2026-04-19):** Lead auditor [Sonnet 4.6 + PAL gpt-5.2-pro CV]: REJECT
  with 2 HIGH + 5 MEDIUM + 4 LOW. Findings F-1..F-10 fixed in this revision:
  - F-1 (HIGH): D5 + CB.0 corrected — `add-server-panel.test.ts` already exists
    (41 it() blocks at `src/test/webview/`); CB.0 now extends, not creates.
  - F-2 (HIGH): CA.4b added — explicit `ajv@^8` / `ajv-cli@^5` / `ajv-formats@^3`
    dependency-add task before CA.5.
  - F-3 (MEDIUM): CA.5 tightened — `fs.stat` size precheck before any read,
    `readFile` spy assertion in CA.6.
  - F-4 (MEDIUM): CC.4 corrected — 25 → ≥30 (was 24 → ≥29 off-by-one).
  - F-5 (MEDIUM): CB.1 acceptance replaced unverifiable 50ms timing with
    observable assertions (init-message tick, synchronous handler) + manual
    smoke note.
  - F-6 (MEDIUM): CA.5 ajv config fully specified (`strict:true, allErrors:false,
    allowUnionTypes:false` + `ajv-formats` for `format:"uri"`).
  - F-7 (LOW): CA Rollback step 2 added — `package-lock.json` regen instructions.
  - F-8 (LOW): CC.1 cites `sap-tree-provider.ts:27` + `extension.ts:178` for
    config-watcher pattern.
  - F-9 (LOW): CB Rollback step 5 added — explicit "loader is read-only, no
    SecretStorage / globalState mutations" assertion.
  - F-10 (LOW): CB.4 acceptance split into automated manifest-schema check
    (CA.6) + manual settings-UI smoke.
- **Round 2 (2026-04-19):** Lead auditor [Sonnet 4.6 + PAL gpt-5.2-pro CV]:
  REJECT with 3 LOW (N-1, N-2, N-3) — propagation gaps from Round 1 fixes.
  All Round 1 findings F-1..F-10 verified Fixed. Round 2 fixes:
  - N-1 (LOW): Acceptance Criteria Matrix row CB updated — replaced "50 ms"
    hard criterion with the (a)-(d) observables from CB.1 body; sub-50 ms
    moved to a separate manual-smoke row.
  - N-2 (LOW): CA.6 acceptance bumped from "≥8" to "≥10 Mocha test cases
    (9 loader behaviour + 1 manifest-schema check for CB.4)"; TASKS row
    aligned to 10.
  - N-3 (LOW): CA.5 implementation contract pinned — `readFile` MUST be
    called via the `fs.promises.readFile` namespace (not destructured
    `import { readFile } from 'node:fs/promises'`) so the F-3 spy
    assertion in CA.6 is meaningful.
- **Round 6 — CC implementation gate (2026-04-20):** Phase catalog.C complete and gated. PAL thinkdeep (gpt-5.2-pro, async queue via orchestrator) PASS / 0 findings in 152 s. PAL codereview timed out twice at the MCP layer (30 s × 2) — same infrastructure pattern as CA.GATE + CB.GATE — fell back to internal cross-model review per CLAUDE.md rule: `code-reviewer` agent on Sonnet 4.6 APPROVE / 0 findings. Plan deviation: a shared helper `src/catalog-path.ts` (47 lines) extracted from AddServerPanel so the CB and CC code paths apply identical operator-override resolution. Refactor is minimal (CB keeps its `loadCatalogForPanel` static, only the inlined `resolveCatalogDir` moved) and eliminates the drift risk the reviewer would otherwise have flagged. Tests: 506 → 513 passing (+7), 31 pre-existing failures unchanged (zero regressions).
- **Round 4 — CA implementation gate (2026-04-19):** Phase catalog.A complete and gated. PAL thinkdeep (gpt-5.2-pro) PASS / 0 findings. PAL codereview timed out twice at infra level → internal cross-model fallback (Sonnet 4.6 code-reviewer agent) returned 1 MEDIUM PKG-1 (false positive — line-number misread; ajv-formats verified at `package.json:316` inside the `dependencies` block) + 1 LOW DOC-1 (plan referenced superseded `out/scripts/...` path; fixed). Additional gate fix found during VSIX audit (not by reviewers): `.vscodeignore` excluded `node_modules/**` wholesale, which would have stripped ajv runtime deps from installed VSIX → added negation entries for the 6 runtime packages and CI-step assertion. Plan deviation logged: ajv-formats in `dependencies` (not `devDependencies` as plan letter said) — required for runtime VSIX inclusion.
- **Round 3 / CV-gate dispute (2026-04-19):** CV-gate consensus DISPUTE.
  PAL `mcp__pal__consensus` (gpt-5.2-pro against, gpt-5.1-codex neutral) —
  pro raised 5 concrete plan-level defects, codex defended as ready. Pro's
  defects (D-1..D-5) all fixed:
  - D-1: Phase A↔B acceptance dependency loop. Manifest-schema check moved
    out of CA.6 (which depended on CB.4 artifacts) into CB.4's own Mocha
    test in `src/test/webview/add-server-panel.test.ts`.
  - D-2: `check-catalog-refs.ts` CI execution mechanics — switched to
    plain JavaScript (`scripts/check-catalog-refs.js`) so CI never has to
    run a TS executor; alternative compile-then-run path documented for
    teams preferring TS authoring.
  - D-3: ajv-cli command pinned in CA.6 to
    `ajv validate --spec=draft7 --strict=true -s <schema> -d <data>` —
    no implementation ambiguity.
  - D-4: VSIX packaging — added new task CA.7. Verifies `.vscodeignore`
    does NOT exclude `docs/catalog/**` (uses `!docs/catalog/**` negation
    if `docs/**` is excluded); requires `npx vsce ls | grep docs/catalog/`
    to return 4 lines; CA.6 packaged-VSIX-fixture test confirms runtime
    resolution after packaging.
  - D-5: TOCTOU between `fs.stat` and `fs.readFile` — replaced split call
    with `fs.promises.open` + `fileHandle.stat` + bounded `fileHandle.read`
    (single open handle, no swap window). Added bounded-read truncation
    test to CA.6 for the sparse-file edge case.

## Decisions Locked Before Planning

These five resolutions of the architect's open questions are firm defaults for v1.5.0
and must not be re-opened during implementation:

| # | Decision | Rationale |
|---|----------|-----------|
| D1 | Go-side catalog loader is OUT of v1.5; no `internal/catalog/` package. The extension is the only consumer. | No daemon endpoint surfaces catalog data on the v1.5 roadmap. A Go loader returns when (and only when) a daemon endpoint advertises catalog data — see Deferred Work. |
| D2 | The extension ships `docs/catalog/{servers,commands}.json` inside the VSIX `extensionPath`. The new `mcpGateway.catalogPath` setting (`scope: "machine"`, `default: ""`) optionally overrides; operator path wins when non-empty. | Bundling guarantees a usable catalog on first install. Per-machine override supports operators with curated internal catalogs without enabling per-workspace exfiltration vectors. |
| D3 | Slash-command marker stays binary (line-1 magic header) for v1.5. Below-line-1 edit loss is a documented known limitation. | Keeps Phase 11.E `SlashCommandGenerator` semantics unchanged (`vscode/mcp-gateway-dashboard/src/slash-command-generator.ts:167-189` skeleton path). Hash-augmented marker is a v1.6 candidate. |
| D4 | JSON Schema `$id`s are `https://mcp-gateway.dev/schema/catalog/server.v1.json` and `https://mcp-gateway.dev/schema/catalog/command.v1.json`, NEVER network-resolved. Validators are configured with the local schema files; document this explicitly in CA.5 + README. | Pinning by `$id` lets v2 schemas coexist; refusing network resolution preserves the supply-chain hygiene principle inherited from D1. |
| D5 | `AddServerPanel` already has a 41-`it()` test fixture at `vscode/mcp-gateway-dashboard/src/test/webview/add-server-panel.test.ts` (mock-vscode harness, `simulateSubmit` / `latestPanel` helpers, `createTrackingClient`). CB.0 is a small **extension** of that file (catalog-aware fixtures + 3 baseline catalog regression cases), not a new harness. CB.5 layers full catalog assertions on top. | Audit-corrected (lead-auditor F-1, 2026-04-19): an earlier draft asserted no harness existed — verified false. The harness is reused, not rebuilt. |

## Dependency Graph

```
catalog.A (schemas + seeds + TS loader + ajv CI lint)
        │
        ├── catalog.B
        │      ├── CB.0 (panel test harness — prereq)
        │      └── CB.1..CB.5 (dropdown, pre-fill, host re-validate, setting, tests)
        │
        └── catalog.C (slash-command template enrichment)
                │
                └── catalog.D (README + CHANGELOG + VSIX + final security codereview)
```

CB and CC both depend on CA (loader API, schema files, seed data); they are
independent of each other and may be implemented in parallel. CD strictly
follows both.

---

## Phase catalog.A — Schemas, seeds & TS-side loader

**Goal:** Define stable JSON Schemas for server and command catalogs, ship a
first-party seed, and provide a defensive extension-side loader with CI-enforced
schema validation.

- [x] CA.1: `docs/catalog/schema.server.json` — JSON Schema draft-07 for one
      server catalog entry. `$id` pinned to
      `https://mcp-gateway.dev/schema/catalog/server.v1.json`. Required fields:
      `name`, `display_name`, `transport` (enum: `["http", "stdio"]`),
      `description`. Optional fields: `url` (required when `transport=http`),
      `command` + `args` (required when `transport=stdio`),
      `env_keys` (string array), `header_keys` (string array), `homepage`
      (URL), `tags` (string array), `default_config` (object). `additionalProperties: false`.
      **Acceptance:** `npm run lint:catalog` (ajv-cli) validates
      `docs/catalog/servers.json` against this schema and exits 0 in CI.
- [x] CA.2: `docs/catalog/schema.command.json` — JSON Schema draft-07 for one
      command catalog entry. `$id` pinned to
      `https://mcp-gateway.dev/schema/catalog/command.v1.json`. Required
      fields: `server_name`, `command_name`, `description`, `template_md`
      (Markdown body). Optional fields: `required_vars` (string array),
      `suggested_vars` (string array). `additionalProperties: false`.
      Cross-reference rule: every `server_name` MUST exist in
      `servers.json` — enforced by the cross-check script in CA.6, NOT by the
      schema itself (JSON Schema cannot express cross-document refs cleanly).
      **Acceptance:** ajv-cli validates `docs/catalog/commands.json` against this
      schema and exits 0; cross-check script reports zero unresolved
      `server_name` references.
- [x] CA.3: `docs/catalog/servers.json` — Seed array containing entries for
      `context7`, `pdap-docs`, `orchestrator`, `pal-mcp`, `sap-gui-control`.
      Each entry populates the realistic transport/url/command/args/env_keys
      values for that server (operator still supplies secret values). Include
      at least one stdio entry and one http entry to exercise both transport
      branches. **Acceptance:** every entry passes `schema.server.json`
      validation under `npm run lint:catalog`.
- [x] CA.4: `docs/catalog/commands.json` — Seed array containing at least one
      command entry per seed server (5 entries minimum). `template_md` bodies
      contain at least one variable substitution to exercise CC.2.
      **Acceptance:** every entry passes `schema.command.json` validation;
      every `server_name` resolves to an entry in `docs/catalog/servers.json`.
- [x] CA.4b: Add `ajv@^8` to `dependencies` and `ajv-cli@^5` + `ajv-formats@^3`
      to `devDependencies` in `vscode/mcp-gateway-dashboard/package.json`. Run
      `npm install`, commit the updated `package-lock.json` together with
      `package.json`. **Acceptance:** `npm ci && npm run compile` exits 0 with
      no missing-module errors against a clean clone.
- [x] CA.5: `vscode/mcp-gateway-dashboard/src/catalog.ts` — exports
      `loadServersCatalog(path?: string): Promise<{ entries: ServerEntry[]; warnings: string[] }>`
      and `loadCommandsCatalog(path?: string): Promise<{ entries: CommandEntry[]; warnings: string[] }>`.
      Behaviour contract: NEVER throws (returns `{ entries: [], warnings: [...] }`
      on every error class); **size precheck via `fs.promises.stat()` BEFORE any
      read** — when `stat.size > 1_048_576` bytes, return immediately with a
      warning and never invoke `readFile` (audit-mandated: lead-auditor F-3, to
      eliminate OOM risk on multi-GB attacker file); NEVER performs network
      fetch (`$id` is documentation only — validators are pre-configured with
      the bundled schema files via `ajv.addSchema(require(localPath))`, the URL
      is never resolved); validates with `ajv@^8` configured as
      `{ strict: true, allErrors: false, allowUnionTypes: false }` plus
      `addFormats(ajv)` from `ajv-formats` to support `format: "uri"` on the
      `homepage` field; rejects any `$id` whose major version segment differs
      from `v1` (regex-free string parse: split on `.v`, take suffix, compare
      `startsWith("1.")` or equals `"1"`). Implementation MUST call `readFile`
      via the `fs.promises.readFile` namespace (NOT a destructured
      `import { readFile } from 'node:fs/promises'`) so the F-3 spy assertion
      in CA.6 is meaningful regardless of import style.
      **TOCTOU hardening:** the size precheck uses `fs.promises.open(path, 'r')`,
      then `fileHandle.stat()`, then a bounded `fileHandle.read()` of at most
      `1_048_576` bytes — a single open file handle eliminates the swap window
      between `stat` and read. Loader closes the handle in a `finally` block.
      **Acceptance:** unit tests in CA.6 cover all error classes plus a
      `fs.promises.readFile` spy assertion that confirms `readFile` is NEVER
      called when `stat.size > 1 MiB`. Additional test: bounded-read truncation
      kicks in when the open file's reported size is ≤1 MiB but the actual
      stream exceeds 1 MiB (sparse-file edge case).
- [x] CA.6: `vscode/mcp-gateway-dashboard/src/test/catalog.test.ts` — Mocha
      tests covering: roundtrip parse, malformed JSON, schema mismatch, oversize
      (>1 MiB) **with `fs.promises.readFile` spy asserting zero invocations**,
      `ENOENT`, **operator path set but directory exists with no `servers.json`**
      (must warn, return empty entries, not crash), unsupported `$id` major
      version (v2), cross-reference validation (commands referencing missing
      servers), mixed servers+commands batch load. Plus `npm run lint:catalog`
      script registered in `vscode/mcp-gateway-dashboard/package.json:303-311`
      `scripts` block. **Pinned command (no ambiguity):**
      `ajv validate --spec=draft7 --strict=true -c ajv-formats -s docs/catalog/schema.server.json -d docs/catalog/servers.json && ajv validate --spec=draft7 --strict=true -c ajv-formats -s docs/catalog/schema.command.json -d docs/catalog/commands.json && node scripts/check-catalog-refs.js`
      (run via `npm run lint:catalog` from `vscode/mcp-gateway-dashboard/`).
      The `-c ajv-formats` flag is required so ajv-cli loads the format
      validators at lint time (the schemas use `format:"uri"` on the
      `homepage` field). CI workflow runs the same script.
      Cross-reference check is **plain JavaScript** at
      `vscode/mcp-gateway-dashboard/scripts/check-catalog-refs.js` (NOT TypeScript,
      to avoid runtime-TS-executor questions in CI — per CV-gate Round 3
      finding D-2). The implemented `lint:catalog` script invokes
      `node scripts/check-catalog-refs.js` (source path, plain JS).
      **Acceptance:** ≥9 loader-behaviour Mocha test cases pass (the
      manifest-schema check for CB.4 lives under CB.4 itself, not CA.6 — see
      CB.4 acceptance — to break the Phase A↔B dependency loop flagged by
      CV-gate Round 3 D-1); CI step exits 0 against the seeds and exits
      non-zero when a deliberately broken fixture is staged.
- [x] CA.7: VSIX packaging audit. Verify
      `vscode/mcp-gateway-dashboard/.vscodeignore` does NOT exclude
      `docs/catalog/**` (or `docs/**`). If `docs/**` is currently excluded,
      add a negation entry `!docs/catalog/**` (vsce supports glob negation).
      Run `npx vsce ls` and confirm `docs/catalog/schema.server.json`,
      `docs/catalog/schema.command.json`, `docs/catalog/servers.json`,
      `docs/catalog/commands.json` all appear in the file list.
      **Acceptance:** `npx vsce ls | grep docs/catalog/` returns 4 lines
      (one per catalog file); CA.6 unit test loads from a packaged-VSIX
      fixture path to confirm runtime resolution works after packaging.
- [x] CA.GATE: tests + codereview + thinkdeep — zero errors (any finding at or above CLAUDE_GATE_MIN_BLOCKING_SEVERITY; default: any finding)
  - **Tests:** catalog.test.ts — 17/17 passing in 266 ms isolated run. Full suite reports 495 passing / 31 failing — the 31 failures (gateway-client + log-viewer) were verified pre-existing by stashing all CA changes and re-running gateway-client.test.ts against the clean baseline (still 13+ failures). CA introduces zero regressions.
  - **Lint:** `npm run lint:catalog` exits 0 — both seed files valid, 5 commands reference 5 servers.
  - **Compile:** `npm run compile` clean.
  - **VSIX audit:** `npx vsce ls` shows 4 `docs/catalog/*` files + 6 runtime deps (ajv, ajv-formats, fast-deep-equal, fast-uri, json-schema-traverse, require-from-string) after `.vscodeignore` negation entries. End-to-end smoke test (`node -e "require('./out/catalog').loadServersCatalog(...)"` against compiled output) returns 5 entries / 0 warnings.
  - **PAL thinkdeep (gpt-5.2-pro):** PASS, 0 findings (175 710 ms).
  - **PAL codereview:** PAL MCP timed out twice (30 s × 2 attempts) — infrastructure issue on the codereview pipeline specifically; thinkdeep on the same queue succeeded. Fell back to internal cross-model review per CLAUDE.md rule: **code-reviewer agent on Sonnet 4.6**. Verdict: 1 MEDIUM (PKG-1 — *false positive*, reviewer misread package.json line numbers; ajv-formats IS in `dependencies` at line 316, inside the `dependencies` block that opens at line 314) + 1 LOW (DOC-1 — plan's pinned command referenced superseded `out/scripts/...` path; fixed in this revision).
  - **Additional gate fix (found during VSIX audit, not by reviewers):** `.vscodeignore` excluded `node_modules/**` wholesale, which would have stripped ajv + ajv-formats + transitives from the installed extension. Added negation entries for the 6 runtime packages; CI step now asserts their presence in every build.

**Files touched:**
- `docs/catalog/schema.server.json` (new)
- `docs/catalog/schema.command.json` (new)
- `docs/catalog/servers.json` (new)
- `docs/catalog/commands.json` (new)
- `vscode/mcp-gateway-dashboard/src/catalog.ts` (new)
- `vscode/mcp-gateway-dashboard/src/test/catalog.test.ts` (new)
- `vscode/mcp-gateway-dashboard/scripts/check-catalog-refs.js` (new — plain JS for CI portability)
- `vscode/mcp-gateway-dashboard/.vscodeignore` (audit + amend so `docs/catalog/**` is NOT excluded — see CA.7)
- `vscode/mcp-gateway-dashboard/package.json` (`scripts.lint:catalog` + `dependencies.ajv` + `devDependencies.ajv-cli` + `devDependencies.ajv-formats`)
- `vscode/mcp-gateway-dashboard/package-lock.json` (regenerated by `npm install` in CA.4b)
- `.github/workflows/ci.yml` (catalog-lint step added)

**Rollback:**
1. `git revert` the CA commit(s).
2. Regenerate `package-lock.json` after revert: `npm install` in
   `vscode/mcp-gateway-dashboard/` and commit the lockfile, OR
   `git checkout HEAD~N -- vscode/mcp-gateway-dashboard/package-lock.json`
   from the pre-CA commit. Without this step, `npm ci` on every operator
   machine and CI runner refuses to install (lockfile/manifest mismatch).
3. Remove the bundled `docs/catalog/` files from any locally installed VSIX
   (operator action: `code --uninstall-extension <id>` then reinstall the
   prior VSIX).
4. Operators who set `mcpGateway.catalogPath` will see no behaviour change
   (extension simply lacks the loader); reset the setting to `""` on next minor
   to remove the orphaned config key.
5. Drop the `lint:catalog` CI job to keep the pipeline green during the revert
   window.

---

## Phase catalog.B — Add Server browse webview

**Goal:** Operators select a catalog entry from a dropdown in the existing
`AddServerPanel`; the form pre-fills transport/url/command/env_keys without ever
trusting webview-supplied catalog data on the host side.

- [x] CB.0: Extend the **existing** harness at
      `vscode/mcp-gateway-dashboard/src/test/webview/add-server-panel.test.ts`
      (41 `it()` blocks today, 473 lines, mock-vscode + `simulateSubmit` /
      `latestPanel` / `createTrackingClient` helpers already in place — see
      `vscode/mcp-gateway-dashboard/src/test/webview/add-server-panel.test.ts:1-65`).
      Add a small fixture group (`describe('catalog regression', ...)` block)
      with ≥3 baseline cases covering today's submit flow when catalog selection
      is absent (one happy-path stdio, one happy-path http, one validation-
      failure case) so CB.5 can layer catalog-aware assertions on a stable
      foundation without touching the 41 pre-existing tests.
      **Acceptance:** ≥3 new cases pass against unmodified `add-server-panel.ts`;
      existing 41 cases still pass; total test count rises from 41 to ≥44.
- [x] CB.1: "Choose from catalog" dropdown + pre-fill behaviour. Edit
      `vscode/mcp-gateway-dashboard/src/webview/html-builder.ts` to add a
      `<select>` element above the Name field; default option label
      `(Custom server)` keeps today's free-form behaviour. The catalog payload
      is passed to the webview via the existing `jsonForScript` helper
      (`vscode/mcp-gateway-dashboard/src/webview/html-builder.ts:17-22`) — never
      raw HTML interpolation. Edit
      `vscode/mcp-gateway-dashboard/src/webview/add-server-panel.ts` to send
      a host→webview `init` message containing the catalog entries.
      **Acceptance:** (a) host receives an `init` message containing the full
      catalog entries within one event-loop tick of `panel.reveal()`; (b) the
      webview's `<select>` change handler synchronously updates input field
      values with no `await` chain; (c) every catalog string rendered into the
      DOM passes through `escapeHtml`
      (`vscode/mcp-gateway-dashboard/src/webview/html-builder.ts:6-13`); (d)
      manual smoke note: dropdown should feel instantaneous to the operator
      (sub-50 ms perceptible latency).
- [x] CB.2: Pre-population of `env_keys` and `header_keys`. When a catalog
      entry is selected, render one empty input row per declared env key /
      header key so the operator fills only the secret VALUE, never the key
      structure. **Acceptance:** selecting an entry with N env_keys produces
      exactly N empty inputs; switching back to `(Custom server)` clears those
      inputs and restores the empty default state.
- [x] CB.3: Extension-host re-validation of catalog selection. When the
      submit payload contains a `catalogId`, `AddServerPanel.handleSubmit`
      re-loads the catalog via `loadServersCatalog()` (CA.5), looks up the
      entry by `name`, and re-validates EVERY resolved field through the
      existing helpers in `vscode/mcp-gateway-dashboard/src/validation.ts`
      before calling `client.addServer()`. The webview is NOT trusted —
      a forged `catalogId` referencing a non-existent entry MUST be rejected
      with the same error UX as a validation failure.
      **Acceptance:** CB.5 includes a test asserting that a forged catalogId
      payload (entry not present in loader output) is rejected and never
      reaches `client.addServer()`.
- [x] CB.4: Register `mcpGateway.catalogPath` in
      `vscode/mcp-gateway-dashboard/package.json` `contributes.configuration`
      block. Properties: `"type": "string"`, `"default": ""`,
      `"scope": "machine"`,
      `"description": "Optional override path to a catalog directory containing servers.json and commands.json. When empty, the bundled catalog under the extension's installation directory is used."`.
      Loader resolution: operator path wins when non-empty AND the directory
      exists; otherwise fall back to `<extensionPath>/docs/catalog/`.
      **Acceptance:** (a) automated — a Mocha test inside the CB.4 task
      (lives in `vscode/mcp-gateway-dashboard/src/test/webview/add-server-panel.test.ts`
      under a `describe('manifest registration', ...)` group) parses the
      package.json, walks `contributes.configuration.properties`, and asserts
      that `mcpGateway.catalogPath` exists with `type === "string"`,
      `default === ""`, `scope === "machine"` — this assertion lives in CB
      because it tests CB-introduced state; CA.6 only tests CA-introduced
      state, eliminating the Phase A↔B dependency loop; (b) loader unit test
      in CA.6 shows non-empty operator path overrides bundled path; empty
      operator path falls back to bundled; (c) manual smoke — open VS Code
      Settings UI, search `mcpGateway.catalogPath`, verify visible under
      "MCP Gateway" group with machine-scope indicator.
- [x] CB.5: Mocha tests in `add-server-panel.test.ts` (built on the CB.0
      harness) covering: catalog selection pre-fills name/url/env_keys
      correctly; malformed catalog (loader returns warnings) shows non-blocking
      warning toast and keeps the panel functional; host re-validation rejects
      forged `catalogId`; `escapeHtml` neutralises `<script>`-laden malicious
      catalog entries (HTML strings appear escaped in the captured webview
      messages); Phase 11.C in-flight submit guard
      (`docs/ROADMAP.md:23 — "in-flight submit guard"`) still holds when the
      operator switches catalog selection mid-submit.
      **Acceptance:** ≥5 new Mocha cases pass; total `npm test` count
      increases by ≥5 + the CB.0 baseline cases.
- [x] CB.GATE: tests + codereview + thinkdeep — zero errors (any finding at or above CLAUDE_GATE_MIN_BLOCKING_SEVERITY; default: any finding)
  - **Tests:** `npm test` → **506 passing / 31 failing** (37s). All 31 failures
    are pre-existing GatewayClient + LogViewer issues documented in CA.GATE
    (verified by stashing CB changes and re-running — identical 31 failures,
    same tests). CB introduces **zero regressions** in the AddServerPanel
    suite. New CB-group test counts: 3 CB.0 baseline + 7 CB.5 + 1 CB.4
    manifest = 11 new passing cases (52 total in `add-server-panel.test.ts`,
    up from 41 pre-CB).
  - **Compile:** `tsc -p ./` exits 0 with zero errors after every fix.
  - **PAL codereview:** PAL MCP unavailable — fell back to internal
    cross-model review per CLAUDE.md rule. **Agent:** code-reviewer on
    **Sonnet 4.6** (parent session is Opus 4.7). Round 1 returned:
    0 CRITICAL, 0 HIGH, **2 MEDIUM (MEDIUM-1 + MEDIUM-2)**, **3 LOW (LOW-1,
    LOW-2, LOW-3)**. All 5 findings fixed in-cycle:
    - MEDIUM-1 (html-builder.ts:528): filter warnings array to strings only
      before `showBanner` concat — fixed.
    - MEDIUM-2 (add-server-panel.ts:157-160): silent `?? ''` fallback in
      `resolveCatalogDir` could return a relative `docs/catalog` path under
      a non-standard URI; changed to return `null` + explicit warning via
      loader — fixed.
    - LOW-1 (html-builder.ts:417-425): added comment documenting that ajv
      schema enforcement rejects `=` / `:` in env/header keys — fixed.
    - LOW-2 (test helper): `waitForPostedMessage` now throws on timeout
      instead of silently returning — fixed.
    - LOW-3 (XSS test comment): clarified what the assertions actually
      verify (architectural invariant + static source text, not runtime
      DOM behaviour) — fixed.
  - **PAL thinkdeep:** PAL MCP unavailable — fell back to general-purpose
    Sonnet 4.6 agent with deep-analysis + combinatoric-gap prompt. Round 2
    returned: 0 CRITICAL, 0 HIGH, **2 MEDIUM (CB-1 + CB-2)**, **5 LOW
    (CB-3..CB-6, CB-11)**, 4 INFO (D1/D3/D4/D5 compliance confirmed). Fixes:
    - CB-1 (add-server-panel.ts:148): `get<string>` TypeScript generic is
      type-cast-only; a non-string config value (null / number from a
      corrupted settings.json) would crash `.trim()` as an unhandled promise
      rejection. Added explicit `typeof rawCatalogPath === 'string'`
      runtime guard — fixed.
    - CB-2 (html-builder.ts:433-457): `applyCatalogSelection` now resets all
      four form fields to empty before populating from the new entry. This
      closes a latent stale-field risk if future transport types ever miss
      a branch — fixed defensively.
    - CB-3 (add-server-panel.ts:208): added a long clarifying comment
      explaining that CB.3 checks catalogId EXISTENCE, not field equality
      — by design per plan. No code change, doc only — fixed.
    - CB-4 (UX): no reachable UX scenario (dropdown with zero entries
      cannot produce a non-empty catalogId), no code change needed.
    - CB-5: confirmed correct concurrency behaviour, no code change needed.
    - CB-6 (.vscodeignore): added a long comment warning future maintainers
      that adding `docs/**` exclusion requires `!docs/catalog/**` negation
      — fixed.
    - CB-11 (PLAN acceptance matrix): CB row's superseded "within one
      event-loop tick" phrasing (N-1 fix updated CB.1 body but not the
      matrix) replaced with observable criteria (post-reveal `init`
      + synchronous change handler + textContent/.value render) — fixed.
  - **Post-fix re-test:** `npm test` → 506 passing / 31 failing (identical
    baseline; zero regressions from any of the 7 review fixes).
  - **Reviewers / fallback models:** code-reviewer (Sonnet 4.6) +
    general-purpose (Sonnet 4.6). Parent session ran on Opus 4.7 — tier
    diversity achieved.

**Files touched:**
- `vscode/mcp-gateway-dashboard/src/test/webview/add-server-panel.test.ts` (extended — new catalog regression group + CB.5 catalog cases)
- `vscode/mcp-gateway-dashboard/src/webview/html-builder.ts` (dropdown + pre-fill scaffold)
- `vscode/mcp-gateway-dashboard/src/webview/add-server-panel.ts` (init message + handleSubmit re-validation)
- `vscode/mcp-gateway-dashboard/src/validation.ts` (no signature changes; consumed)
- `vscode/mcp-gateway-dashboard/package.json` (`contributes.configuration` + setting)
- `vscode/mcp-gateway-dashboard/src/catalog.ts` (loader path resolution updated to honour `mcpGateway.catalogPath`)

**Rollback:**
1. `git revert` the CB commit(s).
2. The `mcpGateway.catalogPath` setting becomes orphaned in any operator's
   `settings.json`; document in CHANGELOG that the setting is no-op after the
   revert and may be removed manually.
3. Reissue the prior VSIX so operators see the dropdown disappear; the form
   reverts to the Phase 11.C free-form layout with no migration needed
   (`docs/ROADMAP.md:23` "AddServerPanel webview form").
4. No daemon-side state to clean — CB never modifies daemon configs that the
   operator did not explicitly submit.
5. Loader is read-only with respect to extension-managed state — it writes
   nothing to `vscode.SecretStorage`, `globalState`, or `workspaceState`.
   No cleanup of credential-index entries or persisted state is needed after
   revert.

---

## Phase catalog.C — Slash-command template enrichment

**Goal:** `SlashCommandGenerator` injects per-server command templates from the
catalog when a server transitions to running, falling back to today's bare
skeleton when no catalog entry exists.

- [x] CC.1: Extend `vscode/mcp-gateway-dashboard/src/slash-command-generator.ts`
      to call `loadCommandsCatalog()` (from CA.5) per server transition. The
      lookup is best-effort: missing catalog file, loader warnings, or no
      matching `server_name` entry MUST fall back to the existing skeleton
      builder at `vscode/mcp-gateway-dashboard/src/slash-command-generator.ts:167-189`.
      Catalog is read once per generator instance and cached in memory; the
      cache is invalidated when the `mcpGateway.catalogPath` setting changes
      (re-use the established `vscode.workspace.onDidChangeConfiguration`
      pattern from `vscode/mcp-gateway-dashboard/src/sap-tree-provider.ts:27`
      and `vscode/mcp-gateway-dashboard/src/extension.ts:178`).
      **Acceptance:** server with catalog entry produces enriched template;
      server without entry produces today's skeleton output verbatim
      (regression of existing test at line 129-130 of
      `vscode/mcp-gateway-dashboard/src/test/slash-command-generator.test.ts`).
- [x] CC.2: Template variable substitution at write time. Allow-list approach —
      ONLY `${server_name}` and `${server_url}` are substituted. Any other
      `${var}` token is left literal in the output (never recursive
      substitution, never silent eval). Substitution is a simple `String.replaceAll`
      pass over `template_md`, performed AFTER markdown is assembled and BEFORE
      the magic-header marker is prepended.
      **Acceptance:** test in CC.4 asserts that `${unknown_var}` survives
      verbatim and `${server_name}` is replaced with the running server's name.
- [x] CC.3: Existing magic-header marker (constant `MARKER` re-exported from
      `vscode/mcp-gateway-dashboard/src/slash-command-generator.ts:192`) still
      gates overwrite/delete. Catalog-enriched template content sits BELOW the
      marker, exactly as today's skeleton does
      (`vscode/mcp-gateway-dashboard/src/slash-command-generator.ts:173`).
      Document in code comment AND README that edits below line 1 are silently
      overwritten on regeneration — this is the v1.5 known limitation locked in
      decision D3.
      **Acceptance:** the existing user-edit-preservation test
      (`vscode/mcp-gateway-dashboard/src/test/slash-command-generator.test.ts:121-131`)
      still passes when the catalog is present (marker check happens before
      catalog lookup).
- [x] CC.4: Mocha tests in `vscode/mcp-gateway-dashboard/src/test/slash-command-generator.test.ts`
      EXTENDING the existing 25-test suite (verified by
      `grep -c "it(" vscode/mcp-gateway-dashboard/src/test/slash-command-generator.test.ts`
      → 25; ROADMAP line 18's "24 tests" was an older snapshot pre-12.B).
      New cases: (a) catalog-enriched template write produces expected body;
      (b) no-catalog path emits today's skeleton (regression); (c) user-edited
      file preserved when marker missing (extends test at lines 121-131);
      (d) template variable substitution replaces known vars and leaves
      unknown vars literal; (e) removing the entry from the catalog and
      triggering another transition falls back to skeleton on the next
      regeneration cycle.
      **Acceptance:** ≥5 new test cases pass; total slash-command-generator
      test count rises from 25 to ≥30; no existing test regresses.
- [x] CC.GATE: tests + codereview + thinkdeep — zero errors (any finding at or above CLAUDE_GATE_MIN_BLOCKING_SEVERITY; default: any finding)
  - **Tests:** `npm test` → **513 passing / 31 failing** (43s). The 31 failures
    are the identical pre-existing GatewayClient (13) + LogViewer (18) set from
    CB.GATE; verified unchanged. CC introduces **+7 passing, zero regressions**
    (506 → 513). New CC test counts: 7 cases in the `catalog enrichment (CC.1
    / CC.2 / CC.3)` describe block inside
    `src/test/slash-command-generator.test.ts`. Total `slash-command-generator`
    suite cases: 25 → 32 (above plan's ≥30 threshold).
  - **Compile:** `tsc -p ./` exits 0 with no errors.
  - **PAL thinkdeep (gpt-5.2-pro, async queue):** **PASS, 0 findings** (152 s).
    Task `rev-720a00600f3b`. Deep gap analysis, concurrency audit, TOCTOU
    check, lifecycle review — clean.
  - **PAL codereview:** PAL MCP **timed out twice** (30 s × 2, primary +
    fallback) — identical infrastructure issue to CA.GATE + CB.GATE. Fell
    back to internal cross-model review per CLAUDE.md rule.
    - **Reviewer:** `code-reviewer` agent on **Sonnet 4.6** (parent session
      Opus 4.7 — tier diversity achieved). Covered: correctness (marker on
      line 1, skeleton-path byte identity, replaceAll allow-list),
      concurrency (lazy one-shot, `catalogLoaded=true` pre-await guard,
      in-flight invalidation window), trust boundaries (operator-only
      catalog, no webview input, `scope: "machine"` preserved),
      resource hygiene (configSubscription double-enable guard, dispose
      cleanup), TypeScript soundness (`typeof srvEntry?.url === 'string'`
      fall-through), test isolation (Mocha hook ordering, schema-cache
      reset in inner beforeEach), and pattern drift from CB.GATE.
    - **Verdict:** APPROVE — 0 findings at any severity.
  - **New file:** `vscode/mcp-gateway-dashboard/src/catalog-path.ts` (47 lines)
    shared `resolveCatalogDir` helper. AddServerPanel (CB) refactored to
    import it; SlashCommandGenerator (CC) imports it. Eliminates drift
    risk flagged in CB.GATE audit. The `typeof rawCatalogPath === 'string'`
    runtime guard (CB-1 finding in CB.GATE Round 2) is now applied once in
    the shared helper.

**Files touched:**
- `vscode/mcp-gateway-dashboard/src/slash-command-generator.ts` (catalog hook + variable substitution + config-watcher)
- `vscode/mcp-gateway-dashboard/src/catalog-path.ts` (NEW — shared `resolveCatalogDir` helper, 47 lines, factored out of CB so CB + CC apply identical resolution rules)
- `vscode/mcp-gateway-dashboard/src/webview/add-server-panel.ts` (refactor: drops inlined `resolveCatalogDir` + unused `fsp` import, imports shared helper; behaviour identical)
- `vscode/mcp-gateway-dashboard/src/extension.ts` (3-line wiring: passes `context.extensionUri` to `SlashCommandGenerator`)
- `vscode/mcp-gateway-dashboard/src/test/slash-command-generator.test.ts` (extended suite — 7 new cases, 25 → 32 total)

**Rollback:**
1. `git revert` the CC commit(s).
2. Already-generated `.claude/commands/<server>.md` files keep their enriched
   bodies on disk — they are NOT regenerated until the next server transition.
   Operators who want to revert content can delete the files; the next
   transition writes the original skeleton.
3. No setting changes; `mcpGateway.slashCommandsEnabled` semantics are
   unchanged (`docs/ROADMAP.md:30`).

---

## Phase catalog.D — Integration + release

**Goal:** Ship v1.5.0 — README, CHANGELOG, VSIX rebuild, final security-focused
codereview across the catalog surface.

- [ ] CD.1: README — add `## Catalogs` section with: an example catalog
      fragment (servers.json snippet + commands.json snippet), a screenshot of
      the new dropdown, the explicit statement "catalogs are local files only —
      the extension never fetches catalog data from the network", documentation
      of `mcpGateway.catalogPath`, and the documented known limitation that
      slash-command edits below line 1 are overwritten on regeneration (D3).
      **Acceptance:** README diff reviewed; screenshot committed under
      `docs/screenshots/`.
- [ ] CD.2: `CHANGELOG.md` — add v1.5.0 entry covering: Add Server catalog
      browse, slash-command template enrichment, new `mcpGateway.catalogPath`
      setting (with default and scope), bundled seed catalog of 5 servers,
      ajv-cli CI lint job, no daemon-side changes.
      **Acceptance:** entry follows the format of the v1.4.0 entry; lists every
      user-visible change from CB and CC.
- [ ] CD.3: VSIX rebuild via `npm run deploy`
      (`vscode/mcp-gateway-dashboard/package.json:310`); commit the rebuilt
      `mcp-gateway-dashboard-latest.vsix` together with the source changes per
      the VSCode Extension Build Discipline rule in `.claude/CLAUDE.md`.
      **Acceptance:** single commit contains BOTH source changes AND the
      rebuilt VSIX; reload-window reminder posted to the user.
- [ ] CD.4: Final `mcp__pal__codereview` across all files touched in CA / CB / CC.
      Model: `gpt-5.2-pro`. Mode: `security` (the CB webview trust boundary is
      the highest-risk surface in v1.5). Record verdict and findings table
      inline in this section before closing the gate.
      **Acceptance:** verdict `APPROVE` with zero findings at or above
      `CLAUDE_GATE_MIN_BLOCKING_SEVERITY` (default: any finding). CV-GATE entry
      block written here with `verdict | findings | model | timestamp`.
- [ ] CD.GATE: tests + codereview + thinkdeep — zero errors (any finding at or above CLAUDE_GATE_MIN_BLOCKING_SEVERITY; default: any finding)
      - Capture evidence: full-suite `npm test` output line, `go test ./...`
        output line (sanity — no Go changes expected, but verify zero
        regressions), PAL codereview from CD.4 verdict, PAL thinkdeep verdict.

**Files touched:**
- `README.md`
- `CHANGELOG.md`
- `docs/screenshots/catalog-dropdown.png` (new)
- `vscode/mcp-gateway-dashboard/mcp-gateway-dashboard-latest.vsix` (rebuilt)

**Rollback:**
1. `git revert` the CD commit(s) (note: this also reverts the rebuilt VSIX
   binary — the prior VSIX is restored from the parent commit).
2. Operators must reload VSCode after the revert lands to pick up the older
   VSIX bundle.
3. CHANGELOG revert is informational only; the entry was never tagged as a
   release on a remote channel until the user runs the v1.5.0 tag step
   separately.
4. README screenshot file persists on disk in operator clones until they pull;
   no harm.

---

## Acceptance Criteria Matrix

| Phase | Concrete observable criterion | How verified | Owner-step |
|-------|-------------------------------|--------------|------------|
| CA | Both seed files validate against bundled schemas via ajv-cli | `npm run lint:catalog` exits 0 in CI | CA.6 |
| CA | Loader never throws and never reads >1 MiB | Mocha test cases for each error class pass | CA.5, CA.6 |
| CA | `$id` v2 schemas are rejected | Mocha test with v2 fixture passes | CA.6 |
| CB | Host posts `init` message with full catalog after `panel.reveal()` (observable via `panel._postedMessages`, typical latency &lt;100ms); `<select>` change handler synchronous (no await chain); every catalog-derived DOM write uses `textContent` / `.value` (never `innerHTML`) | Mocha assertions in CB.5 | CB.1, CB.5 |
| CB | Sub-50 ms perceptible pre-fill latency | Manual smoke test (per F-5 fix — not an automated criterion) | CB.1 manual |
| CB | Forged `catalogId` from webview never reaches `client.addServer()` | Mocha test in CB.5 | CB.3, CB.5 |
| CB | `mcpGateway.catalogPath` honoured when non-empty; falls back to bundled path otherwise | Loader unit test + integration test | CB.4, CB.5 |
| CC | Catalog-enriched template emitted when entry exists; skeleton emitted otherwise | Existing test (line 121-131) + new tests in CC.4 | CC.1, CC.4 |
| CC | Unknown `${var}` tokens left literal | Mocha test in CC.4 | CC.2, CC.4 |
| CC | Existing user-edit preservation test still passes | Run `npm test` | CC.3, CC.4 |
| CD | Final PAL codereview returns zero findings at or above gate threshold | CV-GATE block in CD.4 | CD.4 |
| CD | Source + rebuilt VSIX in single commit | `git show --stat` includes `mcp-gateway-dashboard-latest.vsix` | CD.3 |

## Risks & Mitigations

| ID | Severity | Risk (architect step 1) | Mitigation in plan | Residual |
|----|----------|-------------------------|--------------------|----------|
| R1 | HIGH | Webview-supplied catalog data trusted on host side, leading to RCE-equivalent (operator clicks "Add" with malicious catalog name that sneaks through validation). | CB.3 mandates host-side re-load + per-field re-validation; CB.5 includes a forged-catalogId test. `escapeHtml` covers the rendering side. | LOW: depends on completeness of `validation.ts` helpers — covered by Phase 11.C audit. |
| R2 | MEDIUM | XSS via malicious catalog content rendered into the webview. | All catalog payload reaches the webview via `jsonForScript`; all DOM-rendered strings pass through `escapeHtml`. CB.5 test covers `<script>`-laden entries. | LOW. |
| R3 | MEDIUM | Operator path override (`mcpGateway.catalogPath`) used as a per-workspace exfiltration vector. | Setting registered with `scope: "machine"` (CB.4), blocking per-workspace overrides. | LOW. |
| R4 | MEDIUM | Network-resolved schema `$id` triggers HTTP fetch from the validator. | D4 + CA.5: validators are pre-configured with bundled schema files; `$id` is documentation only. Documented explicitly in README (CD.1). | NEGLIGIBLE — verified by ajv config in CA.5. |
| R5 | MEDIUM | Slash-command edits below line 1 silently lost on regeneration. | D3: documented as known limitation in README + code comment (CC.3); hash-augmented marker is v1.6 candidate (Deferred Work). | KNOWN — documented. |
| R6 | LOW | Catalog file >1 MiB causes loader OOM or stall. | CA.5: 1 MiB hard cap; warning, no read. Test in CA.6. | NEGLIGIBLE. |
| R7 | LOW | Cross-reference drift (commands.json references a server.name not present in servers.json). | CA.6: cross-check script in `npm run lint:catalog`. | NEGLIGIBLE. |
| R8 | LOW | VSIX rebuild forgotten on commit, user sees stale UI. | CD.3 + `.claude/CLAUDE.md` "VSCode Extension Build Discipline" rule; pre-commit reminder. | LOW. |

## Known Limitations (documented, not blocking)

1. **Below-line-1 marker edit loss** — slash-command files are regenerated in
   full when the magic-header marker is detected; user edits below line 1 are
   silently overwritten. Documented in CC.3 + README (CD.1). Hash-augmented
   marker is the v1.6 candidate fix.
2. **One-shot catalog read at panel open** — `AddServerPanel` reads the
   catalog once when the panel opens. If the operator edits the catalog file
   while the panel is open, the dropdown does not refresh until the panel is
   closed and reopened. Acceptable for v1.5 because the catalog is operator-
   curated, not a hot-reload data source.
3. **No remote catalog fetch** — by design (D4 + CA.5). Operators who want a
   shared internal catalog must pre-fetch and point `mcpGateway.catalogPath`
   at the local copy.

## Deferred Work (out of v1.5)

| Item | Reason for deferral | Trigger to revisit |
|------|---------------------|--------------------|
| Go-side `internal/catalog/` loader | No daemon endpoint advertises catalog data on the v1.5 roadmap (D1). | When (and only when) a daemon endpoint such as `GET /api/v1/catalog` is added to the roadmap. |
| Hash-augmented slash-command marker | Keeps Phase 11.E semantics unchanged for v1.5 (D3). | v1.6 candidate when the below-line-1 edit-loss limitation generates user friction. |
| Remote catalog fetch | Supply-chain hygiene (D2 + D4). | Only if a signed-fetch design (cosign-style verification) lands first. |
| Auto-publishing of catalog from daemon `GET /api/v1/catalog` | No daemon endpoint in v1.5; depends on the deferred Go loader. | Coupled with the Go loader trigger above. |

## Next Plans

Pulled from `docs/ROADMAP.md` Backlog tail (`docs/ROADMAP.md:90-92, 100-102`):

| Plan | Status | Goal |
|------|--------|------|
| `docs/PLAN-v15.md` (v1.5.0 tail) | Planned (parallel to this plan) | LOW findings from 12.A / 13 PAL reviews (ConstantTimeCompare length, Scanner 64KB limit) + TLS self-signed integration test + Windows DACL enforcement-tier runner. |
| v1.6 candidate — hash-augmented slash-command marker | Backlogged (Deferred Work above) | Eliminate the below-line-1 edit-loss limitation. |
| v1.6 candidate — daemon catalog endpoint + Go loader | Backlogged (Deferred Work above) | Surface catalog data via `GET /api/v1/catalog` and re-introduce `internal/catalog/`. |
