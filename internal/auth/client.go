package auth

import (
	"errors"
	"fmt"
	"os"
)

// EnvVarName is the name of the environment variable that overrides the
// token file for all consumers (daemon self-test, mcp-ctl, extension).
const EnvVarName = "MCP_GATEWAY_AUTH_TOKEN"

// ErrNoToken is returned when neither the env var nor the token file
// provides a usable Bearer token. Consumers surface this with a
// human-readable message naming both fallbacks.
var ErrNoToken = errors.New("no auth token found: set " + EnvVarName + " or create the token file")

// BuildHeader returns the value of the `Authorization` HTTP header —
// "Bearer <token>" — discovered via the canonical ladder:
//
//   1. MCP_GATEWAY_AUTH_TOKEN env var (if non-empty).
//   2. File at tokenPath (if exists and content is a well-formed token).
//   3. Otherwise return ErrNoToken (wrapped with a clear message).
//
// This helper is the single source of truth shared between the daemon's
// self-test path and mcp-ctl. The extension ships an equivalent helper
// in TypeScript (src/auth-header.ts).
//
// See ADR-0003 §auth-header-fallback.
func BuildHeader(tokenPath string) (string, error) {
	tok, err := ResolveToken(tokenPath)
	if err != nil {
		return "", err
	}
	return "Bearer " + tok, nil
}

// ResolveToken returns the raw token value (without the "Bearer " prefix)
// using the same discovery ladder as BuildHeader. Exposed for callers
// that need the bare token (e.g. for logging-redaction tests, CLI health
// commands that print a truncated token hint).
func ResolveToken(tokenPath string) (string, error) {
	if env := os.Getenv(EnvVarName); env != "" {
		if !looksLikeToken(env) {
			return "", fmt.Errorf("%s env var is set but malformed (expected >=%d base64url chars)", EnvVarName, MinTokenLen)
		}
		return env, nil
	}
	if tok, ok := tryReadToken(tokenPath); ok {
		return tok, nil
	}
	return "", fmt.Errorf("%w: checked %s", ErrNoToken, tokenPath)
}
