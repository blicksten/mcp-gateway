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
	DefaultPingTimeout           = 5 * time.Second
	DefaultConsecutiveFailures   = 3
	DefaultCircuitBreakerThresh  = 5
	DefaultCircuitBreakerWindow  = 300 * time.Second
	DefaultRESTHealthTimeout     = 5 * time.Second
	DefaultMaxConcurrentChecks   = 20
	DefaultRestartStuckTimeout   = 60 * time.Second
)

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
}

// Monitor periodically checks backend server health and manages
// state transitions including auto-restart and circuit breaking.
type Monitor struct {
	lm           LifecycleManager
	logger       *slog.Logger
	interval     time.Duration
	httpClient   *http.Client
	startedAt    time.Time // written once at construction, never mutated — read without lock is safe

	mu     sync.Mutex
	states map[string]*serverState

	// Configurable thresholds (exported for testing).
	ConsecutiveFailureThreshold int
	CircuitBreakerThreshold     int
	CircuitBreakerWindow        time.Duration
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
		states: make(map[string]*serverState),
		ConsecutiveFailureThreshold: DefaultConsecutiveFailures,
		CircuitBreakerThreshold:     DefaultCircuitBreakerThresh,
		CircuitBreakerWindow:        DefaultCircuitBreakerWindow,
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

	state.restartCount++
	state.consecutiveFailures = 0 // reset to avoid double-restart on transient post-restart failure
	state.lastCrashAt = time.Now()
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
		m.lm.SetStatus(name, models.StatusError, err.Error())
	}
}

// shouldOpenCircuit checks if the restart count exceeds the threshold
// within the circuit breaker window.
func (m *Monitor) shouldOpenCircuit(state *serverState) bool {
	if state.restartCount < m.CircuitBreakerThreshold { //nolint:intrange — threshold comparison not a range
		return false
	}
	// Check if restarts happened within the window.
	if time.Since(state.firstRestartAt) <= m.CircuitBreakerWindow {
		return true
	}
	// CR-15 fix: window elapsed — start new window with current restart counted.
	state.restartCount = 1
	state.firstRestartAt = time.Now()
	return false
}

// ResetCircuit resets the circuit breaker for a server and attempts to start it.
func (m *Monitor) ResetCircuit(ctx context.Context, name string) error {
	m.mu.Lock()
	state := m.getOrCreateState(name)
	state.restartCount = 0
	state.consecutiveFailures = 0
	state.firstRestartAt = time.Time{}
	state.lastCrashAt = time.Time{}   // clear crash history on circuit reset
	state.uptimeStart = time.Time{}   // uptime resets with circuit
	state.cumulativeUptime = 0        // clear accumulated uptime
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
