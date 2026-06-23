package obs

import (
	"log/slog"
	"testing"
)

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug":   slog.LevelDebug,
		"DEBUG":   slog.LevelDebug,
		"info":    slog.LevelInfo,
		"warn":    slog.LevelWarn,
		"warning": slog.LevelWarn,
		"error":   slog.LevelError,
		"err":     slog.LevelError,
		"":        slog.LevelInfo, // default
		"bogus":   slog.LevelInfo, // typo defaults to info, never silent-off
		" info ":  slog.LevelInfo, // trimmed
	}
	for in, want := range cases {
		if got := ParseLevel(in); got != want {
			t.Errorf("ParseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestResolveLevel_Precedence(t *testing.T) {
	// flag wins over env.
	if got := ResolveLevel("debug", "error"); got != slog.LevelDebug {
		t.Errorf("flag should win: got %v", got)
	}
	// empty flag falls back to env.
	if got := ResolveLevel("", "warn"); got != slog.LevelWarn {
		t.Errorf("env fallback: got %v", got)
	}
	// both empty -> info.
	if got := ResolveLevel("", ""); got != slog.LevelInfo {
		t.Errorf("default: got %v", got)
	}
}
