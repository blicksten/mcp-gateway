// Package ctlclient provides an HTTP client for the MCP Gateway REST API.
package ctlclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Default timeout for HTTP requests.
const defaultTimeout = 10 * time.Second

// authHeaderProvider returns the Authorization header value, or "" to
// skip auth (legacy / unauthenticated daemon). Errors propagate to
// callers verbatim — CLI code surfaces them with guidance.
type authHeaderProvider func() (string, error)

// Client communicates with the MCP Gateway REST API.
type Client struct {
	baseURL      string
	httpClient   *http.Client
	streamClient *http.Client // no timeout — for long-lived SSE connections
	auth         authHeaderProvider
}

// New creates a Client for the given base URL (e.g. "http://127.0.0.1:8765").
// Authentication is disabled — use NewAuthed when a Bearer token is required.
func New(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: defaultTimeout,
		},
		streamClient: &http.Client{},
	}
}

// NewAuthed returns a client that prefixes every request with an
// `Authorization: Bearer <token>` header resolved from authHeader.
// The provider is consulted per request so token file rotations are
// picked up without recreating the client.
func NewAuthed(baseURL string, authHeader func() (string, error)) *Client {
	c := New(baseURL)
	c.auth = authHeader
	return c
}

// attachAuthHeader injects the Authorization header using the provider,
// if one is configured. A provider returning "" means auth is off and
// the request is sent unchanged.
func (c *Client) attachAuthHeader(req *http.Request) error {
	if c.auth == nil {
		return nil
	}
	header, err := c.auth()
	if err != nil {
		return err
	}
	if header != "" {
		req.Header.Set("Authorization", header)
	}
	return nil
}

// --- API response types (mirrors api.ServerView) ---

// HealthResponse is the response from GET /api/v1/health.
// Fields added in D.1 are tagged omitempty so that this struct can decode
// responses from older daemons gracefully (AUDIT L-2: decoder does NOT use
// DisallowUnknownFields, so new fields on the daemon side are also safe).
type HealthResponse struct {
	Status        string `json:"status"`
	Servers       int    `json:"servers"`
	Running       int    `json:"running"`
	Auth          string `json:"auth,omitempty"`
	StartedAt     string `json:"started_at,omitempty"`
	PID           int    `json:"pid,omitempty"`
	Version       string `json:"version,omitempty"`
	UptimeSeconds int64  `json:"uptime_seconds,omitempty"`
}

// ServerView is the API response for a server entry.
type ServerView struct {
	Name         string   `json:"name"`
	Status       string   `json:"status"`
	Transport    string   `json:"transport"`
	PID          int      `json:"pid,omitempty"`
	Tools        []Tool   `json:"tools,omitempty"`
	RestartCount int      `json:"restart_count"`
	LastError    string   `json:"last_error,omitempty"`
	EnvKeys      []string `json:"env_keys,omitempty"`
	HeaderKeys   []string `json:"header_keys,omitempty"`
}

// Tool describes a single tool from a backend.
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Server      string `json:"server"`
	InputSchema any    `json:"input_schema,omitempty"`
}

// ContentItem represents a single content block in a tool call result.
type ContentItem struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

// CallResult is the response from POST /api/v1/servers/{name}/call.
type CallResult struct {
	Content []ContentItem `json:"content"`
}

// ServerConfig mirrors the fields needed to add a server via the API.
type ServerConfig struct {
	Command  string            `json:"command,omitempty"`
	Args     []string          `json:"args,omitempty"`
	Cwd      string            `json:"cwd,omitempty"`
	Env      []string          `json:"env,omitempty"`
	URL      string            `json:"url,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Disabled bool              `json:"disabled,omitempty"`
}

// APIError is returned when the gateway responds with a non-success status.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("gateway error (HTTP %d): %s", e.StatusCode, e.Message)
}

// ConnectionError indicates the gateway is unreachable.
type ConnectionError struct {
	URL string
	Err error
}

func (e *ConnectionError) Error() string {
	return fmt.Sprintf("gateway not running at %s: %v", e.URL, e.Err)
}

func (e *ConnectionError) Unwrap() error { return e.Err }

// --- API methods ---

// Health calls GET /api/v1/health.
func (c *Client) Health(ctx context.Context) (*HealthResponse, error) {
	var resp HealthResponse
	if err := c.get(ctx, "/api/v1/health", &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ListServers calls GET /api/v1/servers.
func (c *Client) ListServers(ctx context.Context) ([]ServerView, error) {
	var servers []ServerView
	if err := c.get(ctx, "/api/v1/servers", &servers); err != nil {
		return nil, err
	}
	if servers == nil {
		servers = []ServerView{}
	}
	return servers, nil
}

// GetServer calls GET /api/v1/servers/{name}.
func (c *Client) GetServer(ctx context.Context, name string) (*ServerView, error) {
	var sv ServerView
	if err := c.get(ctx, "/api/v1/servers/"+url.PathEscape(name), &sv); err != nil {
		return nil, err
	}
	return &sv, nil
}

// AddServer calls POST /api/v1/servers with the given name and config.
func (c *Client) AddServer(ctx context.Context, name string, cfg ServerConfig) error {
	body := struct {
		Name   string       `json:"name"`
		Config ServerConfig `json:"config"`
	}{Name: name, Config: cfg}
	return c.post(ctx, "/api/v1/servers", body, nil)
}

// RemoveServer calls DELETE /api/v1/servers/{name}.
func (c *Client) RemoveServer(ctx context.Context, name string) error {
	return c.doRequest(ctx, "DELETE", "/api/v1/servers/"+url.PathEscape(name), nil, nil)
}

// PatchServer calls PATCH /api/v1/servers/{name} with the given patch body.
func (c *Client) PatchServer(ctx context.Context, name string, patch map[string]any) error {
	return c.doRequest(ctx, "PATCH", "/api/v1/servers/"+url.PathEscape(name), patch, nil)
}

// PatchServerEnv calls PATCH with add_env/remove_env fields.
func (c *Client) PatchServerEnv(ctx context.Context, name string, addEnv []string, removeEnv []string) error {
	patch := map[string]any{}
	if len(addEnv) > 0 {
		patch["add_env"] = addEnv
	}
	if len(removeEnv) > 0 {
		patch["remove_env"] = removeEnv
	}
	return c.PatchServer(ctx, name, patch)
}

// PatchServerHeaders calls PATCH with add_headers/remove_headers fields.
func (c *Client) PatchServerHeaders(ctx context.Context, name string, addHeaders map[string]string, removeHeaders []string) error {
	patch := map[string]any{}
	if len(addHeaders) > 0 {
		patch["add_headers"] = addHeaders
	}
	if len(removeHeaders) > 0 {
		patch["remove_headers"] = removeHeaders
	}
	return c.PatchServer(ctx, name, patch)
}

// RestartServer calls POST /api/v1/servers/{name}/restart.
func (c *Client) RestartServer(ctx context.Context, name string) error {
	return c.post(ctx, "/api/v1/servers/"+url.PathEscape(name)+"/restart", nil, nil)
}

// ResetCircuit calls POST /api/v1/servers/{name}/reset-circuit.
func (c *Client) ResetCircuit(ctx context.Context, name string) error {
	return c.post(ctx, "/api/v1/servers/"+url.PathEscape(name)+"/reset-circuit", nil, nil)
}

// Shutdown calls POST /api/v1/shutdown to request a graceful daemon exit.
// A 202 Accepted response is treated as success. Auth is forwarded via the
// same provider used by all other mutation endpoints.
func (c *Client) Shutdown(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/v1/shutdown", nil)
	if err != nil {
		return err
	}
	if err := c.attachAuthHeader(req); err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &ConnectionError{URL: c.baseURL, Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusAccepted {
		return nil
	}
	return c.parseErrorBody(resp)
}

// ListTools calls GET /api/v1/tools.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	var tools []Tool
	if err := c.get(ctx, "/api/v1/tools", &tools); err != nil {
		return nil, err
	}
	if tools == nil {
		tools = []Tool{}
	}
	return tools, nil
}

// CallTool calls POST /api/v1/servers/{server}/call with the given tool name and args.
func (c *Client) CallTool(ctx context.Context, server, tool string, args map[string]any) (*CallResult, error) {
	body := map[string]any{
		"tool":      tool,
		"arguments": args,
	}
	var result CallResult
	if err := c.post(ctx, "/api/v1/servers/"+url.PathEscape(server)+"/call", body, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// StreamLogsOptions configures log streaming behavior.
type StreamLogsOptions struct {
	// Reconnect enables automatic reconnection with exponential backoff.
	Reconnect bool
	// MaxRetries is the maximum number of consecutive reconnection attempts (default 10).
	MaxRetries int
	// InitialBackoff is the delay before the first reconnection (default 1s).
	InitialBackoff time.Duration
	// MaxBackoff caps the exponential backoff duration (default 5m).
	MaxBackoff time.Duration
}

// streamLogsOnce opens a single SSE connection and streams log lines.
// Returns nil on clean disconnect (EOF), context error on cancellation,
// or an error on failure. Does not reconnect.
func (c *Client) streamLogsOnce(ctx context.Context, name string, callback func(line string)) error {
	reqURL := c.baseURL + "/api/v1/servers/" + url.PathEscape(name) + "/logs"
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	if err := c.attachAuthHeader(req); err != nil {
		return err
	}

	resp, err := c.streamClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &ConnectionError{URL: c.baseURL, Err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return &APIError{StatusCode: 404, Message: "server not found"}
	}
	if resp.StatusCode != http.StatusOK {
		return c.parseErrorBody(resp)
	}

	scanner := bufio.NewScanner(resp.Body)
	// Raise SSE scanner line cap from default 64KB to 1MB. MCP server log
	// lines (long tracebacks, JSON traces) can exceed 64KB and were
	// truncating the stream with bufio.ErrTooLong. Paired with the
	// producer-side cap in lifecycle.scanStderr; both must agree for the
	// end-to-end cap to hold. 1MB bounds DoS risk for a localhost tool.
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		// Normalize bare \r (defensive against proxies).
		line = strings.ReplaceAll(line, "\r", "")

		if strings.HasPrefix(line, "data: ") {
			callback(line[6:])
		} else if strings.HasPrefix(line, "data:") {
			callback(line[5:])
		}
		// Ignore event:, id:, retry: and empty lines.
	}
	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	return nil
}

// StreamLogs opens an SSE connection to GET /api/v1/servers/{name}/logs
// and calls callback for each received line. Blocks until ctx is cancelled
// or the connection is closed. Returns nil on clean context cancellation.
//
// When opts is nil or opts.Reconnect is false, a single connection attempt is made.
// When reconnect is enabled, the client retries with exponential backoff on
// connection loss, resetting the retry counter after each successful data receipt.
// Reconnect stops immediately on HTTP 404 (server removed) or context cancellation.
func (c *Client) StreamLogs(ctx context.Context, name string, callback func(line string), opts *StreamLogsOptions) error {
	if opts == nil || !opts.Reconnect {
		err := c.streamLogsOnce(ctx, name, callback)
		if ctx.Err() != nil {
			return nil
		}
		return err
	}

	maxRetries := opts.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 10
	}
	initialBackoff := opts.InitialBackoff
	if initialBackoff <= 0 {
		initialBackoff = 1 * time.Second
	}
	maxBackoff := opts.MaxBackoff
	if maxBackoff <= 0 {
		maxBackoff = 5 * time.Minute
	}

	retries := 0
	backoff := initialBackoff

	for {
		gotData := false
		wrappedCB := func(line string) {
			if !gotData {
				gotData = true
			}
			callback(line)
		}

		err := c.streamLogsOnce(ctx, name, wrappedCB)

		// Context cancelled — clean exit.
		if ctx.Err() != nil {
			return nil
		}

		// HTTP 404 — server removed, no reconnect.
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			return err
		}

		// Reset retry counter if we received data (connection was healthy).
		if gotData {
			retries = 0
			backoff = initialBackoff
		}

		retries++
		if retries >= maxRetries {
			if err != nil {
				return fmt.Errorf("max retries (%d) exceeded: %w", maxRetries, err)
			}
			return fmt.Errorf("max retries (%d) exceeded", maxRetries)
		}

		// Wait before reconnecting.
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}

		// Exponential backoff.
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// --- internal helpers ---

func (c *Client) get(ctx context.Context, path string, out any) error {
	return c.doRequest(ctx, "GET", path, nil, out)
}

func (c *Client) post(ctx context.Context, path string, body any, out any) error {
	return c.doRequest(ctx, "POST", path, body, out)
}

func (c *Client) doRequest(ctx context.Context, method, path string, body any, out any) error {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if err := c.attachAuthHeader(req); err != nil {
		return err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &ConnectionError{URL: c.baseURL, Err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return c.parseErrorBody(resp)
	}

	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

func (c *Client) parseErrorBody(resp *http.Response) error {
	var errResp struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
		return &APIError{StatusCode: resp.StatusCode, Message: resp.Status}
	}
	msg := errResp.Error
	if msg == "" {
		msg = resp.Status
	}
	return &APIError{StatusCode: resp.StatusCode, Message: msg}
}
