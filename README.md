# MCP Gateway

> Created by **Stanislav Naumov** ([@blicksten](https://github.com/blicksten))

A standalone daemon that sits between AI clients (Claude Code, Cursor, Continue.dev, Cline) and all your MCP servers — managing connections, health, and the full server lifecycle **on the fly**, without restarting sessions or editing config files.

## Core Idea: Live Control Plane for MCP

Traditional MCP setup is **static** — you edit JSON, restart the session, hope it works. If a server crashes mid-conversation, you're stuck.

MCP Gateway makes it **dynamic**:

- **Connect / disconnect servers on the fly** — add a new MCP server mid-session via REST API or VS Code UI, no restart needed
- **Real-time status** — every server has a live health state (`running` / `degraded` / `error` / `disabled`), visible in VS Code status bar and via API
- **REST API for everything** — `POST /api/servers` to add, `DELETE` to remove, `PATCH` to enable/disable, `GET` to see status — all while the AI agent is working
- **Auto-recovery** — crashed server? Gateway restarts it with exponential backoff. Circuit breaker disables servers that keep failing. Zero manual intervention.
- **Context window friendly** — tool namespacing + schema compression + per-server tool budgets keep the AI's context clean

```
Without Gateway:                 With Gateway:
  Client -> MCP 1 (crashed!)       Client -> Gateway -> MCP 1 (auto-restarted)
  Client -> MCP 2                                    -> MCP 2 (healthy)
  Client -> MCP 3 (added later?)                     -> MCP 3 (hot-added via API)
  (edit JSON, restart session)                        (zero downtime)
```

## What It Solves

| Problem | Without Gateway | With Gateway |
|---------|----------------|-------------|
| **MCP not loading in profile** | No MCP access at all (e.g. Claude Code `.claude-personal` bug) | Connect to gateway via HTTP — works from any profile |
| Server crashes | Manual restart, lost session | Auto-restart, invisible to AI client |
| Add new server | Edit JSON, restart session | `POST /api/servers` or click in VS Code |
| Remove server | Edit JSON, restart session | `DELETE /api/servers/name` or click |
| Check what's running | `cat ~/.claude.json`, guess | `GET /api/servers` — live status with health |
| Too many tools in context | All tools always loaded | Namespace filtering, tool budgets, disable on the fly |
| Debug failing MCP | Read logs manually | `GET /api/servers/name/logs` — streaming SSE |

## Architecture

All MCP transports supported — both for serving clients and connecting to backends:

```
FRONTEND (clients connect to gateway)     BACKEND (gateway connects to servers)

  stdio ────────┐                          ┌──── stdio (child process)
  HTTP  ────────┤    ┌──────────────┐      │     orchestrator, pal-mcp
  SSE   ────────┼──> │  MCP Gateway │ ─────┤
  REST  ────────┤    │    :8765     │      ├──── HTTP (Streamable HTTP)
                │    └──────────────┘      │     context7, pdap-docs
                │                          │
                │    /mcp  Streamable HTTP  ├──── SSE (Server-Sent Events)
                │    /sse  SSE transport         vsp-DEV-100, sap-gui-control
                │    /api  REST management
                │    stdio native MCP
```

**Key property:** ONE daemon, all MCP transports (stdio + HTTP + SSE) + REST API — on both frontend and backend. Servers behind it come and go — clients never know. Works even when the client's native MCP is broken.

**Backend flexibility:** A server can expose MCP, REST, or both. Gateway uses MCP for tool calls, REST for deep health checks and API proxying. REST-only backends can be wrapped as MCP tools automatically.

## Components

| Component | Language | Purpose |
|-----------|----------|---------|
| `cmd/mcp-gateway/` | Go | Daemon entry point |
| `cmd/mcp-ctl/` | Go | CLI entry point (`mcp-ctl`) |
| `internal/` | Go | Shared packages — lifecycle, health, proxy, config, router |
| `vscode/mcp-gateway-dashboard/` | TypeScript | VS Code extension — tree view, status bar, one-click management |

## Key Architectural Decisions

1. **Gateway as sole MCP entry point** — not a peer manager. Gateway owns stdio backends as child processes, connects to HTTP/SSE backends as clients.
2. **Live management via REST API** — add/remove/enable/disable/restart servers without touching config files or restarting sessions. The API is the primary interface; config file is just initial state.
3. **Real-time health as first-class feature** — every server has a state machine (`stopped` -> `starting` -> `running` -> `degraded` -> `error` -> `restarting` -> `disabled`). Status is always available via API and VS Code UI.
4. **tools/list is cached per session in Claude Code** (Issue #13646) — gateway bypasses this by being the only server. New backends' tools appear via gateway's dynamic `tools/list`.
5. **Go for daemon** — single binary, zero dependencies, instant startup (<10ms), goroutines for parallel process management.
6. **TypeScript for VS Code extension** — native VS Code API, tree view with live status, status bar with health counts.
7. **On-the-fly config** — config file watcher detects changes; REST API writes propagate to config automatically. Two-way sync: file -> runtime and runtime -> file.

## Status

**v1.0.0** — all core features complete and tested.

See [CHANGELOG.md](CHANGELOG.md) for details.

## Installation

### Installer script (recommended)

```bash
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/blicksten/mcp-gateway/main/installer/install.sh | bash

# Windows (PowerShell)
irm https://raw.githubusercontent.com/blicksten/mcp-gateway/main/installer/install.ps1 | iex
```

The installer downloads pre-built binaries, verifies SHA-256 checksums, installs to `~/.local/bin`, and registers a user-level service (systemd on Linux, LaunchAgent on macOS, Scheduled Task on Windows).

> Inspect before running: download the script first and review it. The installer itself is not signed — only release binaries are.

### Binary download

Download the archive for your platform from [GitHub Releases](https://github.com/blicksten/mcp-gateway/releases). Verify the checksum before extracting:

```bash
sha256sum -c checksums.txt  # Linux
shasum -a 256 -c checksums.txt  # macOS
```

Extract and place the binaries in your PATH.

### Build from source

```bash
go install ./cmd/mcp-gateway
go install ./cmd/mcp-ctl
```

## Verification

Release checksums are signed with [Sigstore cosign](https://docs.sigstore.dev/) (keyless). To verify:

```bash
cosign verify-blob --bundle checksums.txt.bundle \
  --certificate-identity-regexp 'https://github.com/blicksten/mcp-gateway/.github/workflows/release.yml@refs/tags/v.*' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  checksums.txt
```

## Quick Start

```bash
# Start the gateway daemon
mcp-gateway

# Check health
mcp-ctl health

# List servers and tools
mcp-ctl servers list
mcp-ctl tools list

# Add a server on the fly
mcp-ctl servers add my-server --command /usr/local/bin/my-mcp-server

# Operational metrics
curl http://127.0.0.1:8765/api/v1/metrics
```

## VS Code Extension

The extension provides a visual interface for managing MCP servers.

**Install from source:**

```bash
cd vscode/mcp-gateway-dashboard
npm install && npm run compile
npx @vscode/vsce package --allow-missing-repository
code --install-extension mcp-gateway-dashboard-1.0.0.vsix
```

**Features:**
- Activity Bar icon with "Backends" and "SAP Systems" tree views
- Status bar showing running/total server counts
- One-click server management: enable, disable, restart, remove
- Live SSE log streaming per server
- Webview detail panels with server config and tools
- Credential management via OS keychain
- Auto-start daemon when VS Code opens

**Settings:**

| Setting | Default | Description |
|---------|---------|-------------|
| `mcpGateway.apiUrl` | `http://localhost:8765` | Gateway REST API URL |
| `mcpGateway.autoStart` | `true` | Auto-start daemon on VS Code launch |
| `mcpGateway.daemonPath` | `""` | Path to `mcp-gateway` binary (empty = use PATH) |
| `mcpGateway.pollInterval` | `5000` | Status polling interval (ms) |

## CLI Reference

```
mcp-ctl [--api-url URL] <command>

  health (alias: status)          Gateway health status
  servers list [--json]           List all backends
  servers get <name> [--json]     Show backend details
  servers add <name> --command <cmd> [--args a,b] [--cwd dir] [--env K=V]
  servers add <name> --url <url>  Add HTTP/SSE backend
  servers remove <name> [--force] Remove backend
  servers enable <name>           Enable disabled backend
  servers disable <name>          Disable running backend
  servers restart <name>          Restart backend
  servers reset-circuit <name>    Reset circuit breaker
  tools list [--server <name>]    List all tools
  tools call <tool> [--arg k=v]   Call a tool
  logs <name> [--no-reconnect]    Stream backend logs (SSE)
  validate --command <cmd>        Test MCP server compliance
  credential import-kdbx <file>   Import from KeePass
  version                         Print version info

Environment: MCP_GATEWAY_URL overrides default API URL (flag takes precedence)
Exit codes: 0 = success, 1 = error, 2 = gateway unreachable
```

## REST API

All endpoints under `/api/v1/`. Backward-compatible redirect from `/api/*`.

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/health` | `{status, servers, running}` |
| GET | `/api/v1/servers` | List all backends |
| GET | `/api/v1/servers/{name}` | Backend details |
| POST | `/api/v1/servers` | Add backend `{name, config}` |
| DELETE | `/api/v1/servers/{name}` | Remove backend |
| PATCH | `/api/v1/servers/{name}` | Update `{disabled, env, headers}` |
| POST | `/api/v1/servers/{name}/restart` | Restart backend |
| POST | `/api/v1/servers/{name}/reset-circuit` | Reset circuit breaker |
| POST | `/api/v1/servers/{name}/call` | Call tool `{tool, arguments}` |
| GET | `/api/v1/tools` | List all exposed tools |
| GET | `/api/v1/metrics` | Operational metrics |
| GET | `/api/v1/servers/{name}/logs` | SSE log stream |

## Configuration

Default config path: `~/.mcp-gateway/config.json` (auto-created on first run).

```json
{
  "gateway": {
    "http_port": 8765,
    "bind_address": "127.0.0.1",
    "ping_interval": "30s",
    "compress_schemas": false,
    "tool_filter": {
      "per_server_budget": 0,
      "consolidate_excess": false,
      "tool_budget": 0
    }
  },
  "servers": {
    "my-server": {
      "command": "/usr/local/bin/my-mcp-server",
      "args": ["--verbose"],
      "env": ["API_KEY=${MY_API_KEY}"]
    }
  }
}
```

**Environment variable expansion:** Use `${VAR}` in config strings. Variables are resolved from `.env` file (via `--env-file` flag) with restricted `os.Getenv` fallback (only safe vars like `HOME`, `USER`, `TMPDIR`).

**Hot-reload:** Config file changes are detected automatically. No daemon restart needed.

**Local overrides:** Place `config.local.json` next to `config.json` for machine-specific settings (not committed to git).

## Metrics

`GET /api/v1/metrics` returns per-server crash counts, MTBF, uptime, and token cost estimates.

| Field | Description |
|-------|-------------|
| `restart_count` | Process restarts (resets on circuit breaker reset) |
| `mtbf` | Mean time between failures (`"0s"` = no failures) |
| `uptime` | Current continuous uptime |
| `est_schema_tokens` | Approximate schema token count (`rune_count / 4`) |
| `est_total_tokens` | Schema + description token estimates |

## Security

- **Localhost by default:** Binds to `127.0.0.1:8765`. Non-loopback requires `allow_remote: true`.
- **CSRF protection:** `Sec-Fetch-Site` header validated on mutating requests.
- **Rate limiting:** 100 concurrent requests, 200 backlog, 30s timeout.
- **Body size limit:** 1 MB max.
- **SSE connection limit:** Max 20 concurrent log streams.
- **Env key blocklist:** 25+ dangerous keys (`LD_PRELOAD`, `DYLD_INSERT_LIBRARIES`, etc.) rejected.
- **Atomic config writes:** Temp file + rename to prevent corruption.

## Building from Source

**Requirements:** Go 1.25+, Node.js 20+ (for extension)

```bash
git clone https://github.com/blicksten/mcp-gateway.git
cd mcp-gateway

# Build daemon + CLI
go build ./...
go install ./cmd/mcp-gateway
go install ./cmd/mcp-ctl

# Run tests
go test ./...

# Build VS Code extension
cd vscode/mcp-gateway-dashboard
npm install && npm run compile
```

## Contributing

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/my-feature`)
3. Make your changes
4. Run tests (`go test ./...`)
5. Commit (`git commit -m 'feat: add my feature'`)
6. Push to your fork (`git push origin feature/my-feature`)
7. Open a Pull Request

Please follow [Conventional Commits](https://www.conventionalcommits.org/) for commit messages.

## License

[MIT](LICENSE)
