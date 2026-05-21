package health

// TestMonitor_AttemptRestart_DefersToSupervisorWhenActive (F2) verifies that
// Monitor.attemptRestart is a no-op when the suture supervisor is active.
// This closes the dual-restart race documented in
// docs/spikes/2026-05-21-shim-architecture-draft.md §11 F2.

import (
	"context"
	"testing"

	"mcp-gateway/internal/models"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
)

// f2MockLM is a minimal LifecycleManager fake that tracks whether Restart
// was called and exposes a configurable SupervisorActive() return value.
type f2MockLM struct {
	supervisorActive bool
	restartCalled    bool
}

func (m *f2MockLM) Entries() []models.ServerEntry {
	return []models.ServerEntry{
		{Name: "test-srv", Status: models.StatusRunning, Config: models.ServerConfig{Command: "x"}},
	}
}

func (m *f2MockLM) Session(string) (*mcp.ClientSession, bool) { return nil, false }

func (m *f2MockLM) SetStatus(string, models.ServerStatus, string) {}

func (m *f2MockLM) Restart(_ context.Context, _ string) error {
	m.restartCalled = true
	return nil
}

func (m *f2MockLM) Start(_ context.Context, _ string) error { return nil }

func (m *f2MockLM) SupervisorActive() bool { return m.supervisorActive }

// TestMonitor_AttemptRestart_DefersToSupervisorWhenActive: with supervisor
// active, attemptRestart must return without calling Restart.
func TestMonitor_AttemptRestart_DefersToSupervisorWhenActive(t *testing.T) {
	lm := &f2MockLM{supervisorActive: true}
	mon := NewMonitor(lm, 0, nil)
	// Disable backoff so the call is not gated on nextRestartAllowedAt.
	mon.RestartBackoffBase = 0

	mon.attemptRestart(context.Background(), "test-srv")

	assert.False(t, lm.restartCalled,
		"Restart must NOT be called when supervisor is active (F2 deferred-to-supervisor path)")
}

// TestMonitor_AttemptRestart_RestartCalledWhenSupervisorInactive: without
// supervisor, attemptRestart must proceed and call Restart normally.
func TestMonitor_AttemptRestart_RestartCalledWhenSupervisorInactive(t *testing.T) {
	lm := &f2MockLM{supervisorActive: false}
	mon := NewMonitor(lm, 0, nil)
	// Disable backoff so the call is not gated on nextRestartAllowedAt.
	mon.RestartBackoffBase = 0
	// Disable circuit breaker threshold (set very high) so we don't trip it.
	mon.CircuitBreakerThreshold = 1000

	mon.attemptRestart(context.Background(), "test-srv")

	assert.True(t, lm.restartCalled,
		"Restart MUST be called when supervisor is not active (legacy restart path)")
}
