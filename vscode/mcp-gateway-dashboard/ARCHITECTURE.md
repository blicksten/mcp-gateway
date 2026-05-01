# MCP Gateway Dashboard — Architecture

A 50-line orientation for new contributors. Closes [B-17](../../docs/PLAN-audit-dashboard.md).

The extension is a thin VSCode UI on top of the `mcp-gateway` REST API. It owns no domain state — every visible widget is driven from one polling cache that hits `GET /api/v1/health` and `GET /api/v1/servers` on a configurable interval (default 5 s).

## Components (one-line each)

| File | Role |
|---|---|
| [src/extension.ts](src/extension.ts) | Activation entry. Wires `ServerDataCache` → tree providers → status bars → webview panels, registers the 21 commands declared in `package.json::contributes.commands`. |
| [src/server-data-cache.ts](src/server-data-cache.ts) | Single source of truth. One `setInterval` poll cycle calls `GatewayClient.getHealth()` + `listServers()`, classifies failures (`network` / `auth` / `http`), fans out via `EventEmitter`. All UI consumers subscribe — no UI calls REST directly. |
| [src/gateway-client.ts](src/gateway-client.ts) | Typed REST wrapper. Returns `GatewayError` with `kind: 'network' \| 'http' \| 'auth' \| 'timeout'`. The `'auth'` kind (Phase 0d) lets the extension show an actionable toast instead of a generic offline banner on 401. |
| [src/auth-header.ts](src/auth-header.ts) | Bearer-token resolver. Reads `mcpGateway.authTokenPath` setting with `MCP_GATEWAY_AUTH_TOKEN` env override. Re-resolved per REST call so token rotation takes effect without VS Code reload. |
| [src/daemon.ts](src/daemon.ts) | `DaemonManager` — child-process lifecycle for the gateway binary (start/stop/restart). REST-first stop, SIGTERM only as fallback (Phase 9 — pending). |
| [src/logger.ts](src/logger.ts) | Shared wrapper around the `MCP Gateway` `OutputChannel`. All silent-catch sites route here; the `mcpGateway.verboseLogging` setting controls debug-level chatter. |
| [src/version-compat.ts](src/version-compat.ts) | One-shot daemon-version check after the first successful `/health` — toasts an actionable hint when the daemon is older than the extension's documented minimum. |
| [src/claude-config-sync.ts](src/claude-config-sync.ts) | Workaround for Claude Code 2.1.123+'s plugin-mcpServers loader regression. Mirrors gateway backends into `~/.claude.json::mcpServers` so they show up in the `/mcp` panel. |
| [src/credential-store.ts](src/credential-store.ts) | `vscode.SecretStorage`-backed env-var vault for backend definitions. Reconciled at activation against the daemon's view of the world. |
| [src/keepass-importer.ts](src/keepass-importer.ts) | One-shot import flow — runs `mcp-ctl keepass-import`, writes results into `CredentialStore`. |
| [src/slash-command-generator.ts](src/slash-command-generator.ts) | Generates `.claude/commands/<server>.md` files when servers start/stop. Gated by `mcpGateway.slashCommandsEnabled`. |

## Tree views & status bars

| Provider | View ID | Source |
|---|---|---|
| [GatewayTreeProvider](src/gateway-tree-provider.ts) | `mcpGatewayDaemon` | `cache.gatewayHealth` — root row reflects daemon up/down/auth-failed. |
| [BackendTreeProvider](src/backend-tree-provider.ts) | `mcpBackends` | `cache.servers` — one node per gateway backend, contextValue drives the inline action menu in `package.json::menus.view/item/context`. |
| [SapTreeProvider](src/sap-tree-provider.ts) | `mcpSapSystems` | Filtered subset of `cache.servers` matched by [`SapDetector`](src/sap-detector.ts) regex. Gated by `mcpGateway.sapSystemsEnabled` setting; activation only registers the provider when the setting is on. |
| [McpStatusBar](src/status-bar.ts) / [SapStatusBar](src/sap-status-bar.ts) | bottom bar | Both subscribe to `cache.onDidChange`. |

## Webview panels (postMessage contract)

[`src/webview/`](src/webview/) hosts 5 panels — each is a singleton TS class wrapping a `vscode.WebviewPanel`. Render entry points live in `updateAll(...)` methods called from extension/cache subscribers. The panel-side script in each HTML payload posts JSON messages with `{type, payload}`; the host responds via `panel.webview.postMessage`. See `claude-code-panel.ts` for the canonical pattern (probe-trigger, refresh-token toggle, Activate-for-Claude-Code button).

## REST surface consumed

`/api/v1/health`, `/api/v1/servers[/{name}]`, `/api/v1/servers/{name}/{enable,disable,restart,reset}`, `/api/v1/tools[/call]`, `/api/v1/logs?stream=sse`, `/api/v1/shutdown`, and the Phase-16 Claude Code endpoints (`/api/v1/claude-code/{plugin-sync,probe-trigger,probe-result,sessions,...}`). All requests carry the Bearer token from `auth-header.ts`.

## Build & deploy

`npm run deploy` is the one command operators run: `tsc → vsce package → install-vsix.js`. The `pretest` hook runs `scripts/check-zombie-dom.js` (Phase 6 regression harness — flags HTML `id="…"` placeholders with no JS handler, the bug class that caused B-01..B-04). Tests live in `src/test/**/*.test.ts`, run via `mocha + ts-node`.

## Diagnostics from the CLI

`mcp-ctl doctor` (Phase 7 / [cmd/mcp-ctl/doctor.go](../../cmd/mcp-ctl/doctor.go)) replays the same five checks the [Claude Code Integration panel](src/webview/claude-code-panel.ts) shows in the UI, but headless — handy for support sessions where opening the dashboard is not an option.
