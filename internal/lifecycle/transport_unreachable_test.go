// Classifier unit tests for IsTransportUnreachable.
//
// Pins all 8 known error shapes that should map to StatusUnreachable so a
// future Go-stdlib change cannot silently reroute a TCP-level failure to
// StatusError (which would re-introduce the aggressive-restart pattern
// that this feature exists to eliminate).
//
// See docs/PLAN-unreachable-handling.md (R-1 of architect design).
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestIsTransportUnreachable_NilReturnsFalse(t *testing.T) {
	assert.False(t, IsTransportUnreachable(nil))
}

func TestIsTransportUnreachable_PlainErrorReturnsFalse(t *testing.T) {
	// Generic non-network error must NOT be classified as transport-unreachable;
	// preserves StatusError for application-level failures.
	assert.False(t, IsTransportUnreachable(errors.New("application error: invalid response")))
}

func TestIsTransportUnreachable_DNSError(t *testing.T) {
	err := &net.DNSError{
		Err:  "no such host",
		Name: "nonexistent.invalid",
	}
	assert.True(t, IsTransportUnreachable(err))
}

func TestIsTransportUnreachable_WrappedDNSError(t *testing.T) {
	inner := &net.DNSError{Err: "no such host", Name: "x.invalid"}
	wrapped := fmt.Errorf("dial tcp: lookup x.invalid: %w", inner)
	assert.True(t, IsTransportUnreachable(wrapped))
}

func TestIsTransportUnreachable_ConnRefused(t *testing.T) {
	assert.True(t, IsTransportUnreachable(syscall.ECONNREFUSED))
}

func TestIsTransportUnreachable_NetUnreach(t *testing.T) {
	assert.True(t, IsTransportUnreachable(syscall.ENETUNREACH))
}

func TestIsTransportUnreachable_HostUnreach(t *testing.T) {
	assert.True(t, IsTransportUnreachable(syscall.EHOSTUNREACH))
}

func TestIsTransportUnreachable_DialOpErrorTimeout(t *testing.T) {
	opErr := &net.OpError{
		Op:  "dial",
		Net: "tcp",
		Err: &timeoutError{},
	}
	assert.True(t, IsTransportUnreachable(opErr))
}

func TestIsTransportUnreachable_WindowsConnectexSubstring(t *testing.T) {
	// Defensive: Windows surfaces TCP-refusal through a custom message
	// when syscall.Errno wrapping fails. Pinned here so any silent change
	// in Go's Windows networking is caught in CI.
	opErr := &net.OpError{
		Op:  "dial",
		Net: "tcp",
		Err: errors.New(
			"connectex: A connection attempt failed because the connected " +
				"party did not properly respond after a period of time",
		),
	}
	assert.True(t, IsTransportUnreachable(opErr))
}

func TestIsTransportUnreachable_WindowsActivelyRefusedSubstring(t *testing.T) {
	opErr := &net.OpError{
		Op:  "dial",
		Net: "tcp",
		Err: errors.New(
			"connectex: No connection could be made because the target " +
				"machine actively refused it",
		),
	}
	assert.True(t, IsTransportUnreachable(opErr))
}

func TestIsTransportUnreachable_LinuxNoRouteToHostSubstring(t *testing.T) {
	opErr := &net.OpError{
		Op:  "dial",
		Net: "tcp",
		Err: errors.New("dial tcp 192.0.2.1:80: connect: no route to host"),
	}
	assert.True(t, IsTransportUnreachable(opErr))
}

func TestIsTransportUnreachable_DialDeadlineExceeded(t *testing.T) {
	opErr := &net.OpError{
		Op:  "dial",
		Net: "tcp",
		Err: context.DeadlineExceeded,
	}
	assert.True(t, IsTransportUnreachable(opErr))
}

func TestIsTransportUnreachable_BareDeadlineExceededReturnsFalse(t *testing.T) {
	// A naked context.DeadlineExceeded NOT paired with a dial-OpError
	// should preserve current behavior — could be a request-context
	// deadline from a higher layer that has nothing to do with reachability.
	assert.False(t, IsTransportUnreachable(context.DeadlineExceeded))
}

func TestIsTransportUnreachable_NonDialOpErrorReturnsFalse(t *testing.T) {
	// OpError with Op != "dial" (e.g. "read", "write") is NOT a
	// reachability problem — connection was established and broke later.
	// That belongs in StatusError, not StatusUnreachable.
	opErr := &net.OpError{
		Op:  "read",
		Net: "tcp",
		Err: &timeoutError{},
	}
	assert.False(t, IsTransportUnreachable(opErr))
}

func TestIsTransportUnreachable_HTTPProtocolErrorReturnsFalse(t *testing.T) {
	// Application/protocol-level errors must NOT be misrouted.
	assert.False(t, IsTransportUnreachable(errors.New("http: server returned 503 Service Unavailable")))
	assert.False(t, IsTransportUnreachable(errors.New("tls: handshake failure")))
	assert.False(t, IsTransportUnreachable(errors.New("EOF")))
}

// timeoutError implements net.Error with Timeout() == true to drive the
// opErr.Timeout() branch without depending on a real OS timeout firing.
type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

// Compile-time guard: timeoutError satisfies net.Error.
var _ net.Error = timeoutError{}

// Sanity: returns a duration > 0 in case any future test uses it.
func TestTimeoutErrorIsRealNetError(t *testing.T) {
	te := timeoutError{}
	assert.True(t, te.Timeout())
	// Confirm typical use shape.
	assert.Equal(t, "i/o timeout", te.Error())
	_ = time.Second // keep time import live
}
