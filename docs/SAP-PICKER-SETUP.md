# SAP Picker Setup

The SAP Picker (palette: `MCP Gateway: Open SAP Picker (landscape ∪ KeePass)`)
opens a webview that lists SAP systems from your KeePass vault and lets you
register them as VSP + SAP-GUI MCP servers in the gateway with one click.

This document explains every setting the picker uses, why it exists, and
how it is resolved at runtime.

## 30-second TL;DR

If you already have the team-local `team-local.mcp-dashboard` extension
configured (`mcpDashboard.vibingPath`, `mcpDashboard.sapGuiPath`,
`mcpDashboard.uvPath`, `mcpDashboard.orchestratorPath`, `mcpDashboard.keepassDbPath`),
the picker will **auto-fall-back** to those values — you do not need to
re-enter them under the `mcpGateway.*` namespace.

If you do not have team-local installed, fill the five `mcpGateway.*`
settings listed below. Browse buttons in `MCP Gateway: Open Settings`
help with every path field.

## The five settings the picker reads

| Setting | What | Why | Auto-fallback |
|---|---|---|---|
| `mcpGateway.keepassPath` | Full path to your `.kdbx` file | The Python script `sap-credentials.py` opens this vault to list SAP entries | `mcpDashboard.keepassDbPath` |
| `mcpGateway.defaultVspCommand` | Full path to `vsp.exe` (vibing-steampunk) | Spawned as the VSP MCP server when you Apply a row | `mcpDashboard.vibingPath` |
| `mcpGateway.defaultGuiUvProject` | Path to the `sap-gui-control` project directory (not a file) | The directory `uv` enters via `--project` to launch `sap-gui-server` | `mcpDashboard.sapGuiPath` |
| `mcpGateway.uvPath` | Full path to the `uv` binary | Wraps the SAP-GUI server launch | `mcpDashboard.uvPath`, then bare `uv` on PATH |
| `mcpGateway.defaultGuiMode` | `uv` (recommended) or `exec` | `uv` = `uv run --project <project> sap-gui-server` (team-local convention). `exec` = treat `defaultVspCommand` as the single binary for both VSP and GUI | schema default = `uv` |

Two more optional settings:

| Setting | What | Default |
|---|---|---|
| `mcpGateway.sapCredentialsPyPath` | Full path to `sap-credentials.py` | Auto-derived from `mcpDashboard.orchestratorPath`/../scripts/sap-credentials.py, then `~/claude-workspace/claude-team-control/scripts/sap-credentials.py` |
| `mcpGateway.pythonPath` | Python interpreter | `python` on PATH |
| `mcpGateway.sapLandscapePath` | Path to `SAPUILandscape.xml` | `%APPDATA%/SAP/Common/SAPUILandscape.xml` on Windows (SAP Logon default) |

## What happens when you click Apply

For each row you ticked, the picker builds a server-add request:

- **VSP** → `POST /api/v1/servers { command: <defaultVspCommand> }`
- **GUI in `uv` mode** → `POST /api/v1/servers { command: <uvPath>, args: ['run', '--project', <defaultGuiUvProject>, 'sap-gui-server'] }`
- **GUI in `exec` mode** → `POST /api/v1/servers { command: <defaultVspCommand> }` (same binary as VSP)

The daemon then spawns those processes and the gateway tracks them in
the backends tree.

## How values are resolved (priority order)

For each setting the picker checks, in this order:

1. **Per-row override** typed by the operator in the picker's row expand
   panel (`vspCommand` / `guiCommand` / `guiUvProject` columns). Wins
   everything.
2. **`mcpGateway.<key>`** — the canonical setting on this extension.
3. **`mcpDashboard.<legacy>`** — the team-local equivalent (we read it
   for free so operators with the older dashboard don't have to migrate).
4. **Schema default** — `defaultGuiMode` falls back to `uv`; the rest
   fall back to `undefined` and surface a clear "missing config" banner
   so you know what to set.

## Common failure modes

- **"Apply skipped N change(s) because configuration is missing"** —
  one or more of `defaultVspCommand`, `defaultGuiUvProject`, `uvPath`
  is empty AND the corresponding `mcpDashboard.*` fallback is also
  empty. The banner lists exactly which row/component is missing
  which setting, plus the resolved-defaults dump so you can see
  what the picker actually read.

- **"KeePass master password rejected"** — the password you typed
  was rejected by `pykeepass`. Click Refresh in the picker to
  re-enter (you get 3 attempts before the picker bails out). The
  picker auto-evicts the wrong password from VSCode SecretStorage
  so it does not loop.

- **"sap-credentials.py: pykeepass not available"** — Python is
  installed but `pykeepass` is not. Run `pip install pykeepass`
  in the same Python that `mcpGateway.pythonPath` points at, then
  click Refresh.

- **Multiple logins for one SID not visible** — fixed in v1.33.12.
  Each KeePass entry now gets its own row (key = `sid-client-user`)
  so e.g. `Q26-800-naumov` and `Q26-800-naumov1` are two distinct
  rows.

## See also

- `docs/spikes/2026-05-07-sap-picker-and-import-mcp.md` — design spike
  for the picker (hybrid landscape ∩ KeePass model).
- `claude-team-control/scripts/sap-credentials.py` — the Python script
  the picker shells out to (read-only KeePass listing via pykeepass).
- `claude-team-control/vscode-dashboard/src/services/credential-manager.ts`
  — the team-local dashboard's `CredentialManager.listAllEntries` — our
  picker's KP listing path is a verbatim port of that flow.
