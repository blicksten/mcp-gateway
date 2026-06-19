// Package health implements the health monitor for backend MCP servers.
// It periodically pings backends and manages state transitions
// including auto-restart and circuit breaking.
package health

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"mcp-gateway/internal/models"
	"mcp-gateway/internal/sapname"

	"github.com/failsafe-go/failsafe-go/bulkhead"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/sync/singleflight"
)

// Default thresholds.
const (
	DefaultPingTimeout         = 5 * time.Second
	DefaultConsecutiveFailures = 3
	// DefaultSAPProbeFailures is the consecutive-failure threshold for the SAP
	// reachability probe (vsp host-dial / sap-gui session check) before a live,
	// MCP-ping-OK SAP backend is marked Degraded. Mirrors the MCP-ping threshold
	// so a single transient VPN/host blip does not flap the status. SAP backends
	// are deliberately NEVER routed to StatusUnreachable (stdio has no slow-poll
	// recovery); they ride the same Running/Degraded loop as every other backend,
	// which re-probes every tick and self-recovers.
	DefaultSAPProbeFailures = 3
	// DefaultSAPDegradedConfirm is the consecutive-confirmation threshold for a
	// RELIABLE "not logged in" verdict (StatusDegraded from a successful probe)
	// before the backend is actually marked Degraded. Two ticks confirms the
	// session is truly absent rather than a race between two probes reading the
	// same shared STA engine at a transient moment. Lower than the I/O-error
	// threshold (3) because this path has no transport noise.
	DefaultSAPDegradedConfirm   = 2
	DefaultCircuitBreakerThresh = 5
	DefaultCircuitBreakerWindow = 300 * time.Second
	DefaultRESTHealthTimeout    = 5 * time.Second
	DefaultMaxConcurrentChecks  = 20
	DefaultRestartStuckTimeout  = 60 * time.Second
	// UnreachableProbeInitial — interval between reachability TCP probes
	// for backends in StatusUnreachable for the first 5 probes (~5 min).
	UnreachableProbeInitial = 60 * time.Second
	// UnreachableProbeMax — interval cap for reachability probes after
	// the initial window. Long-unreachable backends are checked every
	// 5 min, not flooded with retries. See docs/PLAN-unreachable-handling.md.
	UnreachableProbeMax = 5 * time.Minute
	// UnreachableProbeDialTimeout — single TCP-dial budget for one
	// reachability probe. Short to keep the monitor loop responsive
	// even when the slow-poll target is genuinely unreachable.
	UnreachableProbeDialTimeout = 3 * time.Second
	// UnreachableProbeInitialBurstCount — number of probes at the
	// initial cadence before backing off to UnreachableProbeMax.
	UnreachableProbeInitialBurstCount = 5
	// DefaultConsecutiveFailedStarts trips the circuit breaker when a server
	// fails to start this many times in a row, regardless of the time window.
	// Guards against the window-reset escape: with 90s restart intervals and
	// a 300s window, 5 restarts take 450s and always reset the window before
	// the threshold is reached, so the time-window circuit never opens.
	DefaultConsecutiveFailedStarts = 5
	// RestartBackoffBase / RestartBackoffMax bound the exponential delay
	// between restart attempts (P0: previously 0ms, which let a
	// spawn-OK-but-unreachable backend restart-storm the daemon to death).
	RestartBackoffBase = 5 * time.Second
	RestartBackoffMax  = 300 * time.Second
)

// restartBackoff returns the minimum wait before the next restart attempt.
// restartCount 1 → base (no growth yet); each subsequent restart doubles
// the wait, capped at RestartBackoffMax. restartCount is >= 1 at every call
// site (incremented before use). A base of 0 disables backoff entirely
// (used by unit tests that drive many restart cycles with no real time
// elapsing).
func (m *Monitor) restartBackoff(restartCount int) time.Duration {
	base := m.RestartBackoffBase
	if base <= 0 {
		return 0
	}
	maxD := m.RestartBackoffMax
	if maxD <= 0 {
		maxD = RestartBackoffMax
	}
	if restartCount < 1 {
		restartCount = 1
	}
	d := base
	for i := 1; i < restartCount; i++ {
		d *= 2
		if d >= maxD {
			return maxD
		}
	}
	if d > maxD {
		return maxD
	}
	return d
}

// LifecycleManager is the interface the monitor uses to interact with
// the lifecycle manager. Avoids a circular dependency.
type LifecycleManager interface {
	Entries() []models.ServerEntry
	Session(name string) (*mcp.ClientSession, bool)
	SetStatus(name string, status models.ServerStatus, lastErr string)
	Restart(ctx context.Context, name string) error
	Start(ctx context.Context, name string) error
	// SupervisorActive returns true when the suture supervisor tree has been
	// wired. When true, the supervisor owns restart policy and Monitor's
	// attemptRestart must not issue an independent Restart call (F2 fix).
	SupervisorActive() bool
	// AddBackendToSupervisor re-registers a backend with the suture supervisor
	// tree after recovery from StatusUnreachable. The backend's Serve returned
	// ErrDoNotRestart when it went Unreachable, so it was removed from suture;
	// calling this re-adds it so crash-restart policy applies again. No-op when
	// the supervisor is not set up or the backend is already registered (idempotent).
	AddBackendToSupervisor(name string, logger *slog.Logger)
}

// serverState tracks per-server health state.
type serverState struct {
	consecutiveFailures int
	restartCount        int
	firstRestartAt      time.Time
	lastCrashAt         time.Time     // set in attemptRestart before each restart
	uptimeStart         time.Time     // set on first successful health check after (re)start
	cumulativeUptime    time.Duration // total operational time across all uptime windows
	// consecutiveFailedStarts counts how many restart attempts in a row
	// ended with Start() returning an error. Reset to 0 on a successful
	// start. When it reaches DefaultConsecutiveFailedStarts the circuit
	// opens regardless of the time window (time-window escape hatch fix).
	consecutiveFailedStarts int
	// lastHealthyAt is the wall-clock time of the most recent successful
	// MCP health check. The sticky-circuit gate uses
	// lastHealthyAt.After(firstRestartAt) to distinguish a backend that
	// genuinely recovered (allow window reset) from one that spawns OK but
	// never becomes reachable (must accumulate toward the threshold and
	// open). Zeroed by the CR-15 window reset and by ResetCircuit.
	lastHealthyAt time.Time
	// nextRestartAllowedAt gates the exponential restart backoff: an
	// attemptRestart before this instant is skipped. Cleared on a proven
	// healthy check and by ResetCircuit.
	nextRestartAllowedAt time.Time
	// nextReachProbeAt gates the slow-poll reachability check for
	// backends in StatusUnreachable. Set by maybeProbeUnreachable each
	// time it dispatches a probe. Cleared when the backend transitions
	// back to StatusRunning. See docs/PLAN-unreachable-handling.md.
	nextReachProbeAt time.Time
	// reachProbeCount counts dispatched reachability probes for the
	// current StatusUnreachable episode. Drives the burst-then-backoff
	// cadence (first N at 60s, then 5min cap). Reset to 0 on transition
	// back to StatusRunning.
	reachProbeCount int
	// sapUnreachableStreak counts consecutive SAP probe I/O failures
	// (transport/dial error → StatusUnreachable from mapSAPGUIResult / TCP probe)
	// while the MCP child is alive (mcpOK). Tolerated up to SAPProbeFailureThreshold
	// before the backend is marked Degraded. Reset to 0 on any good SAP outcome or
	// by ResetCircuit. SAP backends never use the StatusUnreachable slow-poll.
	sapUnreachableStreak int
	// sapDegradedStreak counts consecutive RELIABLE "not logged in" verdicts
	// (StatusDegraded from a SUCCESSFUL sap_list_sessions probe — no I/O error,
	// but the session list is empty or the expected system is absent). Tolerated up
	// to SAPDegradedConfirmThreshold (2) before the backend is marked Degraded.
	// Reset to 0 on any good SAP outcome or by ResetCircuit.
	sapDegradedStreak int
}

// sapSnapshot holds the result of a single sap_list_sessions probe shared
// across all sap-gui-* backends in one monitor cycle. The STA COM engine is
// global and returns the same connection list regardless of which backend
// calls it, so probing once and sharing the result eliminates 8 concurrent
// STA-thread contentions per cycle. (Review finding: design step 1.)
type sapSnapshot struct {
	res     *mcp.CallToolResult
	err     error
	takenAt time.Time
}

// Monitor periodically checks backend server health and manages
// state transitions including auto-restart and circuit breaking.
type Monitor struct {
	lm         LifecycleManager
	logger     *slog.Logger
	interval   time.Duration
	httpClient *http.Client
	startedAt  time.Time // written once at construction, never mutated — read without lock is safe

	mu     sync.Mutex
	states map[string]*serverState

	// SAP GUI shared-snapshot state (design step 1).
	// sapSnapMu guards sapSnap; sapSnapSF deduplicates concurrent callers
	// within the same cycle before the cache is warm.
	sapSnapMu sync.Mutex
	sapSnap   sapSnapshot
	sapSnapSF singleflight.Group
	// sapTargetFails maps backend name -> consecutive probe-failure count for
	// target ordering (eviction): failing targets sink to the back of the
	// candidate list so a healthy session is tried first. Only touched on
	// the singleflight-serialised probe path — no extra lock needed beyond SF.
	sapTargetFails    map[string]int
	sapLastGoodTarget string
	// sapProbeFn, when non-nil, replaces probeSAPOnce as the snapshot probe.
	// Production leaves it nil; tests inject a stub so the real getSAPSnapshot
	// (singleflight + TTL) and the checkOne mask run against controlled probe
	// results without a live *mcp.ClientSession.
	sapProbeFn func(ctx context.Context) (*mcp.CallToolResult, error)

	// Configurable thresholds (exported for testing). These MUST be set
	// only before NewMonitor's caller starts the monitor (Run/CheckOnce)
	// and treated as read-only afterwards — they are read without the
	// mutex on the health-check path (review MED-1).
	ConsecutiveFailureThreshold      int
	CircuitBreakerThreshold          int
	CircuitBreakerWindow             time.Duration
	ConsecutiveFailedStartsThreshold int
	// SAPProbeFailureThreshold is the consecutive SAP-probe I/O failure count
	// (transport errors → StatusUnreachable from mapSAPGUIResult) before a
	// live (MCP-ping-OK) SAP backend is marked Degraded. Tolerates VPN blips.
	SAPProbeFailureThreshold int
	// SAPDegradedConfirmThreshold is the consecutive "not logged in" verdict
	// count (StatusDegraded from a SUCCESSFUL probe) before the backend is
	// marked Degraded. Lower than the I/O-error threshold because this path
	// has no transport noise. (Review finding MEDIUM-2, design step 4.)
	SAPDegradedConfirmThreshold int
	// RestartBackoffBase/Max bound the exponential restart backoff.
	// A base of 0 disables backoff (unit tests set this to drive many
	// restart cycles without real time elapsing).
	RestartBackoffBase time.Duration
	RestartBackoffMax  time.Duration
}

// NewMonitor creates a health monitor.
func NewMonitor(lm LifecycleManager, interval time.Duration, logger *slog.Logger) *Monitor {
	if logger == nil {
		logger = slog.Default()
	}
	return &Monitor{
		lm:        lm,
		logger:    logger,
		interval:  interval,
		startedAt: time.Now(),
		httpClient: &http.Client{
			Timeout: DefaultRESTHealthTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse // never follow redirects
			},
		},
		states:                           make(map[string]*serverState),
		sapTargetFails:                   make(map[string]int),
		ConsecutiveFailureThreshold:      DefaultConsecutiveFailures,
		CircuitBreakerThreshold:          DefaultCircuitBreakerThresh,
		CircuitBreakerWindow:             DefaultCircuitBreakerWindow,
		ConsecutiveFailedStartsThreshold: DefaultConsecutiveFailedStarts,
		SAPProbeFailureThreshold:         DefaultSAPProbeFailures,
		SAPDegradedConfirmThreshold:      DefaultSAPDegradedConfirm,
		RestartBackoffBase:               RestartBackoffBase,
		RestartBackoffMax:                RestartBackoffMax,
	}
}

// Run starts the health check loop. Blocks until ctx is cancelled.
func (m *Monitor) Run(ctx context.Context) error {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			m.checkAll(ctx)
		}
	}
}

// CheckOnce runs a single health check cycle (for testing).
func (m *Monitor) CheckOnce(ctx context.Context) {
	m.checkAll(ctx)
}

// checkAll checks all running/degraded servers concurrently with bounded parallelism.
// T1.5.1: replaced channel semaphore with failsafe-go bulkhead for structured
// concurrency budget enforcement across the health-check goroutine fan-out.
func (m *Monitor) checkAll(ctx context.Context) {
	entries := m.lm.Entries()
	bh := bulkhead.New[any](uint(DefaultMaxConcurrentChecks))

	var wg sync.WaitGroup
	for _, entry := range entries {
		switch entry.Status {
		case models.StatusRunning, models.StatusDegraded, models.StatusError:
			wg.Add(1)
			go func(e models.ServerEntry) {
				defer wg.Done()
				if err := bh.AcquirePermit(ctx); err != nil {
					return // context cancelled — skip this check
				}
				defer bh.ReleasePermit()
				// Panic isolation (R2 — "the heart fails last"): a panic in
				// one backend's health check must never take down the
				// monitor loop. Defer order matters: on unwind this defer
				// runs FIRST (recovers), then bh.ReleasePermit() returns the
				// bulkhead permit, then wg.Done() unblocks the outer Wait().
				// The nested double-recover guard prevents a re-panic from
				// SetStatus from skipping ReleasePermit.
				defer func() {
					if r := recover(); r != nil {
						m.logger.Error("panic in health check recovered",
							"server", e.Name, "panic", r)
						// Review HIGH-1: SetStatus must not be able to
						// re-panic out of this defer — a re-panic would
						// bypass the ReleasePermit and wg.Done defers
						// (bulkhead leak + wg.Wait deadlock). Contain it.
						func() {
							defer func() {
								if r2 := recover(); r2 != nil {
									m.logger.Error("panic while reporting health-check panic",
										"server", e.Name, "panic", r2)
								}
							}()
							m.lm.SetStatus(e.Name, models.StatusError,
								fmt.Sprintf("panic in health check: %v", r))
						}()
					}
				}()
				m.checkOne(ctx, e)
			}(entry)
		case models.StatusRestarting:
			// Detect stuck-restarting servers.
			if !entry.StartedAt.IsZero() && time.Since(entry.StartedAt) > DefaultRestartStuckTimeout {
				m.lm.SetStatus(entry.Name, models.StatusError, "stuck in restarting state")
			}
		case models.StatusUnreachable:
			// pdap-docs unreachable feature: slow-poll for reachability
			// instead of restart-storming. The probe is short (3s TCP
			// dial) and throttled (60s initial, 5min cap) so it does not
			// add load to the monitor loop. On reachable -> Start() to
			// resume normal flow. See docs/PLAN-unreachable-handling.md.
			wg.Add(1)
			go func(e models.ServerEntry) {
				defer wg.Done()
				if err := bh.AcquirePermit(ctx); err != nil {
					return
				}
				defer bh.ReleasePermit()
				defer func() {
					if r := recover(); r != nil {
						m.logger.Error("panic in unreachable probe recovered",
							"server", e.Name, "panic", r)
					}
				}()
				m.maybeProbeUnreachable(ctx, e)
			}(entry)
		}
	}
	wg.Wait()
}

// maybeProbeUnreachable runs the slow-poll reachability probe for a
// StatusUnreachable backend. Throttled via serverState.nextReachProbeAt
// to UnreachableProbeInitial (60s) for the first
// UnreachableProbeInitialBurstCount (5) probes, then capped at
// UnreachableProbeMax (5min). On reachable, schedules a full Start
// (which runs the TCP pre-check + connectSafe MCP handshake; failure
// there will route the backend back to StatusUnreachable or
// StatusError via the same classifier).
//
// CRITICAL: this path does NOT touch serverState.restartCount,
// firstRestartAt, consecutiveFailures, or any other field driving the
// existing circuit-breaker. An unreachable backend can sit in slow-poll
// indefinitely without ever opening the circuit — that is the desired
// "stable yellow warning" behaviour. The breaker exists for genuine
// flapping (start succeeds, ping fails repeatedly), not for VPN-off
// network partitions.
func (m *Monitor) maybeProbeUnreachable(ctx context.Context, entry models.ServerEntry) {
	// Determine the probe URL for this backend:
	//   - HTTP/SSE backends: use Config.URL (original path).
	//   - TASK C1 — stdio backends skipped at spawn time (vsp-* with SAP_URL):
	//     use the SAP_URL extracted from Config.Env. This enables slow-poll
	//     retry-on-recovery for stdio SAP backends, which mirrors the HTTP path
	//     but targets the SAP application server TCP endpoint rather than the
	//     MCP transport URL (stdio has no transport URL).
	//   - All other stdio backends (no URL, no SAP_URL): defensive no-op.
	probeURL := unreachableProbeURL(entry)
	if probeURL == "" {
		// No URL to probe — skip (defensive guard for non-SAP stdio).
		return
	}

	now := time.Now()
	m.mu.Lock()
	state := m.getOrCreateState(entry.Name)
	if !state.nextReachProbeAt.IsZero() && now.Before(state.nextReachProbeAt) {
		m.mu.Unlock()
		return // throttled — next probe scheduled for later
	}
	state.reachProbeCount++
	probeCount := state.reachProbeCount
	delay := UnreachableProbeInitial
	if probeCount > UnreachableProbeInitialBurstCount {
		delay = UnreachableProbeMax
	}
	state.nextReachProbeAt = now.Add(delay)
	m.mu.Unlock()

	reachable := m.probeReachable(ctx, probeURL)
	if !reachable {
		m.logger.Debug("unreachable probe still failing",
			"server", entry.Name,
			"probe_count", probeCount,
			"next_probe_in", delay,
		)
		return
	}

	// Host is reachable again — reset probe state and trigger Start.
	m.logger.Info("backend reachable again, attempting Start",
		"server", entry.Name, "probe_count", probeCount)
	m.mu.Lock()
	state.reachProbeCount = 0
	state.nextReachProbeAt = time.Time{}
	m.mu.Unlock()

	// Start runs the full lifecycle (TCP pre-check + MCP handshake).
	// If anything fails, the classifier re-routes back to
	// StatusUnreachable / StatusError as appropriate. We don't touch
	// status here directly — Start owns that.
	if err := m.lm.Start(ctx, entry.Name); err != nil {
		m.logger.Warn("auto-restart after reachability recovery failed",
			"server", entry.Name, "error", err)
		return
	}

	// H-1 fix: the backend's BackendSupervisor.Serve returned ErrDoNotRestart
	// when the backend entered StatusUnreachable, removing it from the suture
	// tree. Now that Start() succeeded, re-register so suture owns crash-restart
	// policy again. AddBackendToSupervisor is idempotent (no-op if already
	// registered) and is a no-op when the supervisor is not active (legacy/test
	// path). Gate on SupervisorActive() to avoid calling into an uninitialised
	// supervisorTokens map.
	if m.lm.SupervisorActive() {
		m.lm.AddBackendToSupervisor(entry.Name, m.logger)
		m.logger.Info("re-registered backend with supervisor after reachability recovery",
			"server", entry.Name)
	}
}

// unreachableProbeURL returns the URL to TCP-probe for a backend in
// StatusUnreachable. Returns "" when there is nothing to probe (non-SAP stdio
// without a transport URL — should not enter slow-poll at all).
//
// Logic:
//   - HTTP/SSE backend (Config.URL != ""): use Config.URL (unchanged behaviour).
//   - Stdio backend with SAP_URL in env (TASK C1 skip-spawn path): use SAP_URL.
//   - Stdio backend without SAP_URL: return "" (defensive; should never be
//     StatusUnreachable via TASK C1 path, but guard against accidental entry).
func unreachableProbeURL(entry models.ServerEntry) string {
	if entry.Config.URL != "" {
		return entry.Config.URL
	}
	// stdio backend — check for SAP_URL (TASK C1 path).
	if entry.Config.Command != "" {
		sapURL, ok := entry.Config.SAPEnvURL()
		if ok {
			return sapURL
		}
	}
	return ""
}

// probeReachable performs a short TCP dial to verify reachability. Returns
// true if the dial succeeds within UnreachableProbeDialTimeout, false on
// any failure. Mirrors lifecycle.checkTCPReachable but without error
// classification — the only consumer of the result is a boolean decision.
func (m *Monitor) probeReachable(ctx context.Context, rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" || u.Scheme == "mcps" {
			port = "443"
		} else {
			port = "80"
		}
	}
	addr := net.JoinHostPort(host, port)
	dialCtx, cancel := context.WithTimeout(ctx, UnreachableProbeDialTimeout)
	defer cancel()
	// DIAL-FIX-1 (fanout-fixes T1.3): Dialer.Timeout mirrors the dialCtx
	// deadline so a Windows connectex to a black-holed host is capped at the
	// OS level, not just by ctx cancellation. Same rationale as
	// lifecycle.checkTCPReachable.
	conn, err := (&net.Dialer{Timeout: UnreachableProbeDialTimeout}).DialContext(dialCtx, "tcp", addr)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// checkOne performs a two-level health check on one server.
func (m *Monitor) checkOne(ctx context.Context, entry models.ServerEntry) {
	name := entry.Name

	// Level 1: MCP Ping.
	mcpOK := m.checkMCPPing(ctx, name)

	// Level 2: REST health (optional).
	restOK := true
	if entry.Config.RestURL != "" && entry.Config.HealthEndpoint != "" {
		healthURL, err := url.JoinPath(entry.Config.RestURL, entry.Config.HealthEndpoint)
		if err != nil {
			m.logger.Warn("invalid health URL", "server", name, "error", err)
		} else {
			restOK = m.checkRESTHealth(ctx, healthURL)
		}
	}

	// Level 3 (SAP stdio backends only): after MCP ping succeeds, verify SAP
	// reachability. For vsp-* backends, TCP-probe the SAP_URL host:port. For
	// sap-gui-* backends, call sap_list_sessions to confirm a live GUI session.
	// These probes run outside the mutex — they are blocking I/O and would
	// hold the lock across a network dial, which is forbidden.
	//
	// Both probes are only applied when MCP ping already succeeded (mcpOK),
	// so false-negatives from a dead child process can't reach here.
	var sapStatus models.ServerStatus
	var sapReason string
	if mcpOK {
		if sapURL, hasSAPURL := entry.Config.SAPEnvURL(); hasSAPURL && sapname.IsVSP(name) {
			// BUG-STDIO-1/3: vsp-* backend: TCP-probe the SAP application server.
			if !m.probeReachable(ctx, sapURL) {
				sapStatus = models.StatusUnreachable
				sapReason = "SAP host unreachable (VPN?)"
			}
		} else if sapname.IsSAPGUI(name) {
			// BUG-STDIO-2/4: sap-gui-* backend: check for an open GUI session.
			sapStatus, sapReason = m.checkSAPGUISession(ctx, name)
		}
	}

	m.mu.Lock()
	state := m.getOrCreateState(name)

	if mcpOK {
		state.consecutiveFailures = 0
		// Proven healthy: record the timestamp (sticky-circuit gate) and
		// clear the restart backoff so a recovered backend restarts
		// promptly if it fails again.
		state.lastHealthyAt = time.Now()
		state.nextRestartAllowedAt = time.Time{}
		// Track uptime start on first successful health check.
		if state.uptimeStart.IsZero() {
			state.uptimeStart = time.Now()
		}

		// SAP reachability blip mask — split into two independent counters
		// (review MEDIUM-2, design step 4). The streak bookkeeping + threshold
		// decision live in updateSAPStreaks (unit-testable without a live MCP
		// session); a handled verdict is published here, otherwise we fall
		// through to the normal Running/REST path with both streaks reset.
		if pub, reason, handled := m.updateSAPStreaks(state, sapStatus, sapReason); handled {
			m.mu.Unlock()
			m.lm.SetStatus(name, pub, reason)
			return
		}
		// SAP probe passed (or backend is non-SAP / explicitly running):
		// updateSAPStreaks already reset both streaks.
		m.mu.Unlock()

		if sapStatus == models.StatusRunning {
			// sap-gui explicit running: graceful tool-missing fallback, or an
			// active GUI session was found.
			m.lm.SetStatus(name, models.StatusRunning, "")
			return
		}

		if restOK {
			m.lm.SetStatus(name, models.StatusRunning, "")
		} else {
			m.lm.SetStatus(name, models.StatusDegraded, "REST health check failed")
		}
		return
	}

	// MCP ping failed.
	state.consecutiveFailures++
	failures := state.consecutiveFailures
	m.mu.Unlock()

	m.logger.Warn("health check failed", "server", name, "consecutive_failures", failures)

	if failures < m.ConsecutiveFailureThreshold {
		// Not enough failures yet — mark degraded if was running.
		if entry.Status == models.StatusRunning {
			m.lm.SetStatus(name, models.StatusDegraded, "MCP ping failed")
		}
		return
	}

	// Running-backend-lost-its-host transition (PLAN-unreachable-handling
	// follow-up): before restart-storming, check whether the host is
	// TCP-unreachable (e.g. VPN dropped mid-session). If so, route to
	// StatusUnreachable slow-poll instead of the restart path — the same
	// transport-vs-protocol distinction the lifecycle Start TCP pre-check
	// makes at startup, now applied to a backend that WAS running and lost
	// its host. Previously StatusUnreachable could only be entered at Start
	// time; a live HTTP backend whose host vanished would restart-storm into
	// StatusError instead. Only HTTP backends (Config.URL != "") can be
	// unreachable; stdio backends fall straight through to restart.
	//
	// Concurrency (CV MEDIUM): probeReachable does a blocking TCP dial
	// (UnreachableProbeDialTimeout) during the m.mu-unlocked window opened
	// at the consecutiveFailures++ above. This is race-free because checkAll
	// dispatches exactly one checkOne goroutine per server name per tick and
	// wg.Wait()s before the next tick — so no second checkOne for the same
	// name can mutate this serverState concurrently. The reset below is
	// therefore an unconditional write under the re-lock, not a read-modify
	// of a stale snapshot. Worst-case latency (CV LOW): if every HTTP backend
	// loses its host at once (VPN drop), each goroutine blocks here for up to
	// UnreachableProbeDialTimeout, bounded by the bulkhead permit count.
	if entry.Config.URL != "" && !m.probeReachable(ctx, entry.Config.URL) {
		m.logger.Warn("running backend host became unreachable; routing to slow-poll instead of restart",
			"server", name, "consecutive_failures", failures)
		// Reset consecutiveFailures so recovery starts clean. Do NOT touch
		// restartCount/firstRestartAt — the Unreachable path is explicitly
		// outside the circuit-breaker (see maybeProbeUnreachable).
		m.mu.Lock()
		state := m.getOrCreateState(name)
		state.consecutiveFailures = 0
		m.mu.Unlock()
		m.lm.SetStatus(name, models.StatusUnreachable, "host unreachable (slow-polling)")
		return
	}

	// Enough failures — attempt auto-restart.
	m.attemptRestart(ctx, name)
}

// attemptRestart tries to restart a server, respecting the circuit breaker.
func (m *Monitor) attemptRestart(ctx context.Context, name string) {
	// F2 fix: when the suture supervisor (P1.5 step 2) is active, IT owns
	// restart policy IN NORMAL CASES. Monitor's role here becomes
	// observational — we still track failures and mark degraded in
	// checkOne, but we usually DO NOT issue an independent Restart call.
	// Suture's Serve loop will see the session end and restart via its
	// FailureBackoff=15s policy. Without this guard, two Restart paths
	// race (suture + Monitor) and the Manager's `starting` guard
	// surfaces transient errors.
	//
	// EXCEPTION — stale-session recovery (2026-05-25): when consecutive
	// failures cross 2× the normal threshold and the supervisor has
	// clearly NOT acted, the monitor MUST issue lm.Restart directly to
	// force session refresh. Typical trigger: VPN flap leaves an
	// in-memory MCP session that pings fail on, but suture's Serve() is
	// still "alive" because the session object is technically not torn
	// down — suture only restarts on Serve() return, not on ping
	// failures. Without this exception the backend stays StatusDegraded
	// forever and the operator must manually POST /api/v1/servers/{name}
	// /restart. See docs/PLAN-unreachable-handling.md (post-feature
	// follow-up).
	if m.lm.SupervisorActive() {
		m.mu.Lock()
		state := m.getOrCreateState(name)
		failures := state.consecutiveFailures
		m.mu.Unlock()
		if failures < 2*m.ConsecutiveFailureThreshold {
			m.logger.Info("backend health failure detected; restart deferred to supervisor",
				"server", name, "consecutive_failures", failures)
			return
		}
		m.logger.Warn(
			"supervisor inactive on persistent ping failures; forcing monitor-initiated restart (stale-session recovery)",
			"server", name,
			"consecutive_failures", failures,
			"threshold", 2*m.ConsecutiveFailureThreshold,
		)
		// Fall through to the monitor-driven restart path below.
	}

	m.mu.Lock()
	state := m.getOrCreateState(name)

	// Backoff gate: if a prior attempt scheduled a cooldown that has not
	// elapsed, skip this restart. consecutiveFailures MUST be reset here
	// (HIGH-1) — otherwise the next checkOne sees failures still at/over
	// threshold and re-enters attemptRestart immediately, bypassing the
	// backoff entirely.
	if !state.nextRestartAllowedAt.IsZero() && time.Now().Before(state.nextRestartAllowedAt) {
		retryIn := time.Until(state.nextRestartAllowedAt).Round(time.Second)
		state.consecutiveFailures = 0
		m.mu.Unlock()
		m.lm.SetStatus(name, models.StatusError,
			fmt.Sprintf("restart backoff: retry in %s", retryIn))
		return
	}

	state.restartCount++
	state.consecutiveFailures = 0 // reset to avoid double-restart on transient post-restart failure
	state.lastCrashAt = time.Now()
	// Schedule the next allowed restart (exponential, capped). Applied to
	// every attempt so a spawn-OK-but-unreachable backend cannot storm.
	state.nextRestartAllowedAt = time.Now().Add(m.restartBackoff(state.restartCount))
	// Accumulate the current uptime window before resetting.
	if !state.uptimeStart.IsZero() {
		state.cumulativeUptime += time.Since(state.uptimeStart)
	}
	state.uptimeStart = time.Time{} // reset; will be set on next successful health check
	if state.firstRestartAt.IsZero() {
		state.firstRestartAt = time.Now()
	}

	if m.shouldOpenCircuit(state) {
		m.mu.Unlock()
		m.logger.Error("circuit breaker opened", "server", name,
			"restarts", state.restartCount, "threshold", m.CircuitBreakerThreshold)
		m.lm.SetStatus(name, models.StatusDisabled, fmt.Sprintf(
			"circuit breaker: %d restarts in %s", state.restartCount, m.CircuitBreakerWindow))
		return
	}
	m.mu.Unlock()

	m.lm.SetStatus(name, models.StatusRestarting, "auto-restart after health failure")
	m.logger.Info("auto-restarting server", "server", name)

	if err := m.lm.Restart(ctx, name); err != nil {
		m.logger.Error("auto-restart failed", "server", name, "error", err)

		m.mu.Lock()
		state := m.getOrCreateState(name)
		state.consecutiveFailedStarts++
		failedStarts := state.consecutiveFailedStarts
		m.mu.Unlock()

		if failedStarts >= m.ConsecutiveFailedStartsThreshold {
			// Review HIGH-2: clear the pending backoff so a disabled
			// server has no stale cooldown lingering if its status is
			// later flipped without going through ResetCircuit.
			m.mu.Lock()
			m.getOrCreateState(name).nextRestartAllowedAt = time.Time{}
			m.mu.Unlock()
			m.logger.Error("circuit breaker opened: consecutive startup failures",
				"server", name, "consecutive_failed_starts", failedStarts)
			m.lm.SetStatus(name, models.StatusDisabled, fmt.Sprintf(
				"circuit breaker: %d consecutive startup failures", failedStarts))
			return
		}
		m.lm.SetStatus(name, models.StatusError, err.Error())
		return
	}

	// Successful restart — reset the consecutive-failed-starts counter.
	m.mu.Lock()
	m.getOrCreateState(name).consecutiveFailedStarts = 0
	m.mu.Unlock()
}

// shouldOpenCircuit decides whether the circuit breaker should open.
//
// P0 sticky gate: the original time-window-only logic let a backend that
// spawns successfully but never becomes reachable storm forever — every
// window simply reset the counter (the CR-15 escape). The fix keys
// stickiness on whether the backend was ever PROVEN healthy since the
// current restart window began (lastHealthyAt vs firstRestartAt):
//
//   - never healthy this window  → open, regardless of the time window
//     (covers spawn-error AND spawn-OK-but-unreachable, since restartCount
//     is incremented before Restart() is even attempted).
//   - genuinely recovered this window → keep the original CR-15 window
//     behaviour (a flapper that exceeds threshold inside the window still
//     opens; once the window elapses it gets a fresh window).
//
// CRIT-1: when the window resets, lastHealthyAt MUST be zeroed too —
// otherwise a recovered-then-failed backend's stale lastHealthyAt is older
// than the new firstRestartAt and the sticky gate would falsely disable it
// permanently on the next threshold breach.
func (m *Monitor) shouldOpenCircuit(state *serverState) bool {
	if state.restartCount < m.CircuitBreakerThreshold { //nolint:intrange — threshold comparison not a range
		return false
	}
	// Never proven healthy since this restart window began → sticky open,
	// independent of the time window.
	if !state.lastHealthyAt.After(state.firstRestartAt) {
		return true
	}
	// Genuinely recovered this window: original CR-15 window logic.
	if time.Since(state.firstRestartAt) <= m.CircuitBreakerWindow {
		return true
	}
	// Window elapsed — start a new window with the current restart counted.
	// Zero lastHealthyAt so the sticky gate is evaluated against the NEW
	// window's start, not stale prior-window health (CRIT-1).
	state.restartCount = 1
	state.firstRestartAt = time.Now()
	state.lastHealthyAt = time.Time{}
	return false
}

// ResetCircuit resets the circuit breaker for a server and attempts to start it.
func (m *Monitor) ResetCircuit(ctx context.Context, name string) error {
	m.mu.Lock()
	state := m.getOrCreateState(name)
	state.restartCount = 0
	state.consecutiveFailures = 0
	state.firstRestartAt = time.Time{}
	state.lastCrashAt = time.Time{}          // clear crash history on circuit reset
	state.uptimeStart = time.Time{}          // uptime resets with circuit
	state.cumulativeUptime = 0               // clear accumulated uptime
	state.consecutiveFailedStarts = 0        // MED-1: stale count would re-disable
	state.lastHealthyAt = time.Time{}        // MED-1: stale health would skew gate
	state.nextRestartAllowedAt = time.Time{} // MED-1: clear pending backoff
	state.sapUnreachableStreak = 0           // clear SAP I/O-error streak on reset
	state.sapDegradedStreak = 0              // clear SAP "not logged in" streak on reset
	m.mu.Unlock()

	m.lm.SetStatus(name, models.StatusStopped, "")
	return m.lm.Start(ctx, name)
}

// getOrCreateState returns the state for a server, creating it if needed.
// Must be called with m.mu held.
func (m *Monitor) getOrCreateState(name string) *serverState {
	s, ok := m.states[name]
	if !ok {
		s = &serverState{}
		m.states[name] = s
	}
	return s
}

// checkMCPPing sends an MCP ping to a backend via its session.
func (m *Monitor) checkMCPPing(ctx context.Context, name string) bool {
	session, ok := m.lm.Session(name)
	if !ok {
		return false
	}

	pingCtx, cancel := context.WithTimeout(ctx, DefaultPingTimeout)
	defer cancel()

	if err := session.Ping(pingCtx, nil); err != nil {
		return false
	}
	return true
}

// sapGUISessionProbeToolName is the read-only MCP tool on the sap-gui backend
// that reports open SAP GUI sessions without side effects. Chosen over
// sap_get_session_info because it requires no index arguments and returns a
// list — zero items == no sessions open.
const sapGUISessionProbeToolName = "sap_list_sessions"

// mapSAPGUIResult maps the raw output of a sap_list_sessions CallTool call to a
// ServerStatus + reason pair. Extracted as a pure function so unit tests can
// exercise all five branches without spawning a real MCP subprocess.
//
// Branch table:
//
//	callErr contains "not found"/"unknown tool" → (StatusRunning,    "")            [tool absent — graceful fallback]
//	callErr != nil (other transport error)      → (StatusUnreachable, reason)
//	res.IsError == true                         → (StatusDegraded,    reason from text content)
//	logged-in session for this SID present      → (StatusRunning,     "")
//	sessions parsed but none logged-in for SID  → (StatusDegraded,    "no logged-in … for system <SID>")
//	no sessions parsed (empty / unparseable)    → (StatusDegraded,    "no SAP GUI session open …")   [fail-CLOSED]
//
// The verdict aggregates EVERY TextContent block (FastMCP returns one block per
// session), and an unparseable result fails CLOSED to degraded — never green —
// which is the inverse of the old fail-open that masked the always-running bug.
//
// serverName and logger are used only for the Warn log on the tool-absent path.
// Passing a nil logger suppresses that log (useful in tests).
func mapSAPGUIResult(res *mcp.CallToolResult, callErr error, serverName, expectSystem, expectUser, expectClient string, logger *slog.Logger) (models.ServerStatus, string) {
	if callErr != nil {
		errStr := callErr.Error()
		// Tool missing at runtime (backend too old / tool renamed) — fall back
		// to treating MCP ping alone as sufficient so we don't flip to
		// unreachable incorrectly.
		if strings.Contains(errStr, "not found") || strings.Contains(errStr, "unknown tool") {
			if logger != nil {
				logger.Warn("sap-gui session probe tool not available; falling back to MCP-ping-only health",
					"server", serverName, "tool", sapGUISessionProbeToolName)
			}
			return models.StatusRunning, ""
		}
		return models.StatusUnreachable, "sap-gui session probe failed: " + errStr
	}

	if res.IsError {
		// The MCP tool itself returned an error result (e.g. COM not available).
		reason := "no SAP GUI session (sap_list_sessions error)"
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok && tc.Text != "" {
				reason = "no SAP GUI session: " + tc.Text
				break
			}
		}
		return models.StatusDegraded, reason
	}

	// Parse EVERY content block. FastMCP serialises the tool's list[dict] return
	// as ONE TextContent block PER session (each a single JSON object) — NOT a
	// single JSON array — so reading only the first block (and treating a parse
	// failure as "running") made the badge permanently green regardless of login
	// state. Aggregate all blocks, then derive a fail-CLOSED verdict.
	recs, sawText := parseSAPSessions(res.Content)

	// SID unknown (name parse / env missing). A real sap-gui backend ALWAYS
	// carries a SID, so this is a misconfiguration. Fail CLOSED (review HIGH):
	// require at least one LOGGED-IN session (non-empty user) before reporting
	// Running — a bare login-screen window or a foreign SID must NOT green it.
	if strings.TrimSpace(expectSystem) == "" {
		for _, r := range recs {
			if strings.TrimSpace(r.User) != "" {
				return models.StatusRunning, ""
			}
		}
		if len(recs) == 0 {
			return models.StatusDegraded, sapNoSessionReason(sawText)
		}
		return models.StatusDegraded, "sap-gui backend not signed in (SID unknown)"
	}

	// Per-system verdict. SAP GUI Scripting exposes a SINGLE global engine, so
	// sap_list_sessions returns EVERY connection on the desktop — not just this
	// backend's system. The DISCRIMINATOR is the SAP system id (SID), because
	// deployments commonly share one SAP_USER + SAP_CLIENT across many systems
	// (e.g. NAUMOV/100 on CTC, Q25, S23, …). Require a LOGGED-IN session
	// (non-empty user) on THIS backend's SID.
	if sapSessionsMatch(recs, expectSystem, expectUser, expectClient) {
		return models.StatusRunning, ""
	}
	if len(recs) == 0 {
		return models.StatusDegraded, sapNoSessionReason(sawText)
	}
	return models.StatusDegraded, fmt.Sprintf(
		"no logged-in SAP GUI session for system %s (client %s) — SAP Logon not signed in",
		expectSystem, expectClient)
}

// sapNoSessionReason returns the degraded reason for a successful probe that
// yielded no parseable sessions: distinguishes a genuinely empty list from an
// empty/unrecognised response.
func sapNoSessionReason(sawText bool) string {
	if sawText {
		return "no SAP GUI session open"
	}
	return "no SAP GUI session open (empty response)"
}

// sapSessionRec is the subset of one sap_list_sessions row that drives the
// health verdict.
type sapSessionRec struct {
	SystemName string `json:"system_name"`
	User       string `json:"user"`
	Client     string `json:"client"`
}

// parseSAPSessions extracts session records from a sap_list_sessions result.
// FastMCP emits one TextContent block per list element (each a single JSON
// object); older/test callers may emit a single block holding a JSON array.
// Each non-empty block is tried as an array first, then as a single object.
// Unparseable blocks are SKIPPED (never counted as a session) so a malformed
// result fails CLOSED to "degraded" instead of masking as "running" — the
// fail-open that previously hid the always-green bug. sawText reports whether
// any non-empty text block was present at all.
func parseSAPSessions(contents []mcp.Content) (recs []sapSessionRec, sawText bool) {
	for _, c := range contents {
		tc, ok := c.(*mcp.TextContent)
		if !ok {
			continue
		}
		text := strings.TrimSpace(tc.Text)
		if text == "" {
			continue
		}
		sawText = true
		if text == "[]" {
			continue
		}
		var arr []sapSessionRec
		if err := json.Unmarshal([]byte(text), &arr); err == nil {
			recs = append(recs, arr...)
			continue
		}
		var one sapSessionRec
		if err := json.Unmarshal([]byte(text), &one); err == nil {
			recs = append(recs, one)
		}
	}
	return recs, sawText
}

// sapSessionsMatch reports whether recs contains at least one LOGGED-IN session
// for the backend's own SAP system (expectSystem != ""). A session qualifies
// when its system_name equals expectSystem (the SID, case-insensitive) AND it
// carries a non-empty user (empty user = window still at the SAP login screen).
// When expectUser/expectClient are known they must also match (defence in depth).
// The expected values are normalised (TrimSpace) up front so stray whitespace in
// SAP_USER / SAP_CLIENT env (review MEDIUM) can't strand a genuinely logged-in
// backend in Degraded.
func sapSessionsMatch(recs []sapSessionRec, expectSystem, expectUser, expectClient string) bool {
	expectSystem = strings.TrimSpace(expectSystem)
	expectUser = strings.TrimSpace(expectUser)
	expectClient = strings.TrimSpace(expectClient)
	for _, s := range recs {
		if !strings.EqualFold(strings.TrimSpace(s.SystemName), expectSystem) {
			continue
		}
		if strings.TrimSpace(s.User) == "" {
			continue // window at the login screen — not signed in
		}
		if expectUser != "" && !strings.EqualFold(strings.TrimSpace(s.User), expectUser) {
			continue
		}
		if expectClient != "" && strings.TrimSpace(s.Client) != expectClient {
			continue
		}
		return true
	}
	return false
}

// sapSnapshotTTL returns the cache lifetime for the shared SAP snapshot.
// Set to half the monitor interval so one cached result covers all sap-gui-*
// goroutines in a single ~30s cycle without leaking into the next cycle.
// Returns 0 when interval is 0 (tests with interval=0 still deduplicate via
// singleflight within a single CheckOnce call).
func (m *Monitor) sapSnapshotTTL() time.Duration {
	if m.interval <= 0 {
		return 0
	}
	return m.interval / 2
}

// getSAPSnapshot returns a shared sap_list_sessions result for the current
// monitor cycle. The first sap-gui-* goroutine to call this probes the STA
// engine via probeSAPOnce; all subsequent callers within the TTL window read
// the cached result without re-probing. singleflight deduplicates concurrent
// callers that arrive before the cache is warm (design step 1).
//
// CRITICAL: the cache is written — with a fresh takenAt — even when the probe
// errors, so subsequent callers within the TTL do not re-invoke probeSAPOnce
// and regress to 8 parallel STA-thread contenders.
func (m *Monitor) getSAPSnapshot(ctx context.Context) (*mcp.CallToolResult, error) {
	// Fast path: cache hit. A non-positive TTL means "no cache" — always
	// re-probe (review F1: ttl==0 must NOT mean cache-forever); singleflight
	// still collapses concurrent callers within one cycle.
	ttl := m.sapSnapshotTTL()
	m.sapSnapMu.Lock()
	if ttl > 0 && !m.sapSnap.takenAt.IsZero() && time.Since(m.sapSnap.takenAt) < ttl {
		res, err := m.sapSnap.res, m.sapSnap.err
		m.sapSnapMu.Unlock()
		return res, err
	}
	m.sapSnapMu.Unlock()

	// Slow path: probe once; singleflight collapses concurrent callers.
	// Tests inject sapProbeFn to stub the leaf probe; production uses probeSAPOnce.
	probe := m.probeSAPOnce
	if m.sapProbeFn != nil {
		probe = m.sapProbeFn
	}
	v, err, _ := m.sapSnapSF.Do("sap-gui-snapshot", func() (any, error) {
		res, probeErr := probe(ctx)
		// Write the cache unconditionally (even on error) so subsequent
		// callers within the TTL don't re-probe (design step 1, CRITICAL note).
		m.sapSnapMu.Lock()
		m.sapSnap = sapSnapshot{res: res, err: probeErr, takenAt: time.Now()}
		m.sapSnapMu.Unlock()
		return res, probeErr
	})
	var res *mcp.CallToolResult
	if v != nil {
		res = v.(*mcp.CallToolResult)
	}
	return res, err
}

// probeSAPOnce selects the best available sap-gui-* backend, probes
// sap_list_sessions through it, and returns the result. The backend
// selection uses a fail-count eviction policy (design step 2, review HIGH):
// targets with more consecutive probe errors sink to the back of the
// candidate list so a session that previously hung the STA thread is not
// tried first next cycle. A 2s per-attempt sub-timeout prevents one
// COM-hung backend from consuming the full 5s budget.
//
// sapTargetFails is only written here — on the singleflight-serialised
// probe path — so no extra mutex is needed beyond the singleflight dedup.
func (m *Monitor) probeSAPOnce(ctx context.Context) (*mcp.CallToolResult, error) {
	// Collect live sap-gui-* sessions (any status OK — a StatusError backend can
	// still recover from a shared healthy snapshot).
	sessions := make(map[string]*mcp.ClientSession)
	var names []string
	for _, e := range m.lm.Entries() {
		if !sapname.IsSAPGUI(e.Name) {
			continue
		}
		if sess, ok := m.lm.Session(e.Name); ok {
			sessions[e.Name] = sess
			names = append(names, e.Name)
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no live sap-gui session for snapshot probe")
	}

	// Order by the eviction policy (review HIGH + F2): ascending fail-count,
	// last-good target first among ties, then by name.
	order := orderSAPCandidates(names, m.sapTargetFails, m.sapLastGoodTarget)

	budget := DefaultPingTimeout
	budgetStart := time.Now()
	var lastRes *mcp.CallToolResult
	var lastErr error

	for _, name := range order {
		remaining := budget - time.Since(budgetStart)
		if remaining <= 0 {
			break
		}
		// Cap each attempt at 2s so one COM-hung target cannot eat the full
		// budget and starve the healthy session behind it.
		perAttempt := min(remaining, 2*time.Second)
		attemptCtx, cancel := context.WithTimeout(ctx, perAttempt)
		res, err := sessions[name].CallTool(attemptCtx, &mcp.CallToolParams{
			Name: sapGUISessionProbeToolName,
		})
		cancel()

		if err == nil {
			// Success: this target is healthy — float it to the front next cycle.
			m.sapTargetFails[name] = 0
			m.sapLastGoodTarget = name
			return res, nil
		}
		// Failure: deprioritise this target next cycle.
		m.sapTargetFails[name]++
		lastRes, lastErr = res, err
	}

	return lastRes, lastErr
}

// orderSAPCandidates returns backend names ordered for probe preference:
// ascending fail-count first (a target that previously hung the STA thread
// sinks to the back), the last-good target first among equal fail-counts
// (review F2: wires sapLastGoodTarget), then lexicographically by name. Pure
// (no I/O, no receiver) so the ordering/eviction policy is unit-testable.
func orderSAPCandidates(names []string, fails map[string]int, lastGood string) []string {
	ordered := append([]string(nil), names...)
	sort.SliceStable(ordered, func(i, j int) bool {
		fi, fj := fails[ordered[i]], fails[ordered[j]]
		if fi != fj {
			return fi < fj
		}
		if (ordered[i] == lastGood) != (ordered[j] == lastGood) {
			return ordered[i] == lastGood // last-good wins the tie
		}
		return ordered[i] < ordered[j]
	})
	return ordered
}

// updateSAPStreaks applies a SAP probe verdict to the two independent streak
// counters and returns the status/reason to publish plus whether the SAP path
// handled the verdict (review MEDIUM-2, design step 4). The caller holds m.mu.
//
//   - StatusUnreachable — probe I/O failure (transport error / timeout);
//     tolerated up to SAPProbeFailureThreshold (3). VPN/host blips fit here.
//     vsp-* only ever yields this, so it rides the I/O-error streak unchanged.
//   - StatusDegraded — RELIABLE "not logged in" from a SUCCESSFUL probe;
//     tolerated up to SAPDegradedConfirmThreshold (2). One tolerated tick
//     absorbs the race where the STA engine returns the list a split-second
//     before the session appears.
//   - anything else (StatusRunning / empty) — a good outcome: reset BOTH
//     streaks and return handled=false so the caller runs its Running/REST path.
//
// The two counters are reset across each other so an alternating
// Unreachable/Degraded sequence cannot accumulate toward either threshold.
func (m *Monitor) updateSAPStreaks(state *serverState, sapStatus models.ServerStatus, sapReason string) (pub models.ServerStatus, reason string, handled bool) {
	switch sapStatus {
	case models.StatusUnreachable:
		state.sapUnreachableStreak++
		state.sapDegradedStreak = 0
		if state.sapUnreachableStreak < m.SAPProbeFailureThreshold {
			return models.StatusRunning, "", true
		}
		return models.StatusDegraded, sapReason, true
	case models.StatusDegraded:
		state.sapDegradedStreak++
		state.sapUnreachableStreak = 0
		if state.sapDegradedStreak < m.SAPDegradedConfirmThreshold {
			return models.StatusRunning, "", true
		}
		return models.StatusDegraded, sapReason, true
	}
	state.sapUnreachableStreak = 0
	state.sapDegradedStreak = 0
	return "", "", false
}

// checkSAPGUISession derives the sap-gui-* verdict from the shared snapshot
// produced by getSAPSnapshot (design step 3). One probe per cycle is shared
// across all 8 sap-gui-* backends; each backend applies mapSAPGUIResult with
// its own expected identity to derive its per-system verdict.
func (m *Monitor) checkSAPGUISession(ctx context.Context, name string) (models.ServerStatus, string) {
	expectSystem, expectUser, expectClient := m.sapExpectedIdentity(name)
	res, err := m.getSAPSnapshot(ctx)
	return mapSAPGUIResult(res, err, name, expectSystem, expectUser, expectClient, m.logger)
}

// sapExpectedIdentity returns the SAP system id (SID), SAP_USER and SAP_CLIENT
// this backend owns. user/client come from Config.Env; the SID is derived from
// the backend name (sap-gui-<SID>-<client>) because deployments commonly share
// one SAP_USER + SAP_CLIENT across many systems, so the SID is the only stable
// per-system discriminator. Returns "" for any field that cannot be resolved —
// callers treat an empty SID as "unknown" and fall back to any-session-counts.
func (m *Monitor) sapExpectedIdentity(name string) (system, user, client string) {
	for _, e := range m.lm.Entries() {
		if e.Name == name {
			user, _ = e.Config.EnvValue("SAP_USER")
			client, _ = e.Config.EnvValue("SAP_CLIENT")
			return sapSIDFromName(name, client), user, client
		}
	}
	return "", "", ""
}

// sapSIDFromName extracts the SAP system id from a sap-gui-* backend name of
// the form "sap-gui-<SID>-<client>" (e.g. "sap-gui-CTC-100" -> "CTC"), matching
// the system_name reported by sap_list_sessions. Strips the known client suffix
// when available, else the final "-<segment>". Returns "" when the name does
// not carry the sap-gui- prefix.
func sapSIDFromName(name, client string) string {
	const prefix = "sap-gui-"
	if !strings.HasPrefix(name, prefix) {
		return ""
	}
	rest := name[len(prefix):] // e.g. "CTC-100"
	if client != "" && strings.HasSuffix(rest, "-"+client) {
		return strings.TrimSuffix(rest, "-"+client)
	}
	if i := strings.LastIndex(rest, "-"); i > 0 {
		return rest[:i]
	}
	return rest
}

// checkRESTHealth performs an HTTP GET to the health endpoint.
func (m *Monitor) checkRESTHealth(ctx context.Context, url string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return false
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// GatewayUptime returns the time since the monitor was created.
func (m *Monitor) GatewayUptime() time.Duration {
	return time.Since(m.startedAt)
}

// StartedAt returns the wall-clock moment the monitor was constructed.
// Read without lock: startedAt is written once at construction.
func (m *Monitor) StartedAt() time.Time { return m.startedAt }

// AllServerMetrics returns metrics for all servers in entries.
// Uses entries (from lm.Entries()) as the authoritative server list;
// looks up internal state for crash/uptime data. Servers with no
// health-check state yet get zero values.
func (m *Monitor) AllServerMetrics(entries []models.ServerEntry) []models.ServerMetricsInfo {
	m.mu.Lock()
	defer m.mu.Unlock()

	result := make([]models.ServerMetricsInfo, len(entries))
	for i, e := range entries {
		info := models.ServerMetricsInfo{
			Name: e.Name,
		}

		state, ok := m.states[e.Name]
		if ok {
			info.RestartCount = state.restartCount
			if !state.lastCrashAt.IsZero() {
				t := state.lastCrashAt
				info.LastCrashAt = &t
			}
			// Current uptime window (since last recovery or initial start).
			var currentUptime time.Duration
			if !state.uptimeStart.IsZero() {
				currentUptime = time.Since(state.uptimeStart)
				info.Uptime = models.Duration(currentUptime)
			}
			// MTBF = total operational time / crash count.
			// Uses cumulative uptime across all windows plus the current window.
			// Returns 0 when no failures recorded.
			if state.restartCount > 0 {
				totalUptime := state.cumulativeUptime + currentUptime
				info.MTBF = models.Duration(totalUptime / time.Duration(state.restartCount))
			}
		}

		result[i] = info
	}
	return result
}
