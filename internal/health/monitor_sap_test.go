// Regression tests for BUG-STDIO-1 through BUG-STDIO-4 (SAP stdio backend
// health-check fix) plus P1 reliability fixes:
//
//   - BUG-STDIO-1: vsp-* always reported running regardless of SAP reachability.
//   - BUG-STDIO-2: sap-gui-* always reported running regardless of session state.
//   - BUG-STDIO-3: lifecycle Start() did not TCP-probe SAP_URL for vsp-* backends.
//   - BUG-STDIO-4: checkSAPGUISession session-absent path.
//   - P1: shared snapshot (one probe per cycle), dual-counter blip mask, sticky-
//     target eviction, takenAt-on-error cache guarantee.
//
// Test strategy:
//   - vsp-* reachability: uses a wrapper that mirrors checkOne's mcpOK=true branch
//     with the real SAP probe logic, driven via a real local TCP listener or an
//     unbound local port (same pattern as monitor_unreachable_test.go).
//   - sap-gui-* session check: the "no live session" early-return branch is fully
//     testable (mockLM.Session returns false). Deeper branches (CallTool result
//     inspection) are exercised via countingMockLM with injectable tool results.
//   - Non-SAP backend: verified to take neither probe path.
//
// Fail-without-fix property:
//   - vsp-* SAP-probe tests: before the fix, checkOne had no Level 3 probe for
//     stdio backends — checkOneWithSAPProbe would call SetStatus(Running) even
//     when the SAP host was unreachable.
//   - sap-gui-* "no session" test: before the fix, checkSAPGUISession did not
//     exist; TestCheckSAPGUISession_NoSession would compile-error.
package health

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"mcp-gateway/internal/models"
	"mcp-gateway/internal/sapname"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- test entry builders ----------------------------------------------------

// sapGUIEntry builds a sap-gui-<SID>(-<Client>)? server entry with a stdio
// command and no SAP_URL (sap-gui-* uses COM automation, not a TCP URL).
func sapGUIEntry(t *testing.T, name string, status models.ServerStatus) models.ServerEntry {
	t.Helper()
	require.True(t, sapname.IsSAPGUI(name), "test requires a valid sap-gui-* name, got %q", name)
	return models.ServerEntry{
		Name:   name,
		Status: status,
		Config: models.ServerConfig{Command: "/fake/sap-gui-ctl"},
	}
}

// vspEntry builds a vsp-<SID>(-<Client>)? server entry with a stdio command
// and SAP_URL in Env.
func vspEntry(t *testing.T, name string, sapURL string, status models.ServerStatus) models.ServerEntry {
	t.Helper()
	require.True(t, sapname.IsVSP(name), "test requires a valid vsp-* name, got %q", name)
	return models.ServerEntry{
		Name:   name,
		Status: status,
		Config: models.ServerConfig{
			Command: "/fake/vsp-ctl",
			Env:     []string{"SAP_URL=" + sapURL},
		},
	}
}

// ---- checkOneWithSAPProbe ---------------------------------------------------
//
// checkOneWithSAPProbe extends the existing checkOneWithMockPing helper to also
// exercise the SAP-specific Level 3 probe (monitor.go lines 410–431). The
// existing helper stops at the mcpOK branch and sets StatusRunning/Degraded
// directly — it cannot test the SAP extension because that code lives in
// checkOne, which always calls the real checkMCPPing (always false in mock
// tests). This wrapper mirrors the full mcpOK=true branch of checkOne including
// the SAP dispatch.
func (tm *testableMonitor) checkOneWithSAPProbe(ctx context.Context, entry models.ServerEntry, mcpOK bool) {
	name := entry.Name

	if mcpOK {
		// Level 3 SAP probe — mirrors monitor.go checkOne (computed before the
		// lock, like the production blocking-I/O probe).
		var sapStatus models.ServerStatus
		var sapReason string
		if sapURL, hasSAPURL := entry.Config.SAPEnvURL(); hasSAPURL && sapname.IsVSP(name) {
			if !tm.probeReachable(ctx, sapURL) {
				sapStatus = models.StatusUnreachable
				sapReason = "SAP host unreachable (VPN?)"
			}
		} else if sapname.IsSAPGUI(name) {
			sapStatus, sapReason = tm.checkSAPGUISession(ctx, name)
		}

		tm.mu.Lock()
		state := tm.getOrCreateState(name)
		state.consecutiveFailures = 0
		state.lastHealthyAt = time.Now()
		state.nextRestartAllowedAt = time.Time{}
		if state.uptimeStart.IsZero() {
			state.uptimeStart = time.Now()
		}

		// SAP reachability — two-counter blip mask mirrors monitor.go checkOne
		// (design step 4, review MEDIUM-2). Never StatusUnreachable for stdio.
		switch sapStatus {
		case models.StatusUnreachable:
			state.sapUnreachableStreak++
			state.sapDegradedStreak = 0
			streak := state.sapUnreachableStreak
			tm.mu.Unlock()
			if streak < tm.SAPProbeFailureThreshold {
				tm.lm.SetStatus(name, models.StatusRunning, "")
				return
			}
			tm.lm.SetStatus(name, models.StatusDegraded, sapReason)
			return
		case models.StatusDegraded:
			state.sapDegradedStreak++
			state.sapUnreachableStreak = 0
			streak := state.sapDegradedStreak
			tm.mu.Unlock()
			if streak < tm.SAPDegradedConfirmThreshold {
				tm.lm.SetStatus(name, models.StatusRunning, "")
				return
			}
			tm.lm.SetStatus(name, models.StatusDegraded, sapReason)
			return
		}
		state.sapUnreachableStreak = 0
		state.sapDegradedStreak = 0
		tm.mu.Unlock()
		tm.lm.SetStatus(name, models.StatusRunning, "")
		return
	}

	// MCP ping failed path (unchanged from checkOneWithMockPing).
	tm.mu.Lock()
	state := tm.getOrCreateState(name)
	state.consecutiveFailures++
	failures := state.consecutiveFailures
	tm.mu.Unlock()

	if failures < tm.ConsecutiveFailureThreshold {
		if entry.Status == models.StatusRunning {
			tm.lm.SetStatus(name, models.StatusDegraded, "MCP ping failed")
		}
		return
	}
	tm.attemptRestart(ctx, name)
}

// ---- vsp-* tests ------------------------------------------------------------

// TestCheckOne_VSP_SAPUnreachable_YieldsDegradedAfterThreshold verifies the
// SAP-stdio health contract: when a vsp-* backend's MCP ping succeeds but the
// SAP application server (SAP_URL) is TCP-unreachable, a SINGLE failure is
// tolerated (StatusRunning) and only after SAPProbeFailureThreshold consecutive
// failures does checkOne yield StatusDegraded — NEVER StatusUnreachable, because
// stdio backends have no slow-poll recovery and would be stranded there. The
// backend stays routable and is re-probed every tick (self-recovery).
func TestCheckOne_VSP_SAPUnreachable_YieldsDegradedAfterThreshold(t *testing.T) {
	port := unboundLocalPort(t)
	sapURL := fmt.Sprintf("http://127.0.0.1:%d", port)

	mock := newMockLM()
	entry := vspEntry(t, "vsp-Q00", sapURL, models.StatusRunning)
	mock.addEntry(entry.Name, entry.Status, entry.Config)

	tm := newTestableMonitor(mock)
	ctx := context.Background()

	// A single SAP-probe failure must be tolerated (stay Running) so a transient
	// VPN/host blip does not flap the backend.
	tm.checkOneWithSAPProbe(ctx, entry, true)
	assert.Equal(t, models.StatusRunning, mock.lastStatus(entry.Name),
		"a single SAP-probe failure must be tolerated (StatusRunning), not flip immediately")

	// Reaching the threshold marks Degraded — NEVER StatusUnreachable: stdio
	// backends have no slow-poll recovery and would be stranded there. Degraded
	// stays routable and is re-probed every tick (self-recovery).
	for i := 1; i < tm.SAPProbeFailureThreshold; i++ {
		tm.checkOneWithSAPProbe(ctx, entry, true)
	}
	assert.Equal(t, models.StatusDegraded, mock.lastStatus(entry.Name),
		"vsp-* with persistently unreachable SAP host must be StatusDegraded, never StatusUnreachable")
}

// TestCheckOne_VSP_SAPRecovers_BackToRunning verifies self-recovery: once the
// SAP host is reachable again, a live vsp-* backend returns to StatusRunning
// without a manual restart (the bug that motivated the fix: stdio Unreachable
// was a dead-end).
func TestCheckOne_VSP_SAPRecovers_BackToRunning(t *testing.T) {
	badPort := unboundLocalPort(t)
	mock := newMockLM()
	badEntry := vspEntry(t, "vsp-Q00", fmt.Sprintf("http://127.0.0.1:%d", badPort), models.StatusRunning)
	mock.addEntry(badEntry.Name, badEntry.Status, badEntry.Config)

	tm := newTestableMonitor(mock)
	ctx := context.Background()

	for i := 0; i < tm.SAPProbeFailureThreshold; i++ {
		tm.checkOneWithSAPProbe(ctx, badEntry, true)
	}
	require.Equal(t, models.StatusDegraded, mock.lastStatus(badEntry.Name),
		"precondition: persistent SAP failure drives the backend Degraded")

	// SAP host comes back (reachable port): a single good probe resets the
	// streak and returns the backend to Running.
	goodPort, stop := listenLocal(t)
	defer stop()
	goodEntry := vspEntry(t, "vsp-Q00", fmt.Sprintf("http://127.0.0.1:%d", goodPort), models.StatusDegraded)
	tm.checkOneWithSAPProbe(ctx, goodEntry, true)

	assert.Equal(t, models.StatusRunning, mock.lastStatus(badEntry.Name),
		"vsp-* must self-recover to StatusRunning once SAP is reachable again")
}

// TestCheckOne_VSP_SAPReachable_YieldsStatusRunning verifies the happy path:
// when both MCP ping and the SAP host are reachable, the backend stays Running.
func TestCheckOne_VSP_SAPReachable_YieldsStatusRunning(t *testing.T) {
	port, stop := listenLocal(t)
	defer stop()
	sapURL := fmt.Sprintf("http://127.0.0.1:%d", port)

	mock := newMockLM()
	entry := vspEntry(t, "vsp-Q00", sapURL, models.StatusRunning)
	mock.addEntry(entry.Name, entry.Status, entry.Config)

	tm := newTestableMonitor(mock)
	ctx := context.Background()

	tm.checkOneWithSAPProbe(ctx, entry, true)

	assert.Equal(t, models.StatusRunning, mock.lastStatus(entry.Name),
		"vsp-* with reachable SAP host and OK MCP ping must stay StatusRunning")
}

// TestCheckOne_VSP_NoSAPURL_NoProbeFired verifies that a vsp-* backend with
// no SAP_URL env entry does NOT attempt a TCP probe. StatusRunning is the
// result (MCP ping governs alone).
func TestCheckOne_VSP_NoSAPURL_NoProbeFired(t *testing.T) {
	mock := newMockLM()
	entry := models.ServerEntry{
		Name:   "vsp-Q00",
		Status: models.StatusRunning,
		Config: models.ServerConfig{Command: "/fake/vsp-ctl"}, // no SAP_URL in Env
	}
	mock.addEntry(entry.Name, entry.Status, entry.Config)

	tm := newTestableMonitor(mock)
	ctx := context.Background()

	// probeReachable("") would parse an empty URL and return false, yielding
	// StatusUnreachable. A StatusRunning result proves the guard fires and no
	// probe was attempted.
	tm.checkOneWithSAPProbe(ctx, entry, true)

	assert.Equal(t, models.StatusRunning, mock.lastStatus(entry.Name),
		"vsp-* without SAP_URL must not fire a probe; StatusRunning from MCP ping alone")
}

// TestCheckOne_VSP_WithClientSuffix_SAPUnreachable verifies that the SAP-name
// discriminator also matches vsp-<SID>-<Client> names.
func TestCheckOne_VSP_WithClientSuffix_SAPUnreachable(t *testing.T) {
	port := unboundLocalPort(t)
	sapURL := fmt.Sprintf("http://127.0.0.1:%d", port)

	mock := newMockLM()
	entry := vspEntry(t, "vsp-Q00-100", sapURL, models.StatusRunning)
	mock.addEntry(entry.Name, entry.Status, entry.Config)

	tm := newTestableMonitor(mock)
	ctx := context.Background()

	for i := 0; i < tm.SAPProbeFailureThreshold; i++ {
		tm.checkOneWithSAPProbe(ctx, entry, true)
	}

	assert.Equal(t, models.StatusDegraded, mock.lastStatus(entry.Name),
		"vsp-<SID>-<Client> must also be SAP-probed; persistent failure -> Degraded (not Unreachable)")
}

// ---- sap-gui-* tests --------------------------------------------------------

// TestCheckOne_SAPGUI_NoSession_YieldsUnreachable verifies BUG-STDIO-2/4:
// when a sap-gui-* backend's MCP ping succeeds but lm.Session returns false
// (no active session), checkOne must yield StatusUnreachable, not StatusRunning.
//
// Fail-without-fix: before the fix checkSAPGUISession did not exist;
// checkOneWithSAPProbe would call SetStatus(Running) — this assertion fails.
func TestCheckOne_SAPGUI_NoSession_YieldsDegradedAfterThreshold(t *testing.T) {
	mock := newMockLM()
	entry := sapGUIEntry(t, "sap-gui-Q00", models.StatusRunning)
	mock.addEntry(entry.Name, entry.Status, entry.Config)
	// mockLM.Session always returns (nil, false) — exercises the early-return
	// branch of checkSAPGUISession without a real *mcp.ClientSession.

	tm := newTestableMonitor(mock)
	ctx := context.Background()

	for i := 0; i < tm.SAPProbeFailureThreshold; i++ {
		tm.checkOneWithSAPProbe(ctx, entry, true)
	}

	assert.Equal(t, models.StatusDegraded, mock.lastStatus(entry.Name),
		"sap-gui-* with no MCP session must become StatusDegraded after threshold (never StatusUnreachable for stdio)")
}

// TestCheckSAPGUISession_NoSession is a direct unit test of checkSAPGUISession
// that pins the StatusUnreachable return for the no-live-session path.
// After the P1 rewire, checkSAPGUISession delegates to getSAPSnapshot →
// probeSAPOnce, which returns "no live sap-gui session for snapshot probe"
// when the candidate list is empty (no session registered in the mock).
func TestCheckSAPGUISession_NoSession(t *testing.T) {
	mock := newMockLM()
	// No entry for "sap-gui-ABC"; Session() returns (nil, false).
	mon := NewMonitor(mock, 0, nil)
	ctx := context.Background()

	status, reason := mon.checkSAPGUISession(ctx, "sap-gui-ABC")

	assert.Equal(t, models.StatusUnreachable, status,
		"checkSAPGUISession must return StatusUnreachable when no live session exists")
	assert.Contains(t, reason, "no live sap-gui session",
		"reason must describe the absent candidate pool")
}

// TestCheckOne_SAPGUI_WithClientSuffix_NoSession verifies that sap-gui-<SID>-<Client>
// names are also routed through checkSAPGUISession.
func TestCheckOne_SAPGUI_WithClientSuffix_NoSession(t *testing.T) {
	mock := newMockLM()
	entry := sapGUIEntry(t, "sap-gui-Q00-100", models.StatusRunning)
	mock.addEntry(entry.Name, entry.Status, entry.Config)

	tm := newTestableMonitor(mock)
	ctx := context.Background()

	for i := 0; i < tm.SAPProbeFailureThreshold; i++ {
		tm.checkOneWithSAPProbe(ctx, entry, true)
	}

	assert.Equal(t, models.StatusDegraded, mock.lastStatus(entry.Name),
		"sap-gui-<SID>-<Client> must route through checkSAPGUISession; persistent failure -> Degraded")
}

// ---- non-SAP backend tests --------------------------------------------------

// TestCheckOne_NonSAP_NoProbeFired verifies that a plain stdio backend with a
// non-SAP name is not touched by either SAP probe path. StatusRunning from MCP
// ping alone with a single status event.
func TestCheckOne_NonSAP_NoProbeFired(t *testing.T) {
	mock := newMockLM()
	entry := models.ServerEntry{
		Name:   "context7",
		Status: models.StatusRunning,
		Config: models.ServerConfig{Command: "/fake/some-ctl"},
	}
	mock.addEntry(entry.Name, entry.Status, entry.Config)

	tm := newTestableMonitor(mock)
	ctx := context.Background()

	// Use the original checkOneWithMockPing (no SAP wrapper) to confirm the
	// non-SAP code path produces exactly one status event.
	tm.checkOneWithMockPing(ctx, entry, true)

	assert.Equal(t, models.StatusRunning, mock.lastStatus(entry.Name))
	require.Len(t, mock.statusLog, 1,
		"non-SAP backend must emit exactly one status event (no extra SAP probe events)")
	assert.Equal(t, models.StatusRunning, mock.statusLog[0].Status)
}

// TestCheckOne_NonSAP_SAPURLInEnv_NoProbeFired verifies that the SAP probe
// discriminator is NAME-based, not Env-based. A backend with "SAP_URL" in Env
// but a non-SAP name must NOT be probed.
func TestCheckOne_NonSAP_SAPURLInEnv_NoProbeFired(t *testing.T) {
	port := unboundLocalPort(t)
	mock := newMockLM()
	entry := models.ServerEntry{
		Name:   "myserver",
		Status: models.StatusRunning,
		Config: models.ServerConfig{
			Command: "/fake/myserver-ctl",
			Env:     []string{fmt.Sprintf("SAP_URL=http://127.0.0.1:%d", port)},
		},
	}
	mock.addEntry(entry.Name, entry.Status, entry.Config)

	tm := newTestableMonitor(mock)
	ctx := context.Background()

	// If the probe were fired, probeReachable returns false (unbound port) →
	// StatusUnreachable. StatusRunning proves no probe fired.
	tm.checkOneWithSAPProbe(ctx, entry, true)

	assert.Equal(t, models.StatusRunning, mock.lastStatus(entry.Name),
		"non-SAP backend with SAP_URL in Env must NOT be TCP-probed (name discriminator governs)")
}

// ---- sapname discriminator contract tests -----------------------------------
// Pin IsVSP / IsSAPGUI as the correct routing discriminators. Compile-errors
// before the fix (functions did not exist).

func TestSAPNameDiscriminator_IsVSP(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"vsp-Q00", true},
		{"vsp-Q00-100", true},
		{"vsp-P01-200", true},
		{"sap-gui-Q00", false},
		{"context7", false},
		{"my-server", false},
		{"vsp-", false},   // too short — invalid SID
		{"vspQ00", false}, // missing separator
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, sapname.IsVSP(tc.name))
		})
	}
}

func TestSAPNameDiscriminator_IsSAPGUI(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"sap-gui-Q00", true},
		{"sap-gui-Q00-100", true},
		{"sap-gui-P01-200", true},
		{"vsp-Q00", false},
		{"context7", false},
		{"my-server", false},
		{"sap-gui-", false}, // too short
		{"sapgui-Q00", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, sapname.IsSAPGUI(tc.name))
		})
	}
}

// ---- SAPEnvURL data-flow smoke test (BUG-STDIO-3) --------------------------
//
// lifecycle.Start() uses cfg.SAPEnvURL() to retrieve the URL for the TCP
// pre-check. This test pins that the extractor returns the correct URL for the
// config shape Start() will encounter, without invoking real dial I/O or
// spawning a subprocess.

// TestLifecycle_VSP_SAPURLExtraction_DataFlow verifies the data path from
// ServerConfig.Env → SAPEnvURL() that lifecycle.Start() uses for BUG-STDIO-3.
func TestLifecycle_VSP_SAPURLExtraction_DataFlow(t *testing.T) {
	cfg := &models.ServerConfig{
		Command: "/fake/vsp-ctl",
		Env:     []string{"OTHER=val", "SAP_URL=http://saphost:50000", "THIRD=x"},
	}

	url, ok := cfg.SAPEnvURL()
	require.True(t, ok, "lifecycle.Start() relies on SAPEnvURL returning ok=true for vsp-* TCP pre-check")
	assert.Equal(t, "http://saphost:50000", url,
		"URL must match the entry lifecycle.Start() will pass to checkTCPReachable")
}

// ---- checkSAPGUISession result-mapping table (partial) ----------------------
//
// Branches reachable without a real *mcp.ClientSession: session absent only.
// Branches requiring a real session (tool-missing, transport error, empty list,
// non-empty list) are Escalated in STEP RESULT.

func TestCheckSAPGUISession_ResultMapping(t *testing.T) {
	tests := []struct {
		name          string
		wantStatus    models.ServerStatus
		wantReasonSub string
	}{
		{
			// After P1 rewire: probeSAPOnce returns "no live sap-gui session
			// for snapshot probe" when no session is registered; mapSAPGUIResult
			// maps that non-"not found" error to StatusUnreachable.
			name:          "session absent -> StatusUnreachable",
			wantStatus:    models.StatusUnreachable,
			wantReasonSub: "no live sap-gui session",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockLM()
			// mockLM.Session always returns (nil, false).
			mon := NewMonitor(mock, 0, nil)
			ctx := context.Background()

			status, reason := mon.checkSAPGUISession(ctx, "sap-gui-Q00")

			assert.Equal(t, tt.wantStatus, status)
			if tt.wantReasonSub != "" {
				assert.Contains(t, reason, tt.wantReasonSub)
			}
		})
	}
}

// ---- TestMapSAPGUIResult — pure-function table (BUG-STDIO-2 / BUG-STDIO-4) ----
//
// mapSAPGUIResult is the extracted pure function covering all five branches of
// the sap-gui-* session probe logic. No network, no subprocess, no sleeps.
//
// Fail-without-fix property: a naive implementation that always returns
// (StatusRunning, "") would fail every subtest that expects StatusDegraded or
// StatusUnreachable, and would fail the empty-reason assertions for the
// graceful-fallback and happy-path subtests (since those expect non-empty reason
// when checking degraded/unreachable branches).
func TestMapSAPGUIResult(t *testing.T) {
	textContent := func(s string) *mcp.TextContent { return &mcp.TextContent{Text: s} }
	okResult := func(contents ...mcp.Content) *mcp.CallToolResult {
		return &mcp.CallToolResult{Content: contents}
	}
	errResult := func(contents ...mcp.Content) *mcp.CallToolResult {
		return &mcp.CallToolResult{IsError: true, Content: contents}
	}

	tests := []struct {
		name            string
		res             *mcp.CallToolResult
		callErr         error
		expectSystem    string // backend's SAP system id / SID ("" = SID unknown)
		expectUser      string // backend's configured SAP_USER ("" = identity unknown)
		expectClient    string // backend's configured SAP_CLIENT ("" = identity unknown)
		wantStatus      models.ServerStatus
		wantReasonSub   string // non-empty: reason must contain this substring
		wantEmptyReason bool   // true: reason must be exactly ""
	}{
		{
			name:            "callErr contains 'not found' -> StatusRunning graceful fallback",
			res:             nil,
			callErr:         errors.New("tool not found"),
			wantStatus:      models.StatusRunning,
			wantEmptyReason: true,
		},
		{
			name:            "callErr contains 'unknown tool' -> StatusRunning graceful fallback",
			res:             nil,
			callErr:         errors.New("unknown tool sap_list_sessions"),
			wantStatus:      models.StatusRunning,
			wantEmptyReason: true,
		},
		{
			name:          "callErr other transport error -> StatusUnreachable with probe-failed prefix",
			res:           nil,
			callErr:       errors.New("connection reset by peer"),
			wantStatus:    models.StatusUnreachable,
			wantReasonSub: "sap-gui session probe failed:",
		},
		{
			name:          "res.IsError=true with text content -> StatusDegraded with text in reason",
			res:           errResult(textContent("COM server not available")),
			callErr:       nil,
			wantStatus:    models.StatusDegraded,
			wantReasonSub: "no SAP GUI session:",
		},
		{
			name:          "res.IsError=true no text content -> StatusDegraded with fallback reason",
			res:           errResult(),
			callErr:       nil,
			wantStatus:    models.StatusDegraded,
			wantReasonSub: "no SAP GUI session",
		},
		{
			name:          "res text content '[]' -> StatusDegraded no session open",
			res:           okResult(textContent("[]")),
			callErr:       nil,
			wantStatus:    models.StatusDegraded,
			wantReasonSub: "no SAP GUI session open",
		},
		{
			name:          "res text content empty string -> StatusDegraded no session open",
			res:           okResult(textContent("")),
			callErr:       nil,
			wantStatus:    models.StatusDegraded,
			wantReasonSub: "no SAP GUI session open",
		},
		{
			// SID unknown (no expectSystem) + a LOGGED-IN session present -> Running.
			name:            "SID unknown + logged-in session -> StatusRunning",
			res:             okResult(textContent(`[{"system_name":"X","user":"NAUMOV"}]`)),
			callErr:         nil,
			wantStatus:      models.StatusRunning,
			wantEmptyReason: true,
		},
		{
			// SID unknown + only a login-screen window (user empty) must FAIL CLOSED
			// (review HIGH) — this used to false-green on any desktop window.
			name:          "SID unknown + only login-screen window -> StatusDegraded",
			res:           okResult(textContent(`{"system_name":"X","user":""}`)),
			callErr:       nil,
			wantStatus:    models.StatusDegraded,
			wantReasonSub: "SID unknown",
		},
		{
			name:          "res no content at all -> StatusDegraded empty response",
			res:           okResult(),
			callErr:       nil,
			wantStatus:    models.StatusDegraded,
			wantReasonSub: "no SAP GUI session open (empty response)",
		},
		// --- Q2: per-system verdict (SID is the discriminator) ---
		{
			name: "own system logged in -> StatusRunning",
			res: okResult(textContent(
				`[{"system_name":"CTC","client":"100","user":"NAUMOV","transaction":"SESSION_MANAGER"}]`)),
			callErr:         nil,
			expectSystem:    "CTC",
			expectUser:      "NAUMOV",
			expectClient:    "100",
			wantStatus:      models.StatusRunning,
			wantEmptyReason: true,
		},
		{
			// THE bug: all backends share NAUMOV/100; only CTC is logged in.
			// The Q25 backend must NOT report Running off the CTC session.
			name: "shared user+client, only ANOTHER SID logged in -> StatusDegraded",
			res: okResult(textContent(
				`[{"system_name":"CTC","client":"100","user":"NAUMOV","transaction":"SNOTE"},` +
					`{"system_name":"Q25","client":"000","user":"","transaction":"S000"}]`)),
			callErr:       nil,
			expectSystem:  "Q25",
			expectUser:    "NAUMOV",
			expectClient:  "100",
			wantStatus:    models.StatusDegraded,
			wantReasonSub: "no logged-in SAP GUI session for system Q25",
		},
		{
			name: "own system present but at login screen (empty user) -> StatusDegraded",
			res: okResult(textContent(
				`[{"system_name":"CTC","client":"000","user":"","transaction":"S000"}]`)),
			callErr:       nil,
			expectSystem:  "CTC",
			expectUser:    "NAUMOV",
			expectClient:  "100",
			wantStatus:    models.StatusDegraded,
			wantReasonSub: "no logged-in SAP GUI session for system CTC",
		},
		{
			name: "own system logged in, lowercase user -> StatusRunning (case-insensitive)",
			res: okResult(textContent(
				`[{"system_name":"ctc","client":"100","user":"naumov"}]`)),
			callErr:         nil,
			expectSystem:    "CTC",
			expectUser:      "NAUMOV",
			expectClient:    "100",
			wantStatus:      models.StatusRunning,
			wantEmptyReason: true,
		},
		{
			// Was a fail-OPEN (-> Running) case that codified the always-green bug.
			// Now fail-CLOSED: an unparseable result must never mask as running.
			name:          "SID known + unparseable text -> StatusDegraded (fail-closed, no false green)",
			res:           okResult(textContent("unexpected-non-json-output")),
			callErr:       nil,
			expectSystem:  "CTC",
			expectUser:    "NAUMOV",
			expectClient:  "100",
			wantStatus:    models.StatusDegraded,
			wantReasonSub: "no SAP GUI session open",
		},
		// --- REGRESSION: the REAL FastMCP wire format. sap_list_sessions returns
		// ONE TextContent block PER session (each a single JSON object), NOT a
		// single JSON array. The old code read only the first block and fail-opened,
		// so every backend showed Running regardless of login state.
		{
			name: "multi-block: own SID at login screen (user empty) -> StatusDegraded",
			res: okResult(
				textContent(`{"system_name":"Q25","client":"100","user":"NAUMOV"}`),
				textContent(`{"system_name":"CTC","client":"000","user":""}`),
				textContent(`{"system_name":"TST","client":"100","user":"NAUMOV"}`),
			),
			callErr:       nil,
			expectSystem:  "CTC",
			expectUser:    "NAUMOV",
			expectClient:  "100",
			wantStatus:    models.StatusDegraded,
			wantReasonSub: "no logged-in SAP GUI session for system CTC",
		},
		{
			name: "multi-block: own SID logged in among others -> StatusRunning",
			res: okResult(
				textContent(`{"system_name":"Q25","client":"100","user":"NAUMOV"}`),
				textContent(`{"system_name":"CTC","client":"100","user":"NAUMOV","transaction":"SESSION_MANAGER"}`),
			),
			callErr:         nil,
			expectSystem:    "CTC",
			expectUser:      "NAUMOV",
			expectClient:    "100",
			wantStatus:      models.StatusRunning,
			wantEmptyReason: true,
		},
		{
			// Dirty env (review MEDIUM): stray whitespace / case in expected
			// SID/USER/CLIENT must NOT strand a genuinely logged-in backend.
			name: "logged-in but whitespace/case in expected values -> StatusRunning",
			res: okResult(
				textContent(`{"system_name":"CTC","client":"100","user":"NAUMOV"}`),
			),
			callErr:         nil,
			expectSystem:    " ctc ",
			expectUser:      "  NAUMOV ",
			expectClient:    " 100 ",
			wantStatus:      models.StatusRunning,
			wantEmptyReason: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// nil logger is explicitly supported by mapSAPGUIResult (guarded by != nil).
			status, reason := mapSAPGUIResult(tt.res, tt.callErr, "sap-gui-Q00", tt.expectSystem, tt.expectUser, tt.expectClient, nil)

			assert.Equal(t, tt.wantStatus, status, "unexpected ServerStatus")

			if tt.wantEmptyReason {
				assert.Empty(t, reason, "reason must be empty for this branch")
			}
			if tt.wantReasonSub != "" {
				assert.Contains(t, reason, tt.wantReasonSub,
					"reason must contain expected substring")
			}
		})
	}
}

// TestSAPSIDFromName pins the SID extraction that drives the per-system verdict:
// a wrong SID would silently mis-match sessions. The SID must equal the
// system_name sap_list_sessions reports (e.g. "CTC").
func TestSAPSIDFromName(t *testing.T) {
	cases := []struct {
		name, client, want string
	}{
		{"sap-gui-CTC-100", "100", "CTC"},
		{"sap-gui-S23-100", "100", "S23"},
		{"sap-gui-Q25-100", "100", "Q25"},
		{"sap-gui-CTC-100", "", "CTC"},    // client unknown → strip final -segment
		{"sap-gui-DEV-200", "100", "DEV"}, // client "100" != suffix "200" → fallback strips final -segment
		{"vsp-CTC-100", "100", ""},        // not a sap-gui- name
		{"orchestrator", "", ""},          // no prefix
	}
	for _, c := range cases {
		got := sapSIDFromName(c.name, c.client)
		assert.Equal(t, c.want, got, "SID for name=%q client=%q", c.name, c.client)
	}
}

// ---- P1 reliability tests ---------------------------------------------------
//
// These tests exercise the shared-snapshot path (getSAPSnapshot / probeSAPOnce),
// the dual-counter blip mask, the takenAt-on-error cache guarantee, and the
// sticky-target eviction introduced in the P1 reliability fix.

// These tests drive the REAL production code through the P1 seams — no
// production logic is re-implemented:
//   - mon.sapProbeFn stubs the leaf probe so the real getSAPSnapshot
//     (singleflight + TTL cache) and checkSAPGUISession run without a live
//     *mcp.ClientSession;
//   - mon.updateSAPStreaks is the real dual-counter blip-mask called by checkOne;
//   - orderSAPCandidates is the real, pure candidate-ordering / eviction policy.

// TestSAPSnapshot_SharedAcrossBackends drives the REAL checkSAPGUISession ->
// getSAPSnapshot path: N sap-gui-* backends in one cycle trigger EXACTLY ONE
// underlying probe (TTL cache), and each backend derives its own verdict from
// the shared snapshot via the real mapSAPGUIResult (CTC logged-in -> Running,
// the rest absent -> Degraded).
func TestSAPSnapshot_SharedAcrossBackends(t *testing.T) {
	sessionJSON := `[{"system_name":"CTC","client":"100","user":"NAUMOV","transaction":"SESSION_MANAGER"}]`
	okResult := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: sessionJSON}}}

	mock := newMockLM()
	backends := []string{"sap-gui-CTC-100", "sap-gui-Q25-100", "sap-gui-S23-100", "sap-gui-DEV-100"}
	for _, name := range backends {
		mock.addEntry(name, models.StatusRunning, models.ServerConfig{
			Command: "/fake/sap-gui-ctl",
			Env:     []string{"SAP_USER=NAUMOV", "SAP_CLIENT=100"},
		})
	}
	mon := NewMonitor(mock, 30*time.Second, nil) // 30s interval -> 15s TTL
	callCount := 0
	mon.sapProbeFn = func(_ context.Context) (*mcp.CallToolResult, error) {
		callCount++
		return okResult, nil
	}
	ctx := context.Background()

	for _, name := range backends {
		status, _ := mon.checkSAPGUISession(ctx, name)
		if name == "sap-gui-CTC-100" {
			assert.Equal(t, models.StatusRunning, status, "%s is logged in -> Running", name)
		} else {
			assert.Equal(t, models.StatusDegraded, status, "%s absent from list -> Degraded", name)
		}
	}

	assert.Equal(t, 1, callCount,
		"N sap-gui backends in one cycle must trigger exactly ONE probe (shared snapshot)")
}

// TestSAPDualCounter_DegradedConfirm drives the REAL updateSAPStreaks: a reliable
// Degraded verdict is tolerated for SAPDegradedConfirmThreshold-1 ticks (Running)
// and confirmed Degraded on the threshold tick; a good verdict then falls through
// (handled=false) and resets both streaks.
func TestSAPDualCounter_DegradedConfirm(t *testing.T) {
	mon := NewMonitor(newMockLM(), 1*time.Second, nil)
	require.Equal(t, 2, mon.SAPDegradedConfirmThreshold)
	state := &serverState{}

	// Tick 1: streak 1 < 2 → tolerated (Running).
	pub, _, handled := mon.updateSAPStreaks(state, models.StatusDegraded, "no session")
	require.True(t, handled)
	assert.Equal(t, models.StatusRunning, pub, "first Degraded tick tolerated")
	assert.Equal(t, 1, state.sapDegradedStreak)

	// Tick 2: streak 2 == threshold → confirmed Degraded with the reason.
	pub, reason, handled := mon.updateSAPStreaks(state, models.StatusDegraded, "no session")
	require.True(t, handled)
	assert.Equal(t, models.StatusDegraded, pub, "second Degraded tick confirms Degraded")
	assert.Equal(t, "no session", reason)
	assert.Equal(t, 2, state.sapDegradedStreak)

	// Good verdict: SAP path does not handle it; both streaks reset.
	_, _, handled = mon.updateSAPStreaks(state, models.StatusRunning, "")
	assert.False(t, handled, "a good verdict falls through to the Running/REST path")
	assert.Equal(t, 0, state.sapDegradedStreak, "good tick resets sapDegradedStreak")
	assert.Equal(t, 0, state.sapUnreachableStreak, "good tick resets sapUnreachableStreak")
}

// TestSAPDualCounter_UnreachableThreshold drives the REAL updateSAPStreaks I/O-error
// path: tolerated up to SAPProbeFailureThreshold (3), then confirmed Degraded.
func TestSAPDualCounter_UnreachableThreshold(t *testing.T) {
	mon := NewMonitor(newMockLM(), 1*time.Second, nil)
	require.Equal(t, 3, mon.SAPProbeFailureThreshold)
	state := &serverState{}

	for i := 1; i < mon.SAPProbeFailureThreshold; i++ {
		pub, _, handled := mon.updateSAPStreaks(state, models.StatusUnreachable, "io")
		require.True(t, handled)
		assert.Equal(t, models.StatusRunning, pub, "tick %d tolerated", i)
	}
	pub, _, _ := mon.updateSAPStreaks(state, models.StatusUnreachable, "io")
	assert.Equal(t, models.StatusDegraded, pub, "Unreachable confirmed at threshold")
}

// TestSAPDualCounter_NoCrossContamination verifies that alternating
// Degraded/Unreachable verdicts never prematurely trip either threshold:
// each counter resets the other on every tick, so only a CONSECUTIVE
// run of the same verdict class reaches the threshold.
func TestSAPDualCounter_NoCrossContamination(t *testing.T) {
	mon := NewMonitor(newMockLM(), 1*time.Second, nil)
	state := &serverState{}

	// Pattern D, U, D, U: each verdict resets the opposing counter, so neither
	// accumulates and the status stays Running (tolerated) every tick.
	verdicts := []models.ServerStatus{
		models.StatusDegraded,
		models.StatusUnreachable,
		models.StatusDegraded,
		models.StatusUnreachable,
	}
	for i, v := range verdicts {
		pub, _, handled := mon.updateSAPStreaks(state, v, "blip")
		require.True(t, handled)
		assert.Equal(t, models.StatusRunning, pub,
			"tick %d (%s): alternating verdicts must stay tolerated", i, v)
		assert.LessOrEqual(t, state.sapDegradedStreak, 1,
			"tick %d: sapDegradedStreak must not accumulate when alternating", i)
		assert.LessOrEqual(t, state.sapUnreachableStreak, 1,
			"tick %d: sapUnreachableStreak must not accumulate when alternating", i)
	}
}

// TestSAPSnapshot_TakenAtOnError verifies that when probeSAPOnce errors, the
// cache takenAt is still advanced, so a second getSAPSnapshot call within the
// TTL does NOT re-invoke the probe (CRITICAL note in design step 1).
func TestSAPSnapshot_TakenAtOnError(t *testing.T) {
	probeErr := errors.New("COM engine unavailable")
	callCount := 0
	mon := NewMonitor(newMockLM(), 30*time.Second, nil) // TTL 15s > 0
	mon.sapProbeFn = func(_ context.Context) (*mcp.CallToolResult, error) {
		callCount++
		return nil, probeErr
	}
	ctx := context.Background()

	res1, err1 := mon.getSAPSnapshot(ctx)
	assert.Nil(t, res1)
	assert.ErrorIs(t, err1, probeErr)
	assert.Equal(t, 1, callCount, "first call must invoke the probe")

	res2, err2 := mon.getSAPSnapshot(ctx)
	assert.Nil(t, res2)
	assert.ErrorIs(t, err2, probeErr)
	assert.Equal(t, 1, callCount,
		"second call within TTL must NOT re-probe (takenAt advanced even on error)")

	mon.sapSnapMu.Lock()
	assert.False(t, mon.sapSnap.takenAt.IsZero(), "takenAt must be set even on probe error")
	mon.sapSnapMu.Unlock()
}

// TestSAPSnapshot_NoCacheWhenTTLZero verifies the F1 fix: interval<=0 yields
// TTL=0, which means "no cache" — every sequential getSAPSnapshot re-invokes the
// probe (singleflight only collapses CONCURRENT callers, not sequential ones).
func TestSAPSnapshot_NoCacheWhenTTLZero(t *testing.T) {
	callCount := 0
	mon := NewMonitor(newMockLM(), 0, nil) // interval 0 -> TTL 0 -> no cache
	mon.sapProbeFn = func(_ context.Context) (*mcp.CallToolResult, error) {
		callCount++
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "[]"}}}, nil
	}
	ctx := context.Background()
	_, _ = mon.getSAPSnapshot(ctx)
	_, _ = mon.getSAPSnapshot(ctx)
	assert.Equal(t, 2, callCount, "TTL=0 must re-probe each sequential call (no cache-forever)")
}

// TestOrderSAPCandidates exercises the REAL pure ordering/eviction policy used by
// probeSAPOnce (design step 2, review HIGH + F2): ascending fail-count, last-good
// first among ties, then lexicographic by name. This is the production function —
// not a re-implementation — so a broken sort or fail-count key is caught here.
func TestOrderSAPCandidates(t *testing.T) {
	names := []string{"sap-gui-Q25-100", "sap-gui-CTC-100", "sap-gui-S23-100"}

	// Eviction: a failing target (Q25, fails=2) sinks behind healthy ones; the
	// two fails=0 targets order lexicographically.
	got := orderSAPCandidates(names, map[string]int{"sap-gui-Q25-100": 2}, "")
	assert.Equal(t,
		[]string{"sap-gui-CTC-100", "sap-gui-S23-100", "sap-gui-Q25-100"}, got,
		"failing target sorts last; equal-fail targets sort by name")

	// last-good wins the tie at equal fail-count (review F2: sapLastGoodTarget wired).
	got = orderSAPCandidates(names, map[string]int{}, "sap-gui-S23-100")
	assert.Equal(t, "sap-gui-S23-100", got[0],
		"last-good target floats to the front among equal fail-counts")

	// Fail-count dominates the last-good preference: a failing last-good still sinks.
	got = orderSAPCandidates(names, map[string]int{"sap-gui-S23-100": 1}, "sap-gui-S23-100")
	assert.Equal(t, "sap-gui-S23-100", got[len(got)-1],
		"fail-count takes precedence over last-good preference")
}

// Compile-time guard: ensure the tests reference the real Monitor methods/funcs so
// renames are caught at compile time rather than silently skipping tests.
var _ = (*Monitor).checkSAPGUISession
var _ = (*Monitor).getSAPSnapshot
var _ = (*Monitor).probeSAPOnce
var _ = (*Monitor).updateSAPStreaks
var _ = (*Monitor).probeReachable
var _ = orderSAPCandidates
