package config

import (
	"os"
	"testing"

	"mcp-gateway/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExpandVar_Simple(t *testing.T) {
	envMap := map[string]string{"FOO": "bar"}
	assert.Equal(t, "bar", ExpandVar("${FOO}", envMap))
}

func TestExpandVar_Multiple(t *testing.T) {
	envMap := map[string]string{"A": "hello", "B": "world"}
	assert.Equal(t, "hello_world", ExpandVar("${A}_${B}", envMap))
}

func TestExpandVar_Missing_ResolvesToEmpty(t *testing.T) {
	envMap := map[string]string{}
	assert.Equal(t, "", ExpandVar("${MISSING}", envMap))
}

func TestExpandVar_AllowlistFallback(t *testing.T) {
	// HOME is in the allowlist — should fall back to os.Getenv.
	envMap := map[string]string{}
	home := os.Getenv("HOME")
	if home == "" {
		// Windows fallback.
		home = os.Getenv("USERPROFILE")
		t.Setenv("HOME", home)
	}
	result := ExpandVar("${HOME}", envMap)
	assert.Equal(t, home, result)
}

func TestExpandVar_NotInAllowlist_ResolvesToEmpty(t *testing.T) {
	// Set an env var that is NOT in the allowlist.
	t.Setenv("AWS_SECRET_ACCESS_KEY", "supersecret")
	envMap := map[string]string{}
	assert.Equal(t, "", ExpandVar("${AWS_SECRET_ACCESS_KEY}", envMap))
}

func TestExpandVar_BareVar_NotExpanded(t *testing.T) {
	envMap := map[string]string{"FOO": "bar"}
	// Bare $FOO should NOT be expanded — only ${FOO} is.
	assert.Equal(t, "$FOO", ExpandVar("$FOO", envMap))
}

func TestExpandVar_DollarWithoutBrace_Preserved(t *testing.T) {
	envMap := map[string]string{}
	assert.Equal(t, "$100", ExpandVar("$100", envMap))
	assert.Equal(t, "$var", ExpandVar("$var", envMap))
	assert.Equal(t, "$$", ExpandVar("$$", envMap))
}

func TestExpandVar_EmptyString(t *testing.T) {
	assert.Equal(t, "", ExpandVar("", map[string]string{}))
}

func TestExpandVar_NoVars(t *testing.T) {
	assert.Equal(t, "plain text", ExpandVar("plain text", map[string]string{}))
}

func TestExpandVar_UnclosedBrace(t *testing.T) {
	envMap := map[string]string{"FOO": "bar"}
	assert.Equal(t, "${FOO", ExpandVar("${FOO", envMap))
}

func TestExpandVar_EmptyVarName(t *testing.T) {
	// ${} has empty var name — regex requires at least one char, so it should not match.
	assert.Equal(t, "${}", ExpandVar("${}", map[string]string{}))
}

func TestExpandVar_NilEnvMap(t *testing.T) {
	// nil envMap is safe — map read on nil returns zero value in Go.
	assert.Equal(t, "", ExpandVar("${FOO}", nil))
	assert.Equal(t, "plain", ExpandVar("plain", nil))
}

func TestExpandVar_EnvMapTakesPrecedence(t *testing.T) {
	// Even if HOME is in the allowlist, envMap value wins.
	envMap := map[string]string{"HOME": "/custom/home"}
	assert.Equal(t, "/custom/home", ExpandVar("${HOME}", envMap))
}

func TestExpandServerConfig_AllFields(t *testing.T) {
	envMap := map[string]string{
		"CMD":     "/usr/bin/node",
		"ARG":     "server.js",
		"DIR":     "/opt/app",
		"MCPURL":  "http://localhost:3000/mcp",
		"RESTURL": "http://localhost:3000",
		"HEALTH":  "/health",
		"ENVVAL":  "secret123",
		"HDR":     "Bearer token",
	}

	sc := &models.ServerConfig{
		Command:        "${CMD}",
		Args:           []string{"${ARG}", "--port", "3000"},
		Cwd:            "${DIR}",
		URL:            "${MCPURL}",
		RestURL:        "${RESTURL}",
		HealthEndpoint: "${HEALTH}",
		Env:            []string{"API_KEY=${ENVVAL}"},
		Headers:        map[string]string{"Authorization": "${HDR}"},
	}

	err := ExpandServerConfig(sc, envMap)
	require.NoError(t, err)

	assert.Equal(t, "/usr/bin/node", sc.Command)
	assert.Equal(t, []string{"server.js", "--port", "3000"}, sc.Args)
	assert.Equal(t, "/opt/app", sc.Cwd)
	assert.Equal(t, "http://localhost:3000/mcp", sc.URL)
	assert.Equal(t, "http://localhost:3000", sc.RestURL)
	assert.Equal(t, "/health", sc.HealthEndpoint)
	assert.Equal(t, []string{"API_KEY=secret123"}, sc.Env)
	assert.Equal(t, "Bearer token", sc.Headers["Authorization"])
}

func TestExpandServerConfig_KeysNotExpanded(t *testing.T) {
	envMap := map[string]string{"KEY": "expanded"}
	sc := &models.ServerConfig{
		Headers: map[string]string{"${KEY}": "value"},
	}

	err := ExpandServerConfig(sc, envMap)
	require.NoError(t, err)

	// Header key should remain as-is (not expanded).
	assert.Equal(t, "value", sc.Headers["${KEY}"])
}

func TestExpandServerConfig_BooleansUntouched(t *testing.T) {
	expose := true
	sc := &models.ServerConfig{
		Disabled:    false,
		ExposeTools: &expose,
	}

	err := ExpandServerConfig(sc, map[string]string{})
	require.NoError(t, err)

	assert.False(t, sc.Disabled)
	assert.True(t, *sc.ExposeTools)
}

func TestExpandServerConfig_NewlineInEnvValue_Error(t *testing.T) {
	envMap := map[string]string{"BAD": "line1\nline2"}
	sc := &models.ServerConfig{
		Env: []string{"KEY=${BAD}"},
	}

	err := ExpandServerConfig(sc, envMap)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "newline")
	assert.Contains(t, err.Error(), "KEY")
}

func TestExpandServerConfig_CarriageReturnInEnvValue_Error(t *testing.T) {
	envMap := map[string]string{"BAD": "line1\rline2"}
	sc := &models.ServerConfig{
		Env: []string{"KEY=${BAD}"},
	}

	err := ExpandServerConfig(sc, envMap)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "newline")
	assert.Contains(t, err.Error(), "KEY")
}

func TestExpandServerConfig_NewlineInHeaderValue_Error(t *testing.T) {
	envMap := map[string]string{"BAD": "Bearer abc\r\nX-Injected: evil"}
	sc := &models.ServerConfig{
		Headers: map[string]string{"Authorization": "${BAD}"},
	}

	err := ExpandServerConfig(sc, envMap)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "newline")
	assert.Contains(t, err.Error(), "Authorization")
}

func TestExpandConfig_MultipleServers(t *testing.T) {
	envMap := map[string]string{
		"PORT":  "3000",
		"PORT2": "4000",
	}

	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"s1": {URL: "http://localhost:${PORT}/mcp"},
			"s2": {URL: "http://localhost:${PORT2}/mcp"},
		},
	}

	err := ExpandConfig(cfg, envMap)
	require.NoError(t, err)
	assert.Equal(t, "http://localhost:3000/mcp", cfg.Servers["s1"].URL)
	assert.Equal(t, "http://localhost:4000/mcp", cfg.Servers["s2"].URL)
}

func TestExpandConfig_ReturnsFirstError(t *testing.T) {
	envMap := map[string]string{"BAD": "a\nb"}

	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"s1": {Env: []string{"K=${BAD}"}},
		},
	}

	err := ExpandConfig(cfg, envMap)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "newline")
	assert.Contains(t, err.Error(), "s1") // server name must appear in wrapped error
}

func TestExpandServerConfig_MalformedEnv_Skipped(t *testing.T) {
	// Malformed env entry (no =) should be skipped by expand (caught by validation later).
	sc := &models.ServerConfig{
		Env: []string{"MALFORMED"},
	}
	err := ExpandServerConfig(sc, map[string]string{})
	require.NoError(t, err)
	assert.Equal(t, []string{"MALFORMED"}, sc.Env)
}

func TestExpandConfig_NilServerEntry(t *testing.T) {
	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"valid":   {Command: "echo"},
			"bad-nil": nil,
		},
	}
	err := ExpandConfig(cfg, map[string]string{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bad-nil")
	assert.Contains(t, err.Error(), "nil config entry")
}

func TestExpandServerConfig_NulInEnvValue_Error(t *testing.T) {
	sc := &models.ServerConfig{
		Env: []string{"KEY=${V}"},
	}
	err := ExpandServerConfig(sc, map[string]string{"V": "val\x00ue"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "illegal characters")
}

func TestExpandServerConfig_NulInHeaderValue_Error(t *testing.T) {
	sc := &models.ServerConfig{
		Headers: map[string]string{"X-Key": "${V}"},
	}
	err := ExpandServerConfig(sc, map[string]string{"V": "val\x00ue"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "illegal characters")
}
