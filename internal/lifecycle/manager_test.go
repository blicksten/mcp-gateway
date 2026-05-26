package lifecycle

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mcp-gateway/internal/logbuf"
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

	// Remove (stops automatically). The primary error is non-nil only when the
	// server is not found; Stop errors surface via RemoveResult.Orphan.
	result, err := m.RemoveServer(ctx, "new")
	require.NoError(t, err)
	assert.False(t, result.Orphan, "clean stop must not produce an orphan")
	_, ok = m.Entry("new")
	assert.False(t, ok)
}

// TestRemoveServer_StopErrorSurfacesOrphan verifies that when Stop returns a
// non-nil error, RemoveServer surfaces Orphan=true in the result. The entry
// is still deleted from the manager (caller's remove intent is honoured).
func TestRemoveServer_StopErrorSurfacesOrphan(t *testing.T) {
	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"flaky": {URL: "http://localhost:9999/mcp"},
		},
	}
	cfg.ApplyDefaults()
	m := NewManager(cfg, "test", testLogger())

	// Inject a failing Stop that simulates a process that didn't exit.
	stopErr := fmt.Errorf("process did not exit in time")
	m.testStopHook = func(name string) error {
		return stopErr
	}

	ctx := context.Background()
	result, err := m.RemoveServer(ctx, "flaky")
	require.NoError(t, err, "primary error must be nil — server was found")
	assert.True(t, result.Orphan, "Stop failure must set Orphan=true")
	assert.ErrorIs(t, result.StopErr, stopErr, "StopErr must wrap the original Stop error")

	// Entry must be deleted despite the Stop failure.
	_, ok := m.Entry("flaky")
	assert.False(t, ok, "entry must be removed even when Stop fails")
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

// TestScanStderr_AcceptsLineOver64KB pins the 1MB scanner cap added in
// T15A.2b. Before v1.5.0 the default 64KB bufio.Scanner limit caused
// scanStderr to exit with bufio.ErrTooLong on long child-process stderr
// lines (stack traces, JSON traces), dropping the line entirely — it
// never reached the ring buffer. Regression would silently re-introduce
// the drop. Asserting the line count (not length) sidesteps logbuf.Redact
// which rewrites long base64url-shaped strings to a fixed placeholder.
func TestScanStderr_AcceptsLineOver64KB(t *testing.T) {
	ring := logbuf.New(16)

	// 256KB single line using "line " chunks separated by spaces so the
	// logbuf.Redact base64url catch-all (`\b[A-Za-z0-9_\-]{32,}\b`) does
	// NOT match — we're testing the scanner, not the redactor.
	longLine := strings.Repeat("line ", 50*1024) + "\n"
	reader := strings.NewReader(longLine)

	scanStderr(ring, reader, "test-server", testLogger())

	lines := ring.Lines()
	require.Len(t, lines, 1, "250KB+ line must reach the ring buffer in one piece (old 64KB cap would drop it entirely via bufio.ErrTooLong)")
	assert.NotEmpty(t, lines[0].Text, "scanner must deliver the line content")
}

// --- Fix 2 regression guard: TCP pre-check fast-fail for unreachable HTTP backends ---

// closedPort returns a localhost port that was just closed (so the OS refuses
// connections on it). The listen + close approach avoids using a hard-coded
// port that might be occupied, and produces a reliably refused address.
func closedPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

// TestStart_HTTPBackend_UnreachableHost_FastFail verifies that Manager.Start
// returns quickly (within 6 seconds) with an error containing "host unreachable"
// when the HTTP backend URL is not reachable — instead of the previous 42-second
// Windows connectex timeout.  This is the regression guard for the
// checkTCPReachable pre-check added in the unfreeze-button fix.
func TestStart_HTTPBackend_UnreachableHost_FastFail(t *testing.T) {
	addr := closedPort(t)
	backendURL := "http://" + addr + "/mcp"

	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"unreachable": {URL: backendURL},
		},
	}
	cfg.ApplyDefaults()

	m := NewManager(cfg, "test", testLogger())

	// 10-second context — must complete well before that; old code took ~42s on Windows.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start := time.Now()
	err := m.Start(ctx, "unreachable")
	elapsed := time.Since(start)

	require.Error(t, err, "Start must fail for unreachable host")
	assert.Less(t, elapsed, 6*time.Second,
		"Start must fast-fail within 6s (TCP pre-check), not block for ~42s (old Windows connectex timeout); elapsed=%s", elapsed)
	assert.Contains(t, err.Error(), "host unreachable",
		"error must contain 'host unreachable' from checkTCPReachable")

	// Backend status must be StatusUnreachable (NOT StatusError or
	// StatusStarting). pdap-docs unreachable feature
	// (docs/PLAN-unreachable-handling.md) routes TCP-level failures to
	// StatusUnreachable so the UI shows a stable yellow warning and the
	// health monitor switches to slow-poll recovery instead of the old
	// aggressive exponential-restart loop.
	e, ok := m.Entry("unreachable")
	require.True(t, ok)
	assert.Equal(t, models.StatusUnreachable, e.Status,
		"backend must be in StatusUnreachable after TCP pre-check failure")
}

// TestStart_StdioBackend_NoTCPCheck verifies that stdio backends (cfg.URL == "",
// cfg.Command set) do NOT go through the TCP pre-check.  The pre-check is only
// applicable to HTTP/SSE backends.  A stdio backend with a valid binary should
// start successfully without any TCP dial attempt.
func TestStart_StdioBackend_NoTCPCheck(t *testing.T) {
	binary := buildMockServer(t)

	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			// URL is empty — stdio transport; Command is set.
			"stdio-only": {Command: binary},
		},
	}
	cfg.ApplyDefaults()

	m := NewManager(cfg, "test", testLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Must start successfully — no TCP pre-check should block it.
	err := m.Start(ctx, "stdio-only")
	require.NoError(t, err, "stdio backend must start without TCP pre-check interference")

	e, ok := m.Entry("stdio-only")
	require.True(t, ok)
	assert.Equal(t, models.StatusRunning, e.Status,
		"stdio backend must reach StatusRunning — URL-based pre-check must not apply")

	require.NoError(t, m.Stop(ctx, "stdio-only"))
}
