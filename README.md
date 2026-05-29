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
| **MCP reconnects every 44s** | Unreachable backend causes HTTP 400 → Claude Code retries all transports | Backend keeps empty stub → returns HTTP 200 → no retries |
| **MCP disconnects every 11min** | WriteTimeout fires on long-lived SSE streams | Per-connection deadline cleared for GET — connections stay open indefinitely |

## Why It Matters at Scale

Classical stdio MCP spawns a dedicated subprocess tree **for every Claude Code CLI process**. With the Claude Code VSCode extension behavior observed around 2026-04 — each chat tab is its own CLI process — 3 windows with 5 + 8 + 3 = **16 tabs** and 6 MCP servers configured means **96 subprocesses** running in parallel. Under this load operators typically hit three failure modes: memory swap, stdio-pipe deadlocks, and long MCP cold-starts when opening a new tab.

Gateway keeps **one** subprocess per backend on the whole machine and proxies all 16 tabs through it over HTTP:

| Metric | Classical (16 tabs × 6 servers) | Gateway (1 per backend) | Reduction |
|--------|--------------------------------:|------------------------:|----------:|
| MCP subprocesses | 96 | 6 (+ 1 daemon) | **−93%** |
| Warm RSS (~200 MB avg) | ~19.2 GB | ~1.2 GB | **−94%** |
| File descriptors | ~288 | ~20 | **−93%** |
| Cold-start per new tab | 6 process spawns | 0 (already warm) | 100% |
| Disk I/O on tab open | 6× read `node_modules`/venv | 0 | 100% |

### Concurrent-call safety

16 tabs issuing tool calls simultaneously do **not** interfere with each other. Four independent multiplexing layers keep requests isolated:

1. **Client ↔ Gateway (HTTP Streamable)** — each tab holds its own HTTP connection with its own `mcp-session-id`.
2. **Gateway internal routing** — lock-free on the hot path; mutexes only engage during config reload.
3. **Gateway ↔ Backend** — a single JSON-RPC 2.0 session multiplexes all calls by request id; out-of-order backend responses route correctly.
4. **Inside the backend process** — the only real bottleneck. Async backends (Python/Node asyncio) process concurrently; single-threaded backends (Playwright, KeePass) serialize inside the process — gateway consolidates the queue but does not change the serial constraint.

### Disconnect resilience

Operator pain with classical mode: **a crashed MCP server is gone until the VSCode window is reloaded**. High-frequency servers like a pipeline orchestrator or a cross-model reviewer (PAL) fail often under memory pressure; when they die, automated flows silently proceed without their gates.

Gateway runs an active health monitor (`internal/health/monitor.go`): periodic ping, 3 consecutive failures → auto-restart, 5 restarts in 300 s → circuit breaker opens, 60 s stuck-restart timeout. **Clients never see the backend go away** — the client's HTTP session to gateway stays alive across backend restarts; at most one in-flight tool call returns an error and the next call hits the fresh backend transparently. The circuit breaker is one-way — once it opens, the backend is marked `Disabled` and stays there until an operator resets it via `mcp-ctl servers reset-circuit <name>` or the dashboard Reset action (this is by design: a genuinely flapping backend should not silently self-reset into another burst of failures). On Windows a Job Object guarantees child-process reaping on daemon exit — no zombie subprocesses.

Largest single operational win: **stateful infrastructure servers** — orchestrators, review/audit services, persistent session managers — become reliably available where classical stdio would flap under memory pressure.

**When a backend is temporarily unreachable** (e.g., VPN-dependent server with VPN off): the gateway keeps an empty stub for the backend. Claude Code sees the endpoint as "available with no tools" rather than "failed" — preventing the 44-second reconnect storm that would otherwise affect all active sessions. Once the backend becomes reachable again, the gateway reconnects automatically on the next health-monitor cycle.

**`StatusUnreachable` — TCP-level vs protocol-level distinction.** Connection failures split into two buckets so transient network outages don't trigger the same crash-loop response as a genuinely broken backend:

- **TCP-unreachable** (host down, DNS fail, `ECONNREFUSED`, `ENETUNREACH`, `EHOSTUNREACH`, Windows `connectex` no-route, dial timeout): classified by `internal/lifecycle/transport.go::IsTransportUnreachable`, routed to a new `StatusUnreachable` state. The supervisor returns `ErrDoNotRestart` — no exponential backoff, no restart counter increment. A slow-poll loop in `internal/health/monitor.go` (`maybeProbeUnreachable`) checks reachability every 60 s; the moment a TCP probe succeeds the backend transitions back to `StatusStarting` automatically. UI surfaces a yellow warning badge ("host offline — slow-polling every 60s") instead of the red error spinner.
- **Protocol-level** failures (HTTP 4xx/5xx, TLS handshake error, MCP rejection, malformed JSON): the existing `StatusError` path applies — exponential backoff restart, circuit breaker after 5 in 300 s.

Triggering case that drove this split: `pdap-docs` (HTTP backend at `10.149.207.124`, VPN-gated) previously cycled `StatusStarting → StatusError` continuously when the VPN was off, burning the restart budget on a network condition the gateway cannot fix. With `StatusUnreachable`, the backend sits quietly waiting for the host to come back; no operator action needed.

### Trade-offs

- **Stateful backends shared across tabs.** If a backend keeps per-session state (Playwright browser, shell with `cwd`), all tabs share that state. Define named instances in the gateway config — `Servers` is a name-keyed map, so two entries `playwright-main` / `playwright-aux` with the same command and distinct `args`/`cwd`/`env` (e.g. separate `--user-data-dir`) are supported today — or leave stateful backends in classical `.mcp.json` alongside gateway (hybrid mode is fully supported).
- **Gateway is a single point of failure.** If the daemon itself crashes, all tabs lose MCP at once. Gateway is a thin Go proxy with a small crash surface; supervise it with systemd/launchd/Windows service.
- **Security surface concentrates.** 96 subprocesses collapse to one gateway process — fewer listening surfaces, but any breach of the gateway's auth (Bearer token, loopback binding) now affects every backend. Keep the default loopback-only binding, audit token-file permissions, and review the auth policy matrix in [`docs/ADR-0003-bearer-token-auth.md`](docs/ADR-0003-bearer-token-auth.md) before exposing the gateway beyond `127.0.0.1`.
- **Circuit breaker is explicit and one-way.** If a backend flaps badly, gateway returns clear "server unavailable" errors instead of hiding the state, and it keeps the backend disabled until you reset it — by design, so automated workflows don't silently proceed with a broken gate and a flapping backend doesn't cycle between restart bursts.

Full analysis including code references and empirical validation plan: [`docs/spikes/2026-04-24-scalability-vs-classical-mcp.md`](docs/spikes/2026-04-24-scalability-vs-classical-mcp.md).

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
3. **Real-time health as first-class feature** — every server has a state machine (`stopped` -> `starting` -> `running` -> `degraded` -> `error` -> `restarting` -> `disabled` -> `unreachable`). Status is always available via API and VS Code UI. (`unreachable` covers TCP-level connection failures — host down / DNS fail / dial timeout — separated from protocol-level `error` so transient network outages don't burn the restart budget; see Disconnect resilience below.)
4. **tools/list is cached per session in Claude Code** (Issue #13646) — gateway bypasses this by being the only server. New backends' tools appear via gateway's dynamic `tools/list`.
5. **Go for daemon** — single binary, zero dependencies, instant startup (<10ms), goroutines for parallel process management.
6. **TypeScript for VS Code extension** — native VS Code API, tree view with live status, status bar with health counts.
7. **On-the-fly config** — config file watcher detects changes; REST API writes propagate to config automatically. Two-way sync: file -> runtime and runtime -> file.

## Status

**Daemon v1.9.0 + Extension 1.33.21** (Wave 2 of `sap-picker-and-import-mcp` + SAP Picker stabilization cycle)
— SAP Picker (Wave 1) and Import-from-Claude (Wave 2) complete on top of
the v1.7.x daemon-lifecycle and v1.6.x Claude Code integration baselines.

The two semver tracks are independent: `mcp-gateway` daemon is
ldflags-versioned (visible via `mcp-ctl version`); the VSCode extension
is `package.json`-versioned. Wave 1 git tag jumped from the legacy
`v1.0.0` to **`v1.8.0`** to align with the embedded daemon version
users have been seeing all along (PLAN-sap-picker-and-import-mcp.md
§C OQ-5 records the rationale).

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

**Important for operators who build from source:** `go build -o mcp-gateway.exe ./cmd/mcp-gateway` writes to the current directory but does NOT update your installed binary. To update the binary used by `mcp-ctl daemon restart`, run `go install ./cmd/mcp-gateway` or copy the workspace binary to `$GOPATH/bin`. The VS Code extension uses `mcpGateway.daemonPath` (VSCode settings) to locate the daemon — if set to the workspace path, it picks up local builds automatically.

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
- One-click server management: enable, disable, restart, remove, **rename**
- Live SSE log streaming per server
- Webview detail panels with server config and tools
- Credential management via OS keychain
- Auto-start daemon when VS Code opens

### Renaming a server

Right-click a non-SAP server in the **MCP Backends** tree view → **Rename Server**. Enter the new name in the input box (1–64 alphanumeric/hyphen/underscore chars; cannot collide with an existing server, cannot be SAP-shaped). Confirm the modal that summarises what gets carried over (env vars, headers, secrets). The rename then:

1. Calls `PATCH /api/v1/servers/{old}` with `{"new_name":"{new}"}`. The gateway runs Plan A: lifecycle add under new name → lifecycle remove old name with `context.Background()` rollback on failure → atomic `cfgMu`-protected map swap → auto-start under new name (warn-only) → `RebuildTools` + `TriggerPluginRegen`. Returns `{"status":"patched","old_name":"{old}","new_name":"{new}"}`. The propagation channel for clients is `RebuildTools` (see [docs/spikes/2026-05-08-mcp-server-routing-bypasses.md](docs/spikes/2026-05-08-mcp-server-routing-bypasses.md) §F1).
2. Migrates extension-side secrets: every entry under `mcpGateway/{old}/env/*` and `mcpGateway/{old}/headers/*` is copied to `mcpGateway/{new}/*` then deleted from the old key, with **index-first ordering** inside the credential-store mutex so a mid-rename crash leaves the new-name index entry pointing at partially-migrated secrets — recoverable by `reconcile()` on next extension activation.
3. Refreshes the cache so `~/.claude.json::mcpServers` reflects the new namespaced name (`mcp-gateway:{new}`) via the TS-side reflector ([docs/spikes/2026-05-09-reflector-coordination.md](docs/spikes/2026-05-09-reflector-coordination.md)).

**SAP servers** (names matching the YAML grammar `docs/grammar/sap-server-name.yaml` — `vsp-{SID}(-{Client})?` or `sap-gui-{SID}(-{Client})?`) are refused at API (400 `"renaming SAP-named servers is not supported"`) and in the UI (hidden from the context menu via the `viewItem` regex, and rejected by the input-box validator if invoked from the command palette). SAP names encode SID/client and renaming would break the SAP detector.

**Plan A rollback semantics.** If the rename fails after the gateway has already registered the new name in lifecycle but before the cfg-map swap commits, the gateway invokes a rollback `lm.RemoveServer({new})` with `context.Background()` (not the request context — which may itself be cancelled, which is the reason the original `RemoveServer({old})` failed). On rollback success, returns HTTP 500 with `"rename failed at remove stage (rolled back): ..."` and the cfg-map plus on-disk config show the OLD name. If the rollback also fails, both errors are logged at ERROR level (operator must reconcile manually — extremely rare).

**Credential-migration failure path.** If the gateway PATCH succeeds but `credentialStore.renameServerCredentials` throws (e.g. `SecretStorage unavailable` from a locked keychain), the extension shows a warning toast: *"Server renamed to '{new}' but {N} credential(s) could not be migrated. They remain under '{old}' in the keychain. Re-import KeePass or re-enter them manually."* — the gateway rename **is not rolled back** (it has already succeeded), and `cache.refresh()` still fires. The operator either re-imports from KeePass or re-enters secrets manually under the new name.

**REST equivalents** (CLI / scripting):

```bash
# Rename a server (gateway-side only — credentials remain under old name in the keychain)
curl -X PATCH http://localhost:8765/api/v1/servers/ctx7 \
     -H "Content-Type: application/json" \
     -d '{"new_name":"context7-prod"}'
# Combined rename + env update (atomic)
curl -X PATCH http://localhost:8765/api/v1/servers/ctx7 \
     -H "Content-Type: application/json" \
     -d '{"new_name":"context7-prod","add_env":["TIMEOUT=30s"]}'
```

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

## SAP Picker

> Available in v1.8.0+ (Wave 1 of `docs/PLAN-sap-picker-and-import-mcp.md`).

The **SAP Picker** webview replaces the per-system "Add SAP System" form
with a hybrid landscape ∪ KeePass picker. It enumerates every SID found
in `SAPUILandscape.xml`, joins it with KeePass entries, and lets the
operator register or unregister `vsp-<SID>-<CLIENT>` and
`sap-gui-<SID>-<CLIENT>` backends in a single batch.

**Open it:** command palette → `MCP Gateway: Open SAP Picker`, or click the
`[⊞]` icon at the top of the **SAP Systems** view.

**What it does:**

- Reads `SAPUILandscape.xml` via a regex-free `encoding/xml` parser
  (`internal/saplandscape/parser.go`) — handles `<Include>` chains with
  cycle detection, UNC normalisation, and `%APPDATA%`/`%USERPROFILE%`
  expansion.
- Reads KeePass via `gokeepasslib/v3` and joins on `(SID, Client)`.
  Recycle-bin entries are filtered out (`internal/sapcreds/keepass.go`).
- Hybrid join: every landscape SID appears, with `kpMissing: true` flag
  when the KeePass DB has no matching entry (R-14 / R-30 — registration
  blocked at the UI for `kpMissing` rows; tooltip points at
  `mcp-ctl credential import`).
- Batch Apply suppresses per-call `TriggerPluginRegen` and fires a
  single regen + `RebuildTools` at end-of-batch (R-26 / X2 fix —
  `addServerInProcess` / `removeServerInProcess` refactor in
  `internal/api/server.go`). Per-row status surfaces in a 9-state
  machine (`pending` → `in_progress` → `config_added_running` /
  `config_added_start_failed` / `removed` / `removed_with_orphan` / …).
- `removed_with_orphan` rows expose a **Force kill** button — surfaces
  Stop errors that classical lifecycle code silently swallowed (R-28 /
  X4 fix — `internal/lifecycle/manager.go::RemoveServer` now returns
  `RemoveResult{Orphan, StopErr}`).

**Settings used:**

| Setting | Default | Description |
|---------|---------|-------------|
| `mcpGateway.sapSystemsEnabled` | `false` | Show the SAP Systems view + enable the picker entry point. |
| `mcpGateway.keepassPath` | `""` | Path to the KDBX file. Required for the join to surface non-`kpMissing` rows. |
| `mcpGateway.defaultVspCommand` | `""` | Default `command` for `vsp-*` backends — pre-fills the override field. |
| `mcpGateway.defaultGuiUvProject` | `""` | Default `--directory` for `sap-gui-*` backends when `defaultGuiMode = "uv"`. |
| `mcpGateway.defaultGuiMode` | `"exec"` | `"exec"` (raw command) or `"uv"` (resolves through `uvx --directory <project>`). |
| `mcpGateway.uvPath` | `""` | Path to `uv` / `uvx`. Empty = use PATH. |

REST contract (FROZEN as of v1.8.0):

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/api/v1/sap/picker-snapshot` | Joined landscape ∪ KeePass rows |
| POST | `/api/v1/sap/batch-begin` | Open a 5-minute batch window; returns `{batch_id}` |
| POST | `/api/v1/sap/batch-end` | Close the batch + fire single regen |

## Import-from-Claude

> Available in v1.9.0+ (Wave 2 of `docs/PLAN-sap-picker-and-import-mcp.md`).

The **Import from Claude** webview ingests MCP server entries from
Claude Code and Claude Desktop config files into the gateway. Read-only
on the gateway side; the daemon never writes to Claude's config files
unless the operator picks `move` action.

**Open it:** command palette → `MCP Gateway: Open Import from Claude`.

**What it imports:**

| Source | Path |
|--------|------|
| `cc_global` | `~/.claude.json` (`mcpServers` key) |
| `cc_project` | `<workspace>/.mcp.json` (`mcpServers` key) |
| `desktop` | `%APPDATA%\Claude\claude_desktop_config.json` (Windows) · `$HOME/Library/Application Support/Claude/claude_desktop_config.json` (macOS) · `$XDG_CONFIG_HOME/Claude/claude_desktop_config.json` (Linux) |

**Action / conflict matrix:**

| Action | Conflict = `skip` | Conflict = `overwrite` |
|--------|-------------------|------------------------|
| `copy` | If gateway has the name, leave gateway as-is | Replace gateway entry; source file untouched |
| `move` | If gateway has the name, leave both as-is | Replace gateway entry **AND delete from source file** (banner warning surfaces) |

`duplicate` is intentionally not supported (R-25). The webview surfaces
a red banner whenever any checked row matches `move + overwrite` — the
combination mutates the source AND discards local edits, so the
operator gets two visual cues (banner + modal repeat) before Apply.

**How `move` is safe against the Claude Code config reflector (R-31 / X7):**

- Daemon side: `internal/claudeimport/apply.go::mutateSourceRemove` runs
  under a per-file refcounted mutex, re-reads the source under that
  lock, asserts the mtime has not changed since the snapshot was taken,
  then writes via `CreateTemp` + `Rename` (atomic on POSIX,
  best-effort-atomic on Windows).
- TS side: the existing `vscode/mcp-gateway-dashboard/src/claude-config-sync.ts`
  reflector ALREADY implements CAS-style retry over a sha256 of the
  `mcpServers` value before / after its own writes. The two layers
  jointly cover the "concurrent external write" surface — a daemon
  write that lands between the reflector's read and rename is caught by
  the reflector's hash mismatch and retried; a reflector write that
  lands during the daemon's apply is caught by the daemon's mtime-CAS
  and surfaces as `OpResult.SourceUpdated=false` with a `mtime` reason.

**Provenance:** every successful copy/move appends a record to
`~/.mcp-gateway/claude-imported.json` (atomic `CreateTemp` + `Rename`,
in-process `sync.Mutex` serialises concurrent appenders). The next
snapshot reads the sidecar and surfaces a `◊ previously imported`
badge with hover-timestamp on rows seen before — operator can decide
whether to re-import or skip.

**Provenance write failure** (e.g. Windows AV scanner racing the rename)
is surfaced via `OpResult.ProvenanceWarning` — the Apply succeeded but
the badge will not show this import.

REST contract (FROZEN as of v1.9.0):

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/api/v1/claude-code/import-snapshot?source=…&project_root=…` | Source rows + drift / provenance metadata |
| POST | `/api/v1/claude-code/import-apply` | Per-row apply with single end-of-batch regen |

Older daemons (<v1.6) return 404; the webview detects this via the
existing `/api/v1/claude-code/compat-matrix` probe and shows
"Upgrade gateway to v1.6+ to import" instead of calling the endpoint
(R-24 — version-skew gate).

### Known limitations — webview file dialogs

- **Multi-monitor `showOpenDialog` (Windows, Q3.4).** When VSCode is on
  a non-primary monitor, the OS Open File dialog (used by the SAP
  Picker's `[⋮]` override fields and the Settings webview's Browse
  buttons) may appear on the primary monitor instead of next to the
  VSCode window. **This is upstream VSCode behaviour, not a gateway
  bug.** Drag the dialog to the correct monitor, or move VSCode to the
  primary monitor before clicking Browse. We surface this here rather
  than working around it because the workarounds (custom dialog,
  manual coordinate hint) introduce more failure modes than the quirk
  itself.

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

## Connecting Claude Desktop to the Gateway

Claude Desktop has no plugin system, so the gateway is registered
manually as a single MCP backend in `claude_desktop_config.json`. After
this one-time edit, Claude Desktop sees every backend the gateway
proxies — adding or removing servers in the gateway requires no further
Desktop config changes.

**Config path:**

| Platform | Path |
|---------|------|
| Windows | `%APPDATA%\Claude\claude_desktop_config.json` |
| macOS | `~/Library/Application Support/Claude/claude_desktop_config.json` |
| Linux | `~/.config/Claude/claude_desktop_config.json` |

**Entry to add** (under existing `mcpServers`, preserving any other
entries you have):

```json
{
  "mcpServers": {
    "mcp-gateway": {
      "type": "http",
      "url": "http://localhost:8765/mcp",
      "headers": {
        "Authorization": "Bearer <contents of ~/.mcp-gateway/auth.token>"
      }
    }
  }
}
```

The gateway's aggregate `/mcp` endpoint exposes every registered backend
under namespaced tool names (`<backend>__<tool>`). Replace `8765` with
your `mcpGateway.apiUrl` port if you changed it from the default.

Restart Claude Desktop after editing. Tools appear under one
`mcp-gateway` source in Desktop's MCP panel.

**Why no `install-claude-desktop` CLI?** The Desktop install is one JSON
edit — far less work than Claude Code (which needs plugin marketplace
registration, plugin install, and an optional webview patch). The
sample above is the install. If you script this for many machines,
templating the snippet with your config-management tool of choice is
simpler than a dedicated subcommand.

**Per-backend entries (advanced).** If you prefer separate Desktop
entries per backend (one per gateway-registered server) instead of the
single aggregate entry above, use the per-backend URL form:
`http://localhost:8765/mcp/<backend-name>`. The cost is keeping the
Desktop entry list manually in sync with the gateway as you add or
remove backends — the aggregate form avoids that entirely.

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

## Gateway meta-tools vs Claude Code ToolSearch

Phase 16.6 added three aggregate-only meta-tools to the gateway's `/mcp`
surface — `gateway.list_servers`, `gateway.list_tools`, and
`gateway.invoke`. They solve a context-budget problem: a host that loads
every MCP tool schema into the prompt at session start would burn tens
of kilobytes before the first call. The meta-tools let a client lazily
discover topology and invoke any backend tool by name, keeping only
three schemas in the prompt.

Claude Code 2.x ships a built-in mechanism (`ToolSearch`) that solves the
same context-budget problem at the harness level: every tool name is
listed in a `system-reminder` block, and the model fetches a schema only
when it needs to call that specific tool. So inside Claude Code there
are two paths to the same call.

**Both surfaces stay. They operate at different layers and target
different clients.**

| Concern | `ToolSearch` (Claude Code) | Gateway meta-tools | Gateway core |
|---------|---------------------------|--------------------|--------------|
| Lazy load tool schemas | yes — by name or keyword | yes — `gateway.list_tools` then `gateway.invoke` | n/a |
| Manage MCP subprocess lifecycle | no | no | yes |
| Health monitor + auto-restart | no | no | yes |
| Circuit breaker | no | no | yes |
| REST API for hot add/remove | no | no | yes |
| Cross-tab subprocess multiplexing | no | no | yes |
| Works for clients other than Claude Code | no | yes (Cursor / Continue.dev / Cline / SDK) | yes |

**Inside Claude Code:** the namespaced tool surface
(`mcp__mcp-gateway__<backend>__<tool>`) is the primary path. Each
namespaced tool name appears in the deferred-tool list and Claude Code
loads its schema via `ToolSearch` on demand. The meta-tools sit dormant
but stay available as a fallback against `tools/list` cache bugs (Issue
#13646) and topology changes mid-session.

**Outside Claude Code** (Cursor, Continue.dev, Cline, custom Anthropic
SDK apps): no `ToolSearch` equivalent exists. The meta-tools become the
canonical lazy-discovery surface — `gateway.list_tools` enumerates every
backend tool, `gateway.invoke` calls anything.

Full decision rationale, alternatives considered, and the monitoring
triggers that would reopen this question:
[`docs/ADR-0006-toolsearch-vs-gateway-metatools.md`](docs/ADR-0006-toolsearch-vs-gateway-metatools.md).

## Managing the daemon

The gateway daemon is a separate, long-lived process. That is the whole
point — it outlives any single Claude Code / VSCode session so backends
stay warm across `/clear`, reload window, and closing the editor. The
trade-off is that "long-lived" needs **explicit operator control** so the
daemon is never a black box.

### From the CLI (`mcp-ctl daemon ...`)

```
mcp-ctl daemon start     [--daemon-path PATH] [--wait 10s]
mcp-ctl daemon stop      [--timeout 10s]
mcp-ctl daemon restart   [--daemon-path PATH] [--timeout 10s] [--wait 10s]
mcp-ctl daemon status                  # alias: info
```

- **`start`** works when no daemon is running. It resolves the binary
  from `--daemon-path` → `MCP_GATEWAY_BIN` → `PATH` in priority order,
  spawns detached (`DETACHED_PROCESS|CREATE_NEW_PROCESS_GROUP` on
  Windows, `Setpgid` on POSIX), then polls `GET /api/v1/health` until
  reachable or `--wait` times out.
- **`stop`** prefers a graceful `POST /api/v1/shutdown` (auth-gated,
  202 + drain). If the REST endpoint is unreachable it falls back to
  reading the PID from `$XDG_RUNTIME_DIR/mcp-gateway.pid` (Linux) or
  `%TEMP%\mcp-gateway.pid` (Windows) and sending a signal — SIGTERM
  first, SIGKILL after 2 s on POSIX; `Kill()` on Windows.
- **`status`** prints a compact table with PID, version, RFC3339 start
  time, human-readable uptime (e.g. `2h 14m`), and server counts.
  Returns exit code 2 (`exitUnreachable`) when the daemon is offline.

### From the VSCode extension

The **Gateway** tree view at the top of the MCP Gateway activity bar
shows live daemon status:

- Root row: "Gateway" with an uptime description (e.g. `2h 3m`) and a
  status icon. Offline daemons show `offline` and collapse their detail
  rows.
- Expandable children: `PID`, `Version`, `Started`, `Uptime`.
- Inline action buttons on the root:
  - `▶` **Start** when the daemon is unreachable
  - `■` **Stop** when the daemon is running
  - `↻` **Restart** when the daemon is running — REST-based, so it works
    even when the extension does NOT own the child process (i.e. you
    started the daemon via `mcp-ctl daemon start` earlier).

The aggregate **MCP: N/M** status bar item tooltip now leads with a
`**Gateway**: 2h 3m · v1.7.3 · pid 12345` line so uptime is visible at a
glance without opening the sidebar.

### Graceful shutdown semantics

`POST /api/v1/shutdown` and `SIGTERM` share the same exit path:
1. Response flushes (Flusher.Flush) with 202 before cancel fires.
2. Root context cancels — the errgroup that runs HTTP, config watcher,
   health monitor, and backend lifecycles drains.
3. A bounded 8-second `context.WithTimeout` wraps the post-`Wait`
   cleanup (`ps.FlushPersists` + `lm.StopAll`) so a hung SSE client or
   patch-state flush cannot keep the daemon alive indefinitely.
4. PID file is removed. Process exits with code 0.

**`--no-auth` caveat.** When the daemon runs with `--no-auth` (opt-in via
`MCP_GATEWAY_I_UNDERSTAND_NO_AUTH=1`), `POST /api/v1/shutdown` is
reachable by any local process on the host. This matches the existing
"local trust" stance documented in ADR-0003 §no-auth-escape-hatch — if
you disable authentication you accept that anything on the loopback can
stop the daemon. Do not combine `--no-auth` with `allow_remote`.

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
