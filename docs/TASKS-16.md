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
| T16.2.3 | `internal/plugin/regen.go` | backend-dev | T16.2.1 | M | Atomic write + backup + JSON validate; [REVIEW-16 M-02] mutex serialization + [REVIEW-16 L-05] OWNS file (unconditional overwrite) |
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
| T16.3.2 | `internal/patchstate/state.go` (heartbeats/actions/probes) | backend-dev | — | M | TTL-evicted; [REVIEW-16 M-01] disk persistence to ~/.mcp-gateway/patch-state.json (0600) + TTL reload-filter |
| T16.3.3 | Enqueue `reload-plugins` action on regen + 500ms debounce | backend-dev | T16.2.4, T16.3.2 | S | |
| T16.3.4 | CORS narrow-scope for `vscode-webview://` on CC routes | backend-dev | T16.3.1 | S | [REVIEW-16 L-02] explicit OPTIONS preflight handler (before auth) |
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
| T16.4.5 | Extension activation hook auto-reapplies on CC update | backend-dev | T16.4.1 | M | Closes R-7; [REVIEW-16 M-04] explicit process.platform win32/unix dispatch |
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
| T16.5.5 | 11 failure-mode specific messages (A-K) | frontend-dev | T16.5.1 | M | [REVIEW-16 L-06 + M-03] K = token rotation detected via mtime compare |
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

---

## Phase 16.4 — Work Orders (pipeline feature-b8f2decf, 2026-04-22)

Design is frozen: `docs/PLAN-16.md` §16.4 lines 384–510. Architect review
(lines 509–531) verdict: zero gaps, ready for implementation. API contract
consumed by the JS patch: `docs/api/claude-code-endpoints.md` v1.6.0 (FROZEN).

**Scope boundary (hard — other phases running in parallel windows):**

- **In-scope (5 files):** `installer/patches/apply-mcp-gateway.sh`,
  `installer/patches/apply-mcp-gateway.ps1`,
  `installer/patches/porfiry-mcp.js`,
  `installer/patches/porfiry-mcp.test.mjs`,
  `configs/supported_claude_code_versions.json`.
- **Out-of-scope this pipeline:** `internal/api/**`, `internal/patchstate/**`,
  `internal/proxy/**`, `vscode/**`, `cmd/mcp-ctl/**`, `README.md`,
  `CHANGELOG.md`, `.mcp.json`.

**Frozen integration contract (placeholder names — all three installer bits
substitute identically, and the test harness mirrors these constants):**

| Placeholder | Source | Validator |
|---|---|---|
| `__GATEWAY_URL__` | `$MCP_GATEWAY_URL` env, fallback `http://127.0.0.1:8765` | non-empty string; must parse as URL |
| `__GATEWAY_AUTH_TOKEN__` | POSIX: `~/.mcp-gateway/auth.token` · Windows: `$env:USERPROFILE\.mcp-gateway\auth.token` | `^[A-Za-z0-9_\-\.]+$` (SP4-cross — reject on any metachar, abort apply, preserve `.bak`) |
| `__PATCH_VERSION__` | first-line version comment in `porfiry-mcp.js` (e.g. `/* === MCP Gateway Patch v1.0.0 === */`) | semver regex |

Post-write permission lockdown (T16.4.1.a): POSIX `chmod 600 "$INDEX_JS"`;
Windows `.ps1` uses `icacls "$INDEX_JS" /inheritance:r /grant:r "${env:USERNAME}:(F)"`
— mirrors the Protected-DACL + single current-user ALLOW pattern from
`internal/auth/token_perms_windows.go:37` (`D:P(A;;FA;;;<SID>)`).

---

### backend-dev

**Files:** `installer/patches/porfiry-mcp.js`,
`installer/patches/apply-mcp-gateway.sh`,
`installer/patches/apply-mcp-gateway.ps1` (3 files, single owner for
cohesion — placeholder names + file paths + error messages must match
byte-exactly across all three).

**Recommended session split (to avoid context-thrash fatigue per PAL
cross-val 2026-04-22):**

- **Session 1 — JS contract freeze first:** write the JS header with
  `/* === MCP Gateway Patch v1.0.0 === */` version line + the three
  `__GATEWAY_*__` placeholders in place (stubs OK for internal logic at
  this point). Freeze placeholder names + marker comment
  `"MCP Gateway Patch v"`. This unblocks parallel start on scripts.
- **Session 2 (fresh head) — fill in Alt-E JS logic + both installers:**
  proceed `a → b → c` per architect's order once contract is frozen. `.sh`
  and `.ps1` can start immediately after Session 1 closes.

**Tasks (PLAN-16 §Phase 16.4 task IDs):**

- **T16.4.3** — `porfiry-mcp.js` (~200 lines, L-size). Alt-E structure:
  React Fiber walk (`inputContainer_` → `.return` ≤ depth 80; lookup order
  `p.session?.reconnectMcpServer`, `p.actions?.reconnectMcpServer`,
  any own prop exposing `reconnectMcpServer`; first match wins). Reference
  pattern: `../claude-team-control/patches/porfiry-taskbar.js:70-98`
  (MutationObserver + `invalidateSession()` + retry schedule 2s/8s).
  **Must-implement invariants:**
  - Explicit state machine `mcpSessionState ∈ {unknown, discovering, ready, lost}`
    with transitions per PLAN-16 line 422.
  - Debounce: `DEBOUNCE_WINDOW_MS=10000` fixed-from-first with
    `DEBOUNCE_FORCE_FIRE_COUNT=10` starvation cap (SP4-L1).
  - SP4-M1 debounce-timer state-check at fire time: if state ∈
    `{lost, discovering}`, transfer coalesced action to `awaitingDiscovery`
    FIFO (bound `AWAITING_DISCOVERY_QUEUE_MAX=16`, drop-oldest).
  - Singleflight on `reconnectMcpServer(serverName)` per-server.
  - Active-tool-call suppression (DOM spinner check, cap 10s).
  - Heartbeat + poll jitter: per-tick (new random per interval) + once-at-load
    `INITIAL_SKEW_MS` persisted in `localStorage['porfiry-mcp-initial-skew']`
    with 10-min TTL (P4-04).
  - SP4-M2 error scrub: truncate 256 chars + path regex (Unix home + UNC +
    Windows drive + `/opt/claude-code/` + `/System/Library/` + `/workspace/`
    + stack-frame strip via `^\s*at\s+`). Covers all 6 T16.4.6 SP4-M2
    inputs.
  - `CONFIG` object at top of file with all named constants per PLAN-16
    line 427; merged at runtime from `config_override` response on
    `/patch-heartbeat`, with hard-bounded ranges per SP4-L2 (API contract
    lines 122–126):
    - `LATENCY_WARN_MS ∈ [5000, 300000]`
    - `DEBOUNCE_WINDOW_MS ∈ [2000, 60000]`
    - `CONSECUTIVE_ERRORS_FAIL_THRESHOLD ∈ [2, 20]`
  - Both action types `{type:"reconnect"}` and `{type:"probe-reconnect"}`
    route through the SAME code path; ack body preserves `action_type`
    metadata (P4-06).
  - All `fetch`/`reconnectMcpServer` calls wrapped `.catch(() => {})` —
    never crash the webview.
  - **Export-for-test discipline:** the functions consumed by the Node
    test harness (fiber walk, debounce, scrub regex, state machine
    transitions) MUST be written so they can be copy-ported into
    `porfiry-mcp.test.mjs` without DOM globals (mirror the pure-port
    pattern from `porfiry-taskbar.test.mjs:23-67`). The test harness
    copies normalized functions; it does NOT import the patched file.

- **T16.4.1** — `apply-mcp-gateway.sh` (POSIX, M-size). Mirror
  `../claude-team-control/patches/apply-taskbar.sh` structure verbatim:
  - Lines 1-32: `set -euo pipefail` + `--auto` mode + `SCRIPT_DIR` resolution + extension-dir discovery via `find $HOME/.vscode/extensions -maxdepth 1 -name "anthropic.claude-code-*" -type d | sort -V | tail -1` with lexicographic fallback.
  - Lines 46-58: idempotency via `grep -q "MCP Gateway Patch v"` marker, restore from `.bak` before re-patch, abort if `.bak` missing.
  - Lines 60-62: backup preservation — `[ ! -f "$INDEX_JS.bak" ] && cp "$INDEX_JS" "$INDEX_JS.bak"`.
  - Lines 74-84: token substitution via `awk -v token="$AUTH_TOKEN" -v url="$GATEWAY_URL" -v ver="$PATCH_VERSION"` — **NEVER** shell-interpolate `$TOKEN` inline (SP4-cross).
  - Token read + validate: read `~/.mcp-gateway/auth.token` bytes, validate with `case "$TOKEN" in *[!A-Za-z0-9_.\\-]*) exit 1 ;; esac` — abort with non-zero exit + preserve `.bak` on any invalid character (shell metachar `|`, `&`, `;`, `$`, backtick, `\`, quote chars, newline).
  - Placeholder survival guard: `grep -q "__GATEWAY_" "$INDEX_JS"` after awk → restore `.bak` + exit 1.
  - Post-write: `chmod 600 "$INDEX_JS"` (T16.4.1.a).
  - `--uninstall` mode: restore `.bak`, remove marker, no-op if not patched.

- **T16.4.2** — `apply-mcp-gateway.ps1` (Windows, M-size). PowerShell parity of `.sh` semantics:
  - Extension discovery: `Get-ChildItem "$env:USERPROFILE\.vscode\extensions" -Directory -Filter "anthropic.claude-code-*" | Sort-Object { [version]($_.Name -replace '^anthropic\.claude-code-','') } | Select-Object -Last 1`.
  - Token validator guard: `if ($token -notmatch '^[A-Za-z0-9_\-\.]+$') { Write-Error "Invalid token format"; exit 1 }`.
  - Byte-safe write via `[System.IO.File]::WriteAllBytes($path, [System.Text.Encoding]::UTF8.GetBytes($content))` — no `cmd.exe` interpolation path.
  - Placeholder survival guard: `if ((Get-Content $INDEX_JS -Raw) -match '__GATEWAY_') { Copy-Item $bak $INDEX_JS -Force; exit 1 }`.
  - Post-write DACL lockdown: `icacls "$INDEX_JS" /inheritance:r /grant:r "${env:USERNAME}:(F)"` — mirrors `internal/auth/token_perms_windows.go:37` Protected-DACL SDDL `D:P(A;;FA;;;<SID>)` with exactly one ALLOW ACE for current user (architect flag PLAN-16 line 521).
  - `--auto` and `--uninstall` modes, matching `.sh` exit codes.
  - Compatibility: PowerShell 5.1 and 7+ (use avoidable PS-version-specific cmdlets; prefer `-Raw` + `-Encoding UTF8` explicit flags).

- **T16.4.4** — auth-token substitution mechanism is already folded into T16.4.1 + T16.4.2 via the SP4-cross validator + awk/`WriteAllBytes` path. No separate file — mark complete when the two scripts are done.

**Dependencies (intra-agent):**

- Session 1 (JS contract freeze) → unblocks scripts start.
- Full JS implementation parallelizable with scripts once contract frozen (architect handoff note 3).

**Blocks:** code-reviewer (all 3 files), security-lead (shell injection + DACL audit).

**Estimated effort:** L (JS) + M (sh) + M (ps1) = ~2–3 working days in two sessions.

**Explicit deferrals:**

- **T16.4.5 — VSCode extension activation hook → DEFER to Phase 16.5.**
  Edits `vscode/**` which is out-of-scope for this pipeline (architect
  handoff note 4). Not assigned.
- **T16.4.1.a README §"Security considerations for the webview patch"
  documentation → DEFER to Phase 16.9 (docs + dogfood).** The in-code
  `chmod 600` / `icacls` lockdown IS in-scope; only the README prose is
  deferred because `README.md` is out-of-scope for this pipeline.

---

### test-engineer

**Files:** `installer/patches/porfiry-mcp.test.mjs`,
`configs/supported_claude_code_versions.json` (2 files).

**Tasks:**

- **T16.4.6** — `porfiry-mcp.test.mjs` (M-size). Node-built-in test harness
  (`node:test` + `node:assert/strict`). Mirror
  `../claude-team-control/patches/porfiry-taskbar.test.mjs:1-150`
  `createMockEnv()` pattern — pure-port the JS functions from
  `porfiry-mcp.js` (fiber walk, debounce, scrub, state machine); do NOT
  import the patched file. Test run command: `node --test installer/patches/porfiry-mcp.test.mjs`.
  **Required test cases (15, per PLAN-16 lines 449-465):**
  1. Fiber walk depth resolution + `mcp_method_fiber_depth` recording.
  2. Basic reconnect: `{type:"reconnect"}` → one call, ack sent.
  3. Debounce: 3 actions in 10s → 1 call, 3 acks (latest wins).
  4. Independent-server: two serverNames in 10s → 2 parallel calls.
  5. Active-tool-call suppression via `[class*="spinnerRow_"]`.
  6. Failed fiber walk heartbeat shape (`fiber_ok:false`, `mcp_session_state:"discovering"`, no reconnect attempted).
  7. Heartbeat shape — assert new fields present + old fields (`registry_ok`, `reload_command_exists`) absent.
  8. Flapping test — 10 alternating good/error actions at 200ms → ≤ 2 reconnect calls, all 10 acked.
  9. Singleflight — 5 same-serverName actions during in-flight 3s Promise → exactly 1 call, 6 acks.
  10. State-machine root remount → `ready → lost → discovering → ready` transitions; actions during `lost` queued not dropped.
  11. Jitter — mock `Math.random`; assert heartbeat fires within `[HEARTBEAT_INTERVAL_MS, +HEARTBEAT_JITTER_MAX_MS]` across 100 intervals, uniform distribution, fresh random each tick.
  12. **P4-06 probe-reconnect** — `{type:"probe-reconnect", serverName:"__probe_nonexistent_abc123"}` → `reconnectMcpServer` called, ack body has `action_type:"probe-reconnect", ok:false, error_message:"Server not found: …", latency_ms:<n>`.
  13. **P4-02 remount-during-inflight** — 3s Promise, remount at t=1s, settle at t=3.5s → original action acked with settled result; new actions queued in `awaitingDiscovery`, fire after re-discovery.
  14. **P4-03 debounce starvation-cap** — 12 actions at 500ms intervals → force-fire at 10th (no wait for window), 1 call, 12 acks with coalesced result.
  15. **P4-04 initial-skew persistence** — load draws + stores `INITIAL_SKEW_MS` in `localStorage` with TTL; reload within TTL reuses; reload after TTL re-draws.
  16. **SP4-M1 debounce-fires-during-lost** — window armed at t=0,1,2s; `ready→lost` at t=5s; timer fires at t=10s → detects state=lost, transfers to `awaitingDiscovery`, no call on null session; after re-discovery at t=15s, queued action fires once, 3 original actions acked.
  17. **SP4-M2 error-scrub 6-input coverage** — (a) `/Users/alice/...` + stack, (b) `/workspace/...`, (c) `/opt/claude-code/...` in first line, (d) `\\corp-server\share\...` UNC, (e) `/System/Library/...`, (f) `C:\Users\alice\AppData\...` — for each, emitted `last_reconnect_error` contains `<path>` placeholder AND NO substring of the original path survives.

  *(Note: PLAN-16 calls out 15 cases; the list above breaks SP4-M2 into its
  own case for clarity, yielding 17 subtests under the same T16.4.6 scope.
  Passes architect test-traceability audit per PLAN-16 line 515.)*

- **T16.4.7** — `configs/supported_claude_code_versions.json` (S-size,
  static seed values from spike `docs/spikes/2026-04-20-reload-plugins-probe.md`):
  ```json
  {
    "min": "2.0.0",
    "max_tested": "2.5.8",
    "known_broken": [],
    "last_verified": "2026-04-21",
    "alt_e_verified_versions": ["2.1.114"],
    "observed_fiber_depths": {"2.1.114": 2},
    "max_accepted_fiber_depth": 80,
    "observed_reconnect_latency_ms_p50": 5400,
    "observed_reconnect_latency_note": "Single-point measurement on 2026-04-21 (server='pal'). Expand to p50/p95/p99 over accumulated heartbeat telemetry once the patch ships; update this field weekly via `mcp-ctl stats versions` (Phase 17 candidate)."
  }
  ```
  Shape must be byte-identical to the `/compat-matrix` response schema
  (API contract lines 349–363). Validate with `jq empty` or equivalent in
  the GATE.

  **P4-07(a) pre-ship probe expansion — DEFER:** PLAN-16 line 485-491
  requires live-measuring reconnect latency on ≥ 2 backends and ≥ 2
  machines BEFORE merge, updating `observed_reconnect_latency_ms_p95` and
  potentially raising `DEBOUNCE_WINDOW_MS`. This is a physical-measurement
  task requiring running gateways; flag to qa-lead for the T16.4.GATE
  pre-merge checklist. Update the JSON seed with measured p95 at that
  gate point, not now.

**Dependencies (intra-agent):** `porfiry-mcp.test.mjs` depends on
backend-dev Session 1 (frozen JS placeholder contract) — can be drafted
once placeholder names + CONFIG keys are locked, even before full JS
implementation lands (architect handoff note 3). `supported_claude_code_versions.json`
is fully parallelizable from pipeline start (no code deps).

**Blocks:** qa-lead GATE (runs `node --test` + `jq empty`).

**Estimated effort:** M (tests) + S (json) = ~1–1.5 working days, mostly
parallel to backend-dev Session 2.

---

### code-reviewer

**Files (review, do not modify):** all 5 in-scope files, after both
backend-dev and test-engineer complete.

**Scope:** one `mcp__pal__codereview` pass covering quality + security +
correctness. Must flag if any of the following diverge between `.sh` and
`.ps1`:

- Extension-dir discovery resolves to different path classes (path-segment-by-segment comparison, not string equality).
- Exit codes for identical failure modes (token invalid, `.bak` missing, placeholder survived, extension not found).
- Permission lockdown invocation (chmod 600 vs icacls Protected-DACL — both must narrow to current-user-only).
- Token validator regex (must be `^[A-Za-z0-9_\-\.]+$` byte-identical).
- Idempotency marker string (`"MCP Gateway Patch v"` byte-identical).

**Dependencies:** backend-dev + test-engineer both complete.

**Blocks:** qa-lead GATE.

**Estimated effort:** S (~2–4 hours of review time).

---

### qa-lead

**Files (execute, do not modify):** runs the gate on all 5.

**T16.4.GATE checklist:**

1. `node --test installer/patches/porfiry-mcp.test.mjs` — all 17 subtests pass, 0 failures.
2. `shellcheck installer/patches/apply-mcp-gateway.sh` — 0 findings (not "0 errors" — clean).
3. PowerShell syntax: `[System.Management.Automation.Language.Parser]::ParseFile("installer/patches/apply-mcp-gateway.ps1", [ref]$null, [ref]$null)` — 0 parse errors.
4. `jq empty configs/supported_claude_code_versions.json` — valid JSON; shape matches `/compat-matrix` response schema in API contract lines 349–363.
5. `mcp__pal__codereview` on all 5 files — 0 findings at or above `CLAUDE_GATE_MIN_BLOCKING_SEVERITY` (default: any finding).
6. `mcp__pal__thinkdeep` on Alt-E failure modes + debounce correctness + fiber walk resilience — 0 findings.
7. **P4-07(a) pre-ship probe expansion** — raw latency measurements recorded in `docs/spikes/2026-04-20-reload-plugins-probe.md` §"Additional measurements" from ≥ 2 backends + ≥ 2 machines; p95 updated in JSON seed; if observed p95 > 8000 ms, `DEBOUNCE_WINDOW_MS` raised to `max(10000, p95 * 1.5)` in `porfiry-mcp.js`.
8. Spike T16.0 sign-off still valid for target Claude Code versions (spot-check spike report dates).

**Dependencies:** backend-dev + test-engineer + code-reviewer + security-lead all complete.

**Blocks:** pipeline completion.

**Estimated effort:** S for gate execution; **bounded by P4-07(a)
measurement time** which may require coordination with another operator
for the second machine.

---

### security-lead

**Files (audit):** `installer/patches/apply-mcp-gateway.sh`,
`installer/patches/apply-mcp-gateway.ps1`,
`installer/patches/porfiry-mcp.js`.

**Audit scope (focus areas):**

1. **Shell-injection surface (SP4-cross):**
   - `apply-mcp-gateway.sh` — verify `awk -v` variable-passing only; no `sed -i "s/X/$TOKEN/"` unquoted expansions; no `eval` / `source` / backtick / `$(…)` around token value; token bytes read once, validated once, substituted once.
   - Integration test check: poisoned-token file containing `malicious";cat /etc/passwd;"` → script aborts non-zero + `.bak` preserved byte-identical.
2. **Windows DACL (T16.4.1.a):** verify `icacls /inheritance:r /grant:r "${env:USERNAME}:(F)"` produces exactly one ACE, Protected, current-user ALLOW. No ADD-ON ACEs from inheritance leak.
3. **Token handling in-memory (porfiry-mcp.js):** verify inlined token is used ONLY as `Authorization: Bearer …` header; no token logged, stored in `localStorage`, emitted in heartbeat payload, or exposed through any `console.*` path; scrubbed-error pipeline does not accidentally expose the token substring (add token-value-specific scrub case if not covered).
4. **CORS and auth on the patch's fetch targets:** review fetch URL construction — no protocol downgrade, no server-side URL reflection (URL is compile-time from `__GATEWAY_URL__` substitution, never constructed from runtime DOM/props).
5. **`.bak` race:** verify the read-then-validate-then-substitute cycle in one script run; no parallel readers — document the invariant in script comments.

**Dependencies:** backend-dev complete (all 3 files written).

**Blocks:** qa-lead GATE.

**Estimated effort:** S (~3–4 hours).

---

### doc-writer

**Files:** none in this pipeline.

**Deferrals:**

- **README §"Security considerations for the webview patch" → DEFER to
  Phase 16.9 (docs + dogfood).** PLAN-16 T16.4.1.a mentions documenting
  in README, but `README.md` is out-of-scope for this pipeline. Phase
  16.9 owns the README update (see T16.9.1, T16.9.2).
- **CHANGELOG.md v1.6.0 entry → Phase 16.9 T16.9.6** (already assigned).

No work orders for doc-writer this pipeline.

---

### Risks + mitigations (recorded for audit trail)

| Risk | Mitigation | Owner |
|---|---|---|
| Shell injection via poisoned `~/.mcp-gateway/auth.token` | SP4-cross allowlist regex + `awk -v` variable passing (proven in `apply-taskbar.sh`); integration test with malicious token content | backend-dev, security-lead |
| Token file read race on concurrent apply invocations | Single-run read-then-validate-then-substitute; no parallel readers; document in script comments | backend-dev |
| `.bak` corruption on re-patch | Restore from `.bak` before re-apply (`apply-taskbar.sh:47-58` pattern); abort if `.bak` missing | backend-dev |
| Placeholder survival after substitution (corrupted patch file) | `grep`/`-match` guard after awk/`WriteAllBytes`; restore `.bak`, exit non-zero | backend-dev |
| Windows extension path discovery (semver sort vs lexicographic) | `[version]` cast in `Sort-Object` (PowerShell); `sort -V` with lexicographic fallback (bash) | backend-dev |
| `.ps1` non-trivial ACL / PS-version compatibility (PAL cross-val flag) | Explicit smoke-test on clean Windows VM as part of T16.4.GATE step 3; target PS 5.1 + 7+ | qa-lead |
| Single-point latency measurement for `DEBOUNCE_WINDOW_MS` | P4-07(a) pre-ship probe expansion (≥ 2 backends + ≥ 2 machines); raise `DEBOUNCE_WINDOW_MS` to `max(10000, p95 * 1.5)` if observed p95 > 8000 ms; post-ship runtime `config_override` (P4-07(b)) for further recalibration | qa-lead, backend-dev |
| `registry_ok` / `reload_command_exists` old heartbeat fields reappearing | Heartbeat-shape test explicitly asserts absence (T16.4.6 case 7) | test-engineer |

---

### Pipeline sequence

1. **backend-dev Session 1** — JS contract freeze (header + placeholders + marker).
2. **backend-dev Session 2 + test-engineer** — parallel execution:
   - backend-dev: full `porfiry-mcp.js` + `apply-mcp-gateway.sh` + `apply-mcp-gateway.ps1`.
   - test-engineer: `porfiry-mcp.test.mjs` + `supported_claude_code_versions.json`.
3. **security-lead** — audit shell/DACL/token surfaces on the 3 installer files.
4. **code-reviewer** — `mcp__pal__codereview` on all 5 files + divergence check between `.sh` and `.ps1`.
5. **qa-lead** — T16.4.GATE execution (node:test + shellcheck + PS parse + jq + PAL review + P4-07(a) probe measurements + spike sign-off).

Pipeline completes when step 5 passes with zero findings at or above gate threshold.

