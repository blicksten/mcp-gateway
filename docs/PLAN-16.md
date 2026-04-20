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
- Webview patch precedent: `claude-team-control/patches/porfiry-taskbar.js` at line 187 already calls `appRegistry.executeCommand("fast")` — the exact mechanism we reuse for `executeCommand("reload-plugins")`.
- go-sdk API verified: `NewStreamableHTTPHandler(getServer func(*http.Request) *Server, ...)` per-request routing confirmed (streamable.go:187-192).

## Scope Assessment

### In scope

1. Gateway dual-mode: existing `/mcp` aggregate endpoint untouched (backward compat), plus new `/mcp/{backend}` per-backend proxy endpoints via `getServer(req)` routing.
2. Claude Code Plugin packaging (`.claude-plugin/plugin.json` + regenerated `.mcp.json`), local marketplace for installation.
3. Webview patch (`apply-mcp-gateway.sh` + `porfiry-mcp.js`) that automates `/reload-plugins` on backend add/remove.
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
| R-1 | Claude Code update renames `commandRegistry` / removes `reload-plugins` command | Medium | High | Version-detect in patch; heartbeat reports `fiber_ok` + `registry_ok` + `cmd_exists`; aggregate mode always works as fallback; supported-versions map flags known-broken. |
| R-2 | Webview CORS blocks `fetch("http://127.0.0.1:8765")` from patched page | Medium | High | Gateway CORS config: `allowed_origins` must include `vscode-webview://` schema; integration test pins this. If blocked, fallback to file-based IPC (write sentinel file in `~/.mcp-gateway/pending-actions/`). |
| R-3 | `/reload-plugins` side effects interrupt active Claude turn | Low | Medium | Debounce 500ms; skip if active tool call in progress (patch reads DOM spinner indicator same as porfiry-taskbar.js `enforceLock`). |
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

- [ ] T16.0.GATE: Maintainer review of spike report. On FAIL, Phase 16 rescopes (see "Rescope on spike failure" below).

### Rollback

Nothing to roll back — this phase has no code changes. Spike report stays in `docs/spikes/` as historical reference.

### Rescope on spike failure

If T16.0.2 fails (reload-plugins doesn't fire, or throws, or has bad side effects):

- Drop phases 16.4 and 16.5 (patch + dashboard panel).
- Keep 16.1 (dual-mode), 16.2 (plugin packaging), 16.3 (REST endpoints — scoped back to plugin heartbeat without patch), 16.6–16.9.
- Dashboard still gets `[Activate for Claude Code]` button (plugin install) but NOT the auto-reload checkbox.
- Manual workflow documented: "After adding an MCP, run `/reload-plugins` in Claude Code chat."
- Phase 17 candidate: SIGHUP+wrapper as alternative restart path.

---

## Phase 16.1 — Gateway dual-mode (aggregate + per-backend proxy)

**Goal:** Add `/mcp/{backend}` per-backend endpoints without breaking existing `/mcp` aggregate. Backward-compatible by construction.

### Tasks

- [ ] T16.1.1 — Extend `internal/proxy/gateway.go`: replace single `server *mcp.Server` field (gateway.go:28) with:
  ```go
  aggregateServer  *mcp.Server           // existing behavior, keyed by "" (empty string)
  perBackendServer map[string]*mcp.Server // new: keyed by backend name
  serverMu         sync.RWMutex          // guards perBackendServer map mutations
  ```
  `Gateway.Server()` returns `aggregateServer` (unchanged API). New method `Gateway.ServerFor(backend string) *mcp.Server` returns per-backend instance or nil.

- [ ] T16.1.2 — Extend `Gateway.RebuildTools()` (gateway.go:91) to update BOTH registries:
  - Aggregate: existing namespaced tools `<server>__<tool>` with description prefix `[<server>] ...` (gateway.go:141) — unchanged.
  - Per-backend: for each running backend, ensure `perBackendServer[backend]` exists (lazy-create with `mcp.NewServer(&mcp.Implementation{Name: backend, Version: g.version}, nil)`); register that backend's tools WITHOUT namespace prefix, and description WITHOUT `[<server>]` prefix. When a backend is removed, tear down its `perBackendServer` entry.

- [ ] T16.1.3 — Add `registerToolForBackend(nt namespacedTool)` helper mirroring `registerTool()` (gateway.go:140) but writing to `perBackendServer[nt.server]`. Reuse `router.Call` for dispatch via the same `nt.namespaced` path — per-backend view is just a different surface, same underlying routing.

- [ ] T16.1.4 — Add `internal/proxy/gateway_proxy_test.go`:
  - Unit: `TestRebuildTools_DualMode` — two backends, aggregate has both namespaced, per-backend has each independently unnamespaced.
  - Unit: `TestPerBackendServer_ListChangedScoping` — adding a tool to backend X fires `list_changed` on `perBackendServer[X]` only, not on other per-backend servers (verify via SDK session mock).
  - Unit: `TestPerBackendServer_ToolDescriptionNoBrackets` — `[context7]` prefix MUST be absent in per-backend view.
  - Unit: `TestConcurrentRebuildAndBackendAdd` — race-safe (reuse pattern from existing `TestConcurrentRebuildAndFilteredTools` at gateway_test.go:310).

- [ ] T16.1.5 — Extend `internal/api/server.go:200-213` HTTP handler wiring. Replace:
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

- [ ] T16.1.5.a — **[REVIEW-16 L-01]** SDK path verification. Before committing T16.1.5, read ALL of `go-sdk@v1.4.1/mcp/streamable.go` and `protocol.go`. Enumerate every sub-path the SDK uses (e.g. if it uses `/mcp/session/{id}` for resumption). Extend `SERVER_NAME_RE` or add a name-denylist rejecting backend names that would collide. Document findings in code comment at the dispatcher. If SDK uses NO sub-paths (single-endpoint), removes the "anything-else" ambiguity entirely — router becomes: exact `/mcp` → aggregate; `/mcp/{name}` → per-backend; unknown → 404.

- [ ] T16.1.6 — Add `internal/api/server_proxy_test.go`:
  - `TestStreamablePerBackendRoute` — GET /mcp/context7 returns context7's serverInfo
  - `TestStreamableUnknownBackend404` — /mcp/nonexistent returns 400 (SDK default for nil return)
  - `TestAggregateRouteStillWorks` — /mcp returns aggregate serverInfo
  - `TestPerBackendAuthRequiresSameBearer` — same Bearer token works for both paths

- [ ] T16.1.7 — Extend `mcpTransportPolicy` middleware (server.go:231) to match `/mcp` AND `/mcp/*` paths identically (loopback-only or bearer-required apply uniformly). No security regression.

- [ ] T16.1.GATE: `go test ./...` PASS + `go vet ./...` clean + `mcp__pal__codereview` (go files changed) zero errors (any finding at or above CLAUDE_GATE_MIN_BLOCKING_SEVERITY; default: any finding) + `mcp__pal__thinkdeep` on dual-mode design zero errors.

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

- [ ] T16.2.1 — Create `installer/plugin/` directory structure:
  ```
  installer/plugin/
  ├── .claude-plugin/
  │   └── plugin.json       # metadata + userConfig
  ├── .mcp.json             # generated at runtime; checked-in stub with no mcpServers
  └── README.md             # plugin user docs
  ```

- [ ] T16.2.2 — Author `installer/plugin/.claude-plugin/plugin.json`:
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

- [ ] T16.2.3 — Implement `internal/plugin/regen.go`:
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

- [ ] T16.2.4 — Wire `RegenerateMCPJSON` into gateway lifecycle:
  - Call on `POST /api/v1/servers` (backend added) — api/server.go:531.
  - Call on `DELETE /api/v1/servers/{name}` — api/server.go:553.
  - Call on `PATCH /api/v1/servers/{name}` if `disabled` flag changes (disabled backend should drop out of plugin view).
  - Idempotent: no-op if generated content matches existing file.

- [ ] T16.2.5 — Plugin directory discovery. Gateway needs to know WHERE to write `.mcp.json`. Two paths:
  - **Dev path** (from repo): `$GATEWAY_PLUGIN_DIR` env var points to `installer/plugin/`.
  - **Installed path** (post `claude plugin install`): `~/.claude/plugins/cache/mcp-gateway@<marketplace>/`.
  - Implement `internal/plugin/discover.go` with `FindPluginDir() (string, error)` that walks:
    1. `$GATEWAY_PLUGIN_DIR` if set
    2. `~/.claude/plugins/cache/mcp-gateway@*/` (glob)
    3. Return nil + user-friendly error if neither found (regen is skipped, not fatal).

- [ ] T16.2.6 — Author `installer/marketplace.json` (local marketplace for one-command install):
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

- [ ] T16.2.7 — Add `internal/plugin/regen_test.go`:
  - `TestRegen_AtomicWrite` — write is visible only after rename (partial file never observable).
  - `TestRegen_Idempotent` — second call with same backends produces identical output.
  - `TestRegen_BackupOnOverwrite` — `.mcp.json.bak` contains previous content.
  - `TestRegen_JSONValid` — malformed Go struct can't produce invalid JSON (schema validation pre-rename).
  - `TestRegen_DiscoverFallbacks` — env > glob > error, cross-platform paths.
  - `TestRegen_DisabledBackendExcluded` — disabled servers don't appear in output.
  - Cross-platform: use `t.TempDir()` + `filepath.Join` everywhere; no raw slashes.

- [ ] T16.2.GATE: `go test ./internal/plugin/...` PASS + `mcp__pal__codereview` on plugin package zero errors + `mcp__pal__thinkdeep` on plugin lifecycle edge cases zero errors.

### Files

- `installer/plugin/.claude-plugin/plugin.json` (new)
- `installer/plugin/.mcp.json` (new, stub)
- `installer/plugin/README.md` (new)
- `installer/marketplace.json` (new)
- `internal/plugin/regen.go` (new)
- `internal/plugin/discover.go` (new)
- `internal/plugin/regen_test.go` (new)
- `internal/api/server.go` (modify: call regen on mutations)

### Rollback

Plugin directory is isolated under `installer/plugin/`. Revert removes the directory + `internal/plugin/` package + the regen hooks in api/server.go. Existing `/api/v1/servers` behavior unchanged when regen skipped (no plugin dir).

---

## Phase 16.3 — Gateway REST endpoints for patch integration

**Goal:** Provide the HTTP surface for the webview patch to heartbeat, poll actions, and report probe results.

### Tasks

- [ ] T16.3.1 — New REST group `/api/v1/claude-code/*`, Bearer-auth-required (reuse existing auth middleware):
  - `POST /api/v1/claude-code/patch-heartbeat` — accepts JSON `{patch_version, cc_version, vscode_version, fiber_ok, registry_ok, reload_command_exists, session_id, timestamp}`. Gateway stores latest per `session_id` with 1h TTL; emits structured log entry.
  - `GET /api/v1/claude-code/patch-status` — returns array of latest heartbeats across all active sessions (for dashboard polling).
  - `GET /api/v1/claude-code/pending-actions` — returns next action for patch to execute, e.g. `{id, action: "reload-plugins", nonce}`; idempotent read with `?after=<cursor>` for at-most-once semantics.
  - `POST /api/v1/claude-code/pending-actions/{id}/ack` — patch confirms execution. Gateway marks as delivered.
  - `POST /api/v1/claude-code/probe-result` — patch reports `[Test now]` result `{nonce, ok, error?}`.

- [ ] T16.3.2 — Implement `internal/patchstate/state.go`:
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

- [ ] T16.3.3 — Wire `RegenerateMCPJSON` (16.2.4) to ALSO enqueue a `reload-plugins` pending action after successful plugin regen. Debounce: if another regen fires within 500ms, coalesce into a single queued action (prevents action-flood on bulk backend operations).

- [ ] T16.3.4 — CORS: add `vscode-webview://` to `Access-Control-Allow-Origin` for `/api/v1/claude-code/*` routes ONLY. The rest of `/api/v1` keeps its existing CSRF-protected origin policy. Verify via request from patched webview in integration test. **[REVIEW-16 L-02]** Explicit OPTIONS preflight handler required — browsers send OPTIONS before POST from a different origin. Respond 204 with: `Access-Control-Allow-Origin: vscode-webview://*`, `Access-Control-Allow-Methods: GET, POST`, `Access-Control-Allow-Headers: Authorization, Content-Type`, `Access-Control-Max-Age: 300`. Preflight handler runs BEFORE bearer auth (preflight has no auth header).

- [ ] T16.3.5 — Rate limiting: `/pending-actions` GET polled every 2s by patch; set per-IP rate limit of 60 req/min (generous but bounded). Heartbeat has separate 5 req/min limit per session_id.

- [ ] T16.3.6 — Add `internal/api/claude_code_handlers_test.go`:
  - `TestHeartbeatStoreAndRetrieve`
  - `TestPendingActionsFIFO` + ack semantics
  - `TestProbeResultTTL`
  - `TestClaudeCodeRoutesBearerRequired` — 401 without token
  - `TestCORSVSCodeWebview` — `vscode-webview://` origin allowed
  - `TestCORSWebExternal` — `https://evil.com` origin denied
  - `TestClaudeCodeCORSPreflight` — OPTIONS request returns 204 with correct Allow-* headers, BEFORE bearer auth layer — **[REVIEW-16 L-02]**
  - `TestActionDebounce` — two regens within 500ms produce one queued action
  - `TestPatchStatePersistenceRoundtrip` — write state, restart state, assert actions survive with TTL filter — **[REVIEW-16 M-01]**

- [ ] T16.3.7 — Document schema in `docs/api/claude-code-endpoints.md` with examples.

- [ ] T16.3.GATE: `go test ./internal/api/... ./internal/patchstate/...` PASS + PAL codereview zero errors + PAL thinkdeep on concurrency/TTL correctness zero errors.

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

**Prerequisite:** Phase 16.0 SPIKE must pass (maintainer sign-off).

**Goal:** Install a JavaScript patch into Claude Code's webview that runs a heartbeat + polls pending actions + triggers `executeCommand("reload-plugins")` via React Fiber walk. Copy the proven pattern from `claude-team-control/patches/`.

### Tasks

- [ ] T16.4.1 — Author `installer/patches/apply-mcp-gateway.sh` (bash, POSIX-sh-compatible):
  - Mirror `claude-team-control/patches/apply-taskbar.sh` structure (lines 1-97).
  - Locate `~/.vscode/extensions/anthropic.claude-code-*/webview/index.js` via semantic-version sort.
  - Backup to `index.js.bak` ONCE (preserve first clean backup).
  - Idempotent: detect `"MCP Gateway Patch v"` marker, reapply cleanly.
  - Substitute `${GATEWAY_URL}` and `${GATEWAY_AUTH_TOKEN}` placeholders at patch time via `awk` (POSIX) — match the `__ORCHESTRATOR_REST_PORT__` pattern in taskbar apply script.
  - Reject corrupted patch file if placeholder survives after substitution (restore backup).
  - `--auto` mode (silent if already patched) for hook invocation.
  - `--uninstall` mode: restore `.bak`, remove marker.

- [ ] T16.4.1.a — **[REVIEW-16 L-03]** Lock down patched file permissions. After write:
  - POSIX: `chmod 600 "$INDEX_JS"` (only current user can read the inlined bearer token).
  - Windows (in .ps1 counterpart): `icacls` grants current-user-only ALLOW, deny-by-default (mirror the DACL pattern from `internal/auth/token_perms_windows.go`).
  - Integration test asserts post-apply file mode = 0600 on Unix; DACL shape on Windows.
  - Document in README §"Security considerations for the webview patch": inlined token is at same trust boundary as `~/.mcp-gateway/auth.token`.

- [ ] T16.4.2 — Author `installer/patches/apply-mcp-gateway.ps1` (PowerShell variant for Windows without Git Bash). Same semantics, different syntax.

- [ ] T16.4.3 — Author `installer/patches/porfiry-mcp.js` (~200 lines). Key parts:
  - React Fiber walk (copy from `porfiry-taskbar.js:65-93`).
  - Heartbeat every 60s via `fetch(GATEWAY_URL + "/api/v1/claude-code/patch-heartbeat", {method:"POST", headers:{Authorization: "Bearer " + TOKEN, "Content-Type": "application/json"}, body: JSON.stringify({...})})`.
  - Poll `/api/v1/claude-code/pending-actions` every 2s.
  - On action `"reload-plugins"`: call `appRegistry.executeCommand("reload-plugins")`. Debounce: skip if active tool call in DOM (check `[class*="spinnerRow_"]` same as taskbar's `enforceLock`).
  - Register `__mcp_gateway_probe` command via `appRegistry.registerCommand` if available; handler posts probe result.
  - On each action success, POST `/pending-actions/{id}/ack`.
  - Resilience: if fiber walk fails (e.g. after React hot reload), invalidate cache + retry on next DOM mutation (same pattern as taskbar's `MutationObserver` on `#root`).
  - Silent-on-error: all `fetch` calls wrapped in `.catch(() => {})` to avoid crashing the webview.
  - Heartbeat payload: `{patch_version, cc_version (parsed from extension path), vscode_version (from `process.versions.vscode` if available), fiber_ok: !!appSession, registry_ok: !!appRegistry, reload_command_exists: tryListRegistryCommands().includes("reload-plugins"), session_id: getOrCreateSessionId()}`.

- [ ] T16.4.4 — Auth token injection. Challenge: webview patch cannot read `~/.mcp-gateway/auth.token` directly (sandbox). Options evaluated:
  - **A (chosen)**: `apply-mcp-gateway.sh` substitutes `${GATEWAY_AUTH_TOKEN}` at patch-install time from file contents. Patch ships token inline. Token rotation → re-run apply script.
  - **B (rejected)**: dashboard posts token into webview via `localStorage` — cross-origin restrictions in VSCode webviews make this unreliable.
  - Document rotation procedure in README.

- [ ] T16.4.5 — VSCode extension activation hook: extension checks on activate whether patch marker exists in index.js (extension knows path from its own installation dir). If stale (extension version changed, patch marker absent), silently run patch installer. **[REVIEW-16 M-04]** Explicit platform dispatch (Node.js):
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

- [ ] T16.4.6 — Add `installer/patches/porfiry-mcp.test.mjs` — node-based test harness mirroring `porfiry-taskbar.test.mjs`:
  - Mock DOM + fiber tree; assert fiber walk finds registry.
  - Mock `appRegistry.executeCommand`; assert called with `"reload-plugins"` after pending-action arrives.
  - Debounce test: two actions within 500ms → one call.
  - Active-tool-call skip test.

- [ ] T16.4.7 — Supported-versions table in `configs/supported_claude_code_versions.json`:
  ```json
  {
    "min": "2.0.0",
    "max_tested": "2.5.8",
    "known_broken": [],
    "last_verified": "2026-04-2X"
  }
  ```
  Consumed by the patch + dashboard to classify current CC version.

- [ ] T16.4.GATE: mocha tests PASS + `shellcheck installer/patches/apply-mcp-gateway.sh` clean + PAL codereview (JS + shell) zero errors + PAL thinkdeep on patch failure modes zero errors + spike T16.0 sign-off still valid for target Claude Code versions.

### Files

- `installer/patches/apply-mcp-gateway.sh` (new)
- `installer/patches/apply-mcp-gateway.ps1` (new)
- `installer/patches/porfiry-mcp.js` (new)
- `installer/patches/porfiry-mcp.test.mjs` (new)
- `configs/supported_claude_code_versions.json` (new)

### Rollback

Run `apply-mcp-gateway.sh --uninstall` to restore `index.js.bak` and remove the patch. No persistent state outside `~/.vscode/extensions/anthropic.claude-code-*/webview/`. On full revert, also remove activation-hook call in the extension.

---

## Phase 16.5 — Dashboard "Claude Code Integration" panel

**Prerequisite:** 16.3 endpoints + 16.4 patch.

**Goal:** Surface the integration in the VSCode extension with user-friendly UX. Two independent status indicators (Patch + Channel), tri-state (green/yellow/red), specific error message per failure mode.

### Tasks

- [ ] T16.5.1 — New webview panel `vscode/mcp-gateway-dashboard/src/webview/claude-code-panel.ts`:
  - Section header "Claude Code Integration"
  - `[Activate for Claude Code]` button
  - Plugin status line: `● Installed — mcp-gateway plugin registered` / `● Not installed` / `● Installation failed: <reason>`
  - Divider
  - `[x] Auto-reload plugins` checkbox
  - Two status rows (only shown when checkbox on):
    - `Patch:    ● Installed  v1.6.0` / `● Not installed` / `● Incompatible (CC v2.6.1, max tested 2.5.8)` / `● Stale — VSCode reload required`
    - `Channel:  ● Active   last heartbeat 12s ago` / `● Idle  (VSCode unfocused)` / `● Broken — <reason>`
  - Overall status banner: green "✓ Auto-reload is working" / yellow "⏸ Claude Code idle" / red "✗ <specific reason + action>"
  - Buttons: `[Test now]` + `[Copy diagnostics]`.

- [ ] T16.5.2 — `[Activate for Claude Code]` handler:
  - Check `claude plugin list --json` for `mcp-gateway` presence.
  - If missing, run `claude plugin marketplace add <repo>/installer/marketplace.json && claude plugin install mcp-gateway@mcp-gateway-local` — prompt user for auth_token (read from `~/.mcp-gateway/auth.token` if exists, show masked preview).
  - Regenerate `.mcp.json` via REST `POST /api/v1/claude-code/plugin-sync` (new endpoint — thin wrapper around 16.2.3 regen).
  - Report success/failure in UI with actionable next step.

- [ ] T16.5.3 — Auto-reload checkbox handler:
  - Off → On: run `apply-mcp-gateway.sh` (Unix) or `apply-mcp-gateway.ps1` (Windows) via VSCode terminal; show progress; prompt "Reload VSCode window now" on success.
  - On → Off: run `apply-mcp-gateway.sh --uninstall`; confirm restore of backup.
  - Persist checkbox state in workspace settings.

- [ ] T16.5.4 — Status polling: webview calls `GET /api/v1/claude-code/patch-status` every 10s (lightweight — gateway cache, no CC round-trip). Compose patch status locally (FS check via extension's Node context reading index.js for marker) + channel status from response.

- [ ] T16.5.5 — Failure-mode messages (matrix from design doc):
  - A. Patch file missing → "Click ☑ to install patch"
  - B. VSCode not reloaded after apply → "Reload VSCode: Ctrl+Shift+P → 'Developer: Reload Window'"
  - C. CC version incompatible → "Claude Code v{X} not supported (patch tested up to {MAX}). [Report success/failure on GitHub]"
  - D. Registry API changed (`fiber_ok=false`) → "Claude Code internal API changed. Aggregate mode still works as fallback."
  - E. `reload-plugins` command missing (`reload_command_exists=false`) → "Claude Code removed /reload-plugins command. Manual restart needed."
  - F. CORS blocks gateway → "Gateway unreachable from Claude Code webview. Check `gateway.allowed_origins` setting."
  - G. No plugin installed → "Click [Activate for Claude Code] first."
  - H. Gateway not running → "mcp-gateway daemon not running on port 8765."
  - I. VSCode idle → YELLOW "⏸ Claude Code idle — patch OK"
  - J. Multiple sessions → show per-session list.
  - **K. Token rotation detected** — **[REVIEW-16 L-06 + M-03]** — `mtime(~/.mcp-gateway/auth.token) > mtime(patched-index.js)` → RED "Gateway token rotated since patch install. Inlined token is stale. [Reinstall patch] to pick up new token." Dashboard also offers [Reinstall via mcp-ctl] which runs `mcp-ctl install-claude-code --no-plugin-change` to re-apply patch with current token without touching plugin.

- [ ] T16.5.6 — `[Test now]` handler: dashboard `POST /api/v1/claude-code/probe-trigger {nonce}` → gateway enqueues `__mcp_gateway_probe` action → patch executes it → posts result → dashboard polls and shows green "Test passed" / red "Test failed: <reason>". Timeout 5s → "Timeout — patch not responding".

- [ ] T16.5.7 — `[Copy diagnostics]` generates structured report (see design doc):
  - Environment (OS, VSCode version, CC version, gateway version)
  - Plugin status (installed/location/entries)
  - Patch status (installed/location/version/backup existence)
  - Supported-versions map + classification of current CC version
  - Last 5 heartbeats
  - Failure trace if any
  - Report-to URL with issue template link
  - Copied to clipboard via `vscode.env.clipboard.writeText`.

- [ ] T16.5.8 — Unit tests `vscode/mcp-gateway-dashboard/src/test/claude-code-panel.test.ts`:
  - State matrix: each failure mode produces correct banner + action.
  - Checkbox behavior when RED: stays checkable with warning banner (not paternalize).
  - Diagnostics dump includes all required sections.

- [ ] T16.5.9 — Extension `package.json`: register new command `mcpGateway.showClaudeCodeIntegration`, wire to tree view or status bar context menu.

- [ ] T16.5.GATE: `npm test` PASS (all 513+ tests + new) + `npm run compile` clean + `npm run deploy` rebuilds VSIX + PAL codereview zero errors + manual VSCode smoke test on macOS/Windows (matrix row in REVIEW-16.md).

### Files

- `vscode/mcp-gateway-dashboard/src/webview/claude-code-panel.ts` (new)
- `vscode/mcp-gateway-dashboard/src/claude-code/*` (new module: status, patch-installer, diagnostics)
- `vscode/mcp-gateway-dashboard/src/test/claude-code-panel.test.ts` (new)
- `vscode/mcp-gateway-dashboard/src/extension.ts` (modify: register command)
- `vscode/mcp-gateway-dashboard/package.json` (modify: command registration)

### Rollback

Uncheck auto-reload checkbox (runs uninstall). Uninstall plugin via `claude plugin uninstall mcp-gateway`. Revert extension commit. No gateway state affected — dashboard is a consumer only.

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

**Goal:** Automated proof of the full chain: add backend via REST → plugin .mcp.json regen → patch heartbeat → `reload-plugins` triggered → new MCP visible via a simulated client.

### Tasks

- [ ] T16.7.1 — `internal/api/integration_phase16_test.go` (new). Build tag `integration`. Flow:
  1. Start gateway with temp config.
  2. Simulate plugin dir: create `./testdata/plugin/` with .claude-plugin/plugin.json.
  3. Set `GATEWAY_PLUGIN_DIR` env var.
  4. Start an MCP client using go-sdk (same library we use for the server) pointed at `http://127.0.0.1:8765/mcp`.
  5. Initial `tools/list` returns aggregate tools for initially-configured backends.
  6. Simulate patch heartbeat: `POST /patch-heartbeat` with `fiber_ok=true, registry_ok=true, reload_command_exists=true`.
  7. Add a new backend via `POST /api/v1/servers` (a local stub MCP child process).
  8. Assert: plugin `.mcp.json` regenerated with new entry (read file, JSON parse).
  9. Assert: pending action in queue (poll `GET /pending-actions`).
  10. Simulate patch executing action: `POST /pending-actions/{id}/ack`.
  11. Assert: client receives `notifications/tools/list_changed` (this is separate — SDK-level, verifies aggregate hot-add works regardless of patch).
  12. Assert: `tools/list` returns new tool.
  13. Cleanup: stop backend, repeat for removal — plugin entry disappears + action enqueued.

- [ ] T16.7.2 — `internal/api/integration_cors_test.go`: simulate cross-origin request from `vscode-webview://` schema; assert allowed. Assert `https://evil.com` denied.

- [ ] T16.7.3 — Patch JS side: `installer/patches/porfiry-mcp.integration.test.mjs` — spin up gateway in a child process, point mock DOM's fetch at it, run full patch lifecycle, assert heartbeat appears in gateway state, action delivery loop works end-to-end.

- [ ] T16.7.4 — Document test procedure in `docs/TESTING-PHASE-16.md` — how to run each tier, prerequisites (Go 1.25+, Node 20+, stub MCP server binary).

- [ ] T16.7.GATE: Integration tests PASS on CI (Linux + macOS; Windows manual protocol via Makefile target `test-integration-phase16-windows`). PAL thinkdeep on edge cases zero errors.

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
  - Manual path for users who decline patches: `/reload-plugins` after adding MCP.
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
  - Title: "Claude Code Integration: Dual-mode gateway + Plugin packaging + Webview patch"
  - Context: 3 findings closed
  - Decision: hybrid approach (aggregate + proxy + plugin + optional patch)
  - Rejected alternatives (with reasoning): HTTP reverse proxy (MCP stateful), suppression of aggregate when plugin loaded (disappearing tools UX), SIGHUP wrapper (invasive shell rc edit), socket injection (unofficial).
  - Consequences: patch is fragile vs Claude Code API changes; aggregate fallback mitigates; supported-versions map tracks compat.
  - References: Issues #13646, #16143, #18174; PAL consultation rounds; spike T16.0 results.

- [ ] T16.9.6 — CHANGELOG.md entry for v1.6.0:
  - **Added**: Dual-mode gateway (/mcp/{backend} per-backend endpoints), Claude Code Plugin packaging, mcp-ctl install-claude-code, webview patch for /reload-plugins automation (opt-in), gateway.invoke universal fallback tool, supported-versions map.
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
- **Webview patch**: opt-in, idempotent, uninstallable. Copies the proven pattern from `claude-team-control/patches/`. Triggers `executeCommand("reload-plugins")` via React Fiber walk.
- **No suppression of aggregate tools when backend also in plugin**: PAL-validated; prevents "disappearing tools" UX bug during the window between plugin regen and reload.
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
