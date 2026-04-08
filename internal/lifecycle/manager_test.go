package lifecycle

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"mcp-gateway/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func buildMockServer(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	binary := filepath.Join(tmpDir, "mock-server")
	if os.PathSeparator == '\\' {
		binary += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", binary, "mcp-gateway/internal/testutil")
	cmd.Dir = filepath.Join("..", "..") // module root — go test runs from package dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "Failed to build mock server: %s", string(out))
	return binary
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestNewManager(t *testing.T) {
	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"s1": {Command: "echo"},
			"s2": {URL: "http://localhost:3000/mcp"},
		},
	}
	cfg.ApplyDefaults()

	m := NewManager(cfg, "test", testLogger())
	entries := m.Entries()
	assert.Len(t, entries, 2)

	e1, ok := m.Entry("s1")
	require.True(t, ok)
	assert.Equal(t, models.StatusStopped, e1.Status)

	_, ok = m.Entry("nonexistent")
	assert.False(t, ok)
}

func TestStart_StdioBackend(t *testing.T) {
	binary := buildMockServer(t)

	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"mock": {Command: binary},
		},
	}
	cfg.ApplyDefaults()

	m := NewManager(cfg, "test", testLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := m.Start(ctx, "mock")
	require.NoError(t, err)

	e, ok := m.Entry("mock")
	require.True(t, ok)
	assert.Equal(t, models.StatusRunning, e.Status)
	assert.Greater(t, e.PID, 0, "expected non-zero PID")
	assert.NotEmpty(t, e.Tools, "expected tools from mock server")

	// Verify tools.
	toolNames := make([]string, len(e.Tools))
	for i, tool := range e.Tools {
		toolNames[i] = tool.Name
	}
	assert.Contains(t, toolNames, "echo")
	assert.Contains(t, toolNames, "add")
	assert.Contains(t, toolNames, "fail")
	t.Logf("Started mock server PID=%d, tools=%v", e.PID, toolNames)

	// Session should be accessible.
	session, ok := m.Session("mock")
	require.True(t, ok)
	require.NotNil(t, session)

	// Cleanup.
	require.NoError(t, m.Stop(ctx, "mock"))
}

func TestStop(t *testing.T) {
	binary := buildMockServer(t)

	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"mock": {Command: binary},
		},
	}
	cfg.ApplyDefaults()

	m := NewManager(cfg, "test", testLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	require.NoError(t, m.Start(ctx, "mock"))

	e, _ := m.Entry("mock")
	assert.Equal(t, models.StatusRunning, e.Status)

	require.NoError(t, m.Stop(ctx, "mock"))

	e, _ = m.Entry("mock")
	assert.Equal(t, models.StatusStopped, e.Status)
	assert.Equal(t, 0, e.PID)
	assert.Empty(t, e.Tools)

	_, ok := m.Session("mock")
	assert.False(t, ok)
}

func TestRestart(t *testing.T) {
	binary := buildMockServer(t)

	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"mock": {Command: binary},
		},
	}
	cfg.ApplyDefaults()

	m := NewManager(cfg, "test", testLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	require.NoError(t, m.Start(ctx, "mock"))
	e1, _ := m.Entry("mock")
	pid1 := e1.PID

	require.NoError(t, m.Restart(ctx, "mock"))
	e2, _ := m.Entry("mock")
	assert.Equal(t, models.StatusRunning, e2.Status)
	assert.Equal(t, 1, e2.RestartCount)
	assert.NotEqual(t, pid1, e2.PID, "PID should change after restart")
	t.Logf("Restart: PID %d → %d, restarts=%d", pid1, e2.PID, e2.RestartCount)

	require.NoError(t, m.Stop(ctx, "mock"))
}

func TestStartAll_Concurrent(t *testing.T) {
	binary := buildMockServer(t)

	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"s1": {Command: binary},
			"s2": {Command: binary},
			"s3": {Command: binary},
		},
	}
	cfg.ApplyDefaults()

	m := NewManager(cfg, "test", testLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	require.NoError(t, m.StartAll(ctx))

	entries := m.Entries()
	for _, e := range entries {
		assert.Equal(t, models.StatusRunning, e.Status, "server %s not running", e.Name)
		assert.Greater(t, e.PID, 0, "server %s has no PID", e.Name)
	}
	t.Logf("StartAll: %d servers running concurrently", len(entries))

	m.StopAll(ctx)
	for _, e := range m.Entries() {
		assert.Equal(t, models.StatusStopped, e.Status)
	}
}

func TestStart_DisabledServer(t *testing.T) {
	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"disabled": {Command: "echo", Disabled: true},
		},
	}
	cfg.ApplyDefaults()

	m := NewManager(cfg, "test", testLogger())
	ctx := context.Background()

	require.NoError(t, m.Start(ctx, "disabled"))

	e, _ := m.Entry("disabled")
	assert.Equal(t, models.StatusDisabled, e.Status)
}

func TestStart_NonexistentServer(t *testing.T) {
	cfg := &models.Config{}
	cfg.ApplyDefaults()
	m := NewManager(cfg, "test", testLogger())

	err := m.Start(context.Background(), "nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestAddServer_RemoveServer(t *testing.T) {
	binary := buildMockServer(t)

	cfg := &models.Config{}
	cfg.ApplyDefaults()
	m := NewManager(cfg, "test", testLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Add.
	require.NoError(t, m.AddServer("new", &models.ServerConfig{Command: binary}))
	e, ok := m.Entry("new")
	require.True(t, ok)
	assert.Equal(t, models.StatusStopped, e.Status)

	// Start.
	require.NoError(t, m.Start(ctx, "new"))
	e, _ = m.Entry("new")
	assert.Equal(t, models.StatusRunning, e.Status)

	// Remove (stops automatically).
	require.NoError(t, m.RemoveServer(ctx, "new"))
	_, ok = m.Entry("new")
	assert.False(t, ok)
}

func TestAddServer_Duplicate(t *testing.T) {
	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"exists": {Command: "echo"},
		},
	}
	cfg.ApplyDefaults()
	m := NewManager(cfg, "test", testLogger())

	err := m.AddServer("exists", &models.ServerConfig{Command: "echo"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestReconcile(t *testing.T) {
	binary := buildMockServer(t)

	oldCfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"keep":   {Command: binary},
			"remove": {Command: binary},
			"change": {Command: binary, Args: []string{"--old"}},
		},
	}
	oldCfg.ApplyDefaults()

	m := NewManager(oldCfg, "test", testLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	require.NoError(t, m.StartAll(ctx))

	newCfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"keep":   {Command: binary},
			"change": {Command: binary, Args: []string{"--new"}},
			"added":  {Command: binary},
		},
	}
	newCfg.ApplyDefaults()

	require.NoError(t, m.Reconcile(ctx, newCfg))

	// "remove" should be gone.
	_, ok := m.Entry("remove")
	assert.False(t, ok, "removed server should not exist")

	// "keep" should still be running.
	e, ok := m.Entry("keep")
	require.True(t, ok)
	assert.Equal(t, models.StatusRunning, e.Status)

	// "change" should have been restarted with new args.
	e, ok = m.Entry("change")
	require.True(t, ok)
	assert.Equal(t, models.StatusRunning, e.Status)
	assert.Equal(t, []string{"--new"}, e.Config.Args)

	// "added" should be running.
	e, ok = m.Entry("added")
	require.True(t, ok)
	assert.Equal(t, models.StatusRunning, e.Status)

	m.StopAll(ctx)
}

func TestSetStatus(t *testing.T) {
	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"s1": {Command: "echo"},
		},
	}
	cfg.ApplyDefaults()
	m := NewManager(cfg, "test", testLogger())

	m.SetStatus("s1", models.StatusDegraded, "ping timeout")
	e, _ := m.Entry("s1")
	assert.Equal(t, models.StatusDegraded, e.Status)
	assert.Equal(t, "ping timeout", e.LastError)

	m.SetStatus("s1", models.StatusRunning, "")
	e, _ = m.Entry("s1")
	assert.Equal(t, models.StatusRunning, e.Status)
	assert.NotZero(t, e.LastPing)
}

func TestConcurrentAccess(t *testing.T) {
	binary := buildMockServer(t)

	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"s1": {Command: binary},
		},
	}
	cfg.ApplyDefaults()

	m := NewManager(cfg, "test", testLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	require.NoError(t, m.Start(ctx, "s1"))

	// Concurrent reads while status updates happen.
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = m.Entries()
		}()
		go func() {
			defer wg.Done()
			m.SetStatus("s1", models.StatusRunning, "")
		}()
	}
	wg.Wait()

	require.NoError(t, m.Stop(ctx, "s1"))
}
