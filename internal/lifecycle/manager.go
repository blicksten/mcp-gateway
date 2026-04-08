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
	m := &Manager{
		entries:  make(map[string]*entry),
		impl:     &mcp.Implementation{Name: "mcp-gateway", Version: version},
		logger:   logger,
		job:      job,
		jobValid: jobValid,
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

	session, client, transport, cmd, err := m.connectSafe(ctx, name, &cfg)

	m.mu.Lock()
	e, ok = m.entries[name]
	if !ok {
		m.mu.Unlock()
		if session != nil {
			_ = session.Close()
		}
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
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
func (m *Manager) connect(ctx context.Context, name string, cfg *models.ServerConfig) (
	*mcp.ClientSession, *mcp.Client, mcp.Transport, *exec.Cmd, error,
) {
	client := mcp.NewClient(&mcp.Implementation{
		Name:    fmt.Sprintf("mcp-gateway/%s", name),
		Version: m.impl.Version,
	}, nil)

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
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
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

	return session, client, transport, cmd, nil
}

// scanStderr reads lines from a child process stderr and writes them to the ring buffer.
func scanStderr(ring *logbuf.Ring, r io.Reader, name string, logger *slog.Logger) {
	scanner := bufio.NewScanner(r)
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

// Stop disconnects from a backend server and kills its process if stdio.
func (m *Manager) Stop(ctx context.Context, name string) error {
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
	e.Status = models.StatusStopped
	e.PID = 0
	e.Tools = nil
	m.mu.Unlock()

	if session != nil {
		if cmd == nil {
			// Non-stdio (HTTP/SSE) — write synthetic log before closing session.
			m.writeLog(name, "session closing")
		}
		_ = session.Close()
	}

	// For stdio backends, ensure the child process is dead.
	if cmd != nil && cmd.Process != nil {
		done := make(chan struct{})
		go func() {
			_ = cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
			// Process exited cleanly.
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				m.logger.Error("process did not exit after kill", "server", name)
			}
		}
	}

	m.logger.Info("server stopped", "server", name)
	return nil
}

// Restart stops then starts a server. Increments restart count.
func (m *Manager) Restart(ctx context.Context, name string) error {
	if err := m.Stop(ctx, name); err != nil {
		m.logger.Warn("stop during restart failed", "server", name, "error", err)
	}

	m.mu.Lock()
	e, ok := m.entries[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("server %q not found", name)
	}
	e.RestartCount++
	e.Status = models.StatusRestarting
	m.mu.Unlock()

	return m.Start(ctx, name)
}

// StartAll starts all non-disabled servers concurrently.
func (m *Manager) StartAll(ctx context.Context) error {
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
}

// AddServer adds a new server entry. Does not start it.
func (m *Manager) AddServer(name string, cfg *models.ServerConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.entries[name]; exists {
		return fmt.Errorf("server %q already exists", name)
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

// RemoveServer stops and removes a server. Must be called without the lock held.
func (m *Manager) RemoveServer(ctx context.Context, name string) error {
	// Stop outside the lock.
	_ = m.Stop(ctx, name)

	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, name)
	return nil
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
		if err := m.RemoveServer(ctx, name); err != nil {
			m.logger.Warn("reconcile: remove failed", "server", name, "error", err)
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

// SetTools replaces the tool list for a managed server entry.
func (m *Manager) SetTools(name string, tools []models.ToolInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[name]; ok {
		e.Tools = tools
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
	return false
}
