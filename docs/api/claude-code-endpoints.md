# Claude Code Integration REST Endpoints

> **Status: FROZEN — v1.6.0 contract, 2026-04-22.** This document is the
> cross-window bus between Phase 16.3 (server handlers, `internal/api/`
> + `internal/patchstate/`), 16.4 (webview patch, `installer/patches/`),
> 16.5 (VSCode dashboard panel), and 16.6.5 (compat-matrix endpoint).
> Field names, JSON shapes, enum values, HTTP status codes, and CORS /
> auth policy are locked. Additive changes (new optional fields, new
> endpoints) are permitted and do not break the freeze. Renames, type
> changes, removals, or shape changes require coordinated agreement
> across all four phase owners and a bump of the media type to
> `application/vnd.mcp-gateway.claude-code+json;v=2` before merge.

Added in Phase 16.3 (v1.6.0). All routes live under `/api/v1/claude-code/*`
and require `Authorization: Bearer <token>` — the same Bearer token that
gates the rest of the gateway's REST surface (see ADR-0003).

These endpoints are consumed by the Claude Code webview patch
(`installer/patches/porfiry-mcp.js`, Phase 16.4). The VSCode dashboard
(Phase 16.5) reads `GET /patch-status` for display purposes but never
posts.

## Authentication & CORS

| Aspect | Behavior |
|---|---|
| Scheme | `Authorization: Bearer <token>` (RFC 6750) |
| Missing/invalid token | 401 with `{"error":"authentication required","hint":"..."}` |
| Allowed origin | `vscode-webview://*` (exact echo on `Access-Control-Allow-Origin`) |
| Disallowed origin | No `Access-Control-Allow-Origin` header — browser blocks response |
| Preflight OPTIONS | 204, `Allow-Methods: GET, POST, OPTIONS`, `Allow-Headers: Authorization, Content-Type`, `Max-Age: 300` |
| csrfProtect | NOT applied — webview patch uses Bearer, not cookies |

Preflight runs **before** bearer auth because browsers don't attach
`Authorization` to `OPTIONS` requests. Unknown origins still get a 204
for preflight, but without the `Allow-*` headers the client-side fetch
is blocked — this denies by omission rather than an explicit 403.

## Rate Limits

| Endpoint | Key | Budget |
|---|---|---|
| `POST /patch-heartbeat` | `session_id` (from body) | 5 req/min |
| `GET /pending-actions` | client IP | 60 req/min |
| `GET /patch-status` | client IP | 60 req/min |
| `POST /pending-actions/{id}/ack` | — | unlimited |
| `POST /probe-result` | — | unlimited |

Limiters are in-memory token buckets; idle buckets are evicted once the
bucket count grows past a soft threshold.

## Endpoints

### `POST /api/v1/claude-code/patch-heartbeat`

Patch → gateway. Reports the live state of the webview patch. Gateway
stores the latest per `session_id` with 1 h TTL; the dashboard and probe
flows read it back.

**Request body:**

```json
{
  "session_id": "sess-abc-123",
  "patch_version": "1.0.0",
  "cc_version": "2.0.0",
  "vscode_version": "1.90.0",
  "fiber_ok": true,
  "mcp_method_ok": true,
  "mcp_method_fiber_depth": 2,
  "last_reconnect_latency_ms": 5404,
  "last_reconnect_ok": true,
  "last_reconnect_error": "",
  "pending_actions_inflight": 0,
  "fiber_walk_retry_count": 0,
  "mcp_session_state": "ready",
  "ts": 1713715200
}
```

**Field semantics (locked):**

| Field | Type | Values / constraints |
|---|---|---|
| `session_id` | string | Non-empty; unique per VSCode window lifetime. Drives per-session rate-limit bucket + TTL eviction key. |
| `mcp_session_state` | enum | `unknown` \| `discovering` \| `ready` \| `lost`. FSM defined in PLAN-16 T16.4.3 / P4-02. Gateway accepts any of the four verbatim; any other value is a protocol violation but MUST NOT 4xx (log + store). |
| `last_reconnect_ok` | bool | `true` / `false` = outcome of the last `reconnectMcpServer` call since previous heartbeat. A heartbeat with no attempt since last beat uses `false` with empty `last_reconnect_error` — treat this as "idle / neutral" for the P4-05 counter-reset rule. |
| `last_reconnect_error` | string | **Pre-scrubbed on the patch side** (SP4-M2): truncated to 256 chars, filesystem paths replaced with `<path>`, stack-trace `at …` frames stripped. Gateway does NOT re-scrub; logs the received string verbatim. Empty string when `last_reconnect_ok: true`. |
| `last_reconnect_latency_ms` | int | Wall-clock from `reconnectMcpServer` invocation to promise settle. `0` when no reconnect attempted since previous heartbeat. |
| `mcp_method_fiber_depth` | int | `0` when fiber walk hasn't yet located the method; else measured depth. Expected ≤ `max_accepted_fiber_depth` from compat-matrix. |
| `fiber_walk_retry_count` | int | Walks attempted since last successful discovery. Drives dashboard Mode D (P4-09, requires ≥ 5 + `mcp_session_state != "ready"` across 3 consecutive beats). |
| `pending_actions_inflight` | int | Count of actions received but not yet acked, PLUS entries queued in patch's `awaitingDiscovery` FIFO (bound `AWAITING_DISCOVERY_QUEUE_MAX = 16`). |
| `patch_version` | string | Semver of the inlined `porfiry-mcp.js` at apply-time. |
| `cc_version` / `vscode_version` | string | Semver. Gateway compares `cc_version` against compat-matrix `alt_e_verified_versions` for dashboard YELLOW. |
| `ts` | int64 | Client wall clock, ms since epoch. Informational; TTL eviction uses server-side `received_at`. |

**Response 200:**

```json
{
  "acked": true,
  "next_heartbeat_in_ms": 60000,
  "config_override": {
    "LATENCY_WARN_MS": 3000,
    "DEBOUNCE_WINDOW_MS": 10000,
    "CONSECUTIVE_ERRORS_FAIL_THRESHOLD": 3
  }
}
```

`config_override` is optional. If present, the patch merges into its
in-memory `CONFIG` after validating each value against hard-bounded ranges
(Phase 16.4 T16.4.7 §b). Omitted in the default gateway build; enabled via
config.json in future maintenance releases without re-patching.

**Accepted ranges (hard-bounded, SP4-L2).** The patch rejects out-of-range
values silently, keeps its compiled-in default, and logs the rejection to
`[Copy diagnostics]`. Whenever any override is in effect, the dashboard
surfaces an advisory banner so operators can trace unusual UX back to the
pushed value.

| Key | Range | Patch default | Rationale |
|---|---|---|---|
| `LATENCY_WARN_MS` | `[5000, 300000]` | 30000 | Below p50 reconnect latency (~5400 ms) the warn becomes meaningless noise |
| `DEBOUNCE_WINDOW_MS` | `[2000, 60000]` | 10000 | Window shorter than reconnect latency saturates the API and interrupts active Claude turns (R-3) |
| `CONSECUTIVE_ERRORS_FAIL_THRESHOLD` | `[2, 20]` | 3 | A single transient error flipping RED is user-hostile |

**Errors:**

| Status | Reason |
|---|---|
| 400 | Missing `session_id`, malformed JSON |
| 401 | Missing/invalid Bearer |
| 429 | Per-session rate limit exceeded (`Retry-After: 60`) |
| 503 | Patch state subsystem not initialized |

### `GET /api/v1/claude-code/patch-status`

Gateway → dashboard. Returns all active (non-expired) heartbeats.

**Response 200:**

```json
[
  { "session_id": "sess-abc-123", "patch_version": "1.0.0", "fiber_ok": true, ... }
]
```

### `GET /api/v1/claude-code/pending-actions`

Patch → gateway. Returns undelivered actions in FIFO order.

**Query parameters:**

| Name | Type | Description |
|---|---|---|
| `after` | string (action id) | Return only actions created after the given id. Used for at-most-once polling. |

**Response 200 — reconnect action (production):**

```json
[
  {
    "id": "7f3a2b1c...",
    "type": "reconnect",
    "serverName": "mcp-gateway",
    "nonce": "9e8d7c6b...",
    "created_at": "2026-04-21T22:15:00Z",
    "delivered": false
  }
]
```

**Response 200 — probe-reconnect action (dashboard [Probe reconnect]):**

```json
[
  {
    "id": "abc123...",
    "type": "probe-reconnect",
    "serverName": "__probe_nonexistent_9e8d7c6b...",
    "nonce": "9e8d7c6b...",
    "created_at": "2026-04-21T22:15:00Z",
    "delivered": false
  }
]
```

Scoping invariant (P4-08): `reconnect` actions always target
`serverName="mcp-gateway"` — our plugin's aggregate entry. Per-backend
reconnects are out of scope.

### `POST /api/v1/claude-code/pending-actions/{id}/ack`

Patch → gateway. Confirms action was executed (idempotent).

**Request body:**

```json
{
  "ok": false,
  "error_message": "Server not found: __probe_nonexistent_9e8d7c6b...",
  "action_type": "probe-reconnect",
  "latency_ms": 312
}
```

| Field | Type | Notes |
|---|---|---|
| `ok` | bool | Outcome of the underlying `reconnectMcpServer(serverName)` promise. For `action_type: "probe-reconnect"` the EXPECTED value is `false` carrying the `"Server not found: …"` rejection — that is the GREEN-success path for the dashboard probe (the round-trip succeeded; only the fabricated server name was correctly rejected). |
| `error_message` | string | Optional. Pre-scrubbed identically to `Heartbeat.last_reconnect_error` (SP4-M2). Empty when `ok: true`. |
| `action_type` | enum | `"reconnect"` \| `"probe-reconnect"` — echo of the original action's `type`. Lets the dashboard distinguish "real reconnect succeeded" from "probe rejected (expected)". |
| `latency_ms` | int | Wall-clock ms from patch calling `reconnectMcpServer` to promise settle. |

Ack is fire-and-forget from the patch side: the POST is sent regardless of
the resolve/reject outcome of the underlying call. A missing ack leaves
the action in the queue until TTL eviction (10 min).

**Response 200:**

```json
{ "status": "acked" }
```

**Errors:**

| Status | Reason |
|---|---|
| 400 | Empty `id` |
| 401 | Missing/invalid Bearer |
| 404 | Unknown action id |

### `POST /api/v1/claude-code/probe-result`

Patch → gateway. Reports `[Probe reconnect]` result (dashboard button).

**Request body:**

```json
{ "nonce": "9e8d7c6b...", "ok": false, "error": "Server not found: ..." }
```

For a healthy probe, `ok: false` with the expected "Server not found"
error is the success case — the reconnect round-trip worked, just the
fabricated server name was correctly rejected by Claude Code.

**Response 200:**

```json
{ "status": "recorded" }
```

**Errors:**

| Status | Reason |
|---|---|
| 400 | Empty `nonce`, malformed JSON |
| 401 | Missing/invalid Bearer |

### `POST /api/v1/claude-code/probe-trigger`

Dashboard → gateway. Invoked by the `[Probe reconnect]` button (T16.5.6).
Gateway enqueues a `probe-reconnect` action targeting a synthetic server
name `__probe_nonexistent_<nonce>`; the patch dequeues it and fires
`mcpSession.reconnectMcpServer(serverName)` which is expected to reject
with `"Server not found: …"` — the intentional negative path proving the
fiber walk + webview → gateway round-trip are healthy.

**Request body:**

```json
{ "nonce": "9e8d7c6b5a4f3e2d" }
```

| Field | Type | Notes |
|---|---|---|
| `nonce` | string | Dashboard-generated hex, ≥ 16 chars. Echoed on the subsequent `/probe-result` so the dashboard can correlate. |

**Response 200:**

```json
{ "status": "enqueued", "action_id": "abc123def456" }
```

**Errors:**

| Status | Reason |
|---|---|
| 400 | Empty or too-short `nonce`, malformed JSON |
| 401 | Missing/invalid Bearer |
| 503 | Patch state subsystem not initialized |

Dashboard timeout: if `/probe-result` with the matching `nonce` has not
arrived within 15 s, the dashboard shows `Timeout — patch not
responding (check heartbeat)`. The gateway does not time out on its own;
probe results are TTL-evicted after 5 minutes regardless.

### `POST /api/v1/claude-code/plugin-sync`

Dashboard / CLI → gateway. Thin wrapper around `TriggerPluginRegen`
(T16.2.4). Regenerates `<plugin-dir>/.mcp.json` from the current live
backend list and enqueues a `reconnect` action so the webview patch
picks up the new topology on its next poll. Idempotent — if no backends
changed, the regenerated file is byte-identical and the enqueue is
coalesced into the 500 ms server-side debounce window.

Callers: `[Activate for Claude Code]` dashboard button (T16.5.2);
`mcp-ctl install-claude-code` step 5 (T16.8.2); CI dogfood smoke
(T16.9.4.a).

**Request body:** empty (the in-memory backend list is authoritative).

**Response 200:**

```json
{
  "status": "synced",
  "mcp_json_path": "/home/alice/.claude/plugins/cache/mcp-gateway@mcp-gateway-local/.mcp.json",
  "entries_count": 3,
  "action_enqueued": true,
  "action_id": "7f3a2b1c9d8e4f0a"
}
```

| Field | Type | Notes |
|---|---|---|
| `mcp_json_path` | string | Absolute path of the regenerated file. Informational only — clients MUST NOT read it back; use `/pending-actions` for the reconnect signal. |
| `entries_count` | int | Count of MCP server entries written. Equals the count of `running` backends at regen time. |
| `action_enqueued` | bool | `false` when the 500 ms debounce coalesced this call into a prior enqueue. Clients treat both cases as success. |
| `action_id` | string | Present only when `action_enqueued: true`. |

**Errors:**

| Status | Reason |
|---|---|
| 401 | Missing/invalid Bearer |
| 409 | Plugin directory not configured (`GATEWAY_PLUGIN_DIR` unset at daemon start) |
| 500 | Disk write / atomic rename failed on `.mcp.json` |

### `GET /api/v1/claude-code/compat-matrix`

Dashboard → gateway. Returns the content of
`configs/supported_claude_code_versions.json` verbatim (T16.4.7, T16.6.5).
Single source of truth for "is this Claude Code version covered by Alt-E
live verification?". The dashboard consumes this rather than bundling its
own copy, so maintainer updates land with the next gateway release
without re-issuing the VSIX.

**Response 200:**

```json
{
  "min": "2.0.0",
  "max_tested": "2.5.8",
  "known_broken": [],
  "last_verified": "2026-04-21",
  "alt_e_verified_versions": ["2.1.114"],
  "observed_fiber_depths": { "2.1.114": 2 },
  "max_accepted_fiber_depth": 80,
  "observed_reconnect_latency_ms_p50": 5400,
  "observed_reconnect_latency_note": "Single-point measurement; expand to p50/p95/p99 post-ship."
}
```

| Field | Type | Notes |
|---|---|---|
| `min` / `max_tested` | string | Semver range the maintainer claims support for. |
| `known_broken` | string[] | Versions inside `[min, max_tested]` that the maintainer has verified do NOT work — dashboard shows RED. |
| `alt_e_verified_versions` | string[] | Versions where a live probe confirmed the fiber walk locates `reconnectMcpServer`. Versions outside this list render dashboard YELLOW. |
| `observed_fiber_depths` | object (version → int) | Per-version measured depth. Heartbeat `mcp_method_fiber_depth` should match ±0. |
| `max_accepted_fiber_depth` | int | Upper bound on the walk. Beyond this the patch gives up and reports `fiber_ok: false`. |
| `observed_reconnect_latency_ms_p50` | int | Post-ship this is computed from accumulated heartbeat telemetry (T16.4.7 pre-ship probe expansion). Post-ship an additional `_p95` field is added. |

**Errors:**

| Status | Reason |
|---|---|
| 401 | Missing/invalid Bearer |
| 500 | `configs/supported_claude_code_versions.json` missing from gateway install |

Read-only. Maintained in the repo and shipped with the gateway binary.

## Persistence & Durability

Heartbeats and the pending-action queue are persisted to
`~/.mcp-gateway/patch-state.json` (0600) on every mutation via atomic
`CreateTemp` + `rename(2)`. On daemon startup the file is loaded and
TTL-filtered (heartbeats > 1 h, actions > 10 min are dropped).

This closes the "pending reconnect lost on gateway restart" bug class
(REVIEW-16 M-01): if the operator `systemctl restart`s the daemon between
"user adds a backend" and "patch polls /pending-actions", the queued
reconnect survives the restart.

Heartbeat persistence is debounced per `session_id` (30 s) so steady-state
60-s heartbeats don't churn disk I/O.

## Action enqueue flow

```
backend mutation (POST /servers, DELETE /servers/{name}, PATCH Disabled toggle)
  → TriggerPluginRegen (regenerates .mcp.json)
  → patchstate.EnqueueReconnectAction(patchstate.AggregatePluginServerName)
                                                       ← 500 ms server-side debounce
  → patch polls /pending-actions, dequeues action
  → patch.session.reconnectMcpServer(<AggregatePluginServerName>)
                                                       ← 10 s webview-side debounce
  → patch POSTs /pending-actions/{id}/ack
```

`AggregatePluginServerName` is a Go const in `internal/patchstate/state.go` that
resolves to the string `"mcp-gateway"` — the P4-08 invariant ties the plugin
manifest's name, the `plugin-sync` regen target, and the reconnect-action
`serverName` field to a single source of truth.

The two-stage debounce (500 ms server + 10 s webview) coalesces bursts of
backend mutations into a single user-visible Claude Code reload.
