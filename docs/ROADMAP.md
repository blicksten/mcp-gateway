# MCP Gateway — Roadmap

## Released

| Version | Date | Description |
|---------|------|-------------|
| v1.0.0 | 2026-04-09 | Go daemon + CLI (mcp-ctl) + VS Code extension + REST API + installers + cosign signing |
| v1.0.1 | 2026-04-14 | Security hardening: env key blocklist bypass fix, CRLF validation, URL validation, config permissions, MaxServers enforcement, disable-stops-process |

Phases 1–10.5 implemented. Full history preserved locally in `full-history-backup` branch.

---

## Backlog

### Phase 11 — Extension UX (v1.1.0)

11.0 COMPLETE (`7fbd4c0` — status bar foreground tinting). 11.A COMPLETE (`8650505`, 2026-04-15 — fingerprint-based diff refresh, inline start/stop/restart icons, `ServerDataCache.lastRefreshFailed`). 11.B COMPLETE (`6d059e2`, 2026-04-15 — sidebar `WebviewView` detail, MarkdownString tooltips, `McpStatusBar` cache migration). 11.C COMPLETE (`ec3aae7`, 2026-04-15 — `AddServerPanel` webview form replacing sequential InputBox flow, shared `src/validation.ts` module, platform-agnostic `isAbsolutePath`, CSP `form-action 'none'`, in-flight submit guard, exception-safe dispose ordering). 11.D COMPLETE (`44f0d7a`, 2026-04-15 — SAP hierarchical tree via new `SapComponentItem` + `sapGroupBySid` setting + live config watcher, new `AddSapPanel` webview form with absolute-path VSP/GUI executable fields + Set-based duplicate-detection). 11.E COMPLETE (`991121f`, 2026-04-16 — `SlashCommandGenerator` with promise queue, magic-header marker, transition detection, orphan cleanup, daemon outage protection; opt-in via `slashCommandsEnabled` setting; 24 tests).

| # | Task | Description |
|---|------|-------------|
| 11.1 | ✅ Diff-based tree refresh | Only fire TreeView update when data actually changed — eliminate flicker and scroll jumps. |
| 11.2 | ✅ Inline start/stop/restart buttons | Show control icons directly on each server row, no context menu needed. |
| 11.3 | ✅ Sidebar detail webview | Always-on `WebviewView` beneath tree views; re-renders on selection + cache refresh. |
| 11.4 | ✅ Status bar + tree item MarkdownString tooltips | Per-server breakdown with per-status sections; McpStatusBar migrated from `getHealth` polling to `ServerDataCache.onDidRefresh`. |
| 11.5 | ✅ Add Server webview form | Replace sequential InputBox prompts with a single form, auto-detect transport type. |
| 11.6 | ✅ SAP grouping toggle | Group SAP systems by SID with colored VSP/GUI icons, opt-in via settings. |
| 11.7 | ✅ Add SAP System flow | Webview form: SID + component checkboxes + absolute-path VSP/GUI executables. |
| 11.8 | ✅ Settings consolidation | All 7 settings already under `mcpGateway.*` (apiUrl, autoStart, daemonPath, pollInterval, sapGroupBySid, slashCommandsEnabled, slashCommandsPath); all `getConfiguration('mcpGateway')` calls, all commands, and storage key `mcpGateway.credentialIndex` verified namespace-consistent (audit 2026-04-16, no code changes). |
| 11.9 | ✅ Slash command generation | Auto-generate `.claude/commands/<server>.md` on server start, delete on stop. |

### Phase 12 — Auth + KeePass (v1.2.0)

Detailed plan `docs/PLAN-main.md:115-354` (audited 2026-04-17, architect APPROVE_WITH_REFINEMENTS + dev-lead + lead-auditor 2 cycles + specialist-auditor 2 cycles, zero MEDIUM+ findings).

**Phase 12.A COMPLETE 2026-04-18** — commits `30bffd9` (ADR-0003), `6686cd2` (daemon + mcp-ctl), `4e075bf` (VS Code extension), `a168647` (PAL gate fixes). Bearer token auth is live on daemon REST + SSE + MCP transports, `mcp-ctl` carries the Authorization header, the extension shares one `buildAuthHeader` helper between `GatewayClient` and `LogViewer`, and a 22-assertion integration test (8-case MCP transport policy matrix + auth-before-csrf ordering + intentional-non-coverage assertions + X-Forwarded-For spoof regression + Sec-Fetch-Site cross-site deny + 401 no-store headers) guards the policy matrix. PAL codereview (gpt-5.2-pro, security mode) on the phase landed 1 CRITICAL (`X-Forwarded-For` bypass via `middleware.RealIP`), 3 HIGH, 3 MEDIUM — all fixed in `a168647`. Bearer-without-TLS WARN is in place for the `allow_remote=true` + no-TLS startup path until Phase 13.B lands TLS.

**Phase 12.B COMPLETE 2026-04-18** — commits `7b1e52f` (T12B.1–T12B.6) + follow-up PAL fix (1 CRITICAL Found=0 JSON contract break, 3 HIGH mcp-ctl path / server-name validation / stdin password []byte hygiene, 5 MEDIUM stdin cap / exit-code typing / env isolation / per-entry SecretStorage loop / summary UX). KeePass credentials now flow from KDBX → `mcp-ctl credential import --json --password-stdin` (argv-array exec, no shell, stdout never logged) → extension parses the stable version=1 JSON contract → `applyImportedCredentials` writes per-server into SecretStorage with partial-failure tolerance and malformed-name rejection. 478 extension tests passing (+7 new: 4 initial keepass + 3 PAL regression). 6 new Go assertions cover the JSON contract golden shape + version guard + Found=0 + password-stdin piping / mutex / empty-rejection.

**Phase 12.A — Bearer Token Auth (v1.2.0 Go daemon + mcp-ctl + GatewayClient + LogViewer)**

| # | Task | Description |
|---|------|-------------|
| 12.A.0 | ADR-0003 | Policy matrix + Windows DACL rationale + token lifecycle + CSRF scope + escape-hatch semantics |
| 12.A.1 | Token package | `crypto/rand` 32 bytes, base64url, atomic tmp+rename, read-if-exists persistence |
| 12.A.2 | Windows DACL | Split `auth_file_windows.go` (DACL SID restrict, deny-by-default) + `auth_file_other.go` (Unix 0600); tiered CI+integration tests |
| 12.A.3a | BearerAuthMiddleware | chi middleware, `crypto/subtle.ConstantTimeCompare`, 401 with `hint` field guidance |
| 12.A.3b | Middleware wiring | Global `r.Use` → explicit `/api/v1` groups; auth first (cheap 401), then csrf; CSRF scope narrowed to `/api/v1` |
| 12.A.3c | MCP transport policy | loopback-only default; Bearer when `allow_remote=true`; 8-case policy matrix. Open question resolved 2026-04-18 — Claude Desktop/Code/Cursor all support custom `Authorization` headers via `mcpServers[].headers` in local config files (MCP spec 2025-03-26+). T12A.3c is single-path. |
| 12.A.3d | `/logs` SSE auth | Wrap SSE group with `auth.Middleware` before `middleware.Throttle(20)` (auth-first DoS hardening) |
| 12.A.4 | Startup guards | Refuse `--no-auth + allow_remote` without `MCP_GATEWAY_I_UNDERSTAND_NO_AUTH=1`; Bearer-without-TLS WARN |
| 12.A.5 | Atomic token write ordering | `LoadOrCreate` before `http.Server.Serve`; token file mtime < first request |
| 12.A.6 | Go auth helper | `internal/auth/client.go` `BuildHeader(pathOrEnv)`; shared by daemon self-test + mcp-ctl |
| 12.A.7 | mcp-ctl auth wiring | Bearer on all HTTP subcommands; `MCP_GATEWAY_AUTH_TOKEN` env override; 401 error messaging |
| 12.A.8 | Extension `GatewayClient` auth | Read token (env > file), bounded ENOENT retry (5×200ms), `buildAuthHeader()` shared helper |
| 12.A.9 | Extension `LogViewer` auth | Import `buildAuthHeader()` from T12A.8; attach header to `/logs` requests |
| 12.A.10 | Auth token path setting | `mcpGateway.authTokenPath` in `package.json`; platform-resolved default |
| 12.A.11 | First-start migration UX | Token generation on fresh install; permission-wrong WARN (no auto-fix); extension reload-token on 401 |
| 12.A.12 | Logging hygiene pass | Grep for token/Authorization leaks; redact to `Bearer ***`; capture-logs test |
| 12.A.13 | Auth integration test matrix | All routes: 401 without Bearer, 200 with; csrf+auth ordering; intentional non-coverage docs (`/mcp`, `/sse`, `/api/*` redirect exempt) |
| 12.A.GATE | Per-Phase Gate | `go test ./...` + `go vet ./...` + `npm test` + PAL codereview+thinkdeep (zero MEDIUM+) + `npm run deploy` + VSIX commit |

**Phase 12.B — KeePass Credential Push (TS extension + 2 Go tweaks)**

| # | Task | Description |
|---|------|-------------|
| 12.B.1 | `--json` flag | `mcp-ctl credential import --json` outputs stable JSON contract; golden test |
| 12.B.2 | `--password-stdin` flag | Mutual-exclusive with `--password-file`; no-TTY stdin password piping; clears buffer |
| 12.B.3 | `keepass-importer.ts` | `child_process.execFile` with argv-array, 1MB maxBuffer; never log stdout/stderr |
| 12.B.4 | SecretStorage dual-write | Partial-failure aware; merge-on-write `_addToIndex` for multi-window safety (concurrent test required) |
| 12.B.5 | Command + settings registration | `package.json` `mcpGateway.importKeepassCredentials` + `keepassPath`/`keepassGroup` settings |
| 12.B.6 | Extension E2E test | Full flow: KeePass → `mcp-ctl --json` → token (T12A.8) → PATCH authed → SecretStorage multi-window scenario |
| 12.B.GATE | Per-Phase Gate | `go test ./...` + `go vet ./...` + `npm test` + PAL codereview+thinkdeep (zero MEDIUM+) + CHANGELOG.md v1.2.0 entry + `npm run deploy` + VSIX commit |

### Phase 13 — Security Hardening (v1.3.0) ✅ COMPLETE

Commits `a845a78` (initial) + `c242b35` (PAL gate fixes — 6 HIGH + 2 MEDIUM resolved: manager error-path process groups, Stop/session deadlock guard, watcher Rename/Remove + loop-var capture, IPv6 bind address, JWT redaction pattern, context-preserving redaction, comment accuracy).

| # | Task | Description |
|---|------|-------------|
| 13.1 | ✅ POSIX process groups (F-5) | `Setpgid=true` + group-aware SIGTERM/SIGKILL helpers, applied to Stop() and both error paths; Windows unchanged (Job Objects already handle grandchildren). |
| 13.2 | ✅ Config watcher race (F-6) | `sync.Mutex` around `onChange` with double-checked `ctx.Err()`; also handles Rename/Remove for atomic-save editors; regression test asserts peak in-flight = 1. |
| 13.3 | ✅ TLS support (F-7) | `GatewaySettings.TLSCert/KeyPath`; `ServeTLS` branch; **non-loopback + Bearer + no TLS refuses to start** (cleartext tokens impossible by design). IPv6 bind address now handled via `net.JoinHostPort`. |
| 13.4 | ✅ Log redaction (F-9) | `internal/logbuf/redact.go` ordered-match pipeline: Authorization Bearer, bare Bearer, api/access/secret/auth keys, password, AWS AKIA, GitHub PAT (ghp/gho/ghu/ghs/ghr), JWTs (three-segment), generic 32+ char base64url. Context-bearing patterns preserve field name. Ring.Write scrubs on entry — SSE and history both sanitised. 10 regression tests. |

### Phase 14 — Community & CI (v1.4.0) ✅ COMPLETE

Shipped in commit `29e6fc2`: `SECURITY.md` (responsible disclosure, 30-day target, scope boundaries), `.gitleaks.toml` (project-specific allowlist + committed-token rule), `gitleaks` job in `.github/workflows/ci.yml`, README Security section updated for auth / TLS / redaction / KeePass import.

Deferred work split into dedicated plans:
- `docs/PLAN-catalogs.md` (v1.5.0) — server catalog, command catalog, catalog browse in Add Server webview, slash-command template enrichment. **Phase CA complete 2026-04-19** — schemas (server + command, draft-07, $id v1), 5+5 seed entries, ajv strict-mode loader (TOCTOU-safe via `fs.promises.open` + bounded read, never throws, 1 MiB cap), 17 mocha tests, plain-JS cross-ref script, `lint:catalog` script, CI step (lint + VSIX-contents check), `.vscodeignore` negations for ajv runtime deps. Gate [thinkdeep PASS / codereview fallback to Sonnet → 0 blocking after fixes]. Remaining: CB (Add Server browse webview), CC (slash-command template enrichment), CD (release).
- `docs/PLAN-v15.md` (v1.5.0 tail) — LOW findings from 12.A / 13 PAL reviews (ConstantTimeCompare length, Scanner 64KB limit) + TLS self-signed integration test + Windows DACL enforcement-tier runner. **Phase 15.A complete 2026-04-19** — T15A.1 ConstantTimeCompare hygiene refactor + T15A.2a/b scanner 1MB cap (atomic pair, F-11 closed). Gate [C+O] PASS, 0 blocking findings, 446 tests pass (+3 new). Remaining: 15.B (TLS integration tests + half-configured refusal), 15.C (Windows DACL enforcement tier), 15.D (release + docs).

---

## Roadmap — v1.4.0 shipped

PLAN-main.md is complete as of commit `29e6fc2`. v1.4.0 delivers Bearer auth (12.A), KeePass credential push (12.B), security hardening (13), and community/CI foundations (14).

Next plans:
- `docs/PLAN-catalogs.md` — v1.5.0 catalog UX
- `docs/PLAN-v15.md` — v1.5.0 tail items (LOW fixes + integration-test tiers)


| # | Task | Description |
|---|------|-------------|
| 14.1 | SECURITY.md | Responsible disclosure policy for contributors. |
| 14.2 | gitleaks in CI | Automated secret scanning in GitHub Actions on every push. |
| 14.3 | Server catalog | Built-in catalog of popular MCP servers (context7, pal, etc.) with auto-fill in Add Server. |
| 14.4 | Slash command catalog | Library of ready-made slash commands for common MCP servers. |

Detailed plan: `docs/PLAN-main.md` (audited [C+O] 2026-04-10, 0 MEDIUM+).
Full codebase audit: `docs/REVIEW-main.md` (audited [C+O] 2026-04-14, 13 findings fixed, 11 deferred to planned phases).

### Phase 11 Completion Summary (2026-04-16)

- **Status:** Phase 11 (Extension UX v1.1.0) — ALL sub-phases COMPLETE. Final commits: `8650505` (11.A) → `6d059e2` (11.B) → `ec3aae7` (11.C) → `44f0d7a` (11.D) → `991121f` (11.E) → `64f6057` (ROADMAP/REVIEW update). 11.8 audit 2026-04-16.
- **Unpushed commits on `main`:** `8650505`, `2a10e8b`, `6d059e2`, `ec3aae7`, `44f0d7a`, `991121f`, `64f6057` — push to GitLab (`git push origin main`) to publish v1.1.0 work.
- **Next plan:** Phase 12 (Auth + KeePass) — run `/phase 12` to create detailed task breakdown, then `/run main`.
- **PAL MCP status:** unavailable in this project; all cross-validation runs via sonnet sub-agent fallback per CLAUDE.md.
- **Known test-infra debt:** 31 pre-existing `GatewayClient` + `LogViewer` test failures need a local mock HTTP server — tracked as out-of-phase cleanup, not a Phase 11 blocker. The rebuilt VSIX (`vscode/mcp-gateway-dashboard/mcp-gateway-dashboard-latest.vsix`) must be installed manually via VSCode UI because `code --install-extension` fails on this machine's PATH.

---

## Known Limitations (LOW, documented)

| ID | Description |
|----|-------------|
| F-10 | KeePass master password in Go string cannot be zeroed due to GC — acceptable for localhost. |
| F-11 | ~~bufio.Scanner 64KB line limit for stderr~~ — CLOSED 2026-04-19 in Phase 15.A (T15A.2a + T15A.2b). Both scanner sites (SSE client + stderr producer) raised to 1MB atomically; regression tests pin the cap. End-to-end log-line ceiling is now 1MB. |
| RF-3 | Gateway crash = all MCP servers lost — HA/clustering not planned (localhost daemon). |
