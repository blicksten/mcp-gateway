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

// OrchestratorServerName is the canonical gateway-config name for the
// orchestrator stdio backend. Used by ApplyDefaults to inject REST health
// defaults and by the health monitor to emit a missing-REST warning.
const OrchestratorServerName = "orchestrator"

// OrchestratorRESTURL is the default REST base URL for the orchestrator daemon.
// The orchestrator listens on :8100; /health returns 200 when the daemon is alive.
// These constants are the single source of truth — health monitor and config
// defaults both read from here.
const OrchestratorRESTURL = "http://127.0.0.1:8100"

// OrchestratorHealthEndpoint is the REST health endpoint path on the orchestrator daemon.
const OrchestratorHealthEndpoint = "/health"

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
	// StatusUnreachable indicates a transport-level failure where the
	// backend host cannot be reached at the TCP layer (host down, DNS
	// failure, VPN off, network partition). Distinct from StatusError
	// which is for protocol-level failures (bad MCP handshake, 4xx/5xx
	// HTTP responses, TLS handshake fails). An unreachable backend is
	// slow-polled for reachability (~60s) instead of being aggressively
	// restarted with exponential backoff — operator UX is a stable yellow
	// "host offline" badge rather than a perpetually-spinning loader.
	// Auto-recovers (Start → Running) when the host becomes reachable
	// again. See docs/PLAN-unreachable-handling.md.
	StatusUnreachable ServerStatus = "unreachable"

	// StatusIdle indicates a backend whose tools are known from the
	// manifest cache but that has not yet been spawned. Semantics:
	// "configured, tools cached from last session, not yet running —
	// will spawn on first tool invocation (connect VPN / launch)."
	//
	// TASK C2 — docs/DESIGN-mcp-gateway-lazy-spawn.md §4.2.
	// Guard 3 (PAL/UX): the dashboard should display this as a neutral
	// "idle" badge, NOT red/yellow down state. Idle is not an error.
	// Only set when MCP_GATEWAY_LAZY_SPAWN=1 is active.
	StatusIdle ServerStatus = "idle"

	// StatusIdleReason is the human-readable last_error string for an Idle
	// backend. Surfaced in the dashboard as context so operators understand
	// the backend is intentionally deferred, not failing.
	StatusIdleReason = "idle — SAP tools available from cache; spawns on first use (connect VPN/launch)"
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
	Disabled    bool  `json:"disabled,omitempty"`
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

// SAPEnvURL extracts the SAP_URL value from Config.Env entries (KEY=VALUE format).
// Returns ("", false) when SAP_URL is not present. Uses strings.Cut per
// CLAUDE.md Regex Discipline — no regex used.
func (sc *ServerConfig) SAPEnvURL() (string, bool) {
	return sc.EnvValue("SAP_URL")
}

// EnvValue extracts the value of a KEY=VALUE entry from Config.Env by key.
// Returns ("", false) when the key is absent or its value is empty. Uses
// strings.Cut per CLAUDE.md Regex Discipline — no regex used.
func (sc *ServerConfig) EnvValue(key string) (string, bool) {
	for _, entry := range sc.Env {
		k, val, ok := strings.Cut(entry, "=")
		if ok && k == key && val != "" {
			return val, true
		}
	}
	return "", false
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
	if u.Host == "" {
		return fmt.Errorf("%s: URL %q must include a host", field, raw)
	}
	if u.User != nil {
		return fmt.Errorf("%s: URL %q must not include userinfo", field, raw)
	}
	return nil
}

// IsDangerousEnvKey returns true if the key is in the dangerous env keys blocklist.
// Case-insensitive: Windows env vars are case-insensitive, so "path" == "PATH".
func IsDangerousEnvKey(key string) bool {
	return dangerousEnvKeys[strings.ToUpper(key)]
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
		if key == "" {
			return fmt.Errorf("env entry %q has empty key", e)
		}
		if dangerousEnvKeys[strings.ToUpper(key)] {
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
	Transports      []string    `json:"transports,omitempty"` // e.g. ["stdio","http","sse"]
	HTTPPort        int         `json:"http_port,omitempty"`
	BindAddress     string      `json:"bind_address,omitempty"` // IP to bind; default "127.0.0.1"
	PingInterval    Duration    `json:"ping_interval,omitempty"`
	ToolFilter      *ToolFilter `json:"tool_filter,omitempty"`
	CompressSchemas bool        `json:"compress_schemas,omitempty"` // Truncate tool descriptions, strip schema examples
	AllowRemote     bool        `json:"allow_remote,omitempty"`     // Allow non-loopback bind_address (requires auth unless --no-auth + escape hatch)
	// AuthMCPTransport controls how MCP transports (/mcp, /sse) authenticate.
	// See ADR-0003 §policy-matrix-mcp-modes.
	//   "" | "loopback-only" — refuse non-loopback clients with 403 (default, safe).
	//   "bearer-required"     — apply BearerAuthMiddleware; requires AllowRemote=true.
	AuthMCPTransport string `json:"auth_mcp_transport,omitempty"`
	// TLSCertPath is the filesystem path to a PEM-encoded TLS
	// certificate chain. When set alongside TLSKeyPath, the HTTP
	// server listens with TLS. Phase 13.B (F-7).
	TLSCertPath string `json:"tls_cert_path,omitempty"`
	// TLSKeyPath is the filesystem path to the PEM-encoded TLS
	// private key matching TLSCertPath.
	TLSKeyPath string `json:"tls_key_path,omitempty"`
}

// MCP transport policy mode constants (GatewaySettings.AuthMCPTransport).
const (
	AuthMCPTransportLoopbackOnly   = "loopback-only"
	AuthMCPTransportBearerRequired = "bearer-required"
)

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
	Gateway GatewaySettings          `json:"gateway"`
	Servers map[string]*ServerConfig `json:"servers"`
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
		// "http" enables the HTTP transport family (/mcp + /sse together);
		// see server.go Handler(). A bare ["http"] default keeps both
		// surfaces mounted, matching the historical unconditional behavior.
		c.Gateway.Transports = []string{"http"}
	}
	if c.Gateway.PingInterval == 0 {
		c.Gateway.PingInterval = Duration(30 * time.Second)
	}
	if c.Servers == nil {
		c.Servers = make(map[string]*ServerConfig)
	}

	// W-5b: inject orchestrator REST health defaults so Level-2 monitoring
	// fires automatically even when the operator omits rest_url/health_endpoint
	// from config.json. Only applies when:
	//   1. A server named "orchestrator" exists in the config.
	//   2. Its RestURL is currently unset (explicit config always wins).
	// This keeps the change non-breaking for operators who have already
	// configured a non-default URL, and harmless for machines that don't
	// run the orchestrator (no such server in config → no change).
	if sc, ok := c.Servers[OrchestratorServerName]; ok && sc != nil && sc.RestURL == "" {
		sc.RestURL = OrchestratorRESTURL
		sc.HealthEndpoint = OrchestratorHealthEndpoint
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
		if sc == nil {
			return fmt.Errorf("server %q: nil config entry", name)
		}
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
	NewName       *string           `json:"new_name,omitempty"`
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
	Timestamp     time.Time           `json:"timestamp"`
	GatewayUptime Duration            `json:"gateway_uptime"`
	Servers       []ServerMetricsInfo `json:"servers"`
	Tokens        TokenMetrics        `json:"tokens"`
	// LazySpawn carries the C2 lazy-spawn observability counters (TASK T1).
	// It is populated only when the MCP_GATEWAY_LAZY_SPAWN flag is enabled;
	// nil (and omitted from JSON) when the feature is OFF, so the default-config
	// metrics payload stays byte-identical to pre-T1.
	LazySpawn *LazySpawnMetrics `json:"lazy_spawn,omitempty"`
}

// LazySpawnMetrics holds monotonic observability counters for the C2 lazy-spawn
// feature (TASK T1). Counters are cumulative since gateway start. They let an
// operator watch a canary and see warming/degrade/mismatch/spawn rates.
type LazySpawnMetrics struct {
	// SpawnOnInvoke counts successful on-demand spawns triggered by a tool
	// invocation against a StatusIdle backend.
	SpawnOnInvoke int64 `json:"spawn_on_invoke"`
	// WarmingReturned counts ErrLazyWarming responses returned to callers whose
	// budget expired while a background spawn was still in progress.
	// Note: a caller whose context cancels at the same instant the spawn result
	// lands may receive ErrLazyWarming even though that spawn ultimately
	// succeeded (the select resolves ctx.Done over the result channel). The
	// counters are independent: SpawnOnInvoke + WarmingReturned does not equal a
	// total attempt count.
	WarmingReturned int64 `json:"warming_returned"`
	// DegradeEvicted counts Guard-2 events: a lazy spawn failed, the backend was
	// marked StatusError, and its manifest entry was evicted (tool un-advertised).
	DegradeEvicted int64 `json:"degrade_evicted"`
	// SigMismatchRediscover counts boot-seed events where a manifest entry's
	// stored signature did not match the current config, forcing an eager
	// re-discovery instead of seeding StatusIdle.
	SigMismatchRediscover int64 `json:"sig_mismatch_rediscover"`
}

// ServerMetricsInfo holds operational metrics for a single backend server.
type ServerMetricsInfo struct {
	Name         string     `json:"name"`
	RestartCount int        `json:"restart_count"`
	MTBF         Duration   `json:"mtbf"` // 0 when no failures recorded
	Uptime       Duration   `json:"uptime"`
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
