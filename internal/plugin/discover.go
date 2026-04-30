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

// ClaudePluginCacheGlobSegment is the legacy glob pattern (Claude CLI v1
// layout) appended to `~/.claude/plugins/cache/`. v1 created a single
// directory per installed marketplace version, e.g. `mcp-gateway@mcp-gateway-local`.
//
// Claude CLI v2 changed the layout to a 3-level structure:
//
//	~/.claude/plugins/cache/<source>/<name>/<version>/
//
// e.g. `~/.claude/plugins/cache/mcp-gateway-local/mcp-gateway/1.6.0/`.
//
// Discover() probes both layouts: v2 first, v1 second.
const ClaudePluginCacheGlobSegment = "mcp-gateway@*"

// ClaudePluginCacheV2GlobSegments is the v2 glob pattern. It matches
// `<any-source>/mcp-gateway/<any-version>/` under the cache root.
var ClaudePluginCacheV2GlobSegments = []string{"*", "mcp-gateway", "*"}

// ErrPluginDirNotFound signals that neither `$GATEWAY_PLUGIN_DIR` nor the
// v1/v2 globs under `~/.claude/plugins/cache/` matched an existing
// directory. Callers treat this as non-fatal — regen is skipped, the
// daemon continues to serve.
var ErrPluginDirNotFound = errors.New("plugin directory not found")

// Discover resolves the plugin's on-disk directory, in order:
//
//  1. `$GATEWAY_PLUGIN_DIR` (must exist and be a directory if set)
//  2. Claude CLI v2 layout glob: `~/.claude/plugins/cache/*/mcp-gateway/*`
//     (3-level: source/name/version, lex-max basename wins)
//  3. Claude CLI v1 layout glob: `~/.claude/plugins/cache/mcp-gateway@*`
//     (1-level: name@source, lex-max wins)
//  4. ErrPluginDirNotFound
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
	cacheRoot := filepath.Join(home, ".claude", "plugins", "cache")

	// Probe v2 layout first (newer; primary path on Claude CLI ≥v2).
	v2Pattern := filepath.Join(cacheRoot, ClaudePluginCacheV2GlobSegments[0],
		ClaudePluginCacheV2GlobSegments[1], ClaudePluginCacheV2GlobSegments[2])
	if dir, err := globPickLatest(v2Pattern); err != nil {
		return "", err
	} else if dir != "" {
		return dir, nil
	}

	// Fallback: v1 layout (legacy; kept for back-compat).
	v1Pattern := filepath.Join(cacheRoot, ClaudePluginCacheGlobSegment)
	if dir, err := globPickLatest(v1Pattern); err != nil {
		return "", err
	} else if dir != "" {
		return dir, nil
	}

	return "", ErrPluginDirNotFound
}

// globPickLatest runs filepath.Glob, filters to directories, and returns
// the lex-max basename (deterministic proxy for "most recent version" in
// both v1 `name@source` and v2 numeric-version layouts). Returns
// ("", nil) when the glob matches nothing — caller decides whether that
// is fatal or a fallback trigger.
func globPickLatest(pattern string) (string, error) {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		// filepath.Glob returns ErrBadPattern for a malformed pattern;
		// our patterns are fixed literals so this is a "should never
		// happen" path, but surface it rather than pretending nothing matched.
		return "", fmt.Errorf("glob %q: %w", pattern, err)
	}
	if len(matches) == 0 {
		return "", nil
	}
	dirs := make([]string, 0, len(matches))
	for _, m := range matches {
		info, err := os.Stat(m)
		if err != nil || !info.IsDir() {
			continue
		}
		dirs = append(dirs, m)
	}
	if len(dirs) == 0 {
		return "", nil
	}
	sort.Strings(dirs)
	return dirs[len(dirs)-1], nil
}
