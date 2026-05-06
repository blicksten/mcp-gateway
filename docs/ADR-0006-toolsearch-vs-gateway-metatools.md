# ADR-0006: Gateway meta-tools coexist with Claude Code's native ToolSearch

**Status:** Accepted
**Date:** 2026-05-06
**Deciders:** Porfiry [Opus 4.7] + user (architectural review)
**Version:** v1.6.0+ (no code change — decision-record only)

## Context

In Claude Code 2.x the host harness exposes a built-in tool called
`ToolSearch`. It is a client-side context optimisation: every MCP tool a
session has access to is listed by name in a `system-reminder` block, but
its JSON Schema is not loaded into the prompt. When the model wants to
call a tool, it asks `ToolSearch` to fetch the schema for that specific
name (`select:<tool_name>` or keyword search). Once the schema is
returned the tool becomes invokable like any tool defined at the top of
the prompt.

Phase 16.6 of mcp-gateway (commit landed 2026-04-22, see
`docs/ROADMAP.md` v1.6.0 entry) added three aggregate-only meta-tools to
the `/mcp` surface for the same purpose:

- `gateway.list_servers` — runtime topology `[{name, status, transport,
  tool_count, health, uptime_seconds}]`.
- `gateway.list_tools` — map keyed by backend with `[{name, namespaced,
  description, inputSchema}]`, optional `server` filter.
- `gateway.invoke` — universal fallback invoker requiring `{backend,
  tool, args}`; bypasses the client's stale `tools/list` cache (Issue
  #13646).

These were introduced before `ToolSearch` was generally available in
Claude Code. The user surfaced the question:

> "ToolSearch seems to do the same thing — should we throw away the
> meta-tools?"

This ADR records the conclusion of that review and the operating rule
going forward.

## Decision

**Keep the gateway meta-tools. They coexist with `ToolSearch`; neither
replaces the other.**

The two mechanisms operate at different layers and serve different
client populations. The narrow zone of overlap (lazy schema discovery
inside Claude Code) is acceptable redundancy because mcp-gateway is
explicitly cross-client.

### Layer separation

| Concern | `ToolSearch` (Claude Code harness) | Gateway meta-tools | Gateway core |
|---------|-----------------------------------|--------------------|--------------|
| Lazy load tool schemas | yes — by name or keyword query | yes — `gateway.list_tools` then `gateway.invoke` | n/a |
| Manage MCP server subprocess lifecycle | no | no | yes |
| Health monitor + auto-restart | no | no | yes |
| Circuit breaker for flapping backends | no | no | yes |
| REST API for hot add/remove | no | no | yes |
| Cross-tab subprocess multiplexing (16 → 1) | no | no | yes |
| Works for clients other than Claude Code | no | yes (Cursor, Continue.dev, Cline, custom Anthropic SDK apps) | yes |

`ToolSearch` is a Claude Code-only context optimisation. It cannot start
or supervise a backend; it only fetches a schema that some other layer
has already exposed. Gateway core (process lifecycle + health + REST +
circuit breaker + multiplexing) is unrelated to `ToolSearch` and
unaffected by this ADR.

### Coexistence inside Claude Code

The two mechanisms compose cleanly:

```
Claude Code                  Gateway                    Backends
┌──────────────────┐         ┌─────────────────┐        ┌─────────────┐
│ ToolSearch       │         │ Aggregate /mcp  │        │ context7    │
│ (lazy schemas)   │ ──────▶ │  + meta-tools   │ ─────▶ │ orchestrator│
│                  │         │  + namespaced   │        │ pal-mcp     │
└──────────────────┘         │    tools        │        │ playwright  │
                             └─────────────────┘        └─────────────┘
        ▲                            ▲
        │                            │
   loads schemas            exposes both surfaces:
   on demand                (a) namespaced tools (default path)
                            (b) gateway.invoke + gateway.list_tools
                                (fallback when client lacks lazy load)
```

When the client is Claude Code, the namespaced tools surface
(`mcp__mcp-gateway__<backend>__<tool>`) is the primary path: each
namespaced tool name appears in the deferred-tool list, and Claude Code
loads its schema via `ToolSearch` on demand. The meta-tools are
available but rarely needed in this configuration.

When the client is Cursor / Continue.dev / Cline / a custom Anthropic
SDK app, those clients emit every tool schema into the prompt at session
start and have no `ToolSearch`-equivalent lazy loader. There the
meta-tools are the only way to keep the prompt small while still being
able to reach every backend tool — `gateway.list_tools` (one schema in
prompt) discovers everything, and `gateway.invoke` (one more schema)
calls anything.

### Why we don't deprecate the meta-tools for Claude Code-only deployments

We considered a "Claude Code only" build that strips the meta-tools.
Rejected because:

1. **A single gateway daemon serves multiple clients simultaneously.**
   The same `/mcp` endpoint is reached by Claude Code, Cursor, custom
   SDK scripts, and dashboard tooling at the same time. We cannot pick
   one client population and disable a feature globally.
2. **`gateway.invoke` is also a defence against `tools/list` cache
   bugs** in any MCP client (Issue #13646 was the original motivator).
   Even Claude Code with `ToolSearch` benefits when a backend's tool
   list changes mid-session and the client cache is stale.
3. **Three tools have negligible context cost.** `gateway.list_servers
   / list_tools / invoke` add ~1–2 KB of schema. Removing them does
   not meaningfully shrink the prompt.

## Consequences

### Positive

- mcp-gateway remains a single binary that works identically for every
  MCP-speaking client, regardless of whether the client has its own
  lazy-schema mechanism.
- Inside Claude Code, the two mechanisms cooperate without operator
  configuration: the namespaced surface flows through `ToolSearch`
  natively; the meta-tools sit dormant unless explicitly invoked.
- Phase 16.6's stale-cache regression test (`gateway_invoke_test.go ::
  StaleToolsCache_Fallback`) keeps `gateway.invoke` honest as a
  cache-bypass primitive.

### Negative / accepted

- ~1–2 KB of redundant schema in Claude Code prompts. Negligible.
- Two paths to the same call site (namespaced tool vs `gateway.invoke`)
  means the model could pick either. Practice shows it picks the
  namespaced form unless the namespaced tool is missing from the
  visible list (the design intent).

### Monitoring trigger — when to revisit this ADR

Reopen this decision if any of the following occur:

1. **Anthropic ships a Claude Code feature that supervises MCP server
   subprocesses, runs health checks, or exposes a REST control plane**
   — then the gateway core's job, not just the meta-tools, starts
   overlapping with the harness. Today (Claude Code 2.1.x) the harness
   is a static `tools/list` cache (Issue #13646) with `ToolSearch` for
   schema laziness; nothing more.
2. **`ToolSearch` semantics change** in a way that breaks the
   namespaced tool flow (e.g. caching schemas across topology changes).
   The cache-busting `serverInfo.version` Phase 16.6 introduced is the
   defence; if `ToolSearch` ignores it, the meta-tools become the only
   reliable path even inside Claude Code.
3. **Cursor / Continue.dev / Cline ship a `ToolSearch` equivalent.**
   Then the case for meta-tools weakens further; we still keep them as
   a universal fallback, but their day-to-day use drops to ~zero.

## Alternatives considered

- **Strip the meta-tools.** Rejected — would break non-Claude-Code
  clients and remove the cache-bypass primitive (see "Why we don't
  deprecate" above).
- **Split the gateway into a "Claude Code build" and a "generic build"
  with different tool surfaces.** Rejected — operationally we serve
  multiple clients from one daemon at the same time; build splits do
  not match deployment reality.
- **Rename the meta-tools to discourage their use inside Claude Code.**
  Rejected — names are part of the FROZEN v1.6.0 contract; renaming
  breaks every existing integration for cosmetic value.
- **Document the meta-tools as deprecated, retain them indefinitely.**
  Rejected — they are not deprecated; they are the canonical surface
  for non-Claude-Code clients. Calling them deprecated would mislead
  operators using Cursor or SDK code.

## Cross-references

- `README.md` §"Gateway meta-tools vs Claude Code ToolSearch" — operator-facing summary of this decision.
- `docs/ADR-0005-claude-code-integration.md` — Phase 16 hybrid architecture this ADR builds on.
- `docs/ROADMAP.md` v1.6.0 entry — Phase 16.6 implementation notes (`gateway.invoke` + meta-tools + cache-busting `serverInfo.version`).
- `internal/proxy/gateway.go` — `registerGatewayBuiltins()` wires the three meta-tools into the aggregate `/mcp` surface only.
- `internal/proxy/gateway_invoke_test.go` — `StaleToolsCache_Fallback` and `GatewayBuiltins_NotOnPerBackend` invariants.
- Anthropic's `ToolSearch` is part of the Claude Code harness; not a public Anthropic SDK feature.

## Open questions

None blocking. Two follow-ups worth tracking but not scoped to this ADR:

- Should the dashboard's "Claude Code Integration" panel show whether
  `ToolSearch` is observed in the running Claude Code build, so
  operators can see which path their tool calls are taking? (Diagnostic
  nicety, not a correctness issue.)
- Should `gateway.invoke` carry a `schema_version` parameter so a future
  contract migration can run side-by-side with v1.6.0 callers? (Future
  Phase, only if we ever break the contract.)
