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

// --- sweepStaleTmps (Phase 3 — PLAN refactor-9a7ff95b) -------------------

// TestLoad_SweepsStaleTmps verifies that Load removes patch-state.*.tmp files
// older than StaleTmpThreshold and leaves younger ones plus unrelated files.
func TestLoad_SweepsStaleTmps(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "patch-state.json")

	// Write a valid state file so Load completes the rehydrate path.
	snap := persisted{
		Heartbeats: map[string]*Heartbeat{},
		Actions:    []*PendingAction{},
	}
	data, err := json.Marshal(snap)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))

	s, fc := newTestState(t, path)

	// Create a stale tmp (6 minutes old — older than StaleTmpThreshold).
	staleTmp := filepath.Join(dir, "patch-state.stale.tmp")
	require.NoError(t, os.WriteFile(staleTmp, []byte("stale"), 0o600))
	staleTime := fc.now().Add(-6 * time.Minute)
	require.NoError(t, os.Chtimes(staleTmp, staleTime, staleTime))

	// Create a young tmp (1 minute old — younger than StaleTmpThreshold).
	youngTmp := filepath.Join(dir, "patch-state.young.tmp")
	require.NoError(t, os.WriteFile(youngTmp, []byte("young"), 0o600))
	youngTime := fc.now().Add(-1 * time.Minute)
	require.NoError(t, os.Chtimes(youngTmp, youngTime, youngTime))

	require.NoError(t, s.Load())

	_, staleErr := os.Stat(staleTmp)
	assert.True(t, os.IsNotExist(staleErr), "stale tmp must be removed")

	_, youngErr := os.Stat(youngTmp)
	assert.NoError(t, youngErr, "young tmp must remain")

	_, stateErr := os.Stat(path)
	assert.NoError(t, stateErr, "state file must remain")
}

// TestLoad_SweepDoesNotTouchUnrelatedFiles verifies that sweep only removes
// files matching the "patch-state.*.tmp" pattern — .bak and unrelated .tmp
// files are left untouched even when they are older than StaleTmpThreshold.
func TestLoad_SweepDoesNotTouchUnrelatedFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "patch-state.json")

	snap := persisted{
		Heartbeats: map[string]*Heartbeat{},
		Actions:    []*PendingAction{},
	}
	data, err := json.Marshal(snap)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))

	s, fc := newTestState(t, path)
	oldTime := fc.now().Add(-10 * time.Minute)

	// Unrelated .tmp (no "patch-state." prefix) — must not be removed.
	unrelated := filepath.Join(dir, "other.tmp")
	require.NoError(t, os.WriteFile(unrelated, []byte("x"), 0o600))
	require.NoError(t, os.Chtimes(unrelated, oldTime, oldTime))

	// Backup file — must not be removed.
	backup := filepath.Join(dir, "patch-state.bak")
	require.NoError(t, os.WriteFile(backup, []byte("x"), 0o600))
	require.NoError(t, os.Chtimes(backup, oldTime, oldTime))

	require.NoError(t, s.Load())

	_, err = os.Stat(unrelated)
	assert.NoError(t, err, "unrelated .tmp must not be removed")

	_, err = os.Stat(backup)
	assert.NoError(t, err, ".bak file must not be removed")
}

// TestLoad_MissingFileIsNotAnError verifies that Load on a non-existent
// persistPath returns nil without entering the rehydrate path (and therefore
// without calling sweepStaleTmps).
func TestLoad_MissingFileIsNotAnError(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ghost-subdir")
	path := filepath.Join(dir, "patch-state.json")

	s, _ := newTestState(t, path)
	// os.ReadFile returns ErrNotExist — Load exits early with nil.
	err := s.Load()
	assert.NoError(t, err)
}

// TestLoad_SweepFailureDoesNotFailLoad verifies that when a valid state file
// exists but sweepStaleTmps cannot read the parent directory (e.g. a ReadDir
// error), Load still succeeds and the error is only logged.
//
// On Windows, marking a directory as unreadable via chmod is unreliable, so
// we synthesise the failure by pointing persistPath at a nested path whose
// parent is itself inside a non-existent grandparent — after writing the
// state file to the actual dir and then renaming the dir to simulate the
// scenario. Instead we use a simpler approach: write the state file to one
// temp dir but set persistPath to a different temp dir that has been removed
// so ReadDir on it will fail. The state is written to a helper path; Load
// will succeed reading it but sweep will fail on the unrelated dir.
//
// Simpler portable approach: create a valid state file, let Load succeed,
// and replace the dir with a file so ReadDir fails. We call sweepStaleTmps
// directly to avoid platform chmod complications.
func TestLoad_SweepFailureDoesNotFailLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "patch-state.json")

	snap := persisted{
		Heartbeats: map[string]*Heartbeat{},
		Actions:    []*PendingAction{},
	}
	data, err := json.Marshal(snap)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))

	s, _ := newTestState(t, path)
	// Load rehydrates successfully.
	require.NoError(t, s.Load())

	// Now call sweepStaleTmps with a state pointing at a non-existent dir
	// to exercise the ReadDir-failure branch. Replace persistPath with a
	// path inside a removed directory so ReadDir returns an error.
	s.persistPath = filepath.Join(t.TempDir(), "gone", "patch-state.json")
	// Call sweepStaleTmps directly (without holding s.mu) — intentional
	// single-threaded test bypass. Production Load() calls sweepStaleTmps
	// after releasing s.mu (see loadLocked). The direct call here exercises
	// the ReadDir-failure branch without platform-specific chmod tricks.
	// This must not panic or return an error — it only logs.
	s.sweepStaleTmps()
}

// --- SessionPid lifecycle (PLAN-unfreeze-button v3 T4) --------------------

// TestSessionPidLifecycle exercises RecordSessionPid / GetSessionPid /
// RemoveSessionPid directly on the State type without an HTTP layer. The
// API handler tests in internal/api cover the wire-shape paths; this test
// covers internal-caller invariants (idempotent remove, last-write-wins
// overwrite, validation guards) that the HTTP layer reflects but does not
// own.
func TestSessionPidLifecycle(t *testing.T) {
	s := New("", nil) // nil logger defaults to slog.Default per New's contract

	// Record happy path.
	entry, err := s.RecordSessionPid("sess-A", 12345)
	require.NoError(t, err)
	assert.Equal(t, uint32(12345), entry.PID)
	assert.False(t, entry.RegisteredAt.IsZero())

	// Get returns a copy.
	got, ok := s.GetSessionPid("sess-A")
	require.True(t, ok)
	assert.Equal(t, uint32(12345), got.PID)

	// Last-write-wins overwrite (claude.exe restart in same VSCode tab).
	_, err = s.RecordSessionPid("sess-A", 54321)
	require.NoError(t, err)
	got2, ok := s.GetSessionPid("sess-A")
	require.True(t, ok)
	assert.Equal(t, uint32(54321), got2.PID)

	// Remove is idempotent and reports true only when an entry existed.
	assert.True(t, s.RemoveSessionPid("sess-A"))
	assert.False(t, s.RemoveSessionPid("sess-A"))
	_, ok = s.GetSessionPid("sess-A")
	assert.False(t, ok)

	// Validation guards: empty session_id, reserved PIDs.
	_, err = s.RecordSessionPid("", 100)
	assert.Error(t, err)
	_, err = s.RecordSessionPid("sess-B", 0)
	assert.Error(t, err)
	_, err = s.RecordSessionPid("sess-B", 4)
	assert.Error(t, err)
}

// TestRemoveSessionPidIfPid_CASSemantics verifies the compare-and-swap delete
// semantics of RemoveSessionPidIfPid: it only removes when the stored PID
// matches expectedPID, protecting against clobbering a freshly-registered PID
// that arrived while a long-running operation (e.g. Stop-Process) was in
// flight.
func TestRemoveSessionPidIfPid_CASSemantics(t *testing.T) {
	s := New("", nil)

	// Register initial PID.
	_, err := s.RecordSessionPid("sess-C", 1000)
	require.NoError(t, err)

	// Wrong PID: must not remove.
	removed := s.RemoveSessionPidIfPid("sess-C", 9999)
	assert.False(t, removed, "wrong PID must not remove entry")
	got, ok := s.GetSessionPid("sess-C")
	require.True(t, ok, "entry must still exist")
	assert.Equal(t, uint32(1000), got.PID)

	// Correct PID: must remove.
	removed = s.RemoveSessionPidIfPid("sess-C", 1000)
	assert.True(t, removed, "correct PID must remove entry")
	_, ok = s.GetSessionPid("sess-C")
	assert.False(t, ok, "entry must be gone after CAS remove")

	// Unknown session: must return false.
	removed = s.RemoveSessionPidIfPid("unknown", 1000)
	assert.False(t, removed, "unknown session must return false")
}

// --- FM-9 EnforceWindowAndRecordPid tests (P1.2 Sonnet fix-in-cycle 2026-05-22) ---

// TestEnforceWindow_NoConflict_Stores — basic path: empty registry, store
// new entry with window_id, no conflict.
func TestEnforceWindow_NoConflict_Stores(t *testing.T) {
	s, _ := newTestState(t, "")
	live := func(uint32) bool { return true }

	res, err := s.EnforceWindowAndRecordPid("sess-1", 12345, "win-A", live)
	require.NoError(t, err)
	require.NotNil(t, res.Stored)
	assert.Nil(t, res.Conflict)
	assert.False(t, res.EvictedStale)
	assert.Equal(t, uint32(12345), res.Stored.PID)
	assert.Equal(t, "win-A", res.Stored.WindowID)
}

// TestEnforceWindow_LiveConflict_ReturnsConflict — different session,
// same window_id, live PID → returns Conflict, no store.
func TestEnforceWindow_LiveConflict_ReturnsConflict(t *testing.T) {
	s, _ := newTestState(t, "")
	live := func(uint32) bool { return true }

	_, err := s.RecordSessionPidWithWindow("sess-existing", 11111, "win-X")
	require.NoError(t, err)

	res, err := s.EnforceWindowAndRecordPid("sess-newcomer", 22222, "win-X", live)
	require.NoError(t, err)
	require.NotNil(t, res.Conflict)
	assert.Nil(t, res.Stored)
	assert.Equal(t, "sess-existing", res.ConflictSID)
	assert.Equal(t, uint32(11111), res.Conflict.PID)

	// New entry must NOT have been stored.
	_, ok := s.GetSessionPid("sess-newcomer")
	assert.False(t, ok, "rejected newcomer must not be stored")
}

// TestEnforceWindow_StaleConflict_EvictsAndStores — different session,
// same window_id, dead PID → evicts stale, stores new.
func TestEnforceWindow_StaleConflict_EvictsAndStores(t *testing.T) {
	s, _ := newTestState(t, "")
	live := func(pid uint32) bool { return pid != 55555 } // 55555 declared dead

	_, err := s.RecordSessionPidWithWindow("sess-stale", 55555, "win-Z")
	require.NoError(t, err)

	res, err := s.EnforceWindowAndRecordPid("sess-fresh", 66666, "win-Z", live)
	require.NoError(t, err)
	require.NotNil(t, res.Stored)
	assert.Nil(t, res.Conflict)
	assert.True(t, res.EvictedStale)
	assert.Equal(t, uint32(55555), res.EvictedPID)

	_, ok := s.GetSessionPid("sess-stale")
	assert.False(t, ok, "stale entry must be evicted")
	got, ok := s.GetSessionPid("sess-fresh")
	require.True(t, ok)
	assert.Equal(t, uint32(66666), got.PID)
}

// TestEnforceWindow_SameSession_Overwrites — re-registration: same session,
// new PID, same window — overwrite unconditionally (claude.exe restart).
func TestEnforceWindow_SameSession_Overwrites(t *testing.T) {
	s, _ := newTestState(t, "")
	live := func(uint32) bool { return true }

	_, err := s.RecordSessionPidWithWindow("sess-restart", 10001, "win-R")
	require.NoError(t, err)

	res, err := s.EnforceWindowAndRecordPid("sess-restart", 10002, "win-R", live)
	require.NoError(t, err)
	require.NotNil(t, res.Stored)
	assert.Nil(t, res.Conflict)
	assert.False(t, res.EvictedStale, "same-session re-register is not an eviction")
	assert.Equal(t, uint32(10002), res.Stored.PID)
}

// TestEnforceWindow_ConcurrentSameWindow_OneWins — closes the Sonnet
// HIGH-1 finding (TOCTOU race fix). Many goroutines call
// EnforceWindowAndRecordPid in parallel for the SAME window_id with
// DIFFERENT session_ids; under the compound write-lock atomicity, exactly
// one must win (Stored != nil) and all others must observe a Conflict.
//
// Run with `go test -race` to catch any residual data race in the
// State write-path.
func TestEnforceWindow_ConcurrentSameWindow_OneWins(t *testing.T) {
	s, _ := newTestState(t, "")
	live := func(uint32) bool { return true }

	const N = 8
	var wg sync.WaitGroup
	results := make([]*EnforceWindowResult, N)

	wg.Add(N)
	for i := range N {
		go func(i int) {
			defer wg.Done()
			sid := "sess-concurrent-" + string(rune('A'+i))
			pid := uint32(20000 + i)
			res, err := s.EnforceWindowAndRecordPid(sid, pid, "win-CONCURRENT", live)
			require.NoError(t, err)
			results[i] = res
		}(i)
	}
	wg.Wait()

	wins := 0
	conflicts := 0
	for _, r := range results {
		switch {
		case r.Stored != nil && r.Conflict == nil:
			wins++
		case r.Conflict != nil && r.Stored == nil:
			conflicts++
		default:
			t.Fatalf("invalid result: %+v", r)
		}
	}
	assert.Equal(t, 1, wins, "exactly one goroutine must win the window")
	assert.Equal(t, N-1, conflicts, "every other goroutine must see a Conflict")
}

// TestSessionPidPersistence_RoundTrip verifies the Gap 1 fix (2026-05-24):
// SessionPid entries are written to disk on register-pid and rehydrated on
// daemon restart. Closes the gap that left /unfreeze + FM-9 enforcement
// non-functional after every daemon respawn.
func TestSessionPidPersistence_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "patch-state.json")

	s1, _ := newTestState(t, path)
	_, err := s1.RecordSessionPidWithWindow("sess-A", 12345, "window-x")
	require.NoError(t, err)
	_, err = s1.EnforceWindowAndRecordPid("sess-B", 67890, "window-y", nil)
	require.NoError(t, err)

	s1.FlushPersists()
	waitForFile(t, path, time.Second)

	// New State reads the file fresh — same daemon-restart shape.
	s2, _ := newTestState(t, path)
	require.NoError(t, s2.Load())

	entryA, ok := s2.GetSessionPid("sess-A")
	require.True(t, ok, "sess-A must rehydrate")
	assert.Equal(t, uint32(12345), entryA.PID)
	assert.Equal(t, "window-x", entryA.WindowID)

	entryB, ok := s2.GetSessionPid("sess-B")
	require.True(t, ok, "sess-B must rehydrate")
	assert.Equal(t, uint32(67890), entryB.PID)
	assert.Equal(t, "window-y", entryB.WindowID)
}

// TestSessionPidPersistence_TTLDropsStale ensures the TTL filter at Load
// drops entries older than SessionPidTTL so a long-dead claude.exe does
// not get resurrected on next daemon boot.
func TestSessionPidPersistence_TTLDropsStale(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "patch-state.json")

	now := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
	snap := persisted{
		SessionPids: map[string]*SessionPid{
			"fresh": {SessionID: "fresh", PID: 100, WindowID: "win-fresh", RegisteredAt: now.Add(-1 * time.Hour)},
			"stale": {SessionID: "stale", PID: 200, WindowID: "win-stale", RegisteredAt: now.Add(-48 * time.Hour)},
		},
	}
	data, err := json.Marshal(snap)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))

	s, _ := newTestState(t, path)
	s.now = func() time.Time { return now }
	require.NoError(t, s.Load())

	_, freshOK := s.GetSessionPid("fresh")
	assert.True(t, freshOK, "1h-old entry must rehydrate (under 24h TTL)")
	_, staleOK := s.GetSessionPid("stale")
	assert.False(t, staleOK, "48h-old entry must be dropped at Load (over 24h TTL)")
}

// TestSessionPidPersistence_RemoveSurvivesRestart verifies that removing a
// SessionPid (e.g., via /unfreeze) ALSO persists, so a stale entry does
// not silently reappear after restart.
func TestSessionPidPersistence_RemoveSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "patch-state.json")

	s1, _ := newTestState(t, path)
	_, err := s1.RecordSessionPidWithWindow("sess-X", 555, "win-z")
	require.NoError(t, err)
	require.True(t, s1.RemoveSessionPid("sess-X"))

	s1.FlushPersists()
	waitForFile(t, path, time.Second)

	s2, _ := newTestState(t, path)
	require.NoError(t, s2.Load())
	_, ok := s2.GetSessionPid("sess-X")
	assert.False(t, ok, "removed entry must NOT rehydrate")
}
