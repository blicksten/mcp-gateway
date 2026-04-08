// Package models defines the core data types for the MCP Gateway.
package models

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// MaxServers is the maximum number of backend servers allowed.
const MaxServers = 100

// dangerousEnvKeys are environment variable keys that must not be set via config.
// Covers dynamic linker injection, interpreter hijack, and shell manipulation vectors.
var dangerousEnvKeys = map[string]bool{
	// Dynamic linker injection (Linux).
	"LD_PRELOAD":      true,
	"LD_LIBRARY_PATH": true,
	"LD_AUDIT":        true,
	"LD_DEBUG":        true,
	"LD_PROFILE":      true,
	// Dynamic linker injection (macOS).
	"DYLD_INSERT_LIBRARIES":      true,
	"DYLD_LIBRARY_PATH":          true,
	"DYLD_FALLBACK_LIBRARY_PATH": true,
	"DYLD_FRAMEWORK_PATH":        true,
	// Python interpreter hijack.
	"PYTHONPATH":     true,
	"PYTHONSTARTUP":  true,
	"PYTHONUSERSITE": true,
	// Node.js hijack.
	"NODE_OPTIONS": true,
	"NODE_PATH":    true,
	// Java hijack.
	"JAVA_TOOL_OPTIONS": true,
	"_JAVA_OPTIONS":     true,
	"JAVA_OPTIONS":      true,
	// Ruby hijack.
	"RUBYOPT": true,
	"RUBYLIB": true,
	// Perl hijack.
	"PERL5LIB": true,
	"PERL5OPT": true,
	// Shell / PATH — prevent command lookup hijack.
	"PATH":  true,
	"SHELL": true,
	"IFS":   true,
}

// ToolNameSeparator is used to namespace tools: "server__tool".
const ToolNameSeparator = "__"

// serverNameRe matches valid server names: alphanumeric start, up to 64 chars.
var serverNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// ValidateServerName checks that the name is a valid server identifier.
func ValidateServerName(name string) error {
	if name == "" {
		return fmt.Errorf("server name must not be empty")
	}
	if !serverNameRe.MatchString(name) {
		return fmt.Errorf("server name %q is invalid (must be 1-64 alphanumeric/hyphen/underscore characters)", name)
	}
	if strings.Contains(name, ToolNameSeparator) {
		return fmt.Errorf("server name %q must not contain %q", name, ToolNameSeparator)
	}
	return nil
}

// ServerStatus represents the lifecycle state of a backend MCP server.
type ServerStatus string

const (
	StatusStopped    ServerStatus = "stopped"
	StatusStarting   ServerStatus = "starting"
	StatusRunning    ServerStatus = "running"
	StatusDegraded   ServerStatus = "degraded"
	StatusError      ServerStatus = "error"
	StatusRestarting ServerStatus = "restarting"
	StatusDisabled   ServerStatus = "disabled"
)

// ServerConfig defines how to connect to a backend MCP server.
type ServerConfig struct {
	// stdio backend: command + args to spawn child process.
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	Cwd     string   `json:"cwd,omitempty"`
	Env     []string `json:"env,omitempty"` // SECURITY: must NOT be exposed in API responses — use ServerEntryView

	// HTTP/SSE backend: URL to connect to.
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"` // SECURITY: must NOT be exposed in API responses — may contain auth tokens

	// REST endpoints for health checks and API proxying.
	RestURL        string `json:"rest_url,omitempty"`
	HealthEndpoint string `json:"health_endpoint,omitempty"`

	// Behavior flags.
	Disabled    bool `json:"disabled,omitempty"`
	ExposeTools *bool `json:"expose_tools,omitempty"` // default true when nil
}

// ExposeToolsEnabled returns whether this server's tools should be
// exposed to MCP clients. Defaults to true when ExposeTools is nil.
func (sc *ServerConfig) ExposeToolsEnabled() bool {
	if sc.ExposeTools == nil {
		return true
	}
	return *sc.ExposeTools
}

// TransportType returns the detected backend transport type.
func (sc *ServerConfig) TransportType() string {
	if sc.Command != "" {
		return "stdio"
	}
	if sc.URL != "" {
		if strings.HasSuffix(sc.URL, "/sse") {
			return "sse"
		}
		return "http"
	}
	return ""
}

// Validate checks that the server config has the minimum required fields.
func (sc *ServerConfig) Validate() error {
	hasCommand := sc.Command != ""
	hasURL := sc.URL != ""

	if !hasCommand && !hasURL {
		// Allow rest-only servers (health check / proxy targets).
		if sc.RestURL != "" {
			return validateHTTPURL(sc.RestURL, "rest_url")
		}
		return fmt.Errorf("server config must have command, url, or rest_url")
	}
	if hasCommand && hasURL {
		return fmt.Errorf("server config cannot have both command and url")
	}
	if hasCommand {
		if !filepath.IsAbs(sc.Command) {
			return fmt.Errorf("command %q must be an absolute path", sc.Command)
		}
	}
	if hasURL {
		if err := validateHTTPURL(sc.URL, "url"); err != nil {
			return err
		}
	}
	if sc.RestURL != "" {
		if err := validateHTTPURL(sc.RestURL, "rest_url"); err != nil {
			return err
		}
	}
	if err := validateEnv(sc.Env); err != nil {
		return err
	}
	if err := validateHeaders(sc.Headers); err != nil {
		return err
	}
	return nil
}

// validateHTTPURL checks that a URL uses http or https scheme.
func validateHTTPURL(raw, field string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s: invalid URL %q: %w", field, raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%s: URL %q must use http or https scheme", field, raw)
	}
	return nil
}

// IsDangerousEnvKey returns true if the key is in the dangerous env keys blocklist.
func IsDangerousEnvKey(key string) bool {
	return dangerousEnvKeys[key]
}

// ValidateEnvEntries checks env entries are KEY=VALUE format and rejects dangerous keys.
// Exported for use by PATCH handler (delta validation before merge).
func ValidateEnvEntries(env []string) error {
	return validateEnv(env)
}

// ValidateHeaderEntries checks that header names are valid and not dangerous.
// Exported for use by PATCH handler (delta validation before merge).
func ValidateHeaderEntries(headers map[string]string) error {
	return validateHeaders(headers)
}

// validateEnv checks env entries are KEY=VALUE format and rejects dangerous keys.
func validateEnv(env []string) error {
	for _, e := range env {
		key, val, ok := strings.Cut(e, "=")
		if !ok {
			return fmt.Errorf("env entry %q must be in KEY=VALUE format", e)
		}
		if dangerousEnvKeys[key] {
			return fmt.Errorf("env key %q is not permitted (security risk)", key)
		}
		if strings.ContainsAny(val, "\r\n\x00") {
			return fmt.Errorf("env key %q value contains invalid characters (CR/LF/NUL)", key)
		}
	}
	return nil
}

// dangerousHeaders are HTTP header names that must not be set via config
// because they can break the transport protocol.
var dangerousHeaders = map[string]bool{
	"host":              true,
	"content-length":    true,
	"transfer-encoding": true,
	"connection":        true,
	"upgrade":           true,
	"te":                true,
	"trailer":           true,
}

// headerNameRe matches valid HTTP header names (RFC 7230 token).
var headerNameRe = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+\-.^_` + "`" + `|~]+$`)

// validateHeaders checks that header names are valid and not dangerous.
func validateHeaders(headers map[string]string) error {
	for k, v := range headers {
		if k == "" {
			return fmt.Errorf("header name must not be empty")
		}
		if !headerNameRe.MatchString(k) {
			return fmt.Errorf("header name %q contains invalid characters", k)
		}
		if dangerousHeaders[strings.ToLower(k)] {
			return fmt.Errorf("header %q is not permitted (would break transport)", k)
		}
		if strings.ContainsAny(v, "\r\n\x00") {
			return fmt.Errorf("header %q value contains invalid characters (CR/LF/NUL)", k)
		}
	}
	return nil
}

// ToolFilter controls which tools are exposed to MCP clients.
type ToolFilter struct {
	Mode              string   `json:"mode,omitempty"` // "allowlist" or "blocklist"
	IncludeServers    []string `json:"include_servers,omitempty"`
	ExcludeServers    []string `json:"exclude_servers,omitempty"`
	ToolBudget        int      `json:"tool_budget,omitempty"`
	PerServerBudget   int      `json:"per_server_budget,omitempty"`
	ConsolidateExcess bool     `json:"consolidate_excess,omitempty"` // When true + PerServerBudget>0, excess tools become a __more_tools meta-tool
}

// Validate checks that the ToolFilter configuration is valid.
func (tf *ToolFilter) Validate() error {
	switch tf.Mode {
	case "", "allowlist", "blocklist":
	default:
		return fmt.Errorf("invalid tool_filter mode %q (must be allowlist or blocklist)", tf.Mode)
	}
	if tf.ConsolidateExcess && tf.PerServerBudget <= 0 {
		return fmt.Errorf("consolidate_excess requires per_server_budget > 0")
	}
	return nil
}

// GatewaySettings controls global gateway behavior.
type GatewaySettings struct {
	Transports      []string    `json:"transports,omitempty"`       // e.g. ["stdio","http","sse"]
	HTTPPort        int         `json:"http_port,omitempty"`
	BindAddress     string      `json:"bind_address,omitempty"`     // IP to bind; default "127.0.0.1"
	PingInterval    Duration    `json:"ping_interval,omitempty"`
	ToolFilter      *ToolFilter `json:"tool_filter,omitempty"`
	CompressSchemas bool        `json:"compress_schemas,omitempty"` // Truncate tool descriptions, strip schema examples
	AllowRemote     bool        `json:"allow_remote,omitempty"`     // Allow non-loopback bind_address (no auth — DANGEROUS)
}

// ValidateBindAddress checks that BindAddress is a valid IP and returns
// true if the address is non-loopback (a security warning should be logged).
func ValidateBindAddress(addr string) (nonLoopback bool, err error) {
	ip := net.ParseIP(addr)
	if ip == nil {
		return false, fmt.Errorf("bind_address %q is not a valid IP address", addr)
	}
	return !ip.IsLoopback(), nil
}

// Config is the top-level gateway configuration.
type Config struct {
	Gateway         GatewaySettings          `json:"gateway"`
	Servers         map[string]*ServerConfig  `json:"servers"`
}

// ApplyDefaults fills in zero-value fields with sensible defaults.
func (c *Config) ApplyDefaults() {
	if c.Gateway.HTTPPort == 0 {
		c.Gateway.HTTPPort = 8765
	}
	if c.Gateway.BindAddress == "" {
		c.Gateway.BindAddress = "127.0.0.1"
	}
	if len(c.Gateway.Transports) == 0 {
		c.Gateway.Transports = []string{"http"}
	}
	if c.Gateway.PingInterval == 0 {
		c.Gateway.PingInterval = Duration(30 * time.Second)
	}
	if c.Servers == nil {
		c.Servers = make(map[string]*ServerConfig)
	}
}

// Validate checks all server configs and gateway settings.
func (c *Config) Validate() error {
	if len(c.Servers) > MaxServers {
		return fmt.Errorf("too many servers: %d (max %d)", len(c.Servers), MaxServers)
	}
	if c.Gateway.BindAddress != "" {
		if _, err := ValidateBindAddress(c.Gateway.BindAddress); err != nil {
			return err
		}
	}
	if c.Gateway.ToolFilter != nil {
		if err := c.Gateway.ToolFilter.Validate(); err != nil {
			return err
		}
	}
	for name, sc := range c.Servers {
		if err := sc.Validate(); err != nil {
			return fmt.Errorf("server %q: %w", name, err)
		}
		if strings.Contains(name, ToolNameSeparator) {
			return fmt.Errorf("server name %q must not contain %q", name, ToolNameSeparator)
		}
	}
	return nil
}

// ServerPatch defines the fields that can be updated via PATCH /api/v1/servers/{name}.
type ServerPatch struct {
	Disabled      *bool             `json:"disabled,omitempty"`
	AddEnv        []string          `json:"add_env,omitempty"`
	RemoveEnv     []string          `json:"remove_env,omitempty"`
	AddHeaders    map[string]string `json:"add_headers,omitempty"`
	RemoveHeaders []string          `json:"remove_headers,omitempty"`
}

// ToolInfo describes a single tool from a backend.
type ToolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Server      string `json:"server"`
	InputSchema any    `json:"input_schema,omitempty"` // JSON Schema from backend
}

// ServerEntry holds runtime state for a managed backend.
type ServerEntry struct {
	Name         string       `json:"name"`
	Config       ServerConfig `json:"config"`
	Status       ServerStatus `json:"status"`
	PID          int          `json:"pid,omitempty"`
	Tools        []ToolInfo   `json:"tools,omitempty"`
	RestartCount int          `json:"restart_count"`
	LastPing     time.Time    `json:"last_ping,omitzero"`
	LastError    string       `json:"last_error,omitempty"`
	StartedAt    time.Time    `json:"started_at,omitzero"`
}

// MetricsResponse is the JSON payload for GET /api/v1/metrics.
type MetricsResponse struct {
	Timestamp     time.Time          `json:"timestamp"`
	GatewayUptime Duration           `json:"gateway_uptime"`
	Servers       []ServerMetricsInfo `json:"servers"`
	Tokens        TokenMetrics       `json:"tokens"`
}

// ServerMetricsInfo holds operational metrics for a single backend server.
type ServerMetricsInfo struct {
	Name         string   `json:"name"`
	RestartCount int      `json:"restart_count"`
	MTBF         Duration `json:"mtbf"` // 0 when no failures recorded
	Uptime       Duration `json:"uptime"`
	LastCrashAt  *time.Time `json:"last_crash_at,omitempty"` // nil if never crashed
}

// TokenMetrics holds token cost estimates for the gateway's tool set.
type TokenMetrics struct {
	TotalTools      int `json:"total_tools"`
	EstSchemaTokens int `json:"est_schema_tokens"`
	EstDescTokens   int `json:"est_description_tokens"`
	EstTotalTokens  int `json:"est_total_tokens"`
}

// Duration wraps time.Duration for JSON (un)marshalling as a string.
type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) {
	return []byte(`"` + time.Duration(d).String() + `"`), nil
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		return nil
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}
