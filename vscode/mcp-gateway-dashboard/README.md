# MCP Gateway Dashboard

VS Code extension for managing MCP backend servers through MCP Gateway.

## Features

- **Backend Tree View** — sidebar panel showing all MCP backends with live status icons (running, stopped, error, degraded, disabled, starting, restarting) and their exposed tools
- **Status Bar** — aggregate `MCP: N/M` indicator showing running/total server count with color-coded health
- **Daemon Management** — auto-start/stop the `mcp-gateway` daemon from VS Code
- **Log Streaming** — live SSE-based log viewer per backend with auto-reconnect
- **Full CRUD** — add, remove, enable, disable, restart servers and reset circuit breakers via commands and context menus

## Requirements

- MCP Gateway daemon (`mcp-gateway` binary on PATH or configured path)
- VS Code 1.85.0 or later

## Installation

### From VSIX (local)

```bash
cd vscode/mcp-gateway-dashboard
npm install
npm run compile
npx @vscode/vsce package --allow-missing-repository
code --install-extension mcp-gateway-dashboard-0.1.0.vsix
```

### From source (development)

1. Open `vscode/mcp-gateway-dashboard` in VS Code
2. Run `npm install`
3. Press `F5` to launch Extension Development Host

## Settings

| Setting | Default | Description |
|---------|---------|-------------|
| `mcpGateway.apiUrl` | `http://localhost:8765` | Gateway REST API URL |
| `mcpGateway.autoStart` | `true` | Auto-start daemon when VS Code opens |
| `mcpGateway.daemonPath` | (empty = PATH) | Path to `mcp-gateway` executable |
| `mcpGateway.pollInterval` | `5000` | Status polling interval (ms, minimum 1000) |

## Commands

| Command | Description |
|---------|-------------|
| MCP Gateway: Refresh | Refresh tree view and status bar |
| MCP Gateway: Add Server | Add a new MCP backend server |
| MCP Gateway: Remove Server | Remove a backend (with confirmation) |
| MCP Gateway: Enable Server | Enable a disabled backend |
| MCP Gateway: Disable Server | Disable a running backend |
| MCP Gateway: Restart Server | Restart a backend |
| MCP Gateway: Reset Circuit Breaker | Reset a tripped circuit breaker |
| MCP Gateway: Show Logs | Open live log stream for a backend |
| MCP Gateway: Copy Tool Name | Copy namespaced tool name to clipboard |
| MCP Gateway: Start Daemon | Start the gateway daemon |
| MCP Gateway: Stop Daemon | Stop the gateway daemon |

## Context Menus

Right-click a backend in the tree view for context-sensitive actions. Available actions depend on server status:

- **Running**: Disable, Restart, Show Logs, Remove
- **Degraded**: Disable, Restart, Reset Circuit, Show Logs, Remove
- **Stopped**: Restart, Show Logs, Remove
- **Error**: Restart, Reset Circuit, Show Logs, Remove
- **Disabled**: Enable, Show Logs, Remove
- **Starting/Restarting**: Show Logs

## Architecture

```
VS Code Extension
    ├── GatewayClient      HTTP client for REST API
    ├── BackendTreeProvider Tree view data provider
    ├── McpStatusBar        Status bar indicator
    ├── DaemonManager       Process lifecycle (spawn/kill)
    └── LogViewer           SSE log streaming per backend
```

All communication with the gateway is via its REST API at `localhost:8765`. Zero external dependencies beyond Node.js built-ins and VS Code API.

## License

MIT
