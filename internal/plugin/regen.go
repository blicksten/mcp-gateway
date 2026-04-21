// Package plugin regenerates the Claude Code plugin's `.mcp.json` from the
// gateway's currently-registered backends.
//
// Contract (REVIEW-16 L-05): the gateway OWNS the file. Each call overwrites
// it unconditionally with a generated banner; hand-edits are not preserved
// across regens (the prior content is copied once to `.mcp.json.bak` before
// the new content is rename(2)-installed on top).
//
// Concurrency (REVIEW-16 M-02): concurrent callers are serialized via an
// internal mutex so arrival order at the daemon = order of observable
// `.mcp.json` contents. Without this, two concurrent `POST /api/v1/servers`
// requests would race on "which writer wins", producing a tool-list/file
// inconsistency even though each rename(2) is individually atomic.
//
// Placeholders: the generated file uses Claude Code's `${user_config.*}`
// substitution surface. The plugin's `userConfig` schema declares
// `gateway_url` (not sensitive) and `auth_token` (sensitive, OS keychain).
// The MCP client substitutes these at request time; the gateway never sees
// the actual token in this file.
package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"mcp-gateway/internal/models"
)

// GeneratedBannerKey is the banner key inserted at the top of the generated
// `.mcp.json`. The leading `// ` prefix is a convention Claude Code ignores
// (unknown keys are tolerated) but that signals clearly to readers and to
// grep-based tooling that the file is machine-managed.
const GeneratedBannerKey = "// GENERATED"

// GeneratedBannerValue is the banner message.
const GeneratedBannerValue = "mcp-gateway — DO NOT EDIT. Regenerated on every backend mutation."

// DefaultGatewayURLPlaceholder is the Claude-Code substitution token for the
// gateway base URL. Callers that pass an empty `gatewayURL` to Regenerate
// get this placeholder in the output, which lets the plugin's userConfig
// survey drive the actual URL at MCP-client runtime.
const DefaultGatewayURLPlaceholder = "${user_config.gateway_url}"

// AuthTokenPlaceholder is the Claude-Code substitution token for the Bearer
// value. Written verbatim into the headers block of each backend entry.
const AuthTokenPlaceholder = "${user_config.auth_token}"

// MCPJSONFileName is the filename regenerated inside the plugin directory.
const MCPJSONFileName = ".mcp.json"

// MCPJSONBackupFileName is the one-shot backup filename written before an
// overwrite whenever the prior content differs from the new content.
const MCPJSONBackupFileName = ".mcp.json.bak"

// filePerm is the mode applied to the regenerated file. 0600 keeps it to
// the owner; the file is under `~/.claude/plugins/cache/...` on real
// installs but owner-only is still the right posture for an auth-header
// template.
const filePerm os.FileMode = 0o600

// Regenerator rewrites the plugin's `.mcp.json` from the current backend
// set. The zero value is not usable; call NewRegenerator.
type Regenerator struct {
	mu sync.Mutex
}

// NewRegenerator constructs a Regenerator ready for concurrent use.
func NewRegenerator() *Regenerator {
	return &Regenerator{}
}

// Regenerate builds the plugin's `.mcp.json` from servers and writes it
// into pluginDir. Calls are serialized via an internal mutex so
// "arrival order wins" observably (REVIEW-16 M-02).
//
// Behavior:
//
//   - Input is validated: pluginDir must not be empty.
//   - The target JSON is rendered fully in memory and parsed-back as a
//     sanity check before any disk state is touched.
//   - If the existing file matches the new bytes exactly, the call is a
//     no-op (no rename, no backup, no mtime bump) — the idempotent path.
//   - Otherwise, the prior content (if any) is copied to `.mcp.json.bak`
//     and the new content is installed via `CreateTemp` + rename(2), which
//     is atomic on POSIX and "essentially atomic" on Windows (MoveFileEx
//     with MOVEFILE_REPLACE_EXISTING).
//   - Disabled backends (`ServerConfig.Disabled == true`) are excluded.
//   - Entries are sorted alphabetically by name for stable, diff-friendly
//     output.
//
// The gatewayURL parameter is interpolated verbatim into each backend's
// URL. Pass DefaultGatewayURLPlaceholder (or the empty string, which is
// expanded to that placeholder) in production so Claude Code's userConfig
// substitutes the actual URL at runtime. Tests pass a concrete URL.
func (r *Regenerator) Regenerate(pluginDir string, servers map[string]*models.ServerConfig, gatewayURL string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if pluginDir == "" {
		return errors.New("plugin directory is empty")
	}
	if gatewayURL == "" {
		gatewayURL = DefaultGatewayURLPlaceholder
	}

	content, err := buildMCPJSON(servers, gatewayURL)
	if err != nil {
		return fmt.Errorf("build .mcp.json: %w", err)
	}

	// Sanity re-parse before any disk write — guarantees the bytes we
	// are about to install are valid JSON. Unit tests should catch any
	// break long before production, but the cost is microseconds and
	// the failure mode of shipping corrupt JSON to Claude Code is bad
	// UX (plugin loads, every tool 500s).
	var probe map[string]any
	if err := json.Unmarshal(content, &probe); err != nil {
		return fmt.Errorf("generated .mcp.json did not round-trip as JSON: %w", err)
	}

	targetPath := filepath.Join(pluginDir, MCPJSONFileName)
	existing, readErr := os.ReadFile(targetPath)
	switch {
	case readErr == nil:
		if bytes.Equal(existing, content) {
			return nil // idempotent no-op
		}
	case errors.Is(readErr, os.ErrNotExist):
		// First-time write; nothing to back up.
	default:
		return fmt.Errorf("read existing %s: %w", MCPJSONFileName, readErr)
	}

	// Back up prior content if it exists and differs.
	if readErr == nil && len(existing) > 0 {
		backupPath := filepath.Join(pluginDir, MCPJSONBackupFileName)
		if err := os.WriteFile(backupPath, existing, filePerm); err != nil {
			return fmt.Errorf("write backup %s: %w", MCPJSONBackupFileName, err)
		}
	}

	// Atomic install via tmp + rename. CreateTemp in the same directory
	// guarantees rename stays within one filesystem (no cross-device
	// move).
	tmpFile, err := os.CreateTemp(pluginDir, MCPJSONFileName+".tmp.*")
	if err != nil {
		return fmt.Errorf("create tmp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	renamed := false
	defer func() {
		if !renamed {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmpFile.Write(content); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("write tmp file: %w", err)
	}
	// Sync before rename so the new content is persisted to the
	// underlying device even on an unclean shutdown mid-rename.
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("sync tmp file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close tmp file: %w", err)
	}
	// Chmod the tmp file before rename — Windows' rename preserves the
	// source's DACL, so this is the right moment. No-op beyond mode
	// bits on POSIX.
	if err := os.Chmod(tmpPath, filePerm); err != nil {
		return fmt.Errorf("chmod tmp file: %w", err)
	}
	if err := os.Rename(tmpPath, targetPath); err != nil {
		return fmt.Errorf("rename tmp file to %s: %w", MCPJSONFileName, err)
	}
	renamed = true
	return nil
}

// buildMCPJSON renders the plugin's `.mcp.json` body from the given
// backend set. Exposed only through Regenerate and its unit tests —
// package-private is deliberate because the REST layer is expected to
// go through Regenerate (which also owns file I/O + serialization).
func buildMCPJSON(servers map[string]*models.ServerConfig, gatewayURL string) ([]byte, error) {
	names := make([]string, 0, len(servers))
	for name, sc := range servers {
		if sc == nil || sc.Disabled {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)

	// Using map[string]any rather than a struct keeps the JSON shape
	// flexible (we may add fields like transport overrides later without
	// touching callers) and preserves the `// GENERATED` banner key,
	// which Go's struct tags cannot express.
	mcpServers := make(map[string]any, len(names))
	for _, name := range names {
		mcpServers[name] = map[string]any{
			"type": "http",
			"url":  gatewayURL + "/mcp/" + name,
			"headers": map[string]string{
				"Authorization": "Bearer " + AuthTokenPlaceholder,
			},
		}
	}

	doc := map[string]any{
		GeneratedBannerKey: GeneratedBannerValue,
		"mcpServers":       mcpServers,
	}

	buf, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	// Trailing newline for POSIX conventions and git-diff cleanliness.
	buf = append(buf, '\n')
	return buf, nil
}
