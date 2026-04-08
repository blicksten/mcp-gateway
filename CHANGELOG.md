# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
