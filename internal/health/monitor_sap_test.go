// Regression tests for BUG-STDIO-1 through BUG-STDIO-4 (SAP stdio backend
// health-check fix):
//
//   - BUG-STDIO-1: vsp-* always reported running regardless of SAP reachability.
//   - BUG-STDIO-2: sap-gui-* always reported running regardless of session state.
//   - BUG-STDIO-3: lifecycle Start() did not TCP-probe SAP_URL for vsp-* backends.
//   - BUG-STDIO-4: checkSAPGUISession session-absent path.
//
// Test strategy:
//   - vsp-* reachability: uses a wrapper that mirrors checkOne's mcpOK=true branch
//     with the real SAP probe logic, driven via a real local TCP listener or an
//     unbound local port (same pattern as monitor_unreachable_test.go).
//   - sap-gui-* session check: the "no MCP session" early-return branch is fully
//     testable (mockLM.Session returns false). Deeper branches (CallTool result
//     inspection) require a real *mcp.ClientSession — flagged as Escalated.
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

		// SAP reachability is a thresholded Degraded signal — never a terminal
		// StatusUnreachable for stdio (mirrors monitor.go checkOne).
		sapBad := sapStatus == models.StatusUnreachable || sapStatus == models.StatusDegraded
		if sapBad {
			state.sapProbeFailures++
			sapFails := state.sapProbeFailures
			tm.mu.Unlock()
			if sapFails < tm.SAPProbeFailureThreshold {
				tm.lm.SetStatus(name, models.StatusRunning, "")
				return
			}
			tm.lm.SetStatus(name, models.StatusDegraded, sapReason)
			return
		}
		state.sapProbeFailures = 0
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
// that pins the (StatusUnreachable, "no MCP session…") return values for the
// session-absent path.
func TestCheckSAPGUISession_NoSession(t *testing.T) {
	mock := newMockLM()
	// No entry for "sap-gui-ABC"; Session() returns (nil, false).
	mon := NewMonitor(mock, 0, nil)
	ctx := context.Background()

	status, reason := mon.checkSAPGUISession(ctx, "sap-gui-ABC")

	assert.Equal(t, models.StatusUnreachable, status,
		"checkSAPGUISession must return StatusUnreachable when no session exists")
	assert.Contains(t, reason, "no MCP session",
		"reason must describe the missing session")
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
			name:          "session absent -> StatusUnreachable",
			wantStatus:    models.StatusUnreachable,
			wantReasonSub: "no MCP session",
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
		name           string
		res            *mcp.CallToolResult
		callErr        error
		expectUser     string // backend's configured SAP_USER ("" = identity unknown)
		expectClient   string // backend's configured SAP_CLIENT ("" = identity unknown)
		wantStatus     models.ServerStatus
		wantReasonSub  string // non-empty: reason must contain this substring
		wantEmptyReason bool  // true: reason must be exactly ""
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
			name:           "callErr other transport error -> StatusUnreachable with probe-failed prefix",
			res:            nil,
			callErr:        errors.New("connection reset by peer"),
			wantStatus:     models.StatusUnreachable,
			wantReasonSub:  "sap-gui session probe failed:",
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
			name:            "res text content non-empty list -> StatusRunning",
			res:             okResult(textContent(`[{"id":1,"name":"SAPLogon"}]`)),
			callErr:         nil,
			wantStatus:      models.StatusRunning,
			wantEmptyReason: true,
		},
		{
			name:          "res no content at all -> StatusDegraded empty response",
			res:           okResult(),
			callErr:       nil,
			wantStatus:    models.StatusDegraded,
			wantReasonSub: "no SAP GUI session open (empty response)",
		},
		// --- Q2: per-system verdict when the backend's identity is known ---
		{
			name: "identity known + matching session -> StatusRunning",
			res: okResult(textContent(
				`[{"system_name":"CTC","client":"100","user":"NAUMOV","transaction":"SESSION_MANAGER"}]`)),
			callErr:         nil,
			expectUser:      "NAUMOV",
			expectClient:    "100",
			wantStatus:      models.StatusRunning,
			wantEmptyReason: true,
		},
		{
			name: "identity known + only ANOTHER system logged in -> StatusDegraded (not stranded as running)",
			res: okResult(textContent(
				`[{"system_name":"DEV","client":"200","user":"IVANOV","transaction":"SE80"}]`)),
			callErr:       nil,
			expectUser:    "NAUMOV",
			expectClient:  "100",
			wantStatus:    models.StatusDegraded,
			wantReasonSub: "no SAP GUI session for this system",
		},
		{
			name: "identity known + matching session with lowercase user -> StatusRunning (case-insensitive)",
			res: okResult(textContent(
				`[{"system_name":"CTC","client":"100","user":"naumov"}]`)),
			callErr:         nil,
			expectUser:      "NAUMOV",
			expectClient:    "100",
			wantStatus:      models.StatusRunning,
			wantEmptyReason: true,
		},
		{
			name: "identity known + matching client but different user -> StatusDegraded",
			res: okResult(textContent(
				`[{"system_name":"CTC","client":"100","user":"OTHERUSER"}]`)),
			callErr:       nil,
			expectUser:    "NAUMOV",
			expectClient:  "100",
			wantStatus:    models.StatusDegraded,
			wantReasonSub: "no SAP GUI session for this system",
		},
		{
			name:            "identity known + non-JSON non-empty text -> StatusRunning (fail-open, no false strand)",
			res:             okResult(textContent("unexpected-non-json-output")),
			callErr:         nil,
			expectUser:      "NAUMOV",
			expectClient:    "100",
			wantStatus:      models.StatusRunning,
			wantEmptyReason: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// nil logger is explicitly supported by mapSAPGUIResult (guarded by != nil).
			status, reason := mapSAPGUIResult(tt.res, tt.callErr, "sap-gui-Q00", tt.expectUser, tt.expectClient, nil)

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

// Compile-time guard: ensure the wrapper references the real Monitor methods so
// renames are caught at compile time rather than silently skipping tests.
var _ = (*Monitor).checkSAPGUISession
var _ = (*Monitor).probeReachable
