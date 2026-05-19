// Package health implements the health monitor for backend MCP servers.
// It periodically pings backends and manages state transitions
// including auto-restart and circuit breaking.
package health

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"mcp-gateway/internal/models"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Default thresholds.
const (
	DefaultPingTimeout          = 5 * time.Second
	DefaultConsecutiveFailures  = 3
	DefaultCircuitBreakerThresh = 5
	DefaultCircuitBreakerWindow = 300 * time.Second
	DefaultRESTHealthTimeout    = 5 * time.Second
	DefaultMaxConcurrentChecks  = 20
	DefaultRestartStuckTimeout  = 60 * time.Second
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
func (m *Monitor) checkAll(ctx context.Context) {
	entries := m.lm.Entries()
	sem := make(chan struct{}, DefaultMaxConcurrentChecks)

	var wg sync.WaitGroup
	for _, entry := range entries {
		switch entry.Status {
		case models.StatusRunning, models.StatusDegraded, models.StatusError:
			wg.Add(1)
			go func(e models.ServerEntry) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				// Panic isolation (R2 — "the heart fails last"): a panic in
				// one backend's health check must never take down the
				// monitor loop. Registered LAST so on unwind it runs FIRST
				// (recovers), then <-sem, then wg.Done() — no leaked
				// semaphore slot, no wg.Wait() deadlock.
				defer func() {
					if r := recover(); r != nil {
						m.logger.Error("panic in health check recovered",
							"server", e.Name, "panic", r)
						// Review HIGH-1: SetStatus must not be able to
						// re-panic out of this defer — a re-panic would
						// bypass the <-sem and wg.Done() defers (semaphore
						// leak + wg.Wait deadlock). Contain it.
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
		}
	}
	wg.Wait()
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
		m.mu.Unlock()

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

	// Enough failures — attempt auto-restart.
	m.attemptRestart(ctx, name)
}

// attemptRestart tries to restart a server, respecting the circuit breaker.
func (m *Monitor) attemptRestart(ctx context.Context, name string) {
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
