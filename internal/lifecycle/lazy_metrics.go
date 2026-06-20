// Package lifecycle — TASK T1: lazy-spawn observability counters.
//
// lazyMetrics holds four cumulative, monotonic counters for the C2 lazy-spawn
// feature. They are surfaced on GET /api/v1/metrics (only when the flag is ON)
// so an operator can watch a canary and see warming/degrade/mismatch/spawn rates.
//
// Concurrency: every field is an atomic.Int64. Increment sites run on the
// detached spawn goroutines (EnsureStarted) and at boot under m.mu
// (SetupSupervisor); the read site is the metrics HTTP handler. Atomics make all
// of these data-race-free without coupling to m.mu.
//
// Feature flag: MCP_GATEWAY_LAZY_SPAWN (default OFF). When OFF the increment
// sites are never reached (EnsureStarted is not called; SetupSupervisor's
// manifest branch is skipped) so the counters stay zero and the metrics handler
// omits the LazySpawn block entirely (byte-identical payload to pre-T1).
package lifecycle

import (
	"sync/atomic"

	"mcp-gateway/internal/models"
)

// lazyMetrics is the embedded counter block on Manager.
type lazyMetrics struct {
	spawnOnInvoke         atomic.Int64
	warmingReturned       atomic.Int64
	degradeEvicted        atomic.Int64
	sigMismatchRediscover atomic.Int64
}

// LazyMetricsSnapshot returns an immutable point-in-time copy of the lazy-spawn
// counters. Each Load is independently atomic; the snapshot is therefore not a
// single consistent instant across all four fields, which is acceptable for a
// monitoring endpoint (the counters are independent and monotonically rising).
func (m *Manager) LazyMetricsSnapshot() models.LazySpawnMetrics {
	return models.LazySpawnMetrics{
		SpawnOnInvoke:         m.lazyMetrics.spawnOnInvoke.Load(),
		WarmingReturned:       m.lazyMetrics.warmingReturned.Load(),
		DegradeEvicted:        m.lazyMetrics.degradeEvicted.Load(),
		SigMismatchRediscover: m.lazyMetrics.sigMismatchRediscover.Load(),
	}
}
