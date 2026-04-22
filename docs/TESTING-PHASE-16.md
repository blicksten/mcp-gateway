# Testing — Phase 16 Claude Code Integration

Procedures for running the four test tiers that validate the Phase 16
implementation: Go unit tests, Go integration tests (build-tagged), TS
dashboard tests (Mocha), and the webview-patch node harness.

## Prerequisites

- **Go 1.25+** — build + run the gateway + tests. `go version` should
  report `go1.25.x` or newer.
- **Node 20+** — build + run the dashboard extension + patch harness.
  `node --version`.
- **VS Code 1.90+** — manual smoke only; unit tests run headless.
- **mcp-gateway mock server binary** — built on the fly by
  `internal/api/integration_test.go::buildMockServer` from
  `internal/testutil`. No pre-install step.

Optional (for T16.7.3, currently deferred — see below):
- The patched `porfiry-mcp.js` must be present at
  `installer/patches/porfiry-mcp.js`. This file is authored by
  concurrent Phase 16.4 pipeline `feature-b8f2decf`; the node harness
  is skipped here until it lands.

## Tier 1 — Go unit tests

Scope: `internal/api/*` handler + middleware coverage, `internal/patchstate/*`
state machine + persistence, `internal/proxy/*` (Phase 16.6 scope),
`internal/plugin/*` (Phase 16.2 scope).

Run:

```bash
go test ./... -count=1
```

Expected: 14 packages green, 0 failures. Typical runtime ~2-3 minutes on
a modern dev machine.

Claude-Code-specific scope only:

```bash
go test ./internal/api/ ./internal/patchstate/ -count=1 -run 'ClaudeCode|PatchState|Heartbeat|ProbeTrigger|CORS'
```

## Tier 2 — Go integration tests (build-tagged)

Scope: full-chain patch lifecycle — add backend via REST → regen plugin
`.mcp.json` → pending-action enqueue → patch ack → remove backend;
probe-reconnect round-trip; `/plugin-sync` response shape; CORS preflight
vs external origin.

Run:

```bash
go test -tags=integration ./internal/api/... -count=1 -run TestIntegration_Phase16
```

Expected: 3 tests pass (`FullPatchChain`, `ProbeTriggerRoundTrip`,
`PluginSyncReturnsStatus`). Each test builds the mock MCP server binary
(`internal/testutil`) and runs full gateway start/stop — ~5-10 s per test.

Run non-tagged CORS integration tests (always-on):

```bash
go test ./internal/api/... -count=1 -run TestIntegration_CORS
```

Expected: 5 tests pass (preflight allow + deny + origin-echo + ordering
regression guard + external-origin-omits-echo).

Windows manual protocol: run from a PowerShell session with Go in PATH.
The integration tests use `t.TempDir()` + `filepath.Join` everywhere so
POSIX-vs-Windows path separators are handled.

## Tier 3 — TypeScript dashboard tests (Mocha)

Scope: Phase 16.5 `src/claude-code/*` state machine + diagnostics report
builder + applyConfigOverride boundary rules.

From `vscode/mcp-gateway-dashboard/`:

```bash
# Fast scoped run — just Phase 16.5 files (~150 ms)
./node_modules/.bin/mocha --require ts-node/register --file 'src/test/setup.ts' 'src/test/claude-code-*.test.ts'

# Broader sample (excluding v16-4 tech-debt tests)
./node_modules/.bin/mocha --require ts-node/register --file 'src/test/setup.ts' --reporter min --exclude 'src/test/gateway-client.test.ts' --exclude 'src/test/log-viewer.test.ts' --exclude 'src/test/commands.test.ts' 'src/test/**/*.test.ts'

# npm-scripted full run (slow due to known-flaky tests — see MEMORY v16-4)
npm test
```

Expected for the scoped run: 55 passing (147 ms). Broader run: 492
passing (~2 s).

## Tier 4 — Webview patch harness (node:test) — CURRENTLY DEFERRED

**Status: pending Phase 16.4 (`feature-b8f2decf`).**

Scope when ready: `installer/patches/porfiry-mcp.test.mjs` (unit — 22
tests pass in ~290 ms) + `installer/patches/porfiry-mcp.integration.test.mjs`
(T16.7.3 — spins up gateway in a child process, points mock DOM's fetch
at it, runs the full patch lifecycle).

Run when files land:

```bash
node --test installer/patches/porfiry-mcp.test.mjs
node --test installer/patches/porfiry-mcp.integration.test.mjs
```

## Manual VSCode smoke — Phase 16.5

The dashboard panel has no automated UI test (headless VSCode Webview is
out of scope for Mocha). Manual smoke checklist:

1. Install the rebuilt VSIX: `vsce package` in
   `vscode/mcp-gateway-dashboard/`, then VSCode `Developer: Install
   Extension from VSIX`.
2. `Developer: Reload Window`.
3. Command palette → `MCP Gateway: Show Claude Code Integration`.
4. Expect the panel to open with "Polling gateway..." yellow banner.
5. Start the gateway (`mcp-ctl start` or daemon process). Panel should
   switch to green "✓ Auto-reload is working" within 10 s.
6. Click `[Copy diagnostics]` — clipboard should receive a multi-line
   markdown report including Alt-E metric fields.
7. Click `[Probe reconnect]` — expect info toast with nonce.

## CI — current scope

- `go test ./... -count=1` — runs on every PR.
- `go test -tags=integration ./internal/api/... -run TestIntegration_Phase16`
  — runs on PRs that touch `internal/api/*`, `internal/patchstate/*`, or
  `internal/plugin/*`.
- `go test ./internal/api/... -run TestIntegration_CORS` — always on.
- Mocha dashboard — runs on PRs that touch `vscode/mcp-gateway-dashboard/*`.

## Out of scope for this phase

- Patch node harness — lands with Phase 16.4.
- `mcp-ctl install-claude-code` end-to-end install — lands with Phase
  16.8 (separate subcommand test in `cmd/mcp-ctl/install_claude_code_test.go`).
- Dogfood `.mcp.json` smoke — lands with Phase 16.9 T16.9.4.a as a CI
  workflow file (`dogfood-smoke.yml`).
