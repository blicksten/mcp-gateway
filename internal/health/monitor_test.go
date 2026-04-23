package health

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"mcp-gateway/internal/models"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockLM implements LifecycleManager for testing.
type mockLM struct {
	mu       sync.Mutex
	entries  map[string]models.ServerEntry
	sessions map[string]*mcp.ClientSession

	statusLog []statusEvent
	restarts  int
	starts    int

	// Control behavior.
	restartErr error
	startErr   error
	pingOK     bool
}

type statusEvent struct {
	Name    string
	Status  models.ServerStatus
	LastErr string
}

func newMockLM() *mockLM {
	return &mockLM{
		entries:  make(map[string]models.ServerEntry),
		sessions: make(map[string]*mcp.ClientSession),
		pingOK:   true,
	}
}

func (m *mockLM) addEntry(name string, status models.ServerStatus, cfg models.ServerConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[name] = models.ServerEntry{
		Name:   name,
		Config: cfg,
		Status: status,
	}
}

func (m *mockLM) Entries() []models.ServerEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]models.ServerEntry, 0, len(m.entries))
	for _, e := range m.entries {
		result = append(result, e)
	}
	return result
}

func (m *mockLM) Session(name string) (*mcp.ClientSession, bool) {
	// We don't use real sessions in health tests — ping is mocked via checkMCPPing override.
	return nil, false
}

func (m *mockLM) SetStatus(name string, status models.ServerStatus, lastErr string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[name]; ok {
		e.Status = status
		e.LastError = lastErr
		m.entries[name] = e
	}
	m.statusLog = append(m.statusLog, statusEvent{name, status, lastErr})
}

func (m *mockLM) Restart(ctx context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.restarts++
	if m.restartErr != nil {
		return m.restartErr
	}
	if e, ok := m.entries[name]; ok {
		e.Status = models.StatusRunning
		e.RestartCount++
		m.entries[name] = e
	}
	return nil
}

func (m *mockLM) Start(ctx context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.starts++
	if m.startErr != nil {
		return m.startErr
	}
	if e, ok := m.entries[name]; ok {
		e.Status = models.StatusRunning
		m.entries[name] = e
	}
	return nil
}

func (m *mockLM) lastStatus(name string) models.ServerStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.statusLog) - 1; i >= 0; i-- {
		if m.statusLog[i].Name == name {
			return m.statusLog[i].Status
		}
	}
	return ""
}

func (m *mockLM) getRestarts() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.restarts
}

// testMonitor creates a monitor with a mock lifecycle manager.
// It overrides the MCP ping to use the mock's pingOK flag.
type testableMonitor struct {
	*Monitor
	mock *mockLM
}

func newTestableMonitor(mock *mockLM) *testableMonitor {
	mon := NewMonitor(mock, 1*time.Second, nil)
	mon.ConsecutiveFailureThreshold = 3
	mon.CircuitBreakerThreshold = 5
	mon.CircuitBreakerWindow = 300 * time.Second
	return &testableMonitor{Monitor: mon, mock: mock}
}

// checkOneOverride replaces the MCP ping with mock behavior.
func (tm *testableMonitor) checkOneWithMockPing(ctx context.Context, entry models.ServerEntry, mcpOK bool) {
	name := entry.Name

	restOK := true
	if entry.Config.RestURL != "" && entry.Config.HealthEndpoint != "" {
		restOK = tm.checkRESTHealth(ctx, entry.Config.RestURL+entry.Config.HealthEndpoint)
	}

	tm.mu.Lock()
	state := tm.getOrCreateState(name)

	if mcpOK {
		state.consecutiveFailures = 0
		if state.uptimeStart.IsZero() {
			state.uptimeStart = time.Now()
		}
		tm.mu.Unlock()
		if restOK {
			tm.lm.SetStatus(name, models.StatusRunning, "")
		} else {
			tm.lm.SetStatus(name, models.StatusDegraded, "REST health check failed")
		}
		return
	}

	state.consecutiveFailures++
	failures := state.consecutiveFailures
	tm.mu.Unlock()

	if failures < tm.ConsecutiveFailureThreshold {
		if entry.Status == models.StatusRunning {
			tm.lm.SetStatus(name, models.StatusDegraded, "MCP ping failed")
		}
		return
	}

	tm.attemptRestart(ctx, name)
}

func TestHealthy_RunningServer(t *testing.T) {
	mock := newMockLM()
	mock.addEntry("s1", models.StatusRunning, models.ServerConfig{Command: "echo"})

	tm := newTestableMonitor(mock)
	ctx := context.Background()

	entry, _ := mock.entries["s1"]
	tm.checkOneWithMockPing(ctx, entry, true)

	assert.Equal(t, models.StatusRunning, mock.lastStatus("s1"))
}

func TestHealthy_WithRESTHealth(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	mock := newMockLM()
	mock.addEntry("s1", models.StatusRunning, models.ServerConfig{
		Command:        "echo",
		RestURL:        ts.URL,
		HealthEndpoint: "/health",
	})

	tm := newTestableMonitor(mock)
	ctx := context.Background()

	entry := mock.entries["s1"]
	tm.checkOneWithMockPing(ctx, entry, true)

	assert.Equal(t, models.StatusRunning, mock.lastStatus("s1"))
}

func TestDegraded_MCPOKButRESTFail(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	mock := newMockLM()
	mock.addEntry("s1", models.StatusRunning, models.ServerConfig{
		Command:        "echo",
		RestURL:        ts.URL,
		HealthEndpoint: "/health",
	})

	tm := newTestableMonitor(mock)
	ctx := context.Background()

	entry := mock.entries["s1"]
	tm.checkOneWithMockPing(ctx, entry, true)

	assert.Equal(t, models.StatusDegraded, mock.lastStatus("s1"))
}

func TestDegraded_SinglePingFailure(t *testing.T) {
	mock := newMockLM()
	mock.addEntry("s1", models.StatusRunning, models.ServerConfig{Command: "echo"})

	tm := newTestableMonitor(mock)
	ctx := context.Background()

	// First failure — should degrade.
	entry := mock.entries["s1"]
	tm.checkOneWithMockPing(ctx, entry, false)

	assert.Equal(t, models.StatusDegraded, mock.lastStatus("s1"))
	assert.Equal(t, 0, mock.getRestarts(), "should not restart after 1 failure")
}

func TestRestart_AfterConsecutiveFailures(t *testing.T) {
	mock := newMockLM()
	mock.addEntry("s1", models.StatusRunning, models.ServerConfig{Command: "echo"})

	tm := newTestableMonitor(mock)
	ctx := context.Background()

	// 3 consecutive failures → should trigger restart.
	for i := range 3 {
		entry := mock.entries["s1"]
		tm.checkOneWithMockPing(ctx, entry, false)
		if i < 2 {
			assert.Equal(t, 0, mock.getRestarts(), "no restart after %d failures", i+1)
		}
	}

	assert.Equal(t, 1, mock.getRestarts(), "should restart after 3 consecutive failures")
}

func TestRecovery_AfterDegraded(t *testing.T) {
	mock := newMockLM()
	mock.addEntry("s1", models.StatusRunning, models.ServerConfig{Command: "echo"})

	tm := newTestableMonitor(mock)
	ctx := context.Background()

	// 2 failures → degraded.
	for range 2 {
		entry := mock.entries["s1"]
		tm.checkOneWithMockPing(ctx, entry, false)
	}
	assert.Equal(t, models.StatusDegraded, mock.lastStatus("s1"))

	// 1 success → running.
	entry := mock.entries["s1"]
	tm.checkOneWithMockPing(ctx, entry, true)
	assert.Equal(t, models.StatusRunning, mock.lastStatus("s1"))
	assert.Equal(t, 0, mock.getRestarts())
}

func TestCircuitBreaker_Opens(t *testing.T) {
	mock := newMockLM()
	mock.addEntry("s1", models.StatusRunning, models.ServerConfig{Command: "echo"})

	tm := newTestableMonitor(mock)
	tm.CircuitBreakerThreshold = 3
	ctx := context.Background()

	// Trigger 3 restart cycles (each needs 3 consecutive failures).
	for cycle := range 3 {
		for range 3 {
			entry := mock.entries["s1"]
			tm.checkOneWithMockPing(ctx, entry, false)
		}
		if cycle < 2 {
			// After restart, mock sets status to Running.
			mock.mu.Lock()
			e := mock.entries["s1"]
			e.Status = models.StatusRunning
			mock.entries["s1"] = e
			mock.mu.Unlock()
			// Reset consecutive failures for next cycle.
			tm.mu.Lock()
			tm.states["s1"].consecutiveFailures = 0
			tm.mu.Unlock()
		}
	}

	// After 3 restarts, circuit should be open → Disabled.
	assert.Equal(t, models.StatusDisabled, mock.lastStatus("s1"))
}

func TestResetCircuit(t *testing.T) {
	mock := newMockLM()
	mock.addEntry("s1", models.StatusDisabled, models.ServerConfig{Command: "echo"})

	tm := newTestableMonitor(mock)
	ctx := context.Background()

	// Manually set circuit breaker state.
	tm.mu.Lock()
	tm.states["s1"] = &serverState{
		restartCount:   10,
		firstRestartAt: time.Now(),
	}
	tm.mu.Unlock()

	err := tm.ResetCircuit(ctx, "s1")
	require.NoError(t, err)

	// Circuit should be reset.
	tm.mu.Lock()
	state := tm.states["s1"]
	assert.Equal(t, 0, state.restartCount)
	assert.Equal(t, 0, state.consecutiveFailures)
	tm.mu.Unlock()

	mock.mu.Lock()
	assert.Equal(t, 1, mock.starts)
	mock.mu.Unlock()
}

func TestRestartFails_SetsError(t *testing.T) {
	mock := newMockLM()
	mock.addEntry("s1", models.StatusRunning, models.ServerConfig{Command: "echo"})
	mock.restartErr = fmt.Errorf("restart failed")

	tm := newTestableMonitor(mock)
	ctx := context.Background()

	// 3 failures → restart attempt → fails.
	for range 3 {
		entry := mock.entries["s1"]
		tm.checkOneWithMockPing(ctx, entry, false)
	}

	assert.Equal(t, models.StatusError, mock.lastStatus("s1"))
}

func TestAllSevenStates(t *testing.T) {
	// Verify all 7 states are reachable via the health monitor.
	states := map[models.ServerStatus]bool{
		models.StatusStopped:    false,
		models.StatusStarting:   false,
		models.StatusRunning:    false,
		models.StatusDegraded:   false,
		models.StatusError:      false,
		models.StatusRestarting: false,
		models.StatusDisabled:   false,
	}

	mock := newMockLM()
	mock.addEntry("s1", models.StatusRunning, models.ServerConfig{Command: "echo"})

	tm := newTestableMonitor(mock)
	tm.CircuitBreakerThreshold = 2
	ctx := context.Background()

	// Running → Degraded (1 ping failure).
	entry := mock.entries["s1"]
	tm.checkOneWithMockPing(ctx, entry, false)

	// Degraded → Running (recovery).
	entry = mock.entries["s1"]
	tm.checkOneWithMockPing(ctx, entry, true)

	// Running → ... 3 failures → Restarting → Running (auto-restart).
	for range 3 {
		entry = mock.entries["s1"]
		tm.checkOneWithMockPing(ctx, entry, false)
	}

	// Reset for second restart cycle.
	mock.mu.Lock()
	e := mock.entries["s1"]
	e.Status = models.StatusRunning
	mock.entries["s1"] = e
	mock.mu.Unlock()
	tm.mu.Lock()
	tm.states["s1"].consecutiveFailures = 0
	tm.mu.Unlock()

	// 3 more failures → circuit breaker → Disabled.
	for range 3 {
		entry = mock.entries["s1"]
		tm.checkOneWithMockPing(ctx, entry, false)
	}

	// ResetCircuit → Stopped → Start (lifecycle sets Starting internally) → Running.
	_ = tm.ResetCircuit(ctx, "s1")
	// "starting" is set by the lifecycle manager's Start() method, not by health monitor.
	// Simulate it here for completeness.
	mock.SetStatus("s1", models.StatusStarting, "")

	// Error state (restart failure).
	mock.restartErr = fmt.Errorf("boom")
	mock.mu.Lock()
	e = mock.entries["s1"]
	e.Status = models.StatusRunning
	mock.entries["s1"] = e
	mock.mu.Unlock()
	tm.mu.Lock()
	tm.states["s1"].consecutiveFailures = 0
	tm.states["s1"].restartCount = 0
	tm.mu.Unlock()
	for range 3 {
		entry = mock.entries["s1"]
		tm.checkOneWithMockPing(ctx, entry, false)
	}

	// Collect all statuses from the log.
	mock.mu.Lock()
	for _, ev := range mock.statusLog {
		if ev.Name == "s1" {
			states[ev.Status] = true
		}
	}
	mock.mu.Unlock()

	for status, seen := range states {
		assert.True(t, seen, "state %q was never reached", status)
	}
}

// --- Phase 10.4: Metrics tests ---

func TestAllServerMetrics_ZeroCrash(t *testing.T) {
	mock := newMockLM()
	mock.addEntry("s1", models.StatusRunning, models.ServerConfig{Command: "echo"})

	tm := newTestableMonitor(mock)
	ctx := context.Background()

	// One successful health check — sets uptimeStart.
	entry := mock.entries["s1"]
	tm.checkOneWithMockPing(ctx, entry, true)
	time.Sleep(2 * time.Millisecond) // ensure clock advances (Windows ~15ms resolution)

	entries := mock.Entries()
	metrics := tm.AllServerMetrics(entries)
	require.Len(t, metrics, 1)

	m := metrics[0]
	assert.Equal(t, "s1", m.Name)
	assert.Equal(t, 0, m.RestartCount)
	assert.Equal(t, models.Duration(0), m.MTBF) // no failures
	assert.True(t, time.Duration(m.Uptime) >= 0, "uptime should be non-negative")
	assert.Nil(t, m.LastCrashAt)
}

func TestAllServerMetrics_WithCrashes(t *testing.T) {
	mock := newMockLM()
	mock.addEntry("s1", models.StatusRunning, models.ServerConfig{Command: "echo"})

	tm := newTestableMonitor(mock)
	tm.ConsecutiveFailureThreshold = 1 // crash after 1 failure
	ctx := context.Background()

	// First successful check — sets uptimeStart.
	entry := mock.entries["s1"]
	tm.checkOneWithMockPing(ctx, entry, true)

	// Fail → triggers restart (crash).
	entry = mock.entries["s1"]
	tm.checkOneWithMockPing(ctx, entry, false)

	// Recover.
	mock.mu.Lock()
	e := mock.entries["s1"]
	e.Status = models.StatusRunning
	mock.entries["s1"] = e
	mock.mu.Unlock()
	entry = mock.entries["s1"]
	tm.checkOneWithMockPing(ctx, entry, true)

	time.Sleep(2 * time.Millisecond) // ensure clock advances
	entries := mock.Entries()
	metrics := tm.AllServerMetrics(entries)
	require.Len(t, metrics, 1)

	m := metrics[0]
	assert.Equal(t, 1, m.RestartCount)
	assert.NotNil(t, m.LastCrashAt)
	assert.True(t, time.Duration(m.MTBF) >= 0, "MTBF should be non-negative after crash")
	assert.True(t, time.Duration(m.Uptime) >= 0)
}

func TestAllServerMetrics_NoHealthCheckYet(t *testing.T) {
	mock := newMockLM()
	mock.addEntry("s1", models.StatusRunning, models.ServerConfig{Command: "echo"})

	tm := newTestableMonitor(mock)
	// No health checks run — server has no state entry.

	entries := mock.Entries()
	metrics := tm.AllServerMetrics(entries)
	require.Len(t, metrics, 1)

	m := metrics[0]
	assert.Equal(t, "s1", m.Name)
	assert.Equal(t, 0, m.RestartCount)
	assert.Equal(t, models.Duration(0), m.MTBF)
	assert.Equal(t, models.Duration(0), m.Uptime)
	assert.Nil(t, m.LastCrashAt)
}

func TestAllServerMetrics_MultiCrashMTBF(t *testing.T) {
	mock := newMockLM()
	mock.addEntry("s1", models.StatusRunning, models.ServerConfig{Command: "echo"})

	tm := newTestableMonitor(mock)
	tm.ConsecutiveFailureThreshold = 1
	ctx := context.Background()

	// Simulate 3 crashes.
	for range 3 {
		entry := mock.entries["s1"]
		tm.checkOneWithMockPing(ctx, entry, true) // recover/start
		entry = mock.entries["s1"]
		tm.checkOneWithMockPing(ctx, entry, false) // crash
		mock.mu.Lock()
		e := mock.entries["s1"]
		e.Status = models.StatusRunning
		mock.entries["s1"] = e
		mock.mu.Unlock()
	}
	// Final recovery.
	entry := mock.entries["s1"]
	tm.checkOneWithMockPing(ctx, entry, true)

	time.Sleep(2 * time.Millisecond) // ensure clock advances
	entries := mock.Entries()
	metrics := tm.AllServerMetrics(entries)
	require.Len(t, metrics, 1)
	assert.Equal(t, 3, metrics[0].RestartCount)
	assert.True(t, time.Duration(metrics[0].MTBF) >= 0)
}

func TestGatewayUptime(t *testing.T) {
	mock := newMockLM()
	tm := newTestableMonitor(mock)
	time.Sleep(5 * time.Millisecond)
	assert.True(t, tm.GatewayUptime() >= 5*time.Millisecond)
}

func TestStartedAt_BeforeNowAndStable(t *testing.T) {
	before := time.Now()
	tm := newTestableMonitor(newMockLM())

	sa := tm.StartedAt()
	assert.True(t, sa.Before(time.Now()) || sa.Equal(time.Now()),
		"StartedAt() should be at or before now")
	assert.False(t, sa.Before(before),
		"StartedAt() should be at or after the moment before construction")

	// Calling again must return the same value (write-once field).
	assert.Equal(t, sa, tm.StartedAt(), "StartedAt() must be stable across calls")
}

func TestAllServerMetrics_ResetCircuitClearsMetrics(t *testing.T) {
	mock := newMockLM()
	mock.addEntry("s1", models.StatusRunning, models.ServerConfig{Command: "echo"})

	tm := newTestableMonitor(mock)
	tm.ConsecutiveFailureThreshold = 1
	ctx := context.Background()

	// Crash once.
	entry := mock.entries["s1"]
	tm.checkOneWithMockPing(ctx, entry, true)
	entry = mock.entries["s1"]
	tm.checkOneWithMockPing(ctx, entry, false)

	// Verify crash was recorded.
	entries := mock.Entries()
	metrics := tm.AllServerMetrics(entries)
	require.Equal(t, 1, metrics[0].RestartCount)

	// Reset circuit — should clear crash count, uptime, and crash timestamp.
	_ = tm.ResetCircuit(ctx, "s1")
	entries = mock.Entries()
	metrics = tm.AllServerMetrics(entries)
	assert.Equal(t, 0, metrics[0].RestartCount)
	assert.Equal(t, models.Duration(0), metrics[0].Uptime)
	assert.Nil(t, metrics[0].LastCrashAt)
	assert.Equal(t, models.Duration(0), metrics[0].MTBF)
}
