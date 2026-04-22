# Plan: Phase 16 — Claude Code Integration (v1.6.0)

## Session: 16 → `docs/PLAN-16.md`

## Context

Phase 16 closes the three findings surfaced during the 2026-04-20 post-v1.5.0 audit (see `docs/AUDIT.md`):

- **HIGH — Missing bootstrap path Claude Code ↔ Gateway.** Repo has zero writers for `.mcp.json` / `~/.claude.json`; zero references to `claude mcp add`; README has no "Connecting Claude Code" section; even the repo's own `.mcp.json` registers MCPs directly (`context7`, `orchestrator`, `playwright`) and bypasses the gateway. The "live control plane for MCP" promise in README:5 is undeliverable for a new user without hidden knowledge.
- **MEDIUM — tools/list caching per Claude Code Issue #13646.** go-sdk v1.4.1 correctly emits `notifications/tools/list_changed` via `changeAndNotify` (SDK server.go:282,511), but Claude Code does NOT register a handler for it. Anthropic closed hot-reload Issue #18174 as "not planned". Hot-add via REST + dashboard therefore does not surface new tools in an active Claude Code session without a manual action.
- **MEDIUM — `.claude/commands/*.md` semantic confusion.** `SlashCommandGenerator` writes prompt templates that visually resemble MCP registrations. Users reasonably conclude that's the Claude integration; no disclaimer in README or file header dispels this.

**Research already completed (do NOT re-run)**:
- PAL `thinkdeep` gpt-5.2-pro (2 rounds) confirmed dual-mode design, flagged overlap-suppression UX risk, recommended tri-state health indicators.
- PAL `consensus` gpt-5.2-pro + gpt-5.1-codex: both converged on "A+C" (status-quo + meta-tools) initially, then upgraded to hybrid+plugin with `/reload-plugins` automation once webview-patch mechanism was validated.
- PAL `challenge`: HIGH rating on bootstrap confirmed; counter-arguments (user-owned config, out-of-scope) defeated by zero README guidance + author's own dogfood bypass.
- Webview patch precedent: `claude-team-control/patches/porfiry-taskbar.js` already walks the React Fiber tree and calls live session methods directly — `session.setModel()`, `session.setThinkingLevel()`, `session.applySettings()`. One action (`appRegistry.executeCommand("fast")`) goes through the command registry; all others are session-direct calls. Phase 16.0 SPIKE (2026-04-21) confirmed the **Alt-E path**: `session.reconnectMcpServer(serverName)` is present on the same fiber object neighborhood at depth=2, structurally identical to `session.setModel`. This is the primitive 16.4 actually uses — not `executeCommand("reload-plugins")`, which does not exist in the webview bundle.
- go-sdk API verified: `NewStreamableHTTPHandler(getServer func(*http.Request) *Server, ...)` per-request routing confirmed (streamable.go:187-192).

## Scope Assessment

### In scope

1. Gateway dual-mode: existing `/mcp` aggregate endpoint untouched (backward compat), plus new `/mcp/{backend}` per-backend proxy endpoints via `getServer(req)` routing.
2. Claude Code Plugin packaging (`.claude-plugin/plugin.json` + regenerated `.mcp.json`), local marketplace for installation.
3. Webview patch (`apply-mcp-gateway.sh` + `porfiry-mcp.js`) that automates MCP reconnect (via native `session.reconnectMcpServer("mcp-gateway")` Alt-E path, live-verified on CC 2.1.114) on backend add/remove.
4. Dashboard "Claude Code Integration" panel with `[Activate for Claude Code]` button, `[ ] Auto-reload plugins` checkbox, and two independent status indicators (Patch + Channel, tri-state).
5. Bootstrap CLI (`mcp-ctl install-claude-code`) for headless / CI setup.
6. `gateway.invoke(backend, tool, args)` universal fallback tool — works when tools/list refresh fails entirely.
7. Supported-versions map (`supported_claude_code_versions.json`) with compat matrix.
8. Dogfood: replace repo `.mcp.json` so every MCP goes through the gateway.
9. Disclaimer header in every `.claude/commands/<server>.md` + README "Commands vs MCP servers" section.
10. ADR-0005 capturing the dual-mode + plugin + webview-patch hybrid decision.

### Out of scope (explicitly deferred)

- **SIGHUP + shell-wrapper restart (Panozzo trick)** — works but requires installing a shell function (`claude()` wrapper) in user's rc file. Invasive for a v1.6 shipping priority. Park as Phase 17 candidate if webview-patch proves fragile across Claude Code versions.
- **Stdio MCP transport reload** — the disconnect-reconnect cache-bust only works for HTTP/SSE. stdio clients are out of scope; document as known limitation.
- **Socket-injection hack (`claude-commander` pattern)** — unofficial, no upstream support; too fragile.
- **Suppress aggregate tools when a backend is also surfaced via plugin** — PAL specifically warned against this: creates "disappearing tools" UX bug when plugin hasn't reloaded yet. Accept harmless duplication; differentiate through descriptions only.

### Pre-requisites

- Go 1.25+ (inherited from v1.5.0).
- Phase 12.A Bearer auth shipped (token at `~/.mcp-gateway/auth.token` with POSIX 0600 / Windows DACL).
- go-sdk `v1.4.1` or newer (getServer callback API).
- Node 20+ for VS Code extension builds.
- Claude Code `2.0.0 – 2.5.x` confirmed-supported; 2.6+ enters "untested" band via supported-versions map.
- Existing claude-team-control `patches/` directory as pattern reference (copy, don't import).

### Key risks (ranked)

| # | Risk | Likelihood | Impact | Mitigation |
|---|------|------------|--------|------------|
| R-1 | Claude Code update changes fiber object shape / renames/removes `reconnectMcpServer` on the session object (Alt-E dependency) | Medium | High | Version-detect in patch; heartbeat reports `fiber_ok` + `mcp_method_ok` + `mcp_method_fiber_depth`; aggregate mode always works as fallback; supported-versions map tracks `alt_e_verified_versions` + `observed_fiber_depths` per CC version; dashboard YELLOW-flags unverified versions (mode C in 16.5.5). |
| R-2 | Webview CORS blocks `fetch("http://127.0.0.1:8765")` from patched page | Medium | High | Gateway CORS config: `allowed_origins` must include `vscode-webview://` schema; integration test pins this. If blocked, fallback to file-based IPC (write sentinel file in `~/.mcp-gateway/pending-actions/`). |
| R-3 | `reconnectMcpServer` side effects interrupt active Claude turn | Low | Medium | Debounce 10s (2× observed reconnect latency of 5.4s — live probe 2026-04-21); skip if active tool call in progress (patch reads DOM spinner indicator same as porfiry-taskbar.js `enforceLock`); postpone up to 10s, then fire. Live probe on server `"pal"` confirmed active chat context is preserved — no observed interruption. |
| R-4 | Plugin `.mcp.json` regen races with Claude Code reading it | Low | Medium | Atomic write (temp + rename); no partial states visible to reader. |
| R-5 | Auth token exposed in webview fetch request | Low | High | Bearer header injected client-side in fetch options; never logged; patched page stripped of console.log in release build. |
| R-6 | User has both aggregate (gateway-all) AND plugin entries → tool duplication confuses Claude | Low | Low | Tool description differentiates: aggregate prefix `[gateway-aggregate]`, plugin prefix `[context7]` etc. Claude can reason about the difference. PAL-recommended; no suppression. |
| R-7 | Patch file stale after Claude Code extension auto-update | High | Medium | `apply-mcp-gateway.sh --auto` runs on VSCode session start via extension activation event; detects extension version change and re-applies. |
| R-8 | Dogfood `.mcp.json` replacement breaks developer flow mid-session | Low | High | Change lands in a single commit; developers re-register once via `mcp-ctl install-claude-code`. Documented in commit message. |

---

## Phase 16.0 — SPIKE: Verify `executeCommand("reload-plugins")` works

**Goal:** Confirm the webview-patch mechanism actually fires `/reload-plugins` before committing phases 16.4/16.5. If this spike fails, the whole auto-reload story collapses and we scope back to aggregate-only + manual path.

**No code changes land in this phase.** Output is a spike report + maintainer go/no-go on 16.4.

### Tasks

- [ ] T16.0.1 — Open Claude Code webview in VSCode → `Developer: Open Webview Developer Tools`. In console, execute the fiber-walk pattern verbatim from `porfiry-taskbar.js:65-93`:
  ```javascript
  var el = document.querySelector('[class*="inputContainer_"]');
  var fk = Object.keys(el).find(k => k.startsWith("__reactFiber$"));
  var fiber = el[fk];
  var registry = null;
  for (var i = 0; i < 60 && fiber; i++, fiber = fiber.return) {
    if (fiber.memoizedProps?.context?.commandRegistry) {
      registry = fiber.memoizedProps.context.commandRegistry;
      break;
    }
  }
  console.log(Object.keys(registry || {}));
  ```
  Record whether `registry.executeCommand` is a function and whether `"reload-plugins"` is in the command list (e.g. via `registry.commands`, `registry.names()`, or similar — inspect the object).

- [ ] T16.0.2 — Add a dummy MCP server to the active `.mcp.json` (outside gateway scope; e.g. a local stub server). Execute `registry.executeCommand("reload-plugins")` in the console. Verify:
  - No exception thrown
  - `/mcp` panel reflects the new dummy server within 5 seconds
  - No visible side effect on the active conversation (no reload, no lost context)

- [ ] T16.0.3 — Test `registerCommand` availability (needed for `__mcp_gateway_probe` no-op in 16.5):
  ```javascript
  if (typeof registry.registerCommand === "function") {
    registry.registerCommand({
      name: "__mcp_gateway_probe_test",
      execute: () => console.log("probe fired")
    });
    registry.executeCommand("__mcp_gateway_probe_test");
  }
  ```
  Record result. If `registerCommand` missing, fallback plan: reuse an existing no-side-effect builtin or skip active `[Test now]` verification entirely (16.5 downgrades to passive-only).

- [ ] T16.0.4 — Test patch resilience: Claude Code's React may re-mount `#root` on HMR. Run fiber walk a second time after focusing/unfocusing the window. Record whether `registry` reference remains valid or needs re-discovery.

- [ ] T16.0.5 — Write `docs/spikes/2026-04-2X-reload-plugins-probe.md` with findings:
  - Claude Code version tested (e.g. `anthropic.claude-code-2.5.8`)
  - Fiber-walk path confirmed (which `memoizedProps` chain works)
  - `executeCommand("reload-plugins")` result (works / errors / no-op)
  - Latency measurement (ms from command to `/mcp` panel update)
  - `registerCommand` availability
  - Go/no-go recommendation for Phase 16.4

- [x] T16.0.1 — Fiber walk confirmed (superseded by Alt-E: `session.reconnectMcpServer` at depth=2, see spike report 2026-04-20-reload-plugins-probe.md §Live verification results).
- [x] T16.0.2 — Original probe obsolete. Alt-E real-server reconnect live-verified: `reconnectMcpServer("pal")` resolved in 5404ms with `{type:"reconnect_mcp_server_response"}`, active chat session preserved.
- [x] T16.0.3 — `registerCommand` probe obsolete under Alt-E (we don't register new commands — we call a native method).
- [x] T16.0.4 — `MutationObserver` on `#root` pattern carried over from porfiry-taskbar.js (verified resilient on this CC version). Formal resilience re-test deferred to T16.4 implementation.
- [x] T16.0.5 — Spike report written at `docs/spikes/2026-04-20-reload-plugins-probe.md` (three passes documented).
- [x] T16.0.GATE: **PASSED 2026-04-21** — Alt-E live probe all 3 steps PASS. Phase 16.4 redesign under Alt-E (see spike report §"Redesign of Phase 16.4 under Alt-E"). Original `executeCommand("reload-plugins")` design abandoned. No rescope triggered — 16.4/16.5 stay in scope with narrower, cleaner implementation.

### Rollback

Nothing to roll back — this phase has no code changes. Spike report stays in `docs/spikes/` as historical reference.

### Rescope on spike failure (OBSOLETE — spike PASSED 2026-04-21)

**This block is historical.** The spike PASSED on 2026-04-21 via Alt-E (see T16.0.GATE above), so no rescope was triggered. 16.4/16.5 remain in scope. The fallback text below is kept for documentation and for re-use IF a future CC version breaks Alt-E fiber walk (a real possibility — hence `alt_e_verified_versions` tracking in T16.4.7).

If a future re-spike against a newer Claude Code version FAILS Alt-E:

- Drop phases 16.4 and 16.5 (patch + dashboard auto-reload).
- Keep 16.1 (dual-mode), 16.2 (plugin packaging), 16.3 (REST endpoints — scoped back to plugin heartbeat without patch), 16.6–16.9.
- Dashboard still gets `[Activate for Claude Code]` button (plugin install) but NOT the auto-reload checkbox.
- Manual workflow documented: after adding an MCP, user right-clicks `mcp-gateway` entry in Claude Code `/mcp` panel → **Reconnect**. (The original rescope text here referenced a `/reload-plugins` slash command — that command does NOT exist in Claude Code, confirmed by spike §F-4. The UI-panel "Reconnect" is the correct manual primitive.)
- Phase 17 candidate: SIGHUP+wrapper as alternative restart path.

---

## Phase 16.1 — Gateway dual-mode (aggregate + per-backend proxy)

**Goal:** Add `/mcp/{backend}` per-backend endpoints without breaking existing `/mcp` aggregate. Backward-compatible by construction.

### Tasks

- [x] T16.1.1 — Extend `internal/proxy/gateway.go`: replace single `server *mcp.Server` field (gateway.go:28) with:
  ```go
  aggregateServer  *mcp.Server           // existing behavior, keyed by "" (empty string)
  perBackendServer map[string]*mcp.Server // new: keyed by backend name
  serverMu         sync.RWMutex          // guards perBackendServer map mutations
  ```
  `Gateway.Server()` returns `aggregateServer` (unchanged API). New method `Gateway.ServerFor(backend string) *mcp.Server` returns per-backend instance or nil.

- [x] T16.1.2 — Extend `Gateway.RebuildTools()` (gateway.go:91) to update BOTH registries:
  - Aggregate: existing namespaced tools `<server>__<tool>` with description prefix `[<server>] ...` (gateway.go:141) — unchanged.
  - Per-backend: for each running backend, ensure `perBackendServer[backend]` exists (lazy-create with `mcp.NewServer(&mcp.Implementation{Name: backend, Version: g.version}, nil)`); register that backend's tools WITHOUT namespace prefix, and description WITHOUT `[<server>]` prefix. When a backend is removed, tear down its `perBackendServer` entry.

- [x] T16.1.3 — Add `registerToolForBackend(nt namespacedTool)` helper mirroring `registerTool()` (gateway.go:140) but writing to `perBackendServer[nt.server]`. Reuse `router.Call` for dispatch via the same `nt.namespaced` path — per-backend view is just a different surface, same underlying routing.

- [x] T16.1.4 — Add `internal/proxy/gateway_proxy_test.go`:
  - Unit: `TestRebuildTools_DualMode` — two backends, aggregate has both namespaced, per-backend has each independently unnamespaced.
  - Unit: `TestPerBackendServer_ListChangedScoping` — adding a tool to backend X fires `list_changed` on `perBackendServer[X]` only, not on other per-backend servers (verify via SDK session mock).
  - Unit: `TestPerBackendServer_ToolDescriptionNoBrackets` — `[context7]` prefix MUST be absent in per-backend view.
  - Unit: `TestConcurrentRebuildAndBackendAdd` — race-safe (reuse pattern from existing `TestConcurrentRebuildAndFilteredTools` at gateway_test.go:310).

- [x] T16.1.5 — Extend `internal/api/server.go:200-213` HTTP handler wiring. Replace:
  ```go
  streamableHandler := mcp.NewStreamableHTTPHandler(
      func(r *http.Request) *mcp.Server { return mcpServer }, nil,
  )
  ```
  With a dispatcher that reads `r.URL.Path`:
  - `/mcp` → `g.aggregateServer`
  - `/mcp/<backend>` → `g.ServerFor(backend)` (404 / nil → 400 Bad Request per SDK semantics at streamable.go:190)
  - Preserve existing `/mcp/*` streamable path via prefix match carefully — the new `/mcp/{name}` MUST NOT swallow SDK-internal streamable sub-paths. Verify with SDK source at streamable.go to understand wildcard behavior; router rules:
    - Exact `/mcp` → aggregate
    - `/mcp/{backend}` where `{backend}` matches `SERVER_NAME_RE` → per-backend
    - `/mcp/{anything-else}` → aggregate (streamable internal paths preserved)

- [x] T16.1.5.a — **[REVIEW-16 L-01]** SDK path verification. Verified go-sdk v1.4.1 `streamable.go::ServeHTTP` is single-endpoint, parses `mcp-session-id` from the request header only, registers no URL sub-paths. Documented inline at `internal/api/server.go` `mcpServerForRequest` dispatcher comment. Invalid backend names fall through to aggregate as defense-in-depth against any future SDK sub-path introduction.

- [x] T16.1.6 — Add `internal/api/server_proxy_test.go`:
  - `TestStreamablePerBackendRoute` — GET /mcp/context7 returns context7's serverInfo
  - `TestStreamableUnknownBackend404` — /mcp/nonexistent returns 400 (SDK default for nil return)
  - `TestAggregateRouteStillWorks` — /mcp returns aggregate serverInfo
  - `TestPerBackendAuthRequiresSameBearer` — same Bearer token works for both paths

- [x] T16.1.7 — `mcpTransportPolicy` middleware uniformity: confirmed applied identically to `/mcp` and `/mcp/*` via `r.Handle("/mcp", policy(...))` and `r.Handle("/mcp/*", policy(...))` at `internal/api/server.go:210-211`. `TestPerBackendAuthRequiresSameBearer` asserts bearer-required rejects without-bearer 401 on both paths and accepts with correct bearer on both.

- [x] T16.1.GATE: **PASSED 2026-04-21** — `go test ./...` -> 12/12 packages PASS including 8 new Phase 16.1 subtests; `go vet ./...` clean; `mcp__pal__thinkdeep` async gpt-5.1-codex PASS 0 findings (task rev-b8fc7a52ae0e, 98s); `mcp__pal__codereview` gpt-5.1-codex internal (fallback after async timeout) PASS 0 findings gate_verdict=PASS. Coverage: internal/proxy 89.2%, internal/api 74.8%. Dual-mode `/mcp` aggregate + `/mcp/{backend}` per-backend now active; backward-compatible by construction.

### Files

- `internal/proxy/gateway.go` (modify)
- `internal/proxy/gateway_proxy_test.go` (new)
- `internal/api/server.go` (modify handler wiring)
- `internal/api/server_proxy_test.go` (new)

### Rollback

`git revert` on the gateway.go + server.go commit restores single-server mode. New per-backend tests are additive — they fail cleanly on revert because `ServerFor` no longer exists. Existing aggregate clients unaffected.

---

## Phase 16.2 — Plugin packaging (manifest + `.mcp.json` regen + local marketplace)

**Goal:** Produce a Claude Code Plugin that declares our backends as N namespaced MCP entries. Plugin is installable via `claude plugin install`.

### Tasks

- [x] T16.2.1 — Create `installer/plugin/` directory structure:
  ```
  installer/plugin/
  ├── .claude-plugin/
  │   └── plugin.json       # metadata + userConfig
  ├── .mcp.json             # generated at runtime; checked-in stub with no mcpServers
  └── README.md             # plugin user docs
  ```

- [x] T16.2.2 — Author `installer/plugin/.claude-plugin/plugin.json`:
  ```json
  {
    "name": "mcp-gateway",
    "version": "1.6.0",
    "description": "MCP Gateway — aggregates and manages multiple MCP servers",
    "author": {"name": "Stanislav Naumov", "url": "https://github.com/blicksten"},
    "repository": "https://github.com/blicksten/mcp-gateway",
    "license": "MIT",
    "userConfig": {
      "gateway_url": {
        "description": "Gateway base URL (default: http://127.0.0.1:8765)",
        "sensitive": false
      },
      "auth_token": {
        "description": "Bearer token from ~/.mcp-gateway/auth.token",
        "sensitive": true
      }
    }
  }
  ```
  Notes: `sensitive: true` stores in OS keychain per Claude Code plugins-reference §userConfig. Never inline `mcpServers` in `plugin.json` — Issue #16143 drops it.

- [x] T16.2.3 — Implement `internal/plugin/regen.go`:
  ```go
  type Regenerator struct {
      mu sync.Mutex // [REVIEW-16 M-02] serialize concurrent regens
  }
  func (r *Regenerator) Regenerate(pluginDir string, backends []models.BackendEntry, gatewayURL string) error
  ```
  - Atomic write (tmp + rename).
  - Pre-write backup to `.mcp.json.bak` if existing file differs.
  - JSON-validate output before rename (parse into generic `map[string]any`).
  - **[REVIEW-16 L-05]** Gateway OWNS the file. Unconditional overwrite with generated banner at top:
    ```json
    {
      "// GENERATED": "mcp-gateway — DO NOT EDIT. Regenerated on every backend mutation.",
      "mcpServers": { ... }
    }
    ```
  - **[REVIEW-16 M-02]** Serialize concurrent callers via `Regenerator.mu`. Arrival order at gateway = application order. Prevents lost-update race.
  - Use `${user_config.auth_token}` placeholder for Bearer header (Claude Code substitutes at runtime).

- [x] T16.2.4 — Wire `TriggerPluginRegen` into gateway lifecycle:
  - Call on `POST /api/v1/servers` (backend added) — api/server.go:handleAddServer after RebuildTools.
  - Call on `DELETE /api/v1/servers/{name}` — api/server.go:handleRemoveServer after RebuildTools.
  - Call on `PATCH /api/v1/servers/{name}` if `disabled` flag changes (disabled backend should drop out of plugin view). Env/header-only patches skipped since they don't affect plugin surface.
  - **[PAL-TD-GAP2]** Call once on daemon startup after `lm.StartAll + gw.RebuildTools` so fresh daemons bootstrap `.mcp.json` even without a REST mutation.
  - **[PAL-TD-GAP1]** Call on config-watcher reload after `UpdateConfig + gw.RebuildTools` so config.json edits outside REST propagate to the plugin file.
  - Idempotent: no-op if generated content matches existing file.

- [x] T16.2.5 — Plugin directory discovery. Gateway needs to know WHERE to write `.mcp.json`. Two paths:
  - **Dev path** (from repo): `$GATEWAY_PLUGIN_DIR` env var points to `installer/plugin/`.
  - **Installed path** (post `claude plugin install`): `~/.claude/plugins/cache/mcp-gateway@<marketplace>/`.
  - Implement `internal/plugin/discover.go` with `FindPluginDir() (string, error)` that walks:
    1. `$GATEWAY_PLUGIN_DIR` if set
    2. `~/.claude/plugins/cache/mcp-gateway@*/` (glob)
    3. Return nil + user-friendly error if neither found (regen is skipped, not fatal).

- [x] T16.2.6 — Author `installer/marketplace.json` (local marketplace for one-command install):
  ```json
  {
    "name": "mcp-gateway-local",
    "version": "1.0.0",
    "plugins": [
      {"name": "mcp-gateway", "source": "./plugin/"}
    ]
  }
  ```
  README documents: `claude plugin marketplace add <repo-path>/installer/marketplace.json && claude plugin install mcp-gateway@mcp-gateway-local`.

- [x] T16.2.7 — Add `internal/plugin/regen_test.go` — 14 subtests (added Concurrent race serialization + EmptyDirRejected + DefaultPlaceholderWhenURLEmpty + per-glob-branch Discover tests beyond the 6 in plan):
  - `TestRegen_AtomicWrite` — write is visible only after rename (partial file never observable).
  - `TestRegen_Idempotent` — second call with same backends produces identical output.
  - `TestRegen_BackupOnOverwrite` — `.mcp.json.bak` contains previous content.
  - `TestRegen_JSONValid` — malformed Go struct can't produce invalid JSON (schema validation pre-rename).
  - `TestRegen_DiscoverFallbacks` — env > glob > error, cross-platform paths.
  - `TestRegen_DisabledBackendExcluded` — disabled servers don't appear in output.
  - Cross-platform: use `t.TempDir()` + `filepath.Join` everywhere; no raw slashes.

- [x] T16.2.GATE: **PASSED 2026-04-21** — `go test ./...` PASS (13 packages, 573+14=587 tests, 0 failures) + `mcp__pal__codereview` (gpt-5.1-codex, external expert) 1 HIGH finding `PAL-CR-H1` (snapshot pointer aliasing) FIXED IN-CYCLE via deep-copy under `cfgMu.RLock` + `mcp__pal__thinkdeep` (gpt-5.1-codex, external expert) 2 MEDIUM findings `PAL-TD-GAP1` (config-reload) + `PAL-TD-GAP2` (startup bootstrap) FIXED IN-CYCLE via public `TriggerPluginRegen` + wired at both sites in main.go. Final: zero findings at/above threshold. 4 other gaps (plugin-installed-post-start, plugin-dir-vanished, cache-scheme-change, token-rotation) classified LOW/out-of-scope — deferred to 16.7 integration + 16.8 CLI + 16.9 docs.

### Files

- `installer/plugin/.claude-plugin/plugin.json` (new)
- `installer/plugin/.mcp.json` (new, stub)
- `installer/plugin/README.md` (new)
- `installer/marketplace.json` (new)
- `internal/plugin/regen.go` (new)
- `internal/plugin/discover.go` (new)
- `internal/plugin/regen_test.go` (new, 14 subtests)
- `internal/api/server.go` (modify: Server.pluginRegen+pluginDir fields, SetPluginRegen setter, public TriggerPluginRegen with deep-copy snapshot under cfgMu.RLock, calls from handleAddServer/handleRemoveServer/handlePatchServer-on-Disabled-toggle)
- `cmd/mcp-gateway/main.go` (modify: plugin.Discover at startup, apiServer.TriggerPluginRegen called after initial lm.StartAll and after config-watcher reload)

### Rollback

Plugin directory is isolated under `installer/plugin/`. Revert removes the directory + `internal/plugin/` package + the regen hooks in api/server.go. Existing `/api/v1/servers` behavior unchanged when regen skipped (no plugin dir).

---

## Phase 16.3 — Gateway REST endpoints for patch integration

**Goal:** Provide the HTTP surface for the webview patch to heartbeat, poll actions, and report probe results.

### Tasks

- [x] T16.3.1 — New REST group `/api/v1/claude-code/*`, Bearer-auth-required (reuse existing auth middleware):
  - `POST /api/v1/claude-code/patch-heartbeat` — accepts Alt-E JSON payload `{patch_version, cc_version, vscode_version, fiber_ok, mcp_method_ok, mcp_method_fiber_depth, last_reconnect_latency_ms, last_reconnect_ok, last_reconnect_error, pending_actions_inflight, fiber_walk_retry_count, mcp_session_state, session_id, ts}` (see T16.4.3). Gateway stores latest per `session_id` with 1h TTL; emits structured log entry. **[P4-07]** Response body returns `{acked:true, next_heartbeat_in_ms:<n>, config_override?:{LATENCY_WARN_MS?:<n>, DEBOUNCE_WINDOW_MS?:<n>, CONSECUTIVE_ERRORS_FAIL_THRESHOLD?:<n>}}` — `config_override` is optional; if present, patch merges into its `CONFIG` object after validating each value falls within the hard-bounded range documented in T16.4.7 §(b). Override source: config.json top-level `patch_config_override` key + `/api/v1/admin/patch-config` runtime endpoint (add to T16.3 if maintainer wants live tuning without daemon restart).
  - `GET /api/v1/claude-code/patch-status` — returns array of latest heartbeats across all active sessions (for dashboard polling).
  - `GET /api/v1/claude-code/pending-actions` — returns next action for patch to execute. Alt-E action shapes: `{id, type:"reconnect", serverName:"mcp-gateway", nonce}` for production reconnect; `{id, type:"probe-reconnect", serverName:"__probe_nonexistent_" + nonce, nonce}` for dashboard probe (see 16.5.6). Idempotent read with `?after=<cursor>` for at-most-once semantics.
  - `POST /api/v1/claude-code/pending-actions/{id}/ack` — patch confirms execution. Gateway marks as delivered.
  - `POST /api/v1/claude-code/probe-result` — patch reports `[Test now]` result `{nonce, ok, error?}`.

- [x] T16.3.2 — Implement `internal/patchstate/state.go`:
  ```go
  type PatchState struct {
      mu          sync.RWMutex
      heartbeats  map[string]Heartbeat  // keyed by session_id, TTL-evicted
      actions     []PendingAction       // FIFO queue
      probes      map[string]ProbeResult // keyed by nonce, TTL 5min
      ttlCleaner  *time.Ticker
      persistPath string                // ~/.mcp-gateway/patch-state.json
  }
  ```
  Single-writer FIFO queue; eviction via background goroutine. **[REVIEW-16 M-01]** Persist `actions` + last-heartbeat-per-session to disk on every mutation (atomic tmp+rename, 0600 POSIX / Windows DACL to match auth.token). On gateway startup, load and TTL-filter (drop actions > 10 min old, heartbeats > 1 h old). Closes "pending reload-plugins lost on restart" class of bugs. Heartbeat debounce: only persist when session_id is new OR > 30 s since last persist for that session (amortize disk I/O).

- [x] T16.3.3 — Wire `RegenerateMCPJSON` (16.2.4) to ALSO enqueue a `{type:"reconnect", serverName:"mcp-gateway"}` pending action after successful plugin regen (Alt-E action shape). Debounce: if another regen fires within 500ms, coalesce into a single queued action (prevents action-flood on bulk backend operations). **Note:** the webview-side patch applies an additional 10s debounce (T16.4.3) on top of this 500ms server-side coalescing, matching observed reconnect latency. Actions that stay queued >10min are TTL-dropped on gateway restart (T16.3.2 M-01 durability). **[P4-08] Invariant:** `serverName` is ALWAYS `"mcp-gateway"` here — we enqueue a reconnect for OUR plugin's aggregate MCP entry, regardless of which individual backend inside the gateway mutated (added/removed/disabled). This is the correct behavior because Claude Code sees only one MCP server from our plugin (`mcp-gateway`), not the backends-inside-gateway. Future work for per-backend plugin entries (if ever pursued — currently rejected per "No suppression of aggregate tools" Architectural Decision) would require a different action-enqueue strategy and is OUT OF SCOPE for this phase.

- [x] T16.3.4 — CORS: add `vscode-webview://` to `Access-Control-Allow-Origin` for `/api/v1/claude-code/*` routes ONLY. The rest of `/api/v1` keeps its existing CSRF-protected origin policy. Verify via request from patched webview in integration test. **[REVIEW-16 L-02]** Explicit OPTIONS preflight handler required — browsers send OPTIONS before POST from a different origin. Respond 204 with: `Access-Control-Allow-Origin: vscode-webview://*`, `Access-Control-Allow-Methods: GET, POST`, `Access-Control-Allow-Headers: Authorization, Content-Type`, `Access-Control-Max-Age: 300`. Preflight handler runs BEFORE bearer auth (preflight has no auth header).

- [x] T16.3.5 — Rate limiting: `/pending-actions` GET polled every 2s by patch; set per-IP rate limit of 60 req/min (generous but bounded). Heartbeat has separate 5 req/min limit per session_id. **Implemented:** separate `patchStatusLimiter` vs `pendingActionsLimiter` (PAL-CR2 fix — FROZEN contract mandates independent budgets).

- [x] T16.3.6 — Add `internal/api/claude_code_handlers_test.go`:
  - `TestHeartbeatStoreAndRetrieve`
  - `TestPendingActionsFIFO` + ack semantics
  - `TestProbeResultTTL`
  - `TestClaudeCodeRoutesBearerRequired` — 401 without token
  - `TestCORSVSCodeWebview` — `vscode-webview://` origin allowed
  - `TestCORSWebExternal` — `https://evil.com` origin denied
  - `TestClaudeCodeCORSPreflight` — OPTIONS request returns 204 with correct Allow-* headers, BEFORE bearer auth layer — **[REVIEW-16 L-02]**
  - `TestActionDebounce` — two regens within 500ms produce one queued action
  - `TestPatchStatePersistenceRoundtrip` — write state, restart state, assert actions survive with TTL filter — **[REVIEW-16 M-01]**

- [x] T16.3.7 — Document schema in `docs/api/claude-code-endpoints.md` with examples. (FROZEN v1.6.0 contract committed separately as 3a73780 during cross-window bus consolidation; 5 → 8 endpoints as the spec matured.)

- [x] T16.3.GATE: **PASSED 2026-04-22** — `go test ./... -count=1` PASS (14 packages, 0 failures; 19 new patchstate subtests + 15 new claude_code_handlers subtests). `go build ./...` + `go vet ./...` clean. Agent sub-agent fallback code-reviewer (MCP outage during implementation) — 2 HIGH + 3 MEDIUM + 2 LOW all fixed in-cycle. Post-reconnect `mcp__pal__codereview` (gpt-5.1-codex, external expert) surfaced 3 HIGH FROZEN-contract endpoints missing (probe-trigger, plugin-sync, compat-matrix) — implemented in the same cycle with tests. PAL-CR1 `WaitGroup.Go` flagged undefined = false positive (Go 1.25.6 ships this convenience method). `mcp__pal__precommit` (gpt-5.1-codex) APPROVE. Commit `a7521fa`. Zero findings at/above threshold.

### Files

- `internal/api/server.go` (modify: add route group)
- `internal/api/claude_code_handlers.go` (new)
- `internal/api/claude_code_handlers_test.go` (new)
- `internal/patchstate/state.go` (new)
- `internal/patchstate/state_test.go` (new)
- `docs/api/claude-code-endpoints.md` (new)

### Rollback

`/api/v1/claude-code/*` routes are additive. Revert removes the route group + `patchstate` package. Existing API unaffected. CORS policy for other routes unchanged (we add a narrow new policy, don't modify existing).

---

## Phase 16.4 — Webview patch (`apply-mcp-gateway.sh` + `porfiry-mcp.js`)

**Prerequisite:** Phase 16.0 SPIKE PASSED 2026-04-21. See `docs/spikes/2026-04-20-reload-plugins-probe.md` §"Live verification results" + §"Redesign of Phase 16.4 under Alt-E". This phase is rewritten under **Alt-E** — calling the native `session.reconnectMcpServer(name)` method via React Fiber walk. The original `commandRegistry.executeCommand("reload-plugins")` design was abandoned (the command does not exist in Claude Code's webview bundle at any tested version).

**Goal:** Install a JavaScript patch into Claude Code's webview that runs a heartbeat + polls pending actions + triggers `session.reconnectMcpServer("mcp-gateway")` via React Fiber walk (Alt-E pattern). Copy the proven `porfiry-taskbar.js` pattern — the taskbar patch already walks the same Fiber neighborhood to call `session.setModel()`.

### Tasks

- [x] T16.4.1 — Author `installer/patches/apply-mcp-gateway.sh` (bash, POSIX-sh-compatible):
  - Mirror `claude-team-control/patches/apply-taskbar.sh` structure (lines 1-97).
  - Locate `~/.vscode/extensions/anthropic.claude-code-*/webview/index.js` via semantic-version sort.
  - Backup to `index.js.bak` ONCE (preserve first clean backup).
  - Idempotent: detect `"MCP Gateway Patch v"` marker, reapply cleanly.
  - Substitute `${GATEWAY_URL}` and `${GATEWAY_AUTH_TOKEN}` placeholders at patch time via `awk` (POSIX) — match the `__ORCHESTRATOR_REST_PORT__` pattern in taskbar apply script.
  - Reject corrupted patch file if placeholder survives after substitution (restore backup).
  - `--auto` mode (silent if already patched) for hook invocation.
  - `--uninstall` mode: restore `.bak`, remove marker.

- [x] T16.4.1.a — **[REVIEW-16 L-03]** Lock down patched file permissions. After write:
  - POSIX: `chmod 600 "$INDEX_JS"` (only current user can read the inlined bearer token).
  - Windows (in .ps1 counterpart): `icacls` grants current-user-only ALLOW, deny-by-default (mirror the DACL pattern from `internal/auth/token_perms_windows.go`).
  - Integration test asserts post-apply file mode = 0600 on Unix; DACL shape on Windows.
  - Document in README §"Security considerations for the webview patch": inlined token is at same trust boundary as `~/.mcp-gateway/auth.token`.

- [x] T16.4.2 — Author `installer/patches/apply-mcp-gateway.ps1` (PowerShell variant for Windows without Git Bash). Same semantics, different syntax.

- [x] T16.4.3 — Author `installer/patches/porfiry-mcp.js` (~200 lines). **Alt-E structure.** Key parts:
  - React Fiber walk — Alt-E target: find object exposing `reconnectMcpServer` (method-valued) starting from `[class*="inputContainer_"]`, walking `.return` up to depth 80. Empirically reaches at depth=2 on Claude Code 2.1.114 (live-verified 2026-04-21, see spike report). Lookup order within each fiber's `memoizedProps`: (a) `p.session?.reconnectMcpServer`, (b) `p.actions?.reconnectMcpServer`, (c) any own prop `p[k]` whose value exposes `reconnectMcpServer`. First match wins; store reference as `mcpSession`.
  - Heartbeat every 60s via `fetch(GATEWAY_URL + "/api/v1/claude-code/patch-heartbeat", {method:"POST", headers:{Authorization: "Bearer " + TOKEN, "Content-Type": "application/json"}, body: JSON.stringify(heartbeat)})`.
  - Poll `/api/v1/claude-code/pending-actions` every 2s.
  - On action `{type:"reconnect", serverName}` (default `serverName="mcp-gateway"` when absent) OR `{type:"probe-reconnect", serverName}` (dashboard probe from T16.5.6, typically `serverName="__probe_nonexistent_<nonce>"`): treat both action types through the **same code path** — `type` is metadata-only for ack interpretation. If `mcpSession?.reconnectMcpServer` exists, `await mcpSession.reconnectMcpServer(serverName).catch((err) => { lastError = err?.message; })`. Fire-and-forget from the patch's perspective; action is acked regardless of server-side outcome — ack body carries `{ok: <resolved/rejected>, error_message: lastError, action_type: type, latency_ms}` so the dashboard can distinguish "real reconnect succeeded" from "probe rejection is actually GREEN". The gateway's heartbeat will notice if actions pile up. **[P4-01 fix]**
  - **Active-tool-call suppression:** same DOM-spinner check as taskbar's `enforceLock` — if `[class*="spinnerRow_"]` is visible, postpone reconnect by 1s and retry; cap total postponement at 10s, then fire anyway.
  - **Debounce window: 10 seconds.** Rationale: observed reconnect latency ~5.4s on live probe; coalescing to `max(500ms, 2 × observed_latency)` gives ~10s headroom. Multiple pending `reconnect` actions for the same `serverName` within the window coalesce to ONE call (drop earlier, keep latest, ack both). Different `serverName` values do NOT coalesce — each is queued independently. **[P4-03]** Window semantics — **fixed-from-first with starvation cap**: window arms on the FIRST action of a serverName cohort and does NOT reset on subsequent actions within that window (prevents infinite starvation under continuous mutations). Hard cap: if the debounce window accumulates `DEBOUNCE_FORCE_FIRE_COUNT=10` coalesced actions for the same serverName while in `ready` state, FORCE-FIRE immediately (coalesced to the latest action) — cap reached = "operator is clearly in bulk-edit mode, delay no longer helps". **[SP4-L1]** Clarification: this 10-count cap applies ONLY to the debounce path (state=ready); it is SEPARATE from the `awaitingDiscovery` FIFO (state=lost/discovering), which has its own bound `AWAITING_DISCOVERY_QUEUE_MAX=16` with drop-oldest overflow policy. The two bounds operate on two distinct code paths. After fire, clear the window and re-arm on next action. Explicitly documented invariant: any action enqueued is guaranteed to be either acked within `DEBOUNCE_WINDOW_MS + observed_reconnect_latency_ms_p95` OR force-fired at the 10-count cap OR transferred to `awaitingDiscovery` on state transition (see next bullet).
  - **[SP4-M1] Debounce-timer state-check at fire time:** when the debounce `setTimeout` callback fires, FIRST check `mcpSessionState`. If state is `ready`: proceed to invoke `reconnectMcpServer` (singleflight guards re-entrancy). If state is `lost` OR `discovering`: cancel the timer-result path, enqueue the coalesced (latest) action into `awaitingDiscovery` FIFO (subject to `AWAITING_DISCOVERY_QUEUE_MAX` bound — drop-oldest if full), and DO NOT call `reconnectMcpServer`. The queued action fires after `discovering → ready`. This preserves the ack-guarantee under the `ready → lost` mid-window scenario that the previous spec left silent (would have swallowed the action via `.catch(()=>{})`).
  - On each action success, POST `/pending-actions/{id}/ack` with `{ok:true, latency_ms}`. On rejection, ack with `{ok:false, error_message}`.
  - **Resilience (MutationObserver pattern — copied from `porfiry-taskbar.js:86-92`):** watch `document.body` for child changes; if the current `rootRef` element is replaced (React hot reload, panel remount), invalidate `mcpSession` reference and re-run fiber walk on next DOM event. Initial discovery retry: 2s then 8s; after that, failure reported via heartbeat but walk continues on every DOM mutation.
  - **Silent-on-error:** all `fetch` calls and `reconnectMcpServer` invocations wrapped in `.catch(() => {})` to avoid crashing the webview.
  - **Heartbeat payload:** `{patch_version, cc_version, vscode_version, fiber_ok: !!mcpSession, mcp_method_ok: typeof mcpSession?.reconnectMcpServer === "function", mcp_method_fiber_depth: <measured during walk>, last_reconnect_latency_ms, last_reconnect_ok, last_reconnect_error, pending_actions_inflight: <count of actions received but not yet acked>, fiber_walk_retry_count: <since last successful discovery>, session_id: getOrCreateSessionId(), ts: Date.now()}`. Fields `registry_ok` and `reload_command_exists` from the prior design are removed. **[P4-lead-L1 + SP4-M2]** `last_reconnect_error` is SCRUBBED before emission: truncate to 256 chars, strip filesystem paths via the broadened regex covering Windows (drive letters + UNC), Unix conventional roots, and container/CI roots: `/[A-Za-z]:\\[^"'\s]+|\\\\[^"'\s\\]+\\[^"'\s]+|\/(?:home|Users|tmp|var|etc|opt|usr|app|workspace|System|Library|srv|proc|dev|mnt|root)[\/\s"'].*?(?=["'\s]|$)/g` (replace with `<path>`), strip stack-trace frames (any line matching `^\s*at\s+` — keep only the first line of the error message). Prevents leaking user home dirs, container-working-directory, macOS system paths, Claude Code install paths (`/opt/claude-code/`), UNC network paths, and stack-trace PII through the gateway's heartbeat log pipeline. **T16.4.6 scrub-test coverage (EXPANDED):** (a) `"ENOENT: /Users/alice/.mcp-gateway/auth.token\n  at Reconnect (/opt/claude-code/webview/index.js:12345)"` — classic Unix home + stack frame; (b) `"ENOENT: /workspace/.mcp-gateway/auth.token"` — container CI; (c) `"ENOENT: /opt/claude-code/cache/token"` — Claude Code install path in first line (not stack frame); (d) `"Error: \\\\corp-server\\share\\token.txt"` — Windows UNC; (e) `"ENOENT: /System/Library/PrivateFrameworks/XYZ"` — macOS system; (f) `"ENOENT: C:\\Users\\alice\\AppData\\Local\\mcp-gateway\\auth.token"` — Windows user profile. For each, assert emitted `last_reconnect_error` contains `<path>` placeholders and NO original path substrings.
  - **Explicit discovery state machine:** `mcpSessionState ∈ {unknown, discovering, ready, lost}`. Transitions: `unknown → discovering` on first DOM ready / MutationObserver tick; `discovering → ready` on successful fiber walk; `ready → lost` on root remount / `reconnectMcpServer` no longer typeof function; `lost → discovering` on next DOM mutation. Heartbeat reports current state in `mcp_session_state` field. **[P4-02]** When `ready → lost` fires WHILE a reconnect is in-flight: (a) the in-flight Promise is allowed to settle naturally (it was created against the old session reference — if the underlying connection is still valid on the backend side it succeeds; otherwise it rejects). (b) The original caller + ALL singleflight attachees receive the in-flight's eventual result (success OR rejection). (c) Any NEW action arriving during `lost` or `discovering` is QUEUED (not dropped) in an `awaitingDiscovery` FIFO, bounded at 16 entries (overflow drops OLDEST with ack `{ok:false, error_message:"awaiting-discovery-overflow"}`); fires in order after `discovering → ready`. (d) Heartbeat during queued period sets `pending_actions_inflight` to queue length. The old `mcpSession` reference is released on `lost` to avoid closure leaks.
  - **Jitter on timers** (prevents storm-on-reload per PAL review). **[P4-04]** Two independent mechanisms:
    - **Per-tick jitter (desync noise):** each heartbeat + each poll redraws a NEW random offset. `heartbeat_interval_actual = HEARTBEAT_INTERVAL_MS + Math.random() * HEARTBEAT_JITTER_MAX_MS`; `poll_interval_actual = POLL_INTERVAL_MS + Math.random() * POLL_JITTER_MAX_MS`. Uniform distribution per interval. Matches T16.4.6 jitter-test expectation of uniform distribution across 100 intervals.
    - **Once-at-load initial skew (cross-window thundering-herd mitigation):** `INITIAL_SKEW_MS = Math.random() * INITIAL_SKEW_MAX_MS` (`INITIAL_SKEW_MAX_MS=30000`) drawn ONCE at patch startup, persisted in `localStorage['porfiry-mcp-initial-skew']` so it survives the first reload (but re-rolls on subsequent reloads where the key has expired — 10min TTL in the localStorage entry). First heartbeat fires at `INITIAL_SKEW_MS` offset from load. Prevents all concurrent VSCode windows from heartbeating at the same instant after a bulk VSCode restart (e.g. after extension auto-update).
  - **Singleflight on reconnect:** if a `reconnectMcpServer(serverName)` call is in-flight and another pending-action for the same `serverName` arrives, DO NOT start a second call — attach to the in-flight promise. Ack the second action with the in-flight's eventual result. Prevents overlapping reconnects from bulk backend operations within the 5.4s latency window.
  - **Thresholds as named constants** (config-visible, testable, top of file in a single `const CONFIG = {...}` object): `DEBOUNCE_WINDOW_MS=10000`, `DEBOUNCE_FORCE_FIRE_COUNT=10` (P4-03 starvation cap), `ACTIVE_TOOL_POSTPONE_CAP_MS=10000`, `HEARTBEAT_INTERVAL_MS=60000`, `HEARTBEAT_JITTER_MAX_MS=5000`, `POLL_INTERVAL_MS=2000`, `POLL_JITTER_MAX_MS=500`, `INITIAL_SKEW_MAX_MS=30000` (P4-04 cross-window desync), `INITIAL_SKEW_STORAGE_TTL_MS=600000`, `AWAITING_DISCOVERY_QUEUE_MAX=16` (P4-02 queue bound), `LATENCY_WARN_MS=30000` (drives dashboard mode L — threshold rationale: ~5.5× observed p50 to absorb heavy-tail; reconsider once post-ship p95 telemetry lands per T16.4.7), `CONSECUTIVE_ERRORS_FAIL_THRESHOLD=3` (drives dashboard mode M), `MODE_M_RESET_ON_SUCCESS=true` (P4-05 — one `last_reconnect_ok=true` resets the consecutive-errors counter; idle heartbeats with null `last_reconnect_ok` leave counter unchanged), `MODE_D_MIN_RETRY_COUNT=5` + `MODE_D_MIN_CONSECUTIVE_HEARTBEATS=3` (P4-09 — mode D only fires after both thresholds met, preventing immediate RED on fresh window with `/mcp` panel unmounted). **Config override via heartbeat response** (P4-07 mitigation option b): gateway's `POST /patch-heartbeat` response MAY include `{config_override: {LATENCY_WARN_MS: <ms>, DEBOUNCE_WINDOW_MS: <ms>, CONSECUTIVE_ERRORS_FAIL_THRESHOLD: <n>}}` which the patch merges into `CONFIG` at runtime. Allows post-ship recalibration without re-patching webviews (e.g. lowering LATENCY_WARN_MS once telemetry shows p95 ≪ 30s, or raising DEBOUNCE_WINDOW_MS if p95 reconnect latency turns out to be much higher than expected).

- [x] T16.4.4 — Auth token injection. Challenge: webview patch cannot read `~/.mcp-gateway/auth.token` directly (sandbox). Options evaluated:
  - **A (chosen)**: `apply-mcp-gateway.sh` substitutes `${GATEWAY_AUTH_TOKEN}` at patch-install time from file contents. Patch ships token inline. Token rotation → re-run apply script.
  - **B (rejected)**: dashboard posts token into webview via `localStorage` — cross-origin restrictions in VSCode webviews make this unreliable.
  - Document rotation procedure in README.
  - **[SP4-cross] Shell-injection hardening** — token substitution MUST NOT use shell interpolation (no `sed -i "s/PLACEHOLDER/$TOKEN/"` with unquoted expansion). Required approach: read token bytes, scan for any character outside `[A-Za-z0-9_\-\.]` and REFUSE substitution if found (token in repo is always base64url — stricter than typical base64 — so any `/`, `=`, newline, or space is a signal of tampering); write replacement via Python-style byte-safe tools (e.g. `awk` with `-v` variable passing, NOT shell `$TOKEN` inline) exactly mirroring the proven `__ORCHESTRATOR_REST_PORT__` pattern in `claude-team-control/patches/apply-taskbar.sh` — reject the token + abort if any metachar detected (`|`, `&`, `;`, `$`, `` ` ``, `\`, quote chars, newline). Integration test: feed a poisoned token file containing `malicious";cat /etc/passwd;"` and assert apply script aborts with non-zero exit + preserves `.bak` intact. `.ps1` variant: same validation using `-match '^[A-Za-z0-9_\-\.]+$'` as guard; replacement via `[System.IO.File]::WriteAllBytes` (no `cmd.exe` interpolation paths).

- [ ] T16.4.5 — **[DEFER → Phase 16.5]** VSCode extension activation hook — edits `vscode/**` which is out-of-scope for pipeline `feature-b8f2decf` (Phase 16.4 webview-patch build-out). Consolidates cleanly with the dashboard panel work in Phase 16.5. Extension checks on activate whether patch marker exists in index.js (extension knows path from its own installation dir). If stale (extension version changed, patch marker absent), silently run patch installer. **[REVIEW-16 M-04]** Explicit platform dispatch (Node.js):
  ```typescript
  const script = process.platform === "win32"
    ? ["powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", "apply-mcp-gateway.ps1", "--auto"]
    : ["/bin/sh", "apply-mcp-gateway.sh", "--auto"];
  const result = await spawn(script[0], script.slice(1), {cwd: patchDir, timeout: 10_000});
  if (result.status !== 0) {
    // Fail loud: write to dashboard status "Patch auto-apply failed: <stderr>"
    setClaudeCodeStatus("patch_autorepply_failed", stderr);
  }
  ```
  On Windows without PowerShell (old systems or stripped-down images), detect and show actionable error. Closes R-7 (stale patch after auto-update) with explicit platform handling.

- [x] T16.4.6 — Add `installer/patches/porfiry-mcp.test.mjs` — node-based test harness mirroring `porfiry-taskbar.test.mjs`:
  - Mock DOM + fiber tree with nested `memoizedProps.session.reconnectMcpServer` on an ancestor; assert fiber walk resolves `mcpSession` and records `mcp_method_fiber_depth`.
  - Mock `mcpSession.reconnectMcpServer` as a jest-style spy returning `Promise.resolve({type:"reconnect_mcp_server_response"})`; assert called once with `"mcp-gateway"` after `{type:"reconnect"}` pending-action arrives.
  - **Debounce test:** three pending-actions for `"mcp-gateway"` within 10s → exactly ONE `reconnectMcpServer` call (the latest), three acks.
  - **Independent-server test:** two pending-actions `{serverName:"a"}` and `{serverName:"b"}` within 10s → TWO calls (one per server), both acked.
  - **Active-tool-call suppression test:** simulate visible `[class*="spinnerRow_"]` for 3s; assert reconnect is postponed then fires at ~3s mark.
  - **Failed fiber walk test:** mock returns no `reconnectMcpServer`-bearing fiber; assert heartbeat payload contains `fiber_ok:false, mcp_method_ok:false, mcp_session_state:"discovering"`; assert no reconnect attempted.
  - **Heartbeat shape test:** assert payload keys match new schema exactly — old `registry_ok` / `reload_command_exists` fields MUST NOT appear; assert `pending_actions_inflight`, `fiber_walk_retry_count`, `mcp_session_state` ARE present.
  - **Flapping test** (storm-regression): enqueue 10 alternating good/error pending-actions at 200ms intervals (total 2s); assert total reconnect calls ≤ 2 (first kicks off in-flight, singleflight suppresses rest within window), all 10 actions acked.
  - **Singleflight test:** start reconnectMcpServer("mcp-gateway") (mock returns 3s Promise); during that 3s, enqueue 5 more pending-actions for the SAME serverName; assert exactly ONE actual `reconnectMcpServer` invocation, all 6 actions acked with the single result.
  - **State-machine test:** force root-remount (simulate MutationObserver fires with new `#root` node); assert state transitions `ready → lost → discovering → ready`; assert reconnect calls during `lost`/`discovering` are queued (not dropped) and fire after re-discovery.
  - **Jitter test:** mock `Math.random`; assert heartbeat actually fires within `[HEARTBEAT_INTERVAL_MS, HEARTBEAT_INTERVAL_MS + HEARTBEAT_JITTER_MAX_MS]` across 100 simulated intervals, distribution uniform (χ² test or visual). Additionally assert each interval draws a FRESH random value (not once-at-startup).
  - **[P4-06] Probe-reconnect patch-side handler test:** enqueue `{type:"probe-reconnect", serverName:"__probe_nonexistent_abc123"}`; assert `mcpSession.reconnectMcpServer("__probe_nonexistent_abc123")` called; assert ack body includes `action_type:"probe-reconnect"`, `ok:false`, `error_message:"Server not found: __probe_nonexistent_abc123"`, `latency_ms:<n>`. Distinct from `{type:"reconnect"}` test — demonstrates both types route through the same code path with ack metadata preserved.
  - **[P4-02] Remount-during-inflight test:** mock `reconnectMcpServer("mcp-gateway")` to return a 3s unresolved Promise; mid-call (at t=1s) fire MutationObserver with a fresh `#root` node; assert state transitions `ready → lost`; advance to t=3.5s with mock resolving; assert original action acked with the settled result; assert any NEW action queued between t=1s and `discovering → ready` is held in `awaitingDiscovery` (length reported in heartbeat `pending_actions_inflight`), not dropped; after re-discovery, queued action fires with the new session.
  - **[P4-03] Debounce starvation-cap test:** enqueue 12 actions for serverName="mcp-gateway" at 500ms intervals (total 6s, well within 10s window); assert that at the 10th action the force-fire triggers regardless of window-elapsed (<10s); assert only 1 actual `reconnectMcpServer` call (coalesced to latest); all 12 acks carry the coalesced result.
  - **[P4-04] Initial-skew persistence test:** simulate patch load → assert `INITIAL_SKEW_MS` drawn + stored to `localStorage['porfiry-mcp-initial-skew']` with TTL stamp; simulate reload within TTL → assert same skew reused; simulate reload after TTL expiry → assert fresh skew drawn.
  - **[SP4-M1] Debounce-fires-during-lost test:** arm debounce window with 3 actions at t=0,1,2s; at t=5s trigger `ready→lost` (simulate root remount); at t=10s debounce callback fires; assert callback detects `state=lost` and transfers coalesced (latest) action to `awaitingDiscovery` FIFO; assert NO `reconnectMcpServer` call made against null mcpSession; after re-discovery at t=15s, assert queued action fires exactly once with all 3 original actions acked using the resolved result.
  - **[SP4-M2] Error-scrub regex coverage (expanded):** assert scrub handles 6 input patterns: (a) classic `/Users/alice/...` with `/opt/claude-code/...` in stack frame, (b) `/workspace/...` container path, (c) `/opt/claude-code/...` in first line (not stack), (d) UNC `\\\\corp-server\\share\\...`, (e) macOS `/System/Library/...`, (f) Windows `C:\\Users\\alice\\AppData\\...`. For each: emitted `last_reconnect_error` contains `<path>` placeholders; verify NO substring of any original path survives in the output.

- [x] T16.4.7 — Supported-versions table in `configs/supported_claude_code_versions.json`:
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
  Consumed by the patch + dashboard to classify current CC version. Fields `alt_e_verified_versions` + `observed_fiber_depths` track which versions have a live-verified fiber path; dashboard shows YELLOW "unverified version, fiber walk may not locate `reconnectMcpServer`" when running on a version outside this list. **Important:** the single-point p50 latency (5400ms) is based on ONE measurement from one machine — per PAL blind-spot review (2026-04-21). After the patch ships, accumulated heartbeat data should be used to compute actual p50/p95/p99 and recalibrate `LATENCY_WARN_MS` in T16.4.3 if needed.

**[P4-07]** Single-point-data mitigation — BOTH paths in-scope for this phase (not deferred to Phase 17):

  (a) **Pre-ship probe expansion** — architect-audit requirement: before merging `porfiry-mcp.js` (T16.4.3) to main, run the Alt-E live-probe sequence from spike §"Minimal live-test needed to decide Alt-E viability" on:
     - at least 2 different MCP backend servers (not only `pal` — try `context7` and `orchestrator` from the dogfood config);
     - at least 2 different machines (original dev machine + CI runner OR a peer's box);
     - capture reconnect latency per call; compute p50/p95/p99 across the ≥4 measurement points;
     - persist the raw measurements in `docs/spikes/2026-04-20-reload-plugins-probe.md` §"Additional measurements" table;
     - update `observed_reconnect_latency_ms_p50` in this file + ADD `observed_reconnect_latency_ms_p95` field;
     - if observed p95 > `DEBOUNCE_WINDOW_MS * 0.8` (8000ms), RAISE `DEBOUNCE_WINDOW_MS` to `max(10000, p95 * 1.5)` BEFORE merge (not after).

  (b) **Post-ship runtime config override** — gateway's `/api/v1/claude-code/patch-heartbeat` RESPONSE body MAY include `{config_override: {LATENCY_WARN_MS:<n>, DEBOUNCE_WINDOW_MS:<n>, CONSECUTIVE_ERRORS_FAIL_THRESHOLD:<n>}}` which the patch merges into its in-memory `CONFIG` object (runtime-only; not persisted). Allows maintainers to recalibrate thresholds from the gateway side (e.g. via a new `/api/v1/admin/patch-config` endpoint or a field in config.json) without re-releasing the VSIX / re-applying patches on every user machine. Full spec + CORS + integration test added to T16.3 scope (see T16.3.1 amendment below). **[SP4-L2]** Override values MUST fall within a hard-bounded validator range on the patch side (defensive, tightened from permissive lows to prevent DoS-by-misconfiguration under a compromised gateway token scenario — R-5): `LATENCY_WARN_MS ∈ [5000, 300000]` (min raised from 1000 — threshold below p50=5400ms is counterproductive), `DEBOUNCE_WINDOW_MS ∈ [2000, 60000]` (min raised from 500 — window below reconnect latency saturates the reconnect API and interrupts active Claude turns per R-3), `CONSECUTIVE_ERRORS_FAIL_THRESHOLD ∈ [2, 20]` (min raised from 1 — single-transient-error = permanent RED is user-hostile). Out-of-range values logged to dashboard `[Copy diagnostics]` + ignored (keep CONFIG default). Dashboard SHOULD also show an advisory banner "config_override active: LATENCY_WARN_MS=<n> (pushed from gateway)" whenever any override is in effect, so an operator seeing unusual UX can trace it back.

- [x] T16.4.GATE: **PASSED 2026-04-22** — node:test PASS (24/24 tests, 329ms) + `node --check installer/patches/porfiry-mcp.js` + `bash -n installer/patches/apply-mcp-gateway.sh` + `powershell.exe Parser::ParseFile installer/patches/apply-mcp-gateway.ps1` (zero parse errors) + JSON schema byte-identical to `/compat-matrix` (9 keys, 0 missing, 0 extra). **shellcheck**: not installed on build host → SKIP (CI must add). **PAL codereview gpt-5.1-codex**: 5 findings (1 HIGH + 2 MEDIUM + 2 LOW) — all 5 FIXED IN-CYCLE at code-reviewer step. **PAL thinkdeep gpt-5.2-pro** via security-lead: PASS with 6 findings (2 HIGH + 2 MEDIUM + 2 LOW) — all 6 FIXED IN-CYCLE. PAL CV-gate thinkdeep verdict: PASS (0 findings, none blocking, 75.6s). Spike T16.0 sign-off (Alt-E at depth=2 on CC 2.1.114) still valid. Pipeline `feature-b8f2decf` — 9 steps completed. **Total 11 findings across code + security, all fixed in-cycle with regression tests (added 3 tests: CR-16.4-01 regression [probe-result wiring], CR-16.4-01 negative [reconnect does NOT post probe], CR-16.4-03 automated [tool-call suppression]).**

### Files

- `installer/patches/apply-mcp-gateway.sh` (new)
- `installer/patches/apply-mcp-gateway.ps1` (new)
- `installer/patches/porfiry-mcp.js` (new; Alt-E structure)
- `installer/patches/porfiry-mcp.test.mjs` (new)
- `configs/supported_claude_code_versions.json` (new)

### Rollback

Run `apply-mcp-gateway.sh --uninstall` to restore `index.js.bak` and remove the patch. No persistent state outside `~/.vscode/extensions/anthropic.claude-code-*/webview/`. On full revert, also remove activation-hook call in the extension. If Alt-E fiber walk proves unreliable in practice (e.g. on a future CC version), fallback is the manual path documented in 16.9 (user-driven `/mcp` panel "Reconnect" action) — gateway still works, just without auto-reload.

### Architect Review (pipeline feature-b8f2decf, 2026-04-22)

**Verdict: design is complete and internally consistent. Zero gaps found. Ready for dev-lead handoff.**

Traceability audit against the frozen API contract (`docs/api/claude-code-endpoints.md` v1.6.0):

- All 15 T16.4.6 test cases trace to code paths in T16.4.3: fiber walk → depth test, singleflight → flapping/singleflight tests, state machine → remount-during-inflight + debounce-fires-during-lost, scrub regex → 6-input scrub-test. No orphan tests, no orphan code paths.
- CONFIG constants in T16.4.3 cover every threshold referenced elsewhere: `DEBOUNCE_WINDOW_MS`, `DEBOUNCE_FORCE_FIRE_COUNT=10` (ack-guarantee invariant), `AWAITING_DISCOVERY_QUEUE_MAX=16` (P4-02 bound), `INITIAL_SKEW_MAX_MS` (P4-04), `LATENCY_WARN_MS` + `CONSECUTIVE_ERRORS_FAIL_THRESHOLD` + `MODE_D_MIN_*` (all drive dashboard mode L/M/D). Override ranges in §T16.4.7(b) SP4-L2 match the frozen `config_override` bounds table in the API contract (lines 122-126).
- `configs/supported_claude_code_versions.json` shape (T16.4.7) is byte-identical to `/compat-matrix` response schema (API contract lines 349-363) — `min`, `max_tested`, `known_broken`, `last_verified`, `alt_e_verified_versions`, `observed_fiber_depths`, `max_accepted_fiber_depth`, `observed_reconnect_latency_ms_p50`, `observed_reconnect_latency_note`. T16.6.5 consumer reads verbatim.
- Scrub regex in T16.4.3 covers all 6 inputs in T16.4.6 SP4-M2: Unix home + stack, `/workspace/` container, `/opt/claude-code/` in first line, UNC `\…`, macOS `/System/Library/`, Windows `C:\Users\…\AppData\`. Stack-frame strip (`^\s*at\s+`) is orthogonal to path scrub — both apply.
- Debounce semantics (fixed-from-first window + 10-count force-fire cap) are consistent with the ack-guarantee invariant. The SP4-M1 state-check at fire-time closes the `ready→lost` mid-window hole: coalesced action transfers to `awaitingDiscovery` FIFO rather than calling `reconnectMcpServer` on a null session. Debounce cap (10) and discovery queue bound (16) operate on disjoint code paths — no cross-contamination.

**Windows `.ps1` DACL readiness (T16.4.1.a):** spec delegates to `icacls` mirroring `internal/auth/token_perms_windows.go`. That Go file uses `SetNamedSecurityInfo` with SDDL `D:P(A;;FA;;;<SID>)` (Protected DACL, one ALLOW ACE for current user). The `.ps1` equivalent is `icacls "$INDEX_JS" /inheritance:r /grant:r "${env:USERNAME}:(F)"` — PLAN does not spell this out, but the Go reference + test assertions (exactly one ACE, Protected, current-user ALLOW) give dev-lead enough signal. Not a blocking gap; flag for dev-lead to pin the exact `icacls` invocation during implementation and include it in the integration test from T16.4.1.a.

**Reference-pattern note (advisory, not a gap):** `claude-team-control/patches/` contains no `apply-taskbar.ps1`. The `.sh` script is the sole reference. Dev-lead will derive PowerShell semantics from (a) `.sh` control flow, (b) `token_perms_windows.go` for DACL, (c) standard PowerShell idioms for extension discovery (`Get-ChildItem` + `Sort-Object` with `[version]` cast for semver). Token-validator regex `'^[A-Za-z0-9_\-\.]+$'` and byte-safe write via `[System.IO.File]::WriteAllBytes` are already specified in T16.4.4 SP4-cross.

**Hand-off notes for dev-lead (task ordering):**
1. **Sequential path recommended** — not fully parallel — because the JS patch is authored with inline placeholders that both scripts must substitute. Draft the JS first so the script authors know exactly which placeholders to wire: `${GATEWAY_URL}`, `${GATEWAY_AUTH_TOKEN}`, `${PATCH_VERSION}` (derived from T16.4.7 shape).
2. Order: **(a) `porfiry-mcp.js`** (Alt-E fiber walk + state machine + CONFIG — ~200 lines, mirror `porfiry-taskbar.js:70-98` MutationObserver pattern) → **(b) `apply-mcp-gateway.sh`** (mirror `apply-taskbar.sh` structure + add token validator per SP4-cross + add `chmod 600`) → **(c) `apply-mcp-gateway.ps1`** (PowerShell parity + `icacls` DACL) → **(d) `porfiry-mcp.test.mjs`** (node:test harness; mirror `porfiry-taskbar.test.mjs` `createMockEnv` pattern; 15 test cases) → **(e) `configs/supported_claude_code_versions.json`** (static seed values from spike).
3. Steps (a) + (e) can run in parallel with (b)+(c)+(d) once (a) placeholders are frozen — JSON seed has no code dependency.
4. `T16.4.5` (VSCode extension activation hook) is listed under §16.4 but edits `vscode/**` which belongs to Phase 16.5's scope boundary. Suggest dev-lead flag this for cross-phase coordination; implementation lands in the 16.5 dashboard PR, not here. Scope boundary for this pipeline: the 5 files in §16.4 Files list only.

Next agent: **dev-lead** to break 5 files into per-agent work orders for backend-dev.

---

## Phase 16.5 — Dashboard "Claude Code Integration" panel

**Prerequisite:** 16.3 endpoints + 16.4 patch. Alt-E redesign per spike 2026-04-20-reload-plugins-probe.md.

**Goal:** Surface the integration in the VSCode extension with user-friendly UX. Two independent status indicators (Patch + Channel), tri-state (green/yellow/red), specific error message per failure mode. `Channel` status reflects native-method (`reconnectMcpServer`) availability in the fiber tree, not `executeCommand` registry state.

### Tasks

- [x] T16.5.1 — New webview panel `vscode/mcp-gateway-dashboard/src/webview/claude-code-panel.ts`:
  - Section header "Claude Code Integration"
  - `[Activate for Claude Code]` button
  - Plugin status line: `● Installed — mcp-gateway plugin registered` / `● Not installed` / `● Installation failed: <reason>`
  - Divider
  - `[x] Auto-reload plugins` checkbox (label kept for user familiarity; internals now call `reconnectMcpServer`)
  - Two status rows (only shown when checkbox on):
    - `Patch:    ● Installed  v1.6.0` / `● Not installed` / `● Unverified (CC v2.6.1, Alt-E verified up to 2.5.8)` / `● Stale — VSCode reload required`
    - `Channel:  ● Active   last heartbeat 12s ago  (fiber depth 2)` / `● Idle  (VSCode unfocused)` / `● Broken — reconnectMcpServer not reachable via fiber walk` / `● Broken — <reason>`
  - Overall status banner: green "✓ Auto-reload is working" / yellow "⏸ Claude Code idle" / red "✗ <specific reason + action>"
  - Buttons: `[Probe reconnect]` + `[Copy diagnostics]` (replaces `[Test now]` — semantics updated under Alt-E, see T16.5.6).

- [x] T16.5.2 — `[Activate for Claude Code]` handler (partial: REST `/plugin-sync` wired; `claude plugin list --json` + auth-token prompt UX deferred to Phase 16.8 CLI which is the authoritative activation flow):
  - Check `claude plugin list --json` for `mcp-gateway` presence.
  - If missing, run `claude plugin marketplace add <repo>/installer/marketplace.json && claude plugin install mcp-gateway@mcp-gateway-local` — prompt user for auth_token (read from `~/.mcp-gateway/auth.token` if exists, show masked preview).
  - Regenerate `.mcp.json` via REST `POST /api/v1/claude-code/plugin-sync` (new endpoint — thin wrapper around 16.2.3 regen).
  - Report success/failure in UI with actionable next step.

- [x] T16.5.3 — Auto-reload checkbox handler:
  - Off → On: run `apply-mcp-gateway.sh` (Unix) or `apply-mcp-gateway.ps1` (Windows) via VSCode terminal; show progress; prompt "Reload VSCode window now" on success.
  - On → Off: run `apply-mcp-gateway.sh --uninstall`; confirm restore of backup.
  - Persist checkbox state in workspace settings.

- [x] T16.5.4 — Status polling: webview calls `GET /api/v1/claude-code/patch-status` every 10s (lightweight — gateway cache, no CC round-trip). Compose patch status locally (FS check via extension's Node context reading index.js for marker) + channel status from response. Channel status derives from heartbeat fields `fiber_ok` AND `mcp_method_ok` (both must be true for green).

- [x] T16.5.5 — Failure-mode messages (matrix updated for Alt-E):
  - A. Patch file missing → "Click ☑ to install patch"
  - B. VSCode not reloaded after apply → "Reload VSCode: Ctrl+Shift+P → 'Developer: Reload Window'"
  - C. CC version unverified for Alt-E → "Claude Code v{X} not in `alt_e_verified_versions` (last verified {MAX_ALT_E}). Fiber walk may not locate `reconnectMcpServer`. [Report success/failure on GitHub]"
  - D. **Fiber walk failed to locate `reconnectMcpServer` on session object** — **[P4-09]** trigger requires BOTH `fiber_walk_retry_count >= CONFIG.MODE_D_MIN_RETRY_COUNT` (=5) AND `mcp_session_state != "ready"` across `CONFIG.MODE_D_MIN_CONSECUTIVE_HEARTBEATS` (=3) consecutive heartbeats. Prevents false-RED on a fresh VSCode window that hasn't opened the `/mcp` panel yet (walk hasn't had a chance to run). Message: "Claude Code internal API changed or `/mcp` panel never mounted in this window. Open `/mcp` panel to trigger patch discovery, or revert to aggregate-only mode."
  - ~~E. `reload-plugins` command missing~~ **OBSOLETE (Alt-E)** — no longer applicable; Alt-E does not depend on `reload-plugins`.
  - F. CORS blocks gateway → "Gateway unreachable from Claude Code webview. Check `gateway.allowed_origins` setting."
  - G. No plugin installed → "Click [Activate for Claude Code] first."
  - H. Gateway not running → "mcp-gateway daemon not running on port 8765."
  - I. VSCode idle → YELLOW "⏸ Claude Code idle — patch OK"
  - J. Multiple sessions → show per-session list.
  - **K. Token rotation detected** — **[REVIEW-16 L-06 + M-03]** — `mtime(~/.mcp-gateway/auth.token) > mtime(patched-index.js)` → RED "Gateway token rotated since patch install. Inlined token is stale. [Reinstall patch] to pick up new token." Dashboard also offers [Reinstall via mcp-ctl] which runs `mcp-ctl install-claude-code --no-plugin-change` to re-apply patch with current token without touching plugin.
  - **L. Reconnect latency >30s** (NEW, Alt-E) — `last_reconnect_latency_ms > 30000` → YELLOW "Recent `reconnectMcpServer` took {N}s (threshold 30s, baseline ~5s). Gateway may be slow or MCP backend hung. [Open gateway logs] / [Report issue]."
  - **M. Reconnect errors recurring** (NEW, Alt-E) — `CONFIG.CONSECUTIVE_ERRORS_FAIL_THRESHOLD` (=3) consecutive `last_reconnect_ok=false` heartbeats → RED "`reconnectMcpServer` failing: {error}. Check gateway + MCP backend health." **[P4-05]** Reset rule: counter resets to 0 on FIRST `last_reconnect_ok=true` heartbeat (one success clears the alert). Idle heartbeats (null `last_reconnect_ok`, i.e. no reconnect attempted since last heartbeat) do NOT change the counter — they're neutral. Prevents latching RED indefinitely after 3 transient failures followed by a run of successes.

- [x] T16.5.6 — `[Probe reconnect]` handler (Alt-E — replaces `[Test now]` + `__mcp_gateway_probe`):
  - Dashboard `POST /api/v1/claude-code/probe-trigger {nonce}` → gateway enqueues a special action `{type:"probe-reconnect", serverName:"__probe_nonexistent_" + nonce}` → patch sees it, calls `mcpSession.reconnectMcpServer("__probe_nonexistent_" + nonce)` → the call rejects with "Server not found" (verified on live probe 2026-04-21 Step 2: `rejected (expected): Server not found: nonexistent-mcp-<N>`).
  - Patch acks with `{ok:false, error_message:"Server not found: __probe_..."}` — which, paradoxically, is the GREEN success signal for this probe. The rejection path proves (a) the fiber walk succeeded, (b) `reconnectMcpServer` is callable, (c) the round-trip works.
  - Dashboard UI: green "Probe passed — reconnectMcpServer reachable" / red "Probe failed: <unexpected-response>". Timeout 15s → "Timeout — patch not responding (check heartbeat)".
  - No need for `registerCommand` / `__mcp_gateway_probe` — we reuse the real `reconnectMcpServer` method with a sentinel server name.

- [x] T16.5.7 — `[Copy diagnostics]` generates structured report:
  - Environment (OS, VSCode version, CC version, gateway version)
  - Plugin status (installed/location/entries)
  - Patch status (installed/location/version/backup existence)
  - Supported-versions map + classification of current CC version (inc. `alt_e_verified_versions` status)
  - **Alt-E metrics:** `mcp_method_fiber_depth` (last 5 readings), `last_reconnect_latency_ms` (p50/p95 over session), `last_reconnect_ok` count, recent `last_reconnect_error` strings if any
  - Last 5 heartbeats (raw payload)
  - Failure trace if any
  - Report-to URL with issue template link
  - Copied to clipboard via `vscode.env.clipboard.writeText`.

- [x] T16.5.8 — Unit tests `vscode/mcp-gateway-dashboard/src/test/claude-code-panel.test.ts`:
  - State matrix: each failure mode (A/B/C/D/F/G/H/I/J/K/L/M) produces correct banner + action. Mode E is explicitly absent (test asserts the UI never emits an E-class message — safeguard against regression).
  - Checkbox behavior when RED: stays checkable with warning banner (not paternalize).
  - Diagnostics dump includes Alt-E metric fields (`mcp_method_fiber_depth`, `last_reconnect_latency_ms`).
  - Probe-reconnect handler: mock heartbeat `{type:"probe_reconnect_response", ok:false, error_message:"Server not found: __probe_..."}` → assert GREEN "Probe passed".
  - Probe-reconnect unexpected-response: mock any OTHER response (incl. `ok:true`) → assert RED "Probe failed: unexpected response".
  - **[P4-06] Mode L boundary test:** submit heartbeat with `last_reconnect_latency_ms = CONFIG.LATENCY_WARN_MS - 1` → assert no Mode L banner; submit with `last_reconnect_latency_ms = CONFIG.LATENCY_WARN_MS` → assert YELLOW Mode L; submit with `last_reconnect_latency_ms = CONFIG.LATENCY_WARN_MS + 1` → assert YELLOW Mode L; submit with `last_reconnect_latency_ms = null` (idle) → assert Mode L not armed.
  - **[P4-06 + P4-05] Mode M counter-reset test:** feed 3 heartbeats with `last_reconnect_ok=false` → assert RED Mode M at 3rd; feed 1 heartbeat with `last_reconnect_ok=true` → assert Mode M clears; feed 2 more with `last_reconnect_ok=false` → assert NO Mode M (counter was reset, needs 3 new consecutive failures). Additional idle-heartbeat neutrality test: feed 2× `false`, then 1× `null`, then 1× `false` → assert Mode M armed (counter advanced 2→2→3, null neutral).
  - **[P4-06 + P4-09] Mode D threshold test:** feed heartbeat with `fiber_ok=false, fiber_walk_retry_count=1, mcp_session_state="discovering"` → assert no Mode D (fresh window — panel never opened); feed 3 heartbeats with `fiber_walk_retry_count=4, mcp_session_state="lost"` → assert no Mode D (retry count below 5); feed 3 heartbeats with `fiber_walk_retry_count=5, mcp_session_state="lost"` → assert RED Mode D; feed 1 recovery heartbeat with `fiber_ok=true, mcp_method_ok=true, mcp_session_state="ready"` → assert Mode D clears immediately.
  - **[SP4-L2] config_override boundary test:** push `{config_override: {LATENCY_WARN_MS: 4999}}` (below min 5000) → assert value rejected + logged + CONFIG stays at default + advisory banner does NOT appear; push `{config_override: {LATENCY_WARN_MS: 5000}}` (at min) → assert accepted + banner appears; push `{DEBOUNCE_WINDOW_MS: 1999}` (below min 2000) → assert rejected; push `{DEBOUNCE_WINDOW_MS: 2000}` → accepted; push `{CONSECUTIVE_ERRORS_FAIL_THRESHOLD: 1}` → rejected; push `{CONSECUTIVE_ERRORS_FAIL_THRESHOLD: 2}` → accepted. Verifies raised SP4-L2 lower bounds.

- [x] T16.5.9 — Extension `package.json`: register new command `mcpGateway.showClaudeCodeIntegration`, wire to tree view or status bar context menu.

- [x] T16.5.GATE: **PASSED 2026-04-22** — `npm run compile` clean. Scoped mocha `src/test/claude-code-*.test.ts` → 55 passing in 147ms. Broader sample (`claude-code-* + validation + backend-tree-provider + status-bar + daemon` = 194 tests) passes. Full `src/test/**/*.test.ts` excluding pre-existing slow tests (`gateway-client`, `log-viewer`, `commands` — out-of-scope per MEMORY.md "31 pre-existing GatewayClient+LogViewer failures tracked as v16-4") → 492 passing in 2s. `npm run deploy` + manual VSCode smoke deferred to Phase 16.9 dogfood step. PAL codereview deferred to final /finish sweep since parallel feature-b8f2decf pipeline is still committing related code.

### Files

- `vscode/mcp-gateway-dashboard/src/webview/claude-code-panel.ts` (new)
- `vscode/mcp-gateway-dashboard/src/claude-code/*` (new module: status, patch-installer, diagnostics)
- `vscode/mcp-gateway-dashboard/src/test/claude-code-panel.test.ts` (new)
- `vscode/mcp-gateway-dashboard/src/extension.ts` (modify: register command)
- `vscode/mcp-gateway-dashboard/package.json` (modify: command registration)

### Rollback

Uncheck auto-reload checkbox (runs uninstall). Uninstall plugin via `claude plugin uninstall mcp-gateway`. Revert extension commit. No gateway state affected — dashboard is a consumer only. If Alt-E auto-reload turns out unstable in production, the entire auto-reload track can be dropped while keeping `[Activate for Claude Code]` + manual-reload docs from 16.9 — no coupling forces removal of the rest of Phase 16.

---

## Phase 16.6 — `gateway.invoke` universal fallback tool + supported-versions map

**Goal:** Ship a stable tool that works even when tools/list refresh fails entirely. User asks Claude "use gateway.invoke to call context7 query-docs with args X" and it works regardless of cache state.

### Tasks

- [ ] T16.6.1 — Register built-in tool `gateway.invoke` on `aggregateServer` only (NOT per-backend). Schema:
  ```json
  {
    "name": "gateway.invoke",
    "description": "[gateway] Universal fallback invoker. Call any backend tool by name. Use when specific tools aren't yet visible (e.g. recently added).",
    "inputSchema": {
      "type": "object",
      "required": ["backend", "tool"],
      "properties": {
        "backend": {"type": "string", "description": "Backend server name"},
        "tool": {"type": "string", "description": "Tool name on that backend"},
        "args": {"type": "object", "description": "Arguments for the tool"}
      }
    }
  }
  ```
  Handler: validate backend exists, validate tool exists on backend, call `router.Call(namespaced, args)`. Forward result.

- [ ] T16.6.2 — Register `gateway.list_servers` and `gateway.list_tools(server?)` (meta-tools from Option C in design):
  - `gateway.list_servers` → returns array of backends with `{name, status, transport, tool_count, health, uptime_seconds}`.
  - `gateway.list_tools` with optional `server` filter → returns tools grouped by backend (name, namespaced, description, inputSchema).

- [ ] T16.6.3 — Gateway `instructions` field on `initialize` response: set to:
  ```
  This gateway aggregates N MCP backends. Tool names are namespaced as
  <backend>__<tool>. Call `gateway.list_servers` to see backend topology.
  Use `gateway.invoke` to call any backend tool when list refresh is stale.
  ```
  Set via `mcp.NewServer(&mcp.Implementation{..., Instructions: ...}, nil)` — verify API in go-sdk (streamable.go / server.go).

- [ ] T16.6.4 — `serverInfo.version` cache-busting (PAL recommendation): compute version as `baseVersion + "+" + shortHash(sortedBackendNames + toolCount)` on each `RebuildTools`. Some clients cache by `(name, version)`; changing version on topology change invites refetch.

- [ ] T16.6.5 — Integration: `supported_claude_code_versions.json` (16.4.7) is now ALSO the source of truth for patch UI. Gateway exposes read-only `GET /api/v1/claude-code/compat-matrix` that returns the JSON — dashboard consumes this rather than bundling its own copy. Single source of truth; updates by maintainers via repo PR.

- [ ] T16.6.6 — Tests:
  - `TestGatewayInvoke_HappyPath` — invokes context7 query-docs via gateway.invoke, result matches direct namespaced call.
  - `TestGatewayInvoke_UnknownBackend` — returns IsError result with clear message.
  - `TestGatewayInvoke_UnknownTool` — same.
  - `TestListServers_IncludesStatus` — running/degraded/stopped backends reported correctly.
  - `TestServerInfoVersionChangesOnTopology` — add backend → version string differs.

- [ ] T16.6.GATE: `go test ./...` PASS + PAL codereview zero errors + PAL thinkdeep on schema-versioning invariants zero errors.

### Files

- `internal/proxy/gateway.go` (modify: register built-in tools + instructions field)
- `internal/proxy/gateway_invoke_test.go` (new)
- `internal/api/claude_code_handlers.go` (modify: add compat-matrix endpoint)
- `configs/supported_claude_code_versions.json` (already created in 16.4)

### Rollback

Revert tool registrations in gateway.go. Built-in tools are additive; revert leaves aggregate and per-backend views intact but drops `gateway.*` namespace. serverInfo.version falls back to plain `g.version`. No data loss.

---

## Phase 16.7 — Integration test end-to-end

**Goal:** Automated proof of the full chain under Alt-E: add backend via REST → plugin .mcp.json regen → patch heartbeat → `reconnect` action enqueued → patch invokes `reconnectMcpServer("mcp-gateway")` → new MCP tool visible via a simulated client.

### Tasks

- [x] T16.7.1 — `internal/api/integration_phase16_test.go` (new, build tag `integration`). Implemented with a simplified flow that reuses the existing `buildMockServer` helper + `lifecycle.Manager` + `plugin.Regenerator` + `patchstate.State` rather than spinning up a live go-sdk MCP client (go-sdk client is covered by the non-integration `server_proxy_test.go`). Three subtests: FullPatchChain (add→regen→action→ack→remove→action), ProbeTriggerRoundTrip (nonce correlation), PluginSyncReturnsStatus (/plugin-sync endpoint shape).

- [x] T16.7.2 — `internal/api/integration_cors_test.go` (NO build tag — runs as part of default `go test ./...` surface). 5 subtests: preflight-allows-vscode-webview, preflight-denies-external-origin, actual-post-echoes-origin, actual-post-external-omits-echo, preflight-before-auth-ordering (REVIEW-16 L-02 regression guard).

- [ ] T16.7.3 — Patch JS side: `installer/patches/porfiry-mcp.integration.test.mjs` — **DEFERRED to Phase 16.4 close-out**. Depends on `installer/patches/porfiry-mcp.js` which belongs to concurrent pipeline `feature-b8f2decf` (still mid-flight). `docs/TESTING-PHASE-16.md` §Tier 4 documents the run command once the file lands.

- [x] T16.7.4 — `docs/TESTING-PHASE-16.md` (new) — all 4 tiers documented with run commands + prerequisites + expected counts. Includes deferred-task note for T16.7.3 and manual VSCode smoke checklist for Phase 16.5.

- [x] T16.7.GATE: **PASSED 2026-04-22** — `go test ./internal/api/... -count=1 -run 'TestIntegration_CORS'` → 5/5 pass; `go test -tags=integration ./internal/api/... -run 'TestIntegration_Phase16'` → 3/3 pass (FullPatchChain 10.7s, ProbeTriggerRoundTrip 7.9s, PluginSyncReturnsStatus 4.8s); full `go test ./... -count=1` → all packages green, 0 regressions. Makefile target `test-integration-phase16` added. PAL thinkdeep deferred to /finish sweep.

### Files

- `internal/api/integration_phase16_test.go` (new, build tag `integration`)
- `internal/api/integration_cors_test.go` (new)
- `installer/patches/porfiry-mcp.integration.test.mjs` (new)
- `docs/TESTING-PHASE-16.md` (new)
- `Makefile` (modify: add `test-integration-phase16` target)

### Rollback

Integration tests are additive. Revert removes test files. No production code affected.

---

## Phase 16.8 — Bootstrap CLI (`mcp-ctl install-claude-code`)

**Goal:** Headless / CI / power-user path to do what the dashboard button does. Required for automation + systems without VSCode extension.

### Tasks

- [ ] T16.8.1 — Add `cmd/mcp-ctl/install_claude_code.go` subcommand. Flags:
  - `--mode aggregate|proxy|both` (default: proxy)
  - `--scope user|workspace` (default: workspace)
  - `--no-patch` (skip webview patch installation)
  - `--dry-run` (print the resulting .mcp.json + what would be installed; no writes)

- [ ] T16.8.2 — Logic:
  1. Verify gateway is running (`GET /api/v1/health`) — refuse if not.
  2. Read `~/.mcp-gateway/auth.token`.
  3. Find Claude Code install (via `claude --version` or well-known locations).
  4. Invoke `claude plugin install mcp-gateway@mcp-gateway-local` (local marketplace added first if missing).
  5. Trigger `POST /api/v1/claude-code/plugin-sync` to regenerate .mcp.json with current backends.
  6. If `--no-patch` absent and on-supported-version, invoke `apply-mcp-gateway.sh --auto`.
  7. Print actionable next-step instructions: "Open Claude Code. If you see `plugin:mcp-gateway:<backend>` entries in /mcp, you're done."

- [ ] T16.8.3 — Failure handling: any step error → rollback prior steps (uninstall plugin if patch fails, etc.). Clear user-facing error message.

- [ ] T16.8.4 — Cross-platform:
  - On Windows: invoke `apply-mcp-gateway.ps1` via `powershell -File`.
  - On Unix: invoke `apply-mcp-gateway.sh` via `/bin/sh`.
  - Path resolution respects `$HOME` (Unix) / `$USERPROFILE` (Windows).

- [ ] T16.8.5 — Tests `cmd/mcp-ctl/install_claude_code_test.go`:
  - Dry-run prints expected plan without side effects.
  - Gateway-not-running refuses with exit code 2.
  - Missing claude CLI → helpful error.
  - Partial failure → rollback.

- [ ] T16.8.6 — **[REVIEW-16 M-03]** Auth-token drift detection + re-registration flow. `mcp-ctl install-claude-code --refresh-token`:
  1. Read current `~/.mcp-gateway/auth.token`.
  2. Query `claude plugin list --json` → find `mcp-gateway` entry → inspect stored `user_config.auth_token` value.
  3. If mismatch: re-invoke plugin install with `--reconfigure` (or equivalent Claude Code CLI path) passing the fresh token.
  4. Re-run `apply-mcp-gateway.sh --auto` so patched index.js receives the new token too.
  5. Exit 0 if sync succeeded; exit 3 with actionable error if drift couldn't be resolved automatically (e.g., keychain ACL blocks).
  Also ships as `--check-only` flag that exits with 0/3 without making changes (for CI + dashboard T16.5.5.K polling).

- [ ] T16.8.GATE: `go test ./cmd/mcp-ctl/...` PASS + PAL codereview zero errors + manual smoke test on each platform.

### Files

- `cmd/mcp-ctl/install_claude_code.go` (new)
- `cmd/mcp-ctl/install_claude_code_test.go` (new)
- `cmd/mcp-ctl/main.go` (modify: register subcommand)
- `README.md` (modify: document subcommand)

### Rollback

Add `mcp-ctl uninstall-claude-code` as the inverse operation. On revert, `mcp-ctl` reverts to its v1.5.0 shape (subcommand removed). User can manually run `claude plugin uninstall mcp-gateway` + `apply-mcp-gateway.sh --uninstall`.

---

## Phase 16.9 — Docs + dogfood + disclaimers

**Goal:** Close the loop. Repo dogfood demonstrates the integration. README has complete "Connecting Claude Code" section. `.claude/commands/*.md` can no longer mislead.

### Tasks

- [ ] T16.9.1 — README new section §"Connecting Claude Code to the Gateway":
  - Two-line install: `mcp-ctl install-claude-code --mode proxy`.
  - What to expect in Claude Code `/mcp` panel (with screenshot of namespaced entries).
  - Auto-reload opt-in explanation + safety/risk notes (patches webview bundled JS).
  - Manual path for users who decline the webview patch: after adding a backend, open Claude Code `/mcp` panel → right-click the `mcp-gateway` entry → **Reconnect**. (Note: Claude Code 2.1.114 does NOT ship a `/reload-plugins` slash command — the per-server Reconnect action in the `/mcp` panel UI is the native primitive and is what the auto-reload patch calls programmatically under the hood via `reconnectMcpServer`.)
  - Uninstall: `mcp-ctl uninstall-claude-code`.

- [ ] T16.9.2 — README new section §"Commands vs MCP servers":
  - `.claude/commands/*.md` are **prompt templates** (slash-command helpers for the user) — NOT MCP server registrations.
  - `claude plugin install mcp-gateway` is the MCP registration path.
  - One-sentence distinction + link to Claude Code plugin docs.

- [ ] T16.9.3 — Modify `vscode/mcp-gateway-dashboard/src/slash-command-generator.ts:296` or wherever the skeleton template is built to prepend an explicit disclaimer:
  ```markdown
  <!-- AUTO-GENERATED by mcp-gateway extension. DO NOT EDIT — will be overwritten. -->
  <!-- NOTE: This is a slash-command prompt template, NOT an MCP server registration. -->
  <!-- MCP servers are registered via the mcp-gateway plugin (see README §Connecting Claude Code). -->
  ```
  Both skeleton + catalog-driven templates get the disclaimer above the MARKER line. Update regression test at `slash-command-generator.test.ts:120-131` to pin the new first-three-lines shape.

- [ ] T16.9.4.a — **[REVIEW-16 L-04]** Before T16.9.4 swap, add CI workflow `dogfood-smoke.yml` that runs on PRs touching `.mcp.json` or `config.json`:
  1. Start `mcp-gateway` daemon in background.
  2. Wait for health (`GET /api/v1/health` returns 200) or timeout 10 s.
  3. `curl /api/v1/servers | jq '[.[] | select(.status=="running")] | length'` → assert ≥ 3 (context7, orchestrator, playwright).
  4. Shut down gracefully.
  Ensures dogfood config doesn't bit-rot silently on future changes to gateway.

- [ ] T16.9.4 — Replace repo `.mcp.json` to dogfood through the gateway:
  ```json
  {
    "mcpServers": {
      "mcp-gateway": {
        "type": "http",
        "url": "http://127.0.0.1:8765/mcp",
        "headers": {
          "Authorization": "Bearer ${MCP_GATEWAY_AUTH_TOKEN}"
        }
      }
    }
  }
  ```
  Gateway's own config.json registers the three MCPs (context7, orchestrator, playwright) as backends. Proof: the project's author now actually uses the gateway they built.

- [ ] T16.9.5 — Author `docs/ADR-0005-claude-code-integration.md`:
  - Title: "Claude Code Integration: Dual-mode gateway + Plugin packaging + Webview patch (Alt-E native reconnect)"
  - Context: 3 findings closed
  - Decision: hybrid approach (aggregate + proxy + plugin + optional patch using native `session.reconnectMcpServer` fiber walk)
  - Rejected alternatives (with reasoning): HTTP reverse proxy (MCP stateful), suppression of aggregate when plugin loaded (disappearing tools UX), SIGHUP wrapper (invasive shell rc edit), socket injection (unofficial), `executeCommand("reload-plugins")` fiber walk (command string does not exist in webview bundle — proven by spike 2026-04-20), `extension.js` dispatcher patch (superseded by Alt-E — native `reconnectMcpServer` already exposed in webview), `set_plugin_enabled` toggle trick (user-visible disable flicker + per-plugin-only granularity).
  - Consequences: patch is still fragile vs Claude Code API changes, but Alt-E uses the SAME primitive as Claude Code's own `/mcp` panel "Reconnect" button — internal API stability is higher than arbitrary internal commands; aggregate fallback mitigates all patch failure modes; supported-versions map tracks `alt_e_verified_versions`.
  - References: Issues #13646, #16143, #18174; PAL consultation rounds (3); spike `docs/spikes/2026-04-20-reload-plugins-probe.md` (three passes documented — final PASS 2026-04-21 on CC 2.1.114).

- [ ] T16.9.6 — CHANGELOG.md entry for v1.6.0:
  - **Added**: Dual-mode gateway (/mcp/{backend} per-backend endpoints), Claude Code Plugin packaging, mcp-ctl install-claude-code, webview patch for native MCP reconnect automation via `session.reconnectMcpServer` fiber walk — Alt-E pattern (opt-in), gateway.invoke universal fallback tool, supported-versions map (incl. Alt-E verified versions).
  - **Security**: CORS policy for vscode-webview:// narrowly scoped to /api/v1/claude-code/* — no broadening of existing API.
  - **Documentation**: README §Connecting Claude Code, §Commands vs MCP servers, ADR-0005.
  - **Breaking**: None (all additions backward-compatible).
  - **Known limitations**: patch requires opt-in + trusts maintainer release; CC version drift mitigated via supported-versions map.

- [ ] T16.9.7 — ROADMAP.md update: mark Phase 16 COMPLETE; add "Phase 17 candidates" section (SIGHUP wrapper, stdio transport reload, plugin marketplace publication).

- [ ] T16.9.GATE: Docs review (doc-writer agent) + PAL codereview on ADR-0005 + markdown lint clean + links-check clean (run `markdown-link-check` if available) + repo dogfood works end-to-end (manual smoke: fresh clone → install → Claude Code sees gateway MCPs).

### Files

- `README.md` (modify)
- `CHANGELOG.md` (modify)
- `docs/ADR-0005-claude-code-integration.md` (new)
- `docs/ROADMAP.md` (modify)
- `vscode/mcp-gateway-dashboard/src/slash-command-generator.ts` (modify: disclaimer)
- `vscode/mcp-gateway-dashboard/src/test/slash-command-generator.test.ts` (modify)
- `.mcp.json` (modify: dogfood through gateway)

### Rollback

Revert each doc + code change. Previous `.mcp.json` backed up via git history. Disclaimer removal is trivial.

---

## Architectural Decisions

**ADR-0005 (will be authored in 16.9.5)** captures:

- **Dual-mode gateway**: aggregate (backward compat, live hot-add) + per-backend proxy (UI parity). SDK-level multiplexing via `getServer(req)` callback, not HTTP reverse proxy.
- **Plugin packaging**: official Claude Code mechanism for namespaced MCP entries in /mcp panel. `.mcp.json` in plugin dir (not inline per Issue #16143).
- **Webview patch (Alt-E)**: opt-in, idempotent, uninstallable. Copies the proven pattern from `claude-team-control/patches/porfiry-taskbar.js`. Walks React Fiber tree to find a session/actions object with the native `reconnectMcpServer(serverName)` method (live-verified at depth=2 on Claude Code 2.1.114 — spike 2026-04-20-reload-plugins-probe.md). On gateway-signaled MCP topology change, calls `reconnectMcpServer("mcp-gateway")` — the same operation Claude Code's own `/mcp` panel triggers when a user clicks "Reconnect". Debounce window 10s (2× observed reconnect latency of ~5.4s) to coalesce bulk backend operations. The original `commandRegistry.executeCommand("reload-plugins")` design was abandoned — the command string does not exist in the webview bundle at any tested version; Alt-E uses a native method call instead of a non-existent command.
- **No suppression of aggregate tools when backend also in plugin**: PAL-validated; prevents "disappearing tools" UX bug during the window between plugin regen and reconnect.
- **Auth token handling**: patch-install-time substitution into fetch headers; never logged; rotation via `apply-mcp-gateway.sh` re-run.
- **gateway.invoke as safety net**: stable universal invoker; works even when tools/list caching completely fails.
- **Supported-versions map as single source of truth**: maintainer-curated, read by both patch heartbeat handler and dashboard UI.

## Dependencies

- go-sdk `v1.4.1`+ (getServer callback, changeAndNotify)
- Claude Code `2.0.0` – `2.5.x` confirmed-supported at phase start; re-verified per release via T16.0 spike pattern.
- Phase 12.A Bearer auth (token at `~/.mcp-gateway/auth.token`)
- Phase 11.9 SlashCommandGenerator (modified in 16.9.3 for disclaimer)
- claude-team-control patches directory (pattern reference, not import)
- Node 20+, Go 1.25+, bash (for patch script on Unix), PowerShell 5.1+ (on Windows)

## Rollback Strategy (plan-wide)

- Sub-phases 16.1 through 16.9 land as independent commits — each with its own rollback block.
- 16.4 + 16.5 (patch + dashboard) are opt-in; uninstall restores the webview `.bak` backup.
- 16.9.4 (dogfood `.mcp.json`) requires one-time re-registration; rollback restores prior direct-MCP config.
- No database migrations, no on-disk state outside `~/.mcp-gateway/`, `~/.claude/plugins/cache/mcp-gateway@*/`, and `~/.vscode/extensions/anthropic.claude-code-*/webview/index.js*`.
- Catastrophic rollback: `mcp-ctl uninstall-claude-code` + git revert of the Phase 16 commits.

## Acceptance Criteria (plan-wide)

- ✅ All 10 sub-phase GATE checkpoints pass (tests + PAL codereview + PAL thinkdeep, zero errors).
- ✅ Repo dogfood: project's own `.mcp.json` points at gateway; author uses their own tool.
- ✅ README has §"Connecting Claude Code" with verified one-liner install.
- ✅ Fresh-clone smoke test: `mcp-ctl install-claude-code --mode proxy` → Claude Code /mcp panel shows `plugin:mcp-gateway:*` entries within 30 seconds.
- ✅ Add backend via REST → tool visible in Claude Code either via aggregate (immediate) or plugin (after patch-triggered reload, target 1-2s).
- ✅ Patch fail → aggregate still works + clear user guidance in dashboard.
- ✅ ADR-0005 peer-reviewed and merged.

## Next Plans

After Phase 16 ships:

- `docs/PLAN-17.md` (v1.7.0 candidate) — SIGHUP+wrapper restart path as alternative to webview patch (for users who distrust webview patches); stdio MCP transport reload support; plugin marketplace publication to anthropic/claude-plugins-official.
- `docs/PLAN-catalogs-v2.md` — catalog-driven plugin generation (auto-suggest plugins for community MCPs).
- `docs/PLAN-observability.md` — Prometheus metrics endpoint, OpenTelemetry tracing for backend calls, per-tool call latency histograms.

Tracking: see ROADMAP.md "Phase 17 candidates" section (added in 16.9.7).
