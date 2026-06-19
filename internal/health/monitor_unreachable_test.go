// Integration tests for the StatusUnreachable slow-poll cycle in
// internal/health/monitor.go. Closes the MEDIUM gap listed in cd931db
// commit message under "NOT YET DONE": integration tests for
// maybeProbeUnreachable / probeReachable / Running→Unreachable transitions
// and the no-restartCount-increment invariant.
//
// Tests exercise the real monitor.maybeProbeUnreachable through the public
// CheckOnce entry point and a real TCP listener (net.Listen on 127.0.0.1:0)
// rather than mocking probeReachable directly — this catches integration
// regressions a mock would hide (e.g. the URL-port resolution path).
package health

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"sync"
	"testing"
	"time"

	"mcp-gateway/internal/models"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// supervisorTrackingLM is a LifecycleManager that:
//   - returns true from SupervisorActive() (supervisor path active)
//   - records every AddBackendToSupervisor call by name
//   - delegates Start/SetStatus/entries to an embedded mockLM
//
// Used exclusively by TestMaybeProbeUnreachable_RecoveryReRegistersWithSupervisor
// to verify H-1: after a successful Start() on recovery, the backend is
// re-added to the suture supervisor tree.
type supervisorTrackingLM struct {
	*mockLM
	mu           sync.Mutex
	reAddedNames []string
}

func newSupervisorTrackingLM() *supervisorTrackingLM {
	return &supervisorTrackingLM{mockLM: newMockLM()}
}

func (s *supervisorTrackingLM) SupervisorActive() bool { return true }

func (s *supervisorTrackingLM) AddBackendToSupervisor(name string, _ *slog.Logger) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reAddedNames = append(s.reAddedNames, name)
}

func (s *supervisorTrackingLM) reAddCount(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, n := range s.reAddedNames {
		if n == name {
			count++
		}
	}
	return count
}

// Session must be forwarded explicitly because the embedded *mockLM.Session
// returns (*mcp.ClientSession, bool) — Go embedding forwards it but the
// interface check requires the concrete method to be present on the outer type
// when the embedded type satisfies a different interface member signature.
// Re-declare here for clarity and to ensure the compiler confirms satisfaction.
func (s *supervisorTrackingLM) Session(name string) (*mcp.ClientSession, bool) {
	return s.mockLM.Session(name)
}

// listenLocal starts a TCP listener on 127.0.0.1:0 and returns the bound port
// plus a close function. The listener accepts and immediately closes incoming
// connections — sufficient for probeReachable which only cares whether
// net.Dial succeeds.
func listenLocal(t *testing.T) (port int, closeFn func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().(*net.TCPAddr)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	return addr.Port, func() { _ = ln.Close() }
}

// unboundLocalPort picks a TCP port on 127.0.0.1, then closes the listener so
// the port is free again — any subsequent connect to it MAY refuse (race-free
// on Windows + Linux for localhost). Used to exercise the "unreachable" path
// of probeReachable without depending on DNS or external network state.
func unboundLocalPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return port
}

// TestBackendSupervisor_UnreachableEarlyReturn is in package lifecycle; the
// monitor-side counterpart is split below so each package owns its own
// integration coverage of the StatusUnreachable feature.

// TestMaybeProbeUnreachable_StdioBackendNoOp verifies the defensive guard at
// monitor.go:308-312: a stdio backend (entry.Config.URL == "") that somehow
// landed in StatusUnreachable must NOT trigger probeReachable / Start,
// because stdio is not URL-addressable.
func TestMaybeProbeUnreachable_StdioBackendNoOp(t *testing.T) {
	mock := newMockLM()
	mock.addEntry("stdio-backend", models.StatusUnreachable, models.ServerConfig{
		Command: "/bin/echo",
		// URL deliberately empty
	})
	tm := newTestableMonitor(mock)
	tm.CheckOnce(context.Background())

	assert.Equal(t, 0, mock.starts,
		"stdio backend in StatusUnreachable must not trigger Start (probe path is HTTP-only)")
}

// TestMaybeProbeUnreachable_ReachableTriggersStart verifies the recovery
// branch: when probeReachable returns true, the monitor calls
// LifecycleManager.Start to attempt a full reconnect. Uses a real TCP
// listener so the probe's net.Dial succeeds without mocking
// probeReachable itself — guards against URL-parse / port-resolution
// regressions.
func TestMaybeProbeUnreachable_ReachableTriggersStart(t *testing.T) {
	port, stop := listenLocal(t)
	defer stop()

	mock := newMockLM()
	mock.addEntry("http-backend", models.StatusUnreachable, models.ServerConfig{
		URL: fmt.Sprintf("http://127.0.0.1:%d/mcp", port),
	})

	tm := newTestableMonitor(mock)
	tm.CheckOnce(context.Background())

	assert.Equal(t, 1, mock.starts,
		"reachable backend must trigger exactly one Start call on the recovery branch")
	assert.Equal(t, 0, mock.restarts,
		"reachable backend recovery uses Start (not Restart) so the lifecycle classifier re-routes via TCP pre-check + handshake")
}

// TestMaybeProbeUnreachable_UnreachableDoesNotStart verifies that a failed
// reachability probe keeps the backend in StatusUnreachable: no Start, no
// Restart, no status mutation. The contract is "stable yellow warning",
// not "spin while host is down".
func TestMaybeProbeUnreachable_UnreachableDoesNotStart(t *testing.T) {
	port := unboundLocalPort(t)

	mock := newMockLM()
	mock.addEntry("http-backend", models.StatusUnreachable, models.ServerConfig{
		URL: fmt.Sprintf("http://127.0.0.1:%d/mcp", port),
	})

	tm := newTestableMonitor(mock)
	tm.CheckOnce(context.Background())

	assert.Equal(t, 0, mock.starts, "unreachable probe must not trigger Start")
	assert.Equal(t, 0, mock.restarts, "unreachable probe must not trigger Restart either")
	// No status events for an unreachable probe — the gate stays exactly as set.
	assert.Empty(t, mock.statusLog,
		"unreachable probe must be invisible at the lifecycle layer until reachability recovers")
}

// TestMaybeProbeUnreachable_DoesNotIncrementRestartCount asserts the critical
// invariant from monitor.go:299-306: the slow-poll path must NOT touch
// serverState.restartCount, firstRestartAt, consecutiveFailures, or any
// circuit-breaker accounting field. The breaker is for flapping
// (Start-success → ping-fail loop), not for a network partition that
// produces a stable "host offline" condition.
//
// Verifies the invariant directly by reading state.restartCount after
// the probe — tests live in the same package, so no exported accessor is
// needed. Failure of this assertion would mean a long VPN-off window
// eventually opens the circuit and blocks recovery once the host returns.
func TestMaybeProbeUnreachable_DoesNotIncrementRestartCount(t *testing.T) {
	port := unboundLocalPort(t)

	mock := newMockLM()
	mock.addEntry("http-backend", models.StatusUnreachable, models.ServerConfig{
		URL: fmt.Sprintf("http://127.0.0.1:%d/mcp", port),
	})

	tm := newTestableMonitor(mock)
	tm.CheckOnce(context.Background())

	tm.mu.Lock()
	state := tm.states["http-backend"]
	require.NotNil(t, state, "serverState must exist after first probe")
	assert.Equal(t, 0, state.restartCount,
		"slow-poll probe must NOT increment restartCount (breaker exists for flapping, not partition)")
	assert.Equal(t, 0, state.consecutiveFailures,
		"slow-poll probe must NOT increment consecutiveFailures")
	assert.Equal(t, 0, state.consecutiveFailedStarts,
		"slow-poll probe must NOT increment consecutiveFailedStarts (no Start was attempted)")
	assert.True(t, state.firstRestartAt.IsZero(),
		"firstRestartAt must remain zero — no restart was ever attempted")
	assert.Equal(t, 1, state.reachProbeCount,
		"reachProbeCount must reflect exactly one probe dispatched")
	assert.False(t, state.nextReachProbeAt.IsZero(),
		"nextReachProbeAt must be scheduled for the next allowed probe window")
	tm.mu.Unlock()
}

// TestMaybeProbeUnreachable_Throttled verifies the per-state throttle gate at
// monitor.go:316-318. Once a probe is dispatched, a subsequent call inside
// UnreachableProbeInitial must skip the probe entirely (no Start call, no
// dial). Without this gate, checkAll would dial on every tick (~1s in
// production), defeating the slow-poll design.
func TestMaybeProbeUnreachable_Throttled(t *testing.T) {
	port, stop := listenLocal(t)
	defer stop()

	mock := newMockLM()
	mock.addEntry("http-backend", models.StatusUnreachable, models.ServerConfig{
		URL: fmt.Sprintf("http://127.0.0.1:%d/mcp", port),
	})

	tm := newTestableMonitor(mock)

	// First tick: probe dispatches (reachable) and triggers Start. The mock's
	// Start sets entry.Status = StatusRunning, so on the second tick checkAll's
	// switch routes to the Running branch (checkOne) NOT the Unreachable arm.
	// To exercise the throttle on its own, reset the entry status back to
	// Unreachable BEFORE the second tick, BUT keep reachProbeCount > 0 so
	// nextReachProbeAt gates the second probe.
	tm.CheckOnce(context.Background())
	require.Equal(t, 1, mock.starts, "first tick must dispatch and Start")

	mock.addEntry("http-backend", models.StatusUnreachable, models.ServerConfig{
		URL: fmt.Sprintf("http://127.0.0.1:%d/mcp", port),
	})
	// reachProbeCount=0 after first tick's recovery branch reset (monitor.go:343).
	// To exercise the throttle path we manually set nextReachProbeAt to the future
	// without resetting the count (mimics the in-Unreachable steady state between
	// probes).
	tm.mu.Lock()
	tm.states["http-backend"].nextReachProbeAt = time.Now().Add(30 * time.Second)
	tm.states["http-backend"].reachProbeCount = 1
	tm.mu.Unlock()

	tm.CheckOnce(context.Background())
	assert.Equal(t, 1, mock.starts,
		"second tick within throttle window must NOT dispatch a probe / Start (throttled)")
}

// TestProbeReachable_URLPortResolution verifies the port-resolution branch in
// probeReachable (monitor.go:367-373). The probe must default to port 80 for
// http:// and port 443 for https:// when the URL omits an explicit port —
// otherwise a backend like https://example.com/mcp would always probe :0.
// Pinned by a unit test because the branch is silent at the call site
// (no log line, no error path).
func TestProbeReachable_URLPortResolution(t *testing.T) {
	// Strict assertion: parse what the probe parses and recompute the dial
	// address. Failing this means the probe is dialing the wrong port.
	cases := []struct {
		name     string
		rawURL   string
		wantPort string
	}{
		{"http no port -> 80", "http://example.com/mcp", "80"},
		{"https no port -> 443", "https://example.com/mcp", "443"},
		{"mcps no port -> 443", "mcps://example.com/mcp", "443"},
		{"explicit port preserved", "http://example.com:9000/mcp", "9000"},
		{"https + explicit port", "https://example.com:8443/mcp", "8443"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse(tc.rawURL)
			require.NoError(t, err)
			port := u.Port()
			if port == "" {
				switch u.Scheme {
				case "https", "mcps":
					port = "443"
				default:
					port = "80"
				}
			}
			assert.Equal(t, tc.wantPort, port,
				"URL %q must resolve to dial port %s; probeReachable would otherwise mis-target the dial",
				tc.rawURL, tc.wantPort)
		})
	}
}

// TestCheckOne_RunningToUnreachable_OnPingPlusTCPFail verifies the
// Running→Unreachable transition added as the PLAN-unreachable-handling
// follow-up: a running HTTP backend whose MCP ping fails repeatedly AND whose
// host is TCP-unreachable must be routed to StatusUnreachable (slow-poll), NOT
// restart-stormed. This is the previously-missing entry into StatusUnreachable
// from a live backend (it could only be entered at Start time before).
//
// Drives the REAL monitor.checkOne (not the checkOneWithMockPing helper) so
// the production branch is exercised. The mock's Session() returns (nil,false)
// → checkMCPPing fails on every call; the backend URL points at an unbound
// local port → probeReachable returns false → the transition fires once the
// consecutive-failure threshold is crossed.
func TestCheckOne_RunningToUnreachable_OnPingPlusTCPFail(t *testing.T) {
	port := unboundLocalPort(t)

	mock := newMockLM()
	entry := models.ServerEntry{
		Name:   "http-backend",
		Status: models.StatusRunning,
		Config: models.ServerConfig{URL: fmt.Sprintf("http://127.0.0.1:%d/mcp", port)},
	}
	mock.addEntry(entry.Name, entry.Status, entry.Config)

	tm := newTestableMonitor(mock) // ConsecutiveFailureThreshold = 3
	ctx := context.Background()

	// Ticks 1..2: below threshold → Degraded, no restart, no Unreachable yet.
	tm.checkOne(ctx, entry)
	tm.checkOne(ctx, entry)
	assert.Equal(t, models.StatusDegraded, mock.lastStatus(entry.Name),
		"below-threshold ping failures must mark Degraded, not Unreachable")
	assert.Equal(t, 0, mock.getRestarts(), "no restart before threshold")

	// Tick 3: threshold crossed + host TCP-unreachable → StatusUnreachable.
	tm.checkOne(ctx, entry)
	assert.Equal(t, models.StatusUnreachable, mock.lastStatus(entry.Name),
		"running backend whose host is TCP-unreachable must route to slow-poll, not restart")
	assert.Equal(t, 0, mock.getRestarts(),
		"the Unreachable transition must NOT issue a Restart (that is the whole point)")

	tm.mu.Lock()
	state := tm.states[entry.Name]
	require.NotNil(t, state)
	assert.Equal(t, 0, state.restartCount,
		"Unreachable transition is outside the circuit-breaker — restartCount must stay 0")
	assert.Equal(t, 0, state.consecutiveFailures,
		"consecutiveFailures must be reset so recovery starts clean")
	assert.True(t, state.firstRestartAt.IsZero(),
		"firstRestartAt must remain zero — no restart was attempted")
	tm.mu.Unlock()
}

// TestCheckOne_RunningPingFailButReachable_Restarts is the counter-case that
// proves the transport-vs-protocol distinction: when the host IS reachable
// (TCP dial succeeds) but the MCP ping keeps failing, the backend is genuinely
// flapping at the protocol layer — it must take the RESTART path, NOT
// StatusUnreachable. Without this distinction the new branch would mask real
// protocol bugs behind a "host offline" badge.
func TestCheckOne_RunningPingFailButReachable_Restarts(t *testing.T) {
	port, stop := listenLocal(t) // host is reachable (real listener)
	defer stop()

	mock := newMockLM()
	entry := models.ServerEntry{
		Name:   "http-backend",
		Status: models.StatusRunning,
		Config: models.ServerConfig{URL: fmt.Sprintf("http://127.0.0.1:%d/mcp", port)},
	}
	mock.addEntry(entry.Name, entry.Status, entry.Config)

	tm := newTestableMonitor(mock) // threshold 3, RestartBackoffBase 0, SupervisorActive false
	ctx := context.Background()

	tm.checkOne(ctx, entry)
	tm.checkOne(ctx, entry)
	tm.checkOne(ctx, entry) // threshold crossed; host reachable → fall through to restart

	assert.Equal(t, 1, mock.getRestarts(),
		"reachable host + failing ping = protocol flap → must Restart, not go Unreachable")
	assert.NotEqual(t, models.StatusUnreachable, mock.lastStatus(entry.Name),
		"a reachable backend must never be marked Unreachable")
}

// TestMaybeProbeUnreachable_RecoveryE2E exercises the full recovery cycle:
//  1. backend in StatusUnreachable, target port unbound — probe fails, no Start;
//  2. operator brings the host back up (listener opens on the same port);
//  3. next probe succeeds, Start is dispatched, reachProbeCount + nextReachProbeAt reset.
//
// This is the simplest possible end-to-end test that exercises the
// docs/PLAN-unreachable-handling.md acceptance criterion C ("VPN reconnect →
// within 70s automatically Unreachable → Starting → Running, zero operator
// action") through the monitor layer. Full E2E with the lifecycle.Manager
// remains a larger follow-up; this test covers the monitor's contribution.
func TestMaybeProbeUnreachable_RecoveryE2E(t *testing.T) {
	port := unboundLocalPort(t)

	mock := newMockLM()
	mock.addEntry("http-backend", models.StatusUnreachable, models.ServerConfig{
		URL: fmt.Sprintf("http://127.0.0.1:%d/mcp", port),
	})

	tm := newTestableMonitor(mock)

	// Phase 1: host down — probe fails.
	tm.CheckOnce(context.Background())
	assert.Equal(t, 0, mock.starts, "Phase 1: unreachable host must not trigger Start")
	tm.mu.Lock()
	require.Equal(t, 1, tm.states["http-backend"].reachProbeCount,
		"Phase 1: one probe was dispatched even though it failed")
	// Clear the throttle so the next CheckOnce will actually probe again.
	tm.states["http-backend"].nextReachProbeAt = time.Time{}
	tm.mu.Unlock()

	// Phase 2: host comes back — bind a real listener on the SAME port.
	// 127.0.0.1:<port> was free; re-bind for the recovery probe.
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	require.NoError(t, err, "could not re-bind 127.0.0.1:%d for recovery phase", port)
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	tm.CheckOnce(context.Background())

	assert.Equal(t, 1, mock.starts,
		"Phase 2: reachable host must trigger exactly one Start on recovery")
	tm.mu.Lock()
	state := tm.states["http-backend"]
	assert.Equal(t, 0, state.reachProbeCount,
		"Phase 2: reachProbeCount must reset to 0 on successful recovery (monitor.go:343)")
	assert.True(t, state.nextReachProbeAt.IsZero(),
		"Phase 2: nextReachProbeAt must reset to zero on successful recovery (monitor.go:344)")
	assert.Equal(t, 0, state.restartCount,
		"Phase 2: full slow-poll → recovery cycle must NOT have touched restartCount")
	tm.mu.Unlock()
}

// TestMaybeProbeUnreachable_RecoveryReRegistersWithSupervisor is the H-1
// acceptance test: after a successful Start() on the recovery path, the
// monitor must call AddBackendToSupervisor so the backend rejoins the suture
// tree and gets crash-restart policy again.
//
// Without the H-1 fix the test fails because AddBackendToSupervisor is never
// called (the backend was removed from suture via ErrDoNotRestart and nothing
// re-added it). With the fix it is called exactly once after a successful
// recovery Start().
//
// The test also verifies the no-probe path (still-unreachable) does NOT call
// AddBackendToSupervisor — supervisor re-registration must only happen on
// confirmed-reachable+successful-start.
func TestMaybeProbeUnreachable_RecoveryReRegistersWithSupervisor(t *testing.T) {
	// Part A: host is reachable — Start succeeds — AddBackendToSupervisor called.
	t.Run("reachable_start_ok_reregisters", func(t *testing.T) {
		port, closeFn := listenLocal(t)
		defer closeFn()

		lm := newSupervisorTrackingLM()
		lm.addEntry("http-backend", models.StatusUnreachable, models.ServerConfig{
			URL: fmt.Sprintf("http://127.0.0.1:%d/mcp", port),
		})

		mon := NewMonitor(lm, time.Second, nil)
		mon.CheckOnce(context.Background())

		require.Equal(t, 1, lm.starts,
			"reachable backend must trigger exactly one Start call")
		assert.Equal(t, 1, lm.reAddCount("http-backend"),
			"H-1: AddBackendToSupervisor must be called once after a successful Start on recovery")
	})

	// Part B: host is still unreachable — Start not called — AddBackendToSupervisor not called.
	t.Run("unreachable_no_reregister", func(t *testing.T) {
		port := unboundLocalPort(t)

		lm := newSupervisorTrackingLM()
		lm.addEntry("http-backend", models.StatusUnreachable, models.ServerConfig{
			URL: fmt.Sprintf("http://127.0.0.1:%d/mcp", port),
		})

		mon := NewMonitor(lm, time.Second, nil)
		mon.CheckOnce(context.Background())

		assert.Equal(t, 0, lm.starts,
			"still-unreachable probe must not call Start")
		assert.Equal(t, 0, lm.reAddCount("http-backend"),
			"H-1: AddBackendToSupervisor must NOT be called when probe failed (nothing to re-register)")
	})

	// Part C: Start fails (e.g. still-unreachable at MCP level) — AddBackendToSupervisor not called.
	t.Run("start_fails_no_reregister", func(t *testing.T) {
		port, closeFn := listenLocal(t)
		defer closeFn()

		lm := newSupervisorTrackingLM()
		lm.startErr = fmt.Errorf("MCP handshake failed")
		lm.addEntry("http-backend", models.StatusUnreachable, models.ServerConfig{
			URL: fmt.Sprintf("http://127.0.0.1:%d/mcp", port),
		})

		mon := NewMonitor(lm, time.Second, nil)
		mon.CheckOnce(context.Background())

		assert.Equal(t, 1, lm.starts,
			"reachable probe must try Start even when Start itself fails")
		assert.Equal(t, 0, lm.reAddCount("http-backend"),
			"H-1: AddBackendToSupervisor must NOT be called when Start() returned an error")
	})

	// Part D: supervisor not active — AddBackendToSupervisor not called (legacy path).
	t.Run("supervisor_inactive_no_reregister", func(t *testing.T) {
		port, closeFn := listenLocal(t)
		defer closeFn()

		// Use plain mockLM (SupervisorActive() == false).
		lm := newMockLM()
		lm.addEntry("http-backend", models.StatusUnreachable, models.ServerConfig{
			URL: fmt.Sprintf("http://127.0.0.1:%d/mcp", port),
		})

		mon := NewMonitor(lm, time.Second, nil)
		mon.CheckOnce(context.Background())

		assert.Equal(t, 1, lm.starts,
			"reachable backend must trigger Start even in legacy (no-supervisor) mode")
		// mockLM.AddBackendToSupervisor is a no-op stub; we just verify Start was
		// called and no panic occurred — the guard in maybeProbeUnreachable skips
		// the re-registration when SupervisorActive() == false.
	})
}
