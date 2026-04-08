package lifecycle

import (
	"maps"
	"net/http"
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
