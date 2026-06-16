package models

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func absCmd(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs("mock-server")
	require.NoError(t, err)
	return p
}

func TestServerConfig_Validate_Stdio(t *testing.T) {
	sc := &ServerConfig{Command: absCmd(t), Args: []string{"run"}}
	assert.NoError(t, sc.Validate())
}

func TestServerConfig_Validate_HTTP(t *testing.T) {
	sc := &ServerConfig{URL: "http://localhost:3000/mcp"}
	assert.NoError(t, sc.Validate())
}

func TestServerConfig_Validate_RestOnly(t *testing.T) {
	sc := &ServerConfig{RestURL: "http://jenkins.local:8080"}
	assert.NoError(t, sc.Validate())
}

func TestServerConfig_Validate_Empty(t *testing.T) {
	sc := &ServerConfig{}
	err := sc.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must have command, url, or rest_url")
}

func TestServerConfig_Validate_BothCommandAndURL(t *testing.T) {
	sc := &ServerConfig{Command: absCmd(t), URL: "http://localhost:3000"}
	err := sc.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot have both")
}

func TestServerConfig_TransportType(t *testing.T) {
	tests := []struct {
		name     string
		config   ServerConfig
		expected string
	}{
		{"stdio", ServerConfig{Command: absCmd(t)}, "stdio"},
		{"http", ServerConfig{URL: "http://localhost:3000/mcp"}, "http"},
		{"sse", ServerConfig{URL: "http://localhost:8091/sse"}, "sse"},
		{"rest-only", ServerConfig{RestURL: "http://jenkins:8080"}, ""},
		{"empty", ServerConfig{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.config.TransportType())
		})
	}
}

func TestServerConfig_ExposeToolsEnabled(t *testing.T) {
	// nil → default true
	sc := &ServerConfig{Command: absCmd(t)}
	assert.True(t, sc.ExposeToolsEnabled())

	// explicit true
	v := true
	sc.ExposeTools = &v
	assert.True(t, sc.ExposeToolsEnabled())

	// explicit false
	v = false
	sc.ExposeTools = &v
	assert.False(t, sc.ExposeToolsEnabled())
}

func TestServerConfig_Validate_RelativePath(t *testing.T) {
	sc := &ServerConfig{Command: "uv"}
	err := sc.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be an absolute path")
}

func TestServerConfig_Validate_DangerousEnv(t *testing.T) {
	sc := &ServerConfig{Command: absCmd(t), Env: []string{"LD_PRELOAD=/tmp/evil.so"}}
	err := sc.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not permitted")
}

func TestServerConfig_Validate_BadEnvFormat(t *testing.T) {
	sc := &ServerConfig{Command: absCmd(t), Env: []string{"NOEQUALS"}}
	err := sc.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "KEY=VALUE format")
}

func TestServerConfig_Validate_ValidEnv(t *testing.T) {
	sc := &ServerConfig{Command: absCmd(t), Env: []string{"FOO=bar", "API_KEY=secret"}}
	assert.NoError(t, sc.Validate())
}

func TestServerConfig_Validate_DangerousHeader(t *testing.T) {
	sc := &ServerConfig{URL: "http://localhost:3000/mcp", Headers: map[string]string{"Host": "evil.com"}}
	err := sc.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not permitted")
}

func TestServerConfig_Validate_EmptyHeaderName(t *testing.T) {
	sc := &ServerConfig{URL: "http://localhost:3000/mcp", Headers: map[string]string{"": "value"}}
	err := sc.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not be empty")
}

func TestServerConfig_Validate_InvalidHeaderChars(t *testing.T) {
	sc := &ServerConfig{URL: "http://localhost:3000/mcp", Headers: map[string]string{"Bad Header": "value"}}
	err := sc.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid characters")
}

func TestServerConfig_Validate_ValidHeaders(t *testing.T) {
	sc := &ServerConfig{
		URL: "http://localhost:3000/mcp",
		Headers: map[string]string{
			"Authorization": "Bearer token123",
			"X-API-Key":     "abc",
		},
	}
	assert.NoError(t, sc.Validate())
}

func TestConfig_Validate_TooManyServers(t *testing.T) {
	cfg := &Config{Servers: make(map[string]*ServerConfig)}
	for i := range MaxServers + 1 {
		cfg.Servers[fmt.Sprintf("s%d", i)] = &ServerConfig{Command: absCmd(t)}
	}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too many servers")
}

func TestConfig_Validate_SeparatorInName(t *testing.T) {
	cfg := &Config{
		Servers: map[string]*ServerConfig{
			"bad__name": {Command: absCmd(t)},
		},
	}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not contain")
}

func TestConfig_Validate_InvalidServer(t *testing.T) {
	cfg := &Config{
		Servers: map[string]*ServerConfig{
			"broken": {},
		},
	}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `server "broken"`)
}

func TestConfig_ApplyDefaults(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyDefaults()

	assert.Equal(t, 8765, cfg.Gateway.HTTPPort)
	assert.Equal(t, []string{"http"}, cfg.Gateway.Transports)
	assert.Equal(t, Duration(30*time.Second), cfg.Gateway.PingInterval)
	assert.NotNil(t, cfg.Servers)
}

func TestConfig_ApplyDefaults_NoOverride(t *testing.T) {
	cfg := &Config{
		Gateway: GatewaySettings{
			HTTPPort:     9999,
			Transports:   []string{"stdio", "http"},
			PingInterval: Duration(10 * time.Second),
		},
		Servers: map[string]*ServerConfig{
			"x": {Command: "x"},
		},
	}
	cfg.ApplyDefaults()

	assert.Equal(t, 9999, cfg.Gateway.HTTPPort)
	assert.Equal(t, []string{"stdio", "http"}, cfg.Gateway.Transports)
	assert.Equal(t, Duration(10*time.Second), cfg.Gateway.PingInterval)
	assert.Len(t, cfg.Servers, 1)
}

func TestDuration_JSON_RoundTrip(t *testing.T) {
	tests := []struct {
		dur      Duration
		expected string
	}{
		{Duration(30 * time.Second), `"30s"`},
		{Duration(5 * time.Minute), `"5m0s"`},
		{Duration(100 * time.Millisecond), `"100ms"`},
	}
	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			data, err := json.Marshal(tt.dur)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, string(data))

			var parsed Duration
			require.NoError(t, json.Unmarshal(data, &parsed))
			assert.Equal(t, tt.dur, parsed)
		})
	}
}

func TestDuration_JSON_EmptyAndNull(t *testing.T) {
	var d Duration
	assert.NoError(t, json.Unmarshal([]byte(`""`), &d))
	assert.Equal(t, Duration(0), d)

	assert.NoError(t, json.Unmarshal([]byte(`"null"`), &d))
	assert.Equal(t, Duration(0), d)
}

func TestDuration_JSON_Invalid(t *testing.T) {
	var d Duration
	err := json.Unmarshal([]byte(`"not-a-duration"`), &d)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid duration")
}

func TestToolFilter_Validate(t *testing.T) {
	tests := []struct {
		mode    string
		wantErr bool
	}{
		{"", false},
		{"allowlist", false},
		{"blocklist", false},
		{"whitelist", true},
		{"invalid", true},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			tf := &ToolFilter{Mode: tt.mode}
			if tt.wantErr {
				assert.Error(t, tf.Validate())
			} else {
				assert.NoError(t, tf.Validate())
			}
		})
	}
}

func TestToolFilter_Validate_ConsolidateExcessRequiresBudget(t *testing.T) {
	tf := &ToolFilter{ConsolidateExcess: true, PerServerBudget: 0}
	assert.Error(t, tf.Validate())
	assert.Contains(t, tf.Validate().Error(), "consolidate_excess requires per_server_budget")

	tf2 := &ToolFilter{ConsolidateExcess: true, PerServerBudget: 5}
	assert.NoError(t, tf2.Validate())

	tf3 := &ToolFilter{ConsolidateExcess: false, PerServerBudget: 0}
	assert.NoError(t, tf3.Validate())
}

func TestConfig_Validate_InvalidToolFilterMode(t *testing.T) {
	cfg := &Config{
		Gateway: GatewaySettings{
			ToolFilter: &ToolFilter{Mode: "whitelist"},
		},
		Servers: map[string]*ServerConfig{},
	}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid tool_filter mode")
}

func TestValidateBindAddress(t *testing.T) {
	tests := []struct {
		addr        string
		wantErr     bool
		nonLoopback bool
	}{
		{"127.0.0.1", false, false},
		{"::1", false, false},
		{"0.0.0.0", false, true},
		{"192.168.1.100", false, true},
		{"::", false, true},
		{"not-an-ip", true, false},
		{"", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			nonLoopback, err := ValidateBindAddress(tt.addr)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.nonLoopback, nonLoopback)
			}
		})
	}
}

func TestConfig_ApplyDefaults_BindAddress(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyDefaults()
	assert.Equal(t, "127.0.0.1", cfg.Gateway.BindAddress)

	// Custom value not overridden.
	cfg2 := &Config{Gateway: GatewaySettings{BindAddress: "0.0.0.0"}}
	cfg2.ApplyDefaults()
	assert.Equal(t, "0.0.0.0", cfg2.Gateway.BindAddress)
}

func TestConfig_Validate_InvalidBindAddress(t *testing.T) {
	cfg := &Config{
		Gateway: GatewaySettings{BindAddress: "not-an-ip"},
		Servers: map[string]*ServerConfig{},
	}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a valid IP address")
}

func TestServerStatus_Values(t *testing.T) {
	statuses := []ServerStatus{
		StatusStopped, StatusStarting, StatusRunning, StatusDegraded,
		StatusError, StatusRestarting, StatusDisabled,
	}
	assert.Len(t, statuses, 7, "expected 7 server states")

	// All unique
	seen := make(map[ServerStatus]bool)
	for _, s := range statuses {
		assert.False(t, seen[s], "duplicate status: %s", s)
		seen[s] = true
	}
}
