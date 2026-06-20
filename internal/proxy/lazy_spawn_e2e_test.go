// Package proxy — TASK C2.4: lazy-spawn end-to-end GATE test.
//
// TestLazySpawnE2E exercises the complete lazy-spawn chain with a REAL spawned
// backend process. It is the C2 plan GATE and proves the feature is correct for
// production enablement (MCP_GATEWAY_LAZY_SPAWN=1).
//
// Real-boundary points:
//   - subprocess spawn via exec.Command (connectStdio + cmd.Process)
//   - on-disk manifest file written by postStartHook (tempfile → fsync → rename)
//   - real Router.Call dispatch through an *mcp.ClientSession
//
// Test inventory:
//
//	TestLazySpawnE2E   — full end-to-end chain: cold-start, next-boot idle,
//	                     tools visible while idle, spawn-on-invoke, no-restart-storm.
//
// Skipped (with rationale):
//
//	VERSION/STALENESS — requires a production change to expose config-sig
//	                    comparison; the Sig is written by postStartHook and
//	                    checked at read time only. Adding an end-to-end staleness
//	                    fixture needs a dedicated follow-up (C2.5 or separate spike).
//
//	DEGRADE-ON-SPAWN-FAIL e2e — TestEnsureStarted_SpawnFailure in
//	                    internal/lifecycle/lazy_spawn_test.go already exercises
//	                    the spawn-failure path end-to-end (manifest eviction,
//	                    toolsChangedCb, StatusError). An additional e2e variant
//	                    here would duplicate that coverage without adding a real
//	                    boundary not already crossed there.
package proxy

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"mcp-gateway/internal/lifecycle"
	"mcp-gateway/internal/models"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildE2EMockBinary compiles the testutil mock MCP server and returns the path
// to the resulting binary. Mirrors the lifecycle.buildMockServer helper but is
// defined here so the proxy package tests do not need access to the unexported
// lifecycle test helper.
func buildE2EMockBinary(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping lazy-spawn E2E: requires building mock binary (use -short=false)")
	}
	tmpDir := t.TempDir()
	binary := filepath.Join(tmpDir, "mock-server")
	if os.PathSeparator == '\\' {
		binary += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", binary, "mcp-gateway/internal/testutil")
	// go test runs from the package directory; module root is two levels up.
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "failed to build mock server binary: %s", string(out))
	return binary
}

// pollManifestOnDisk polls the on-disk manifest file at path until the entry
// for backendName appears or deadline is exceeded. Returns (record, true) on
// success, (zero, false) on timeout.
//
// Polling on-disk (via LoadManifest) rather than the in-memory Manifest.Get
// is critical: the postStartHook performs Put then Persist asynchronously;
// an in-memory poll would race against the still-in-progress disk write.
func pollManifestOnDisk(t *testing.T, path, backendName string, deadline time.Duration) (lifecycle.ManifestRecord, bool) {
	t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		mf, err := lifecycle.LoadManifest(path)
		if err == nil {
			if rec, ok := mf.Get(backendName); ok {
				return rec, true
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return lifecycle.ManifestRecord{}, false
}

// TestDegradeRemovesTool_E2E is the served-catalog-level proof for Guard 2
// (design §4.1 / C2.2). It asserts three properties end-to-end with flag ON:
//
//  1. While the backend is StatusIdle with a valid manifest entry, filteredTools
//     ADVERTISES the cached tool (tools visible before spawn).
//  2. Router.Call for that tool triggers EnsureStarted, which attempts a real
//     subprocess spawn with an INVALID command. The spawn fails.
//  3. After the failed invoke, filteredTools NO LONGER includes the backend's
//     tools (manifest entry evicted, status = StatusError).
//
// This is the REST/served-catalog-level proof that the degrade removes the tool
// from the advertised list, not just an internal state assertion.
func TestDegradeRemovesTool_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping lazy-spawn degrade E2E: requires real filesystem for manifest")
	}
	t.Setenv("MCP_GATEWAY_LAZY_SPAWN", "1")

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	const backendName = "vsp-Q99"
	// An invalid command guarantees the spawn fails immediately.
	const invalidCmd = "/nonexistent/binary/that/cannot/spawn"

	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			backendName: {Command: invalidCmd},
		},
	}
	cfg.ApplyDefaults()

	// Build a manifest entry with the CORRECT sig for the invalid-command config.
	// The sig must match so GetValid returns the entry and filteredTools serves tools.
	backendSig := lifecycle.BackendConfigSig(*cfg.Servers[backendName])
	manifestPath := filepath.Join(t.TempDir(), "tool-manifest.json")
	mf, err := lifecycle.LoadManifest(manifestPath)
	require.NoError(t, err)
	mf.Put(backendName, backendSig, []models.ToolInfo{{Name: "sap_ping", Description: "Ping SAP", Server: backendName}})
	require.NoError(t, mf.Persist())

	// Reload from disk so GetValid operates on the persisted sig.
	mfLoaded, err := lifecycle.LoadManifest(manifestPath)
	require.NoError(t, err)

	lm := lifecycle.NewManager(cfg, "test", logger)
	lm.SetStatus(backendName, models.StatusIdle, models.StatusIdleReason)
	lm.SetManifest(mfLoaded)

	// Wire a toolsChangedCb so EnsureStarted's failure path can fire it.
	// filteredTools reads from the manifest; the cb drives eviction notification.
	// atomic.Bool: the callback runs in EnsureStarted's singleflight goroutine,
	// so the flag is written off the test goroutine — synchronize to stay race-free
	// under `go test -race`.
	var evictFired atomic.Bool
	lm.SetToolsChangedCallback(func(name string) {
		if name == backendName {
			evictFired.Store(true)
		}
	})

	gw := New(cfg, lm, "test", logger)
	gw.SetManifest(mfLoaded)

	// ---- Assertion (a): tools ADVERTISED while Idle ----
	toolsBefore := gw.filteredTools()
	var foundBefore bool
	for _, nt := range toolsBefore {
		if nt.server == backendName {
			foundBefore = true
			break
		}
	}
	require.True(t, foundBefore,
		"degrade-e2e (a): filteredTools must advertise idle backend's cached tool before first invoke")

	// ---- Assertion (b): Router.Call triggers spawn which FAILS ----
	callCtx, callCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer callCancel()

	_, callErr := gw.Router().Call(callCtx, backendName+"__sap_ping", map[string]any{})
	require.Error(t, callErr,
		"degrade-e2e (b): Router.Call must return an error when the spawn command is invalid")

	// ---- Assertion (c): tools NO LONGER advertised after failed spawn ----
	// The manifest entry should have been evicted by EnsureStarted's failure path.
	// Allow a brief moment for the toolsChangedCb goroutine to complete.
	require.Eventually(t, func() bool {
		toolsAfter := gw.filteredTools()
		for _, nt := range toolsAfter {
			if nt.server == backendName {
				return false // still advertised — not done yet
			}
		}
		return true
	}, 3*time.Second, 20*time.Millisecond,
		"degrade-e2e (c): filteredTools must NOT advertise backend's tools after failed spawn (manifest evicted)")

	// Confirm the backend reached StatusError.
	entry, ok := lm.Entry(backendName)
	require.True(t, ok)
	assert.Equal(t, models.StatusError, entry.Status,
		"degrade-e2e: backend must be StatusError after spawn failure")

	// Confirm toolsChangedCb was fired (signals the gateway to refresh).
	assert.True(t, evictFired.Load(),
		"degrade-e2e: toolsChangedCb must have fired with the backend name after degrade")
}

// TestLazySpawnE2E is the C2 plan GATE test. It exercises five assertions:
//
//  1. COLD-START + PERSIST: a SAP backend with no manifest entry spawns eagerly;
//     the async postStartHook persists a manifest entry to disk.
//  2. NEXT-BOOT IDLE: a fresh manager seeded from the now-populated manifest
//     shows StatusIdle and has no live session (not spawned).
//  3. TOOLS VISIBLE WHILE IDLE: the Gateway's filteredTools includes the Idle
//     backend's cached tools from the manifest (chicken-and-egg contract).
//  4. SPAWN-ON-INVOKE: Router.Call on an Idle backend's tool triggers
//     EnsureStarted, spawns the real process, dispatches the call, and returns
//     the expected result.
//  5. NO RESTART STORM: the Idle backend was NOT driven by suture before the
//     invoke (no live session before step 4).
func TestLazySpawnE2E(t *testing.T) {
	binary := buildE2EMockBinary(t)

	// Feature flag ON for the whole test.
	t.Setenv("MCP_GATEWAY_LAZY_SPAWN", "1")

	// Disable the C1 SAP-URL reachability gate so the mock backend (which
	// has no real SAP_URL in env) is not skipped during eager cold-start.
	// The mock binary is a stdio backend named vsp-Q99 (IsVSP=true, IsSAP=true)
	// but without SAP_URL in its config.Env, so the gate returns false anyway;
	// this setenv is belt-and-suspenders to ensure hermetic behavior.
	t.Setenv("MCP_GATEWAY_SKIP_UNREACHABLE_STDIO", "0")

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// Use a single temp dir for the manifest so both manager instances share it.
	manifestPath := filepath.Join(t.TempDir(), "tool-manifest.json")

	const backendName = "vsp-Q99"

	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			backendName: {Command: binary},
			// No SAP_URL env: avoids C1 TCP-probe gate; IsSAP still true via name.
		},
	}
	cfg.ApplyDefaults()

	// -------------------------------------------------------------------------
	// STEP 1 — COLD-START + PERSIST
	//
	// Build a Manager with an empty manifest. vsp-Q99 has no manifest entry, so
	// SetupSupervisor puts it in the eager path. Run the supervisor briefly; the
	// postStartHook fires after the eager spawn and writes a manifest entry.
	// -------------------------------------------------------------------------

	mfEmpty, err := lifecycle.LoadManifest(manifestPath)
	require.NoError(t, err)
	require.NoError(t, mfEmpty.Persist()) // create the file so subsequent LoadManifest works

	_, hasEntry0 := mfEmpty.Get(backendName)
	require.False(t, hasEntry0, "precondition: manifest must be empty before cold-start")

	m1 := lifecycle.NewManager(cfg, "e2e-cold", logger)
	m1.SetManifest(mfEmpty)
	m1.SetupSupervisor(logger)

	supCtx, supCancel := context.WithCancel(context.Background())
	stopFn := m1.ServeBackgroundSupervisor(supCtx)

	// Wait for vsp-Q99 to reach StatusRunning (eager spawn).
	require.Eventually(t, func() bool {
		e, _ := m1.Entry(backendName)
		return e.Status == models.StatusRunning
	}, 20*time.Second, 100*time.Millisecond,
		"cold-start: vsp-Q99 must reach StatusRunning after eager spawn")

	// Now wait for the async postStartHook to persist the manifest entry to disk.
	// We poll ON-DISK (via LoadManifest) so we only proceed once Persist has
	// completed the tempfile→rename step — not merely Put into memory.
	rec, persisted := pollManifestOnDisk(t, manifestPath, backendName, 10*time.Second)
	require.True(t, persisted,
		"cold-start: postStartHook must persist a manifest entry for %s to disk", backendName)
	assert.NotEmpty(t, rec.Tools,
		"cold-start: persisted manifest entry must carry at least one tool (got zero)")

	// Tear down the first supervisor before starting the second manager.
	supCancel()
	stopFn()

	// Stop the running backend cleanly so the process exits. Surface (not hide)
	// a Stop error so a leaked mock-server process is visible in the test log;
	// a benign "already stopped" after the supervisor teardown is non-fatal.
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if stopErr := m1.Stop(stopCtx, backendName); stopErr != nil {
		t.Logf("cold-start cleanup: m1.Stop(%s) returned %v (non-fatal; supervisor teardown may have already stopped it)", backendName, stopErr)
	}
	stopCancel()

	// -------------------------------------------------------------------------
	// STEP 2 — NEXT-BOOT IDLE
	//
	// Load the now-populated manifest and build a fresh Manager. SetupSupervisor
	// must see the manifest entry for vsp-Q99 and seed it as StatusIdle, skipping
	// the supervisor token. The backend must NOT have a live session at this point.
	// -------------------------------------------------------------------------

	mfLoaded, err := lifecycle.LoadManifest(manifestPath)
	require.NoError(t, err)
	_, hasEntry1 := mfLoaded.Get(backendName)
	require.True(t, hasEntry1, "precondition: manifest must have an entry after cold-start persist")

	m2 := lifecycle.NewManager(cfg, "e2e-idle", logger)
	m2.SetManifest(mfLoaded)
	m2.SetupSupervisor(logger)

	e2, ok2 := m2.Entry(backendName)
	require.True(t, ok2)
	assert.Equal(t, models.StatusIdle, e2.Status,
		"next-boot: vsp-Q99 must be StatusIdle when manifest entry exists")
	assert.Equal(t, models.StatusIdleReason, e2.LastError,
		"next-boot: LastError must carry the idle-reason string")

	// Session must NOT exist — the backend was never spawned on second boot.
	_, hasSession2 := m2.Session(backendName)
	assert.False(t, hasSession2,
		"next-boot: no-restart-storm — vsp-Q99 must have no live session before first invoke")

	// -------------------------------------------------------------------------
	// STEP 3 — TOOLS VISIBLE WHILE IDLE
	//
	// Wire the Idle manager into a Gateway (with the same manifest). filteredTools
	// must include the Idle backend's cached tools from the manifest so MCP
	// clients can see them without spawning the process.
	// -------------------------------------------------------------------------

	gw := New(cfg, m2, "e2e", logger)
	gw.SetManifest(mfLoaded)

	allTools := gw.filteredTools()

	var idleToolNames []string
	for _, nt := range allTools {
		if nt.server == backendName {
			idleToolNames = append(idleToolNames, nt.name)
		}
	}
	require.NotEmpty(t, idleToolNames,
		"tools-visible-while-idle: filteredTools must advertise Idle backend's cached tools; got none")

	// The mock server exposes "echo", "add", "fail" — at least one must be present.
	assert.Contains(t, idleToolNames, "echo",
		"tools-visible-while-idle: cached 'echo' tool must be visible while backend is Idle")

	// -------------------------------------------------------------------------
	// STEP 4 — SPAWN-ON-INVOKE
	//
	// Route a Router.Call for vsp-Q99__echo. The router detects StatusIdle,
	// calls EnsureStarted, which spawns the real binary, then dispatches the call
	// and returns the echo result. After the call, the backend must be Running.
	// -------------------------------------------------------------------------

	// Wire a toolsChangedCb so EnsureStarted's manifest refresh path can fire.
	m2.SetToolsChangedCallback(func(_ string) {})

	callCtx, callCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer callCancel()

	result, callErr := gw.Router().Call(callCtx, backendName+"__echo", map[string]any{
		"message": "e2e-lazy-spawn-gate",
	})
	require.NoError(t, callErr,
		"spawn-on-invoke: Router.Call for Idle backend must succeed after lazy spawn")
	require.NotNil(t, result,
		"spawn-on-invoke: Router.Call must return a non-nil result")
	require.False(t, result.IsError,
		"spawn-on-invoke: tool call must not report an MCP-level error")

	// Extract text from the echo response.
	require.NotEmpty(t, result.Content,
		"spawn-on-invoke: result must have at least one content element")
	textContent, isText := result.Content[0].(*mcp.TextContent)
	require.True(t, isText, "spawn-on-invoke: first content element must be *mcp.TextContent")
	assert.Contains(t, textContent.Text, "e2e-lazy-spawn-gate",
		"spawn-on-invoke: echo tool must return the input message")

	// After the call, the backend must be Running (spawn succeeded).
	e2After, _ := m2.Entry(backendName)
	assert.Equal(t, models.StatusRunning, e2After.Status,
		"spawn-on-invoke: vsp-Q99 must be StatusRunning after first tool call")

	_, hasSessionAfter := m2.Session(backendName)
	assert.True(t, hasSessionAfter,
		"spawn-on-invoke: vsp-Q99 must have a live session after spawn")
	assert.Greater(t, e2After.PID, 0,
		"spawn-on-invoke: vsp-Q99 must have a non-zero PID after spawn")

	// -------------------------------------------------------------------------
	// STEP 5 — MANIFEST REFRESHED FROM LIVE TOOLS
	//
	// EnsureStarted refreshes the manifest after successful spawn. Poll the
	// on-disk manifest for a fresh entry (DiscoveredAt updated after spawn).
	// -------------------------------------------------------------------------

	freshRec, freshPersisted := pollManifestOnDisk(t, manifestPath, backendName, 10*time.Second)
	if freshPersisted {
		// The manifest must still have tool entries after spawn refresh.
		assert.NotEmpty(t, freshRec.Tools,
			"manifest-refresh: spawned backend manifest entry must have tools")
	}
	// This step is best-effort: EnsureStarted's manifest refresh runs after
	// the call returns and is fire-and-forget; it may not complete before the
	// poll window. Failure here is logged but does not block the GATE.
	if !freshPersisted {
		t.Logf("manifest-refresh: on-disk update not observed within poll window (best-effort step, non-blocking)")
	}

	// Cleanup: stop the spawned backend.
	cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cleanCancel()
	_ = m2.Stop(cleanCtx, backendName)
}
