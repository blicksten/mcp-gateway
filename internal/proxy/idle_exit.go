// Package proxy: idle exit monitor (TASK A).
//
// When no MCP client has been active for a configurable idle window, the
// gateway triggers its own graceful shutdown. The JobObject KILL_ON_JOB_CLOSE
// then tears down all backend children (closes leak path L1). The external
// reaper (TASK B, scripts/gateway-orphan-reaper.ps1) is the primary safety
// net; this monitor is a conservative UX optimisation that frees backends
// sooner.
//
// Env: MCP_GATEWAY_IDLE_EXIT_SECONDS (default 300, 0 = disabled).
//
// Guards enforced (per PAL review in DESIGN-mcp-gateway-process-ownership.md):
//   a. Client count: exits only when ClientCounter.ClientCount() == 0.
//      NOTE: the wired counter (sessionCountAdapter over
//      ResumableStreamableHTTPHandler.SessionCount()) counts only
//      STREAMABLE-HTTP (/mcp) sessions. Legacy SSE (/sse) clients are NOT
//      counted — acceptable because Claude Code uses streamable HTTP only.
//      A client that connects via /mcp and holds a GET notification stream
//      keeps a registered session, so SessionCount()>0 and the monitor will
//      not exit while it is connected.
//   b. Inflight guard: never exits while any tool call is in flight.
//   c. Reconnect-flap debounce: idle timer resets on any router activity
//      (every Call/CallDirect updates router.LastCallTime).
//   d. heartbeat_age semantics: idle is measured from last router activity.
//
// Wiring: call Gateway.ConfigureIdleExit then Gateway.ServeIdleExit(ctx).
// Both are typically called from the daemon's main run-loop alongside
// apiServer.ListenAndServe. Because cmd/mcp-gateway/main.go carries
// uncommitted concurrent changes (shared-worktree constraint), the final
// wiring call is deferred to a follow-on commit. The implementation and
// tests are complete here.
package proxy

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"time"
)

const (
	// idleExitDefaultWindow is the conservative default idle window.
	// A 5-minute quiet period with zero connected clients and zero inflight
	// calls is a strong signal that all VSCode windows have closed.
	idleExitDefaultWindow = 5 * time.Minute

	// idleExitCheckInterval is how often the monitor checks conditions.
	// 30 s is much finer than the 5-minute default window, so the actual
	// exit fires within 30 s of the idle window elapsing.
	idleExitCheckInterval = 30 * time.Second
)

// ClientCounter is implemented by anything that can report the number of
// currently connected MCP clients.
//
// The wired implementation (sessionCountAdapter in internal/api) delegates to
// ResumableStreamableHTTPHandler.SessionCount(), which counts only active
// STREAMABLE-HTTP (/mcp) sessions. Legacy SSE (/sse) clients are NOT counted
// — acceptable because Claude Code uses streamable HTTP only. A client that
// connects via /mcp and holds a GET notification stream keeps a registered
// session, so SessionCount()>0 and the monitor will not exit while it is
// connected.
//
// When no counter is wired (nil), ConfigureIdleExit substitutes
// nopClientCounter which always reports 1 (conservative: assume a client is
// present and never exit on the client-count guard alone). Callers should
// wire a real counter via Gateway.ConfigureIdleExit for correct behaviour.
type ClientCounter interface {
	ClientCount() int
}

// nopClientCounter is the safe default: reports 1 client so the monitor
// never exits when no real counter has been wired.
type nopClientCounter struct{}

func (nopClientCounter) ClientCount() int { return 1 }

// shouldExit is the pure, testable decision function. It returns true only
// when ALL of the following hold:
//   - clients == 0: no MCP client session is connected;
//   - inflight == 0: no tool call is currently executing;
//   - idleElapsed >= window: the idle window has elapsed since the last
//     router activity (last Call/CallDirect invocation or startup).
//
// Any single guard failing keeps the gateway alive — a conservative AND-gate.
func shouldExit(clients, inflight int64, idleElapsed, window time.Duration) bool {
	if clients > 0 {
		return false
	}
	if inflight > 0 {
		return false
	}
	return idleElapsed >= window
}

// idleExitConfig holds validated configuration for the idle exit monitor.
type idleExitConfig struct {
	window   time.Duration // idle window; 0 means disabled
	counter  ClientCounter
	shutdown func()        // graceful-shutdown trigger (same fn as signal cancel)
	logger   *slog.Logger
}

// parseIdleExitWindow reads MCP_GATEWAY_IDLE_EXIT_SECONDS.
// Invalid or negative values fall back to the default (logged as warn).
// Zero disables the monitor entirely.
func parseIdleExitWindow(logger *slog.Logger) time.Duration {
	raw := os.Getenv("MCP_GATEWAY_IDLE_EXIT_SECONDS")
	if raw == "" {
		return idleExitDefaultWindow
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		logger.Warn("MCP_GATEWAY_IDLE_EXIT_SECONDS not an integer; using default",
			"value", raw,
			"default_seconds", int(idleExitDefaultWindow.Seconds()))
		return idleExitDefaultWindow
	}
	if n < 0 {
		logger.Warn("MCP_GATEWAY_IDLE_EXIT_SECONDS must be >= 0; using default",
			"value", raw,
			"default_seconds", int(idleExitDefaultWindow.Seconds()))
		return idleExitDefaultWindow
	}
	if n == 0 {
		logger.Info("idle exit monitor disabled (MCP_GATEWAY_IDLE_EXIT_SECONDS=0)")
		return 0
	}
	return time.Duration(n) * time.Second
}

// inflightReporter is the subset of *router.Router used by the monitor.
// Satisfied by the real router and by test doubles.
type inflightReporter interface {
	InflightCalls() int64
	LastCallTime() time.Time
}

// idleExitMonitor runs as a background goroutine (started via ServeIdleExit).
// It polls every idleExitCheckInterval and triggers graceful shutdown when
// shouldExit returns true.
type idleExitMonitor struct {
	cfg    idleExitConfig
	router inflightReporter

	// lastActivity is the last-known activity time from the router.
	// Protected by mu; reset by noteActivity whenever the router's
	// LastCallTime advances (guard c/d: reconnect-flap debounce).
	mu           sync.Mutex
	lastActivity time.Time
}

// newIdleExitMonitor constructs the monitor. counter may be nil (safe
// default nopClientCounter is applied). router must not be nil.
func newIdleExitMonitor(cfg idleExitConfig, r inflightReporter) *idleExitMonitor {
	if cfg.counter == nil {
		cfg.counter = nopClientCounter{}
	}
	return &idleExitMonitor{
		cfg:          cfg,
		router:       r,
		lastActivity: time.Now(),
	}
}

// noteActivity records that router activity was observed at time t.
// Monotonically advances lastActivity; never goes backwards.
func (m *idleExitMonitor) noteActivity(t time.Time) {
	m.mu.Lock()
	if t.After(m.lastActivity) {
		m.lastActivity = t
	}
	m.mu.Unlock()
}

// idleSince returns how long the monitor has been idle (no router activity).
func (m *idleExitMonitor) idleSince() time.Duration {
	m.mu.Lock()
	last := m.lastActivity
	m.mu.Unlock()
	return time.Since(last)
}

// run is the monitor loop. Blocks until ctx is cancelled or until it
// triggers a graceful shutdown (at most once).
func (m *idleExitMonitor) run(ctx context.Context) {
	// M-2: recover from any panic in tick() or shutdown so the daemon does not
	// crash silently — the monitor simply stops, which is safe (the daemon
	// continues serving; the external reaper is the primary safety net).
	if m.cfg.logger != nil {
		defer func() {
			if r := recover(); r != nil {
				m.cfg.logger.Error("idle exit monitor panicked; monitor stopped", "error", r)
			}
		}()
	}

	ticker := time.NewTicker(idleExitCheckInterval)
	defer ticker.Stop()

	m.cfg.logger.Info("idle exit monitor started",
		"window_seconds", int(m.cfg.window.Seconds()),
		"check_interval_seconds", int(idleExitCheckInterval.Seconds()))

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if m.tick() {
				return // shutdown triggered; goroutine exits
			}
		}
	}
}

// tick performs one evaluation cycle. Returns true if shutdown was triggered.
// Separated from run so tests can drive the decision logic directly.
//
// TOCTOU note: the three observations (clients via SessionCount under mutex,
// inflight atomic, idle time) are NOT a single atomic snapshot. A concurrent
// call is still safe because: (a) the streamable session is registered before
// any tool call is dispatched, so SessionCount()>0 whenever a call could be
// inflight; (b) shutdown is graceful (drain window), not abrupt, so a call
// that starts between checks has time to complete.
func (m *idleExitMonitor) tick() bool {
	// Refresh last-activity from the router's atomic timestamp (guard c/d).
	// Any Call/CallDirect recorded after our previous check resets the idle
	// countdown, debouncing reconnect flaps.
	// L-1: lastCallNano is stamped at call START (not completion), so after a
	// very long call the effective remaining idle window is shorter — intentional,
	// avoids a defer write.
	routerLast := m.router.LastCallTime()
	if !routerLast.IsZero() {
		m.noteActivity(routerLast)
	}

	inflight := m.router.InflightCalls()
	clients := int64(m.cfg.counter.ClientCount())
	idle := m.idleSince()

	if !shouldExit(clients, inflight, idle, m.cfg.window) {
		m.cfg.logger.Debug("idle exit monitor: not idle",
			"clients", clients,
			"inflight", inflight,
			"idle_seconds", int(idle.Seconds()),
			"window_seconds", int(m.cfg.window.Seconds()))
		return false
	}

	m.cfg.logger.Info("idle exit monitor: idle window elapsed; triggering graceful shutdown",
		"idle_seconds", int(idle.Seconds()),
		"window_seconds", int(m.cfg.window.Seconds()))
	if m.cfg.shutdown != nil {
		m.cfg.shutdown()
	}
	return true
}

// idleExitField groups the idle-exit monitor state embedded on Gateway.
// The sync.Mutex protects monitor and started.
type idleExitField struct {
	sync.Mutex
	monitor *idleExitMonitor
	started bool
}
