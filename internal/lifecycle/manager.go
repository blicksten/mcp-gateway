// Package lifecycle manages backend MCP server processes and connections.
package lifecycle

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mcp-gateway/internal/logbuf"
	"mcp-gateway/internal/models"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/thejerf/suture/v4"
)

// entry holds runtime state for one backend server.
type entry struct {
	models.ServerEntry

	session   *mcp.ClientSession
	client    *mcp.Client
	transport mcp.Transport
	cmd       *exec.Cmd // non-nil for stdio backends
	starting  bool       // guards against concurrent Start calls
	logs      *logbuf.Ring // captured stderr output
}

// Manager owns all backend server entries and their MCP client sessions.
// All mutations go through Manager methods; health monitor and proxy
// read state via exported accessors.
type Manager struct {
	mu       sync.RWMutex
	entries  map[string]*entry
	impl     *mcp.Implementation
	logger   *slog.Logger
	job       jobHandle   // Windows Job Object; no-op zero value on other platforms
	jobValid  bool        // true if newJobObject succeeded
	jobClose  sync.Once   // guards closeJobObject against double-close
	jobClosed atomic.Bool // set after closeJobObject; guards assignProcess race

	// FM 1 (spike 2026-05-11): subprocess registry tracks PIDs of stdio
	// backends so a future gateway can reap them if this gateway crashes
	// before its Job Object can fire KILL_ON_JOB_CLOSE. Nil when registry
	// open failed at NewManager time — degrades to "no FM 1 reaping",
	// never blocks normal operation.
	registry *Registry

	// supervisorTree is the suture supervisor tree wired in P1.5 step 2.
	// Nil when the tree has not been set up (legacy path used by tests that
	// construct a Manager directly and never call SetupSupervisor).
	// When non-nil, ServeBackgroundSupervisor drives backend restarts and
	// StartAll becomes a no-op so the two paths do not fight each other.
	supervisorTree *suture.Supervisor

	// supervisorTokens tracks the suture ServiceToken for every backend
	// currently registered with supervisorTree. Populated by
	// SetupSupervisor (startup-time backends) and AddBackendToSupervisor
	// (runtime add via PATCH/POST /api/v1/servers). Consumed by
	// RemoveBackendFromSupervisor on delete. Nil before SetupSupervisor.
	//
	// Task C (P1.3 2026-05-22) closes the runtime-add gap documented in
	// REVIEW-stabilization §"Assessment of known limitations": backends
	// added via PATCH after startup must join the supervisor tree so they
	// get the same crash-restart policy as startup-time backends.
	supervisorTokens map[string]suture.ServiceToken

	// toolsChangedCb fires after a backend's tool list has been refreshed
	// in response to a notifications/tools/list_changed from that backend.
	// Wired by main.go to gw.RebuildTools(). Nil-safe: handleToolsChanged guards.
	// F1 fix: closes the "gateway silently drops backend tool changes" gap
	// documented in docs/spikes/2026-05-21-shim-architecture-draft.md §11.
	toolsChangedCb func(name string)

	// testStopHook is non-nil only in tests. When set, Stop calls this
	// function instead of performing the real stop sequence. Allows tests
	// to inject a failing Stop to verify RemoveResult.Orphan semantics.
	// Set only before the manager begins servicing concurrent requests; no
	// concurrent writes in production (F-05 — write-once-before-traffic
	// invariant; no atomic.Pointer needed).
	testStopHook  func(name string) error
	testRemoveHook func(name string) error // non-nil only in tests
}

// NewManager creates a lifecycle manager from the given config.
// The version parameter is injected via ldflags at build time ("dev" for local builds).
func NewManager(cfg *models.Config, version string, logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	job, err := newJobObject()
	jobValid := err == nil
	if err != nil {
		logger.Warn("failed to create job object (child process cleanup disabled)", "error", err)
	}
	// FM 1 (spike 2026-05-11): scan registry dir for orphans from a
	// previously-crashed gateway and reap them, THEN open our own
	// registry. Order matters — opening first would race a parallel
	// startup of two gateways (both seeing each other's fresh files as
	// "live owner" and skipping legitimate reap). Reuses pidfile's
	// XDG/TempDir resolution pattern so registry lives next to the
	// pidfile on every platform.
	registryDir := DefaultRegistryDir()
	if reaped := ScanAndReap(registryDir, os.Getpid(), logger); reaped > 0 {
		logger.Info("subprocess registry: reaped orphans from previous gateway crash",
			"count", reaped, "dir", registryDir)
	}
	registry, regErr := OpenRegistry(registryDir, os.Getpid())
	if regErr != nil {
		logger.Warn("subprocess registry: open failed (FM 1 reaping disabled)",
			"dir", registryDir, "error", regErr)
		// registry stays nil — every Add/Remove call site nil-checks.
	}

	m := &Manager{
		entries:  make(map[string]*entry),
		impl:     &mcp.Implementation{Name: "mcp-gateway", Version: version},
		logger:   logger,
		job:      job,
		jobValid: jobValid,
		registry: registry,
	}
	for name, sc := range cfg.Servers {
		m.entries[name] = &entry{
			ServerEntry: models.ServerEntry{
				Name:   name,
				Config: *sc,
				Status: models.StatusStopped,
			},
			logs: logbuf.New(logbuf.DefaultCapacity),
		}
	}
	return m
}

// LogBuffer returns the log ring buffer for a server.
func (m *Manager) LogBuffer(name string) (*logbuf.Ring, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.entries[name]
	if !ok {
		return nil, false
	}
	return e.logs, true
}

// Entries returns a snapshot of all server entries.
func (m *Manager) Entries() []models.ServerEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]models.ServerEntry, 0, len(m.entries))
	for _, e := range m.entries {
		result = append(result, e.ServerEntry)
	}
	return result
}

// Entry returns a snapshot of a single server entry.
func (m *Manager) Entry(name string) (models.ServerEntry, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.entries[name]
	if !ok {
		return models.ServerEntry{}, false
	}
	return e.ServerEntry, true
}

// Session returns the active MCP client session for a backend.
func (m *Manager) Session(name string) (*mcp.ClientSession, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.entries[name]
	if !ok || e.session == nil {
		return nil, false
	}
	return e.session, true
}

// SetToolsChangedCallback installs a callback fired after a backend
// reports notifications/tools/list_changed and Manager has refreshed
// its cached entry.Tools. Call this BEFORE any backend starts (i.e.
// before SetupSupervisor + ServeBackgroundSupervisor) so the handler
// is installed for every backend connection.
func (m *Manager) SetToolsChangedCallback(cb func(name string)) {
	m.toolsChangedCb = cb
}

// Start connects to a backend server. It must not be called while
// holding the manager lock (callers must release the lock first).
func (m *Manager) Start(ctx context.Context, name string) error {
	m.mu.Lock()
	e, ok := m.entries[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("server %q not found", name)
	}
	if e.Config.Disabled {
		e.Status = models.StatusDisabled
		m.mu.Unlock()
		return nil
	}
	if e.starting {
		m.mu.Unlock()
		return fmt.Errorf("server %q start already in progress", name)
	}
	e.starting = true
	e.Status = models.StatusStarting
	cfg := e.Config // copy for use outside lock
	m.mu.Unlock()

	// TCP reachability pre-check for HTTP/SSE backends. Avoids a 42-second
	// Windows connectex timeout when the backend host is unreachable.
	//
	// pdap-docs unreachable feature: classify the failure mode. Transport-
	// layer failures (DNS, conn-refused, host-unreach, dial-timeout —
	// typical of VPN-off / network-partition) route to StatusUnreachable
	// so the operator UI shows a stable yellow warning instead of red
	// error + the health monitor switches to slow-poll recovery.
	// Protocol-layer failures (TLS handshake, HTTP 4xx/5xx) keep
	// StatusError (with aggressive restart) because those are usually
	// actual backend bugs that benefit from quick retries.
	// See docs/PLAN-unreachable-handling.md.
	if cfg.URL != "" {
		if err := checkTCPReachable(ctx, cfg.URL, 3*time.Second); err != nil {
			m.mu.Lock()
			if e2, ok := m.entries[name]; ok {
				e2.starting = false
				if IsTransportUnreachable(err) {
					e2.Status = models.StatusUnreachable
				} else {
					e2.Status = models.StatusError
				}
				e2.LastError = err.Error()
			}
			m.mu.Unlock()
			return fmt.Errorf("start %q: %w", name, err)
		}
	}

	session, client, transport, cmd, err := m.connectSafe(ctx, name, &cfg)

	m.mu.Lock()
	e, ok = m.entries[name]
	if !ok {
		m.mu.Unlock()
		if session != nil {
			_ = session.Close()
		}
		// PAL HIGH fix: use group-aware termination so grandchildren
		// of this MCP server are reaped on the "removed during start"
		// error path, matching Stop()'s guarantee.
		if cmd != nil && cmd.Process != nil {
			_ = terminateProcessGroup(cmd.Process)
			_ = killProcessGroup(cmd.Process)
			_ = cmd.Wait()
		}
		return fmt.Errorf("server %q removed during start", name)
	}
	e.starting = false
	if err != nil {
		e.Status = models.StatusError
		e.LastError = err.Error()
		m.mu.Unlock()
		return fmt.Errorf("start %q: %w", name, err)
	}

	e.session = session
	e.client = client
	e.transport = transport
	e.cmd = cmd
	e.Status = models.StatusRunning
	e.LastError = ""
	e.StartedAt = time.Now()
	if cmd != nil && cmd.Process != nil {
		e.PID = cmd.Process.Pid
	}
	pid := e.PID // capture before unlock for safe logging
	m.mu.Unlock()

	// Fetch tools outside the lock.
	tools, err := m.fetchTools(ctx, name, session)
	if err != nil {
		m.logger.Warn("failed to fetch tools", "server", name, "error", err)
	}
	if tools != nil {
		m.mu.Lock()
		if e2, ok := m.entries[name]; ok {
			e2.Tools = tools
		}
		m.mu.Unlock()
	}

	m.logger.Info("server started", "server", name, "transport", cfg.TransportType(), "pid", pid)
	return nil
}

// connectSafe wraps connect with panic recovery (CR-1 fix).
func (m *Manager) connectSafe(ctx context.Context, name string, cfg *models.ServerConfig) (
	session *mcp.ClientSession, client *mcp.Client, transport mcp.Transport, cmd *exec.Cmd, err error,
) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic during connect %q: %v", name, r)
			m.logger.Error("connect panic recovered", "server", name, "panic", r)
		}
	}()
	return m.connect(ctx, name, cfg)
}

// connect creates an MCP client and connects to the backend.
// T1.5.2: KeepAlive is injected into ClientOptions so that a missed ping
// causes the SDK to call session.Close(), which unblocks session.Wait() in
// BackendSupervisor.Serve() and triggers a suture restart.
func (m *Manager) connect(ctx context.Context, name string, cfg *models.ServerConfig) (
	*mcp.ClientSession, *mcp.Client, mcp.Transport, *exec.Cmd, error,
) {
	client := mcp.NewClient(&mcp.Implementation{
		Name:    fmt.Sprintf("mcp-gateway/%s", name),
		Version: m.impl.Version,
	}, &mcp.ClientOptions{
		KeepAlive: BackendKeepaliveInterval,
		ToolListChangedHandler: func(handlerCtx context.Context, _ *mcp.ToolListChangedRequest) {
			// F1: backend signalled its tool list changed. Re-fetch + update
			// cached entry.Tools, then notify the registered callback so
			// Gateway can RebuildTools() and propagate to clients.
			//
			// F1-R1 (Sonnet cross-tier review 2026-05-21): launch in goroutine
			// because go-sdk dispatches notification handlers SEQUENTIALLY per
			// client. A blocking handler would hold up keepalive pings + later
			// notifications for up to 30s; off-loading lets the SDK loop continue.
			go m.handleToolsChanged(handlerCtx, name)
		},
	})

	switch cfg.TransportType() {
	case "stdio":
		return m.connectStdio(ctx, name, client, cfg)
	case "http":
		return m.connectHTTP(ctx, name, client, cfg)
	case "sse":
		return m.connectSSE(ctx, name, client, cfg)
	default:
		return nil, nil, nil, nil, fmt.Errorf("no MCP transport for server config (rest-only?)")
	}
}

func (m *Manager) connectStdio(ctx context.Context, name string, client *mcp.Client, cfg *models.ServerConfig) (
	*mcp.ClientSession, *mcp.Client, mcp.Transport, *exec.Cmd, error,
) {
	cmd := exec.Command(cfg.Command, cfg.Args...)
	configureSysProcAttr(cmd) // Windows: CREATE_NEW_PROCESS_GROUP; no-op elsewhere
	if cfg.Cwd != "" {
		cmd.Dir = cfg.Cwd
	}
	if len(cfg.Env) > 0 {
		cmd.Env = append(os.Environ(), cfg.Env...)
	}

	// Capture stderr for log streaming.
	// Use an os.Pipe so CommandTransport can still manage stdin/stdout.
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("stderr pipe: %w", err)
	}
	cmd.Stderr = stderrW

	// Start scanning stderr in the background.
	m.mu.RLock()
	e := m.entries[name]
	ring := e.logs
	m.mu.RUnlock()
	go func() {
		scanStderr(ring, stderrR, name, m.logger)
		stderrR.Close()
	}()

	transport := &mcp.CommandTransport{Command: cmd}
	session, err := client.Connect(ctx, transport, nil)
	// Close the write end so the scanner gets EOF when the child exits.
	stderrW.Close()
	if err != nil {
		// CR-2 fix: do NOT close stderrR here — the background goroutine owns it.
		// It will get EOF from stderrW.Close() above and close stderrR itself.
		// PAL HIGH fix: group-aware termination on the connect-failure
		// path so any grandchildren the MCP server spawned before the
		// handshake failed are also cleaned up.
		if cmd.Process != nil {
			_ = terminateProcessGroup(cmd.Process)
			_ = killProcessGroup(cmd.Process)
			_ = cmd.Wait()
		}
		return nil, nil, nil, nil, fmt.Errorf("stdio connect: %w", err)
	}

	// Assign child process to Job Object for cleanup on daemon exit.
	// TOCTOU: small race between CommandTransport.Start() and this call;
	// accepted limitation — see ADR in docs/PLAN-phase5.md.
	// jobClosed guard prevents calling assignProcess after StopAll has closed the handle.
	if cmd.Process != nil && m.jobValid && !m.jobClosed.Load() {
		if err := assignProcess(m.job, uint32(cmd.Process.Pid)); err != nil {
			m.logger.Warn("failed to assign process to job object", "server", name, "pid", cmd.Process.Pid, "error", err)
		}
	}

	// FM 1 (spike 2026-05-11): record the spawned PID in our subprocess
	// registry so a future gateway can reap it if we crash. Job Object
	// + KILL_ON_JOB_CLOSE handles graceful exit; the registry is the
	// belt-and-suspenders crash-recovery path.
	if m.registry != nil && cmd.Process != nil {
		cmdLine := cfg.Command + " " + strings.Join(cfg.Args, " ")
		if err := m.registry.Add(name, cmd.Process.Pid, cmdLine); err != nil {
			m.logger.Warn("subprocess registry: Add failed (orphan reaping degraded)",
				"server", name, "pid", cmd.Process.Pid, "error", err)
		}
	}

	return session, client, transport, cmd, nil
}

// scanStderr reads lines from a child process stderr and writes them to the ring buffer.
func scanStderr(ring *logbuf.Ring, r io.Reader, name string, logger *slog.Logger) {
	scanner := bufio.NewScanner(r)
	// Raise stderr scanner line cap from default 64KB to 1MB. Producer-side
	// twin of the SSE client-side cap in ctlclient.streamLogsOnce; the
	// end-to-end cap is the minimum of the two scanner limits, so both
	// must agree. Child processes emitting long stack traces or JSON traces
	// above 64KB were being truncated before reaching the ring buffer.
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		ring.Write(scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		logger.Debug("stderr scanner finished", "server", name, "error", err)
	}
}

func (m *Manager) connectHTTP(ctx context.Context, name string, client *mcp.Client, cfg *models.ServerConfig) (
	*mcp.ClientSession, *mcp.Client, mcp.Transport, *exec.Cmd, error,
) {
	m.writeLog(name, "connecting to HTTP endpoint: "+cfg.URL)
	transport := &mcp.StreamableClientTransport{
		Endpoint:   cfg.URL,
		HTTPClient: httpClientWithHeaders(cfg.Headers),
	}
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		m.writeLog(name, "HTTP connect failed: "+err.Error())
		return nil, nil, nil, nil, fmt.Errorf("http connect: %w", err)
	}
	m.writeLog(name, "HTTP connection established")
	return session, client, transport, nil, nil
}

func (m *Manager) connectSSE(ctx context.Context, name string, client *mcp.Client, cfg *models.ServerConfig) (
	*mcp.ClientSession, *mcp.Client, mcp.Transport, *exec.Cmd, error,
) {
	m.writeLog(name, "connecting to SSE endpoint: "+cfg.URL)
	transport := &mcp.SSEClientTransport{
		Endpoint:   cfg.URL,
		HTTPClient: httpClientWithHeaders(cfg.Headers),
	}
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		m.writeLog(name, "SSE connect failed: "+err.Error())
		return nil, nil, nil, nil, fmt.Errorf("sse connect: %w", err)
	}
	m.writeLog(name, "SSE connection established")
	return session, client, transport, nil, nil
}

// writeLog writes a synthetic log entry to a server's ring buffer.
// Used for HTTP/SSE backends that have no stderr to capture.
func (m *Manager) writeLog(name, msg string) {
	m.mu.RLock()
	e, ok := m.entries[name]
	m.mu.RUnlock()
	if ok && e.logs != nil {
		e.logs.Write("[gateway] " + msg)
	}
}

// fetchTools retrieves the tool list from a connected session.
func (m *Manager) fetchTools(ctx context.Context, name string, session *mcp.ClientSession) ([]models.ToolInfo, error) {
	result, err := session.ListTools(ctx, nil)
	if err != nil {
		return nil, err
	}
	tools := make([]models.ToolInfo, 0, len(result.Tools))
	for _, t := range result.Tools {
		// CR-10 fix: skip tools whose names contain the namespace separator.
		if strings.Contains(t.Name, models.ToolNameSeparator) {
			m.logger.Warn("skipping tool with reserved separator in name",
				"server", name, "tool", t.Name, "separator", models.ToolNameSeparator)
			continue
		}
		tools = append(tools, models.ToolInfo{
			Name:        t.Name,
			Description: t.Description,
			Server:      name,
			InputSchema: t.InputSchema,
		})
	}
	return tools, nil
}

// handleToolsChanged fires from the ToolListChangedHandler registered in connect().
// Looks up the live session, calls fetchTools, updates cached entry.Tools,
// and invokes toolsChangedCb (if set). Logs warnings on failure but
// never panics or blocks indefinitely.
//
// F1-R2 (E.2 empirical 2026-05-21): the ctx parameter is INTENTIONALLY ignored.
// F1-R1 wrapped the call in `go m.handleToolsChanged(handlerCtx, name)` to
// avoid blocking the SDK's sequential notification dispatch. But the go-sdk
// cancels the handler-scoped ctx when the handler closure returns — since
// our goroutine outlives the handler, deriving fetchCtx from that ctx causes
// session.ListTools to fail with "context canceled" milliseconds after the
// notification arrives. Empirical evidence: mock-expB sent tools/list_changed
// at t=15s; gateway immediately emitted notifications/cancelled (the SDK
// cancelling our in-flight fetch) and never followed up with tools/list.
// Fix: use context.Background() bounded by a 30s timeout. Daemon graceful
// stop is observed via Manager.Stop being called and clearing e.session
// (guarded below).
func (m *Manager) handleToolsChanged(_ context.Context, name string) {
	session, ok := m.Session(name)
	if !ok || session == nil {
		m.logger.Debug("F1: tools_changed handler fired but session gone", "server", name)
		return
	}
	// Bound the re-fetch so a slow backend cannot wedge the goroutine.
	// Use Background, not the SDK handler ctx — see godoc above.
	fetchCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tools, err := m.fetchTools(fetchCtx, name, session)
	if err != nil {
		m.logger.Warn("F1: tools_changed re-fetch failed", "server", name, "error", err)
		return
	}
	m.mu.Lock()
	if e, ok := m.entries[name]; ok && e.session == session {
		// Guard: only update if the entry still holds the same session we
		// fetched from. If Stop() ran concurrently it cleared e.session, so
		// the pointer mismatch prevents us from overwriting the nil that Stop
		// deliberately set (Tools = nil on stop is a public invariant relied
		// on by TestStop and callers that check entry state post-stop).
		e.Tools = tools
	}
	m.mu.Unlock()
	m.logger.Info("F1: tools refreshed via list_changed notification", "server", name, "count", len(tools))
	// F1 read-once for race-safety: capture pointer before invoking so a
	// concurrent SetToolsChangedCallback cannot observe a torn pointer.
	if cb := m.toolsChangedCb; cb != nil {
		cb(name)
	}
}

// Stop disconnects from a backend server and kills its process if stdio.
func (m *Manager) Stop(ctx context.Context, name string) error {
	// testStopHook is set only in tests to simulate Stop failures without
	// spinning up real processes.
	if m.testStopHook != nil {
		return m.testStopHook(name)
	}

	m.mu.Lock()
	e, ok := m.entries[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("server %q not found", name)
	}
	session := e.session
	cmd := e.cmd
	e.session = nil
	e.client = nil
	e.transport = nil
	e.cmd = nil
	// HIGH-2 (P1.5 step 2) — preserve StatusRestarting when called from
	// Restart. The supervisor's status-gated bottom branch reads BackendStatus
	// after session.Wait() unblocks; if Stop unconditionally overwrote
	// Restarting with Stopped here, the supervisor would observe Stopped and
	// return suture.ErrDoNotRestart on every managed restart, silently
	// removing the backend from the suture tree. Tested by:
	// TestBackendSupervisor_RestartWithRealSession_NoErrDoNotRestart.
	if e.Status != models.StatusRestarting {
		e.Status = models.StatusStopped
	}
	e.PID = 0
	e.Tools = nil
	m.mu.Unlock()

	if session != nil {
		if cmd == nil {
			// Non-stdio (HTTP/SSE) — write synthetic log before closing session.
			m.writeLog(name, "session closing")
		}
		// PAL HIGH fix: a hung or misbehaving MCP server's session.Close
		// used to block indefinitely, preventing the subsequent kill
		// path from running. Cap the close at 2s so the kill path
		// always gets a chance to run.
		closeDone := make(chan struct{})
		go func() {
			_ = session.Close()
			close(closeDone)
		}()
		select {
		case <-closeDone:
		case <-time.After(2 * time.Second):
			m.logger.Warn("session close timed out; proceeding to process termination", "server", name)
		}
	}

	// For stdio backends, ensure the child process (and, on POSIX, its
	// process group) is dead. T13A.2/F-5: use process-group signalling
	// on POSIX so any grandchildren of the MCP server also get reaped.
	if cmd != nil && cmd.Process != nil {
		done := make(chan struct{})
		go func() {
			_ = cmd.Wait()
			close(done)
		}()
		// Graceful SIGTERM to the process group first.
		_ = terminateProcessGroup(cmd.Process)
		select {
		case <-done:
			// Process exited cleanly.
		case <-time.After(5 * time.Second):
			_ = killProcessGroup(cmd.Process)
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				m.logger.Error("process did not exit after kill", "server", name)
			}
		}
		// FM 1 (spike 2026-05-11): drop the entry from the registry now
		// that the child is gone. The registry file lists only LIVE
		// subprocesses, so a crashed gateway leaves no false orphans
		// for the next gateway to chase.
		if m.registry != nil {
			if err := m.registry.Remove(cmd.Process.Pid); err != nil {
				m.logger.Warn("subprocess registry: Remove failed",
					"server", name, "pid", cmd.Process.Pid, "error", err)
			}
		}
	}

	m.logger.Info("server stopped", "server", name)
	return nil
}

// Restart stops then starts a server. Increments restart count.
//
// HIGH-2 (P1.5 step 2): StatusRestarting is set BEFORE Stop() is called so
// that BackendSupervisor.Serve() — which reads the status when session.Wait()
// returns inside Stop() — sees StatusRestarting rather than StatusStopped.
// Without this ordering the supervisor would call ErrDoNotRestart on a
// legitimate restart, silencing the suture tree for that backend permanently.
func (m *Manager) Restart(ctx context.Context, name string) error {
	// HIGH-2: mark restarting BEFORE Stop so the supervisor's status-gate
	// sees Restarting (not Stopped) when session.Wait() unblocks.
	m.mu.Lock()
	e, ok := m.entries[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("server %q not found", name)
	}
	e.RestartCount++
	e.Status = models.StatusRestarting
	m.mu.Unlock()

	if err := m.Stop(ctx, name); err != nil {
		m.logger.Warn("stop during restart failed", "server", name, "error", err)
	}

	return m.Start(ctx, name)
}

// SetupSupervisor builds the suture supervisor tree from the current entry
// map and stores it on the Manager. Call ServeBackgroundSupervisor(ctx) to
// start it. SetupSupervisor must be called before any concurrent traffic;
// it is not goroutine-safe with respect to concurrent entry mutation.
//
// Manager satisfies both BackendManager and StatusChecker, so it passes
// itself for both arguments of NewBackendSupervisorTree.
//
// Task C (P1.3 2026-05-22): SetupSupervisor now records every startup-time
// backend's ServiceToken in m.supervisorTokens so runtime removals
// (RemoveBackendFromSupervisor) can target the right token even for
// backends that predate the runtime-add path.
//
// Lock is m.mu.Lock (not RLock as before Task C) because SetupSupervisor
// now atomically writes BOTH supervisorTree and supervisorTokens — readers
// of either field must observe a consistent paired write.
func (m *Manager) SetupSupervisor(logger *slog.Logger) {
	m.mu.Lock()
	names := make([]string, 0, len(m.entries))
	for name := range m.entries {
		names = append(names, name)
	}
	tree, tokens := newBackendSupervisorTreeWithTokens(m, m, names, logger)
	m.supervisorTree = tree
	m.supervisorTokens = tokens
	m.mu.Unlock()
}

// AddBackendToSupervisor wraps the named backend in a new child supervisor
// and adds it to the running supervisor tree. The backend gets the same
// FailureThreshold/FailureBackoff/FailureDecay policy as startup-time
// backends, so a runtime-added crash is auto-restarted just like any other.
//
// No-op when:
//   - the supervisor tree was never set up (Manager constructed without
//     SetupSupervisor — happens in tests and in legacy callers); or
//   - a token is already recorded for this name (idempotent — repeated
//     PATCH that adds the same backend does not produce duplicate
//     supervisor children).
//
// The backend MUST already be registered in m.entries via AddServer
// before this is called; the supervisor's first Serve() will call
// Manager.Start which looks the entry up by name. Callers in
// internal/api/server.go satisfy this ordering.
//
// Task C (P1.3 2026-05-22). Closes the runtime-add supervisor gap.
func (m *Manager) AddBackendToSupervisor(name string, logger *slog.Logger) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.supervisorTree == nil {
		return
	}
	// Sonnet HIGH (fix-in-cycle 2026-05-22): initialise supervisorTokens
	// BEFORE the map read. Reading a nil map is safe in Go (returns zero
	// value) but the post-read nil-init is unreachable in production
	// (SetupSupervisor always allocates the map). Moving init first keeps
	// the documented invariant honest if any future code path constructs
	// the tree without going through SetupSupervisor.
	if m.supervisorTokens == nil {
		m.supervisorTokens = make(map[string]suture.ServiceToken)
	}
	if _, exists := m.supervisorTokens[name]; exists {
		return
	}
	svc := NewBackendSupervisor(name, m, m, logger)
	childSpec := DefaultSupervisorSpec(name, logger)
	child := suture.New("backends/"+name, childSpec)
	child.Add(svc)
	m.supervisorTokens[name] = m.supervisorTree.Add(child)
}

// RemoveBackendFromSupervisor stops the named backend's child supervisor
// and removes it from the tree. timeout caps the wait for in-flight
// service termination (suture's RemoveAndWait returns ErrTimeout when
// the service does not stop within the deadline).
//
// No-op (returns nil) when the supervisor tree is not set up OR no
// token is recorded for the name. Idempotent on repeated removal.
//
// Suture's RemoveAndWait fires context cancellation to the child's Serve;
// BackendSupervisor.Serve handles ctx.Done by calling Manager.Stop on the
// backend. So removing from the supervisor tree DOES stop the backend.
// Callers that also call Manager.RemoveServer after this will see Stop
// as a no-op (Status already Stopped).
//
// Task C (P1.3 2026-05-22). Closes the runtime-remove supervisor leak.
func (m *Manager) RemoveBackendFromSupervisor(name string, timeout time.Duration) error {
	m.mu.Lock()
	token, ok := m.supervisorTokens[name]
	tree := m.supervisorTree
	if ok {
		delete(m.supervisorTokens, name)
	}
	m.mu.Unlock()
	if !ok || tree == nil {
		return nil
	}
	return tree.RemoveAndWait(token, timeout)
}

// ServeBackgroundSupervisor starts the supervisor tree in the background and
// returns a stop function that, when called, stops the tree and waits for all
// child services to terminate. The caller must invoke the stop function before
// StopAll so suture does not fight concurrent Stop calls.
//
// Panics if SetupSupervisor was not called first.
func (m *Manager) ServeBackgroundSupervisor(ctx context.Context) func() {
	if m.supervisorTree == nil {
		panic("lifecycle.Manager.ServeBackgroundSupervisor: SetupSupervisor was not called")
	}
	treeCtx, treeCancel := context.WithCancel(ctx)
	done := m.supervisorTree.ServeBackground(treeCtx)
	return func() {
		treeCancel()
		<-done
	}
}

// SupervisorActive returns true when the suture supervisor tree has been
// wired (SetupSupervisor was called and supervisorTree is non-nil). The
// Health Monitor uses this to defer restart logic to the supervisor — when
// the supervisor is active, it owns backend restart policy and the Monitor
// becomes purely observational.
//
// F2 fix (post-P1.5 step 2): closes the dual-restart race where both
// Monitor.attemptRestart and suture's Serve loop tried to call Manager.Start
// after a crash, producing transient "start already in progress" errors.
func (m *Manager) SupervisorActive() bool {
	return m.supervisorTree != nil
}

// StartAll starts all non-disabled servers concurrently.
//
// When the supervisor tree has been set up via SetupSupervisor, this method
// is a no-op: the supervisor drives backend starts through BackendSupervisor.Serve.
// Tests that bypass the supervisor continue to use StartAll directly.
func (m *Manager) StartAll(ctx context.Context) error {
	if m.supervisorTree != nil {
		// Supervisor tree is active — it drives all backend starts.
		// Returning nil here is intentional; main.go calls
		// ServeBackgroundSupervisor before the errgroup, so backends are
		// already starting by the time this call would execute.
		return nil
	}

	m.mu.RLock()
	names := make([]string, 0, len(m.entries))
	for name, e := range m.entries {
		if !e.Config.Disabled {
			names = append(names, name)
		}
	}
	m.mu.RUnlock()

	var wg sync.WaitGroup
	errCh := make(chan error, len(names))
	for _, name := range names {
		wg.Add(1)
		go func(n string) {
			defer wg.Done()
			if err := m.Start(ctx, n); err != nil {
				errCh <- err
			}
		}(name)
	}
	wg.Wait()
	close(errCh)

	var errs []error
	for err := range errCh {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// StopAll stops all running servers concurrently (CR-13/AR-7 fix).
//
// HIGH-2 NEW-01 (P1.5 step 2): when the supervisor tree is active, callers
// MUST invoke the stop function returned by ServeBackgroundSupervisor BEFORE
// StopAll so suture-driven restarts do not race with these Stop calls. The
// production path in cmd/mcp-gateway/main.go does this explicitly. Stop's
// conditional-write (manager.go ~line 480) preserves StatusRestarting when a
// Restart is mid-flight, which is required for the supervisor's bottom-branch
// gating but means StopAll alone cannot interrupt an in-flight Restart —
// always cancel the supervisor and the errgroup (Monitor) first.
func (m *Manager) StopAll(ctx context.Context) {
	m.mu.RLock()
	names := make([]string, 0, len(m.entries))
	for name := range m.entries {
		names = append(names, name)
	}
	m.mu.RUnlock()

	var wg sync.WaitGroup
	for _, name := range names {
		wg.Add(1)
		go func(n string) {
			defer wg.Done()
			_ = m.Stop(ctx, n)
		}(name)
	}
	wg.Wait()

	// Close the Job Object handle. On Windows this triggers
	// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE for any remaining processes.
	// sync.Once guards against double-close if StopAll is called concurrently.
	if m.jobValid {
		m.jobClose.Do(func() {
			m.jobClosed.Store(true)
			if err := closeJobObject(m.job); err != nil {
				m.logger.Warn("failed to close job object", "error", err)
			}
		})
	}

	// FM 1 (spike 2026-05-11): delete our registry file on graceful
	// shutdown so the NEXT gateway startup scan sees no orphan to reap.
	// Crash paths skip this entirely — the file lingers and the next
	// gateway's ScanAndReap handles it.
	if m.registry != nil {
		if err := m.registry.Close(); err != nil {
			m.logger.Warn("subprocess registry: Close failed", "error", err)
		}
	}
}

// AddServer adds a new server entry. Does not start it.
func (m *Manager) AddServer(name string, cfg *models.ServerConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.entries[name]; exists {
		return fmt.Errorf("server %q already exists", name)
	}
	if len(m.entries) >= models.MaxServers {
		return fmt.Errorf("cannot add server: maximum of %d servers reached", models.MaxServers)
	}
	m.entries[name] = &entry{
		ServerEntry: models.ServerEntry{
			Name:   name,
			Config: *cfg,
			Status: models.StatusStopped,
		},
		logs: logbuf.New(logbuf.DefaultCapacity),
	}
	return nil
}

// SetTestStopHook installs a hook that replaces the real Stop sequence in tests.
// Must be called before the manager begins servicing concurrent requests.
// The hook is only consulted when non-nil; production code never sets it.
func (m *Manager) SetTestStopHook(hook func(name string) error) {
	m.testStopHook = hook
}

// SetTestRemoveHook installs a hook that makes RemoveServer return an error
// without deleting the entry — used by rename tests to verify rollback behavior
// when removal of the old entry fails. Must be called before concurrent traffic.
func (m *Manager) SetTestRemoveHook(hook func(name string) error) {
	m.testRemoveHook = hook
}

// RemoveResult carries the outcome of a RemoveServer call.
// Orphan is true when Stop returned a non-nil error — the OS process may still
// be running. The entry is still deleted from the manager regardless.
type RemoveResult struct {
	Orphan  bool  // true if Stop returned a non-nil error (process may still be running)
	StopErr error // the Stop error (nil = clean stop)
}

// RemoveServer stops and removes a server. Must be called without the lock held.
// Returns (RemoveResult, error): the error is non-nil only when the server is
// not found. A Stop failure is surfaced via RemoveResult.Orphan / RemoveResult.StopErr
// rather than as the primary error, because the entry is deleted regardless.
func (m *Manager) RemoveServer(ctx context.Context, name string) (RemoveResult, error) {
	m.mu.RLock()
	_, exists := m.entries[name]
	m.mu.RUnlock()
	if !exists {
		return RemoveResult{}, fmt.Errorf("server %q not found", name)
	}

	// testRemoveHook is set only in tests that need RemoveServer to fail without
	// deleting the entry (e.g. rename rollback tests).
	if m.testRemoveHook != nil {
		if err := m.testRemoveHook(name); err != nil {
			return RemoveResult{}, err
		}
	}

	// Stop outside the lock — surface the error rather than swallowing it (R-28).
	stopErr := m.Stop(ctx, name)

	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, name)
	return RemoveResult{Orphan: stopErr != nil, StopErr: stopErr}, nil
}

// Reconcile applies a new config: stops removed servers, starts added ones,
// restarts changed ones. Lock is acquired only for map mutation.
func (m *Manager) Reconcile(ctx context.Context, newCfg *models.Config) error {
	m.mu.RLock()
	oldNames := make(map[string]models.ServerConfig, len(m.entries))
	for name, e := range m.entries {
		oldNames[name] = e.Config
	}
	m.mu.RUnlock()

	// Determine changes.
	var toRemove, toAdd, toRestart []string
	for name := range oldNames {
		if _, exists := newCfg.Servers[name]; !exists {
			toRemove = append(toRemove, name)
		}
	}
	for name, newSC := range newCfg.Servers {
		oldSC, exists := oldNames[name]
		if !exists {
			toAdd = append(toAdd, name)
		} else if configChanged(&oldSC, newSC) {
			toRestart = append(toRestart, name)
		}
	}

	// Apply changes: remove, update+restart, add.
	for _, name := range toRemove {
		result, err := m.RemoveServer(ctx, name)
		if err != nil {
			m.logger.Warn("reconcile: remove failed", "server", name, "error", err)
		} else if result.Orphan {
			m.logger.Warn("reconcile: stop error during remove (process may be orphaned)",
				"server", name, "stop_error", result.StopErr)
		}
	}
	for _, name := range toRestart {
		_ = m.Stop(ctx, name)
		m.mu.Lock()
		if e, ok := m.entries[name]; ok {
			e.Config = *newCfg.Servers[name]
			e.starting = false // CR-9 fix: clear starting flag before re-start
		}
		m.mu.Unlock()
		if err := m.Start(ctx, name); err != nil {
			m.logger.Warn("reconcile: restart failed", "server", name, "error", err)
		}
	}
	for _, name := range toAdd {
		if err := m.AddServer(name, newCfg.Servers[name]); err != nil {
			m.logger.Warn("reconcile: add failed", "server", name, "error", err)
		}
		if !newCfg.Servers[name].Disabled {
			if err := m.Start(ctx, name); err != nil {
				m.logger.Warn("reconcile: start failed", "server", name, "error", err)
			}
		}
	}

	m.logger.Info("reconcile complete",
		"removed", len(toRemove), "restarted", len(toRestart), "added", len(toAdd))
	return nil
}

// SetStatus updates a server's status (used by health monitor).
func (m *Manager) SetStatus(name string, status models.ServerStatus, lastErr string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[name]; ok {
		e.Status = status
		e.LastError = lastErr
		if status == models.StatusRunning {
			e.LastPing = time.Now()
		}
	}
}

// BackendStatus returns the current status of a named backend.
// Implements lifecycle.StatusChecker for use by BackendSupervisor.
func (m *Manager) BackendStatus(name string) models.ServerStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.entries[name]
	if !ok {
		return models.StatusDisabled
	}
	return e.Status
}

// SetTools replaces the tool list for a managed server entry.
func (m *Manager) SetTools(name string, tools []models.ToolInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[name]; ok {
		e.Tools = tools
	}
}

// SetSession injects an *mcp.ClientSession for a managed server entry.
// Intended for tests that need a live session without going through the
// Start() connect path (stdio/http/sse). Mirrors SetStatus/SetTools in shape.
// Production code paths MUST continue to use connect(); this helper does not
// record transport, cmd, or client state the lifecycle manager would
// normally own.
func (m *Manager) SetSession(name string, session *mcp.ClientSession) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[name]; ok {
		e.session = session
	}
}

// configChanged does a simple comparison of two server configs.
func configChanged(a, b *models.ServerConfig) bool {
	if a.Command != b.Command || a.URL != b.URL || a.RestURL != b.RestURL {
		return true
	}
	if a.Cwd != b.Cwd || a.HealthEndpoint != b.HealthEndpoint {
		return true
	}
	if a.Disabled != b.Disabled {
		return true
	}
	if len(a.Args) != len(b.Args) {
		return true
	}
	for i := range a.Args {
		if a.Args[i] != b.Args[i] {
			return true
		}
	}
	if len(a.Env) != len(b.Env) {
		return true
	}
	for i := range a.Env {
		if a.Env[i] != b.Env[i] {
			return true
		}
	}
	if len(a.Headers) != len(b.Headers) {
		return true
	}
	for k, v := range a.Headers {
		if b.Headers[k] != v {
			return true
		}
	}
	// Compare ExposeTools (nil-safe: nil means default true).
	if (a.ExposeTools == nil) != (b.ExposeTools == nil) {
		return true
	}
	if a.ExposeTools != nil && *a.ExposeTools != *b.ExposeTools {
		return true
	}
	return false
}
