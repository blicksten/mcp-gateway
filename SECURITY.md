# Security Policy

Thank you for taking the time to improve the security of mcp-gateway.

## Supported Versions

The project ships security fixes only for the latest minor release line. Operators on older versions are encouraged to upgrade before reporting issues unless the vulnerability is believed to affect HEAD as well.

| Version    | Supported          |
|------------|--------------------|
| v1.3.x     | ✅ current          |
| v1.2.x     | ✅ security fixes   |
| v1.1.x     | ⚠️ critical only    |
| < v1.1.0   | ❌ end-of-life      |

## Reporting a Vulnerability

If you discover a security vulnerability, **please do not open a public GitHub issue.**

Instead, report it privately via one of:

1. **GitHub Security Advisories** (preferred) — open a private advisory at <https://github.com/anthropics/mcp-gateway/security/advisories/new>. This creates an encrypted, maintainer-only channel and lets us coordinate a fix, a CVE, and a coordinated release.
2. **Email** — send details to the maintainer address in `CODEOWNERS` with subject `[mcp-gateway security]`.

Please include:

- A description of the vulnerability and the conditions required to trigger it.
- Reproduction steps (minimal code, config, or request sequence).
- The git commit SHA or release tag you tested against.
- Your preferred disclosure timeline and whether you want to be credited.
- Any draft patch you are willing to share.

## Disclosure Timeline

We aim for the following, measured from the first acknowledged report:

| Step                                   | Target      |
|----------------------------------------|-------------|
| Initial acknowledgement                | 72 hours    |
| Severity + scope confirmation          | 7 days      |
| Fix available in a private branch      | 14 days     |
| Patched release + public advisory      | 30 days     |

Critical remote-code-execution class issues may be shipped faster; minor low-impact issues may slip the 30-day target if they require multi-platform coordination. We will keep you updated as we work and will credit the reporter in the release notes unless anonymity is requested.

## Security Boundaries

The following are **in scope** for coordinated disclosure:

- Authentication or authorization bypass on `/api/v1/*`, `/mcp*`, `/sse*`, `/logs`.
- Credential leakage (Bearer token, KeePass-imported secrets, child-process env) via logs, error messages, API responses, or SecretStorage index.
- Remote code execution via any accepted input: REST payload, MCP protocol message, config file, env file, KeePass import.
- Sandbox escape from a child MCP server process to the daemon.
- TLS configuration bugs that weaken cleartext protection on non-loopback binds.
- Windows DACL / POSIX permission regressions on the token file.

Out of scope (report via a regular GitHub issue if interesting):

- Denial-of-service from malformed requests that merely return 5xx — we already bound body size, header size, and concurrency.
- Issues that require the attacker to already hold the Bearer token, a config file write, or local shell as the daemon user.
- Missing cosmetic security headers on the REST API when bound to loopback.

## See Also

- [ADR-0003 Bearer Token Authentication](docs/ADR-0003-bearer-token-auth.md) — policy matrix, token lifecycle, Windows DACL rationale.
- [ROADMAP](docs/ROADMAP.md) — phase history, shipped security hardening.
