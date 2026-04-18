package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"mcp-gateway/internal/api"
	"mcp-gateway/internal/auth"
	"mcp-gateway/internal/models"
)

// setupAuth enforces startup safety guards (T12A.4), loads or generates
// the Bearer token BEFORE the HTTP server starts accepting requests
// (T12A.5), and emits the Bearer-without-TLS WARN (T12A.4 §L-1).
//
// See ADR-0003 §no-auth-escape-hatch and §token-lifecycle.
func setupAuth(cfg *models.Config, configPath string, noAuth bool, logger *slog.Logger) (api.AuthConfig, error) {
	// --- Guard 1: --no-auth + allow_remote combo ---------------------------
	// Running --no-auth alone is fine (loopback-only developer path).
	// Combined with allow_remote it exposes mutating endpoints to the
	// network. Refuse unless the operator sets the "I understand" env var.
	if noAuth && cfg.Gateway.AllowRemote {
		if os.Getenv(envNoAuthUnderstood) != "1" {
			return api.AuthConfig{}, fmt.Errorf(
				"refusing to start: --no-auth combined with gateway.allow_remote=true "+
					"exposes mutating endpoints to the network; "+
					"set %s=1 to proceed if you understand the risk",
				envNoAuthUnderstood)
		}
		// Operator has acknowledged — emit the three mandated WARN lines.
		// Wording is fixed so tests can grep for it.
		logger.Warn("AUTH DISABLED")
		logger.Warn("network binding is not loopback")
		logger.Warn("anyone on the network can mutate servers and invoke MCP tool calls")
	}

	// --- Guard 2: bearer-required + --no-auth ------------------------------
	// MCP transport policy bearer-required asks the daemon to require
	// Bearer on /mcp and /sse; that cannot hold if auth is disabled.
	if noAuth && cfg.Gateway.AuthMCPTransport == models.AuthMCPTransportBearerRequired {
		return api.AuthConfig{}, fmt.Errorf(
			"refusing to start: gateway.auth_mcp_transport=%q requires Bearer authentication but --no-auth is set",
			cfg.Gateway.AuthMCPTransport)
	}

	// --no-auth path: no token, no middleware, no file.
	if noAuth {
		return api.AuthConfig{Enabled: false, Token: ""}, nil
	}

	// --- Token load/generate -----------------------------------------------
	// Resolve token path: ~/<configDir>/auth.token by default. Operators
	// may override via env var for CI / ephemeral containers.
	tokenPath := auth.DefaultTokenPath(filepath.Dir(configPath))
	envToken := os.Getenv(auth.EnvVarName)

	token, err := auth.LoadOrCreate(tokenPath, envToken)
	if err != nil {
		return api.AuthConfig{}, fmt.Errorf("auth token setup: %w", err)
	}
	if envToken != "" {
		logger.Info("auth token loaded from env var", "var", auth.EnvVarName)
	} else {
		// Log only the path, NEVER the token value.
		logger.Info("auth token ready", "path", tokenPath)
	}

	// --- Guard 3: Bearer-without-TLS WARN (L-1) ----------------------------
	// Auth is enabled but traffic is cleartext on a non-loopback bind.
	// Phase 13.B adds TLS; until then, notify the operator once.
	if cfg.Gateway.AllowRemote {
		logger.Warn("Bearer auth is active but TLS is not configured — token is transmitted in cleartext on public networks (Phase 13 adds TLS support)")
	}

	return api.AuthConfig{Enabled: true, Token: token}, nil
}
