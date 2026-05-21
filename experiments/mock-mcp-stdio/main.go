// Mock stdio MCP server for empirically validating Claude Code's behavior
// against the three product gaps in mcp-gateway:
//
//   A. Session resumption — server crashes mid-session; does the client
//      respawn + re-init cleanly?
//   B. Live tool catalog updates — server announces tools/list_changed
//      mid-session; does the client refresh the tool list without
//      a window reload?
//   C. Cold-start tools latency — server delays tools/list response; does
//      the client surface tools quickly or block on the slowest backend?
//
// Scenarios are controlled via environment variables so the same binary
// can be reused across experiments with different mcp.json configs.
//
// Logs go to MOCK_LOG_FILE (default: stderr of the spawning process,
// which Claude Code captures). Every JSON-RPC message in and out is
// timestamped + flushed so the operator can correlate with Claude Code's
// own logs.
//
// Run as a stdio MCP server (no HTTP). Designed to be launched directly
// by Claude Code via `.mcp.json`.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// scenario controls which behaviors are exercised this run. Each field
// is set from a MOCK_* environment variable; defaults are inert (zero
// delay, no notifications, no crash) so an unconfigured run is a
// well-behaved baseline.
type scenario struct {
	name string

	// initDelay applies to the first `initialize` request.
	initDelay time.Duration

	// toolsListDelay applies to every `tools/list` response — simulates
	// the slow-backend cold-start observed in main.go RebuildTools
	// where playwright takes ~15s to enumerate its tools.
	toolsListDelay time.Duration

	// initialToolCount = how many tools to expose at startup
	initialToolCount int

	// notifyAfter = if > 0, after that many seconds (from process start)
	// send a `notifications/tools/list_changed` and on the next tools/list
	// expose `extraToolCount` additional tools.
	notifyAfter    time.Duration
	extraToolCount int

	// crashAfter = if > 0, after that many seconds exit(1). Lets us see
	// whether Claude Code respawns + re-initializes cleanly.
	crashAfter time.Duration

	// logFile = where this mock writes its event log (in addition to stderr).
	logFile string

	// serverName = surfaces in tool names so multiple instances don't collide.
	serverName string
}

func loadScenario() scenario {
	atoi := func(s string, def int) int {
		if s == "" {
			return def
		}
		v, err := strconv.Atoi(s)
		if err != nil {
			return def
		}
		return v
	}
	dur := func(s string) time.Duration {
		if s == "" {
			return 0
		}
		v, err := strconv.Atoi(s)
		if err != nil {
			return 0
		}
		return time.Duration(v) * time.Second
	}
	name := os.Getenv("MOCK_SERVER_NAME")
	if name == "" {
		name = "mock"
	}
	return scenario{
		name:             os.Getenv("MOCK_SCENARIO_NAME"),
		initDelay:        dur(os.Getenv("MOCK_INIT_DELAY_S")),
		toolsListDelay:   dur(os.Getenv("MOCK_TOOLS_LIST_DELAY_S")),
		initialToolCount: atoi(os.Getenv("MOCK_INITIAL_TOOL_COUNT"), 2),
		notifyAfter:      dur(os.Getenv("MOCK_NOTIFY_AFTER_S")),
		extraToolCount:   atoi(os.Getenv("MOCK_EXTRA_TOOL_COUNT"), 2),
		crashAfter:       dur(os.Getenv("MOCK_CRASH_AFTER_S")),
		logFile:          os.Getenv("MOCK_LOG_FILE"),
		serverName:       name,
	}
}

type logger struct {
	mu   sync.Mutex
	file io.Writer
	t0   time.Time
}

func newLogger(path string) (*logger, error) {
	var w io.Writer = os.Stderr
	if path != "" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return nil, fmt.Errorf("open log: %w", err)
		}
		w = io.MultiWriter(os.Stderr, f)
	}
	return &logger{file: w, t0: time.Now()}, nil
}

func (l *logger) log(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	elapsed := time.Since(l.t0).Truncate(time.Millisecond)
	fmt.Fprintf(l.file, "[%-9s] %s\n", elapsed, fmt.Sprintf(format, args...))
}

// jsonRPCRequest is the union of fields we care about for incoming messages.
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Result  any    `json:"result,omitempty"`
	Error   any    `json:"error,omitempty"`
}

type jsonRPCNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"inputSchema"`
}

func makeTools(serverName, label string, count int) []tool {
	if count < 0 {
		count = 0
	}
	out := make([]tool, count)
	for i := range count {
		out[i] = tool{
			Name:        fmt.Sprintf("%s_%s_%d", serverName, label, i+1),
			Description: fmt.Sprintf("[%s] %s tool #%d — mock for experiment", serverName, label, i+1),
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		}
	}
	return out
}

type server struct {
	scen   scenario
	log    *logger
	writer *bufio.Writer
	writeM sync.Mutex

	notifiedExtra atomic
}

// atomic is a tiny bool-with-CAS wrapper to avoid pulling sync/atomic for one flag.
type atomic struct {
	mu  sync.Mutex
	val bool
}

func (a *atomic) get() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.val
}

func (a *atomic) set() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.val {
		return false
	}
	a.val = true
	return true
}

func (s *server) writeJSON(v any) error {
	s.writeM.Lock()
	defer s.writeM.Unlock()
	enc := json.NewEncoder(s.writer)
	if err := enc.Encode(v); err != nil {
		return err
	}
	return s.writer.Flush()
}

func (s *server) handleInitialize(id json.RawMessage) error {
	if s.scen.initDelay > 0 {
		s.log.log("simulating init delay: %s", s.scen.initDelay)
		time.Sleep(s.scen.initDelay)
	}
	resp := jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      json.RawMessage(id),
		Result: map[string]any{
			"protocolVersion": "2025-03-26",
			"capabilities": map[string]any{
				"tools": map[string]any{
					"listChanged": true,
				},
				"logging": map[string]any{},
			},
			"serverInfo": map[string]any{
				"name":    s.scen.serverName,
				"version": "mock-1.0",
			},
		},
	}
	s.log.log("RESP initialize")
	return s.writeJSON(resp)
}

func (s *server) handleToolsList(id json.RawMessage) error {
	if s.scen.toolsListDelay > 0 {
		s.log.log("simulating tools/list delay: %s", s.scen.toolsListDelay)
		time.Sleep(s.scen.toolsListDelay)
	}
	tools := makeTools(s.scen.serverName, "initial", s.scen.initialToolCount)
	if s.notifiedExtra.get() {
		tools = append(tools, makeTools(s.scen.serverName, "extra", s.scen.extraToolCount)...)
	}
	resp := jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      json.RawMessage(id),
		Result: map[string]any{
			"tools": tools,
		},
	}
	s.log.log("RESP tools/list (count=%d, extras_announced=%v)", len(tools), s.notifiedExtra.get())
	return s.writeJSON(resp)
}

func (s *server) handleToolsCall(id json.RawMessage, params json.RawMessage) error {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	_ = json.Unmarshal(params, &p)
	resp := jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      json.RawMessage(id),
		Result: map[string]any{
			"content": []map[string]any{
				{
					"type": "text",
					"text": fmt.Sprintf("mock %s called %s OK", s.scen.serverName, p.Name),
				},
			},
		},
	}
	s.log.log("RESP tools/call name=%s", p.Name)
	return s.writeJSON(resp)
}

func (s *server) handleUnknown(id json.RawMessage, method string) error {
	if len(id) == 0 || string(id) == "null" {
		s.log.log("DROP notification %s (no response required)", method)
		return nil
	}
	resp := jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      json.RawMessage(id),
		Error: map[string]any{
			"code":    -32601,
			"message": "method not found",
			"data":    method,
		},
	}
	s.log.log("RESP error -32601 for %s", method)
	return s.writeJSON(resp)
}

func (s *server) scheduleAfterEffects(ctx context.Context) {
	if s.scen.notifyAfter > 0 {
		go func() {
			t := time.NewTimer(s.scen.notifyAfter)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			if !s.notifiedExtra.set() {
				return
			}
			s.log.log("SCENARIO: sending notifications/tools/list_changed (extras now available)")
			n := jsonRPCNotification{
				JSONRPC: "2.0",
				Method:  "notifications/tools/list_changed",
			}
			_ = s.writeJSON(n)
		}()
	}
	if s.scen.crashAfter > 0 {
		go func() {
			t := time.NewTimer(s.scen.crashAfter)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			s.log.log("SCENARIO: crash now (exit 1)")
			_ = s.writer.Flush()
			os.Exit(1)
		}()
	}
}

func (s *server) serveStdin(_ context.Context, in io.Reader) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024*16)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var req jsonRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			s.log.log("PARSE ERROR: %v on input: %s", err, string(line))
			continue
		}
		s.log.log("RECV %s id=%s", req.Method, string(req.ID))
		switch req.Method {
		case "initialize":
			if err := s.handleInitialize(req.ID); err != nil {
				return err
			}
		case "notifications/initialized":
			s.log.log("CLIENT confirmed initialized")
		case "tools/list":
			if err := s.handleToolsList(req.ID); err != nil {
				return err
			}
		case "tools/call":
			if err := s.handleToolsCall(req.ID, req.Params); err != nil {
				return err
			}
		case "ping":
			resp := jsonRPCResponse{JSONRPC: "2.0", ID: json.RawMessage(req.ID), Result: map[string]any{}}
			_ = s.writeJSON(resp)
		default:
			if err := s.handleUnknown(req.ID, req.Method); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}

func main() {
	scen := loadScenario()
	lg, err := newLogger(scen.logFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mock log init failed:", err)
		os.Exit(2)
	}
	lg.log("=== mock-mcp-stdio start ===")
	lg.log("scenario: name=%q server=%q initDelay=%s toolsListDelay=%s initialTools=%d notifyAfter=%s extraTools=%d crashAfter=%s",
		scen.name, scen.serverName, scen.initDelay, scen.toolsListDelay, scen.initialToolCount, scen.notifyAfter, scen.extraToolCount, scen.crashAfter)

	w := bufio.NewWriter(os.Stdout)
	srv := &server{scen: scen, log: lg, writer: w}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.scheduleAfterEffects(ctx)

	if err := srv.serveStdin(ctx, os.Stdin); err != nil {
		lg.log("serveStdin error: %v", err)
		os.Exit(1)
	}
	lg.log("=== mock-mcp-stdio clean exit (stdin closed) ===")
}
