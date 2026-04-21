package plugin

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// PluginDirEnvVar is the override env var for the plugin directory. When
// set to a non-empty path, Discover treats it as canonical and does not
// consult the glob fallback. Intended primarily for development against
// an uninstalled checkout of the repo.
const PluginDirEnvVar = "GATEWAY_PLUGIN_DIR"

// ClaudePluginCacheGlobSegment is the glob pattern appended to
// `~/.claude/plugins/cache/` when searching for the post-install plugin
// directory. Claude Code creates one directory per installed marketplace
// version, e.g. `mcp-gateway@mcp-gateway-local`.
const ClaudePluginCacheGlobSegment = "mcp-gateway@*"

// ErrPluginDirNotFound signals that neither `$GATEWAY_PLUGIN_DIR` nor the
// `~/.claude/plugins/cache/mcp-gateway@*/` glob matched an existing
// directory. Callers treat this as non-fatal — regen is skipped, the
// daemon continues to serve.
var ErrPluginDirNotFound = errors.New("plugin directory not found")

// Discover resolves the plugin's on-disk directory, in order:
//
//  1. `$GATEWAY_PLUGIN_DIR` (must exist and be a directory if set)
//  2. `~/.claude/plugins/cache/mcp-gateway@*` (glob match, lex-max wins
//     so the most recent marketplace version is preferred)
//  3. ErrPluginDirNotFound
//
// Returns the absolute path to the plugin root (the directory that
// contains `.claude-plugin/plugin.json` and `.mcp.json`).
//
// Paths are built with filepath.Join for cross-platform correctness —
// Claude Code on Windows resolves `~/.claude/...` to
// `%USERPROFILE%\.claude\...` via the same os.UserHomeDir result.
func Discover() (string, error) {
	if dir := os.Getenv(PluginDirEnvVar); dir != "" {
		info, err := os.Stat(dir)
		if err != nil {
			return "", fmt.Errorf("%s=%q: %w", PluginDirEnvVar, dir, err)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("%s=%q: not a directory", PluginDirEnvVar, dir)
		}
		return dir, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home dir: %w", err)
	}

	pattern := filepath.Join(home, ".claude", "plugins", "cache", ClaudePluginCacheGlobSegment)
	matches, err := filepath.Glob(pattern)
	if err != nil {
		// filepath.Glob returns ErrBadPattern for a malformed pattern; our
		// pattern is a fixed literal so this is a "should never happen"
		// path, but surface it rather than pretending nothing matched.
		return "", fmt.Errorf("glob %q: %w", pattern, err)
	}
	if len(matches) == 0 {
		return "", ErrPluginDirNotFound
	}

	// Prefer the directory whose basename sorts last — this is a
	// deterministic proxy for "most recently installed version" given
	// Claude Code's `name@marketplace` suffix scheme. If Claude Code ever
	// adopts a non-lex-sortable versioning scheme we'll revisit.
	//
	// Filter out non-directories defensively: a stray file matching the
	// pattern would otherwise be returned.
	dirs := make([]string, 0, len(matches))
	for _, m := range matches {
		info, err := os.Stat(m)
		if err != nil || !info.IsDir() {
			continue
		}
		dirs = append(dirs, m)
	}
	if len(dirs) == 0 {
		return "", ErrPluginDirNotFound
	}
	sort.Strings(dirs)
	return dirs[len(dirs)-1], nil
}
