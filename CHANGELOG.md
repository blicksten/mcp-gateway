# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.2.0](https://github.com/blicksten/mcp-gateway/compare/v0.1.0...v0.2.0) (2026-04-20)


### Features

* **catalog-A:** server + command catalogs v1.5.0 — schemas, seeds, TS loader, CI ([54f8c16](https://github.com/blicksten/mcp-gateway/commit/54f8c16beac7ba708f9762aeb8fd641363e72045))
* **catalog-B:** Add Server browse webview — catalog dropdown + host re-validation ([c49e6ef](https://github.com/blicksten/mcp-gateway/commit/c49e6ef422bba92b6a95189267002645ef68f58a))
* **catalog-C:** slash-command template enrichment — catalog-aware SlashCommandGenerator ([6e70dbd](https://github.com/blicksten/mcp-gateway/commit/6e70dbd4b15e0dbbd5d36f2b7d598203fc3c1c1c))
* **catalog-D:** v1.5.0 release gate — README + CHANGELOG + VSIX + final security codereview ([864c5d5](https://github.com/blicksten/mcp-gateway/commit/864c5d51cae7d0d00e29841244741188183dd1db))
* **extension:** phase 11.E slash command auto-generation ([991121f](https://github.com/blicksten/mcp-gateway/commit/991121f82f946e103e007630395d5b5997bc8581))
* mcp-gateway v1.0.0 ([5df38c3](https://github.com/blicksten/mcp-gateway/commit/5df38c348de01f29196afc868b53fa54d3e3bf43))
* **phase-12.A:** Bearer auth - VS Code extension (T12A.8-T12A.12) ([4e075bf](https://github.com/blicksten/mcp-gateway/commit/4e075bfb63d7c71e867b8927e5d0182a4b0bbf57))
* **phase-12.A:** Bearer token auth - daemon + mcp-ctl (Go side) ([6686cd2](https://github.com/blicksten/mcp-gateway/commit/6686cd2ba4d0c28fba606a66d8def6ea3af8c55b))
* **phase-12.B:** KeePass credential push (T12B.1-T12B.6) ([7b1e52f](https://github.com/blicksten/mcp-gateway/commit/7b1e52f51669b674b398dfea79cd8cda03bf5ea7))
* **phase-13:** security hardening - process groups + watcher race + TLS + log redaction ([a845a78](https://github.com/blicksten/mcp-gateway/commit/a845a7830ab0208ed60fab509203750ea493a7ec))
* **phase-14:** community/CI — SECURITY.md + gitleaks + README auth/TLS/redaction ([29e6fc2](https://github.com/blicksten/mcp-gateway/commit/29e6fc22eb40d225752d69352f9e1a910cc7daa8))
* **phase-15.A:** LOW findings closure — ConstantTimeCompare hygiene + scanner 1MB cap ([5c949ca](https://github.com/blicksten/mcp-gateway/commit/5c949caa5a2e26a0c5d24b7928ac59aecf75465c))
* **phase-15.B:** TLS integration tier — half-configured refusal + ServeTLS coverage ([be9bbe9](https://github.com/blicksten/mcp-gateway/commit/be9bbe98d12b96d2c585c0f03658f6bbb90fc34f))
* **phase-15.C:** Windows DACL enforcement tier — integration test + manual-protocol branch ([22f94c3](https://github.com/blicksten/mcp-gateway/commit/22f94c36682c6066e7c4b244ec6191991df060a2))


### Bug Fixes

* **phase-12.A gate:** PAL codereview findings — CRITICAL X-Forwarded-For bypass + 6 others ([a168647](https://github.com/blicksten/mcp-gateway/commit/a168647f09e8501a58b0b51571f1d2b519770b9f))
* **phase-12.B gate:** PAL codereview findings — 1 CRITICAL + 3 HIGH + 5 MEDIUM ([b05f30e](https://github.com/blicksten/mcp-gateway/commit/b05f30e720bf9f17b14512d84708dc4375931592))
* **phase-13 gate:** PAL codereview findings — 6 HIGH + 2 MEDIUM ([c242b35](https://github.com/blicksten/mcp-gateway/commit/c242b351c561afd1dfbd6f20778754fc85e103e8))
* security hardening — env blocklist bypass, CRLF injection, URL validation ([b38447d](https://github.com/blicksten/mcp-gateway/commit/b38447debfdfacbad803936a1583be2849dc786c))

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
