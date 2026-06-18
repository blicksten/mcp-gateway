// Package health implements the health monitor for backend MCP servers.
// It periodically pings backends and manages state transitions
// including auto-restart and circuit breaking.
package health

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"mcp-gateway/internal/models"
	"mcp-gateway/internal/sapname"

	"github.com/failsafe-go/failsafe-go/bulkhead"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Default thresholds.
const (
	DefaultPingTimeout          = 5 * time.Second
	DefaultConsecutiveFailures  = 3
	// DefaultSAPProbeFailures is the consecutive-failure threshold for the SAP
	// reachability probe (vsp host-dial / sap-gui session check) before a live,
	// MCP-ping-OK SAP backend is marked Degraded. Mirrors the MCP-ping threshold
	// so a single transient VPN/host blip does not flap the status. SAP backends
	// are deliberately NEVER routed to StatusUnreachable (stdio has no slow-poll
	// recovery); they ride the same Running/Degraded loop as every other backend,
	// which re-probes every tick and self-recovers.
	DefaultSAPProbeFailures     = 3
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
	// sapProbeFailures counts consecutive SAP reachability-probe failures
	// (vsp host-dial / sap-gui session check) while the MCP child is alive
	// (mcpOK). Drives the Degraded threshold in checkOne so a transient blip is
	// tolerated; reset to 0 on a successful SAP probe / non-SAP backend and by
	// ResetCircuit. SAP backends never use the StatusUnreachable slow-poll.
	sapProbeFailures int
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

	// Configurable thresholds (exported for testing). These MUST be set
	// only before NewMonitor's caller starts the monitor (Run/CheckOnce)
	// and treated as read-only afterwards — they are read without the
	// mutex on the health-check path (review MED-1).
	ConsecutiveFailureThreshold      int
	CircuitBreakerThreshold          int
	CircuitBreakerWindow             time.Duration
	ConsecutiveFailedStartsThreshold int
	// SAPProbeFailureThreshold is the consecutive SAP-probe-failure count
	// before a live (MCP-ping-OK) SAP backend is marked Degraded.
	SAPProbeFailureThreshold int
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
		ConsecutiveFailureThreshold:      DefaultConsecutiveFailures,
		CircuitBreakerThreshold:          DefaultCircuitBreakerThresh,
		CircuitBreakerWindow:             DefaultCircuitBreakerWindow,
		ConsecutiveFailedStartsThreshold: DefaultConsecutiveFailedStarts,
		SAPProbeFailureThreshold:         DefaultSAPProbeFailures,
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

		// SAP reachability (vsp-*/sap-gui-*). The MCP child is alive (mcpOK), so
		// a transient SAP-host/GUI-session failure is a DEGRADED condition, never
		// a terminal StatusUnreachable: stdio backends have no slow-poll recovery
		// (maybeProbeUnreachable is HTTP-only), so flipping them Unreachable on a
		// single 3s probe stranded a live, ping-OK backend until a manual
		// restart. Feed the SAP probe into the SAME running/degraded loop every
		// backend uses — a short consecutive-failure threshold absorbs VPN/host
		// blips, and Degraded stays routable and is re-probed every tick, so it
		// self-recovers when SAP returns.
		sapBad := sapStatus == models.StatusUnreachable || sapStatus == models.StatusDegraded
		if sapBad {
			state.sapProbeFailures++
			sapFails := state.sapProbeFailures
			m.mu.Unlock()
			if sapFails < m.SAPProbeFailureThreshold {
				// Tolerate a transient blip — keep serving this tick.
				m.lm.SetStatus(name, models.StatusRunning, "")
				return
			}
			m.lm.SetStatus(name, models.StatusDegraded, sapReason)
			return
		}
		// SAP probe passed (or backend is non-SAP / explicitly running).
		state.sapProbeFailures = 0
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
	state.sapProbeFailures = 0               // clear SAP-probe streak on reset
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
// Branch table (mirrors checkSAPGUISession's original inline logic):
//
//	callErr contains "not found"/"unknown tool" → (StatusRunning,  "")             [tool absent — graceful fallback]
//	callErr != nil (other transport error)      → (StatusUnreachable, reason)
//	res.IsError == true                         → (StatusDegraded,   reason from text content)
//	res text == "[]" or ""                      → (StatusDegraded,   "no SAP GUI session open")
//	res text non-empty                          → (StatusRunning,    "")
//
// serverName and logger are used only for the Warn log on the tool-absent path.
// Passing a nil logger suppresses that log (useful in tests).
func mapSAPGUIResult(res *mcp.CallToolResult, callErr error, serverName string, logger *slog.Logger) (models.ServerStatus, string) {
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

	// The tool returns a JSON array. An empty array means no sessions are open.
	// We check the raw text content for "[]" as the simplest non-mutating check.
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

	// Non-error result: inspect text content for an empty list.
	for _, c := range res.Content {
		tc, ok := c.(*mcp.TextContent)
		if !ok {
			continue
		}
		text := strings.TrimSpace(tc.Text)
		if text == "[]" || text == "" {
			return models.StatusDegraded, "no SAP GUI session open"
		}
		// Non-empty list → at least one session is active.
		return models.StatusRunning, ""
	}

	// No content at all — treat as no sessions.
	return models.StatusDegraded, "no SAP GUI session open (empty response)"
}

// checkSAPGUISession calls sap_list_sessions on a sap-gui-* backend and
// interprets the result:
//   - >=1 sessions in the list → (StatusRunning, "")
//   - zero sessions or "no session"/"not connected" error → (StatusDegraded, reason)
//   - tool missing / transport error → (StatusUnreachable, reason)
//
// Returns the status to apply and the reason string. Callers must have already
// confirmed MCP ping succeeded before calling this. The ctx passed in should
// already carry an appropriate deadline.
func (m *Monitor) checkSAPGUISession(ctx context.Context, name string) (models.ServerStatus, string) {
	session, ok := m.lm.Session(name)
	if !ok {
		return models.StatusUnreachable, "no MCP session for sap-gui probe"
	}

	callCtx, cancel := context.WithTimeout(ctx, DefaultPingTimeout)
	defer cancel()

	result, err := session.CallTool(callCtx, &mcp.CallToolParams{
		Name: sapGUISessionProbeToolName,
	})
	return mapSAPGUIResult(result, err, name, m.logger)
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
