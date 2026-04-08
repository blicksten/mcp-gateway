package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"

	"mcp-gateway/internal/lifecycle"
	"mcp-gateway/internal/models"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestListTools_BasicAggregation(t *testing.T) {
	cfg := &models.Config{
		Servers: make(map[string]*models.ServerConfig),
	}
	cfg.ApplyDefaults()

	lm := lifecycle.NewManager(cfg, "test", testLogger())
	gw := New(cfg, lm, "test", testLogger())

	// ListTools with no backends returns empty.
	tools := gw.ListTools()
	assert.Empty(t, tools)
}

func TestServerAllowed_NoFilter(t *testing.T) {
	cfg := &models.Config{}
	cfg.ApplyDefaults()
	lm := lifecycle.NewManager(cfg, "test", testLogger())
	gw := New(cfg, lm, "test", testLogger())

	assert.True(t, gw.serverAllowed("any", nil))
}

func TestServerAllowed_Allowlist(t *testing.T) {
	cfg := &models.Config{}
	cfg.ApplyDefaults()
	lm := lifecycle.NewManager(cfg, "test", testLogger())
	gw := New(cfg, lm, "test", testLogger())

	filter := &models.ToolFilter{
		Mode:           "allowlist",
		IncludeServers: []string{"orch", "ctx7"},
	}

	assert.True(t, gw.serverAllowed("orch", filter))
	assert.True(t, gw.serverAllowed("ctx7", filter))
	assert.False(t, gw.serverAllowed("pal", filter))
	assert.False(t, gw.serverAllowed("sap-gui", filter))
}

func TestServerAllowed_Blocklist(t *testing.T) {
	cfg := &models.Config{}
	cfg.ApplyDefaults()
	lm := lifecycle.NewManager(cfg, "test", testLogger())
	gw := New(cfg, lm, "test", testLogger())

	filter := &models.ToolFilter{
		Mode:           "blocklist",
		ExcludeServers: []string{"sap-gui"},
	}

	assert.True(t, gw.serverAllowed("orch", filter))
	assert.True(t, gw.serverAllowed("ctx7", filter))
	assert.False(t, gw.serverAllowed("sap-gui", filter))
}

func TestServerAllowed_EmptyAllowlist(t *testing.T) {
	cfg := &models.Config{}
	cfg.ApplyDefaults()
	lm := lifecycle.NewManager(cfg, "test", testLogger())
	gw := New(cfg, lm, "test", testLogger())

	filter := &models.ToolFilter{
		Mode:           "allowlist",
		IncludeServers: []string{}, // empty = allow all
	}

	assert.True(t, gw.serverAllowed("anything", filter))
}

func TestServerAllowed_DefaultExclude(t *testing.T) {
	cfg := &models.Config{}
	cfg.ApplyDefaults()
	lm := lifecycle.NewManager(cfg, "test", testLogger())
	gw := New(cfg, lm, "test", testLogger())

	filter := &models.ToolFilter{
		ExcludeServers: []string{"hidden"},
	}

	assert.True(t, gw.serverAllowed("visible", filter))
	assert.False(t, gw.serverAllowed("hidden", filter))
}

func TestBuildMCPServer(t *testing.T) {
	cfg := &models.Config{}
	cfg.ApplyDefaults()
	lm := lifecycle.NewManager(cfg, "test", testLogger())
	gw := New(cfg, lm, "test", testLogger())

	require.NotNil(t, gw.Server())
}

// TestConcurrentUpdateConfigAndListTools exercises cfgMu by running
// UpdateConfig concurrently with ListTools and RebuildTools (T1.6).
func TestConcurrentUpdateConfigAndListTools(t *testing.T) {
	cfg := &models.Config{
		Servers: make(map[string]*models.ServerConfig),
	}
	cfg.ApplyDefaults()

	lm := lifecycle.NewManager(cfg, "test", testLogger())
	gw := New(cfg, lm, "test", testLogger())

	const goroutines = 10
	const iterations = 50

	var wg sync.WaitGroup

	// Goroutines calling UpdateConfig.
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				newCfg := &models.Config{
					Servers: make(map[string]*models.ServerConfig),
				}
				newCfg.ApplyDefaults()
				gw.UpdateConfig(newCfg)
			}
		}()
	}

	// Goroutines calling ListTools.
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_ = gw.ListTools()
			}
		}()
	}

	// Goroutines calling RebuildTools.
	for i := 0; i < goroutines/2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				gw.RebuildTools()
			}
		}()
	}

	wg.Wait()
}

// setupGatewayWithTools creates a Gateway with pre-populated running servers and tools.
func setupGatewayWithTools(t *testing.T) (*Gateway, *lifecycle.Manager) {
	t.Helper()
	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"alpha": {Command: "echo"},
			"beta":  {Command: "echo"},
		},
	}
	cfg.ApplyDefaults()

	lm := lifecycle.NewManager(cfg, "test", testLogger())
	gw := New(cfg, lm, "test", testLogger())

	// Mark servers as running and inject tools (entries created by NewManager).
	lm.SetStatus("alpha", models.StatusRunning, "")
	lm.SetStatus("beta", models.StatusRunning, "")
	lm.SetTools("alpha", []models.ToolInfo{
		{Name: "read", Description: "Read a file"},
		{Name: "write", Description: "Write a file"},
	})
	lm.SetTools("beta", []models.ToolInfo{
		{Name: "search", Description: "Search code"},
	})
	return gw, lm
}

// TestFilteredToolsConsistency verifies that ListTools and RebuildTools use the
// same filtering logic via filteredTools() (T4.4).
func TestFilteredToolsConsistency(t *testing.T) {
	gw, _ := setupGatewayWithTools(t)

	// Get tools via ListTools (REST API path).
	listResult := gw.ListTools()

	// Get tools via filteredTools (same path RebuildTools uses).
	filtered := gw.filteredTools()

	// Both must return the same set of namespaced tool names.
	assert.Equal(t, len(filtered), len(listResult), "filteredTools and ListTools must return the same count")

	filteredNames := make([]string, len(filtered))
	for i, nt := range filtered {
		filteredNames[i] = nt.namespaced
	}
	listNames := make([]string, len(listResult))
	for i, ti := range listResult {
		listNames[i] = ti.Name
	}
	assert.ElementsMatch(t, filteredNames, listNames, "tool names must match between filteredTools and ListTools")

	// Verify expected tools are present.
	assert.Contains(t, listNames, "alpha__read")
	assert.Contains(t, listNames, "alpha__write")
	assert.Contains(t, listNames, "beta__search")
	assert.Len(t, listNames, 3)
}

// TestFilteredToolsWithBudget verifies per-server and global budget enforcement.
func TestFilteredToolsWithBudget(t *testing.T) {
	gw, _ := setupGatewayWithTools(t)

	// Apply per-server budget of 1.
	budgetCfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"alpha": {Command: "echo"},
			"beta":  {Command: "echo"},
		},
		Gateway: models.GatewaySettings{
			ToolFilter: &models.ToolFilter{
				PerServerBudget: 1,
			},
		},
	}
	budgetCfg.ApplyDefaults()
	gw.UpdateConfig(budgetCfg)

	tools := gw.filteredTools()
	// alpha has 2 tools but budget=1, so only 1 from alpha + 1 from beta = 2.
	assert.Len(t, tools, 2)
}

// TestFilteredToolsWithGlobalBudget verifies global ToolBudget truncation.
func TestFilteredToolsWithGlobalBudget(t *testing.T) {
	gw, _ := setupGatewayWithTools(t)

	// Global budget of 2: alpha has 2 tools, beta has 1 = 3 total, truncated to 2.
	budgetCfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"alpha": {Command: "echo"},
			"beta":  {Command: "echo"},
		},
		Gateway: models.GatewaySettings{
			ToolFilter: &models.ToolFilter{
				ToolBudget: 2,
			},
		},
	}
	budgetCfg.ApplyDefaults()
	gw.UpdateConfig(budgetCfg)

	tools := gw.filteredTools()
	assert.Len(t, tools, 2, "global ToolBudget should truncate to 2 tools")
}

// TestFilteredToolsWithBothBudgets verifies per-server + global budget interaction.
func TestFilteredToolsWithBothBudgets(t *testing.T) {
	gw, lm := setupGatewayWithTools(t)

	// Add more tools to beta so per-server budget matters.
	lm.SetTools("beta", []models.ToolInfo{
		{Name: "search", Description: "Search code"},
		{Name: "grep", Description: "Grep files"},
		{Name: "find", Description: "Find files"},
	})

	// Per-server budget=2, global budget=3: alpha contributes 2, beta contributes 2 → 4, truncated to 3.
	budgetCfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"alpha": {Command: "echo"},
			"beta":  {Command: "echo"},
		},
		Gateway: models.GatewaySettings{
			ToolFilter: &models.ToolFilter{
				PerServerBudget: 2,
				ToolBudget:      3,
			},
		},
	}
	budgetCfg.ApplyDefaults()
	gw.UpdateConfig(budgetCfg)

	tools := gw.filteredTools()
	assert.Len(t, tools, 3, "per-server budget=2 yields 4, global budget=3 truncates to 3")
}

// TestConcurrentRebuildAndFilteredTools exercises concurrent RebuildTools +
// ListTools + UpdateConfig to verify no races (T4.2).
func TestConcurrentRebuildAndFilteredTools(t *testing.T) {
	gw, _ := setupGatewayWithTools(t)

	const goroutines = 10
	const iterations = 100

	var wg sync.WaitGroup

	// Concurrent RebuildTools.
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				gw.RebuildTools()
			}
		}()
	}

	// Concurrent ListTools.
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_ = gw.ListTools()
			}
		}()
	}

	// Concurrent UpdateConfig (changes ToolFilter).
	for i := 0; i < goroutines/2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				cfg := &models.Config{
					Servers: map[string]*models.ServerConfig{
						"alpha": {Command: "echo"},
					},
					Gateway: models.GatewaySettings{
						ToolFilter: &models.ToolFilter{
							PerServerBudget: 1,
						},
					},
				}
				cfg.ApplyDefaults()
				gw.UpdateConfig(cfg)
			}
		}()
	}

	wg.Wait()
}

func TestNamespacing(t *testing.T) {
	cfg := &models.Config{}
	cfg.ApplyDefaults()
	lm := lifecycle.NewManager(cfg, "test", testLogger())
	gw := New(cfg, lm, "test", testLogger())

	r := gw.Router()
	assert.Equal(t, "pal__thinkdeep", r.NamespacedTool("pal", "thinkdeep"))

	server, tool, ok := r.SplitToolName("ctx7__resolve-library-id")
	require.True(t, ok)
	assert.Equal(t, "ctx7", server)
	assert.Equal(t, "resolve-library-id", tool)
}

// --- Phase 10.2: CompressSchemas tests ---

func TestCompressDescription(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"normal sentence", "Read a file from disk. Returns the content as text.", "Read a file from disk."},
		{"version number v2.0 mid-sentence", "Supports v2.0 features", "Supports v2.0 features"},
		{"version number with following sentence", "Supports v2.0. New features included.", "Supports v2.0."},
		{"no period", "Read a file from disk", "Read a file from disk"},
		{"very short", "Read", "Read"},
		{"empty", "", ""},
		{"period at end", "Read a file.", "Read a file."},
		{"over 80 chars no period",
			"This is a very long tool description that has no sentence boundary and exceeds eighty characters in total length",
			"This is a very long tool description that has no sentence boundary and exceeds e"},
		{"80 chars exactly no period",
			"12345678901234567890123456789012345678901234567890123456789012345678901234567890",
			"12345678901234567890123456789012345678901234567890123456789012345678901234567890"},
		{"under 80 no period", "Short description without a period", "Short description without a period"},
		{"multibyte over 80 runes no period",
			"これは非常に長いツール説明です。文の境界がないため八十文字を超えるとルーンベースで切り詰められますこのテストは正しいUTF8出力を検証します。最後まで読む必要はありません",
			"これは非常に長いツール説明です。文の境界がないため八十文字を超えるとルーンベースで切り詰められますこのテストは正しいUTF8出力を検証します。最後まで読む必要は"},
		{"multibyte under 80 runes", "日本語の短い説明", "日本語の短い説明"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := compressDescription(tt.in)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestStripExamples(t *testing.T) {
	t.Run("strips examples from map schema", func(t *testing.T) {
		schema := map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
			},
			"examples": []any{map[string]any{"name": "test"}},
		}
		result := stripExamples(schema)
		m, ok := result.(map[string]any)
		require.True(t, ok)
		assert.NotContains(t, m, "examples")
		assert.Contains(t, m, "type")
		assert.Contains(t, m, "properties")
	})

	t.Run("does not mutate original schema", func(t *testing.T) {
		schema := map[string]any{
			"type":     "object",
			"examples": []any{"a"},
		}
		_ = stripExamples(schema)
		assert.Contains(t, schema, "examples", "original must be unchanged")
	})

	t.Run("no-op when no examples", func(t *testing.T) {
		schema := map[string]any{"type": "object"}
		result := stripExamples(schema)
		assert.Equal(t, schema, result, "should return same reference")
	})

	t.Run("no-op for nil schema", func(t *testing.T) {
		result := stripExamples(nil)
		assert.Nil(t, result)
	})

	t.Run("no-op for non-map schema", func(t *testing.T) {
		result := stripExamples("not a map")
		assert.Equal(t, "not a map", result)
	})

	t.Run("preserves nested examples in properties", func(t *testing.T) {
		schema := map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":     "string",
					"examples": []any{"Alice", "Bob"},
				},
			},
			"examples": []any{map[string]any{"name": "test"}},
		}
		result := stripExamples(schema)
		m, ok := result.(map[string]any)
		require.True(t, ok)
		assert.NotContains(t, m, "examples", "top-level examples should be stripped")
		// Nested examples in property sub-schemas are intentionally preserved.
		props := m["properties"].(map[string]any)
		nameProp := props["name"].(map[string]any)
		assert.Contains(t, nameProp, "examples", "nested examples must be preserved")
	})
}

func TestFilteredTools_CompressSchemas(t *testing.T) {
	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"alpha": {Command: "echo"},
		},
		Gateway: models.GatewaySettings{
			CompressSchemas: true,
		},
	}
	cfg.ApplyDefaults()

	lm := lifecycle.NewManager(cfg, "test", testLogger())
	gw := New(cfg, lm, "test", testLogger())

	lm.SetStatus("alpha", models.StatusRunning, "")
	lm.SetTools("alpha", []models.ToolInfo{
		{
			Name:        "read",
			Description: "Read a file from disk. Returns the content as UTF-8 text.",
			InputSchema: map[string]any{
				"type":     "object",
				"examples": []any{map[string]any{"path": "/tmp/test"}},
				"properties": map[string]any{
					"path": map[string]any{"type": "string"},
				},
			},
		},
		{
			Name:        "write",
			Description: "Short",
			InputSchema: map[string]any{"type": "object"},
		},
	})

	tools := gw.filteredTools()
	require.Len(t, tools, 2)

	// Tool with long description: truncated to first sentence.
	assert.Equal(t, "Read a file from disk.", tools[0].description)
	// Tool with short description: kept as-is.
	assert.Equal(t, "Short", tools[1].description)

	// Examples stripped from schema.
	m, ok := tools[0].inputSchema.(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, m, "examples")
	assert.Contains(t, m, "properties")

	// No examples to strip — schema unchanged.
	m2, ok := tools[1].inputSchema.(map[string]any)
	require.True(t, ok)
	assert.Contains(t, m2, "type")
}

func TestFilteredTools_CompressSchemasDisabled(t *testing.T) {
	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"alpha": {Command: "echo"},
		},
		Gateway: models.GatewaySettings{
			CompressSchemas: false,
		},
	}
	cfg.ApplyDefaults()

	lm := lifecycle.NewManager(cfg, "test", testLogger())
	gw := New(cfg, lm, "test", testLogger())

	lm.SetStatus("alpha", models.StatusRunning, "")
	lm.SetTools("alpha", []models.ToolInfo{
		{
			Name:        "read",
			Description: "Read a file from disk. Returns the content as UTF-8 text.",
			InputSchema: map[string]any{
				"type":     "object",
				"examples": []any{"x"},
			},
		},
	})

	tools := gw.filteredTools()
	require.Len(t, tools, 1)

	// Description NOT truncated.
	assert.Equal(t, "Read a file from disk. Returns the content as UTF-8 text.", tools[0].description)
	// Examples NOT stripped.
	m, ok := tools[0].inputSchema.(map[string]any)
	require.True(t, ok)
	assert.Contains(t, m, "examples")
}

func TestConcurrentCompressSchemas(t *testing.T) {
	gw, _ := setupGatewayWithTools(t)

	const goroutines = 10
	const iterations = 100

	var wg sync.WaitGroup

	// Toggle CompressSchemas concurrently with ListTools/RebuildTools.
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				cfg := &models.Config{
					Servers: map[string]*models.ServerConfig{
						"alpha": {Command: "echo"},
					},
					Gateway: models.GatewaySettings{
						CompressSchemas: j%2 == 0,
					},
				}
				cfg.ApplyDefaults()
				gw.UpdateConfig(cfg)
			}
		}()
	}

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_ = gw.ListTools()
			}
		}()
	}

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				gw.RebuildTools()
			}
		}()
	}

	wg.Wait()
}

// --- Phase 10.3: ConsolidateExcess tests ---

func TestFilteredTools_ConsolidateExcess(t *testing.T) {
	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"alpha": {Command: "echo"},
		},
		Gateway: models.GatewaySettings{
			ToolFilter: &models.ToolFilter{
				PerServerBudget:   2,
				ConsolidateExcess: true,
			},
		},
	}
	cfg.ApplyDefaults()

	lm := lifecycle.NewManager(cfg, "test", testLogger())
	gw := New(cfg, lm, "test", testLogger())

	lm.SetStatus("alpha", models.StatusRunning, "")
	lm.SetTools("alpha", []models.ToolInfo{
		{Name: "read", Description: "Read a file"},
		{Name: "write", Description: "Write a file"},
		{Name: "delete", Description: "Delete a file"},
		{Name: "list", Description: "List files"},
	})

	tools := gw.filteredTools()
	// Budget=2: "read", "write" + 1 meta-tool = 3 total.
	require.Len(t, tools, 3)

	assert.Equal(t, "alpha__read", tools[0].namespaced)
	assert.Equal(t, "alpha__write", tools[1].namespaced)

	meta := tools[2]
	assert.Equal(t, "alpha__more_tools", meta.namespaced)
	assert.True(t, meta.synthetic)
	assert.Equal(t, []string{"delete", "list"}, meta.allowedTools)
	assert.Contains(t, meta.description, "delete")
	assert.Contains(t, meta.description, "list")

	// Verify schema structure.
	schema, ok := meta.inputSchema.(map[string]any)
	require.True(t, ok)
	props := schema["properties"].(map[string]any)
	toolNameProp := props["tool_name"].(map[string]any)
	assert.Equal(t, "string", toolNameProp["type"])
	assert.Equal(t, []string{"delete", "list"}, toolNameProp["enum"])
}

func TestFilteredTools_ConsolidateExcessDisabled(t *testing.T) {
	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"alpha": {Command: "echo"},
		},
		Gateway: models.GatewaySettings{
			ToolFilter: &models.ToolFilter{
				PerServerBudget:   2,
				ConsolidateExcess: false,
			},
		},
	}
	cfg.ApplyDefaults()

	lm := lifecycle.NewManager(cfg, "test", testLogger())
	gw := New(cfg, lm, "test", testLogger())

	lm.SetStatus("alpha", models.StatusRunning, "")
	lm.SetTools("alpha", []models.ToolInfo{
		{Name: "read", Description: "Read"},
		{Name: "write", Description: "Write"},
		{Name: "delete", Description: "Delete"},
	})

	tools := gw.filteredTools()
	// ConsolidateExcess=false: excess tools silently dropped.
	require.Len(t, tools, 2)
	assert.Equal(t, "alpha__read", tools[0].namespaced)
	assert.Equal(t, "alpha__write", tools[1].namespaced)
}

func TestFilteredTools_ConsolidateExcessBudgetZero(t *testing.T) {
	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"alpha": {Command: "echo"},
		},
		Gateway: models.GatewaySettings{
			ToolFilter: &models.ToolFilter{
				PerServerBudget:   0,
				ConsolidateExcess: true,
			},
		},
	}
	cfg.ApplyDefaults()

	lm := lifecycle.NewManager(cfg, "test", testLogger())
	gw := New(cfg, lm, "test", testLogger())

	lm.SetStatus("alpha", models.StatusRunning, "")
	lm.SetTools("alpha", []models.ToolInfo{
		{Name: "read", Description: "Read"},
		{Name: "write", Description: "Write"},
	})

	tools := gw.filteredTools()
	// Budget=0 means no limit — no consolidation.
	require.Len(t, tools, 2)
}

func TestFilteredTools_AllToolsFitBudget(t *testing.T) {
	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"alpha": {Command: "echo"},
		},
		Gateway: models.GatewaySettings{
			ToolFilter: &models.ToolFilter{
				PerServerBudget:   5,
				ConsolidateExcess: true,
			},
		},
	}
	cfg.ApplyDefaults()

	lm := lifecycle.NewManager(cfg, "test", testLogger())
	gw := New(cfg, lm, "test", testLogger())

	lm.SetStatus("alpha", models.StatusRunning, "")
	lm.SetTools("alpha", []models.ToolInfo{
		{Name: "read", Description: "Read"},
		{Name: "write", Description: "Write"},
	})

	tools := gw.filteredTools()
	// All tools fit in budget — no meta-tool created.
	require.Len(t, tools, 2)
	for _, tool := range tools {
		assert.False(t, tool.synthetic)
	}
}

func TestFilteredTools_ConsolidateRegistration(t *testing.T) {
	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"alpha": {Command: "echo"},
		},
		Gateway: models.GatewaySettings{
			ToolFilter: &models.ToolFilter{
				PerServerBudget:   1,
				ConsolidateExcess: true,
			},
		},
	}
	cfg.ApplyDefaults()

	lm := lifecycle.NewManager(cfg, "test", testLogger())
	gw := New(cfg, lm, "test", testLogger())

	lm.SetStatus("alpha", models.StatusRunning, "")
	lm.SetTools("alpha", []models.ToolInfo{
		{Name: "read", Description: "Read"},
		{Name: "write", Description: "Write"},
		{Name: "delete", Description: "Delete"},
	})

	// RebuildTools should register the meta-tool on the MCP server.
	gw.RebuildTools()
	assert.Contains(t, gw.registeredTools, "alpha__more_tools")
	assert.Contains(t, gw.registeredTools, "alpha__read")
}

func TestFilteredTools_ConsolidateMultiServer(t *testing.T) {
	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"alpha": {Command: "echo"},
			"beta":  {Command: "echo"},
		},
		Gateway: models.GatewaySettings{
			ToolFilter: &models.ToolFilter{
				PerServerBudget:   1,
				ConsolidateExcess: true,
			},
		},
	}
	cfg.ApplyDefaults()

	lm := lifecycle.NewManager(cfg, "test", testLogger())
	gw := New(cfg, lm, "test", testLogger())

	lm.SetStatus("alpha", models.StatusRunning, "")
	lm.SetStatus("beta", models.StatusRunning, "")
	lm.SetTools("alpha", []models.ToolInfo{
		{Name: "read", Description: "Read"},
		{Name: "write", Description: "Write"},
	})
	lm.SetTools("beta", []models.ToolInfo{
		{Name: "search", Description: "Search"},
		{Name: "index", Description: "Index"},
	})

	tools := gw.filteredTools()
	// Each server: 1 real + 1 meta = 4 total.
	require.Len(t, tools, 4)

	var metas []namespacedTool
	for _, tool := range tools {
		if tool.synthetic {
			metas = append(metas, tool)
		}
	}
	assert.Len(t, metas, 2, "each server should have its own meta-tool")
}

func TestFilteredTools_ConsolidateWithCompression(t *testing.T) {
	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"alpha": {Command: "echo"},
		},
		Gateway: models.GatewaySettings{
			CompressSchemas: true,
			ToolFilter: &models.ToolFilter{
				PerServerBudget:   1,
				ConsolidateExcess: true,
			},
		},
	}
	cfg.ApplyDefaults()

	lm := lifecycle.NewManager(cfg, "test", testLogger())
	gw := New(cfg, lm, "test", testLogger())

	lm.SetStatus("alpha", models.StatusRunning, "")
	lm.SetTools("alpha", []models.ToolInfo{
		{Name: "read", Description: "Read a file from disk. Returns content as text."},
		{Name: "write", Description: "Write a file to disk. Creates or overwrites."},
	})

	tools := gw.filteredTools()
	require.Len(t, tools, 2) // 1 real + 1 meta

	// Real tool description compressed.
	assert.Equal(t, "Read a file from disk.", tools[0].description)

	// Meta-tool description also compressed (first sentence or fallback).
	meta := tools[1]
	assert.True(t, meta.synthetic)
	// Meta-tool description is "Access additional tools: write" — no period, under 80 chars.
	assert.Contains(t, meta.description, "write")
}

func TestFilteredTools_ConsolidateDescriptionCap(t *testing.T) {
	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"alpha": {Command: "echo"},
		},
		Gateway: models.GatewaySettings{
			ToolFilter: &models.ToolFilter{
				PerServerBudget:   1,
				ConsolidateExcess: true,
			},
		},
	}
	cfg.ApplyDefaults()

	lm := lifecycle.NewManager(cfg, "test", testLogger())
	gw := New(cfg, lm, "test", testLogger())

	lm.SetStatus("alpha", models.StatusRunning, "")
	// Create 15 excess tools (> metaToolDescMaxNames=10).
	tools := make([]models.ToolInfo, 16)
	tools[0] = models.ToolInfo{Name: "keep", Description: "Kept"}
	for i := 1; i < 16; i++ {
		tools[i] = models.ToolInfo{Name: fmt.Sprintf("tool_%02d", i), Description: "Desc"}
	}
	lm.SetTools("alpha", tools)

	filtered := gw.filteredTools()
	require.Len(t, filtered, 2) // 1 real + 1 meta

	meta := filtered[1]
	assert.True(t, meta.synthetic)
	assert.Contains(t, meta.description, "... and 5 more")
	// First 10 excess names should be present.
	assert.Contains(t, meta.description, "tool_01")
	assert.Contains(t, meta.description, "tool_10")
	// 11th name should NOT be in the description text.
	assert.NotContains(t, meta.description, "tool_11")
}

func TestFilteredTools_ConsolidateWithGlobalBudget(t *testing.T) {
	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"alpha": {Command: "echo"},
		},
		Gateway: models.GatewaySettings{
			ToolFilter: &models.ToolFilter{
				PerServerBudget:   2,
				ToolBudget:        2,
				ConsolidateExcess: true,
			},
		},
	}
	cfg.ApplyDefaults()

	lm := lifecycle.NewManager(cfg, "test", testLogger())
	gw := New(cfg, lm, "test", testLogger())

	lm.SetStatus("alpha", models.StatusRunning, "")
	lm.SetTools("alpha", []models.ToolInfo{
		{Name: "read", Description: "Read"},
		{Name: "write", Description: "Write"},
		{Name: "delete", Description: "Delete"},
		{Name: "list", Description: "List"},
	})

	filtered := gw.filteredTools()
	// PerServerBudget=2: "read", "write" + meta-tool = 3 before global budget.
	// ToolBudget=2 truncates to 2: meta-tool is dropped.
	require.Len(t, filtered, 2)
	assert.Equal(t, "alpha__read", filtered[0].namespaced)
	assert.Equal(t, "alpha__write", filtered[1].namespaced)
}

// --- Phase 10.3 GATE: Meta-tool dispatch handler tests ---

func TestMetaToolHandler_Dispatch(t *testing.T) {
	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"alpha": {Command: "echo"},
		},
		Gateway: models.GatewaySettings{
			ToolFilter: &models.ToolFilter{
				PerServerBudget:   1,
				ConsolidateExcess: true,
			},
		},
	}
	cfg.ApplyDefaults()

	lm := lifecycle.NewManager(cfg, "test", testLogger())
	gw := New(cfg, lm, "test", testLogger())
	lm.SetStatus("alpha", models.StatusRunning, "")

	allowedSet := map[string]struct{}{
		"delete": {},
		"list":   {},
	}
	handler := gw.makeMetaToolHandler(allowedSet, "alpha")

	makeReq := func(argsJSON string) *mcp.CallToolRequest {
		return &mcp.CallToolRequest{
			Params: &mcp.CallToolParamsRaw{
				Name:      "alpha__more_tools",
				Arguments: json.RawMessage(argsJSON),
			},
		}
	}

	tests := []struct {
		name        string
		args        string
		wantErr     bool
		wantContain string
	}{
		{
			name:        "empty tool_name",
			args:        `{}`,
			wantErr:     true,
			wantContain: "tool_name argument is required",
		},
		{
			name:        "missing tool_name key",
			args:        `{"arguments": {}}`,
			wantErr:     true,
			wantContain: "tool_name argument is required",
		},
		{
			name:        "tool_name exceeds 128 chars",
			args:        fmt.Sprintf(`{"tool_name": "%s"}`, strings.Repeat("x", 129)),
			wantErr:     true,
			wantContain: "exceeds maximum length",
		},
		{
			name:        "tool_name with namespace separator",
			args:        `{"tool_name": "foo__bar"}`,
			wantErr:     true,
			wantContain: "must not contain namespace separator",
		},
		{
			name:        "tool_name not in allowed set",
			args:        `{"tool_name": "unknown"}`,
			wantErr:     true,
			wantContain: "not in the allowed set",
		},
		{
			name:        "invalid JSON arguments",
			args:        `{invalid}`,
			wantErr:     true,
			wantContain: "invalid arguments",
		},
		{
			name:        "valid tool_name reaches dispatch",
			args:        `{"tool_name": "delete", "arguments": {}}`,
			wantErr:     true,
			wantContain: "has no active session", // passes validation, fails at CallDirect (no real session)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := handler(context.Background(), makeReq(tt.args))
			require.NoError(t, err) // handler returns tool errors via IsError, not Go errors
			require.NotNil(t, result)
			assert.Equal(t, tt.wantErr, result.IsError)
			if tt.wantContain != "" {
				text := result.Content[0].(*mcp.TextContent).Text
				assert.Contains(t, text, tt.wantContain)
			}
		})
	}
}

func TestMetaToolHandler_NilArguments(t *testing.T) {
	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"alpha": {Command: "echo"},
		},
	}
	cfg.ApplyDefaults()

	lm := lifecycle.NewManager(cfg, "test", testLogger())
	gw := New(cfg, lm, "test", testLogger())

	allowedSet := map[string]struct{}{"foo": {}}
	handler := gw.makeMetaToolHandler(allowedSet, "alpha")

	// Request with nil Arguments (no JSON body).
	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name: "alpha__more_tools",
		},
	}
	result, err := handler(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, result.Content[0].(*mcp.TextContent).Text, "tool_name argument is required")
}
