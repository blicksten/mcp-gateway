package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mcp-gateway/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// absCmd returns an absolute path suitable for tests (cross-platform).
func absCmd(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs("mock-server")
	require.NoError(t, err)
	return p
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o640))
}

func TestLoad_StdioServer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	writeJSON(t, path, map[string]any{
		"servers": map[string]any{
			"orchestrator": map[string]any{
				"command": absCmd(t),
				"args":    []string{"run", "orchestrator"},
			},
		},
	})

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, absCmd(t), cfg.Servers["orchestrator"].Command)
	assert.Equal(t, []string{"run", "orchestrator"}, cfg.Servers["orchestrator"].Args)
	assert.Equal(t, "stdio", cfg.Servers["orchestrator"].TransportType())
}

func TestLoad_HTTPServer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	writeJSON(t, path, map[string]any{
		"servers": map[string]any{
			"context7": map[string]any{
				"url": "http://localhost:3000/mcp",
			},
		},
	})

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, "http", cfg.Servers["context7"].TransportType())
}

func TestLoad_SSEServer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	writeJSON(t, path, map[string]any{
		"servers": map[string]any{
			"sap-gui": map[string]any{
				"url":      "http://localhost:8091/sse",
				"rest_url": "http://localhost:8091",
			},
		},
	})

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, "sse", cfg.Servers["sap-gui"].TransportType())
	assert.Equal(t, "http://localhost:8091", cfg.Servers["sap-gui"].RestURL)
}

func TestLoad_Defaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	writeJSON(t, path, map[string]any{})

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, 8765, cfg.Gateway.HTTPPort)
	assert.Equal(t, []string{"http"}, cfg.Gateway.Transports)
	assert.Equal(t, models.Duration(30*time.Second), cfg.Gateway.PingInterval)
	assert.NotNil(t, cfg.Servers)
}

func TestLoad_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(path, []byte("{bad json"), 0o640))

	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse config")
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := Load("/nonexistent/config.json")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read config")
}

func TestLoad_InvalidConfig_NoTransport(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	writeJSON(t, path, map[string]any{
		"servers": map[string]any{
			"broken": map[string]any{},
		},
	})

	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must have command, url, or rest_url")
}

func TestLoad_InvalidConfig_BothCommandAndURL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	writeJSON(t, path, map[string]any{
		"servers": map[string]any{
			"broken": map[string]any{
				"command": absCmd(t),
				"url":     "http://localhost:3000",
			},
		},
	})

	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot have both command and url")
}

func TestLoad_InvalidConfig_SeparatorInName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	writeJSON(t, path, map[string]any{
		"servers": map[string]any{
			"bad__name": map[string]any{
				"command": absCmd(t),
			},
		},
	})

	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not contain")
}

func TestLoadWithLocal_Merge(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "config.json")
	localPath := filepath.Join(dir, "config.local.json")

	writeJSON(t, mainPath, map[string]any{
		"gateway": map[string]any{
			"http_port": 8765,
		},
		"servers": map[string]any{
			"ctx7": map[string]any{
				"url": "http://localhost:3000/mcp",
			},
			"orch": map[string]any{
				"command": absCmd(t),
				"args":    []string{"run", "orch"},
			},
		},
	})

	// Local overrides port and replaces orch server entirely.
	writeJSON(t, localPath, map[string]any{
		"gateway": map[string]any{
			"http_port": 9999,
		},
		"servers": map[string]any{
			"orch": map[string]any{
				"command": absCmd(t),
				"args":    []string{"-m", "orch"},
				"cwd":     "/opt/orch",
			},
		},
	})

	cfg, err := LoadWithLocal(mainPath, localPath)
	require.NoError(t, err)

	// Gateway field-by-field: port overridden.
	assert.Equal(t, 9999, cfg.Gateway.HTTPPort)

	// ctx7 unchanged (not in local).
	assert.Equal(t, "http://localhost:3000/mcp", cfg.Servers["ctx7"].URL)

	// orch replaced entirely by local.
	assert.Equal(t, absCmd(t), cfg.Servers["orch"].Command)
	assert.Equal(t, []string{"-m", "orch"}, cfg.Servers["orch"].Args)
	assert.Equal(t, "/opt/orch", cfg.Servers["orch"].Cwd)
}

func TestLoadWithLocal_MissingLocal(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "config.json")

	writeJSON(t, mainPath, map[string]any{
		"servers": map[string]any{
			"ctx7": map[string]any{
				"url": "http://localhost:3000/mcp",
			},
		},
	})

	// No local file — should succeed with main config only.
	cfg, err := LoadWithLocal(mainPath, filepath.Join(dir, "config.local.json"))
	require.NoError(t, err)
	assert.Len(t, cfg.Servers, 1)
}

func TestSaveAndLoad_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	expose := true
	original := &models.Config{
		Gateway: models.GatewaySettings{
			HTTPPort:     8765,
			Transports:   []string{"http", "stdio"},
			PingInterval: models.Duration(15 * time.Second),
			ToolFilter: &models.ToolFilter{
				Mode:           "allowlist",
				IncludeServers: []string{"orch", "ctx7"},
				ToolBudget:     50,
			},
		},
		Servers: map[string]*models.ServerConfig{
			"orch": {
				Command:     absCmd(t),
				Args:        []string{"run", "orch"},
				ExposeTools: &expose,
			},
			"ctx7": {
				URL: "http://localhost:3000/mcp",
			},
		},
	}

	require.NoError(t, Save(path, original))

	loaded, err := Load(path)
	require.NoError(t, err)

	// Compare key fields (loaded has defaults applied).
	assert.Equal(t, original.Gateway.HTTPPort, loaded.Gateway.HTTPPort)
	assert.Equal(t, original.Gateway.Transports, loaded.Gateway.Transports)
	assert.Equal(t, original.Gateway.PingInterval, loaded.Gateway.PingInterval)
	assert.Equal(t, original.Gateway.ToolFilter.Mode, loaded.Gateway.ToolFilter.Mode)
	assert.Equal(t, original.Gateway.ToolFilter.IncludeServers, loaded.Gateway.ToolFilter.IncludeServers)
	assert.Equal(t, original.Gateway.ToolFilter.ToolBudget, loaded.Gateway.ToolFilter.ToolBudget)
	assert.Equal(t, absCmd(t), loaded.Servers["orch"].Command)
	assert.Equal(t, "http://localhost:3000/mcp", loaded.Servers["ctx7"].URL)
	assert.True(t, loaded.Servers["orch"].ExposeToolsEnabled())
}

func TestCreateDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	require.NoError(t, CreateDefault(path))

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, 8765, cfg.Gateway.HTTPPort)
	assert.Empty(t, cfg.Servers)
}

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	assert.Equal(t, filepath.Join(home, ".mcp-gateway"), expandHome("~/.mcp-gateway"))
	assert.Equal(t, home, expandHome("~"))
	assert.Equal(t, "/absolute/path", expandHome("/absolute/path"))
	assert.Equal(t, "relative/path", expandHome("relative/path"))
	// ~user forms are NOT expanded.
	assert.Equal(t, "~root/etc", expandHome("~root/etc"))
}

func TestLoad_RestOnlyServer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	writeJSON(t, path, map[string]any{
		"servers": map[string]any{
			"jenkins": map[string]any{
				"rest_url":        "http://jenkins.local:8080",
				"health_endpoint": "/api/json",
			},
		},
	})

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, "", cfg.Servers["jenkins"].TransportType())
	assert.Equal(t, "http://jenkins.local:8080", cfg.Servers["jenkins"].RestURL)
}

func TestLoad_ExposeToolsDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	writeJSON(t, path, map[string]any{
		"servers": map[string]any{
			"orch": map[string]any{
				"command": absCmd(t),
			},
			"hidden": map[string]any{
				"command":      absCmd(t),
				"expose_tools": false,
			},
		},
	})

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.True(t, cfg.Servers["orch"].ExposeToolsEnabled())
	assert.False(t, cfg.Servers["hidden"].ExposeToolsEnabled())
}

func TestLoad_ToolFilter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	writeJSON(t, path, map[string]any{
		"gateway": map[string]any{
			"tool_filter": map[string]any{
				"mode":              "allowlist",
				"include_servers":   []string{"orch", "ctx7"},
				"exclude_servers":   []string{"sap-gui"},
				"tool_budget":       50,
				"per_server_budget": 15,
			},
		},
	})

	cfg, err := Load(path)
	require.NoError(t, err)
	require.NotNil(t, cfg.Gateway.ToolFilter)
	assert.Equal(t, "allowlist", cfg.Gateway.ToolFilter.Mode)
	assert.Equal(t, []string{"orch", "ctx7"}, cfg.Gateway.ToolFilter.IncludeServers)
	assert.Equal(t, []string{"sap-gui"}, cfg.Gateway.ToolFilter.ExcludeServers)
	assert.Equal(t, 50, cfg.Gateway.ToolFilter.ToolBudget)
	assert.Equal(t, 15, cfg.Gateway.ToolFilter.PerServerBudget)
}

func TestLoad_PingInterval(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	writeJSON(t, path, map[string]any{
		"gateway": map[string]any{
			"ping_interval": "10s",
		},
	})

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, models.Duration(10*time.Second), cfg.Gateway.PingInterval)
}

func TestLoadExpanded_VarsResolvedBeforeValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	writeJSON(t, path, map[string]any{
		"servers": map[string]any{
			"test": map[string]any{
				"command": "${CMD}",
			},
		},
	})

	envMap := map[string]string{"CMD": absCmd(t)}
	cfg, err := LoadExpanded(path, envMap)
	require.NoError(t, err)
	assert.Equal(t, absCmd(t), cfg.Servers["test"].Command)
}

func TestLoadExpanded_NilEnvMap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	writeJSON(t, path, map[string]any{
		"servers": map[string]any{
			"test": map[string]any{
				"command": absCmd(t),
			},
		},
	})

	// nil envMap should work (no expansion, values must already be valid).
	cfg, err := LoadExpanded(path, nil)
	require.NoError(t, err)
	assert.Equal(t, absCmd(t), cfg.Servers["test"].Command)
}

func TestLoadWithLocalExpanded_Merge(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "config.json")
	localPath := filepath.Join(dir, "config.local.json")

	writeJSON(t, mainPath, map[string]any{
		"servers": map[string]any{
			"ctx7": map[string]any{
				"url": "http://localhost:3000/mcp",
			},
		},
	})

	writeJSON(t, localPath, map[string]any{
		"servers": map[string]any{
			"orch": map[string]any{
				"command": "${CMD}",
				"env":     []string{"KEY=${SECRET}"},
			},
		},
	})

	envMap := map[string]string{"CMD": absCmd(t), "SECRET": "s3cret"}
	cfg, err := LoadWithLocalExpanded(mainPath, localPath, envMap)
	require.NoError(t, err)

	// ctx7 from main — no expansion needed.
	assert.Equal(t, "http://localhost:3000/mcp", cfg.Servers["ctx7"].URL)

	// orch from local — expanded.
	assert.Equal(t, absCmd(t), cfg.Servers["orch"].Command)
	assert.Equal(t, []string{"KEY=s3cret"}, cfg.Servers["orch"].Env)
}

func TestLoadWithLocalExpanded_MissingLocal(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "config.json")

	writeJSON(t, mainPath, map[string]any{
		"servers": map[string]any{
			"test": map[string]any{
				"url": "${URL}",
			},
		},
	})

	envMap := map[string]string{"URL": "http://localhost:3000/mcp"}
	cfg, err := LoadWithLocalExpanded(mainPath, filepath.Join(dir, "config.local.json"), envMap)
	require.NoError(t, err)
	assert.Equal(t, "http://localhost:3000/mcp", cfg.Servers["test"].URL)
}

func TestLoadExpanded_ValidationFailsOnMissingVar(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	writeJSON(t, path, map[string]any{
		"servers": map[string]any{
			"test": map[string]any{
				"command": "${MISSING_CMD}",
			},
		},
	})

	// ${MISSING_CMD} resolves to "" which fails validation (not absolute path).
	_, err := LoadExpanded(path, map[string]string{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "validate config")
}

func TestLoadExpanded_ExpandConfigError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	writeJSON(t, path, map[string]any{
		"servers": map[string]any{
			"test": map[string]any{
				"command": absCmd(t),
				"env":     []string{"KEY=${BAD}"},
			},
		},
	})

	// Newline in expanded value triggers ExpandConfig error (before validation).
	envMap := map[string]string{"BAD": "line1\nline2"}
	_, err := LoadExpanded(path, envMap)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expand config")
	assert.Contains(t, err.Error(), "newline")
}

func TestLoadWithLocal_CorruptLocalJSON(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "config.json")
	localPath := filepath.Join(dir, "config.local.json")

	writeJSON(t, mainPath, map[string]any{
		"servers": map[string]any{
			"ctx7": map[string]any{
				"url": "http://localhost:3000/mcp",
			},
		},
	})
	require.NoError(t, os.WriteFile(localPath, []byte("{bad json"), 0o640))

	_, err := LoadWithLocal(mainPath, localPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse local config")
}

func TestLoadWithLocalExpanded_CorruptLocalJSON(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "config.json")
	localPath := filepath.Join(dir, "config.local.json")

	writeJSON(t, mainPath, map[string]any{
		"servers": map[string]any{
			"ctx7": map[string]any{
				"url": "http://localhost:3000/mcp",
			},
		},
	})
	require.NoError(t, os.WriteFile(localPath, []byte("{bad json"), 0o640))

	_, err := LoadWithLocalExpanded(mainPath, localPath, map[string]string{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse local config")
}
