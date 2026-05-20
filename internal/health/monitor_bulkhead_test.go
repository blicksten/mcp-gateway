package health

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"testing"
	"time"

	"mcp-gateway/internal/models"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
)

// bulkheadMockLM is a LifecycleManager mock that records per-name Session()
// invocations and can simulate a slow lookup (representative of a hung backend).
type bulkheadMockLM struct {
	mu         sync.Mutex
	entries    []models.ServerEntry
	calls      map[string]int
	slowFor    time.Duration
	slowSet    map[string]bool
	restartSet map[string]int
}

func newBulkheadMockLM(healthyN, slowN int, slowFor time.Duration) *bulkheadMockLM {
	m := &bulkheadMockLM{
		calls:      make(map[string]int),
		slowSet:    make(map[string]bool),
		restartSet: make(map[string]int),
		slowFor:    slowFor,
	}
	for i := 0; i < healthyN; i++ {
		name := "healthy-" + strconv.Itoa(i)
		m.entries = append(m.entries, models.ServerEntry{
			Name:   name,
			Status: models.StatusRunning,
		})
	}
	for i := 0; i < slowN; i++ {
		name := "slow-" + strconv.Itoa(i)
		m.entries = append(m.entries, models.ServerEntry{
			Name:   name,
			Status: models.StatusRunning,
		})
		m.slowSet[name] = true
	}
	return m
}

func (m *bulkheadMockLM) Entries() []models.ServerEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]models.ServerEntry, len(m.entries))
	copy(out, m.entries)
	return out
}

func (m *bulkheadMockLM) Session(name string) (*mcp.ClientSession, bool) {
	m.mu.Lock()
	m.calls[name]++
	slow := m.slowSet[name]
	m.mu.Unlock()
	if slow {
		time.Sleep(m.slowFor)
	}
	// Always report missing session → checkMCPPing returns false fast,
	// avoiding any go-sdk Ping plumbing in this unit test.
	return nil, false
}

func (m *bulkheadMockLM) SetStatus(_ string, _ models.ServerStatus, _ string) {}

func (m *bulkheadMockLM) Restart(_ context.Context, name string) error {
	m.mu.Lock()
	m.restartSet[name]++
	m.mu.Unlock()
	return nil
}

func (m *bulkheadMockLM) Start(_ context.Context, _ string) error {
	return nil
}

func (m *bulkheadMockLM) callsFor(name string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls[name]
}

// TestBulkhead_HealthyNotStarvedBySlowBackends is the T1.5.5 acceptance test:
// when a subset of backends responds slowly (simulating a hung child process),
// the bulkhead must still drive the healthy fan-out to completion within a
// bounded wall-clock — i.e. a permanently-degraded backend must not starve
// the others (reliability §2.6 R1 fault isolation).
func TestBulkhead_HealthyNotStarvedBySlowBackends(t *testing.T) {
	const (
		healthyN    = 25
		slowN       = 5
		slowFor     = 100 * time.Millisecond
		cycleBudget = 1500 * time.Millisecond
	)

	mock := newBulkheadMockLM(healthyN, slowN, slowFor)
	mon := NewMonitor(mock, time.Hour, slog.Default())
	mon.ConsecutiveFailureThreshold = 999 // never trigger restarts in this test
	mon.CircuitBreakerThreshold = 999

	ctx := context.Background()
	start := time.Now()
	mon.CheckOnce(ctx)
	elapsed := time.Since(start)

	// All healthy backends must have been visited exactly once.
	for i := 0; i < healthyN; i++ {
		name := "healthy-" + strconv.Itoa(i)
		assert.Equal(t, 1, mock.callsFor(name),
			"healthy backend %s must be checked once per cycle even when slow backends are blocking permits", name)
	}
	// Wall-clock guard: the load-bearing assertion is the per-backend call
	// count above (every healthy entry observed exactly once even while
	// slowN goroutines are occupying permits). The cycleBudget here is a
	// CI-hang sentinel — with capacity DefaultMaxConcurrentChecks (=20) and
	// 30 total checks, the theoretical worst case is ~200ms (two 100ms
	// rounds); 1.5s leaves headroom for Windows CI scheduler jitter.
	assert.Less(t, elapsed, cycleBudget,
		"slow backends must not block healthy checks beyond %s; elapsed=%s", cycleBudget, elapsed)
}
