// Package config handles loading, saving, and merging gateway configuration files.
package config

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"mcp-gateway/internal/models"
)

// DefaultConfigDir returns ~/.mcp-gateway.
func DefaultConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, ".mcp-gateway"), nil
}

// DefaultConfigPath returns ~/.mcp-gateway/config.json.
func DefaultConfigPath() (string, error) {
	dir, err := DefaultConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// expandHome replaces a leading ~/ or ~\ with the user's home directory.
// Bare ~ is also expanded. ~user forms are NOT expanded (passed through).
func expandHome(path string) string {
	if path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return home
	}
	if !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, "~\\") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[2:])
}

// expandServerPaths resolves ~ in Cwd fields of all server configs.
func expandServerPaths(cfg *models.Config) {
	for _, sc := range cfg.Servers {
		if sc.Cwd != "" {
			sc.Cwd = expandHome(sc.Cwd)
		}
	}
}

// loadRaw reads a config file, unmarshals JSON, applies defaults and expands
// server paths (~), but does NOT validate. This is the pre-validation step
// shared by Load and LoadExpanded.
func loadRaw(path string) (*models.Config, error) {
	path = expandHome(path)

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	var cfg models.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	cfg.ApplyDefaults()
	expandServerPaths(&cfg)

	return &cfg, nil
}

// Load reads a config file from path, applies defaults, and validates.
func Load(path string) (*models.Config, error) {
	cfg, err := loadRaw(path)
	if err != nil {
		return nil, err
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config %s: %w", path, err)
	}

	return cfg, nil
}

// mergeLocalRaw reads a local overlay file, merges it into cfg, and re-expands
// server paths, but does NOT validate. Returns nil if the local file does not exist.
func mergeLocalRaw(cfg *models.Config, localPath string) error {
	localPath = expandHome(localPath)
	localData, err := os.ReadFile(localPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read local config %s: %w", localPath, err)
	}

	var local models.Config
	if err := json.Unmarshal(localData, &local); err != nil {
		return fmt.Errorf("parse local config %s: %w", localPath, err)
	}

	mergeLocal(cfg, &local)
	expandServerPaths(cfg)
	return nil
}

// LoadWithLocal loads the main config then overlays config.local.json.
// If localPath does not exist, only the main config is used.
func LoadWithLocal(mainPath, localPath string) (*models.Config, error) {
	cfg, err := loadRaw(mainPath)
	if err != nil {
		return nil, err
	}

	if err := mergeLocalRaw(cfg, localPath); err != nil {
		return nil, err
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate merged config: %w", err)
	}

	return cfg, nil
}

// LoadExpanded loads a config file with ${VAR} expansion applied before validation.
//
// Order matters — do not reorder:
// 1. loadRaw: Read + Unmarshal + ApplyDefaults + expandServerPaths (tilde ~)
// 2. ExpandConfig: ${VAR} substitution using envMap + restricted fallback
// 3. cfg.Validate: Validation on final resolved values
func LoadExpanded(path string, envMap map[string]string) (*models.Config, error) {
	cfg, err := loadRaw(path)
	if err != nil {
		return nil, err
	}

	if err := ExpandConfig(cfg, envMap); err != nil {
		return nil, fmt.Errorf("expand config %s: %w", path, err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config %s: %w", path, err)
	}

	return cfg, nil
}

// LoadWithLocalExpanded loads main + local overlay with ${VAR} expansion.
//
// Order matters — do not reorder:
// 1. loadRaw: Read + Unmarshal + ApplyDefaults + expandServerPaths (tilde ~)
// 2. mergeLocalRaw: Merge local overlay + re-expand paths
// 3. ExpandConfig: ${VAR} substitution using envMap + restricted fallback
// 4. cfg.Validate: Validation on final resolved values
func LoadWithLocalExpanded(mainPath, localPath string, envMap map[string]string) (*models.Config, error) {
	cfg, err := loadRaw(mainPath)
	if err != nil {
		return nil, err
	}

	if err := mergeLocalRaw(cfg, localPath); err != nil {
		return nil, err
	}

	if err := ExpandConfig(cfg, envMap); err != nil {
		return nil, fmt.Errorf("expand merged config: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate merged config: %w", err)
	}

	return cfg, nil
}

// mergeLocal applies local overrides onto the base config.
// Gateway settings: field-by-field (local overrides non-zero fields).
// Servers: shallow per server (local's entire ServerConfig replaces base).
func mergeLocal(base, local *models.Config) {
	// Gateway-level: override non-zero fields.
	if local.Gateway.HTTPPort != 0 {
		base.Gateway.HTTPPort = local.Gateway.HTTPPort
	}
	if len(local.Gateway.Transports) > 0 {
		base.Gateway.Transports = local.Gateway.Transports
	}
	if local.Gateway.PingInterval != 0 {
		base.Gateway.PingInterval = local.Gateway.PingInterval
	}
	if local.Gateway.ToolFilter != nil {
		base.Gateway.ToolFilter = local.Gateway.ToolFilter
	}

	// Servers: shallow merge — local server replaces entire base server.
	if base.Servers == nil {
		base.Servers = make(map[string]*models.ServerConfig)
	}
	maps.Copy(base.Servers, local.Servers)
}

// Save writes a config to path as indented JSON.
func Save(path string, cfg *models.Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	data = append(data, '\n')
	return SaveBytes(path, data)
}

// SaveBytes writes pre-serialized config data to path using atomic temp+rename.
func SaveBytes(path string, data []byte) error {
	path = expandHome(path)

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create config dir %s: %w", dir, err)
	}

	// Atomic write: temp file + rename.
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o640); err != nil {
		return fmt.Errorf("write temp config %s: %w", tmpPath, err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		// On Windows, Rename fails if target exists. Remove target first.
		// This is not fully atomic (TOCTOU gap) but acceptable for a
		// single-user localhost daemon. True atomicity requires MoveFileEx.
		if runtime.GOOS == "windows" {
			if rmErr := os.Remove(path); rmErr != nil && !os.IsNotExist(rmErr) {
				_ = os.Remove(tmpPath)
				return fmt.Errorf("remove old config %s: %w", path, rmErr)
			}
			if err := os.Rename(tmpPath, path); err != nil {
				return fmt.Errorf("rename config %s: %w", path, err)
			}
			return nil
		}
		return fmt.Errorf("rename config %s: %w", path, err)
	}

	return nil
}

// CreateDefault creates a config file at path with sensible defaults.
func CreateDefault(path string) error {
	cfg := &models.Config{}
	cfg.ApplyDefaults()
	return Save(path, cfg)
}
