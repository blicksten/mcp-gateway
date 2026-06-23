package obs

import (
	"sync"
	"time"
)

// DefaultKillRingSize is the number of recent kill/restart events retained for
// the Phase-3 debug dump (PLAN §D.1).
const DefaultKillRingSize = 64

// KillEvent is one entry of the kill-history ring: a backend/gateway kill or
// restart the gateway itself performed. It mirrors the schema's attributing
// fields so the debug dump can render "who killed what, and why" without log
// archaeology. No secret values — Reason/Actor/Method are enum-like labels.
type KillEvent struct {
	Ts      string `json:"ts"`      // RFC3339Nano UTC
	Backend string `json:"backend"` // target backend name
	Pid     int    `json:"pid"`     // target OS pid (0 when unknown)
	Actor   string `json:"actor"`   // suture|manual|reaper|connect-fail|job-assign-fail
	Reason  string `json:"reason"`  // why the kill/restart happened
	Method  string `json:"method"`  // how: terminate-group|kill-group|...
}

// KillRing is a fixed-size, concurrency-safe ring buffer of recent kill
// events. When full, the oldest entry is overwritten. Snapshot() returns the
// retained events in chronological (oldest-first) order.
type KillRing struct {
	mu   sync.Mutex
	buf  []KillEvent
	size int
	next int  // index of the next write slot
	full bool // true once the ring has wrapped at least once
}

// NewKillRing returns a ring retaining the most-recent size events. A size
// <= 0 falls back to DefaultKillRingSize.
func NewKillRing(size int) *KillRing {
	if size <= 0 {
		size = DefaultKillRingSize
	}
	return &KillRing{
		buf:  make([]KillEvent, size),
		size: size,
	}
}

// Push records a kill event, evicting the oldest when the ring is full. The
// Ts is stamped here when ev.Ts is empty so callers need not repeat it.
func (r *KillRing) Push(ev KillEvent) {
	if r == nil {
		return
	}
	if ev.Ts == "" {
		ev.Ts = time.Now().UTC().Format(time.RFC3339Nano)
	}
	r.mu.Lock()
	r.buf[r.next] = ev
	r.next = (r.next + 1) % r.size
	if r.next == 0 {
		r.full = true
	}
	r.mu.Unlock()
}

// Snapshot returns the retained events oldest-first. Safe for concurrent use;
// returns a copy so the caller cannot mutate ring state.
func (r *KillRing) Snapshot() []KillEvent {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full {
		// Not yet wrapped: live entries are buf[0:next].
		out := make([]KillEvent, r.next)
		copy(out, r.buf[:r.next])
		return out
	}
	// Wrapped: oldest is at r.next, wrapping around.
	out := make([]KillEvent, 0, r.size)
	for i := 0; i < r.size; i++ {
		out = append(out, r.buf[(r.next+i)%r.size])
	}
	return out
}

// Len returns the number of retained events.
func (r *KillRing) Len() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.full {
		return r.size
	}
	return r.next
}
