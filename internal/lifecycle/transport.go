package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// backendInitTimeout is applied as ResponseHeaderTimeout on the shared
// backend transport. It bounds the MCP initialize handshake (header phase
// only) so a cloud backend that accepts TCP but never sends response headers
// does not block StartAll indefinitely.
//
// ResponseHeaderTimeout covers only the time between the end of the outgoing
// request and the arrival of response headers — it does NOT cut streaming
// response bodies, so long-running SSE streams and large tool responses are
// unaffected.
const backendInitTimeout = 30 * time.Second

// backendSharedTransport is the singleton RoundTripper used by all HTTP/SSE
// backend clients. Sharing one transport avoids per-cycle goroutine growth
// from idle connection pool goroutines (goroutine-leak guard). It mirrors
// http.DefaultTransport's settings and adds ResponseHeaderTimeout.
var backendSharedTransport = &http.Transport{ //nolint:gochecknoglobals — intentional shared transport; see comment
	Proxy: http.ProxyFromEnvironment,
	DialContext: (&net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext,
	TLSHandshakeTimeout:   10 * time.Second,
	ResponseHeaderTimeout: backendInitTimeout,
	ExpectContinueTimeout: 1 * time.Second,
	MaxIdleConns:          100,
	IdleConnTimeout:       90 * time.Second,
	ForceAttemptHTTP2:     true,
}

// headerTransport is an http.RoundTripper that injects custom headers
// into every outgoing request. Used to pass auth tokens and API keys
// to HTTP/SSE MCP backends.
type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone the request to avoid mutating the caller's *http.Request,
	// per the RoundTripper contract (net/http docs).
	r2 := req.Clone(req.Context())
	for k, v := range t.headers {
		r2.Header.Set(k, v)
	}
	return t.base.RoundTrip(r2)
}

// httpClientWithHeaders returns an *http.Client for MCP backend connections.
// When headers is non-empty, every request carries those headers (e.g. API
// keys). Always returns a non-nil client backed by backendSharedTransport so
// ResponseHeaderTimeout applies to every backend regardless of header config.
func httpClientWithHeaders(headers map[string]string) *http.Client {
	if len(headers) == 0 {
		return &http.Client{Transport: backendSharedTransport}
	}
	h := make(map[string]string, len(headers))
	maps.Copy(h, headers)
	return &http.Client{
		Transport: &headerTransport{base: backendSharedTransport, headers: h},
	}
}

// checkTCPReachable performs a short TCP dial to verify the host:port in rawURL
// is reachable. Returns nil on success. Returns a descriptive error on failure.
// Callers should use a timeout of 3-5 seconds for a fast pre-check before
// attempting a full MCP initialize handshake.
func checkTCPReachable(ctx context.Context, rawURL string, timeout time.Duration) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL %q: %w", rawURL, err)
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" || u.Scheme == "mcps" {
			port = "443"
		} else {
			port = "80"
		}
	}
	addr := net.JoinHostPort(host, port)
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("host unreachable %s: %w", addr, err)
	}
	conn.Close()
	return nil
}

// IsTransportUnreachable reports whether err represents a transport-layer
// failure to reach the backend host — distinct from a protocol-layer
// failure once the connection is established. Returns true for: DNS
// resolution failure, TCP connection refused, host/network unreachable,
// dial timeout (context deadline OR Go's underlying timeout). Returns
// false for protocol errors (HTTP 4xx/5xx, TLS handshake failure, MCP
// handshake reject) and for non-network errors.
//
// Used by the lifecycle manager to route TCP-unreachable failures to
// StatusUnreachable (slow-poll recovery, yellow UI badge) instead of
// StatusError (aggressive restart). See docs/PLAN-unreachable-handling.md.
//
// On Windows the underlying syscall errors map to ECONNREFUSED /
// ENETUNREACH / EHOSTUNREACH like POSIX, so the syscall checks work
// without OS branching. The Windows-only message substrings
// ("connectex: A connection attempt failed", "target machine actively
// refused") are kept as a defensive fallback in case Go's net package
// stops surfacing syscall.Errno through errors.Is on a future Windows
// build — pinned by the classifier unit tests so any regression is
// caught in CI.
func IsTransportUnreachable(err error) bool {
	if err == nil {
		return false
	}
	// 1. DNS resolution failure (e.g. "no such host", VPN-DNS down).
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	// 2. POSIX/Windows syscall errors meaning "transport refuses/can't reach".
	if errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ENETUNREACH) ||
		errors.Is(err, syscall.EHOSTUNREACH) {
		return true
	}
	// 3. net.OpError on the dial path that timed out — covers both
	//    "i/o timeout" raw and "connectex: A connection attempt failed"
	//    on Windows (which wraps WSAETIMEDOUT but not always exposed
	//    through errors.Is on older Go versions).
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		if opErr.Timeout() {
			return true
		}
		// Defensive Windows substring fallback. Pinned by unit tests.
		msg := opErr.Err.Error()
		if strings.Contains(msg, "connectex: A connection attempt failed") ||
			strings.Contains(msg, "target machine actively refused") ||
			strings.Contains(msg, "no route to host") ||
			strings.Contains(msg, "network is unreachable") {
			return true
		}
	}
	// 4. Dial-time context.DeadlineExceeded — caller passed a deadline
	//    that fired during the dial phase (e.g. our 3s checkTCPReachable
	//    timeout). On a healthy network the dial completes in <100 ms;
	//    a deadline-exceeded here is a strong signal of unreachable.
	//    NOTE: this branch is only taken when the dial was the operation
	//    that hit the deadline — request-context deadlines bubbling up
	//    from higher layers are handled by the syscall.Errno / DNSError
	//    branches above.
	if errors.Is(err, context.DeadlineExceeded) {
		// Only treat as unreachable when paired with a dial-shaped
		// net.OpError (the dial that timed out). Bare DeadlineExceeded
		// can come from any phase and should preserve current behavior.
		if errors.As(err, &opErr) && opErr.Op == "dial" {
			return true
		}
	}
	return false
}
