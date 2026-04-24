# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.7.0] - 2026-04-24

### Added — Daemon lifecycle control

- **`POST /api/v1/shutdown`** — auth-gated graceful shutdown endpoint. Returns 202 + `{"status":"shutting_down"}`, flushes response via `http.Flusher` before triggering the root `context.CancelFunc`, idempotent under concurrent requests (returns `already_shutting_down` for re-entry). Wired to the same signal-handler path as `SIGTERM`, so the in-flight errgroup drain and the new 8-second bounded `context.WithTimeout` apply to both exit paths.
- **Extended `/api/v1/health`** — response now includes `started_at` (RFC3339 UTC), `pid`, `version`, and `uptime_seconds` alongside the existing `status`/`servers`/`running`/`auth` fields. All new fields `omitempty` — older clients decode unchanged.
- **`internal/pidfile` package** — atomic PID file acquisition (`O_CREAT|O_EXCL|O_WRONLY` + post-write `Lstat` non-symlink verification, `ErrAlreadyRunning` sentinel). Liveness probe is HTTP-based (`GET /api/v1/health` with 500ms timeout, TLS-aware with `InsecureSkipVerify` for self-signed loopback certs), so stale-reap works identically on Linux and Windows. `DefaultPath` prefers `$XDG_RUNTIME_DIR/mcp-gateway.pid` on Linux, falls back to `os.TempDir()` on other platforms.
- **`mcp-ctl daemon` CLI subcommands** — `start` (spawns detached via `DETACHED_PROCESS|CREATE_NEW_PROCESS_GROUP` on Windows, `Setpgid` on POSIX; polls `/health` for reachability), `stop` (REST `/shutdown` → PID-file-based OS kill fallback with SIGTERM → 2 s wait → SIGKILL escalation on POSIX), `restart` (composed stop + start with connection-error tolerance), `status` (tabwriter table: STATUS / PID / VERSION / STARTED / UPTIME / SERVERS / RUNNING). Uptime formatter handles `Ns` / `Nm Ss` / `Nh Mm Ss` / `Nd Hh Mm` ranges.

### Added — VSCode extension

- **`mcpGateway.restartDaemon` command** — REST-based (works for daemons started externally via `mcp-ctl daemon start`, not just extension-owned children). `DaemonManager.restart()` flow: `shutdown()` → poll `/health` unreachable → cleanup own child handle if any → spawn fresh. Serialised by a new `restarting` mutex with `start()`/`stop()` to prevent auto-start + user-restart races.
- **Gateway tree view** — new `mcpGatewayDaemon` view at the top of the MCP Gateway activity container. Root "Gateway" row with status icon + uptime description, expandable into `PID` / `Version` / `Started` / `Uptime` detail rows. Inline action buttons: start (when offline), stop + restart (when running). Fingerprint collapses uptime into 5-second buckets so the tree doesn't re-render every poll tick.
- **Status bar tooltip** now leads with `**Gateway**: 2h 3m · v1.7.3 · pid 12345` line when `/health` metadata is available. Missing fields are skipped rather than printed as `unknown`.
- **`ServerDataCache.gatewayHealth`** — cache fetches `/servers` and `/health` in parallel via `Promise.allSettled` on the same refresh cycle. `/health` failures don't mark the cache as offline (only `/servers` does); consumers get `gatewayHealth: null` and render "offline".

### Security

- `POST /api/v1/shutdown` is mounted inside the Bearer-auth-required router group alongside all other mutating endpoints. Rejected with 401 without a valid token.
- PID file mode `0600` with post-write `Lstat` check — world-writable `/tmp` symlink attacks rejected.
- `--no-auth` mode caveat documented: with auth disabled, any local process can POST `/shutdown`. Acceptable per existing `MCP_GATEWAY_I_UNDERSTAND_NO_AUTH=1` operator attestation (ADR-0003 §no-auth-escape-hatch).

### Documentation

- `README.md` — new "Managing the daemon" section covering CLI, extension UI, status bar tooltip, and graceful shutdown semantics.

### Breaking

None. All additions are backward-compatible — `HealthResponse` fields use JSON `omitempty` and TypeScript `?`; `DaemonManager.start()`/`stop()` signatures unchanged.

## [1.6.0] - 2026-04-22

### Added

- **Dual-mode gateway** — `/mcp` aggregate + `/mcp/{backend}` per-backend MCP surfaces from a single daemon. Unblocks Claude Code plugin packaging where each backend registers as its own `.mcp.json` entry without breaking clients that depend on the aggregate endpoint.
- **Claude Code Plugin packaging** — `installer/plugin/` ships an installable plugin with `.claude-plugin/plugin.json` (userConfig: `gateway_url` + `auth_token`) and `installer/marketplace.json` for one-command install. The plugin's `.mcp.json` is regenerated from the gateway's live backend list on every REST mutation (atomic tmp+rename, 0600 POSIX / DACL Windows).
- **`mcp-ctl install-claude-code`** — headless bootstrap CLI. Flags: `--mode|--scope|--no-patch|--dry-run|--refresh-token|--check-only`. LIFO rollback on partial failure. Exit codes 0/1/2/3/4 distinguishing usage / gateway-down / token-drift / rollback-executed.
- **Webview patch with native MCP reconnect (Alt-E pattern)** — opt-in. Walks Claude Code's React fiber tree to capture a reference to `session.reconnectMcpServer` (the same native method the `/mcp` panel's Reconnect button calls) and invokes it when the gateway enqueues a reconnect action. Closes the "tools/list caching" bug class (#13646) without patching `extension.js`.
- **`gateway.invoke` universal fallback tool** + `gateway.list_servers` / `gateway.list_tools` meta-tools on the aggregate endpoint. Callable even when the specific tool isn't in the client's current `tools/list` cache.
- **Supported-versions map** — `configs/supported_claude_code_versions.json` tracks `alt_e_verified_versions`. Served via `GET /api/v1/claude-code/compat-matrix`. Dashboard surfaces Mode C (yellow advisory) when the running CC version is unverified.
- **`/api/v1/claude-code/*` REST endpoints** — `patch-heartbeat`, `patch-status`, `pending-actions`, `pending-actions/{id}/ack`, `probe-trigger`, `probe-result`, `plugin-sync`, `compat-matrix`. FROZEN v1.6.0 contract in `docs/api/claude-code-endpoints.md`.
- **VSCode dashboard "Claude Code Integration" panel** — new command `mcpGateway.showClaudeCodeIntegration`. Displays plugin + patch + channel status with a 12-mode failure matrix (A-M, E obsoleted under Alt-E). Buttons: `[Activate for Claude Code]`, `[Probe reconnect]`, `[Copy diagnostics]`. Diagnostics report includes Alt-E metrics (p50/p95 reconnect latency, fiber depth history, dedup recent errors).
- **Slash-command disclaimer** — every auto-generated `.claude/commands/*.md` carries two disclaimer lines below the AUTO-GENERATED marker stating "this is a slash-command prompt template, NOT an MCP server registration" + pointer to the mcp-gateway plugin install path. Closes operator-confusion bug class (#16143). Regression-pinned by test.

### Security

- **CORS policy for `vscode-webview://`** narrowly scoped to `/api/v1/claude-code/*`; rest of `/api/v1` retains existing csrf-protected origin policy. OPTIONS preflight runs BEFORE bearer auth so browsers can preflight without `Authorization` (REVIEW-16 L-02). Unknown origins get 204 WITHOUT `Access-Control-Allow-Origin` — deny by omission.
- **Rate limits** — separate token-bucket limiters on `/patch-heartbeat` (5/min per session_id), `/pending-actions` (60/min per IP), `/patch-status` (60/min per IP). Amortized idle-bucket eviction.
- **Patch state durability (REVIEW-16 M-01)** — pending reconnect actions + recent heartbeats persist to `~/.mcp-gateway/patch-state.json` (0600, atomic tmp+rename) on every mutation. TTL-filtered on daemon startup. Graceful-shutdown path flushes in-flight persists before `lm.StopAll`.
- **Inlined auth token in patched index.js locked to 0600 on POSIX / DACL on Windows** (REVIEW-16 L-03). `mcp-ctl install-claude-code --refresh-token` re-registers plugin + re-applies patch after gateway token rotation (REVIEW-16 M-03).

### Documentation

- `docs/ADR-0005-claude-code-integration.md` — architectural decision record for the hybrid dual-mode + plugin + Alt-E webview-patch approach.
- `docs/api/claude-code-endpoints.md` — FROZEN v1.6.0 REST contract.
- `docs/TESTING-PHASE-16.md` — four-tier test documentation.
- README §"Connecting Claude Code to the Gateway" + §"Commands vs MCP servers".

### Breaking

None. All additions are backward-compatible.

### Known limitations

- **Webview patch is opt-in** and modifies Claude Code's own `webview/index.js`. Operators who decline still get full functionality via manual `/mcp` panel Reconnect.
- **CC version drift** mitigated via `configs/supported_claude_code_versions.json` + dashboard Mode C advisory — unverified versions are warnings, not errors.

## [1.5.0] - 2026-04-20

### Added
- **Server & command catalogs** — first-party JSON catalogs of popular MCP servers (context7, pdap-docs, orchestrator, pal-mcp, sap-gui-control) and matching slash-command templates. Versioned draft-07 JSON Schemas pinned by `$id` (`v1`). Catalogs ship bundled with the extension VSIX; never fetched from the network.
- **Add Server "Choose from catalog" dropdown** — `AddServerPanel` webview now exposes a catalog dropdown above the Name field. Selecting an entry pre-fills transport / url / command / args and renders one empty row per declared `env_keys` / `header_keys` so the operator fills only secret values. `(Custom server)` preserves the pre-catalog free-form flow.
- **Slash-command template enrichment** — `SlashCommandGenerator` injects the catalog's `template_md` body into `.claude/commands/<server>.md` on server transition to `running`. Allow-list substitution of `${server_name}` / `${server_url}`; unknown `${var}` tokens are left literal. Servers without a catalog entry keep the pre-v1.5 bare skeleton unchanged.
- **`mcpGateway.catalogPath` setting** (`type: string`, `default: ""`, `scope: machine`) — optional override path to a directory containing `servers.json` + `commands.json`. Operator path wins when non-empty and the directory exists; otherwise falls back to the bundled catalog under the extension's installation directory.
- **`npm run lint:catalog`** — ajv-cli validation of both seed files against their schemas plus a cross-reference check that every `command.server_name` resolves to a `server.name`. Added as a CI step alongside a VSIX-contents assertion ensuring the four catalog files plus ajv runtime dependencies are packaged.

### Security
- **Host-side re-validation of catalog selection** — `AddServerPanel.handleSubmit` re-loads the catalog and re-runs every field through `validation.ts` helpers before calling `client.addServer()`; forged `catalogId` payloads are rejected before they reach the daemon.
- **No catalog HTML interpolation** — every catalog string reaches the webview via `jsonForScript` and is rendered via `textContent` / `.value` (never `innerHTML`). `escapeHtml` neutralises `<script>`-laden catalog entries; verified by targeted test.
- **1 MiB catalog cap with TOCTOU-safe bounded read** — loader uses `fs.promises.open` + `fileHandle.stat` + bounded `fileHandle.read` on a single file handle, eliminating the swap window between stat and read. Oversized files produce a warning and an empty entry list; `readFile` is never invoked.
- **`scope: machine`** on `mcpGateway.catalogPath` prevents per-workspace catalog override (exfiltration-vector mitigation).
- **`$id` network refusal by design** — ajv is configured with bundled schema files via `addSchema`; catalog `$id`s are documentation-only and never trigger HTTP fetch.

### Breaking-config

- **Half-configured TLS now refuses to start** (T15B.3). Previously, setting
  exactly one of `gateway.tls_cert_path` / `gateway.tls_key_path` silently
  dropped back to plain HTTP — an operator who edited the config and forgot
  the second setting would see no error, assume TLS, and actually run
  cleartext. The daemon now refuses to start with an error message naming
  **both** paths. The wording is deliberately stable (grep target; future
  refactors must keep the string intact):

  > `TLS is half-configured: gateway.tls_cert_path is set but gateway.tls_key_path is empty — both must be set to enable TLS, or both must be empty for plain HTTP`

  Symmetric variant when only `tls_key_path` is set:

  > `TLS is half-configured: gateway.tls_key_path is set but gateway.tls_cert_path is empty — both must be set to enable TLS, or both must be empty for plain HTTP`

  Both variants are stable grep targets — future refactors must keep the
  strings intact. **No grace period** —
  silent plain-HTTP when the operator intended TLS is a security defect, not
  a feature. Installations running with half-finished TLS config from v1.4.0
  must either complete the pair or remove both settings before upgrading.

### Fixes

- **Scanner line-length cap raised from 64KB to 1MB** on both log paths
  (T15A.2a + T15A.2b — atomic pair, F-11 closed). `bufio.Scanner` defaults to
  a 64KB line limit, which silently truncated long lines both in
  `internal/ctlclient/client.go` (SSE client-side, `streamLogsOnce`) and in
  `internal/lifecycle/manager.go` (producer-side, `scanStderr`). The effective
  end-to-end cap is the minimum of the two sites, so fixing only one would
  still leave the user-visible ceiling at 64KB. Both sites now call
  `scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)` with a comment explaining
  the 64KB→1MB trade-off. Closes ROADMAP F-11.

### Hygiene

- **Bearer auth constant-time compare — pad-to-expected-length refactor**
  (T15A.1). `internal/auth/middleware.go` previously called
  `subtle.ConstantTimeCompare([]byte(received), expectedBytes)`, which the Go
  stdlib documents as returning 0 immediately on length mismatch. For the
  fixed 43-char token the practical leakage is 1 bit out of 256 — this is
  **not a security fix**. Landed anyway to remove the recurring PAL-review
  pattern and provide a clean reference for anyone copying the code to a
  variable-length secret: compare a pad-to-expected-length buffer, then do a
  separate `ConstantTimeEq` length check, combine both results
  unconditionally. Existing `TestMiddleware_ConstantTimeOnDifferentLengths`
  pins the coverage shape.

### Tests

- **TLS integration tier** (T15B.1 / T15B.2 / T15B.3). New
  `internal/api/tls_integration_test.go`: generates a CA → leaf cert chain in
  `t.TempDir()`, drives `ListenAndServeTLS`, probes with a custom `RootCAs`
  client pool — asserts 200 on `/api/v1/health` and 401 on an authed route
  without Bearer. Pins the previously-unexercised `ServeTLS` branch. Negative
  tests cover non-loopback + `authEnabled` + no TLS → startup refusal with
  pinned wording, and half-configured TLS refusal in both orderings
  (cert-only, key-only). Runs under the default `go test ./...` path — no
  external prereqs.
- **Windows DACL enforcement tier** (T15C.1). New
  `internal/auth/token_perms_integration_windows_test.go` under the
  `integration` build tag. Uses `LogonUserW` + `ImpersonateLoggedOnUser` via
  `advapi32.dll` to attempt `os.Open` on the token file as a second local
  account; expects `ACCESS_DENIED`. Confirms the token-file DACL is
  **OS-enforced**, not just structurally correct. Gated behind
  `make test-integration-windows` so the default `go test ./...` path is
  unaffected. `runtime.LockOSThread` pin + deferred `RevertToSelf` prevent
  impersonation from bleeding into other goroutines. Skips gracefully when
  `MCPGW_TEST_USER` / `MCPGW_TEST_PASSWORD` env vars are absent.
- **Manual-protocol branch for Windows enforcement** (T15C.2). The
  `windows-latest` GitHub-hosted runner spike
  (`docs/spikes/2026-04-19-windows-latest-impersonate.md`) was deferred — the
  branch cross-compiles clean but the repo's pre-push hook blocks leaking the
  spike branch to the remote. Scoped back to documented manual protocol:
  new `Makefile` target `test-integration-windows` (fail-fast env-var guard)
  plus a three-tier Testing section in the README with the elevated-PowerShell
  operator protocol. No `.github/workflows/ci.yml` change in v1.5.0.

### Documentation

- **README Testing tiers section** (T15D.2). Three-tier table separates what
  each test command proves and what it needs to run: default `go test ./...`
  covers unit + structural + TLS integration; `make test-integration-windows`
  covers the Windows DACL enforcement tier on a pre-provisioned local test
  account. Includes the elevated-PowerShell sequence (`net user /add` → env
  vars → make → `net user /delete`) and the behavior of the integration test
  when credentials are absent (`go test ./...` unaffected;
  `go test -tags integration ./...` skips with a pointer back to the README;
  `make test-integration-windows` fails fast).
- **README Catalogs section** (CD.1). New end-user-facing section documenting
  catalog layout (`servers.json` + `commands.json`), the `$id` version-pinning
  convention, the `mcpGateway.catalogPath` machine-scope override, hard limits
  (1 MiB cap, `v1.*` schema pin, fail-soft on malformed files), and the
  known-limitation note on slash-command edits below line 1 (regeneration
  overwrites edits unless the line-1 marker is removed). Paired with the
  feature entries in `### Added` / `### Security` above.

### ROADMAP

- **F-11 (bufio.Scanner 64KB stderr limit) — CLOSED** in Phase 15.A. Both
  scanner sites (SSE client + stderr producer) raised to 1MB atomically;
  regression tests pin the cap. End-to-end log-line ceiling is now 1MB.

## [1.0.0] - 2026-04-09

### Added
- **Go daemon** (`mcp-gateway`): MCP server lifecycle management for stdio and HTTP/SSE backends
- **CLI** (`mcp-ctl`): full server management, tool calls, log streaming, stdio compliance validation
- **VS Code extension** (`mcp-gateway-dashboard`): tree view, status bar, daemon lifecycle, webview detail panels
- **REST API** (v1): CRUD for servers, tool listing and calls, metrics, SSE log streaming
- Health monitoring with circuit breakers and configurable auto-restart
- Per-server tool budget with `ConsolidateExcess` meta-tool for budget overflow
- `compress_schemas` option: truncate tool descriptions, strip schema examples for token savings
- Environment variable expansion (`${VAR}`) in config with security-restricted fallback allowlist
- KeePass KDBX credential import via CLI (`mcp-ctl credential import-kdbx`)
- Windows Job Objects for automatic child process cleanup on daemon exit
- Installer scripts for Linux, macOS, and Windows with system service registration
- Binary signing with Sigstore cosign and SHA-256 checksum verification
- `GET /api/v1/metrics`: per-server crash counts, MTBF, uptime, token cost estimates
- `mcp-ctl validate`: black-box stdio compliance harness for MCP server onboarding
- API versioning with backward-compatible redirect (`/api/*` -> `/api/v1/*`)
- SAP system auto-detection and grouping by SID (opt-in via settings)

### Security
- CSRF protection via `Sec-Fetch-Site` header validation on mutating requests
- SSE connection limit (max 20 concurrent) to prevent resource exhaustion
- Non-loopback binding blocked without explicit `allow_remote` configuration
- Rate limiting (100 concurrent / 200 backlog) and 1 MB body size limit
- Dangerous environment key blocklist (25+ hijack vectors: `LD_PRELOAD`, `DYLD_INSERT_LIBRARIES`, etc.)
- Header injection prevention (CRLF/NUL validation)
- Atomic config writes (temp file + rename)
