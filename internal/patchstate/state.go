// Package patchstate stores runtime state for the Claude Code webview patch:
// per-session heartbeats, a FIFO queue of pending actions the patch must
// execute, and probe results reported back by the patch.
//
// Durability (REVIEW-16 M-01): heartbeats and the pending-action queue are
// persisted to disk on every mutation (atomic tmp+rename, 0600). On gateway
// startup, State.Load reads the file and drops entries past their TTL. This
// closes the "pending reconnect lost on daemon restart" bug class.
//
// Concurrency: a single RWMutex protects all in-memory state. Disk I/O
// happens outside the lock with a snapshot. Callers may invoke methods
// concurrently.
//
// TTL policy (hard-coded, matches PLAN-16 §16.3.1–16.3.2):
//   - Heartbeats:      1 hour (per session_id, last-write wins)
//   - Pending actions: 10 minutes (FIFO; acked entries pruned)
//   - Probe results:   5 minutes (keyed by nonce)
//
// Debounce policy:
//   - Heartbeat persist: at most once per 30 s per session_id (amortize disk
//     I/O for steady-state 60 s heartbeats + any extra mutation triggers).
//   - Reconnect-action enqueue: 500 ms coalescing window — a second enqueue
//     within this window is dropped. Prevents action-flood on bulk backend
//     mutations. The webview patch applies an additional 10 s debounce
//     (PLAN-16 T16.4.3) on top of this server-side window.
package patchstate

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Reconnect-action scoping invariant (PLAN-16 P4-08): our plugin exposes ONE
// aggregate MCP server to Claude Code; per-backend surfacing is explicitly
// out of scope. Every queued reconnect targets this constant.
const AggregatePluginServerName = "mcp-gateway"

const (
	// HeartbeatTTL is the TTL for stored heartbeats per session_id.
	HeartbeatTTL = 1 * time.Hour
	// ActionTTL is the TTL for undelivered pending actions.
	ActionTTL = 10 * time.Minute
	// ProbeTTL is the TTL for stored probe results.
	ProbeTTL = 5 * time.Minute

	// HeartbeatPersistDebounce is the minimum gap between disk persists per
	// session_id during steady-state heartbeats.
	HeartbeatPersistDebounce = 30 * time.Second
	// ReconnectActionDebounce is the coalescing window for reconnect
	// enqueues — a second enqueue inside the window is dropped.
	ReconnectActionDebounce = 500 * time.Millisecond

	// filePerm is the mode applied to the persisted state file. Matches
	// auth.token (0600, owner-only). On Windows os.Chmod only toggles the
	// read-only attribute — the 0600 semantic maps to nothing useful, and
	// owner-only isolation is enforced via DACLs at the parent directory
	// level (Phase 15.C). writeAtomic still calls Chmod on Windows; the
	// operation is a no-op but its error path logs rather than returns.
	filePerm os.FileMode = 0o600

	// MaxActionsQueued is a hard cap on the in-memory queue to avoid
	// unbounded growth if a patch disappears without acking (e.g. VSCode
	// crashed mid-session). Excess entries are dropped from the head.
	MaxActionsQueued = 256
	// MaxProbesCached is a hard cap on the probe-result map.
	MaxProbesCached = 128
	// MaxHeartbeatsCached is a hard cap on per-session heartbeats.
	MaxHeartbeatsCached = 64
)

// Heartbeat is the JSON payload the patch sends on POST
// /api/v1/claude-code/patch-heartbeat. Field names mirror PLAN-16 T16.3.1 +
// T16.4.3 verbatim so the webview-side and server-side schemas stay aligned
// under one source of truth.
type Heartbeat struct {
	SessionID              string `json:"session_id"`
	PatchVersion           string `json:"patch_version"`
	CCVersion              string `json:"cc_version"`
	VSCodeVersion          string `json:"vscode_version"`
	FiberOK                bool   `json:"fiber_ok"`
	MCPMethodOK            bool   `json:"mcp_method_ok"`
	MCPMethodFiberDepth    int    `json:"mcp_method_fiber_depth"`
	LastReconnectLatencyMs int    `json:"last_reconnect_latency_ms"`
	LastReconnectOK        bool   `json:"last_reconnect_ok"`
	LastReconnectError     string `json:"last_reconnect_error,omitempty"`
	PendingActionsInflight int    `json:"pending_actions_inflight"`
	FiberWalkRetryCount    int    `json:"fiber_walk_retry_count"`
	MCPSessionState        string `json:"mcp_session_state"`
	Timestamp              int64  `json:"ts"`
	// ReceivedAt is the server's wall clock at request time. Drives TTL
	// eviction independent of the client's reported `ts` (which may be
	// skewed or replayed).
	ReceivedAt time.Time `json:"received_at"`
}

// PendingAction is an instruction queued for the patch to execute. Currently
// two types exist:
//
//   - "reconnect":       production reload after a plugin regen. serverName
//     is always AggregatePluginServerName.
//   - "probe-reconnect": dashboard [Probe reconnect] button. serverName is
//     a fabricated "__probe_nonexistent_<nonce>" that the CC reconnect API
//     rejects — the intent is to exercise the round-trip path, not reload
//     any real server.
type PendingAction struct {
	ID          string    `json:"id"`
	Type        string    `json:"type"`
	ServerName  string    `json:"serverName"`
	Nonce       string    `json:"nonce"`
	CreatedAt   time.Time `json:"created_at"`
	Delivered   bool      `json:"delivered"`
	DeliveredAt time.Time `json:"delivered_at,omitzero"`
}

// ProbeResult is the patch's report of a [Probe reconnect] result,
// correlated by nonce.
type ProbeResult struct {
	Nonce      string    `json:"nonce"`
	OK         bool      `json:"ok"`
	Error      string    `json:"error,omitempty"`
	ReceivedAt time.Time `json:"received_at"`
}

// State is the concurrent owner of heartbeats, pending actions, and probe
// results. Construct via New.
type State struct {
	mu                sync.RWMutex
	heartbeats        map[string]*Heartbeat
	actions           []*PendingAction
	probes            map[string]*ProbeResult
	lastActionEnqueue time.Time
	lastPersist       map[string]time.Time

	persistPath string
	logger      *slog.Logger
	// persistMu serializes disk writes so concurrent async persists don't
	// collide on rename(2) (Windows returns "Access is denied" if two
	// writers race the same target).
	persistMu sync.Mutex
	// persistWg tracks in-flight persist goroutines so Flush can wait for
	// them (tests rely on this; production code is best-effort).
	persistWg sync.WaitGroup

	// Overridable for tests.
	now func() time.Time

	// Cleaner goroutine state.
	stopCh chan struct{}
	done   chan struct{}
}

// New constructs a State. persistPath may be empty to disable disk
// persistence (useful in tests). logger defaults to slog.Default when nil.
func New(persistPath string, logger *slog.Logger) *State {
	if logger == nil {
		logger = slog.Default()
	}
	return &State{
		heartbeats:  make(map[string]*Heartbeat),
		actions:     make([]*PendingAction, 0, 32),
		probes:      make(map[string]*ProbeResult),
		lastPersist: make(map[string]time.Time),
		persistPath: persistPath,
		logger:      logger,
		now:         time.Now,
	}
}

// persisted is the on-disk shape. actions and heartbeats are durable;
// probes are ephemeral (5 min TTL is short enough that reload loss is fine).
type persisted struct {
	Heartbeats map[string]*Heartbeat `json:"heartbeats"`
	Actions    []*PendingAction      `json:"actions"`
}

// Load reads persistPath (if set) and rehydrates state. Missing file is not
// an error. Expired entries are dropped by TTL at load time.
func (s *State) Load() error {
	if s.persistPath == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.persistPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read patch state: %w", err)
	}
	if len(data) == 0 {
		return nil
	}
	var p persisted
	if err := json.Unmarshal(data, &p); err != nil {
		// Corrupt file: log, discard, and start fresh. Stale state is
		// strictly better than a boot loop.
		s.logger.Warn("patch-state file corrupt — starting fresh", "path", s.persistPath, "error", err)
		return nil
	}

	now := s.now()
	for sid, hb := range p.Heartbeats {
		if hb == nil || now.Sub(hb.ReceivedAt) > HeartbeatTTL {
			continue
		}
		s.heartbeats[sid] = hb
	}
	for _, act := range p.Actions {
		if act == nil || act.Delivered {
			continue
		}
		if now.Sub(act.CreatedAt) > ActionTTL {
			continue
		}
		s.actions = append(s.actions, act)
	}
	s.logger.Info("patch-state loaded", "heartbeats", len(s.heartbeats), "actions", len(s.actions))
	return nil
}

// RecordHeartbeat stores (or overwrites) the heartbeat for hb.SessionID and
// triggers a debounced persist. Returns a copy of the stored entry so
// callers can build the response body without holding the lock.
//
// Empty SessionID is rejected with an error — the caller (HTTP handler)
// maps this to 400.
func (s *State) RecordHeartbeat(hb Heartbeat) (*Heartbeat, error) {
	if hb.SessionID == "" {
		return nil, errors.New("session_id is required")
	}
	s.mu.Lock()
	hb.ReceivedAt = s.now()
	stored := hb
	s.heartbeats[hb.SessionID] = &stored

	// Evict oldest session if above cap. This is a hard safety net; real
	// deployments have <10 concurrent VSCode windows per host.
	if len(s.heartbeats) > MaxHeartbeatsCached {
		var oldestSID string
		var oldestAt time.Time
		for sid, existing := range s.heartbeats {
			if oldestSID == "" || existing.ReceivedAt.Before(oldestAt) {
				oldestSID = sid
				oldestAt = existing.ReceivedAt
			}
		}
		if oldestSID != "" && oldestSID != hb.SessionID {
			delete(s.heartbeats, oldestSID)
		}
	}

	// Debounce: persist only if no record yet, or > HeartbeatPersistDebounce
	// since last persist for this session_id.
	last, ok := s.lastPersist[hb.SessionID]
	shouldPersist := !ok || s.now().Sub(last) > HeartbeatPersistDebounce
	if shouldPersist {
		s.lastPersist[hb.SessionID] = s.now()
	}
	s.mu.Unlock()

	if shouldPersist {
		s.persistAsync()
	}
	return &stored, nil
}

// Heartbeats returns a snapshot of non-expired heartbeats across all active
// sessions. Safe to call without the caller holding any lock.
func (s *State) Heartbeats() []*Heartbeat {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := s.now()
	out := make([]*Heartbeat, 0, len(s.heartbeats))
	for _, hb := range s.heartbeats {
		if hb == nil || now.Sub(hb.ReceivedAt) > HeartbeatTTL {
			continue
		}
		clone := *hb
		out = append(out, &clone)
	}
	return out
}

// EnqueueReconnectAction adds a production reconnect action for serverName.
// Coalesces with prior enqueues inside ReconnectActionDebounce — the second
// caller returns a nil action and ok=false signaling "coalesced". P4-08:
// callers from TriggerPluginRegen should always pass
// AggregatePluginServerName here.
func (s *State) EnqueueReconnectAction(serverName string) (*PendingAction, bool) {
	if serverName == "" {
		return nil, false
	}
	s.mu.Lock()
	now := s.now()
	if !s.lastActionEnqueue.IsZero() && now.Sub(s.lastActionEnqueue) < ReconnectActionDebounce {
		s.mu.Unlock()
		return nil, false
	}
	s.lastActionEnqueue = now
	id := newID()
	act := &PendingAction{
		ID:         id,
		Type:       "reconnect",
		ServerName: serverName,
		Nonce:      newNonce(),
		CreatedAt:  now,
	}
	s.actions = append(s.actions, act)
	s.trimActions()
	clone := *act
	s.mu.Unlock()

	s.persistAsync()
	return &clone, true
}

// EnqueueProbeAction adds a dashboard-triggered probe-reconnect action
// with a server-generated nonce. Not subject to debounce coalescing —
// probes are user-initiated.
func (s *State) EnqueueProbeAction() (*PendingAction, error) {
	return s.EnqueueProbeActionWithNonce(newNonce())
}

// EnqueueProbeActionWithNonce adds a probe-reconnect action with a
// caller-supplied nonce so the dashboard (which generated the nonce
// client-side for correlation with the subsequent probe-result) can
// match the asynchronous round-trip without an extra ID translation
// hop. Nonce must be non-empty; the handler layer enforces minimum
// length per the FROZEN API contract.
func (s *State) EnqueueProbeActionWithNonce(nonce string) (*PendingAction, error) {
	if nonce == "" {
		return nil, errors.New("nonce is required")
	}
	s.mu.Lock()
	act := &PendingAction{
		ID:         newID(),
		Type:       "probe-reconnect",
		ServerName: "__probe_nonexistent_" + nonce,
		Nonce:      nonce,
		CreatedAt:  s.now(),
	}
	s.actions = append(s.actions, act)
	s.trimActions()
	clone := *act
	s.mu.Unlock()

	s.persistAsync()
	return &clone, nil
}

// PendingActions returns undelivered actions created after the `after`
// cursor. If after is empty, returns all undelivered actions. The cursor is
// an action ID; callers typically use the ID of the last-seen action to
// avoid re-processing. TTL-expired entries are filtered out (the cleaner
// goroutine removes them lazily).
//
// Cursor-miss behavior: if `after` does not match any action in the current
// slice (e.g. the cursor entry was trimmed away by the TTL cleaner between
// the client's last ack and next poll), the call falls back to returning
// ALL undelivered actions. Without this fallback, a client polling with a
// stale cursor would see an empty list forever even while real pending
// work exists — a silent data-loss window. Returning everything on miss
// yields at-least-once semantics (the client may re-process actions it
// already acked), which is safe for our idempotent reconnect flow: each
// reconnect is a self-contained "refresh Claude Code's plugin view", and
// re-issuing one is harmless beyond a short redundant round-trip.
func (s *State) PendingActions(after string) []*PendingAction {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := s.now()

	cursorFound := false
	if after != "" {
		for _, act := range s.actions {
			if act.ID == after {
				cursorFound = true
				break
			}
		}
	}

	skipping := after != "" && cursorFound
	out := make([]*PendingAction, 0, len(s.actions))
	for _, act := range s.actions {
		if skipping {
			if act.ID == after {
				// Flip the flag, then `continue`: the cursor entry
				// itself is consumed and not returned. The next iteration
				// reaches the filter branch below.
				skipping = false
			}
			continue
		}
		if act.Delivered {
			continue
		}
		if now.Sub(act.CreatedAt) > ActionTTL {
			continue
		}
		clone := *act
		out = append(out, &clone)
	}
	return out
}

// AckAction marks the action as delivered. Returns true if the action was
// found and newly-acked (idempotent: second ack returns true but is a
// no-op).
func (s *State) AckAction(id string) bool {
	if id == "" {
		return false
	}
	s.mu.Lock()
	var found bool
	for _, act := range s.actions {
		if act.ID == id {
			if !act.Delivered {
				act.Delivered = true
				act.DeliveredAt = s.now()
			}
			found = true
			break
		}
	}
	s.mu.Unlock()
	if found {
		s.persistAsync()
	}
	return found
}

// RecordProbeResult stores a probe outcome keyed by nonce. Overwrites on
// duplicate nonce.
func (s *State) RecordProbeResult(pr ProbeResult) error {
	if pr.Nonce == "" {
		return errors.New("nonce is required")
	}
	s.mu.Lock()
	pr.ReceivedAt = s.now()
	s.probes[pr.Nonce] = &pr
	if len(s.probes) > MaxProbesCached {
		// Evict oldest by ReceivedAt.
		var oldestN string
		var oldestAt time.Time
		for n, existing := range s.probes {
			if oldestN == "" || existing.ReceivedAt.Before(oldestAt) {
				oldestN = n
				oldestAt = existing.ReceivedAt
			}
		}
		if oldestN != "" && oldestN != pr.Nonce {
			delete(s.probes, oldestN)
		}
	}
	s.mu.Unlock()
	return nil
}

// ProbeResult returns the probe for nonce, or nil if unknown / expired.
func (s *State) ProbeResult(nonce string) *ProbeResult {
	s.mu.RLock()
	defer s.mu.RUnlock()
	pr, ok := s.probes[nonce]
	if !ok {
		return nil
	}
	if s.now().Sub(pr.ReceivedAt) > ProbeTTL {
		return nil
	}
	clone := *pr
	return &clone
}

// trimActions drops delivered actions older than ActionTTL and caps the
// queue length. Assumes caller holds s.mu write lock.
//
// A fresh slice is allocated rather than reusing s.actions[:0] as the
// backing array. Aliasing would leave dropped *PendingAction pointers
// lingering in the unreachable tail of the old backing array until the
// slice grew past its capacity — a subtle GC retention trap if a future
// PendingAction gains a large embedded buffer.
func (s *State) trimActions() {
	now := s.now()
	kept := make([]*PendingAction, 0, len(s.actions))
	for _, act := range s.actions {
		if act.Delivered && now.Sub(act.DeliveredAt) > ActionTTL {
			continue
		}
		if !act.Delivered && now.Sub(act.CreatedAt) > ActionTTL {
			continue
		}
		kept = append(kept, act)
	}
	if len(kept) > MaxActionsQueued {
		// Drop from head (oldest first). Copy into a fresh slice rather
		// than reslicing so the GC-retention rationale from the comment
		// above holds for the overflow path too — the old dropped
		// pointers are unreachable once kept is reassigned.
		overflow := len(kept) - MaxActionsQueued
		trimmed := make([]*PendingAction, MaxActionsQueued)
		copy(trimmed, kept[overflow:])
		kept = trimmed
	}
	s.actions = kept
}

// StartCleaner launches the TTL cleaner goroutine. tickInterval controls
// how often expired entries are pruned. Call Stop() to terminate.
func (s *State) StartCleaner(tickInterval time.Duration) {
	if tickInterval <= 0 {
		tickInterval = 30 * time.Second
	}
	s.mu.Lock()
	if s.stopCh != nil {
		s.mu.Unlock()
		return // already running
	}
	s.stopCh = make(chan struct{})
	s.done = make(chan struct{})
	stopCh := s.stopCh
	done := s.done
	s.mu.Unlock()

	go func() {
		defer close(done)
		t := time.NewTicker(tickInterval)
		defer t.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-t.C:
				s.evictExpired()
			}
		}
	}()
}

// Stop signals the cleaner goroutine to terminate and waits for it.
// Idempotent; subsequent calls are no-ops.
func (s *State) Stop() {
	s.mu.Lock()
	stopCh := s.stopCh
	done := s.done
	s.stopCh = nil
	s.done = nil
	s.mu.Unlock()
	if stopCh != nil {
		close(stopCh)
	}
	if done != nil {
		<-done
	}
}

// evictExpired prunes all TTL-expired entries. Called by the cleaner ticker.
func (s *State) evictExpired() {
	s.mu.Lock()
	now := s.now()
	for sid, hb := range s.heartbeats {
		if hb == nil || now.Sub(hb.ReceivedAt) > HeartbeatTTL {
			delete(s.heartbeats, sid)
			delete(s.lastPersist, sid)
		}
	}
	for nonce, pr := range s.probes {
		if pr == nil || now.Sub(pr.ReceivedAt) > ProbeTTL {
			delete(s.probes, nonce)
		}
	}
	s.trimActions()
	s.mu.Unlock()
}

// FlushPersists blocks until all in-flight persist goroutines complete.
// Intended for tests and graceful daemon shutdown; production request paths
// never need this (the mutation already succeeded in-memory).
func (s *State) FlushPersists() {
	s.persistWg.Wait()
}

// persistAsync schedules a state snapshot + atomic write. Non-blocking from
// the caller's perspective (spawns a goroutine). Errors are logged but not
// returned — the caller's mutation already succeeded in-memory.
//
// The snapshot is taken INSIDE the persistMu critical section so concurrent
// persist calls always write the freshest view. Without this, two persist
// goroutines can each take a snapshot, then race on who writes second;
// last-writer-wins means an earlier snapshot can overwrite a newer one,
// effectively losing actions.
func (s *State) persistAsync() {
	if s.persistPath == "" {
		return
	}
	s.persistWg.Go(func() {
		s.persistMu.Lock()
		defer s.persistMu.Unlock()
		s.mu.RLock()
		snap := persisted{
			Heartbeats: make(map[string]*Heartbeat, len(s.heartbeats)),
			Actions:    make([]*PendingAction, 0, len(s.actions)),
		}
		for sid, hb := range s.heartbeats {
			clone := *hb
			snap.Heartbeats[sid] = &clone
		}
		for _, act := range s.actions {
			if act.Delivered {
				// Delivered entries are normally trimmed by trimActions
				// before persist fires. This guard handles the narrow
				// window between ack and the next enqueue/cleaner tick —
				// no need to re-persist work the client has already
				// confirmed it saw.
				continue
			}
			clone := *act
			snap.Actions = append(snap.Actions, &clone)
		}
		s.mu.RUnlock()
		if err := writeAtomic(s.persistPath, snap); err != nil {
			s.logger.Warn("patch-state persist failed", "path", s.persistPath, "error", err)
		}
	})
}

// writeAtomic serializes snap to persistPath via CreateTemp + rename(2).
// 0600 mode; directory is created if missing.
func writeAtomic(persistPath string, snap persisted) error {
	dir := filepath.Dir(persistPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "patch-state.*.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		// Remove tmp on any error path (the successful path rename(2)s it
		// away so Remove is a no-op returning ENOENT).
		_ = os.Remove(tmpName)
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}
	if err := os.Chmod(tmpName, filePerm); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}
	if err := os.Rename(tmpName, persistPath); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// newID returns a 16-hex-char random identifier for PendingAction.ID.
func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// newNonce returns a 16-hex-char random nonce. Separate function for call-
// site clarity, even though the shape matches newID.
func newNonce() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
