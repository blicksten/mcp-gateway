// Package lifecycle — TASK T1: lazy-spawn observability counter tests.
//
// These exercise the four counters at their real increment boundaries:
//
//	spawn_on_invoke         — a real mock backend spawned via EnsureStarted.
//	warming_returned        — the real select returns ErrLazyWarming to the caller.
//	degrade_evicted         — a real failed spawn (nonexistent binary) → Guard 2.
//	sig_mismatch_rediscover — a real SetupSupervisor boot pass over a manifest
//	                          whose stored sig does not match the current config.
//
// The sig-mismatch counter is asserted against the full classification matrix
// (mismatch=1, cold-start absent=0, exact match=0, TTL-stale=0) so a future
// change that conflates a cache miss with a config change is caught.
package lifecycle

import (
	"context"
	"testing"
	"time"

	"log/slog"

	"mcp-gateway/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ----- initial state ----------------------------------------------------------

// TestLazyMetrics_InitialSnapshotZero verifies a fresh Manager reports all-zero
// counters (no spurious increments at construction).
func TestLazyMetrics_InitialSnapshotZero(t *testing.T) {
	t.Setenv(lazySpawnEnv, "1")
	m := buildLazyManager(t, "")
	assert.Equal(t, models.LazySpawnMetrics{}, m.LazyMetricsSnapshot())
}

// ----- spawn_on_invoke --------------------------------------------------------

// TestLazyMetrics_SpawnOnInvoke verifies a successful on-demand spawn increments
// spawn_on_invoke exactly once and leaves the other counters at zero.
func TestLazyMetrics_SpawnOnInvoke(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: requires building and running the mock server")
	}
	t.Setenv(lazySpawnEnv, "1")

	binary := buildMockServer(t)
	m, _ := buildLazyManagerWithManifest(t, binary)
	defer func() { _ = m.Stop(context.Background(), "vsp") }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	status, err := m.EnsureStarted(ctx, "vsp")
	require.NoError(t, err)
	require.Equal(t, models.StatusRunning, status)

	snap := m.LazyMetricsSnapshot()
	assert.Equal(t, int64(1), snap.SpawnOnInvoke, "successful spawn must count once")
	assert.Equal(t, int64(0), snap.WarmingReturned, "no warming on a within-budget spawn")
	assert.Equal(t, int64(0), snap.DegradeEvicted, "no degrade on a successful spawn")
}

// TestLazyMetrics_SpawnOnInvoke_SingleflightCountsOnce verifies that N coalesced
// first-invokes increment spawn_on_invoke exactly once (counted per spawn, not
// per caller) — matching the singleflight semantics.
func TestLazyMetrics_SpawnOnInvoke_SingleflightCountsOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: requires building and running the mock server")
	}
	t.Setenv(lazySpawnEnv, "1")

	binary := buildMockServer(t)
	m, _ := buildLazyManagerWithManifest(t, binary)
	defer func() { _ = m.Stop(context.Background(), "vsp") }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const N = 5
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		go func() {
			_, e := m.EnsureStarted(ctx, "vsp")
			errs <- e
		}()
	}
	for i := 0; i < N; i++ {
		require.NoError(t, <-errs)
	}

	assert.Equal(t, int64(1), m.LazyMetricsSnapshot().SpawnOnInvoke,
		"singleflight must yield exactly one spawn_on_invoke for N coalesced callers")
}

// ----- warming_returned -------------------------------------------------------

// TestLazyMetrics_WarmingReturned verifies that a caller whose budget is already
// expired receives ErrLazyWarming and increments warming_returned. The increment
// is synchronous in the caller's select, so it is deterministic regardless of the
// background spawn outcome.
func TestLazyMetrics_WarmingReturned(t *testing.T) {
	t.Setenv(lazySpawnEnv, "1")

	// Empty command → the background spawn fails fast; we assert only the
	// synchronously-incremented warming counter.
	m := buildLazyManager(t, "")

	dl := time.Now().Add(-1 * time.Millisecond)
	ctx, cancel := context.WithDeadline(context.Background(), dl)
	defer cancel()

	_, err := m.EnsureStarted(ctx, "vsp")
	require.ErrorIs(t, err, ErrLazyWarming)

	assert.Equal(t, int64(1), m.LazyMetricsSnapshot().WarmingReturned,
		"an ErrLazyWarming return must increment warming_returned once")

	// Let the detached spawn goroutine settle so it does not touch test state
	// after the test returns.
	require.Eventually(t, func() bool { return !m.IsLazyPending("vsp") },
		5*time.Second, 50*time.Millisecond)
}

// TestLazyMetrics_WarmingReturned_TimerBranch covers the budget-TIMER branch
// (as opposed to ctx.Done) with a REAL spawn slower than the caller budget: the
// 500ms budget floor fires while the 800ms pre-Start hook is still sleeping, so
// the caller returns ErrLazyWarming via the timer and warming_returned ticks.
func TestLazyMetrics_WarmingReturned_TimerBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: requires building and running the mock server")
	}
	t.Setenv(lazySpawnEnv, "1")

	binary := buildMockServer(t)
	m, _ := buildLazyManagerWithManifest(t, binary)
	defer func() { _ = m.Stop(context.Background(), "vsp") }()

	m.SetTestSpawnHook(func(_ string) { time.Sleep(800 * time.Millisecond) })

	dl := time.Now().Add(-1 * time.Millisecond) // floors budget at 500ms
	ctx, cancel := context.WithDeadline(context.Background(), dl)
	defer cancel()

	_, err := m.EnsureStarted(ctx, "vsp")
	require.ErrorIs(t, err, ErrLazyWarming)
	assert.GreaterOrEqual(t, m.LazyMetricsSnapshot().WarmingReturned, int64(1),
		"timer-branch warming must increment warming_returned")

	// Drain the background spawn so the test does not leak it.
	require.Eventually(t, func() bool {
		pollCtx, pollCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer pollCancel()
		s, e := m.EnsureStarted(pollCtx, "vsp")
		return s == models.StatusRunning && e == nil
	}, 10*time.Second, 100*time.Millisecond)
}

// ----- degrade_evicted --------------------------------------------------------

// TestLazyMetrics_DegradeEvicted verifies that a failed spawn (Guard 2) increments
// degrade_evicted and not spawn_on_invoke. The caller waits on the result channel
// (generous budget) so by the time EnsureStarted returns, the failure branch has
// run — fully deterministic.
func TestLazyMetrics_DegradeEvicted(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: requires real filesystem operations for manifest")
	}
	t.Setenv(lazySpawnEnv, "1")

	m, _ := buildLazyManagerWithManifest(t, "/nonexistent/binary/that/cannot/run")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	status, err := m.EnsureStarted(ctx, "vsp")
	require.Error(t, err)
	require.Equal(t, models.StatusError, status)

	snap := m.LazyMetricsSnapshot()
	assert.Equal(t, int64(1), snap.DegradeEvicted, "a failed spawn must count one degrade_evicted")
	assert.Equal(t, int64(0), snap.SpawnOnInvoke, "a failed spawn must NOT count spawn_on_invoke")
}

// ----- sig_mismatch_rediscover (classification matrix) ------------------------

// buildSAPManagerForSig builds a Manager with a single SAP backend (vsp-Q99)
// whose command is /bin/false, plus a manifest loaded from a temp file. The
// caller seeds the manifest before SetupSupervisor.
func buildSAPManagerForSig(t *testing.T) (*models.Config, *Manifest, string) {
	t.Helper()
	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"vsp-Q99": {Command: "/bin/false"},
		},
	}
	cfg.ApplyDefaults()
	mPath := t.TempDir() + "/tool-manifest.json"
	mf, err := LoadManifest(mPath)
	require.NoError(t, err)
	return cfg, mf, mPath
}

// TestLazyMetrics_SigMismatchRediscover verifies that a manifest entry whose
// stored sig does NOT match the current config increments sig_mismatch_rediscover.
func TestLazyMetrics_SigMismatchRediscover(t *testing.T) {
	t.Setenv(lazySpawnEnv, "1")

	cfg, mf, mPath := buildSAPManagerForSig(t)
	mf.Put("vsp-Q99", "deliberately-wrong-sig",
		[]models.ToolInfo{{Name: "stale", Server: "vsp-Q99"}})
	require.NoError(t, mf.Persist())
	mf2, err := LoadManifest(mPath)
	require.NoError(t, err)

	m := NewManager(cfg, "test", slog.Default())
	m.SetManifest(mf2)
	m.SetupSupervisor(slog.Default())

	assert.Equal(t, int64(1), m.LazyMetricsSnapshot().SigMismatchRediscover,
		"a config-sig mismatch must count one sig_mismatch_rediscover")
}

// TestLazyMetrics_ColdStartMiss_NoSigMismatch verifies that a plain cache miss
// (no manifest entry — cold-start) does NOT increment sig_mismatch_rediscover.
func TestLazyMetrics_ColdStartMiss_NoSigMismatch(t *testing.T) {
	t.Setenv(lazySpawnEnv, "1")

	cfg, mf, _ := buildSAPManagerForSig(t) // empty manifest

	m := NewManager(cfg, "test", slog.Default())
	m.SetManifest(mf)
	m.SetupSupervisor(slog.Default())

	assert.Equal(t, int64(0), m.LazyMetricsSnapshot().SigMismatchRediscover,
		"a cold-start cache miss must NOT count as a sig mismatch")
}

// TestLazyMetrics_SigMatch_NoSigMismatch verifies that a matching sig (the entry
// is served as Idle) does NOT increment sig_mismatch_rediscover.
func TestLazyMetrics_SigMatch_NoSigMismatch(t *testing.T) {
	t.Setenv(lazySpawnEnv, "1")

	cfg, mf, mPath := buildSAPManagerForSig(t)
	correctSig := BackendConfigSig(*cfg.Servers["vsp-Q99"])
	mf.Put("vsp-Q99", correctSig,
		[]models.ToolInfo{{Name: "sap_tool", Server: "vsp-Q99"}})
	require.NoError(t, mf.Persist())
	mf2, err := LoadManifest(mPath)
	require.NoError(t, err)

	m := NewManager(cfg, "test", slog.Default())
	m.SetManifest(mf2)
	m.SetupSupervisor(slog.Default())

	// Sanity: the matching entry must have seeded Idle.
	entry, ok := m.Entry("vsp-Q99")
	require.True(t, ok)
	require.Equal(t, models.StatusIdle, entry.Status)

	assert.Equal(t, int64(0), m.LazyMetricsSnapshot().SigMismatchRediscover,
		"a matching sig must NOT count as a sig mismatch")
}

// TestLazyMetrics_TTLStale_NoSigMismatch verifies that a TTL-expired entry (even
// with a matching sig) is classified as stale, NOT as a sig mismatch, so it does
// not increment sig_mismatch_rediscover.
func TestLazyMetrics_TTLStale_NoSigMismatch(t *testing.T) {
	t.Setenv(lazySpawnEnv, "1")

	cfg, mf, _ := buildSAPManagerForSig(t)
	correctSig := BackendConfigSig(*cfg.Servers["vsp-Q99"])
	// Inject a stale entry directly (correct sig, but discovered 8 days ago).
	mf.records["vsp-Q99"] = ManifestRecord{
		Name:          "vsp-Q99",
		Sig:           correctSig,
		Tools:         []CachedTool{{Name: "sap_tool"}},
		DiscoveredAt:  time.Now().Add(-8 * 24 * time.Hour),
		SchemaVersion: manifestSchemaVersion,
	}

	m := NewManager(cfg, "test", slog.Default())
	m.SetManifest(mf)
	m.SetupSupervisor(slog.Default())

	assert.Equal(t, int64(0), m.LazyMetricsSnapshot().SigMismatchRediscover,
		"a TTL-stale entry must be classified stale, NOT as a sig mismatch")
}
