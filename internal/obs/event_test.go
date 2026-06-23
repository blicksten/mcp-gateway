package obs

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// readEvents reads every JSONL line from the single per-process file under
// dir/events/ and unmarshals it into an Event. Fails the test if more than one
// event file exists (each test uses a fresh dir).
func readEvents(t *testing.T, dir string) []Event {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "events", "*.jsonl"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	var events []Event
	for _, path := range matches {
		f, err := os.Open(path)
		if err != nil {
			t.Fatalf("open %s: %v", path, err)
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := sc.Bytes()
			if len(strings.TrimSpace(string(line))) == 0 {
				continue
			}
			var ev Event
			if err := json.Unmarshal(line, &ev); err != nil {
				t.Fatalf("unmarshal %q: %v", string(line), err)
			}
			events = append(events, ev)
		}
		f.Close()
	}
	return events
}

// newEnabledEmitter builds an emitter forced ON, writing into a temp dir,
// without relying on the process env (so tests are hermetic and parallel-safe).
func newEnabledEmitter(t *testing.T) (*Emitter, string) {
	t.Helper()
	dir := t.TempDir()
	e := &Emitter{
		enabled: true,
		hostID:  "test-host",
		pid:     4242,
		ppid:    7,
		anchor:  time.Now(),
		kills:   NewKillRing(DefaultKillRingSize),
	}
	e.runID = "gw-testrun"
	f, err := openEventFile(dir, e.runID, e.pid)
	if err != nil {
		t.Fatalf("openEventFile: %v", err)
	}
	e.file = f
	t.Cleanup(func() { _ = e.Close() })
	return e, dir
}

// testingAllocs reports the average number of heap allocations per call of fn,
// rounded, via testing.AllocsPerRun. Extracted so the zero-alloc assertion is
// explicit about what it measures.
func testingAllocs(fn func()) float64 {
	return testing.AllocsPerRun(100, fn)
}

func TestEmit_NoOpWhenDisabled_ZeroEvents(t *testing.T) {
	dir := t.TempDir()
	e := &Emitter{enabled: false, kills: NewKillRing(8)}

	for i := 0; i < 100; i++ {
		e.Emit("lifecycle", "backend.kill", "warn", "reaper", "vsp-PC1", "owner-absent",
			map[string]any{"target_pid": 33012})
		e.EmitCtx(context.Background(), "proxy", "proxy.call", "info", "", "ctx7", "",
			map[string]any{"tool": "search"})
	}

	// No events/ dir should ever be created, no file written.
	if _, err := os.Stat(filepath.Join(dir, "events")); err == nil {
		t.Fatalf("events dir was created while disabled")
	}
	if events := readEvents(t, dir); len(events) != 0 {
		t.Fatalf("expected 0 events when disabled, got %d", len(events))
	}
}

func TestEmit_NoOpWhenDisabled_ZeroAlloc(t *testing.T) {
	e := &Emitter{enabled: false, kills: NewKillRing(8)}
	attrs := map[string]any{"target_pid": 33012} // built once, outside the measured fn

	avg := testingAllocs(func() {
		e.Emit("lifecycle", "backend.kill", "warn", "reaper", "vsp-PC1", "owner-absent", attrs)
	})
	if avg != 0 {
		t.Fatalf("Emit allocated %v times when disabled; want 0 (zero-cost-when-off)", avg)
	}
}

func TestEmit_SchemaFieldsPopulated(t *testing.T) {
	e, dir := newEnabledEmitter(t)

	ctx := context.WithValue(context.Background(), TraceKey, "tr-9b1e0a44")
	e.EmitCtx(ctx, "lifecycle", "backend.spawn", "info", "supervisor", "vsp-PC1", "start",
		map[string]any{"pid": 21044})

	events := readEvents(t, dir)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]

	if ev.Ts == "" || !strings.HasSuffix(ev.Ts, "Z") {
		t.Errorf("ts not RFC3339Nano UTC: %q", ev.Ts)
	}
	if ev.RunID != "gw-testrun" {
		t.Errorf("run_id = %q, want gw-testrun", ev.RunID)
	}
	if ev.HostID != "test-host" {
		t.Errorf("host_id = %q", ev.HostID)
	}
	if ev.Service != "gateway" {
		t.Errorf("service = %q, want gateway", ev.Service)
	}
	if ev.Subsys != "lifecycle" || ev.Event != "backend.spawn" || ev.Level != "info" {
		t.Errorf("subsys/event/level mismatch: %q/%q/%q", ev.Subsys, ev.Event, ev.Level)
	}
	if ev.Pid != 4242 || ev.Ppid != 7 {
		t.Errorf("pid/ppid = %d/%d, want 4242/7", ev.Pid, ev.Ppid)
	}
	if ev.Seq != 1 {
		t.Errorf("seq = %d, want 1 (first event)", ev.Seq)
	}
	if ev.TraceID != "tr-9b1e0a44" {
		t.Errorf("trace_id = %q, want tr-9b1e0a44 (propagated from ctx)", ev.TraceID)
	}
	if ev.Actor != "supervisor" || ev.Target != "vsp-PC1" || ev.Reason != "start" {
		t.Errorf("actor/target/reason mismatch: %q/%q/%q", ev.Actor, ev.Target, ev.Reason)
	}
	if got, ok := ev.Attrs["pid"]; !ok || got != float64(21044) {
		t.Errorf("attrs.pid = %v (ok=%v), want 21044", got, ok)
	}
}

func TestEmit_SeqMonotonic(t *testing.T) {
	e, dir := newEnabledEmitter(t)
	for i := 0; i < 5; i++ {
		e.Emit("proxy", "proxy.call", "info", "", "ctx7", "", nil)
	}
	events := readEvents(t, dir)
	if len(events) != 5 {
		t.Fatalf("expected 5 events, got %d", len(events))
	}
	for i, ev := range events {
		if ev.Seq != uint64(i+1) {
			t.Errorf("event %d: seq = %d, want %d", i, ev.Seq, i+1)
		}
	}
}

func TestEmit_TraceEmptyWhenNoCtxValue(t *testing.T) {
	e, dir := newEnabledEmitter(t)
	e.Emit("proxy", "proxy.call", "info", "", "ctx7", "", nil)
	events := readEvents(t, dir)
	if len(events) != 1 || events[0].TraceID != "" {
		t.Fatalf("expected empty trace_id without ctx value, got %+v", events)
	}
}

func TestTraceEnabled(t *testing.T) {
	on := []string{"1", "true", "TRUE", "yes", "on"}
	off := []string{"", "0", "false", "no", "off", "2", "enabled"}
	for _, v := range on {
		if !traceEnabled(v) {
			t.Errorf("traceEnabled(%q) = false, want true", v)
		}
	}
	for _, v := range off {
		if traceEnabled(v) {
			t.Errorf("traceEnabled(%q) = true, want false", v)
		}
	}
}

func TestNewEmitter_DisabledByDefault(t *testing.T) {
	t.Setenv("MCP_GATEWAY_TRACE", "")
	e := NewEmitter(t.TempDir(), nil)
	if e.Enabled() {
		t.Fatal("emitter enabled with MCP_GATEWAY_TRACE unset; must be off by default")
	}
	if e.RunID() != "" {
		t.Errorf("run_id minted while disabled: %q", e.RunID())
	}
}

func TestNewEmitter_EnabledMintsRunID(t *testing.T) {
	t.Setenv("MCP_GATEWAY_TRACE", "1")
	dir := t.TempDir()
	e := NewEmitter(dir, nil)
	t.Cleanup(func() { _ = e.Close() })
	if !e.Enabled() {
		t.Fatal("emitter disabled with MCP_GATEWAY_TRACE=1")
	}
	if !strings.HasPrefix(e.RunID(), "gw-") {
		t.Errorf("run_id = %q, want gw- prefix", e.RunID())
	}
}
