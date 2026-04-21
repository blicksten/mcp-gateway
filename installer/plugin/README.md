# mcp-gateway — Claude Code plugin

This directory is packaged as a Claude Code plugin. Installing it lets Claude Code discover every backend MCP server managed by the gateway daemon as a separate namespaced entry, routed through the gateway's Bearer-authed `/mcp/{backend}` endpoint.

## Install

```bash
claude plugin marketplace add <repo-path>/installer/marketplace.json
claude plugin install mcp-gateway@mcp-gateway-local
```

## Configure

After install, Claude Code prompts for two `userConfig` values:

| Field | Source | Sensitive |
|-------|--------|-----------|
| `gateway_url` | Gateway base URL (e.g. `http://127.0.0.1:8765`) | no |
| `auth_token` | Contents of `~/.mcp-gateway/auth.token` | yes (OS keychain) |

Both are substituted at MCP runtime into `.mcp.json` via `${user_config.*}`.

## Layout

```
installer/plugin/
├── .claude-plugin/
│   └── plugin.json   # plugin metadata + userConfig schema
├── .mcp.json         # GENERATED — one entry per non-disabled backend
└── README.md         # this file
```

`.mcp.json` is rewritten by the gateway daemon on every `POST`/`DELETE`/`PATCH (disabled)` of `/api/v1/servers`. The gateway OWNS this file; hand-edits are discarded on the next regen (a one-shot `.mcp.json.bak` is created before overwrite).

## Discovery

The gateway locates this plugin directory in the following order:

1. `$GATEWAY_PLUGIN_DIR` env var — used for dev/test. Points to this repo's `installer/plugin/`.
2. `~/.claude/plugins/cache/mcp-gateway@*/` — post-install location managed by Claude Code.
3. Nothing found → regen is skipped (not an error).

## Rotating the gateway auth token

If the gateway regenerates `auth.token` (e.g. via `mcp-ctl auth rotate`), the plugin's keychain-stored `auth_token` becomes stale. Re-run:

```bash
mcp-ctl install-claude-code --refresh-token
```

to push the new token into the plugin's userConfig without re-installing.
