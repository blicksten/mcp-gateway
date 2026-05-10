package claudeconfig

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

// Source identifies one of the three Claude Code config locations.
type Source string

const (
	SourceCCGlobal  Source = "cc_global"  // ~/.claude.json
	SourceCCProject Source = "cc_project" // <workspace>/.mcp.json
	SourceDesktop   Source = "desktop"    // claude_desktop_config.json
)

// AllSources returns all known sources in canonical order. Useful for
// snapshot endpoints that read every available source.
func AllSources() []Source {
	return []Source{SourceCCGlobal, SourceCCProject, SourceDesktop}
}

// ErrUnknownSource is returned when ResolvePath gets a string that does
// not match any known Source constant.
var ErrUnknownSource = errors.New("claudeconfig: unknown source")

// ErrEmptyProjectRoot is returned when SourceCCProject is requested but
// projectRoot is empty. The cc_project source is not meaningful without
// a workspace context.
var ErrEmptyProjectRoot = errors.New("claudeconfig: empty project root")

// ResolvePath returns the absolute filesystem path for src, given a
// projectRoot (used only for SourceCCProject; ignored otherwise).
//
// Path resolution per spike §4.1:
//   - cc_global  → $HOME/.claude.json (POSIX) or %USERPROFILE%\.claude.json (Windows)
//   - cc_project → <projectRoot>/.mcp.json
//   - desktop    → OS-specific config dir for Claude Desktop:
//   - Linux:   $XDG_CONFIG_HOME/Claude/claude_desktop_config.json (or ~/.config/Claude/...)
//   - macOS:   $HOME/Library/Application Support/Claude/claude_desktop_config.json
//   - Windows: %APPDATA%\Claude\claude_desktop_config.json
//
// Empty projectRoot for SourceCCProject is rejected with ErrEmptyProjectRoot.
func ResolvePath(src Source, projectRoot string) (string, error) {
	switch src {
	case SourceCCGlobal:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".claude.json"), nil

	case SourceCCProject:
		if projectRoot == "" {
			return "", ErrEmptyProjectRoot
		}
		return filepath.Join(projectRoot, ".mcp.json"), nil

	case SourceDesktop:
		return desktopConfigPath()
	}
	return "", ErrUnknownSource
}

// desktopConfigPath returns the OS-specific path to Claude Desktop's
// configuration file.
//
// Per spike §4.1; values verified against Claude Desktop's own
// resolution at the time of writing (2026-05).
func desktopConfigPath() (string, error) {
	switch runtime.GOOS {
	case "windows":
		// %APPDATA%\Claude\claude_desktop_config.json
		appData := os.Getenv("APPDATA")
		if appData == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			// %APPDATA% absent (rare): fall back to AppData/Roaming
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(appData, "Claude", "claude_desktop_config.json"), nil

	case "darwin":
		// $HOME/Library/Application Support/Claude/claude_desktop_config.json
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json"), nil

	default:
		// linux / other unix: $XDG_CONFIG_HOME/Claude/claude_desktop_config.json
		// XDG default: $HOME/.config
		xdg := os.Getenv("XDG_CONFIG_HOME")
		if xdg == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			xdg = filepath.Join(home, ".config")
		}
		return filepath.Join(xdg, "Claude", "claude_desktop_config.json"), nil
	}
}
