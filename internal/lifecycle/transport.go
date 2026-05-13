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

// httpClientWithHeaders returns an *http.Client that injects the given
// headers into every request. Returns nil if headers is empty.
func httpClientWithHeaders(headers map[string]string) *http.Client {
	if len(headers) == 0 {
		return nil
	}
	h := make(map[string]string, len(headers))
	maps.Copy(h, headers)
	return &http.Client{
		Transport: &headerTransport{
			base:    http.DefaultTransport,
			headers: h,
		},
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
