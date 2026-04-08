package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"mcp-gateway/internal/ctlclient"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// executeCommand builds a fresh command tree, overrides the API URL,
// and runs with the given args. Returns stdout output and error.
func executeCommand(ts *httptest.Server, args ...string) (string, error) {
	buf := new(bytes.Buffer)
	cmd := newRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs(append([]string{"--api-url", ts.URL}, args...))
	_, err := cmd.ExecuteC()
	return buf.String(), err
}

func TestHealthCommand(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/health" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"status": "ok", "servers": 2, "running": 1,
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	out, err := executeCommand(ts, "health")
	require.NoError(t, err)
	assert.Contains(t, out, "ok")
	assert.Contains(t, out, "2")
	assert.Contains(t, out, "1")
}

func TestStatusAlias(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"status": "ok", "servers": 0, "running": 0,
		})
	}))
	defer ts.Close()

	out, err := executeCommand(ts, "status")
	require.NoError(t, err)
	assert.Contains(t, out, "ok")
}

func TestServersListCommand(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/servers" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]ctlclient.ServerView{
				{Name: "alpha", Status: "running", Transport: "stdio", RestartCount: 0},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	out, err := executeCommand(ts, "servers", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "alpha")
	assert.Contains(t, out, "running")
	assert.Contains(t, out, "stdio")
}

func TestServersListCommand_Empty(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]ctlclient.ServerView{})
	}))
	defer ts.Close()

	out, err := executeCommand(ts, "servers", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "No servers configured")
}

func TestServersListCommand_JSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]ctlclient.ServerView{
			{Name: "beta", Status: "stopped"},
		})
	}))
	defer ts.Close()

	out, err := executeCommand(ts, "servers", "list", "--json")
	require.NoError(t, err)

	var servers []ctlclient.ServerView
	require.NoError(t, json.Unmarshal([]byte(out), &servers))
	assert.Len(t, servers, 1)
	assert.Equal(t, "beta", servers[0].Name)
}

func TestServersGetCommand(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/servers/my-srv" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(ctlclient.ServerView{
				Name: "my-srv", Status: "running", Transport: "stdio", PID: 5678,
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	out, err := executeCommand(ts, "servers", "get", "my-srv")
	require.NoError(t, err)
	assert.Contains(t, out, "my-srv")
	assert.Contains(t, out, "running")
	assert.Contains(t, out, "5678")
}

func TestServersGetCommand_JSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ctlclient.ServerView{
			Name: "my-srv", Status: "running",
		})
	}))
	defer ts.Close()

	out, err := executeCommand(ts, "servers", "get", "--json", "my-srv")
	require.NoError(t, err)

	var sv ctlclient.ServerView
	require.NoError(t, json.Unmarshal([]byte(out), &sv))
	assert.Equal(t, "my-srv", sv.Name)
}

func TestServersGetCommand_MissingArg(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer ts.Close()

	_, err := executeCommand(ts, "servers", "get")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg")
}

func TestServersGetCommand_NotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "server not found"})
	}))
	defer ts.Close()

	_, err := executeCommand(ts, "servers", "get", "missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "server not found")
}

func TestExitCode_ConnectionError(t *testing.T) {
	err := &ctlclient.ConnectionError{URL: "http://127.0.0.1:1", Err: nil}
	assert.Equal(t, exitUnreachable, exitCode(err))
}

func TestExitCode_APIError(t *testing.T) {
	err := &ctlclient.APIError{StatusCode: 404, Message: "not found"}
	assert.Equal(t, exitError, exitCode(err))
}

func TestExitCode_Nil(t *testing.T) {
	assert.Equal(t, exitOK, exitCode(nil))
}
