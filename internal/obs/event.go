// Package obs provides a toggleable, off-by-default structured-event emitter
// for the gateway (the Go side of the logging/observability instrument,
// PLAN-logging-instrument.md Phase 1).
//
// Design contract (PLAN §E "zero cost when off"):
//   - The emitter is gated by the MCP_GATEWAY_TRACE env var. When unset, Emit
//     short-circuits on the first line via Enabled() before constructing any
//     object, formatting any string, advancing seq, reading the monotonic
//     clock, or touching disk.
//   - Never echo a secret VALUE. Every attrs payload is passed through Redact
//     (field-allowlist + value-pattern scrub) before it is marshalled.
//
// One JSONL object per attributable event, schema per PLAN §A. Field order is
// not significant (JSON) but the struct is declared in schema order for
// human-greppability.
package obs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// TraceKey is the context key under which the API layer stores the inbound
// X-Trace-Id header value (PLAN §B.5). The lifecycle / proxy emitters read it
// back so every gateway-side event carries the originating orchestrator
// step's trace_id. It is an unexported type to avoid context-key collisions.
type traceKeyType struct{}

// TraceKey is the exported context key used to carry the trace id.
var TraceKey = traceKeyType{}

// TraceIDFromContext returns the X-Trace-Id propagated through ctx, or "".
func TraceIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(TraceKey).(string); ok {
		return v
	}
	return ""
}

// Event is the common JSONL line emitted for every attributable event
// (PLAN §A). Identical key set across all four services so a single CLI can
// merge and correlate. json tags use snake_case to match the schema.
type Event struct {
	Ts           string         `json:"ts"`             // RFC3339Nano, UTC, Z
	TsMonoNs     int64          `json:"ts_mono_ns"`     // per-process monotonic ns
	RunID        string         `json:"run_id"`         // one id per process lifetime
	HostID       string         `json:"host_id"`        // machine identity
	Service      string         `json:"service"`        // gateway|orchestrator|reaper|watchdog
	Subsys       string         `json:"subsys"`         // lifecycle|health|proxy|api|...
	Event        string         `json:"event"`          // dotted verb: backend.kill, proxy.call
	Level        string         `json:"level"`          // debug|info|warn|error
	Pid          int            `json:"pid"`            // OS process id
	Ppid         int            `json:"ppid"`           // parent pid
	Seq          uint64         `json:"seq"`            // per-process strict total order
	TraceID      string         `json:"trace_id"`       // minted in orchestrator, propagated
	ParentSpanID string         `json:"parent_span_id"` // nesting within a trace
	Actor        string         `json:"actor"`          // who acted: reaper|suture|...
	Target       string         `json:"target"`         // what was acted on
	Reason       string         `json:"reason"`         // why: keepalive-miss|owner-absent|...
	Attrs        map[string]any `json:"attrs"`          // event-specific, redacted payload
}

// serviceName is the fixed `service` value for the gateway emitter.
const serviceName = "gateway"

// Emitter writes structured events to a per-process JSONL file when enabled.
// A single instance is constructed at startup and shared across the gateway's
// lifecycle / proxy / health / api subsystems.
type Emitter struct {
	enabled bool // gate: MCP_GATEWAY_TRACE=1. The literal first check in Emit.

	runID  string
	hostID string
	pid    int
	ppid   int

	anchor time.Time     // process-start monotonic anchor for ts_mono_ns
	seq    atomic.Uint64 // per-process strict total order

	mu   sync.Mutex // serializes writes to file (single process, single handle)
	file *os.File   // per-process sink; nil when disabled or open failed

	// kills is the fixed-size ring of recent backend/gateway kill events,
	// surfaced by the Phase-3 debug dump. Always allocated (small, bounded)
	// so the dump endpoint can read it even before the first event; pushes
	// are gated by Enabled() at the call site.
	kills *KillRing

	logger *slog.Logger // used only for one-time open-failure warnings
}

// NewEmitter constructs the shared emitter. It is enabled only when
// MCP_GATEWAY_TRACE is set to a truthy value (matching the PLAN toggle). When
// disabled it returns a fully-constructed, no-op emitter: Emit returns on its
// first line and never allocates.
//
// configDir is the gateway config directory (~/.mcp-gateway); the per-process
// JSONL file lives under <configDir>/events/. A non-fatal open failure leaves
// the emitter enabled-but-fileless (events are dropped, logged once) rather
// than crashing the daemon — observability must never take the gateway down.
func NewEmitter(configDir string, logger *slog.Logger) *Emitter {
	if logger == nil {
		logger = slog.Default()
	}
	enabled := traceEnabled(os.Getenv("MCP_GATEWAY_TRACE"))

	e := &Emitter{
		enabled: enabled,
		hostID:  hostID(),
		pid:     os.Getpid(),
		ppid:    os.Getppid(),
		anchor:  time.Now(),
		kills:   NewKillRing(DefaultKillRingSize),
		logger:  logger,
	}
	if !enabled {
		// Off by default: no run_id minted, no dir created, no file opened.
		return e
	}

	e.runID = "gw-" + shortID()
	if f, err := openEventFile(configDir, e.runID, e.pid); err != nil {
		logger.Warn("obs: event file open failed; events disabled for this run",
			"error", err)
	} else {
		e.file = f
	}
	return e
}

// traceEnabled reports whether the MCP_GATEWAY_TRACE value enables tracing.
// "1", "true", "yes", "on" (case-insensitive) enable; everything else (incl.
// "", "0", "false") disables. String methods only — no regex.
func traceEnabled(v string) bool {
	switch v {
	case "1", "true", "TRUE", "True", "yes", "YES", "on", "ON":
		return true
	default:
		return false
	}
}

// Enabled reports whether the emitter will emit. Hot-path callers SHOULD check
// this first to avoid building an attrs map when tracing is off.
func (e *Emitter) Enabled() bool {
	return e != nil && e.enabled
}

// RunID returns the per-process run id ("" when disabled). Useful for the
// debug dump and for log correlation.
func (e *Emitter) RunID() string {
	if e == nil {
		return ""
	}
	return e.runID
}

// Kills returns the kill-event ring buffer (never nil for a constructed
// emitter), consumed by the Phase-3 debug dump.
func (e *Emitter) Kills() *KillRing {
	if e == nil {
		return nil
	}
	return e.kills
}

// Emit writes one JSONL event line. The enabled-check is the LITERAL FIRST
// statement: when tracing is off, Emit returns before any allocation,
// formatting, clock read, seq advance, or disk touch (PLAN §E zero-cost-when-
// off contract).
//
// attrs is redacted in place via Redact before marshalling; callers may pass
// nil. No secret VALUE is ever written.
func (e *Emitter) Emit(subsys, event, level, actor, target, reason string, attrs map[string]any) {
	if e == nil || !e.enabled {
		return
	}
	e.emitWithTrace("", subsys, event, level, actor, target, reason, attrs)
}

// EmitCtx is Emit with the trace_id read from ctx (PLAN §B.5: propagated from
// the inbound X-Trace-Id header). Same zero-cost-when-off contract.
func (e *Emitter) EmitCtx(ctx context.Context, subsys, event, level, actor, target, reason string, attrs map[string]any) {
	if e == nil || !e.enabled {
		return
	}
	e.emitWithTrace(TraceIDFromContext(ctx), subsys, event, level, actor, target, reason, attrs)
}

// emitWithTrace is the shared body. Callers have already passed the gate.
func (e *Emitter) emitWithTrace(traceID, subsys, event, level, actor, target, reason string, attrs map[string]any) {
	ev := Event{
		Ts:       time.Now().UTC().Format(time.RFC3339Nano),
		TsMonoNs: time.Since(e.anchor).Nanoseconds(),
		RunID:    e.runID,
		HostID:   e.hostID,
		Service:  serviceName,
		Subsys:   subsys,
		Event:    event,
		Level:    level,
		Pid:      e.pid,
		Ppid:     e.ppid,
		Seq:      e.seq.Add(1),
		TraceID:  traceID,
		Actor:    actor,
		Target:   target,
		Reason:   reason,
		Attrs:    Redact(attrs),
	}
	e.write(&ev)
}

// write marshals one event and appends it as a single line. Failures are
// swallowed (observability must not break the daemon); the file handle, when
// present, is append-mode and writes are serialized by mu.
func (e *Emitter) write(ev *Event) {
	if e.file == nil {
		return
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return
	}
	line = append(line, '\n')
	e.mu.Lock()
	_, _ = e.file.Write(line)
	e.mu.Unlock()
}

// Close flushes and closes the underlying file. Safe to call on a nil or
// disabled emitter.
func (e *Emitter) Close() error {
	if e == nil || e.file == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	err := e.file.Close()
	e.file = nil
	return err
}

// openEventFile creates <configDir>/events/ and opens the per-process JSONL
// file in append mode. File name: mcp-gateway-<run_id>-<pid>.jsonl, so no two
// live processes share a file (PLAN §C.1 — avoids Windows write contention).
func openEventFile(configDir, runID string, pid int) (*os.File, error) {
	dir := filepath.Join(configDir, "events")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	name := "mcp-gateway-" + runID + "-" + itoa(pid) + ".jsonl"
	path := filepath.Join(dir, name)
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}

// shortID returns an 8-hex-char random id (4 random bytes). Minted once per
// process at construction.
func shortID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is effectively impossible on supported
		// platforms; fall back to a monotonic-derived value so we never panic.
		n := time.Now().UnixNano()
		b[0] = byte(n)
		b[1] = byte(n >> 8)
		b[2] = byte(n >> 16)
		b[3] = byte(n >> 24)
	}
	return hex.EncodeToString(b[:])
}

// hostID returns the machine hostname, or "unknown" when it cannot be read.
func hostID() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "unknown"
	}
	return h
}

// itoa formats a non-negative int without importing strconv into the hot path
// (kept tiny; used only at file-open time).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
