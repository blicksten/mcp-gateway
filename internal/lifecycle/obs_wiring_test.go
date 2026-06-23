package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcp-gateway/internal/models"
	"mcp-gateway/internal/obs"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readEventLines returns every JSONL line written by the emitter under
// <configDir>/events/. The emitter opens exactly one file per process, so a
// single matching file is expected; we concatenate defensively.
func readEventLines(t *testing.T, configDir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(configDir, "events", "*.jsonl"))
	require.NoError(t, err)
	var lines []string
	for _, p := range matches {
		b, err := os.ReadFile(p)
		require.NoError(t, err)
		for _, ln := range strings.Split(string(b), "\n") {
			if strings.TrimSpace(ln) != "" {
				lines = append(lines, ln)
			}
		}
	}
	return lines
}

// TestObsWiring_SpawnConnectEmitted asserts the lifecycle layer emits
// backend.spawn + backend.connect (PLAN §B.2 / item 4) when the emitter is
// enabled, and that a real OS pid is recorded. This is the gateway-side
// attribution of "a backend started".
func TestObsWiring_SpawnConnectEmitted(t *testing.T) {
	binary := buildMockServer(t)

	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"mock": {Command: binary},
		},
	}
	cfg.ApplyDefaults()

	// Enable the emitter and point its JSONL sink at a temp configDir.
	t.Setenv("MCP_GATEWAY_TRACE", "1")
	configDir := t.TempDir()
	emitter := obs.NewEmitter(configDir, testLogger())
	require.True(t, emitter.Enabled(), "emitter must be enabled with MCP_GATEWAY_TRACE=1")
	defer func() { _ = emitter.Close() }()

	m := NewManager(cfg, "test", testLogger())
	m.SetEmitter(emitter)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	require.NoError(t, m.Start(ctx, "mock"))
	defer func() { _ = m.Stop(ctx, "mock") }()

	require.NoError(t, emitter.Close()) // flush before reading
	lines := readEventLines(t, configDir)

	var sawSpawn, sawConnect bool
	for _, ln := range lines {
		if strings.Contains(ln, `"event":"backend.spawn"`) {
			sawSpawn = true
			assert.Contains(t, ln, `"target":"mock"`)
			assert.Contains(t, ln, `"pid":`)
		}
		if strings.Contains(ln, `"event":"backend.connect"`) {
			sawConnect = true
			assert.Contains(t, ln, `"ms":`)
		}
	}
	assert.True(t, sawSpawn, "expected a backend.spawn event; lines=%v", lines)
	assert.True(t, sawConnect, "expected a backend.connect event; lines=%v", lines)
}

// TestObsWiring_RestartPushesKillRing asserts that an explicit Restart records
// a backend.restart entry in the KillRing with actor=manual (PLAN §D.1 — the
// data source for the future /debug/dump), distinguishing operator restarts
// from suture crash-restarts.
func TestObsWiring_RestartPushesKillRing(t *testing.T) {
	binary := buildMockServer(t)

	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"mock": {Command: binary},
		},
	}
	cfg.ApplyDefaults()

	t.Setenv("MCP_GATEWAY_TRACE", "1")
	emitter := obs.NewEmitter(t.TempDir(), testLogger())
	require.True(t, emitter.Enabled())
	defer func() { _ = emitter.Close() }()

	m := NewManager(cfg, "test", testLogger())
	m.SetEmitter(emitter)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	require.NoError(t, m.Start(ctx, "mock"))
	require.NoError(t, m.Restart(ctx, "mock"))
	defer func() { _ = m.Stop(ctx, "mock") }()

	// Restart pushes two related KillRing entries: the orchestration entry from
	// Restart itself (Method "stop+start", actor manual) and the process-kill
	// entry from the Stop it drives (Method "terminate+kill-group", reason
	// restart). Match on Method to disambiguate — that is the field that tells
	// an operator restart apart from the kill it caused.
	var sawRestartOrchestration, sawKillDuringRestart bool
	for _, ke := range emitter.Kills().Snapshot() {
		if ke.Backend != "mock" {
			continue
		}
		switch ke.Method {
		case "stop+start":
			sawRestartOrchestration = true
			assert.Equal(t, "manual", ke.Actor)
			assert.Equal(t, "restart", ke.Reason)
		case "terminate+kill-group":
			if ke.Reason == "restart" {
				sawKillDuringRestart = true
			}
		}
	}
	assert.True(t, sawRestartOrchestration,
		"expected a backend.restart KillRing entry (actor=manual, method=stop+start)")
	assert.True(t, sawKillDuringRestart,
		"expected the kill driven by the restart (method=terminate+kill-group, reason=restart)")
}

// TestObsWiring_NilEmitterNoOp asserts a Manager with no emitter (the default
// for tests and legacy callers) neither panics nor blocks — the nil-safe
// contract (PLAN §E zero-cost-when-off).
func TestObsWiring_NilEmitterNoOp(t *testing.T) {
	binary := buildMockServer(t)

	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"mock": {Command: binary},
		},
	}
	cfg.ApplyDefaults()

	m := NewManager(cfg, "test", testLogger())
	// Intentionally NOT calling SetEmitter — m.emitter stays nil.

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	require.NoError(t, m.Start(ctx, "mock"))
	require.NoError(t, m.Restart(ctx, "mock"))
	require.NoError(t, m.Stop(ctx, "mock"))
}
