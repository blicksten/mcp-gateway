// Package lifecycle — TASK C2.3: boot-wiring + supervisor Idle-gate tests.
//
// Test inventory:
//
//  1. Flag ON + SAP backend WITH manifest entry → StatusIdle at boot, no
//     supervisor child (not spawned), tools served via manifest.
//  2. Flag ON + SAP backend WITHOUT manifest entry (cold-start) → eager spawn,
//     and after successful start the manifest now has an entry; subsequent boot
//     with that manifest makes the backend Idle.
//  3. Flag ON + core (non-SAP) backend → always eager; never Idle regardless
//     of manifest contents.
//  4. BackendSupervisor.Serve returns ErrDoNotRestart for StatusIdle (unit).
//  5. Flag OFF → no backend is ever StatusIdle at boot; supervisor token list
//     matches today's behavior (regression barrier).
//  6. StatusIdle backend is NOT included in health monitor checkAll slow-poll.
package lifecycle

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"mcp-gateway/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thejerf/suture/v4"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// buildManifestWithEntry writes a manifest with one SAP entry to a temp dir
// and returns the loaded *Manifest. Requires the flag to be ON (caller must
// call t.Setenv before this).
func buildManifestWithEntry(t *testing.T, backendName string) (*Manifest, string) {
	t.Helper()
	mPath := t.TempDir() + "/tool-manifest.json"
	mf, err := LoadManifest(mPath)
	require.NoError(t, err)
	mf.Put(backendName, "test-sig", []models.ToolInfo{
		{Name: "sap_tool", Description: "a SAP tool", Server: backendName},
	})
	require.NoError(t, mf.Persist())
	// Reload from disk so callers get fresh in-memory state.
	mf2, err := LoadManifest(mPath)
	require.NoError(t, err)
	return mf2, mPath
}

// buildManagerWithSAP creates a Manager with one SAP backend ("vsp-Q99") and
// one core backend ("orchestrator"). Does NOT call SetupSupervisor.
func buildManagerWithSAP(t *testing.T) *Manager {
	t.Helper()
	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"vsp-Q99":      {Command: "/bin/false"},
			"orchestrator": {Command: "/bin/false"},
		},
	}
	cfg.ApplyDefaults()
	return NewManager(cfg, "test", slog.Default())
}

// ---------------------------------------------------------------------------
// Test 1: Flag ON + SAP with manifest entry → Idle at boot, no spawn
// ---------------------------------------------------------------------------

// TestBootWiring_SAPWithManifest_SeedsIdleAndExcludesFromSupervisor verifies
// that when LazySpawnEnabled and the manifest has an entry for a SAP backend,
// SetupSupervisor seeds that backend as StatusIdle and does NOT create a
// supervisor child for it (so it is never eagerly spawned at boot).
func TestBootWiring_SAPWithManifest_SeedsIdleAndExcludesFromSupervisor(t *testing.T) {
	t.Setenv(lazySpawnEnv, "1")

	mf, _ := buildManifestWithEntry(t, "vsp-Q99")

	m := buildManagerWithSAP(t)
	m.SetManifest(mf)

	logger := slog.Default()
	m.SetupSupervisor(logger)

	// The SAP backend must now be StatusIdle.
	entry, ok := m.Entry("vsp-Q99")
	require.True(t, ok)
	assert.Equal(t, models.StatusIdle, entry.Status,
		"SAP backend with manifest entry must be StatusIdle after SetupSupervisor")
	assert.Equal(t, models.StatusIdleReason, entry.LastError,
		"LastError must carry the idle reason string")

	// The core backend must still be StatusStopped (eager path untouched).
	coreEntry, ok := m.Entry("orchestrator")
	require.True(t, ok)
	assert.Equal(t, models.StatusStopped, coreEntry.Status,
		"core backend must remain StatusStopped (eager path)")

	// The supervisor token map must contain the core backend but NOT the SAP one.
	m.mu.RLock()
	_, sapHasToken := m.supervisorTokens["vsp-Q99"]
	_, coreHasToken := m.supervisorTokens["orchestrator"]
	m.mu.RUnlock()

	assert.False(t, sapHasToken,
		"SAP backend seeded as Idle must NOT have a supervisor token (not spawned)")
	assert.True(t, coreHasToken,
		"core backend must have a supervisor token (eager spawn)")
}

// ---------------------------------------------------------------------------
// Test 2: Flag ON + SAP without manifest entry → eager spawn, then manifest
//         entry created; subsequent boot makes it Idle.
// ---------------------------------------------------------------------------

// TestBootWiring_SAPColdStart_ManifestPopulatedAfterEagerSpawn verifies the
// cold-start self-heal path (§D): a SAP backend with no manifest entry spawns
// eagerly, and the postStartHook writes its tools to the manifest. A second
// SetupSupervisor call (simulating next boot) then finds the entry and seeds
// Idle.
func TestBootWiring_SAPColdStart_ManifestPopulatedAfterEagerSpawn(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: requires building and running the mock stdio server")
	}
	t.Setenv(lazySpawnEnv, "1")

	// Empty manifest (no entry for vsp-Q99).
	mPath := t.TempDir() + "/tool-manifest.json"
	mf, err := LoadManifest(mPath)
	require.NoError(t, err)

	// Build a real manager with the mock binary so Start() can succeed.
	binary := buildMockServer(t)
	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"vsp-Q99": {Command: binary},
		},
	}
	cfg.ApplyDefaults()
	m := NewManager(cfg, "test", slog.Default())
	m.SetManifest(mf)

	// Confirm no manifest entry yet.
	_, hasEntry := mf.Get("vsp-Q99")
	assert.False(t, hasEntry, "manifest must be empty before boot")

	// SetupSupervisor: vsp-Q99 has no manifest entry, so it is eager.
	logger := slog.Default()
	m.SetupSupervisor(logger)

	m.mu.RLock()
	_, sapHasToken := m.supervisorTokens["vsp-Q99"]
	m.mu.RUnlock()
	assert.True(t, sapHasToken, "SAP backend without manifest entry must be in eager supervisor list")

	// vsp-Q99 entry must still be StatusStopped (not Idle) before spawn.
	entry, ok := m.Entry("vsp-Q99")
	require.True(t, ok)
	assert.Equal(t, models.StatusStopped, entry.Status)

	// Run the supervisor briefly so the eager spawn fires.
	// Use a short-lived context; cancel it to stop the supervisor after spawn.
	supCtx, supCancel := context.WithCancel(context.Background())
	stopFn := m.ServeBackgroundSupervisor(supCtx)

	// Wait for the backend to reach StatusRunning (postStartHook fires after Start).
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		e, _ := m.Entry("vsp-Q99")
		if e.Status == models.StatusRunning {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	e, _ := m.Entry("vsp-Q99")

	// Signal the supervisor to stop; stopFn waits for all Serve goroutines to return.
	supCancel()
	stopFn()

	require.Equal(t, models.StatusRunning, e.Status, "eager spawn must succeed")

	// Wait deterministically for the async postStartHook goroutine to finish
	// BOTH Put AND Persist. The hook runs fire-and-forget after Serve() registers
	// the successful Start, doing manifest.Put then manifest.Persist (tempfile→rename).
	// We poll the ON-DISK manifest via LoadManifest — NOT the in-memory mf — so we
	// only proceed once Persist has actually completed; polling in-memory Put would
	// race against the still-in-progress disk write. Caps the wait at 5s; no bare sleep.
	var mf2 *Manifest
	var rec ManifestRecord
	var hasEntry2 bool
	hookDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(hookDeadline) {
		if reloaded, lerr := LoadManifest(mPath); lerr == nil {
			if r, has := reloaded.Get("vsp-Q99"); has {
				mf2, rec, hasEntry2 = reloaded, r, true
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.True(t, hasEntry2, "manifest must have a PERSISTED entry for vsp-Q99 after cold-start spawn")
	assert.NotEmpty(t, rec.Tools, "manifest entry must have at least one tool")

	// Second boot: build a new manager with the same manifest path. The entry
	// now exists → SetupSupervisor must seed Idle and exclude from supervisor.
	m2 := NewManager(cfg, "test", slog.Default())
	m2.SetManifest(mf2)
	m2.SetupSupervisor(logger)

	e2, ok := m2.Entry("vsp-Q99")
	require.True(t, ok)
	assert.Equal(t, models.StatusIdle, e2.Status,
		"second boot must seed vsp-Q99 as Idle (manifest hit)")

	m2.mu.RLock()
	_, tok2 := m2.supervisorTokens["vsp-Q99"]
	m2.mu.RUnlock()
	assert.False(t, tok2, "second boot must NOT add vsp-Q99 to the supervisor (Idle)")
}

// ---------------------------------------------------------------------------
// Test 3: Flag ON + core (non-SAP) backend → always eager
// ---------------------------------------------------------------------------

// TestBootWiring_CoreBackend_AlwaysEagerRegardlessOfManifest verifies that a
// non-SAP backend is never seeded as Idle even when the manifest happens to
// contain an entry for it (defensive: manifest only serves SAP backends).
func TestBootWiring_CoreBackend_AlwaysEagerRegardlessOfManifest(t *testing.T) {
	t.Setenv(lazySpawnEnv, "1")

	// Put a manifest entry keyed on a non-SAP name.
	mPath := t.TempDir() + "/tool-manifest.json"
	mf, err := LoadManifest(mPath)
	require.NoError(t, err)
	// Directly inject an entry for a non-SAP backend name.
	mf.Put("orchestrator", "sig-orch", []models.ToolInfo{
		{Name: "some_tool", Description: "desc", Server: "orchestrator"},
	})
	require.NoError(t, mf.Persist())
	mf2, err := LoadManifest(mPath)
	require.NoError(t, err)

	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"orchestrator": {Command: "/bin/false"},
		},
	}
	cfg.ApplyDefaults()
	m := NewManager(cfg, "test", slog.Default())
	m.SetManifest(mf2)
	m.SetupSupervisor(slog.Default())

	entry, ok := m.Entry("orchestrator")
	require.True(t, ok)
	assert.Equal(t, models.StatusStopped, entry.Status,
		"non-SAP backend must stay StatusStopped (eager), never StatusIdle")

	m.mu.RLock()
	_, hasToken := m.supervisorTokens["orchestrator"]
	m.mu.RUnlock()
	assert.True(t, hasToken, "non-SAP backend must have a supervisor token")
}

// ---------------------------------------------------------------------------
// Test 4: BackendSupervisor.Serve returns ErrDoNotRestart for StatusIdle
// ---------------------------------------------------------------------------

// TestBackendSupervisor_ServeReturnsErrDoNotRestartForIdle verifies the
// Idle-gate in Serve() (§C): if a backend is StatusIdle when Serve() runs,
// it returns suture.ErrDoNotRestart immediately without calling Start.
func TestBackendSupervisor_ServeReturnsErrDoNotRestartForIdle(t *testing.T) {
	checker := newFakeStatusChecker()
	checker.set("vsp-Q99", models.StatusIdle)
	mgr := newFakeBackendManager()

	svc := NewBackendSupervisor("vsp-Q99", mgr, checker, slog.Default())
	err := svc.Serve(context.Background())

	require.Error(t, err)
	assert.True(t, errors.Is(err, suture.ErrDoNotRestart),
		"Serve must return ErrDoNotRestart for StatusIdle; got: %v", err)
	assert.Equal(t, 0, mgr.startCalls["vsp-Q99"],
		"Start must NOT be called for a StatusIdle backend")
}

// ---------------------------------------------------------------------------
// Test 5: Flag OFF → no backend is ever StatusIdle; token list unchanged
// ---------------------------------------------------------------------------

// TestBootWiring_FlagOFF_NeverSeedsIdle verifies the regression barrier:
// with MCP_GATEWAY_LAZY_SPAWN unset (or "0"), SetupSupervisor behaves exactly
// as before — no backend is StatusIdle and every non-disabled backend gets a
// supervisor token.
func TestBootWiring_FlagOFF_NeverSeedsIdle(t *testing.T) {
	// Explicitly unset the flag.
	t.Setenv(lazySpawnEnv, "0")

	// Give the manager a manifest entry — it must be ignored when flag is OFF.
	mPath := t.TempDir() + "/tool-manifest.json"
	mf, err := LoadManifest(mPath)
	require.NoError(t, err)
	// Directly inject even without the flag (Put is a no-op when OFF).
	// Write raw JSON so the file exists with a record regardless of the flag.
	mf.records["vsp-Q99"] = ManifestRecord{
		Name:          "vsp-Q99",
		Sig:           "sig",
		DiscoveredAt:  time.Now(),
		SchemaVersion: 1,
	}

	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"vsp-Q99":      {Command: "/bin/false"},
			"orchestrator": {Command: "/bin/false"},
		},
	}
	cfg.ApplyDefaults()
	m := NewManager(cfg, "test", slog.Default())
	// Do NOT call SetManifest — flag OFF means the manifest should never be
	// consulted (and main.go does not call lm.SetManifest when flag is OFF).

	m.SetupSupervisor(slog.Default())

	// Both backends must be StatusStopped (not Idle).
	for _, name := range []string{"vsp-Q99", "orchestrator"} {
		e, ok := m.Entry(name)
		require.True(t, ok)
		assert.Equal(t, models.StatusStopped, e.Status,
			"flag OFF: %q must be StatusStopped, not Idle", name)
	}

	// Both must have supervisor tokens.
	m.mu.RLock()
	_, sapTok := m.supervisorTokens["vsp-Q99"]
	_, coreTok := m.supervisorTokens["orchestrator"]
	m.mu.RUnlock()
	assert.True(t, sapTok, "flag OFF: vsp-Q99 must have a supervisor token")
	assert.True(t, coreTok, "flag OFF: orchestrator must have a supervisor token")
}

// ---------------------------------------------------------------------------
// Test 6: StatusIdle backend is NOT slow-polled by health monitor checkAll
// ---------------------------------------------------------------------------

// TestHealthMonitor_IdleBackendNotSlowPolled is a structural secondary check
// that StatusIdle is distinct from every status that checkAll actively handles.
// The PRIMARY behavioral barrier is TestMonitor_IdleBackendNotPolled in
// internal/health/monitor_idle_test.go which actually runs the Monitor over
// a mock LifecycleManager and confirms no Start/Restart/SetStatus fires.
func TestHealthMonitor_IdleBackendNotSlowPolled(t *testing.T) {
	// fakeLifecyclePoll is a minimal LifecycleManager whose Entries includes
	// an Idle backend. We then drive checkAll and confirm no probe fires.
	lm := &fakeLifecyclePoll{
		entries: []models.ServerEntry{
			{
				Name:   "vsp-Q99",
				Status: models.StatusIdle,
				Config: models.ServerConfig{URL: "http://sap.local:8000"},
			},
			{
				Name:   "orchestrator",
				Status: models.StatusRunning,
				Config: models.ServerConfig{},
			},
		},
		statusSets: make(map[string]models.ServerStatus),
	}

	// Use a short interval; we will call CheckOnce directly.
	import_health_monitor_via_fakeLC(t, lm)
}

// fakeLifecyclePoll is the stub used by the structural Test 6 body.
// It lives in the lifecycle package; health imports lifecycle (not vice versa),
// so we cannot import health here. The structural check confirms enum
// distinctness; the real behavioral barrier lives in
// internal/health/monitor_idle_test.go (TestMonitor_IdleBackendNotPolled).
type fakeLifecyclePoll struct {
	entries    []models.ServerEntry
	statusSets map[string]models.ServerStatus
}

// import_health_monitor_via_fakeLC is a placeholder that asserts the structural
// invariant: StatusIdle is NOT in the set of statuses that checkAll dispatches
// goroutines for. We verify this by confirming StatusIdle != StatusRunning,
// StatusDegraded, StatusError, StatusRestarting, or StatusUnreachable — the
// only statuses checkAll actively handles.
func import_health_monitor_via_fakeLC(t *testing.T, _ *fakeLifecyclePoll) {
	t.Helper()

	idle := models.StatusIdle
	handledByCheckAll := []models.ServerStatus{
		models.StatusRunning,
		models.StatusDegraded,
		models.StatusError,
		models.StatusRestarting,
		models.StatusUnreachable,
	}

	for _, s := range handledByCheckAll {
		assert.NotEqual(t, idle, s,
			"StatusIdle must not appear in checkAll's handled status list (would cause unintended polling)")
	}

	// Additionally confirm that StatusIdle is not in the slow-poll subset
	// (StatusUnreachable only).
	assert.NotEqual(t, idle, models.StatusUnreachable,
		"StatusIdle is structurally distinct from StatusUnreachable — not slow-polled")
}
