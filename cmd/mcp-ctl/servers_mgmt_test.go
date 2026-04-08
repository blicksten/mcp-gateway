package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mcp-gateway/internal/ctlclient"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// executeRemoveCommand builds a fresh command tree with custom remove options
// and runs with the given args.
func executeRemoveCommand(ts *httptest.Server, opts removeOptions, args ...string) (string, error) {
	buf := new(bytes.Buffer)
	root := newRootCmd()

	// Replace the servers command's remove subcommand with our custom opts.
	for _, cmd := range root.Commands() {
		if cmd.Use == "servers" {
			// Remove existing remove command and add one with custom opts.
			for _, sub := range cmd.Commands() {
				if sub.Use == "remove <name>" {
					cmd.RemoveCommand(sub)
					break
				}
			}
			cmd.AddCommand(newServersRemoveCmdWithOpts(opts))
			break
		}
	}

	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs(append([]string{"--api-url", ts.URL}, args...))
	_, err := root.ExecuteC()
	return buf.String(), err
}

// --- servers add ---

func TestServersAddCommand_Stdio(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/api/v1/servers", r.URL.Path)

		var body struct {
			Name   string         `json:"name"`
			Config map[string]any `json:"config"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "my-srv", body.Name)
		assert.Equal(t, "/usr/bin/srv", body.Config["command"])

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"status": "added"})
	}))
	defer ts.Close()

	out, err := executeCommand(ts, "servers", "add", "my-srv", "--command", "/usr/bin/srv")
	require.NoError(t, err)
	assert.Contains(t, out, "added")
}

func TestServersAddCommand_WithArgsEnvCwd(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name   string              `json:"name"`
			Config ctlclient.ServerConfig `json:"config"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "my-srv", body.Name)
		assert.Equal(t, "/usr/bin/srv", body.Config.Command)
		assert.Equal(t, "/tmp", body.Config.Cwd)
		assert.Equal(t, []string{"a", "b"}, body.Config.Args)
		assert.Equal(t, []string{"FOO=bar"}, body.Config.Env)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"status": "added"})
	}))
	defer ts.Close()

	out, err := executeCommand(ts, "servers", "add", "my-srv",
		"--command", "/usr/bin/srv",
		"--args", "a,b",
		"--cwd", "/tmp",
		"--env", "FOO=bar",
	)
	require.NoError(t, err)
	assert.Contains(t, out, "added")
}

func TestServersAddCommand_URL(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name   string         `json:"name"`
			Config map[string]any `json:"config"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "http://localhost:9000/sse", body.Config["url"])

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"status": "added"})
	}))
	defer ts.Close()

	out, err := executeCommand(ts, "servers", "add", "remote", "--url", "http://localhost:9000/sse")
	require.NoError(t, err)
	assert.Contains(t, out, "added")
}

func TestServersAddCommand_MissingTransport(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer ts.Close()

	_, err := executeCommand(ts, "servers", "add", "fail-srv")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "either --command or --url is required")
}

func TestServersAddCommand_BothTransports(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer ts.Close()

	_, err := executeCommand(ts, "servers", "add", "fail-srv", "--command", "/bin/x", "--url", "http://x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot both be specified")
}

func TestServersAddCommand_InvalidName(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer ts.Close()

	_, err := executeCommand(ts, "servers", "add", "bad/name", "--command", "/bin/x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid")
}

func TestServersAddCommand_NameWithDoubleUnderscore(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer ts.Close()

	_, err := executeCommand(ts, "servers", "add", "bad__name", "--command", "/bin/x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "__")
}

func TestServersAddCommand_WhitespaceName(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer ts.Close()

	_, err := executeCommand(ts, "servers", "add", "  ", "--command", "/bin/x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")
}

func TestServersAddCommand_BadEnvFormat(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer ts.Close()

	_, err := executeCommand(ts, "servers", "add", "srv", "--command", "/bin/x", "--env", "NOEQUALS")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "KEY=VALUE")
}

func TestServersAddCommand_EmptyEnvKey(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer ts.Close()

	_, err := executeCommand(ts, "servers", "add", "srv", "--command", "/bin/x", "--env", "=value")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "KEY=VALUE")
}

// --- servers remove ---

func TestServersRemoveCommand_Force(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "DELETE", r.Method)
		assert.Equal(t, "/api/v1/servers/old-srv", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "removed"})
	}))
	defer ts.Close()

	out, err := executeCommand(ts, "servers", "remove", "old-srv", "--force")
	require.NoError(t, err)
	assert.Contains(t, out, "removed")
}

func TestServersRemoveCommand_NonTTYNoForce(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer ts.Close()

	// Default executeCommand does not have a TTY — should require --force.
	_, err := executeCommand(ts, "servers", "remove", "old-srv")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "use --force")
}

func TestServersRemoveCommand_ConfirmYes(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "DELETE", r.Method)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "removed"})
	}))
	defer ts.Close()

	opts := removeOptions{
		isTTY:       func() bool { return true },
		stdinReader: strings.NewReader("y\n"),
	}
	out, err := executeRemoveCommand(ts, opts, "servers", "remove", "srv")
	require.NoError(t, err)
	assert.Contains(t, out, "Remove server")
	assert.Contains(t, out, "removed")
}

func TestServersRemoveCommand_ConfirmNo(t *testing.T) {
	serverCalled := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		serverCalled = true
	}))
	defer ts.Close()

	opts := removeOptions{
		isTTY:       func() bool { return true },
		stdinReader: strings.NewReader("n\n"),
	}
	out, err := executeRemoveCommand(ts, opts, "servers", "remove", "srv")
	require.NoError(t, err)
	assert.Contains(t, out, "Aborted")
	assert.False(t, serverCalled, "server should not be called when user declines")
}

func TestServersRemoveCommand_ConfirmEOF(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer ts.Close()

	opts := removeOptions{
		isTTY:       func() bool { return true },
		stdinReader: strings.NewReader(""), // EOF — no input
	}
	_, err := executeRemoveCommand(ts, opts, "servers", "remove", "srv")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "aborted")
}

func TestServersRemoveCommand_NotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "server not found"})
	}))
	defer ts.Close()

	_, err := executeCommand(ts, "servers", "remove", "missing", "--force")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "server not found")
}

// --- servers enable ---

func TestServersEnableCommand(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "PATCH", r.Method)
		assert.Equal(t, "/api/v1/servers/srv", r.URL.Path)
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, false, body["disabled"])
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "updated"})
	}))
	defer ts.Close()

	out, err := executeCommand(ts, "servers", "enable", "srv")
	require.NoError(t, err)
	assert.Contains(t, out, "enabled")
}

// --- servers disable ---

func TestServersDisableCommand(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "PATCH", r.Method)
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, true, body["disabled"])
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "updated"})
	}))
	defer ts.Close()

	out, err := executeCommand(ts, "servers", "disable", "srv")
	require.NoError(t, err)
	assert.Contains(t, out, "disabled")
}

// --- servers restart ---

func TestServersRestartCommand(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/api/v1/servers/srv/restart", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "restarted"})
	}))
	defer ts.Close()

	out, err := executeCommand(ts, "servers", "restart", "srv")
	require.NoError(t, err)
	assert.Contains(t, out, "restarted")
}

// --- servers reset-circuit ---

func TestServersResetCircuitCommand(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/api/v1/servers/srv/reset-circuit", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "circuit reset"})
	}))
	defer ts.Close()

	out, err := executeCommand(ts, "servers", "reset-circuit", "srv")
	require.NoError(t, err)
	assert.Contains(t, out, "reset")
}

// --- missing arg tests ---

func TestServersRemoveCommand_MissingArg(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer ts.Close()
	_, err := executeCommand(ts, "servers", "remove")
	require.Error(t, err)
}

func TestServersEnableCommand_MissingArg(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer ts.Close()
	_, err := executeCommand(ts, "servers", "enable")
	require.Error(t, err)
}

func TestServersDisableCommand_MissingArg(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer ts.Close()
	_, err := executeCommand(ts, "servers", "disable")
	require.Error(t, err)
}

func TestServersRestartCommand_MissingArg(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer ts.Close()
	_, err := executeCommand(ts, "servers", "restart")
	require.Error(t, err)
}

func TestServersResetCircuitCommand_MissingArg(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer ts.Close()
	_, err := executeCommand(ts, "servers", "reset-circuit")
	require.Error(t, err)
}

// --- servers add: --headers flag ---

func TestServersAddCommand_WithHeaders(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name   string                 `json:"name"`
			Config ctlclient.ServerConfig `json:"config"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "my-srv", body.Name)
		assert.Equal(t, "http://example.com/sse", body.Config.URL)
		assert.Equal(t, map[string]string{
			"Authorization": "Bearer tok123",
			"X-Custom":      "val",
		}, body.Config.Headers)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"status": "added"})
	}))
	defer ts.Close()

	out, err := executeCommand(ts, "servers", "add", "my-srv",
		"--url", "http://example.com/sse",
		"--headers", "Authorization=Bearer tok123",
		"--headers", "X-Custom=val",
	)
	require.NoError(t, err)
	assert.Contains(t, out, "added")
}

func TestServersAddCommand_BadHeaderFormat(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer ts.Close()

	_, err := executeCommand(ts, "servers", "add", "my-srv",
		"--url", "http://example.com/sse",
		"--headers", "NoEqualsSign",
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "KEY=VALUE")
}

func TestServersAddCommand_EmptyHeaderKey(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer ts.Close()

	_, err := executeCommand(ts, "servers", "add", "my-srv",
		"--url", "http://example.com/sse",
		"--headers", "=value",
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "KEY=VALUE")
}

func TestServersAddCommand_HeaderKeyCRLF(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer ts.Close()

	_, err := executeCommand(ts, "servers", "add", "my-srv",
		"--url", "http://example.com/sse",
		"--headers", "X-Foo\r\nInjected=val",
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "illegal characters")
}

func TestServersAddCommand_HeaderValueCRLF(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer ts.Close()

	_, err := executeCommand(ts, "servers", "add", "my-srv",
		"--url", "http://example.com/sse",
		"--headers", "X-Foo=val\r\nInjected: bad",
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "illegal characters")
}

// --- servers add: server returns error ---

func TestServersAddCommand_ServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{"error": "server already exists"})
	}))
	defer ts.Close()

	_, err := executeCommand(ts, "servers", "add", "dup", "--command", "/bin/x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "server already exists")
}
