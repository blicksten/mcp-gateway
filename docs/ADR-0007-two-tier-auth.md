# ADR-0007 — Two-tier auth for daemon-control endpoints

**Status:** Accepted (2026-05-08)
**Supersedes:** none
**Related:** ADR-0003 (Bearer token auth)

## Context

The daemon's HTTP API exposes two semantically distinct endpoint classes:

1. **MCP/REST routes** — server CRUD, tool invocation, health, version, etc. Used by:
   - The mcp-gateway VS Code extension (operator's dashboard)
   - mcp-ctl (operator CLI)
   - Claude Code's plugin system (reads `~/.claude/plugins/cache/mcp-gateway-local/.mcp.json` → resolves `${user_config.auth_token}`)
   - **VS Code 1.119+ built-in `McpGatewayService`** (auto-introduced by VS Code as part of the platform's MCP support)

2. **Daemon-control routes** — currently `/api/v1/shutdown`. Used by:
   - The mcp-gateway VS Code extension (user-initiated "stop daemon" command)
   - mcp-ctl (`mcp-ctl daemon shutdown`)

Pre-MCPR.3, both classes were gated by a single `Bearer` token (`auth.Middleware`). VS Code 1.119's `McpGatewayService` reads the same token from `.mcp.json` and POSTs `/api/v1/shutdown` on every window close — this was diagnosed as **Bug A** in `claude-team-control:docs/spikes/2026-05-08-mcp-infrastructure-resilience-meta.md` (the daemon-shutdown cascade that triggers Bug A.2's MCP transport orphaning).

## Decision

Daemon-control endpoints are gated by a **separate admin Bearer token** that is structurally inaccessible to MCP clients.

### Three invariants

1. **Exclusive scope.** `/api/v1/shutdown` (and any future daemon-control route) accepts the admin token *only*. The regular Bearer is rejected with 401 (RFC 6750 §3.1 — request fails authentication for the admin scope, not authorization). Symmetrically, the admin token does NOT satisfy regular routes.

2. **File-based bootstrap, plugin-manifest invariant.** Admin token lives at `~/.mcp-gateway/admin.token` (atomic write, mode 0600 / Windows DACL — same pattern as `auth.token`). The plugin manifest template (`plugin/.mcp.json`) substitutes only `${user_config.auth_token}` — the admin token is **never** substituted into client-facing config. Therefore VS Code 1.119's `McpGatewayService`, which acquires its Bearer via the plugin manifest, has no path to discover the admin token.

3. **Audit-logging at router root, before auth.** A pre-auth router middleware logs every `POST /api/v1/shutdown` attempt (including auth-rejected ones) at INFO with `remote`, `user_agent`, `path`. This is the in-production verification path: after MCPR.3 rolls out, the audit log SHOULD show a spike of auth-rejected attempts (VS Code 1.119 still trying with the regular Bearer) followed by a drop as the platform's behavior settles.

### Token shape and discovery (parallel to ADR-0003)

- Env override: `MCP_GATEWAY_ADMIN_TOKEN` (parallel to `MCP_GATEWAY_AUTH_TOKEN`)
- File path: `~/.mcp-gateway/admin.token` (parallel to `~/.mcp-gateway/auth.token`)
- Discovery ladder: env > file > `ErrNoAdminToken`
- Token shape: identical to regular (32 random bytes → 43 base64url chars, `looksLikeToken` reused)

### Refactor of the regular middleware

`auth.Middleware` and `auth.AdminMiddleware` share a single `bearerMiddleware(scope, expected, logger, hint)` helper. The `scope` label is written to the structured-log `scope` field on 401 so operators can grep regular vs admin rejections. The `hint` field of the 401 body is scope-specific so callers know which token shape (env var + file path) to provide.

## Consequences

### Positive

- **Bug A closed at source.** VS Code 1.119's window-close cascade can no longer reach `/api/v1/shutdown`.
- **Defense in depth.** Two token compromises required to fully control the daemon.
- **Audit trail.** Pre-auth shutdown logging gives operators a numeric signal that the new policy is enforced.
- **No breaking change for current MCP clients.** `${user_config.auth_token}` remains the regular Bearer; tools and resources continue to work unchanged.

### Negative / migration concerns

- **mcp-ctl shutdown migration.** Pre-existing `mcp-ctl daemon shutdown` callers using the regular Bearer will get 401 against an MCPR.3 daemon. `cmd/mcp-ctl/main.go` is updated in this commit to use the admin provider with regular-Bearer fallback for legacy daemon compatibility. Operators who run mcp-ctl on a different machine than the daemon need to copy `admin.token` (same operational story as `auth.token`).
- **Missing admin.token at extension startup.** A user who installs the extension before the daemon has ever run sees a one-time warning ("admin token not found...") and the dashboard's daemon-control command fails until the daemon generates the file. All other dashboard functions are unaffected. Extension uses an isolated `_adminTokenCache` slot so this state does not pollute the regular-path cache.
- **Documentation coupling.** Two places now describe the auth model: ADR-0003 (regular) and this ADR (admin). Future Bearer-related changes must update both.

### Rejected alternatives

- **Layered scope** (admin token also satisfies regular Bearer): rejected because admin-token leak would cascade into full regular-route access. Exclusive scope keeps the blast radius contained.
- **Single token + permission claim in the token body** (e.g., JWT with `scope: admin` claim): rejected because it adds JWT validation infrastructure for a single bit of state and makes the token revocable only via re-issue rather than file rotation. The two-file design is simpler and matches the existing `auth.token` ergonomics.
- **KeePass / VS Code SecretStorage as the canonical admin-token store**: deferred to a future phase. MCPR.3 ships file-based bootstrap because (a) it parallels the existing `auth.token` pattern (least-surprise), (b) it works without unlocking KeePass at extension startup (which would block dashboard initialization), and (c) the file-mode 0600 / Windows DACL already provides per-user isolation.

## Verification

| Test | What it proves | Location |
|------|---------------|----------|
| `TestAdminMiddleware_401WhenRegularBearer` | Regular Bearer cannot satisfy admin scope | `internal/auth/admin_test.go` |
| `TestShutdown_RegularBearer_Returns401` | End-to-end: regular Bearer on `/shutdown` → 401 | `internal/api/server_test.go` |
| `TestShutdown_AdminBearer_OnRegularRoute_Returns401` | Admin token cannot satisfy regular routes (exclusive scope) | `internal/api/server_test.go` |
| Extension Jest suite — `admin auth (MCPR.3)` describe block | Extension's gateway-client routes shutdown via admin provider | `vscode/mcp-gateway-dashboard/src/test/gateway-client.test.ts` |
| Extension Jest suite — `admin token (MCPR.3)` describe block | Admin token resolution honors scope-isolation invariants | `vscode/mcp-gateway-dashboard/src/test/auth-header.test.ts` |

Manual GATE MCPR.3 smoke (operator-run): close 5 VS Code windows in sequence; observe daemon process stays alive (PID unchanged); audit log shows VS Code's `shutdown REST request received` entries with the regular Bearer being rejected at `auth: rejected request scope=admin reason=mismatch`.

## Related plans

- `claude-team-control:docs/PLAN-mcp-resilience.md` Phase MCPR.3 — task breakdown
- `claude-team-control:docs/spikes/2026-05-08-mcp-infrastructure-resilience-meta.md` — Bug A diagnosis
- `claude-team-control:base/CLAUDE.md` MCP Emergency Runbook — Symptom A section (post-MCPR.3 update)
