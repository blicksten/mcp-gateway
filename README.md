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
| `mcpGateway.catalogPath` | `""` | Optional override path to a catalog directory (see [Catalogs](#catalogs)). Machine-scope. When empty, the bundled catalog that ships with the extension is used. |

## Catalogs

The extension ships with a first-party catalog of popular MCP servers and matching slash-command templates. The catalog drives two UX surfaces:

1. **Add Server → "Choose from catalog" dropdown.** Operators pick a catalog entry and the form pre-fills `transport`, `url` / `command` / `args`, and one empty row per declared `env_keys` / `header_keys` — the operator fills in only the secret VALUE, never the key structure.
2. **Slash-command templates.** When a catalog-known server transitions to `running`, `SlashCommandGenerator` writes the catalog's `template_md` body into `.claude/commands/<server>.md` (substituting only the `${server_name}` / `${server_url}` allow-list). Servers without a catalog entry keep the previous bare skeleton.

**Catalogs are local files only — the extension never fetches catalog data from the network.** JSON Schema `$id`s (`https://mcp-gateway.dev/schema/catalog/*.v1.json`) are used as version keys only; validators are pre-configured with the bundled schema files.

**Catalog layout** — two JSON arrays beside each other:

```
<catalog-dir>/
├── servers.json   — server entries (schema.server.json v1)
└── commands.json  — command entries (schema.command.json v1)
```

**Example `servers.json` entry:**

```json
{
  "name": "context7",
  "display_name": "Context7 Documentation",
  "transport": "http",
  "description": "Up-to-date library documentation lookup.",
  "url": "https://mcp.context7.com/mcp",
  "header_keys": ["Authorization"],
  "homepage": "https://context7.com",
  "tags": ["docs", "research"]
}
```

**Example `commands.json` entry:**

```json
{
  "server_name": "context7",
  "command_name": "docs",
  "description": "Look up current documentation via ${server_name}.",
  "template_md": "# /${server_name}-docs <library>\n\nFetch docs from ${server_url}.\n",
  "suggested_vars": ["library", "topic"]
}
```

Every `server_name` in `commands.json` must resolve to an entry in `servers.json` — enforced by `npm run lint:catalog`.

### Operator override

`mcpGateway.catalogPath` (machine-scope) points at a directory containing a custom `servers.json` + `commands.json` pair. Operator path wins when non-empty AND the directory exists; otherwise the extension falls back to the bundled catalog under its installation directory. The setting is scoped `machine` so catalog selection cannot be overridden per workspace (closes a per-workspace exfiltration vector).

### Hard limits

- Each catalog file is capped at **1 MiB**. Larger files are refused at load time with a warning; `readFile` is never invoked.
- Schemas are pinned to `$id` major version `v1`. Any document whose `$id` does not match `v1.*` is rejected.
- The loader never throws — malformed JSON, schema mismatch, or oversized files produce warnings and an empty entry list, so the rest of the panel keeps working.

### Known limitation — slash-command edits below line 1

Catalog-enriched slash-command files carry a magic-header marker on line 1. When the server re-transitions to `running`, the file is regenerated in full and any edits **below** line 1 are silently overwritten. To preserve operator edits, delete the line-1 marker — the generator treats markerless files as operator-owned and leaves them alone. A hash-augmented marker that tolerates below-line-1 edits is a v1.6 candidate.

## Connecting Claude Code to the Gateway

> Available in v1.6.0+.

Two-line install (requires the gateway daemon to be running):

```bash
mcp-ctl install-claude-code --mode proxy
# Open Claude Code. In the /mcp panel you should see
# `plugin:mcp-gateway:<backend>` entries for every registered backend.
```

What the installer does:

1. Verifies the gateway is running (`GET /api/v1/health`).
2. Reads `~/.mcp-gateway/auth.token`.
3. Runs `claude plugin marketplace add <repo>/installer/marketplace.json`
   (idempotent) and `claude plugin install mcp-gateway@mcp-gateway-local`.
4. POSTs `/api/v1/claude-code/plugin-sync` to regenerate `.mcp.json` with
   the current backend list.
5. Unless `--no-patch`, applies `installer/patches/apply-mcp-gateway.sh`
   (or `.ps1` on Windows) to enable automatic reconnect on backend
   changes. The patch walks Claude Code's React fiber tree to capture a
   reference to `session.reconnectMcpServer` — the same native method
   Claude Code's `/mcp` panel "Reconnect" button calls.

**Dry-run** first if you want to see the plan without writes:

```bash
mcp-ctl install-claude-code --dry-run
```

**Auto-reload opt-in.** The webview patch is optional. What it does:

- Listens for reconnect actions the gateway enqueues after `POST
  /api/v1/servers` mutations.
- Calls `session.reconnectMcpServer("mcp-gateway")` so Claude Code picks
  up the new backend list without a restart.
- Modifies `~/.vscode/extensions/anthropic.claude-code-*/webview/index.js`
  in place with a backup (`index.js.bak`). File mode locked to 0600 on
  POSIX; DACL-restricted on Windows.

**Manual path (patch declined).** You can skip the patch with
`--no-patch`. After adding a backend, open Claude Code's `/mcp` panel →
right-click the `mcp-gateway` entry → **Reconnect**. Claude Code 2.1.114
does NOT ship a `/reload-plugins` slash command; the per-server
Reconnect action in the `/mcp` panel UI is the native primitive and is
what the auto-reload patch calls programmatically under the hood.

**Uninstall:**

```bash
mcp-ctl uninstall-claude-code
# or, manually:
claude plugin uninstall mcp-gateway
bash installer/patches/apply-mcp-gateway.sh --uninstall
```

**Dashboard shortcut.** The VSCode extension exposes the same flow
via the command palette → `MCP Gateway: Show Claude Code Integration`.
The panel displays plugin + patch + channel status, a 12-mode failure
matrix, and `[Activate]` / `[Probe reconnect]` / `[Copy diagnostics]`
buttons.

## Commands vs MCP servers

Two different things live under `.claude/`, both are markdown, both are
managed by mcp-gateway — and they are NOT interchangeable:

- **`.claude/commands/*.md`** are **prompt templates** — slash-command
  helpers for the user (e.g. `/context7`). They are NOT MCP server
  registrations. The mcp-gateway extension auto-generates these from
  registered backends with an AUTO-GENERATED marker + disclaimer on the
  first three lines.
- **`claude plugin install mcp-gateway@mcp-gateway-local`** is the MCP
  registration path — it registers our plugin with Claude Code's MCP
  client so backends show up in the `/mcp` panel and `tools/list` cache.

See the [Claude Code plugin docs](https://docs.claude.com/en/docs/claude-code/)
for the authoritative distinction between slash-command plugins and MCP
plugins.

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

- **Bearer token authentication (v1.2+):** Every mutating `/api/v1` endpoint, sensitive reads (`/logs`), and (optionally) MCP transports require `Authorization: Bearer <token>`. The daemon auto-generates a 32-byte base64url token at first start and persists it at `~/.mcp-gateway/auth.token` with POSIX `0600` / Windows DACL (current-user-only ALLOW ACE, deny-by-default). Override with `MCP_GATEWAY_AUTH_TOKEN` env var. Full policy matrix: [docs/ADR-0003](docs/ADR-0003-bearer-token-auth.md).
- **TLS (v1.3+):** Optional TLS via `gateway.tls_cert_path` + `gateway.tls_key_path`. **Non-loopback bind with Bearer auth enabled refuses to start without TLS** — cleartext tokens on public networks are not possible by design.
- **Localhost by default:** Binds to `127.0.0.1:8765`. Non-loopback requires `allow_remote: true`.
- **MCP transport policy:** `gateway.auth_mcp_transport=loopback-only` (default) rejects non-loopback MCP clients with 403 and denies cross-site browser-originated POSTs via `Sec-Fetch-Site`. `bearer-required` mode applies Bearer auth to `/mcp` and `/sse`.
- **CSRF protection:** `Sec-Fetch-Site` header validated on mutating `/api/v1` requests (auth runs **before** csrf for cheap 401 short-circuits).
- **Rate limiting:** 100 concurrent requests, 200 backlog, 30s timeout. SSE `/logs` has its own 20-connection throttle with auth-before-throttle so unauthenticated clients cannot exhaust the budget.
- **Body size limit:** 1 MB max.
- **Env key blocklist:** 25+ dangerous keys (`LD_PRELOAD`, `DYLD_INSERT_LIBRARIES`, etc.) rejected.
- **Atomic config writes:** Temp file + rename to prevent corruption.
- **Log redaction (v1.3+):** Child-process stderr streamed through a redaction pipeline before entering the log ring, SSE `/logs` stream, or on-disk log. Matches: Authorization Bearer headers, bare `Bearer X`, `api_key=/access_token=/secret_key=/password=`, AWS access keys (`AKIA*`), GitHub PATs (`ghp_/gho_/ghu_/ghs_/ghr_`), JWTs, and 32+ char base64url blobs. Context-bearing patterns preserve the field name (`Authorization: Bearer ***REDACTED***`) so operators retain diagnostic value.
- **POSIX process groups (v1.3+):** Child processes run in their own process group; the daemon sends SIGTERM/SIGKILL to the group so grandchildren are reaped.

### KeePass credential import (v1.2+)

Operators can import credentials from a KDBX file directly into the extension's SecretStorage:

1. Set `mcpGateway.keepassPath` (and optionally `mcpGateway.keepassGroup`) in VS Code settings.
2. Run **MCP Gateway: Import KeePass Credentials** from the command palette.
3. Enter the master password in the VS Code prompt.

Behind the scenes, the extension spawns `mcp-ctl credential import --json --password-stdin` with an argv-array exec (no shell), pipes the password via stdin, and parses the stable JSON contract (`{version:1, servers:[...]}`). Credentials land in OS keychain via SecretStorage with partial-failure tolerance (one malformed entry does not block the rest).

### Reporting security vulnerabilities

See [SECURITY.md](SECURITY.md) — private reporting via GitHub Security Advisories is preferred.

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

### Testing tiers

The project has three tiers of automated tests, separated by what they prove
and what they need to run:

| Tier | Command | Scope |
|------|---------|-------|
| **Unit + structural** | `go test ./...` | Default path. Covers all platforms. On Windows, verifies the token-file DACL shape (Protected, single ALLOW ACE for current user). No external prereqs. |
| **TLS integration** | `go test ./...` (subset) | Runs as part of the default path. Generates a self-signed cert in `t.TempDir()`, drives `ListenAndServeTLS`, asserts half-configured TLS refuses to start. |
| **Windows DACL enforcement** | `make test-integration-windows` | Requires Windows + a pre-provisioned local test account (`net user /add`). Uses `LogonUser` + `ImpersonateLoggedOnUser` to confirm the token file is OS-denied to a second account, not just structurally correct. |

**Assurance levels for operators:**

- **Windows:** the default `go test ./...` path proves the token-file DACL is
  *shaped* correctly (Protected, single ALLOW ACE, current user only). The
  `make test-integration-windows` path additionally proves the DACL is
  *enforced* by the Windows kernel against a second local account — i.e., a
  real second user cannot read the token even with the shape intact. Run the
  enforcement tier once per release if the token-file isolation guarantee is
  load-bearing for your threat model; the default path alone is enough for
  routine development.
- **Linux / macOS:** the token file is created with POSIX `0600` (owner
  read/write only) at atomic-rename time — kernel-enforced by the filesystem,
  verified structurally at daemon start. There is no separate enforcement-tier
  test on POSIX platforms because the POSIX permission bits are the kernel
  enforcement, not a shape-vs-enforcement layering.

Operator protocol for the Windows enforcement tier (elevated PowerShell
required for `net user /add`; run the commands below in a single elevated
PowerShell session):

```powershell
net user mcpgwtestuser 'Pass1234!MCPGW' /add
$env:MCPGW_TEST_USER = 'mcpgwtestuser'
$env:MCPGW_TEST_PASSWORD = 'Pass1234!MCPGW'
make test-integration-windows
net user mcpgwtestuser /delete
```

Behavior when `MCPGW_TEST_USER` / `MCPGW_TEST_PASSWORD` are absent:

- `go test ./...` — unaffected. The enforcement test file is behind the
  `integration` build tag and is not compiled in this path.
- `go test -tags integration ./...` — the enforcement test calls `t.Skip`
  with a pointer back to this section. The rest of the integration-tagged
  tests still run.
- `make test-integration-windows` — fails fast with a non-zero exit code
  before invoking `go test`. This is deliberate: an operator explicitly
  running the manual protocol shouldn't get a silent pass when the
  credentials aren't set.

See `docs/spikes/2026-04-19-windows-latest-impersonate.md` for why this
tier is operator-driven rather than wired into GitHub Actions in v1.5.0.

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
