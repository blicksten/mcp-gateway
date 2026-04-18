// Package logbuf provides a thread-safe ring buffer for log lines
// with support for real-time subscribers (SSE streaming).
package logbuf

import (
	"sync"
	"time"
)

// DefaultCapacity is the default number of log lines retained.
const DefaultCapacity = 1000

// Line represents a single log entry.
type Line struct {
	Timestamp time.Time `json:"timestamp"`
	Text      string    `json:"text"`
}

// Ring is a fixed-size circular buffer of log lines.
// Subscribers receive new lines via channels in real time.
type Ring struct {
	mu     sync.Mutex
	lines  []Line
	head   int // next write position
	count  int
	cap    int
	subs   map[chan Line]struct{}
}

// New creates a ring buffer with the given capacity.
func New(capacity int) *Ring {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	return &Ring{
		lines: make([]Line, capacity),
		cap:   capacity,
		subs:  make(map[chan Line]struct{}),
	}
}

// Write appends a line to the buffer and notifies all subscribers.
// F-9 (Phase 13.B): every incoming line is passed through Redact so
// secret-shaped tokens are scrubbed before they enter the ring buffer,
// the SSE stream, or the on-disk log. The Redacted constant is fixed
// so operators can grep for evidence of redaction.
func (r *Ring) Write(text string) {
	line := Line{Timestamp: time.Now(), Text: Redact(text)}

	r.mu.Lock()
	r.lines[r.head] = line
	r.head = (r.head + 1) % r.cap
	if r.count < r.cap {
		r.count++
	}

	// Snapshot subscribers under lock to avoid holding lock during send.
	subs := make([]chan Line, 0, len(r.subs))
	for ch := range r.subs {
		subs = append(subs, ch)
	}
	r.mu.Unlock()

	// Non-blocking send to all subscribers.
	for _, ch := range subs {
		select {
		case ch <- line:
		default:
			// Subscriber is slow — drop the line for this subscriber.
		}
	}
}

// Lines returns all buffered lines in chronological order.
func (r *Ring) Lines() []Line {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.count == 0 {
		return nil
	}

	result := make([]Line, r.count)
	start := (r.head - r.count + r.cap) % r.cap
	for i := 0; i < r.count; i++ {
		result[i] = r.lines[(start+i)%r.cap]
	}
	return result
}

// Subscribe returns a channel that receives new log lines.
// The channel has a buffer of 64 lines; slow consumers lose lines.
// Call Unsubscribe to clean up.
func (r *Ring) Subscribe() chan Line {
	ch := make(chan Line, 64)
	r.mu.Lock()
	r.subs[ch] = struct{}{}
	r.mu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber channel from the notification set.
// The channel is NOT closed to avoid a send-on-closed-channel panic race
// with concurrent Write() calls that snapshot subscribers under lock and
// then send outside the lock. Callers should use context cancellation
// (not channel close) to signal the subscriber goroutine to exit.
func (r *Ring) Unsubscribe(ch chan Line) {
	r.mu.Lock()
	delete(r.subs, ch)
	r.mu.Unlock()
}

// Len returns the number of lines currently in the buffer.
func (r *Ring) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count
}
