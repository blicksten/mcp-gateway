package obs

import (
	"log/slog"
	"strings"
)

// ParseLevel maps a level string (debug|info|warn|error, case-insensitive) to
// a slog.Level. Unknown / empty values default to slog.LevelInfo so a typo
// never silently disables logging. Used by main.go to resolve the
// --log-level flag and MCP_GATEWAY_LOG_LEVEL env (PLAN §B.1/§B.2). String
// methods only — no regex.
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error", "err":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// ResolveLevel applies the precedence flag → env → default(info). An empty
// flag falls back to the env var; an empty env falls back to info.
func ResolveLevel(flagVal, envVal string) slog.Level {
	if strings.TrimSpace(flagVal) != "" {
		return ParseLevel(flagVal)
	}
	if strings.TrimSpace(envVal) != "" {
		return ParseLevel(envVal)
	}
	return slog.LevelInfo
}
