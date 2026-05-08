package auth

import (
	"log/slog"
	"net/http"
	"path/filepath"
)

// EnvVarNameAdmin is the env var that overrides the admin token at
// runtime. Mirrors EnvVarName for the regular Bearer.
//
// Used by ephemeral / CI deployments where writing a file is awkward.
// Production deployments rely on the on-disk admin.token (auto-generated
// at first startup, mode 0600 / Windows DACL).
const EnvVarNameAdmin = "MCP_GATEWAY_ADMIN_TOKEN"

// adminHintMessage is the guidance returned in 401 bodies emitted by
// AdminMiddleware. Distinct from hintMessage so a 401 from the admin
// scope tells the operator to use the admin token shape, not the
// regular one. Kept as a fixed string so admin_test.go can assert
// both fallback names appear (env var + file path).
const adminHintMessage = "add admin Bearer token via MCP_GATEWAY_ADMIN_TOKEN env var or ~/.mcp-gateway/admin.token file"

// AdminMiddleware returns an HTTP middleware that requires an
// `Authorization: Bearer <admin-token>` header matching expected.
// Same wire-shape as Middleware: 401 + WWW-Authenticate: Bearer + JSON
// {error,hint} body. The hint mentions the admin-token env var and
// file path so callers know which token to provide.
//
// The admin scope is EXCLUSIVE: the regular Bearer is NOT a valid
// admin token. AdminMiddleware is mounted on daemon-control endpoints
// (currently /api/v1/shutdown) so external callers that only know the
// regular Bearer (e.g., VSCode 1.119's built-in McpGatewayService —
// which reads ${user_config.auth_token} from the plugin .mcp.json and
// has no path to the admin token) cannot invoke them. This is the
// canonical fix for Bug A's daemon-shutdown cascade.
//
// See ADR-0007 §two-tier-auth, PLAN-mcp-resilience.md Phase MCPR.3.
func AdminMiddleware(expected string, logger *slog.Logger) func(http.Handler) http.Handler {
	return bearerMiddleware("admin", expected, logger, adminHintMessage)
}

// DefaultAdminTokenPath returns the canonical admin-token location
// relative to the config directory (~/.mcp-gateway/admin.token).
// Path-distinct from DefaultTokenPath so the two tokens never share
// a file — admin token leaks via auth.token wouldn't apply.
func DefaultAdminTokenPath(configDir string) string {
	return filepath.Join(configDir, "admin.token")
}
