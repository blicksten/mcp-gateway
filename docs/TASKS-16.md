# TASKS — Phase 16 (Claude Code Integration)

Companion to `docs/PLAN-16.md`. Lists every task, assigns ownership, captures dependencies, and flags parallelization opportunities.

## Legend

- **Owner**: primary specialist (`backend-dev`, `frontend-dev`, `test-engineer`, `devops-engineer`, `doc-writer`, `architect`).
- **Dep**: direct prerequisite task IDs (empty = no blocker).
- **Parallel**: tasks that may run in the same window.
- **Blocks**: downstream tasks that cannot start until this one completes.
- **Est**: relative effort (S = < ½ day, M = ½–2 days, L = 2–5 days, XL = > 1 week).

---

## Phase 16.0 — SPIKE (gate decides rest of plan)

| ID | Task | Owner | Dep | Est | Notes |
|----|------|-------|-----|-----|-------|
| T16.0.1 | DevTools fiber-walk probe | architect | — | S | Manual; no code |
| T16.0.2 | Real `executeCommand("reload-plugins")` test | architect | T16.0.1 | S | Add/verify dummy MCP |
| T16.0.3 | `registerCommand` availability | architect | T16.0.1 | S | Determines 16.5.6 path |
| T16.0.4 | Patch resilience under React remount | architect | T16.0.1 | S | MutationObserver pattern validity |
| T16.0.5 | Spike report at `docs/spikes/` | architect | T16.0.1–4 | S | Go/no-go memo |
| T16.0.GATE | Maintainer sign-off on 16.4 path | architect | T16.0.5 | — | GATE — binary pass/rescope |

**Blocks**: Phase 16.4, 16.5. (16.1, 16.2, 16.3, 16.6, 16.7, 16.8, 16.9 can start without spike outcome.)

---

## Phase 16.1 — Gateway dual-mode

| ID | Task | Owner | Dep | Est | Notes |
|----|------|-------|-----|-----|-------|
| T16.1.1 | `perBackendServer` map + `ServerFor()` accessor | backend-dev | — | M | internal/proxy/gateway.go |
| T16.1.2 | Extend `RebuildTools()` to update both registries | backend-dev | T16.1.1 | M | Aggregate namespaced + per-backend unnamespaced |
| T16.1.3 | `registerToolForBackend()` helper | backend-dev | T16.1.1 | S | Mirrors `registerTool` |
| T16.1.4 | `gateway_proxy_test.go` unit suite | test-engineer | T16.1.2, T16.1.3 | M | 4 subtests incl. race-safety |
| T16.1.5 | `getServer(req)` path routing in HTTP handler | backend-dev | T16.1.1 | M | internal/api/server.go; careful of streamable sub-paths |
| T16.1.6 | `server_proxy_test.go` HTTP suite | test-engineer | T16.1.5 | M | 4 route/status/auth cases |
| T16.1.5.a | SDK path verification + backend-name denylist | backend-dev | — | S | [REVIEW-16 L-01]; precedes T16.1.5 implementation |
| T16.1.7 | `mcpTransportPolicy` uniform match for `/mcp/*` | backend-dev | T16.1.5 | S | Security regression test |
| T16.1.GATE | tests + codereview + thinkdeep zero-errors | qa-lead | T16.1.1–7, T16.1.5.a | — | GATE |

**Parallel-safe within phase**: T16.1.2 + T16.1.3 + T16.1.5 after T16.1.1 lands. T16.1.4 and T16.1.6 are independent test suites.
**Blocks**: 16.3, 16.7. **Blocked by**: none (can start immediately).

---

## Phase 16.2 — Plugin packaging

| ID | Task | Owner | Dep | Est | Notes |
|----|------|-------|-----|-----|-------|
| T16.2.1 | `installer/plugin/` directory scaffold | backend-dev | — | S | |
| T16.2.2 | `plugin.json` with userConfig | backend-dev | T16.2.1 | S | Keychain via `sensitive:true` |
| T16.2.3 | `internal/plugin/regen.go` | backend-dev | T16.2.1 | M | Atomic write + backup + JSON validate |
| T16.2.4 | Wire regen into `/api/v1/servers` mutations | backend-dev | T16.2.3 | S | api/server.go:531,553 |
| T16.2.5 | `internal/plugin/discover.go` (env → glob → error) | backend-dev | T16.2.3 | S | Cross-platform paths |
| T16.2.6 | `installer/marketplace.json` | backend-dev | T16.2.1 | S | Local marketplace |
| T16.2.7 | `regen_test.go` (atomic, idempotent, backup, JSON, fallback, disabled) | test-engineer | T16.2.3–5 | M | `t.TempDir()` fixtures |
| T16.2.GATE | tests + codereview + thinkdeep zero-errors | qa-lead | T16.2.1–7 | — | GATE |

**Parallel-safe**: T16.2.2 + T16.2.6 after T16.2.1. T16.2.3 and T16.2.5 after T16.2.1.
**Blocks**: 16.3 (needs plugin dir), 16.5 (needs plugin), 16.7, 16.8.
**Blocked by**: none.

---

## Phase 16.3 — Gateway REST endpoints for patch integration

| ID | Task | Owner | Dep | Est | Notes |
|----|------|-------|-----|-----|-------|
| T16.3.1 | New route group `/api/v1/claude-code/*` | backend-dev | T16.2.3 | M | Bearer-auth-required |
| T16.3.2 | `internal/patchstate/state.go` (heartbeats/actions/probes) | backend-dev | — | M | TTL-evicted in-memory state |
| T16.3.3 | Enqueue `reload-plugins` action on regen + 500ms debounce | backend-dev | T16.2.4, T16.3.2 | S | |
| T16.3.4 | CORS narrow-scope for `vscode-webview://` on CC routes | backend-dev | T16.3.1 | S | Integration-test-validated |
| T16.3.5 | Rate limiting per session + per-IP | backend-dev | T16.3.1 | S | 60 GET/min, 5 heartbeat/min |
| T16.3.6 | `claude_code_handlers_test.go` | test-engineer | T16.3.1–5 | M | 6 cases incl. CORS + debounce |
| T16.3.7 | API docs at `docs/api/claude-code-endpoints.md` | doc-writer | T16.3.1 | S | |
| T16.3.GATE | tests + codereview + thinkdeep zero-errors | qa-lead | T16.3.1–7 | — | GATE |

**Parallel-safe**: T16.3.2 independent of all. T16.3.4, T16.3.5, T16.3.7 parallel after T16.3.1.
**Blocks**: 16.4 (patch needs endpoints), 16.5 (dashboard polls status), 16.7, 16.8.
**Blocked by**: 16.2 (plugin dir) for T16.3.3 only.

---

## Phase 16.4 — Webview patch

**HARD GATE: 16.0 spike must pass before starting 16.4.**

| ID | Task | Owner | Dep | Est | Notes |
|----|------|-------|-----|-----|-------|
| T16.4.1 | `apply-mcp-gateway.sh` (POSIX) | backend-dev | T16.0.GATE | M | Mirror `apply-taskbar.sh` |
| T16.4.1.a | Post-write permission lockdown (chmod 600 / icacls) | backend-dev | T16.4.1 | S | [REVIEW-16 L-03] |
| T16.4.2 | `apply-mcp-gateway.ps1` (Windows) | backend-dev | T16.4.1 | M | Same semantics |
| T16.4.3 | `porfiry-mcp.js` (fiber walk + heartbeat + polling + executeCommand) | backend-dev | T16.0.GATE | L | Copy taskbar pattern |
| T16.4.4 | Auth-token substitution at patch-install time | backend-dev | T16.4.1, T16.4.3 | S | Token rotation doc |
| T16.4.5 | Extension activation hook auto-reapplies on CC update | backend-dev | T16.4.1 | M | Closes R-7 |
| T16.4.6 | `porfiry-mcp.test.mjs` mocha harness | test-engineer | T16.4.3 | M | Mock DOM + fiber |
| T16.4.7 | `supported_claude_code_versions.json` | devops-engineer | — | S | Maintainer-edited JSON |
| T16.4.GATE | tests + `shellcheck` + codereview + thinkdeep + spike still valid | qa-lead | T16.4.1–7 | — | GATE |

**Parallel-safe**: T16.4.1 + T16.4.3 + T16.4.7 parallel. T16.4.2 after T16.4.1. T16.4.4 after both. T16.4.5 after T16.4.1.
**Blocks**: 16.5 (needs patch), 16.7.
**Blocked by**: 16.0 spike GATE, 16.3 endpoints.

---

## Phase 16.5 — Dashboard "Claude Code Integration" panel

| ID | Task | Owner | Dep | Est | Notes |
|----|------|-------|-----|-----|-------|
| T16.5.1 | Webview panel `claude-code-panel.ts` (button, checkbox, status rows) | frontend-dev | T16.4.3 (pattern) | L | HTML + state machine |
| T16.5.2 | `[Activate for Claude Code]` handler (plugin install flow) | frontend-dev | T16.2.6, T16.3.1 | M | Prompts for auth_token |
| T16.5.3 | Auto-reload checkbox on/off → run apply/uninstall | frontend-dev | T16.4.1, T16.4.2 | M | Workspace-persisted state |
| T16.5.4 | Status polling (every 10s) | frontend-dev | T16.3.1 | S | |
| T16.5.5 | 10 failure-mode specific messages | frontend-dev | T16.5.1 | M | Matrix from design doc |
| T16.5.6 | `[Test now]` → probe-trigger flow | frontend-dev | T16.3.1, T16.4.3 | M | Timeout + result display |
| T16.5.7 | `[Copy diagnostics]` clipboard dump | frontend-dev | T16.5.4 | S | vscode.env.clipboard |
| T16.5.8 | Unit tests `claude-code-panel.test.ts` | test-engineer | T16.5.1–7 | M | State matrix coverage |
| T16.5.9 | Extension command registration in package.json | frontend-dev | T16.5.1 | S | |
| T16.5.GATE | `npm test` + `npm run deploy` + codereview + manual smoke | qa-lead | T16.5.1–9 | — | GATE incl. macOS/Win rows |

**Parallel-safe**: T16.5.1 + T16.5.5 + T16.5.7 parallel after T16.5.4. T16.5.2 + T16.5.3 + T16.5.6 parallel after T16.5.1. T16.5.8 after all implementation tasks.
**Blocks**: 16.7. **Blocked by**: 16.3 + 16.4.

---

## Phase 16.6 — `gateway.invoke` + meta-tools + supported-versions

| ID | Task | Owner | Dep | Est | Notes |
|----|------|-------|-----|-----|-------|
| T16.6.1 | Register `gateway.invoke` built-in | backend-dev | T16.1.1 | M | Aggregate only |
| T16.6.2 | Register `gateway.list_servers` + `gateway.list_tools` | backend-dev | T16.1.1 | M | Aggregate only |
| T16.6.3 | `serverInfo.instructions` field | backend-dev | — | S | gateway.go init |
| T16.6.4 | `serverInfo.version` cache-busting via config hash | backend-dev | T16.1.2 | S | |
| T16.6.5 | `GET /api/v1/claude-code/compat-matrix` | backend-dev | T16.3.1, T16.4.7 | S | Single source of truth |
| T16.6.6 | Tests `gateway_invoke_test.go` | test-engineer | T16.6.1–4 | M | 5 subtests |
| T16.6.GATE | tests + codereview + thinkdeep zero-errors | qa-lead | T16.6.1–6 | — | GATE |

**Parallel-safe**: T16.6.1 + T16.6.2 + T16.6.3 after T16.1.1. T16.6.4 after T16.1.2.
**Blocks**: none (safety-net phase). **Blocked by**: 16.1, 16.3, 16.4 (only for T16.6.5).

---

## Phase 16.7 — Integration test end-to-end

| ID | Task | Owner | Dep | Est | Notes |
|----|------|-------|-----|-----|-------|
| T16.7.1 | `integration_phase16_test.go` full-chain test | test-engineer | 16.1, 16.2, 16.3 | L | Build tag `integration` |
| T16.7.2 | `integration_cors_test.go` | test-engineer | T16.3.4 | S | vscode-webview:// allowed, evil.com denied |
| T16.7.3 | `porfiry-mcp.integration.test.mjs` (gateway subprocess) | test-engineer | T16.4.3 | L | Full patch lifecycle |
| T16.7.4 | `docs/TESTING-PHASE-16.md` | doc-writer | T16.7.1–3 | S | Operator guide |
| T16.7.GATE | Pass on CI (Linux + macOS); Makefile target for Windows manual | qa-lead | T16.7.1–4 | — | GATE |

**Parallel-safe**: T16.7.2 + T16.7.4 independent. T16.7.1 and T16.7.3 can run in parallel after their deps.
**Blocks**: none. **Blocked by**: 16.1, 16.2, 16.3 (minimum); T16.7.3 also needs 16.4.

---

## Phase 16.8 — Bootstrap CLI

| ID | Task | Owner | Dep | Est | Notes |
|----|------|-------|-----|-----|-------|
| T16.8.1 | `cmd/mcp-ctl/install_claude_code.go` with flags | backend-dev | T16.2.6 | M | --mode/--scope/--no-patch/--dry-run |
| T16.8.2 | Install logic (gateway check + claude CLI + plugin-sync + patch) | backend-dev | T16.8.1, T16.4.1 | M | |
| T16.8.3 | Failure handling + rollback | backend-dev | T16.8.2 | S | |
| T16.8.4 | Cross-platform path resolution | backend-dev | T16.8.1 | S | sh vs ps1 dispatch |
| T16.8.5 | Tests `install_claude_code_test.go` | test-engineer | T16.8.1–4 | M | 4 subtests |
| T16.8.6 | Auth-token drift detection + `--refresh-token` / `--check-only` | backend-dev | T16.8.1 | M | [REVIEW-16 M-03] |
| T16.8.GATE | tests + codereview + manual smoke matrix | qa-lead | T16.8.1–6 | — | GATE |

**Parallel-safe**: T16.8.1 solo; T16.8.2+T16.8.3+T16.8.4 parallel after T16.8.1.
**Blocks**: 16.9 (docs reference it). **Blocked by**: 16.2, 16.4 (for non-`--no-patch` path).

---

## Phase 16.9 — Docs + dogfood + disclaimers

| ID | Task | Owner | Dep | Est | Notes |
|----|------|-------|-----|-----|-------|
| T16.9.1 | README §"Connecting Claude Code" | doc-writer | T16.8.1 | M | One-liner install + screenshot |
| T16.9.2 | README §"Commands vs MCP servers" | doc-writer | — | S | Semantic disambiguation |
| T16.9.3 | Slash-command .md disclaimer header | backend-dev | — | S | SlashCommandGenerator + regression test |
| T16.9.4.a | CI workflow `dogfood-smoke.yml` | devops-engineer | 16.2, 16.8 | S | [REVIEW-16 L-04]; precedes T16.9.4 |
| T16.9.4 | Replace repo `.mcp.json` (dogfood) | devops-engineer | T16.9.4.a | S | Proof of integration |
| T16.9.5 | `docs/ADR-0005-claude-code-integration.md` | architect | 16.0–16.8 | M | Full decision record |
| T16.9.6 | CHANGELOG.md v1.6.0 entry | doc-writer | 16.1–16.8 | S | Added + Security + Docs + Breaking |
| T16.9.7 | ROADMAP.md Phase 16 COMPLETE + Phase 17 candidates | doc-writer | all | S | |
| T16.9.GATE | Doc review + codereview on ADR + markdown lint + fresh-clone smoke | qa-lead | T16.9.1–7 | — | GATE |

**Parallel-safe**: T16.9.2 + T16.9.3 independent. T16.9.5 needs all other phases done.
**Blocks**: none (phase-close). **Blocked by**: almost everything.

---

## Global dependency graph

```
                  16.0 SPIKE (gate)
                        │
                        ▼
              ┌─────────┴─────────┐
              │                   │
           YES PATH            NO PATH (rescope)
              │                   │
              │          (drops 16.4, 16.5)
              │
  ┌───────────┼───────────┬───────────┐
  ▼           ▼           ▼           ▼
 16.1        16.2        16.6.*      16.3.2
 dual-mode   plugin      meta tools  patchstate
  │           │           │           │
  └───────────┤           │           │
              ▼           │           │
            16.3         │           │
            endpoints   │           │
              │          │           │
              ├──────────┤           │
              ▼          ▼           │
             16.4       16.6.5       │
             patch      compat-mat.  │
              │          │           │
              ├──────────┤           │
              ▼          ▼           │
             16.5       16.7         │
             dashboard  integration  │
              │          tests       │
              ├──────────┤           │
              ▼          ▼           │
             16.8       (CI green)   │
             bootstrap CLI           │
              │                      │
              └──────────┬───────────┘
                         ▼
                        16.9
                       docs + dogfood + ADR
                         │
                         ▼
                     PHASE 16 COMPLETE
```

## Critical path

Assuming spike passes, the critical path is:

**16.0 → 16.1 → 16.3 → 16.4 → 16.5 → 16.8 → 16.9** (≈ 3-5 weeks depending on team size).

## Parallelizable stream breakdown

Given 3 developers (two backend-dev + one frontend-dev) plus shared test-engineer:

**Week 1**:
- Dev A: 16.0 spike → 16.1
- Dev B: 16.2 plugin scaffold + regen → 16.3.2 patchstate (independent of 16.1)
- Dev FE: Prepare 16.5 panel skeleton (blocked on 16.3/16.4, but UI stubs can start early)

**Week 2**:
- Dev A: 16.3.1 endpoints → 16.4.1+16.4.3 patch
- Dev B: 16.6.1–16.6.4 meta-tools
- Dev FE: 16.5 panel implementation (waits for 16.3 and 16.4)
- Test-engineer: 16.7 integration tests (after 16.1+16.2+16.3 land)

**Week 3**:
- Dev A: 16.4 polish + 16.8 CLI
- Dev B: 16.2.7 tests + 16.3.6 tests + 16.6.6 tests
- Dev FE: 16.5 remaining + 16.5.8 tests
- Test-engineer: 16.7.3 patch integration test

**Week 4**: audit cycles (lead-auditor + specialist-auditor), 16.9 docs, dogfood .mcp.json swap, ADR-0005, release.

## Task count summary

| Phase | Tasks | Est range |
|-------|-------|-----------|
| 16.0 | 6 (incl. GATE) | 2-3 days spike + review |
| 16.1 | 8 | 1 week |
| 16.2 | 8 | 4-5 days |
| 16.3 | 8 | 4-5 days |
| 16.4 | 8 | 1-1.5 weeks (largest) |
| 16.5 | 10 | 1-1.5 weeks |
| 16.6 | 7 | 3-4 days |
| 16.7 | 5 | 1 week |
| 16.8 | 6 | 3-4 days |
| 16.9 | 8 | 3-4 days |
| **Total** | **74** | **4-6 weeks** |

## Next Plans

See `docs/PLAN-16.md` §"Next Plans" for Phase 17 candidates and beyond.
