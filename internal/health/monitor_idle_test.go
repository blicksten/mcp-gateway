// Package health — TASK C2.3 behavioral test for §E: Idle backends must NOT
// be probed or started by the health monitor's checkAll cycle.
//
// This test complements the structural assertion in
// internal/lifecycle/boot_wiring_test.go Test 6 (which checks enum values).
// Here we actually run the Monitor.checkAll loop and confirm that a backend
// whose LifecycleManager reports StatusIdle never causes a Start, Restart,
// SetStatus, or AddBackendToSupervisor call — i.e. the monitor's switch
// default case leaves it entirely alone.
package health

import (
	"context"
	"testing"

	"mcp-gateway/internal/models"

	"github.com/stretchr/testify/assert"
)

// TestMonitor_IdleBackendNotPolled is the REAL behavioral barrier for §E.
// It constructs a Monitor over a mockLM that contains one Idle backend
// (vsp-Q99) and one Running backend (orchestrator), drives one CheckOnce
// cycle, and asserts:
//
//   - No Start call was issued for vsp-Q99 (starts counter stays 0).
//   - No Restart call was issued for vsp-Q99 (restarts counter stays 0).
//   - No SetStatus call was recorded for vsp-Q99 (statusLog has no entry
//     for it — the monitor must not transition the Idle backend to any
//     other status).
//
// The Running backend is included so the monitor has genuine work to do
// and we can confirm the Idle entry is specifically skipped (not that the
// whole loop no-ops).
func TestMonitor_IdleBackendNotPolled(t *testing.T) {
	mock := newMockLM()
	// vsp-Q99 is the Idle SAP backend — the monitor must leave it alone.
	mock.addEntry("vsp-Q99", models.StatusIdle, models.ServerConfig{
		Command: "/bin/false", // must never be invoked
	})
	// orchestrator is Running — gives the monitor something to actively check.
	mock.addEntry("orchestrator", models.StatusRunning, models.ServerConfig{
		Command: "echo",
	})
	// pingOK=true: the running backend ping succeeds so the monitor does not
	// emit spurious restart attempts that would pollute the counters.
	mock.pingOK = true

	mon := NewMonitor(mock, 0, nil)
	// Drive exactly one checkAll cycle through the exported entry point.
	mon.CheckOnce(context.Background())

	// Assert no Start or Restart was ever called.
	assert.Equal(t, 0, mock.starts,
		"Start must not be called when the only transition-eligible backend is Idle")
	assert.Equal(t, 0, mock.getRestarts(),
		"Restart must not be called for any backend in this cycle")

	// Assert no SetStatus was recorded for the Idle backend.
	mock.mu.Lock()
	defer mock.mu.Unlock()
	for _, ev := range mock.statusLog {
		if ev.Name == "vsp-Q99" {
			t.Errorf("SetStatus(%q, %q, %q) was called — Idle backend must not be touched by checkAll",
				ev.Name, ev.Status, ev.LastErr)
		}
	}
}

// TestMonitor_IdleBackendWithURLNotSlowPolled verifies the StatusUnreachable
// slow-poll branch specifically: an Idle backend with a URL set must still NOT
// be probed (StatusIdle is not StatusUnreachable; the switch default is hit,
// not the slow-poll case). This closes the specific regression described in
// §E: checkAll's StatusUnreachable case must not misfire for StatusIdle.
func TestMonitor_IdleBackendWithURLNotSlowPolled(t *testing.T) {
	mock := newMockLM()
	// Idle backend WITH a URL — if the switch mistakenly matched
	// StatusUnreachable, maybeProbeUnreachable would try to dial this URL.
	mock.addEntry("vsp-Q99", models.StatusIdle, models.ServerConfig{
		URL: "http://sap.local:8000",
	})
	mock.pingOK = true

	mon := NewMonitor(mock, 0, nil)
	mon.CheckOnce(context.Background())

	assert.Equal(t, 0, mock.starts,
		"Start must not be called for StatusIdle backend even when a URL is configured")
	assert.Equal(t, 0, mock.getRestarts(),
		"Restart must not be called for StatusIdle backend")

	mock.mu.Lock()
	defer mock.mu.Unlock()
	for _, ev := range mock.statusLog {
		if ev.Name == "vsp-Q99" {
			t.Errorf("SetStatus was called for Idle backend: %+v", ev)
		}
	}
}
