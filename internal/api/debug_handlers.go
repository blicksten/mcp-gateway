package api

import (
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"mcp-gateway/internal/models"
	"mcp-gateway/internal/obs"
)

// debug_handlers.go implements the Phase-3 on-demand state dump
// (PLAN-logging-instrument.md §D.1): GET /api/v1/debug/dump.
//
// The dump is a REDACTED snapshot of gateway + backend + kill-history state so
// "which subsystem restarted backend X, and why" is answerable without log
// archaeology. It is registered in the SAME authed (Bearer) group as the other
// /api/v1 reads, so it inherits the 401-without-Bearer contract.
//
// Redaction contract (stricter than the event stream, PLAN §C.3 "/dump"):
//   - Never emit env VALUES — only the sorted list of env KEY names per backend.
//   - Every string field that could carry a secret (last_error, kill reasons,
//     restart reasons/actors) is passed through obs.Redact before marshalling.
//   - No tokens / cookies / authorization headers are surfaced anywhere.

// DebugDump is the top-level redacted state snapshot returned by
// GET /api/v1/debug/dump.
type DebugDump struct {
	Gateway     DebugGateway    `json:"gateway"`
	Backends    []DebugBackend  `json:"backends"`
	KillHistory []obs.KillEvent `json:"kill_history"`
}

// DebugGateway carries process-identity + uptime for the gateway itself.
type DebugGateway struct {
	RunID     string         `json:"run_id"`
	PID       int            `json:"pid"`
	PPID      int            `json:"ppid"`
	StartTS   string         `json:"start_ts,omitempty"` // RFC3339, "" when no monitor
	UptimeMs  int64          `json:"uptime_ms"`
	JobObject DebugJobObject `json:"job_object"`
}

// DebugJobObject mirrors lifecycle.Manager.JobObjectInfo. On non-Windows
// platforms (or when CreateJobObject failed) Enabled is false.
type DebugJobObject struct {
	Enabled     bool `json:"enabled"`
	KillOnClose bool `json:"kill_on_close"`
}

// DebugBackend is one row of the backend table. Only non-secret runtime state
// is surfaced; LastError is redacted; env VALUES are never included (EnvKeys
// only). RestartCount comes from lifecycle state; LastRestartReason/Actor are
// derived from the most-recent kill-history entry for this backend (the honest
// available source — the lifecycle ServerEntry does not itself carry a
// reason/actor field).
type DebugBackend struct {
	Name              string   `json:"name"`
	Status            string   `json:"status"`
	PID               int      `json:"pid,omitempty"`
	StartedAt         string   `json:"started_at,omitempty"`     // RFC3339, "" when not started
	UptimeMs          int64    `json:"uptime_ms"`                // 0 when not running
	LastHealthTS      string   `json:"last_health_ts,omitempty"` // RFC3339 of last successful ping
	RestartCount      int      `json:"restart_count"`
	LastRestartReason string   `json:"last_restart_reason,omitempty"`
	LastRestartActor  string   `json:"last_restart_actor,omitempty"`
	LastError         string   `json:"last_error,omitempty"`
	EnvKeys           []string `json:"env_keys,omitempty"`
}

// handleDebugDump returns the redacted on-demand state dump (PLAN §D.1).
// Registered under the authed (Bearer) group in Handler(), so an
// unauthenticated request is rejected with 401 before this handler runs.
func (s *Server) handleDebugDump(w http.ResponseWriter, _ *http.Request) {
	now := time.Now()

	dump := DebugDump{
		Gateway:     s.debugGateway(now),
		Backends:    s.debugBackends(now),
		KillHistory: s.debugKillHistory(),
	}

	writeJSON(w, http.StatusOK, dump)
}

// debugGateway assembles the gateway-identity section. Nil-safe on emitter and
// monitor (tests / --no-auth construct servers without one).
func (s *Server) debugGateway(now time.Time) DebugGateway {
	g := DebugGateway{
		RunID: s.emitter.RunID(), // "" when emitter nil or tracing off
		PID:   os.Getpid(),
		PPID:  os.Getppid(),
	}

	if s.monitor != nil {
		start := s.monitor.StartedAt()
		g.StartTS = start.UTC().Format(time.RFC3339)
		g.UptimeMs = now.Sub(start).Milliseconds()
		if g.UptimeMs < 0 {
			g.UptimeMs = 0
		}
	}

	enabled, killOnClose := s.lm.JobObjectInfo()
	g.JobObject = DebugJobObject{Enabled: enabled, KillOnClose: killOnClose}

	return g
}

// debugBackends builds the per-backend table from lifecycle state, deriving the
// last restart reason/actor from the kill-history ring and redacting every
// free-text field.
func (s *Server) debugBackends(now time.Time) []DebugBackend {
	entries := s.lm.Entries()
	// Index the most-recent kill-history entry per backend so each row can
	// surface last_restart_reason/actor without a second lock round-trip.
	lastKill := s.lastKillByBackend()

	out := make([]DebugBackend, 0, len(entries))
	for _, e := range entries {
		b := DebugBackend{
			Name:         e.Name,
			Status:       string(e.Status),
			PID:          e.PID,
			RestartCount: e.RestartCount,
			LastError:    redactString(e.LastError),
			EnvKeys:      envKeyNames(e.Config),
		}
		if !e.StartedAt.IsZero() {
			b.StartedAt = e.StartedAt.UTC().Format(time.RFC3339)
			if e.Status == models.StatusRunning {
				b.UptimeMs = now.Sub(e.StartedAt).Milliseconds()
				if b.UptimeMs < 0 {
					b.UptimeMs = 0
				}
			}
		}
		if !e.LastPing.IsZero() {
			b.LastHealthTS = e.LastPing.UTC().Format(time.RFC3339)
		}
		if k, ok := lastKill[e.Name]; ok {
			b.LastRestartReason = redactString(k.Reason)
			b.LastRestartActor = redactString(k.Actor)
		}
		out = append(out, b)
	}
	return out
}

// debugKillHistory returns the redacted kill-history ring (oldest-first).
// Reason/Actor/Method are enum-like labels but are still scrubbed defensively
// so a future caller pushing a free-text reason cannot leak a secret.
func (s *Server) debugKillHistory() []obs.KillEvent {
	events := s.emitter.Kills().Snapshot() // nil-safe: nil ring → nil slice
	if len(events) == 0 {
		return []obs.KillEvent{}
	}
	out := make([]obs.KillEvent, len(events))
	for i, ev := range events {
		ev.Backend = redactString(ev.Backend)
		ev.Actor = redactString(ev.Actor)
		ev.Reason = redactString(ev.Reason)
		ev.Method = redactString(ev.Method)
		out[i] = ev
	}
	return out
}

// lastKillByBackend returns the most-recent kill-history entry per backend
// name. The ring is oldest-first, so a plain forward overwrite leaves the
// newest entry per backend.
func (s *Server) lastKillByBackend() map[string]obs.KillEvent {
	snap := s.emitter.Kills().Snapshot()
	if len(snap) == 0 {
		return nil
	}
	m := make(map[string]obs.KillEvent, len(snap))
	for _, ev := range snap {
		if ev.Backend == "" {
			continue
		}
		m[ev.Backend] = ev
	}
	return m
}

// envKeyNames returns the sorted env KEY names for a backend config — never the
// values. Mirrors toView's env-key exposure (server.go) so the dump leaks no
// secret values: a backend's env is stored as "KEY=VALUE" strings and only the
// KEY before the first '=' is surfaced.
func envKeyNames(cfg models.ServerConfig) []string {
	if len(cfg.Env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(cfg.Env))
	for _, env := range cfg.Env {
		if key, _, ok := strings.Cut(env, "="); ok {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Strings(keys)
	return keys
}

// redactString runs a single string through the obs value-pattern scrub by
// routing it through Redact as a one-field map. Empty strings short-circuit so
// the common (no-error) case allocates nothing.
func redactString(s string) string {
	if s == "" {
		return s
	}
	out := obs.Redact(map[string]any{"v": s})
	if rv, ok := out["v"].(string); ok {
		return rv
	}
	return obsRedactedFallback
}

// obsRedactedFallback is returned only if Redact ever yields a non-string for a
// string input (it does not today) — a defensive default that never leaks the
// original value.
const obsRedactedFallback = "«redacted»"
