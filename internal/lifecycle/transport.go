package lifecycle

import (
	"context"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/url"
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
