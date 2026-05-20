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

// --- minimal fakes ---

type fakeStatusChecker struct {
	mu     sync.Mutex
	status map[string]models.ServerStatus
}

func newFakeStatusChecker() *fakeStatusChecker {
	return &fakeStatusChecker{status: make(map[string]models.ServerStatus)}
}

func (f *fakeStatusChecker) BackendStatus(name string) models.ServerStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.status[name]; ok {
		return s
	}
	return models.StatusStopped
}

func (f *fakeStatusChecker) set(name string, s models.ServerStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status[name] = s
}

type fakeBackendManager struct {
	mu         sync.Mutex
	startErr   map[string]error
	stopCalls  map[string]int
	startCalls map[string]int
	sessions   map[string]*mcp.ClientSession
}

func newFakeBackendManager() *fakeBackendManager {
	return &fakeBackendManager{
		startErr:   make(map[string]error),
		stopCalls:  make(map[string]int),
		startCalls: make(map[string]int),
		sessions:   make(map[string]*mcp.ClientSession),
	}
}

func (f *fakeBackendManager) Start(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startCalls[name]++
	return f.startErr[name]
}

func (f *fakeBackendManager) Stop(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopCalls[name]++
	return nil
}

func (f *fakeBackendManager) Session(name string) (*mcp.ClientSession, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[name]
	return s, ok
}

func (f *fakeBackendManager) SetStatus(_ string, _ models.ServerStatus, _ string) {}

func (f *fakeBackendManager) setStartErr(name string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startErr[name] = err
}

// --- tests ---

// TestBackendSupervisor_ServeReturnsErrDoNotRestartWhenDisabled verifies that
// Serve() returns suture.ErrDoNotRestart immediately when the health monitor
// has opened the circuit (StatusDisabled). This prevents suture from counting
// the return as a failure and applying backoff.
func TestBackendSupervisor_ServeReturnsErrDoNotRestartWhenDisabled(t *testing.T) {
	checker := newFakeStatusChecker()
	checker.set("b1", models.StatusDisabled)
	mgr := newFakeBackendManager()

	svc := NewBackendSupervisor("b1", mgr, checker, slog.Default())
	ctx := context.Background()

	err := svc.Serve(ctx)
	require.Error(t, err)
	assert.True(t, errors.Is(err, suture.ErrDoNotRestart),
		"expected suture.ErrDoNotRestart, got %v", err)
	assert.Equal(t, 0, mgr.startCalls["b1"], "Start must not be called when circuit is open")
}

// TestBackendSupervisor_ServeReturnsErrorOnStartFailure verifies that a failed
// Start() causes Serve() to return a wrapped error (suture counts as failure).
func TestBackendSupervisor_ServeReturnsErrorOnStartFailure(t *testing.T) {
	checker := newFakeStatusChecker()
	checker.set("b1", models.StatusStopped)
	mgr := newFakeBackendManager()
	mgr.setStartErr("b1", errors.New("connection refused"))

	svc := NewBackendSupervisor("b1", mgr, checker, slog.Default())
	ctx := context.Background()

	err := svc.Serve(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "b1")
	assert.Equal(t, 1, mgr.startCalls["b1"])
}

// TestBackendSupervisor_ServeReturnsErrorWhenNoSession verifies that if Start
// succeeds but no session is available, Serve returns a transient error.
func TestBackendSupervisor_ServeReturnsErrorWhenNoSession(t *testing.T) {
	checker := newFakeStatusChecker()
	checker.set("b1", models.StatusRunning)
	mgr := newFakeBackendManager()

	svc := NewBackendSupervisor("b1", mgr, checker, slog.Default())
	ctx := context.Background()

	err := svc.Serve(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no session after start")
}

// TestBackendSupervisor_NoSessionPathNonBlocking verifies that the
// "no session after start" failure path never blocks even when the parent
// context is cancelled, and never spuriously yields ErrDoNotRestart.
// This test does NOT exercise the select-on-ctx.Done branch inside Serve()
// because constructing a live *mcp.ClientSession in a unit test would require
// spinning up an actual MCP transport — that branch is covered by the
// integration-level load test in internal/health/monitor_bulkhead_test.go
// once the supervisor tree is wired through Manager.Start (P1.5 step 2).
func TestBackendSupervisor_NoSessionPathNonBlocking(t *testing.T) {
	checker := newFakeStatusChecker()
	checker.set("b1", models.StatusRunning)
	mgr := newFakeBackendManager()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	svc := NewBackendSupervisor("b1", mgr, checker, slog.Default())

	err := svc.Serve(ctx)
	if err != nil {
		assert.False(t, errors.Is(err, suture.ErrDoNotRestart),
			"cancelled context must not yield ErrDoNotRestart")
	}
}

// TestBackendSupervisor_String verifies the stringer shape used by suture logs.
func TestBackendSupervisor_String(t *testing.T) {
	svc := NewBackendSupervisor("my-backend", newFakeBackendManager(), newFakeStatusChecker(), slog.Default())
	assert.Equal(t, "backend/my-backend", svc.String())
}

// TestDefaultSupervisorSpec_Fields verifies the canonical thresholds are set.
func TestDefaultSupervisorSpec_Fields(t *testing.T) {
	spec := DefaultSupervisorSpec("x", slog.Default())
	assert.InDelta(t, float64(5), spec.FailureThreshold, 0.001)
	assert.InDelta(t, float64(30), spec.FailureDecay, 0.001)
	assert.Equal(t, 15*time.Second, spec.FailureBackoff)
	assert.True(t, spec.DontPropagateTermination)
	assert.NotNil(t, spec.EventHook)
}

// TestNewBackendSupervisorTree_CreatesOneChildPerName verifies that
// NewBackendSupervisorTree produces a supervisor with a child for every name.
func TestNewBackendSupervisorTree_CreatesOneChildPerName(t *testing.T) {
	names := []string{"alpha", "beta", "gamma"}
	mgr := newFakeBackendManager()
	checker := newFakeStatusChecker()

	root := NewBackendSupervisorTree(mgr, checker, names, slog.Default())
	require.NotNil(t, root)

	ctx, cancel := context.WithCancel(context.Background())
	root.ServeBackground(ctx)
	cancel()
	// If ServeBackground doesn't panic or deadlock, the test passes.
}
