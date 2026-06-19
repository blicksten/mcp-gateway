package health

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"mcp-gateway/internal/models"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// P0 — "gateway crash-stop" fault-injection + regression tests.
// Reliability invariant §2.6: each failure class P0 touches ships a
// fault-injection test proving the heart (monitor loop) survives.

// TestCircuitOpensOnSpawnSucceedsUnreachable is the core P0 bug: a backend
// whose process spawns OK (Restart() returns nil) but never becomes
// reachable (MCP ping always fails) must trip the circuit within a bounded
// number of cycles instead of restart-storming forever. Drives the sticky
// gate (lastHealthyAt never advances past firstRestartAt).
func TestCircuitOpensOnSpawnSucceedsUnreachable(t *testing.T) {
	mock := newMockLM()
	mock.addEntry("s1", models.StatusRunning, models.ServerConfig{Command: "echo"})

	tm := newTestableMonitor(mock)
	tm.CircuitBreakerThreshold = 3 // open after 3 restarts
	// RestartBackoffBase=0 (newTestableMonitor default) so the storm is
	// driven synchronously. Restart() returns nil (spawn succeeds) but the
	// mock Session() returns no session → checkMCPPing always fails.
	ctx := context.Background()

	disabled := false
	for i := 0; i < 60 && !disabled; i++ {
		entry := mock.getEntry("s1")
		tm.checkOneWithMockPing(ctx, entry, false)
		if mock.lastStatus("s1") == models.StatusDisabled {
			disabled = true
		}
	}

	assert.True(t, disabled,
		"circuit MUST open for a spawn-OK-but-unreachable backend (P0 bug)")
	assert.LessOrEqual(t, mock.getRestarts(), 3,
		"circuit should open within threshold restarts, not storm")
}

// TestStickyCircuit_RecoveredServerNotFalselyDisabled is the CRIT-1
// regression: a backend that genuinely recovered (lastHealthyAt advanced
// past firstRestartAt) and then re-enters a restart loop AFTER its window
// elapses must NOT be permanently disabled — the CR-15 window reset must
// also zero lastHealthyAt so the sticky gate is evaluated against the new
// window. Without the CRIT-1 fix this server is falsely disabled.
func TestStickyCircuit_RecoveredServerNotFalselyDisabled(t *testing.T) {
	mock := newMockLM()
	mock.addEntry("s1", models.StatusRunning, models.ServerConfig{Command: "echo"})

	tm := newTestableMonitor(mock)
	tm.CircuitBreakerThreshold = 3
	tm.CircuitBreakerWindow = 20 * time.Millisecond
	ctx := context.Background()

	// Restart #1 → firstRestartAt set (= t0).
	for range 3 {
		entry := mock.getEntry("s1")
		tm.checkOneWithMockPing(ctx, entry, false)
	}
	// Separate the recovery timestamp from firstRestartAt by more than the
	// Windows timer granularity (~15ms) so lastHealthyAt is *strictly*
	// after firstRestartAt. In production this gap is always seconds (one
	// health-check interval) — the immediate-recovery case only arises in
	// this synthetic test, so the separation is made explicit here rather
	// than weakening the production .After() comparison.
	time.Sleep(25 * time.Millisecond)
	// Genuine recovery → lastHealthyAt set AFTER firstRestartAt.
	mock.mu.Lock()
	e := mock.entries["s1"]
	e.Status = models.StatusRunning
	mock.entries["s1"] = e
	mock.mu.Unlock()
	entry := mock.getEntry("s1")
	tm.checkOneWithMockPing(ctx, entry, true)
	require.Equal(t, models.StatusRunning, mock.lastStatus("s1"))

	// Let the circuit-breaker window (20ms, measured from t0) elapse.
	time.Sleep(30 * time.Millisecond)

	// Re-enter a restart loop. restartCount climbs to threshold while
	// lastHealthyAt.After(firstRestartAt) is still true → CR-15 window
	// path → window elapsed → reset (NOT disabled), lastHealthyAt zeroed.
	for range 6 {
		entry = mock.getEntry("s1")
		tm.checkOneWithMockPing(ctx, entry, false)
	}

	assert.NotEqual(t, models.StatusDisabled, mock.lastStatus("s1"),
		"a recovered server must not be falsely disabled after window reset (CRIT-1)")
	tm.mu.Lock()
	st := tm.states["s1"]
	assert.Equal(t, 1, st.restartCount, "window reset should restart the count at 1")
	assert.True(t, st.lastHealthyAt.IsZero(),
		"CRIT-1: lastHealthyAt must be zeroed on window reset")
	tm.mu.Unlock()
}

// TestRestartBackoff_SpacingHonoredAndNotBypassed verifies T0.3 + HIGH-1:
// once a restart schedules a backoff, subsequent attempts within the
// cooldown are skipped (no restart-count growth), and the backoff is NOT
// bypassed even though many failing health checks arrive in the window.
func TestRestartBackoff_SpacingHonoredAndNotBypassed(t *testing.T) {
	mock := newMockLM()
	mock.addEntry("s1", models.StatusRunning, models.ServerConfig{Command: "echo"})

	tm := newTestableMonitor(mock)
	tm.CircuitBreakerThreshold = 100 // keep circuit out of the way
	tm.RestartBackoffBase = 40 * time.Millisecond
	tm.RestartBackoffMax = 1 * time.Second
	ctx := context.Background()

	// First restart.
	for range 3 {
		entry := mock.getEntry("s1")
		tm.checkOneWithMockPing(ctx, entry, false)
	}
	require.Equal(t, 1, mock.getRestarts(), "first restart should fire")

	// Hammer with failures during the backoff window — must NOT restart
	// again (HIGH-1: consecutiveFailures reset on the backoff early-return,
	// so it cannot accumulate straight back to threshold and bypass).
	for range 18 {
		entry := mock.getEntry("s1")
		tm.checkOneWithMockPing(ctx, entry, false)
	}
	assert.Equal(t, 1, mock.getRestarts(),
		"backoff MUST suppress restarts during the cooldown window")
	assert.Equal(t, models.StatusError, mock.lastStatus("s1"),
		"server should be in StatusError (backoff) during cooldown")

	// After the cooldown a restart is allowed again.
	time.Sleep(55 * time.Millisecond)
	for range 3 {
		entry := mock.getEntry("s1")
		tm.checkOneWithMockPing(ctx, entry, false)
	}
	assert.Equal(t, 2, mock.getRestarts(),
		"restart should resume once the backoff window elapses")
}

// panicLM is a LifecycleManager whose Session() panics, to verify the
// per-server health-check goroutine recovers (R2) and does not crash the
// monitor loop or deadlock checkAll's wg.Wait().
type panicLM struct {
	mu        sync.Mutex
	statusLog []statusEvent
}

func (p *panicLM) Entries() []models.ServerEntry {
	return []models.ServerEntry{
		{Name: "s1", Status: models.StatusRunning, Config: models.ServerConfig{Command: "x"}},
	}
}

func (p *panicLM) Session(string) (*mcp.ClientSession, bool) {
	panic("simulated health-check panic")
}

func (p *panicLM) SetStatus(name string, status models.ServerStatus, lastErr string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.statusLog = append(p.statusLog, statusEvent{name, status, lastErr})
}

func (p *panicLM) Restart(context.Context, string) error         { return nil }
func (p *panicLM) Start(context.Context, string) error            { return nil }
func (p *panicLM) SupervisorActive() bool                        { return false }
func (p *panicLM) AddBackendToSupervisor(_ string, _ *slog.Logger) {}

// TestPanicInHealthCheck_DoesNotKillMonitorLoop — R2 panic isolation.
func TestPanicInHealthCheck_DoesNotKillMonitorLoop(t *testing.T) {
	lm := &panicLM{}
	mon := NewMonitor(lm, time.Second, nil)
	ctx := context.Background()

	done := make(chan struct{})
	go func() {
		mon.CheckOnce(ctx) // must return despite the panic in Session()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("checkAll deadlocked after a panicking health check (wg.Wait hung)")
	}

	lm.mu.Lock()
	defer lm.mu.Unlock()
	require.NotEmpty(t, lm.statusLog, "panic recovery must record a status")
	last := lm.statusLog[len(lm.statusLog)-1]
	assert.Equal(t, models.StatusError, last.Status)
	assert.Contains(t, last.LastErr, "panic in health check")
}

// TestCheckAll_StatusErrorServerStillHealthChecked — LOW-2/MED-3(e): a
// server parked in StatusError during backoff must still be health-checked
// by checkAll so it can ever recover.
func TestCheckAll_StatusErrorServerStillHealthChecked(t *testing.T) {
	mock := newMockLM()
	mock.addEntry("s1", models.StatusError, models.ServerConfig{Command: "echo"})

	tm := newTestableMonitor(mock)
	ctx := context.Background()

	tm.CheckOnce(ctx) // runs checkAll over the StatusError entry

	tm.mu.Lock()
	st, ok := tm.states["s1"]
	tm.mu.Unlock()
	require.True(t, ok, "checkAll must create state for a StatusError server")
	assert.Equal(t, 1, st.consecutiveFailures,
		"a StatusError (backoff) server must still be health-checked by checkAll")
}
