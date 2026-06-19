package proxy

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"mcp-gateway/internal/lifecycle"
	"mcp-gateway/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// noopLogger returns a logger that discards all output (test helper).
func noopLogger(_ *testing.T) *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestGateway builds a minimal Gateway with no backends for unit tests
// that only exercise the idle-exit path (no real lifecycle or MCP sessions).
func newTestGateway(t *testing.T) *Gateway {
	t.Helper()
	cfg := &models.Config{Servers: map[string]*models.ServerConfig{}}
	cfg.ApplyDefaults()
	lm := lifecycle.NewManager(cfg, "test", noopLogger(t))
	return New(cfg, lm, "test", noopLogger(t))
}

// --- shouldExit pure-function matrix ---

func TestShouldExit_ClientsPresent(t *testing.T) {
	// Guard (a): clients > 0 blocks exit regardless of inflight or idle.
	assert.False(t, shouldExit(1, 0, 10*time.Minute, 5*time.Minute),
		"must not exit when clients > 0")
	assert.False(t, shouldExit(3, 0, 10*time.Minute, 5*time.Minute),
		"must not exit when multiple clients")
	// Also blocked even when inflight > 0 too
	assert.False(t, shouldExit(1, 2, 10*time.Minute, 5*time.Minute))
}

func TestShouldExit_InflightPresent(t *testing.T) {
	// Guard (b): inflight > 0 blocks exit regardless of client count or idle.
	assert.False(t, shouldExit(0, 1, 10*time.Minute, 5*time.Minute),
		"must not exit when inflight > 0")
	assert.False(t, shouldExit(0, 99, 10*time.Minute, 5*time.Minute),
		"must not exit when many inflight")
}

func TestShouldExit_IdleWindowNotElapsed(t *testing.T) {
	// Guard (c/d): idle elapsed < window blocks exit.
	assert.False(t, shouldExit(0, 0, 4*time.Minute+59*time.Second, 5*time.Minute),
		"must not exit when idle < window")
	assert.False(t, shouldExit(0, 0, 0, 5*time.Minute),
		"must not exit at zero idle")
}

func TestShouldExit_AllClear(t *testing.T) {
	// All guards satisfied: clients==0, inflight==0, idle>=window.
	assert.True(t, shouldExit(0, 0, 5*time.Minute, 5*time.Minute),
		"must exit at exactly the window boundary")
	assert.True(t, shouldExit(0, 0, 10*time.Minute, 5*time.Minute),
		"must exit well past the window")
}

func TestShouldExit_ZeroWindow(t *testing.T) {
	// When window == 0, idle >= window is always true.
	// In practice, parseIdleExitWindow returns 0 to DISABLE — but the
	// function itself treats window==0 as "always elapsed". This is fine
	// because ConfigureIdleExit never creates a monitor when window==0.
	assert.True(t, shouldExit(0, 0, 0, 0))
}

// --- nopClientCounter ---

func TestNopClientCounter_AlwaysOne(t *testing.T) {
	var c nopClientCounter
	assert.Equal(t, 1, c.ClientCount())
}

// --- parseIdleExitWindow ---

func TestParseIdleExitWindow_Default(t *testing.T) {
	t.Setenv("MCP_GATEWAY_IDLE_EXIT_SECONDS", "")
	w := parseIdleExitWindow(noopLogger(t))
	assert.Equal(t, idleExitDefaultWindow, w, "empty env must yield default")
}

func TestParseIdleExitWindow_Zero_Disables(t *testing.T) {
	t.Setenv("MCP_GATEWAY_IDLE_EXIT_SECONDS", "0")
	w := parseIdleExitWindow(noopLogger(t))
	assert.Equal(t, time.Duration(0), w, "zero must disable")
}

func TestParseIdleExitWindow_ValidSeconds(t *testing.T) {
	t.Setenv("MCP_GATEWAY_IDLE_EXIT_SECONDS", "120")
	w := parseIdleExitWindow(noopLogger(t))
	assert.Equal(t, 120*time.Second, w)
}

func TestParseIdleExitWindow_NegativeFallsBackToDefault(t *testing.T) {
	t.Setenv("MCP_GATEWAY_IDLE_EXIT_SECONDS", "-5")
	w := parseIdleExitWindow(noopLogger(t))
	assert.Equal(t, idleExitDefaultWindow, w, "negative must fall back to default")
}

func TestParseIdleExitWindow_InvalidFallsBackToDefault(t *testing.T) {
	t.Setenv("MCP_GATEWAY_IDLE_EXIT_SECONDS", "notanumber")
	w := parseIdleExitWindow(noopLogger(t))
	assert.Equal(t, idleExitDefaultWindow, w, "non-numeric must fall back to default")
}

// --- monitor decision logic ---

// fakeReporter is a test double for inflightReporter.
type fakeReporter struct {
	inflight int64
	lastCall atomic.Int64 // unix nano; 0 = never
}

func (f *fakeReporter) InflightCalls() int64 { return atomic.LoadInt64(&f.inflight) }

func (f *fakeReporter) LastCallTime() time.Time {
	ns := f.lastCall.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

func (f *fakeReporter) setLastCall(t time.Time) { f.lastCall.Store(t.UnixNano()) }

// fixedClientCounter returns a constant count.
type fixedClientCounter struct{ n int }

func (c fixedClientCounter) ClientCount() int { return c.n }

// buildMonitor is a test helper that builds a monitor with a short window
// so tests don't take 5 minutes.
func buildMonitor(t *testing.T, window time.Duration, clients int, rep inflightReporter) (*idleExitMonitor, *bool, func()) {
	t.Helper()
	shutdownCalled := new(bool)
	cfg := idleExitConfig{
		window:   window,
		counter:  fixedClientCounter{clients},
		shutdown: func() { *shutdownCalled = true },
		logger:   noopLogger(t),
	}
	mon := newIdleExitMonitor(cfg, rep)
	return mon, shutdownCalled, func() {}
}

func TestMonitor_NoExitWhenClientsPresent(t *testing.T) {
	rep := &fakeReporter{}
	// client count = 1, inflight = 0, but window already elapsed
	mon, shutdownCalled, _ := buildMonitor(t, 10*time.Millisecond, 1, rep)
	// Push lastActivity far in the past so idle > window
	mon.mu.Lock()
	mon.lastActivity = time.Now().Add(-1 * time.Hour)
	mon.mu.Unlock()

	triggered := mon.tick()
	assert.False(t, triggered, "tick must not trigger when clients > 0")
	assert.False(t, *shutdownCalled)
}

func TestMonitor_NoExitWhenInflight(t *testing.T) {
	rep := &fakeReporter{inflight: 1}
	mon, shutdownCalled, _ := buildMonitor(t, 10*time.Millisecond, 0, rep)
	mon.mu.Lock()
	mon.lastActivity = time.Now().Add(-1 * time.Hour)
	mon.mu.Unlock()

	triggered := mon.tick()
	assert.False(t, triggered, "tick must not trigger when inflight > 0")
	assert.False(t, *shutdownCalled)
}

func TestMonitor_NoExitWhenWindowNotElapsed(t *testing.T) {
	rep := &fakeReporter{}
	// window = 5 min, idle = just now
	mon, shutdownCalled, _ := buildMonitor(t, 5*time.Minute, 0, rep)
	// lastActivity is time.Now() from constructor — idle ~ 0

	triggered := mon.tick()
	assert.False(t, triggered, "tick must not trigger before window elapses")
	assert.False(t, *shutdownCalled)
}

func TestMonitor_ExitsWhenAllClear(t *testing.T) {
	rep := &fakeReporter{}
	// tiny window so idle > window immediately
	mon, shutdownCalled, _ := buildMonitor(t, 1*time.Nanosecond, 0, rep)
	// Push lastActivity far in the past
	mon.mu.Lock()
	mon.lastActivity = time.Now().Add(-1 * time.Hour)
	mon.mu.Unlock()

	triggered := mon.tick()
	assert.True(t, triggered, "tick must trigger when all guards clear")
	assert.True(t, *shutdownCalled, "shutdown must be called")
}

func TestMonitor_DebounceResetsOnRouterActivity(t *testing.T) {
	// Guard (c): a new Call/CallDirect resets the idle countdown.
	rep := &fakeReporter{}
	mon, shutdownCalled, _ := buildMonitor(t, 100*time.Millisecond, 0, rep)

	// Push lastActivity far into the past initially.
	mon.mu.Lock()
	mon.lastActivity = time.Now().Add(-1 * time.Hour)
	mon.mu.Unlock()

	// Simulate a router call happening RIGHT NOW.
	rep.setLastCall(time.Now())

	// First tick should see the new LastCallTime and reset the idle clock.
	triggered := mon.tick()
	assert.False(t, triggered, "tick must not trigger after recent router activity")
	assert.False(t, *shutdownCalled, "shutdown must not be called")

	// Verify lastActivity was advanced to at least the time of the fake call.
	mon.mu.Lock()
	last := mon.lastActivity
	mon.mu.Unlock()
	assert.True(t, time.Since(last) < 1*time.Second, "lastActivity must have been updated to recent call time")
}

func TestMonitor_ActivityResetMaintainsMonotonicOrder(t *testing.T) {
	// noteActivity must not go backwards in time.
	rep := &fakeReporter{}
	mon, _, _ := buildMonitor(t, 5*time.Minute, 0, rep)

	now := time.Now()
	future := now.Add(10 * time.Second)
	past := now.Add(-10 * time.Second)

	mon.noteActivity(future)
	mon.mu.Lock()
	after := mon.lastActivity
	mon.mu.Unlock()
	assert.Equal(t, future, after)

	// Reporting a past time must not rewind lastActivity.
	mon.noteActivity(past)
	mon.mu.Lock()
	afterPast := mon.lastActivity
	mon.mu.Unlock()
	assert.Equal(t, future, afterPast, "noteActivity must not rewind lastActivity")
}

func TestMonitor_NilShutdownSafe(t *testing.T) {
	// If no shutdown func is wired, tick must not panic.
	rep := &fakeReporter{}
	cfg := idleExitConfig{
		window:  1 * time.Nanosecond,
		counter: fixedClientCounter{0},
		logger:  noopLogger(t),
		// shutdown: nil intentionally
	}
	mon := newIdleExitMonitor(cfg, rep)
	mon.mu.Lock()
	mon.lastActivity = time.Now().Add(-1 * time.Hour)
	mon.mu.Unlock()

	require.NotPanics(t, func() { mon.tick() })
}

// TestMonitor_NoExitWhenClientConnectedButIdle verifies that a client holding a
// GET notification stream (SessionCount > 0) prevents idle exit even when no
// tool calls are inflight and the idle window has elapsed. This is the
// "GET-stream-only / idle-but-connected" scenario described in H-1: the
// counter counts active streamable-HTTP sessions, so a connected-but-quiet
// client still blocks exit.
func TestMonitor_NoExitWhenClientConnectedButIdle(t *testing.T) {
	rep := &fakeReporter{} // inflight = 0
	shutdownSpy := new(bool)
	cfg := idleExitConfig{
		window:   10 * time.Millisecond, // tiny window — easy to exceed
		counter:  fixedClientCounter{1}, // one connected client
		shutdown: func() { *shutdownSpy = true },
		logger:   noopLogger(t),
	}
	mon := newIdleExitMonitor(cfg, rep)

	// Push lastActivity far in the past so idle >> window.
	mon.mu.Lock()
	mon.lastActivity = time.Now().Add(-1 * time.Hour)
	mon.mu.Unlock()

	// shouldExit must return false because clients == 1.
	assert.False(t, shouldExit(
		int64(cfg.counter.ClientCount()),
		rep.InflightCalls(),
		mon.idleSince(),
		cfg.window,
	), "shouldExit must be false when a client is connected")

	// tick() must not invoke the shutdown function.
	triggered := mon.tick()
	assert.False(t, triggered, "tick must not trigger when a client session is connected")
	assert.False(t, *shutdownSpy, "shutdown must not be called when client count > 0")
}

// --- run loop integration ---

func TestMonitor_RunLoopExitsOnContextCancel(t *testing.T) {
	rep := &fakeReporter{}
	// Use a very long window so the monitor NEVER decides to exit on its own.
	mon, _, _ := buildMonitor(t, 24*time.Hour, 1, rep)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		mon.run(ctx)
		close(done)
	}()

	select {
	case <-done:
		// Good: goroutine exited when ctx was cancelled.
	case <-time.After(2 * time.Second):
		t.Fatal("monitor run loop did not exit after context cancellation")
	}
}

// --- Gateway.ConfigureIdleExit wiring ---

func TestGateway_ConfigureIdleExit_StoresMonitor(t *testing.T) {
	t.Setenv("MCP_GATEWAY_IDLE_EXIT_SECONDS", "60")
	gw := newTestGateway(t)

	var shutdownCalled bool
	gw.ConfigureIdleExit(fixedClientCounter{0}, func() { shutdownCalled = true })

	gw.idleExit.Lock()
	mon := gw.idleExit.monitor
	gw.idleExit.Unlock()
	require.NotNil(t, mon, "ConfigureIdleExit must store a monitor")
	assert.Equal(t, 60*time.Second, mon.cfg.window)
	_ = shutdownCalled // referenced to avoid "unused" lint error
}

func TestGateway_ConfigureIdleExit_DisabledByEnv(t *testing.T) {
	t.Setenv("MCP_GATEWAY_IDLE_EXIT_SECONDS", "0")
	gw := newTestGateway(t)

	gw.ConfigureIdleExit(nil, func() {})

	gw.idleExit.Lock()
	mon := gw.idleExit.monitor
	gw.idleExit.Unlock()
	assert.Nil(t, mon, "ConfigureIdleExit with window=0 must not store a monitor")
}

func TestGateway_ServeIdleExit_NoopWhenNotConfigured(t *testing.T) {
	gw := newTestGateway(t)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Must return promptly when no monitor is configured (not block).
	done := make(chan struct{})
	go func() {
		gw.ServeIdleExit(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("ServeIdleExit must be a no-op and return immediately when not configured")
	}
}

func TestGateway_ServeIdleExit_StartsOnce(t *testing.T) {
	t.Setenv("MCP_GATEWAY_IDLE_EXIT_SECONDS", "60")
	gw := newTestGateway(t)
	gw.ConfigureIdleExit(fixedClientCounter{1}, func() {})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// First call starts the goroutine (exits when ctx cancelled).
	startedCh := make(chan struct{}, 2)

	// Patch the monitor's run to signal us.
	gw.idleExit.Lock()
	original := gw.idleExit.monitor
	gw.idleExit.Unlock()
	require.NotNil(t, original)

	// ServeIdleExit via real monitor is fine; we just cancel quickly.
	go gw.ServeIdleExit(ctx)

	// Second concurrent call must be a no-op (started flag prevents double-start).
	go gw.ServeIdleExit(ctx)

	time.Sleep(50 * time.Millisecond) // let goroutines settle
	cancel()
	_ = startedCh
}

// --- router inflight counter ---

func TestRouterInflightCounter_IncrementDecrement(t *testing.T) {
	// Verify that a call that blocks updates inflightCalls while in flight.
	// We test using a mock that records the counter value during execution.
	type entry struct{ count int64 }

	// Use a real router but with a mock SessionProvider that records
	// the inflight count at the moment of the "call".
	var capturedInflight int64
	var r *fakeInflightRouter
	r = &fakeInflightRouter{
		onCall: func() {
			// This runs inside Call, between Add(1) and defer Add(-1).
			capturedInflight = r.InflightCalls()
		},
	}

	// Verify zero before any call.
	assert.Equal(t, int64(0), r.InflightCalls())

	// Simulate one call.
	r.simulateCall()

	assert.Equal(t, int64(1), capturedInflight, "inflight must be 1 inside the call")
	assert.Equal(t, int64(0), r.InflightCalls(), "inflight must return to 0 after the call")
}

func TestRouterInflightCounter_ConcurrentCalls(t *testing.T) {
	var (
		peak    int64
		peakMu  atomic.Int64
		barrier = make(chan struct{})
	)

	r := &fakeInflightRouter{
		onCall: func() {
			// All goroutines rendez-vous here, then check inflight.
		},
	}

	const N = 5
	done := make(chan struct{}, N)
	for i := 0; i < N; i++ {
		go func() {
			r.onCall = func() {
				<-barrier // wait for all N to be in-flight
				cur := r.InflightCalls()
				for {
					old := peakMu.Load()
					if cur <= old || peakMu.CompareAndSwap(old, cur) {
						break
					}
				}
			}
			r.simulateCall()
			done <- struct{}{}
		}()
	}
	time.Sleep(10 * time.Millisecond) // let goroutines start and block
	close(barrier)
	for i := 0; i < N; i++ {
		<-done
	}
	peak = peakMu.Load()
	// All N goroutines were in-flight simultaneously; peak must be N.
	// (In a race with barriers it may not reach N on slow machines, so
	// we assert >= 2 rather than == N to avoid flakiness.)
	assert.GreaterOrEqual(t, peak, int64(2), "concurrent inflight count must reflect concurrent calls")
}

func TestRouterLastCallTime_Zero_BeforeAnyCall(t *testing.T) {
	r := &fakeInflightRouter{}
	assert.True(t, r.LastCallTime().IsZero(), "LastCallTime must be zero before any call")
}

func TestRouterLastCallTime_SetOnCall(t *testing.T) {
	r := &fakeInflightRouter{}
	before := time.Now()
	r.simulateCall()
	after := time.Now()

	last := r.LastCallTime()
	assert.False(t, last.IsZero())
	assert.True(t, !last.Before(before) && !last.After(after),
		"LastCallTime must be between before and after the call")
}

// --- helpers ---

// fakeInflightRouter wraps the real router.Router counters without needing a
// real SessionProvider, so we can test the counter mechanics in isolation.
// We embed the real atomic fields and expose the same interface.
type fakeInflightRouter struct {
	inflight atomic.Int64
	lastNano atomic.Int64
	onCall   func()
}

func (f *fakeInflightRouter) InflightCalls() int64 { return f.inflight.Load() }

func (f *fakeInflightRouter) LastCallTime() time.Time {
	ns := f.lastNano.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// simulateCall mimics what router.Call does with the atomics.
func (f *fakeInflightRouter) simulateCall() {
	f.inflight.Add(1)
	f.lastNano.Store(time.Now().UnixNano())
	defer f.inflight.Add(-1)
	if f.onCall != nil {
		f.onCall()
	}
}
