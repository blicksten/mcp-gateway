package lifecycle

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"mcp-gateway/internal/models"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thejerf/suture/v4"
)

// startInProcessMCPSession starts an in-process MCP server/client pair.
// Cancel ctx to stop the server and unblock session.Wait().
func startInProcessMCPSession(ctx context.Context, t *testing.T) *mcp.ClientSession {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "test-srv", Version: "1"}, nil)
	cliTrans, srvTrans := mcp.NewInMemoryTransports()
	go func() { _ = srv.Run(ctx, srvTrans) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-cli", Version: "1"}, nil)
	sess, err := client.Connect(ctx, cliTrans, nil)
	require.NoError(t, err)
	return sess
}

// sessionBackedFakeManager satisfies BackendManager with a pre-built session
// so Serve() can reach the session.Wait() select branch.
type sessionBackedFakeManager struct {
	mu     sync.Mutex
	sess   *mcp.ClientSession
	startN int
}

func (m *sessionBackedFakeManager) Start(_ context.Context, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.startN++
	return nil
}

func (m *sessionBackedFakeManager) Stop(_ context.Context, _ string) error { return nil }

func (m *sessionBackedFakeManager) Session(_ string) (*mcp.ClientSession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sess == nil {
		return nil, false
	}
	return m.sess, true
}

func (m *sessionBackedFakeManager) SetStatus(_ string, _ models.ServerStatus, _ string) {}

func (m *sessionBackedFakeManager) startCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.startN
}

// TestBackendSupervisor_CleanStopNoRestart_HIGH2 verifies the HIGH-2 fix:
// when Manager.Stop has set StatusStopped before session.Wait() unblocks,
// the session-ended branch of Serve() returns ErrDoNotRestart so suture
// does not schedule a phantom restart.
//
// Uses a real in-process *mcp.ClientSession so Serve() reaches the
// session.Wait() select (unit tests with fakeBackendManager cannot reach
// this branch because Session() returns nil there).
func TestBackendSupervisor_CleanStopNoRestart_HIGH2(t *testing.T) {
	const name = "test-backend"

	srvCtx, srvCancel := context.WithCancel(context.Background())
	defer srvCancel()
	sess := startInProcessMCPSession(srvCtx, t)

	// StatusStopped — simulates Manager.Stop having already set the status.
	checker := newFakeStatusChecker()
	checker.set(name, models.StatusStopped)

	mgr := &sessionBackedFakeManager{sess: sess}
	svc := NewBackendSupervisor(name, mgr, checker, slog.Default())

	// Cancel server so session.Wait() unblocks inside Serve().
	srvCancel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := svc.Serve(ctx)
	require.Error(t, err)
	assert.True(t, errors.Is(err, suture.ErrDoNotRestart),
		"HIGH-2: StatusStopped after session ends must return ErrDoNotRestart, got: %v", err)
	assert.Equal(t, 1, mgr.startCount(),
		"Start must be called exactly once; no phantom restart after deliberate stop")
}

// sessionBackedFakeManagerWithHook is like sessionBackedFakeManager but calls
// an optional post-Start hook so tests can synchronise on Start completing.
type sessionBackedFakeManagerWithHook struct {
	sessionBackedFakeManager
	afterStart func()
}

func (m *sessionBackedFakeManagerWithHook) Start(ctx context.Context, name string) error {
	err := m.sessionBackedFakeManager.Start(ctx, name)
	if m.afterStart != nil {
		m.afterStart()
	}
	return err
}

// TestBackendSupervisor_DisabledBottomBranch_HIGH2 exercises the bottom-branch
// path of the HIGH-2 fix where the circuit opens (StatusDisabled) after
// Start() succeeds but before session.Wait() unblocks.
//
// The existing top-gate unit test only covers the early-return before Start().
// This test drives a real session to termination by:
//   1. Starting Serve() with StatusRunning so the top gate passes and Start() runs.
//   2. Waiting (via channel) until Start() completes, then flipping to StatusDisabled.
//   3. Cancelling the server so session.Wait() unblocks with Disabled status.
//
// The bottom select-branch must return ErrDoNotRestart in this scenario.
func TestBackendSupervisor_DisabledBottomBranch_HIGH2(t *testing.T) {
	const name = "test-backend"

	srvCtx, srvCancel := context.WithCancel(context.Background())
	defer srvCancel()
	sess := startInProcessMCPSession(srvCtx, t)

	checker := newFakeStatusChecker()
	// StatusRunning so the top gate passes and Serve() proceeds to Start().
	checker.set(name, models.StatusRunning)

	// startDone is closed by afterStart hook once Start() returns inside Serve().
	startDone := make(chan struct{})
	mgr := &sessionBackedFakeManagerWithHook{
		sessionBackedFakeManager: sessionBackedFakeManager{sess: sess},
		afterStart: func() { close(startDone) },
	}
	svc := NewBackendSupervisor(name, mgr, checker, slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Run Serve in a goroutine; it will block on session.Wait().
	result := make(chan error, 1)
	go func() { result <- svc.Serve(ctx) }()

	// Wait until Start() has returned so we are inside the session.Wait() select.
	select {
	case <-startDone:
	case <-ctx.Done():
		t.Fatal("timed out waiting for Start() inside Serve()")
	}

	// Now flip to StatusDisabled and cancel the server — bottom branch fires.
	checker.set(name, models.StatusDisabled)
	srvCancel()

	select {
	case err := <-result:
		require.Error(t, err)
		assert.True(t, errors.Is(err, suture.ErrDoNotRestart),
			"HIGH-2 bottom branch: StatusDisabled when session ends must return ErrDoNotRestart, got: %v", err)
		assert.Equal(t, 1, mgr.startCount(),
			"Start must be called exactly once")
	case <-ctx.Done():
		t.Fatal("timed out waiting for Serve() to return")
	}
}

// TestBackendSupervisor_UnexpectedSessionLossRestarts is the counter-test:
// session ends without a Manager.Stop (status stays Running) → Serve returns
// a non-ErrDoNotRestart error so suture is allowed to restart.
func TestBackendSupervisor_UnexpectedSessionLossRestarts(t *testing.T) {
	const name = "test-backend"

	srvCtx, srvCancel := context.WithCancel(context.Background())
	defer srvCancel()
	sess := startInProcessMCPSession(srvCtx, t)

	// StatusRunning — no deliberate stop.
	checker := newFakeStatusChecker()
	checker.set(name, models.StatusRunning)

	mgr := &sessionBackedFakeManager{sess: sess}
	svc := NewBackendSupervisor(name, mgr, checker, slog.Default())

	// Close server without setting Stopped (simulates unexpected crash).
	srvCancel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := svc.Serve(ctx)
	require.Error(t, err)
	assert.False(t, errors.Is(err, suture.ErrDoNotRestart),
		"unexpected session loss (StatusRunning) must NOT return ErrDoNotRestart")
}

// TestBackendSupervisor_RestartWithRealSession_NoErrDoNotRestart verifies the
// Restart-cycle path through the bottom select branch with a REAL session.
//
// Prior version (TestBackendSupervisor_RestartStatusNotStopped) was a false
// positive — newFakeBackendManager.Session() returned nil so Serve() exited
// early at "no session after start" and never reached the bottom select
// branch where the status check lives.
//
// This test:
//   1. Starts Serve() with StatusRunning so the top gate passes and Start()
//      runs against a real in-process MCP session.
//   2. Waits (via channel) until Start() completes, then flips status to
//      StatusRestarting (simulating Restart having set it AND Stop having
//      preserved it via the HIGH-2 R-01 conditional-write fix at manager.go
//      ~line 480).
//   3. Cancels the server so session.Wait() unblocks with Restarting status.
//
// Bottom branch must FALL THROUGH (no ErrDoNotRestart) and return a generic
// error so suture's FailureBackoff retries — Restart's own m.Start runs
// concurrently and the `starting` guard serialises the two paths.
func TestBackendSupervisor_RestartWithRealSession_NoErrDoNotRestart(t *testing.T) {
	const name = "test-backend"

	srvCtx, srvCancel := context.WithCancel(context.Background())
	defer srvCancel()
	sess := startInProcessMCPSession(srvCtx, t)

	checker := newFakeStatusChecker()
	checker.set(name, models.StatusRunning)

	startDone := make(chan struct{})
	mgr := &sessionBackedFakeManagerWithHook{
		sessionBackedFakeManager: sessionBackedFakeManager{sess: sess},
		afterStart:               func() { close(startDone) },
	}
	svc := NewBackendSupervisor(name, mgr, checker, slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := make(chan error, 1)
	go func() { result <- svc.Serve(ctx) }()

	select {
	case <-startDone:
	case <-ctx.Done():
		t.Fatal("timed out waiting for Start() inside Serve()")
	}

	// Simulate Restart cycle: status is Restarting at the moment session ends.
	checker.set(name, models.StatusRestarting)
	srvCancel()

	select {
	case err := <-result:
		require.Error(t, err)
		assert.False(t, errors.Is(err, suture.ErrDoNotRestart),
			"R-01 + R-03: StatusRestarting when session ends must NOT return ErrDoNotRestart (Restart in flight; suture should re-queue), got: %v", err)
	case <-ctx.Done():
		t.Fatal("timed out waiting for Serve() to return")
	}
}

// TestManager_SetupAndServeBackgroundSupervisor verifies that SetupSupervisor
// and ServeBackgroundSupervisor wire correctly: the tree starts, and the stop
// function cancels it cleanly without deadlock.
func TestManager_SetupAndServeBackgroundSupervisor(t *testing.T) {
	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{},
	}
	cfg.ApplyDefaults()

	m := NewManager(cfg, "test", slog.Default())

	require.NotPanics(t, func() {
		m.SetupSupervisor(slog.Default())
	})

	ctx := t.Context()

	var stopFn func()
	require.NotPanics(t, func() {
		stopFn = m.ServeBackgroundSupervisor(ctx)
	})
	require.NotNil(t, stopFn)

	done := make(chan struct{})
	go func() {
		stopFn()
		close(done)
	}()

	select {
	case <-done:
		// clean stop
	case <-time.After(5 * time.Second):
		t.Fatal("stop function did not return within 5s (deadlock?)")
	}
}

// TestManager_StartAllNoOpWhenSupervisorActive verifies that StartAll returns
// nil immediately when the supervisor tree is active, preventing double-start.
func TestManager_StartAllNoOpWhenSupervisorActive(t *testing.T) {
	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{},
	}
	cfg.ApplyDefaults()

	m := NewManager(cfg, "test", slog.Default())
	m.SetupSupervisor(slog.Default())

	ctx := t.Context()
	_ = m.ServeBackgroundSupervisor(ctx)

	err := m.StartAll(context.Background())
	assert.NoError(t, err, "StartAll must be a no-op when supervisor is active")
}

// TestManager_ServeBackgroundSupervisor_PanicsWithoutSetup verifies that
// calling ServeBackgroundSupervisor before SetupSupervisor panics clearly.
func TestManager_ServeBackgroundSupervisor_PanicsWithoutSetup(t *testing.T) {
	cfg := &models.Config{Servers: map[string]*models.ServerConfig{}}
	cfg.ApplyDefaults()
	m := NewManager(cfg, "test", slog.Default())

	assert.Panics(t, func() {
		m.ServeBackgroundSupervisor(context.Background())
	})
}

// TestStop_PreservesStatusRestarting exercises the R-01 conditional-write
// fix at manager.go ~line 480. When Stop is entered with status already set
// to StatusRestarting (i.e. Restart has set it before calling Stop), Stop
// must NOT overwrite the status — otherwise the supervisor's bottom-branch
// status check would observe Stopped at session.Wait() unblock and return
// suture.ErrDoNotRestart, silently removing the backend from the tree on
// every managed restart.
//
// Replaces TestManager_Restart_SetsRestartingBeforeStop which used
// testStopHook and never executed the real Stop body where line 480 lives.
func TestStop_PreservesStatusRestarting(t *testing.T) {
	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"s1": {URL: "http://localhost:19999/mcp"},
		},
	}
	cfg.ApplyDefaults()
	m := NewManager(cfg, "test", slog.Default())

	// Simulate the state Restart leaves before calling Stop.
	m.SetStatus("s1", models.StatusRestarting, "")

	require.NoError(t, m.Stop(context.Background(), "s1"))

	assert.Equal(t, models.StatusRestarting, m.BackendStatus("s1"),
		"R-01: Stop must PRESERVE StatusRestarting (not overwrite with Stopped)")
}

// TestStop_OverwritesToStoppedWhenNotRestarting is the counter-test for the
// R-01 conditional write. A plain Stop call (status not Restarting) MUST
// transition the status to Stopped so the HIGH-2 deliberate-stop detection
// fires on the supervisor's bottom branch.
func TestStop_OverwritesToStoppedWhenNotRestarting(t *testing.T) {
	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"s1": {URL: "http://localhost:19999/mcp"},
		},
	}
	cfg.ApplyDefaults()
	m := NewManager(cfg, "test", slog.Default())

	m.SetStatus("s1", models.StatusRunning, "")

	require.NoError(t, m.Stop(context.Background(), "s1"))

	assert.Equal(t, models.StatusStopped, m.BackendStatus("s1"),
		"R-01 counter-test: plain Stop (status=Running) MUST overwrite to Stopped")
}
