// TASK C1 — tests for the retry-on-recovery path in maybeProbeUnreachable
// for STDIO backends skipped at spawn time (vsp-* with SAP_URL).
//
// Before TASK C1, maybeProbeUnreachable returned early for ALL stdio backends
// (entry.Config.URL == "" guard). These tests verify that vsp-* backends in
// StatusUnreachable with a SAP_URL in Env are now slow-polled and re-started
// when the endpoint becomes reachable.
//
// Fail-without-fix property:
//   - TestMaybeProbeUnreachable_StdioSAP_StillUnreachable: before the fix the
//     function returned early (no probe, no Start call). After the fix it probes
//     and records no Start on unreachable (same outcome, but for the right
//     reason — the probe now fires).
//   - TestMaybeProbeUnreachable_StdioSAP_BecomesReachable: before the fix no
//     Start was ever issued. After the fix a Start is issued when the SAP_URL
//     becomes reachable. This test would FAIL before the fix (mock.starts == 0).
//   - TestUnreachableProbeURL: pure-function test; unreachableProbeURL did not
//     exist before, so all cases would fail at compile time.
package health

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"mcp-gateway/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- unreachableProbeURL pure-function tests --------------------------------

// TestUnreachableProbeURL_HTTPBackend: HTTP/SSE backend returns Config.URL.
func TestUnreachableProbeURL_HTTPBackend(t *testing.T) {
	entry := models.ServerEntry{
		Name: "my-http-backend",
		Config: models.ServerConfig{
			URL: "http://host:8765",
		},
	}
	assert.Equal(t, "http://host:8765", unreachableProbeURL(entry))
}

// TestUnreachableProbeURL_StdioWithSAPURL: vsp-* stdio backend returns SAP_URL.
func TestUnreachableProbeURL_StdioWithSAPURL(t *testing.T) {
	entry := models.ServerEntry{
		Name: "vsp-Q00",
		Config: models.ServerConfig{
			Command: "/fake/vsp.exe",
			Env:     []string{"SAP_URL=http://saphost:50000"},
		},
	}
	assert.Equal(t, "http://saphost:50000", unreachableProbeURL(entry))
}

// TestUnreachableProbeURL_StdioNoSAPURL: stdio without SAP_URL returns "".
func TestUnreachableProbeURL_StdioNoSAPURL(t *testing.T) {
	entry := models.ServerEntry{
		Name: "orchestrator",
		Config: models.ServerConfig{
			Command: "/usr/bin/orchestrator",
		},
	}
	assert.Equal(t, "", unreachableProbeURL(entry))
}

// TestUnreachableProbeURL_TableDriven: full matrix.
func TestUnreachableProbeURL_TableDriven(t *testing.T) {
	tests := []struct {
		name    string
		entry   models.ServerEntry
		wantURL string
	}{
		{
			name: "http backend",
			entry: models.ServerEntry{
				Config: models.ServerConfig{URL: "http://host:8765"},
			},
			wantURL: "http://host:8765",
		},
		{
			name: "vsp with SAP_URL",
			entry: models.ServerEntry{
				Config: models.ServerConfig{
					Command: "/fake/vsp.exe",
					Env:     []string{"SAP_URL=http://sap.corp:50000"},
				},
			},
			wantURL: "http://sap.corp:50000",
		},
		{
			name: "vsp without SAP_URL",
			entry: models.ServerEntry{
				Config: models.ServerConfig{
					Command: "/fake/vsp.exe",
				},
			},
			wantURL: "",
		},
		{
			name: "sap-gui (no SAP_URL, COM automation)",
			entry: models.ServerEntry{
				Config: models.ServerConfig{
					Command: "/fake/sap-gui-ctl",
				},
			},
			wantURL: "",
		},
		{
			name: "orchestrator (non-SAP stdio)",
			entry: models.ServerEntry{
				Config: models.ServerConfig{
					Command: "/usr/bin/orchestrator",
				},
			},
			wantURL: "",
		},
		{
			name:    "empty config",
			entry:   models.ServerEntry{},
			wantURL: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := unreachableProbeURL(tt.entry)
			assert.Equal(t, tt.wantURL, got)
		})
	}
}

// ---- maybeProbeUnreachable integration tests (TASK C1) ----------------------

// TestMaybeProbeUnreachable_StdioSAP_StillUnreachable: a vsp-* backend in
// StatusUnreachable with an unreachable SAP_URL is probed (no more early return)
// but Start is NOT called when the endpoint is still down.
func TestMaybeProbeUnreachable_StdioSAP_StillUnreachable(t *testing.T) {
	port := unboundLocalPort(t)
	sapURL := fmt.Sprintf("http://127.0.0.1:%d", port)

	mock := newMockLM()
	mock.addEntry("vsp-Q00", models.StatusUnreachable, models.ServerConfig{
		Command: "/fake/vsp.exe",
		Env:     []string{"SAP_URL=" + sapURL},
	})
	tm := newTestableMonitor(mock)
	tm.CheckOnce(context.Background())

	assert.Equal(t, 0, mock.starts,
		"still-unreachable SAP endpoint: Start must not be called")
	// Status should remain unreachable (no SetStatus call on still-unreachable path).
	assert.Equal(t, models.StatusUnreachable, mock.getEntry("vsp-Q00").Status,
		"status must stay StatusUnreachable when endpoint is still unreachable")
}

// TestMaybeProbeUnreachable_StdioSAP_BecomesReachable: a vsp-* backend in
// StatusUnreachable with a now-reachable SAP_URL must trigger a Start call.
// This is the retry-on-recovery path for TASK C1.
func TestMaybeProbeUnreachable_StdioSAP_BecomesReachable(t *testing.T) {
	port, closeFn := listenLocal(t)
	defer closeFn()
	sapURL := fmt.Sprintf("http://127.0.0.1:%d", port)

	mock := newMockLM()
	mock.addEntry("vsp-Q00", models.StatusUnreachable, models.ServerConfig{
		Command: "/fake/vsp.exe",
		Env:     []string{"SAP_URL=" + sapURL},
	})
	tm := newTestableMonitor(mock)
	tm.CheckOnce(context.Background())

	require.Equal(t, 1, mock.starts,
		"reachable SAP endpoint: Start must be called exactly once")
	// The mock Start sets StatusRunning.
	assert.Equal(t, models.StatusRunning, mock.getEntry("vsp-Q00").Status,
		"after Start the status must be StatusRunning")
}

// TestMaybeProbeUnreachable_StdioNoSAPURL_NoOp: a stdio backend without
// SAP_URL in StatusUnreachable is still a no-op (no probe, no Start).
// This covers the defensive guard in unreachableProbeURL returning "".
func TestMaybeProbeUnreachable_StdioNoSAPURL_NoOp(t *testing.T) {
	mock := newMockLM()
	mock.addEntry("orchestrator", models.StatusUnreachable, models.ServerConfig{
		Command: "/usr/bin/orchestrator",
		// No SAP_URL
	})
	tm := newTestableMonitor(mock)
	tm.CheckOnce(context.Background())

	assert.Equal(t, 0, mock.starts,
		"stdio without SAP_URL must not trigger Start (no-op path)")
}

// TestMaybeProbeUnreachable_StdioSAP_ThrottledOnSecondCall: when the first
// probe fires on an unreachable endpoint, nextReachProbeAt is set into the
// future. A second immediate CheckOnce must be throttled and not fire another
// probe or Start call.
func TestMaybeProbeUnreachable_StdioSAP_ThrottledOnSecondCall(t *testing.T) {
	// Use a closed port so the endpoint is unreachable on BOTH calls.
	port := unboundLocalPort(t)
	sapURL := fmt.Sprintf("http://127.0.0.1:%d", port)

	mock := newMockLM()
	mock.addEntry("vsp-Q00", models.StatusUnreachable, models.ServerConfig{
		Command: "/fake/vsp.exe",
		Env:     []string{"SAP_URL=" + sapURL},
	})
	tm := newTestableMonitor(mock)

	// First CheckOnce: probe fires, endpoint unreachable → no Start, but
	// nextReachProbeAt is set to now+60s.
	tm.CheckOnce(context.Background())
	assert.Equal(t, 0, mock.starts, "first call: unreachable → no Start")

	// Second immediate CheckOnce: nextReachProbeAt has not elapsed → throttled.
	tm.CheckOnce(context.Background())
	assert.Equal(t, 0, mock.starts,
		"second immediate CheckOnce must be throttled (nextReachProbeAt not elapsed)")
}

// TestMaybeProbeUnreachable_SAPGUI_Recovers: spike Part B connection-gate
// recovery. A sap-gui-* backend has NO SAP_URL (COM automation), so the old
// URL slow-poll was a no-op and the backend would be STRANDED once gated to
// StatusUnreachable. The new sap-gui-* branch instead RE-ATTEMPTS Start()
// directly on the slow-poll cadence — there is no host to dial; the only
// recovery signal is a successful Start. The mock Start succeeds, so the
// backend recovers to StatusRunning. This FAILS before the fix (old code
// returned early with starts == 0).
func TestMaybeProbeUnreachable_SAPGUI_Recovers(t *testing.T) {
	mock := newMockLM()
	mock.addEntry("sap-gui-Q00", models.StatusUnreachable, models.ServerConfig{
		Command: "/fake/sap-gui-ctl",
		// No SAP_URL — sap-gui uses COM automation
	})
	tm := newTestableMonitor(mock)
	tm.CheckOnce(context.Background())

	require.Equal(t, 1, mock.starts,
		"sap-gui-* in StatusUnreachable must attempt Start (no snapshot dependency, no TCP dial)")
	assert.Equal(t, models.StatusRunning, mock.getEntry("sap-gui-Q00").Status,
		"a successful Start recovers the gated sap-gui-* backend to Running")
}

// TestMaybeProbeUnreachable_SAPGUI_StillHung: when the COM engine is still
// unavailable, the slow-poll Start attempt fails. The backend must NOT
// hot-loop: the second immediate CheckOnce is throttled (nextReachProbeAt set
// into the future), so Start is attempted exactly once per cadence window —
// the de-storm guarantee (bounded by 60s/5min, never suture's 15s lockstep).
func TestMaybeProbeUnreachable_SAPGUI_StillHung(t *testing.T) {
	mock := newMockLM()
	mock.startErr = errors.New("COM STA engine still hung")
	mock.addEntry("sap-gui-Q00", models.StatusUnreachable, models.ServerConfig{
		Command: "/fake/sap-gui-ctl",
	})
	tm := newTestableMonitor(mock)

	// First tick: one Start attempt (fails), nextReachProbeAt scheduled ahead.
	tm.CheckOnce(context.Background())
	assert.Equal(t, 1, mock.starts, "first tick: exactly one Start attempt")

	// Second immediate tick: throttled — no second hot-loop Start.
	tm.CheckOnce(context.Background())
	assert.Equal(t, 1, mock.starts,
		"still-hung sap-gui-* must be throttled on the slow-poll cadence, not hot-loop (de-storm)")
}

// TestMaybeProbeUnreachable_HTTPBackend_Unchanged: the original HTTP path still
// works — a backend with Config.URL in StatusUnreachable probes Config.URL and
// issues Start when reachable.
func TestMaybeProbeUnreachable_HTTPBackend_Unchanged(t *testing.T) {
	port, closeFn := listenLocal(t)
	defer closeFn()
	backendURL := fmt.Sprintf("http://127.0.0.1:%d", port)

	mock := newMockLM()
	mock.addEntry("my-http", models.StatusUnreachable, models.ServerConfig{
		URL: backendURL,
	})
	tm := newTestableMonitor(mock)
	tm.CheckOnce(context.Background())

	require.Equal(t, 1, mock.starts,
		"HTTP backend: reachable URL must trigger Start (unchanged path)")
}
