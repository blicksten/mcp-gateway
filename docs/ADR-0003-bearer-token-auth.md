# ADR-0003 — Bearer Token Authentication for mcp-gateway

**Status:** Accepted (2026-04-18)
**Phase:** 12.A (v1.2.0)
**Authors:** Porfiry [Opus 4.7]
**Supersedes:** — (first ADR in the repo)
**Related:** Phase 13.B (TLS + log redaction — future), Phase 11.E (slash command generator — settles `~/.mcp-gateway` layout)

---

## 1. Context

mcp-gateway ships as a localhost HTTP daemon exposing a REST API (`/api/v1/*`), two MCP transports (`/mcp`, `/sse`), and a SSE log-stream endpoint (`/api/v1/servers/{name}/logs`). Prior to v1.2.0 the daemon has **no authentication**: any process on the host can invoke mutating endpoints (`POST`, `PATCH`, `DELETE`), read server inventory, and stream child-process stderr logs. Until v1.1.x this was acceptable under a loopback-only threat model; Phase 12.A introduces Bearer-token authentication so operators can safely enable `allow_remote=true` for multi-host deployments and so local agents invoking MCP tools against the daemon carry an explicit credential.

Three independent consumers will read the token from the host:
1. the daemon's own self-test path and `/health` introspection hook;
2. the `mcp-ctl` Go CLI (single-point injection in `internal/ctlclient/client.go:doRequest`);
3. the VS Code extension — two independent HTTP callers — `GatewayClient` (REST API) and `LogViewer` (raw `http.request` to the SSE logs endpoint).

All three must share one `buildAuthHeader()` contract; implementations diverge by language (Go helper `internal/auth/client.BuildHeader` vs. TypeScript `src/auth.ts` `buildAuthHeader()`). The contract is: read `MCP_GATEWAY_AUTH_TOKEN` env var first; if unset, read from `~/.mcp-gateway/auth.token`; error if neither is present.

---

## 2. Decision

### 2.1 §policy-matrix — route authentication policy

All routes fall into exactly one of four categories. The matrix is the single source of truth for middleware placement and is tested exhaustively in `T12A.13`.

| Category | Routes | Middleware | Rationale |
|----------|--------|-----------|-----------|
| **Public** | `GET /api/v1/health`, `GET /api/v1/version` | none (Bearer not required) | Health probes and version discovery must work without credentials — monitoring, container orchestration, and the extension's first-start handshake depend on them. |
| **Authed REST** | `GET / POST / PATCH / DELETE /api/v1/*` except public routes above, including **`GET /api/v1/servers/{name}/logs`** (SSE) | `BearerAuthMiddleware` + `csrfProtect` | Every other `/api/v1` route mutates daemon state OR leaks inventory/credential metadata/log stream content. The SSE `/logs` endpoint is mounted in a **separate** `r.Group` at `server.go:105-108` with its own `middleware.Throttle(20)`; **`auth.Middleware(token)` is applied to that group BEFORE the throttle decrement** so unauthenticated callers are rejected without consuming a throttle slot (DoS hardening — T12A.3d). |
| **MCP transport** | `/mcp`, `/mcp/*`, `/sse`, `/sse/*` | `gateway.auth_mcp_transport` policy — see §policy-matrix-mcp-modes below | These are router-level mounts (`server.go:130-133`) that accept tool calls with side effects. They carry MCP protocol payloads, not REST — CSRF does not apply (see §csrf-scope); Bearer auth is gated by a dedicated flag to accommodate loopback-only default + remote-Bearer mode. |
| **Not CSRF-protected** (see §csrf-scope) | `/mcp*`, `/sse*`, `/api/*` redirect | Bearer-gated as above, but no `csrfProtect` | CSRF's threat model is browser-cookie session-riding, which does not apply to daemon-to-daemon Bearer. |

### 2.1.1 §policy-matrix-mcp-modes — `gateway.auth_mcp_transport`

New config flag `gateway.auth_mcp_transport` (string enum, default `loopback-only`):

- **`loopback-only`** (default) — the handler refuses any request whose `RemoteAddr` is not a loopback address with `403 transport_policy_denied`. Bearer header is neither required nor checked. This is safe for every MCP client regardless of header support. First-run defaults are conservative.
- **`bearer-required`** — `BearerAuthMiddleware` is applied to the `/mcp` and `/sse` groups. Requires `allow_remote=true` in the gateway config (otherwise the daemon refuses to start with a clear error). Clients must inject `Authorization: Bearer <token>` (all three major clients — Claude Desktop, Claude Code, Cursor — support this via `mcpServers[].headers` in their respective config files; README ships verified snippets — T12A.11).

**Client compatibility** (settled 2026-04-18 via web research — see `docs/PLAN-main.md:141`):

| Client | Mechanism | Config location |
|--------|-----------|-----------------|
| Claude Code CLI | `claude mcp add --transport http <name> <url> --header "Authorization: Bearer $TOKEN"` | N/A (stored in `~/.claude/mcp-servers.json`) |
| Claude Desktop | `"mcpServers": { ... "headers": { "Authorization": "Bearer ${env:TOKEN}" } }` | `claude_desktop_config.json` |
| Cursor | same `headers` shape | `~/.cursor/mcp.json` or `.cursor/mcp.json` |

**Known limitation:** the claude.ai web-UI advanced-settings dialog for remote connectors supports only OAuth client id / secret, not a flat Bearer ([anthropics/claude-ai-mcp#112](https://github.com/anthropics/claude-ai-mcp/issues/112)). This does **not** affect local Claude Desktop / Code / Cursor (file-based config). The MCP specification 2025-03-26+ streamable-HTTP transport officially supports the `Authorization` header per request.

### 2.2 §token-lifecycle

**Generation.** The token is 32 bytes of `crypto/rand` encoded as base64url (43 ASCII characters, no padding). The base64url alphabet `[A-Za-z0-9_-]` is by construction safe for HTTP header values (no CRLF, no NUL, no whitespace), so there is no injection or parsing ambiguity.

**Persistence.** The daemon calls `LoadOrCreate` at startup **before** `http.Server.Serve` binds the listener (T12A.5 — ordering is a HIGH audit finding). `LoadOrCreate` reads `~/.mcp-gateway/auth.token`; if present and the content is a well-formed base64url token of length ≥ 43 characters, it is reused verbatim. If absent or malformed, a new token is generated and written atomically via temp-file + `fsync` + `rename` (POSIX `rename`; on Windows `os.Rename` maps to `MoveFileEx` with `MOVEFILE_REPLACE_EXISTING`, which is atomic across processes on the same volume). The token file's permissions are set per-platform — see §dacl-rationale below.

**Format (L-2 — mandatory):** the token file format is a bare base64url string with **no version field**. Future format changes (rotation metadata, per-client tokens, signed envelopes) therefore require a coordinated client/daemon upgrade — older clients cannot negotiate an unknown structured payload. A **format version was considered** — specifically a prefix `v1:<token>` — and deferred to Phase 15+: adopt it if and when structured metadata becomes needed. Until then, `LoadOrCreate` detects "looks like a token" by length + base64url character set, with no format version parsed from the file contents.

**Env override.** `MCP_GATEWAY_AUTH_TOKEN` overrides the file for **all three clients** (`mcp-ctl`, extension `GatewayClient`, extension `LogViewer`) and for the daemon's own self-test path. When set, the value is used verbatim as the Bearer token and the file is not read. Semantics: ephemeral — the override does NOT write to disk. When the env var is set on the **daemon**, the daemon ignores the file entirely and uses the env value as its canonical token (this is the CI / container-ephemeral path). When the env is set on a client, the client uses the env value as its Bearer header and does not touch the file. Operators who need persistent tokens rotated out-of-band can set the env var on the daemon and reset it at restart; clients discover the current token by reading the file (the usual path) OR by receiving the env value via deployment config.

**Rotation — out of scope.** Phase 12.A **does not** implement rotation. The daemon generates a token at first start and persists it; subsequent starts reuse it unless the file is deleted or `MCP_GATEWAY_AUTH_TOKEN` overrides it. Live rotation (e.g., generate a new token while running, notify all clients, invalidate the old token) is explicitly deferred to a future phase (Phase 15+ once operator demand surfaces). The format-version gap (§token-lifecycle above) is linked to this: once rotation arrives, either the file format bumps to include a key-id, or the daemon runs a two-key overlap window — that decision is deferred.

### 2.3 §dacl-rationale — Windows DACL vs POSIX 0600

The token file must be unreadable by **other local users** on multi-user machines. On POSIX this is achieved via `os.Chmod(0600)`. On Windows `os.Chmod(0600)` is a no-op (Windows does not honour the POSIX mode bits; `os.File.Chmod` on Windows only toggles the read-only bit). A naive cross-platform implementation would leave the token world-readable on Windows — a CRITICAL finding (12A-3) during planning.

**Decision:** split the implementation along build tags, matching the existing `procattr_windows.go` / `procattr_other.go` pattern already in the repo.

- `auth_file_other.go` (`//go:build !windows`) — `os.OpenFile(path, O_CREATE|O_WRONLY|O_TRUNC, 0600)` + `os.Chmod(0600)` as defence-in-depth against umask.
- `auth_file_windows.go` (`//go:build windows`) — uses `golang.org/x/sys/windows` to construct an explicit DACL: one `ALLOW` ACE for the current-user SID only (`GetCurrentProcessToken` → `GetTokenInformation(TokenUser)` → `SidFromToken`). Inheritance flags are cleared. No ACE for `BUILTIN\Users` or `Everyone`. On startup and on every write, the DACL is **set then verified** by reading it back (structural check via `GetNamedSecurityInfo`); if the ACL is not as expected, the daemon refuses to start with a clear error.

**Testing (M-1 — tiered):** CI tier (default `go test` on `windows-latest`) runs a **structural** DACL verification that reads the ACL back and asserts the expected ACE shape — with a test comment explicitly noting this is structural, not enforcement. The integration tier (`//go:build integration`) runs a **real enforcement** test using `LogonUser` + `ImpersonateLoggedOnUser` to attempt a second-account file-open and assert `ACCESS_DENIED`; this requires a dedicated Windows runner where a second local account has been provisioned (`net user testuser Pass123! /add`). The GATE wording reads: "CI: structural passes; integration: enforcement passes on dedicated Windows runner OR explicitly deferred to release sign-off."

### 2.4 §csrf-scope — CSRF middleware scope change

**Before 12.A** (current code, `server.go:79`): `r.Use(csrfProtect)` applies at the **router root**, covering every route including `/mcp*`, `/sse*`, and the `/api/*` backward-compat redirect.

**After 12.A** (T12A.3b moves the `Use` call): `csrfProtect` is scoped to `r.Route("/api/v1", ...)` only. The routes currently covered by the root-level `csrfProtect` that will **no longer** be covered are:

- `/mcp`, `/mcp/*`, `/sse`, `/sse/*` — MCP transports
- `/api/*` backward-compat redirect (307 that forwards to `/api/v1`)

This is an **intentional scope reduction**, not a regression. Rationale:

- **MCP transports** use Bearer authentication (T12A.3c policy: loopback-only default or Bearer-when-remote) and are daemon-to-daemon, not browser-cookie-authenticated. CSRF defends against cross-origin cookie-bearing requests — a threat model that does not apply when there is no cookie and no browser in the call chain.
- **The `/api/*` redirect** is a 307 that preserves method and forwards to `/api/v1`, where `csrfProtect` applies downstream. CSRF is enforced at the destination, not at the redirect source. Short-circuiting the redirect with a CSRF check would block legitimate method-preserving clients for no security gain.

**Middleware ordering within the `/api/v1` group:** `BearerAuthMiddleware` runs **before** `csrfProtect`. Rationale: Bearer is cheap and decisive — a missing/invalid token returns `401` in constant time; `csrfProtect` then acts only on browser-bearing authenticated requests where the CSRF threat exists. Reversing the order would return `403` before `401` for unauthenticated browser requests, which is misleading for operators.

Tests in T12A.13 include three **intentional non-coverage** assertions:
1. `POST /mcp` with a cross-origin `Origin` header → no `403` from `csrfProtect` (the middleware never sees the request).
2. `POST /sse` same.
3. `POST /api/v1/servers` via `/api/*` redirect (307) → CSRF applies at `/api/v1` destination, not at the redirect source.

Each test references "ADR-0003 §csrf-scope" in a code comment so a future auditor sees the intent and does not re-flag it as a regression.

### 2.5 §no-auth-escape-hatch — `--no-auth` + `allow_remote` guard

Running with `--no-auth` disables `BearerAuthMiddleware` entirely. Combined with `allow_remote=true`, this exposes mutating endpoints and MCP tool calls to anyone on the network — a trivial RCE. **Decision:** the daemon refuses to start when both conditions hold, unless the environment variable `MCP_GATEWAY_I_UNDERSTAND_NO_AUTH=1` is also set.

Belt-and-braces semantics:
- `--no-auth` alone (with `allow_remote=false`) → allowed, silent (legitimate developer loopback).
- `--no-auth` + `allow_remote=true` + no env var → startup error naming both conditions and the env var, exit non-zero.
- `--no-auth` + `allow_remote=true` + `MCP_GATEWAY_I_UNDERSTAND_NO_AUTH=1` → startup proceeds; the daemon emits exactly three WARN lines (`AUTH DISABLED`, `network binding is not loopback`, `anyone on the network can mutate servers and invoke MCP tool calls`) and `/health` returns `"auth": "disabled"` so tooling can detect the state.

**Bearer-without-TLS warning (L-1).** When `allow_remote=true` and auth is **enabled** (not `--no-auth`) but TLS is not yet configured (pre-Phase 13.B), the daemon emits a fourth WARN at startup: `Bearer auth is active but TLS is not configured — token is transmitted in cleartext on public networks (Phase 13 adds TLS support)`. The wording is exact so test `T12A.13` can grep for it.

### 2.6 §401-hint — response body for authentication failure

When `BearerAuthMiddleware` rejects a request, the 401 JSON body is:

```json
{
  "error": "authentication required",
  "hint": "add Bearer token via MCP_GATEWAY_AUTH_TOKEN env var or ~/.mcp-gateway/auth-token file"
}
```

The `hint` field is additive — existing clients that parse only `{"error":"..."}` are unaffected. New clients and human operators see an actionable message. Test in `T12A.3a` asserts the body shape and that the hint mentions both the env var name and the file path.

### 2.7 §auth-header-fallback — consumer token discovery

All three consumers (daemon self-test, `mcp-ctl`, extension `GatewayClient` + `LogViewer`) discover the token via the same ladder:

1. `MCP_GATEWAY_AUTH_TOKEN` env var, if set and non-empty.
2. Otherwise, read `~/.mcp-gateway/auth.token` (path resolved via `config.DefaultConfigDir()` + `auth.token`).
3. Otherwise, error with a clear message pointing to both options.

The shared helper (`internal/auth/client.BuildHeader` in Go; `buildAuthHeader()` in TypeScript) is the single source of truth for this ladder; all consumers call it. **Never** put the token in the URL (query string, path segment, or fragment) — tokens in URLs leak through proxy logs, browser history, referrer headers, and shell history. The extension's `AddServerPanel` and the `mcp-ctl` help text both document this as a hard rule.

---

## 3. Alternatives considered

### 3.1 OAuth 2.0 device flow

Rejected for v1.2.0: mcp-gateway is a localhost daemon with no identity provider; OAuth requires an authorization server, token endpoint, refresh-token rotation, and PKCE handling. Implementation cost is an order of magnitude higher than a single file-based Bearer token, for no v1.2.0 gain. Phase 15+ may revisit this if multi-tenant deployments surface.

### 3.2 Mutual TLS (mTLS)

Rejected for v1.2.0: requires certificate issuance, rotation, and revocation plumbing that does not yet exist in the daemon. Phase 13.B will add server-side TLS; client certificates are deferred to Phase 15+ if operator demand surfaces.

### 3.3 Unix-socket-only mode (no network surface)

Rejected as the only mode: removes the ability to run MCP transports remotely, which is explicitly a supported use case (`allow_remote=true`). Considered as an additional `gateway.auth_mcp_transport=unix-socket` mode in §policy-matrix-mcp-modes — deferred because it requires a Windows Named-Pipes story and complicates client config for no immediate gain.

### 3.4 Token embedded in URL query string

Rejected on security grounds (see §auth-header-fallback last paragraph). Tokens in URLs leak through proxy logs, browser history, referrer headers, shell history, and process-list snapshots. Never an option.

### 3.5 Cookie-based session (with CSRF protection retained globally)

Rejected because mcp-gateway is daemon-to-daemon: there is no browser session to carry the cookie. Keeping global `csrfProtect` in the absence of cookies added zero security value and blocked legitimate non-browser clients. See §csrf-scope.

---

## 4. Consequences

### Positive

- Operators can enable `allow_remote=true` with confidence: all mutating endpoints require Bearer; MCP transports enforce a policy matrix; the `/api/*` boundary preserves CSRF where it matters.
- All three auth consumers share one contract (`buildAuthHeader()` / `BuildHeader`) — one place to fix bugs, one place to add features (e.g., token rotation later).
- Windows and POSIX file permissions are enforced per-platform with matching test rigour (structural DACL in CI, enforcement in integration).
- Bearer-without-TLS is explicitly flagged until Phase 13.B lands TLS; operators are not silently at risk.
- The `--no-auth` + `allow_remote` RCE trap is closed by refusal-to-start + belt-and-braces env var.

### Negative

- Phase 12.A is a **minor-version bump (v1.2.0)** that breaks v1.0.x clients hitting mutating endpoints without Authorization. The 401 `hint` field softens the UX but does not prevent the break. Users on older `mcp-ctl` / older extension must upgrade both together.
- Token rotation is explicitly not implemented — a compromised token cannot be revoked without manual file deletion + daemon restart + client re-read. Acceptable for v1.2.0; deferred to Phase 15+.
- The token file format is version-less (see §token-lifecycle), so any future format change requires a coordinated client/daemon upgrade. Mitigation: plan for this in Phase 15+; no action needed now.
- The `bearer-required` mode is remote-capable but cleartext until Phase 13.B. Explicit WARN at startup mitigates.
- The orchestrator-internal CV-gate SKIPs transient PAL errors (unrelated meta-issue tracked in `claude-team-control/docs/spikes/2026-04-17-cv-gate-rerun-and-retry.md`); this ADR's review trail relied on external PAL thinkdeep calls in the main session to compensate.

### Neutral

- `csrfProtect` is narrower (scope: `/api/v1` only). For legacy non-browser callers this is invisible; for browser callers hitting the REST API, CSRF still works as before.
- `MCP_GATEWAY_AUTH_TOKEN` env override bypasses the file on both daemon and client sides: CI-friendly, ephemeral, and explicit.

---

## 5. References

- `docs/PLAN-main.md` lines 115-354 — Phase 12 detailed task breakdown with 15-row traceability matrix (architect findings ↔ tasks).
- `docs/PLAN-main.md:141` — resolution of the "Authorization header support" open question (2026-04-18).
- `docs/spikes/2026-04-17-skip-quality-gate-loophole.md` and `docs/spikes/2026-04-17-cv-gate-rerun-and-retry.md` (claude-team-control) — CV-gate infrastructure meta-issues tracked separately.
- MCP specification: [streamable HTTP transport](https://modelcontextprotocol.io/specification/2025-03-26/basic/transports) — 2025-03-26 confirms `Authorization` header per request is part of the spec.
- [Claude Desktop / Code MCP connector docs](https://platform.claude.com/docs/en/agents-and-tools/mcp-connector), [Claude Code MCP guide](https://code.claude.com/docs/en/mcp), [Cursor MCP setup](https://www.truefoundry.com/blog/mcp-servers-in-cursor-setup-configuration-and-security-guide) — all three clients support `mcpServers[].headers` for custom `Authorization`.
- [anthropics/claude-ai-mcp#112](https://github.com/anthropics/claude-ai-mcp/issues/112) — known limitation of the claude.ai web-UI (does not affect local clients).
- Existing `internal/lifecycle/procattr_*.go` files — precedent for build-tagged per-platform file pairs used by T12A.2.

---

## 6. Status

**Accepted.** ADR-0003 is the authoritative policy document for Phase 12.A Bearer Token Auth. All implementation tasks in Phase 12.A (T12A.1 through T12A.13 + GATE) must link back to this ADR's section headings when referencing design decisions. Future phases that touch auth (Phase 13.B TLS, Phase 15+ rotation) must write follow-up ADRs or amend this one; they must not silently deviate.

— Porfiry [Opus 4.7]
