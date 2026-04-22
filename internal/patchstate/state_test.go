package patchstate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClock is a mutable clock source. Tests use it to advance time without
// sleeping.
type fakeClock struct {
	mu  sync.Mutex
	cur time.Time
}

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{cur: start}
}

func (fc *fakeClock) now() time.Time {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.cur
}

func (fc *fakeClock) advance(d time.Duration) {
	fc.mu.Lock()
	fc.cur = fc.cur.Add(d)
	fc.mu.Unlock()
}

func newTestState(t *testing.T, persistPath string) (*State, *fakeClock) {
	t.Helper()
	fc := newFakeClock(time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC))
	s := New(persistPath, nil)
	s.now = fc.now
	return s, fc
}

func TestRecordHeartbeat_StoreAndRetrieve(t *testing.T) {
	s, _ := newTestState(t, "")
	hb := Heartbeat{
		SessionID:    "sess-A",
		PatchVersion: "1.0.0",
		FiberOK:      true,
		MCPMethodOK:  true,
	}
	stored, err := s.RecordHeartbeat(hb)
	require.NoError(t, err)
	assert.Equal(t, "sess-A", stored.SessionID)
	assert.True(t, stored.FiberOK)

	list := s.Heartbeats()
	require.Len(t, list, 1)
	assert.Equal(t, "sess-A", list[0].SessionID)
}

func TestRecordHeartbeat_EmptySessionIDRejected(t *testing.T) {
	s, _ := newTestState(t, "")
	_, err := s.RecordHeartbeat(Heartbeat{})
	assert.Error(t, err)
	assert.Empty(t, s.Heartbeats())
}

func TestRecordHeartbeat_OverwritesPerSession(t *testing.T) {
	s, _ := newTestState(t, "")
	_, _ = s.RecordHeartbeat(Heartbeat{SessionID: "sess-A", PatchVersion: "1.0.0"})
	_, _ = s.RecordHeartbeat(Heartbeat{SessionID: "sess-A", PatchVersion: "1.0.1"})
	list := s.Heartbeats()
	require.Len(t, list, 1)
	assert.Equal(t, "1.0.1", list[0].PatchVersion)
}

func TestHeartbeat_TTLEviction(t *testing.T) {
	s, fc := newTestState(t, "")
	_, _ = s.RecordHeartbeat(Heartbeat{SessionID: "sess-A"})
	assert.Len(t, s.Heartbeats(), 1)

	fc.advance(HeartbeatTTL + time.Second)
	// Snapshot filters by TTL.
	assert.Empty(t, s.Heartbeats())

	// Cleaner removes from map as well.
	s.evictExpired()
	s.mu.RLock()
	assert.Empty(t, s.heartbeats)
	s.mu.RUnlock()
}

func TestEnqueueReconnect_Debounce(t *testing.T) {
	s, fc := newTestState(t, "")
	act1, ok := s.EnqueueReconnectAction(AggregatePluginServerName)
	require.True(t, ok)
	require.NotNil(t, act1)
	assert.Equal(t, "reconnect", act1.Type)
	assert.Equal(t, AggregatePluginServerName, act1.ServerName)

	// Second call inside debounce window: coalesced.
	fc.advance(100 * time.Millisecond)
	act2, ok := s.EnqueueReconnectAction(AggregatePluginServerName)
	assert.False(t, ok)
	assert.Nil(t, act2)

	// After debounce elapses: new action.
	fc.advance(ReconnectActionDebounce + time.Millisecond)
	act3, ok := s.EnqueueReconnectAction(AggregatePluginServerName)
	require.True(t, ok)
	assert.NotEqual(t, act1.ID, act3.ID)

	pending := s.PendingActions("")
	assert.Len(t, pending, 2) // act1, act3 (act2 coalesced)
}

func TestPendingActions_FIFOAndAck(t *testing.T) {
	s, fc := newTestState(t, "")
	a1, _ := s.EnqueueReconnectAction("mcp-gateway")
	fc.advance(ReconnectActionDebounce + time.Millisecond)
	a2, _ := s.EnqueueReconnectAction("mcp-gateway")
	fc.advance(ReconnectActionDebounce + time.Millisecond)
	a3, _ := s.EnqueueReconnectAction("mcp-gateway")

	// No cursor: return all 3.
	list := s.PendingActions("")
	require.Len(t, list, 3)
	assert.Equal(t, a1.ID, list[0].ID)
	assert.Equal(t, a2.ID, list[1].ID)
	assert.Equal(t, a3.ID, list[2].ID)

	// Ack middle; cursor-less query still returns remaining two undelivered.
	ok := s.AckAction(a2.ID)
	assert.True(t, ok)
	remaining := s.PendingActions("")
	require.Len(t, remaining, 2)
	assert.Equal(t, a1.ID, remaining[0].ID)
	assert.Equal(t, a3.ID, remaining[1].ID)

	// Cursor after a1 returns a3 only (a2 is delivered, skipped).
	after := s.PendingActions(a1.ID)
	require.Len(t, after, 1)
	assert.Equal(t, a3.ID, after[0].ID)

	// Idempotent ack.
	assert.True(t, s.AckAction(a2.ID))
	assert.False(t, s.AckAction("nonexistent-id"))
}

func TestProbeAction_EnqueueAndRecordResult(t *testing.T) {
	s, _ := newTestState(t, "")
	act, err := s.EnqueueProbeAction()
	require.NoError(t, err)
	assert.Equal(t, "probe-reconnect", act.Type)
	assert.Contains(t, act.ServerName, "__probe_nonexistent_")
	assert.NotEmpty(t, act.Nonce)

	err = s.RecordProbeResult(ProbeResult{
		Nonce: act.Nonce,
		OK:    false,
		Error: "Server not found: __probe_nonexistent_...",
	})
	require.NoError(t, err)

	pr := s.ProbeResult(act.Nonce)
	require.NotNil(t, pr)
	assert.False(t, pr.OK)
}

func TestProbeResult_EmptyNonceRejected(t *testing.T) {
	s, _ := newTestState(t, "")
	err := s.RecordProbeResult(ProbeResult{})
	assert.Error(t, err)
}

func TestProbeResult_TTL(t *testing.T) {
	s, fc := newTestState(t, "")
	_ = s.RecordProbeResult(ProbeResult{Nonce: "n1", OK: true})
	assert.NotNil(t, s.ProbeResult("n1"))

	fc.advance(ProbeTTL + time.Second)
	assert.Nil(t, s.ProbeResult("n1"))
}

func TestPersistenceRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "patch-state.json")
	s1, _ := newTestState(t, path)

	_, _ = s1.RecordHeartbeat(Heartbeat{SessionID: "sess-A", PatchVersion: "1.0.0", FiberOK: true})
	_, _ = s1.EnqueueReconnectAction("mcp-gateway")

	// Wait for both async persist goroutines to complete in order.
	s1.FlushPersists()
	waitForFile(t, path, time.Second)

	// Verify 0600 on POSIX.
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, filePerm, info.Mode().Perm())
	}

	// Second State loads prior file.
	s2, _ := newTestState(t, path)
	require.NoError(t, s2.Load())
	hbs := s2.Heartbeats()
	require.Len(t, hbs, 1)
	assert.Equal(t, "sess-A", hbs[0].SessionID)
	actions := s2.PendingActions("")
	require.Len(t, actions, 1)
	assert.Equal(t, "reconnect", actions[0].Type)
}

func TestLoad_TTLFilter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "patch-state.json")

	// Craft a persisted file with entries that look stale from the
	// perspective of the loader's fake clock.
	now := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	snap := persisted{
		Heartbeats: map[string]*Heartbeat{
			"fresh": {SessionID: "fresh", ReceivedAt: now.Add(-5 * time.Minute)},
			"stale": {SessionID: "stale", ReceivedAt: now.Add(-2 * time.Hour)},
		},
		Actions: []*PendingAction{
			{ID: "a-fresh", Type: "reconnect", CreatedAt: now.Add(-1 * time.Minute)},
			{ID: "a-stale", Type: "reconnect", CreatedAt: now.Add(-1 * time.Hour)},
			{ID: "a-delivered", Type: "reconnect", CreatedAt: now, Delivered: true},
		},
	}
	data, err := json.Marshal(snap)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))

	s, _ := newTestState(t, path)
	require.NoError(t, s.Load())

	// "stale" heartbeat dropped; "fresh" kept.
	hbs := s.Heartbeats()
	require.Len(t, hbs, 1)
	assert.Equal(t, "fresh", hbs[0].SessionID)

	// "a-stale" dropped (> ActionTTL); "a-delivered" dropped (already
	// delivered); only "a-fresh" survives.
	acts := s.PendingActions("")
	require.Len(t, acts, 1)
	assert.Equal(t, "a-fresh", acts[0].ID)
}

func TestLoad_CorruptFileStartsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "patch-state.json")
	require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o600))

	s, _ := newTestState(t, path)
	require.NoError(t, s.Load())
	assert.Empty(t, s.Heartbeats())
	assert.Empty(t, s.PendingActions(""))
}

func TestLoad_MissingFileOK(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nope.json")
	s, _ := newTestState(t, path)
	assert.NoError(t, s.Load())
}

func TestHeartbeatPersistDebounce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "patch-state.json")
	s, fc := newTestState(t, path)

	// First call persists.
	_, _ = s.RecordHeartbeat(Heartbeat{SessionID: "sess-A"})
	s.FlushPersists()
	waitForFile(t, path, time.Second)

	// Remove file — if second call persists, we'll see it reappear.
	require.NoError(t, os.Remove(path))

	// Second call inside debounce window — should NOT persist.
	fc.advance(5 * time.Second)
	_, _ = s.RecordHeartbeat(Heartbeat{SessionID: "sess-A", PatchVersion: "1.0.1"})
	s.FlushPersists() // any in-flight work settles (should be none)
	_, err := os.Stat(path)
	assert.True(t, os.IsNotExist(err), "file should not have been re-persisted inside debounce window")

	// After debounce elapses, next call persists.
	fc.advance(HeartbeatPersistDebounce)
	_, _ = s.RecordHeartbeat(Heartbeat{SessionID: "sess-A", PatchVersion: "1.0.2"})
	s.FlushPersists()
	waitForFile(t, path, time.Second)
}

func TestCleaner_EvictsTTLExpired(t *testing.T) {
	s, fc := newTestState(t, "")
	_, _ = s.RecordHeartbeat(Heartbeat{SessionID: "sess-A"})
	_ = s.RecordProbeResult(ProbeResult{Nonce: "n1"})

	fc.advance(HeartbeatTTL + time.Second)
	s.evictExpired()

	s.mu.RLock()
	assert.Empty(t, s.heartbeats)
	assert.Empty(t, s.probes)
	s.mu.RUnlock()
}

func TestConcurrentAccess(t *testing.T) {
	s, _ := newTestState(t, "")
	s.now = time.Now // real clock for real concurrency

	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for range 20 {
				_, _ = s.RecordHeartbeat(Heartbeat{
					SessionID: sessionIDOf(id),
				})
				_, _ = s.EnqueueProbeAction()
				_ = s.Heartbeats()
				_ = s.PendingActions("")
			}
		}(i)
	}
	wg.Wait()
	assert.NotEmpty(t, s.Heartbeats())
}

func TestStartStop_Idempotent(t *testing.T) {
	s, _ := newTestState(t, "")
	s.StartCleaner(10 * time.Millisecond)
	s.StartCleaner(10 * time.Millisecond) // second call no-op
	s.Stop()
	s.Stop() // second call no-op
}

func TestPendingActions_CursorMiss_FallsBackToAll(t *testing.T) {
	// Regression for the "cursor entry trimmed between ack and poll" window:
	// if the client polls with `after=<id>` where <id> was evicted by the
	// TTL cleaner, we must still return pending work instead of an empty
	// list (otherwise the patch silently stalls).
	s, fc := newTestState(t, "")

	// Enqueue a1 → ack it → advance past ActionTTL → cleaner drops it.
	a1, _ := s.EnqueueReconnectAction("mcp-gateway")
	_ = s.AckAction(a1.ID)
	fc.advance(ActionTTL + time.Minute)
	s.evictExpired()

	// Enqueue a2 (fresh), then poll with the stale a1 cursor.
	fc.advance(ReconnectActionDebounce + time.Millisecond)
	a2, _ := s.EnqueueReconnectAction("mcp-gateway")

	// Cursor a1 was evicted — without fallback, skipping=true loops through
	// the whole slice and returns []. Fallback returns a2.
	list := s.PendingActions(a1.ID)
	require.Len(t, list, 1)
	assert.Equal(t, a2.ID, list[0].ID)
}

func TestTrimActions_DropsDeliveredPastTTL(t *testing.T) {
	s, fc := newTestState(t, "")
	a1, _ := s.EnqueueReconnectAction("mcp-gateway")
	fc.advance(ReconnectActionDebounce + time.Millisecond)
	a2, _ := s.EnqueueReconnectAction("mcp-gateway")

	_ = s.AckAction(a1.ID)
	fc.advance(ActionTTL + time.Minute)
	s.evictExpired()

	// a1 was delivered before fc.advance crossed ActionTTL, so trim drops it.
	// a2 also past TTL (never acked): dropped.
	list := s.PendingActions("")
	assert.Empty(t, list)
	assert.NotNil(t, a2) // referenced to keep compiler happy
}

// sessionIDOf is a tiny helper avoiding fmt.Sprintf in hot test paths.
func sessionIDOf(id int) string {
	const digits = "0123456789"
	if id < 10 {
		return "sess-" + string(digits[id])
	}
	return "sess-" + string(digits[id/10]) + string(digits[id%10])
}

// waitForFile polls until path exists or timeout elapses.
func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("file did not appear within %s: %s", timeout, path)
}
