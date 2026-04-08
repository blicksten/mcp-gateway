package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"mcp-gateway/internal/ctlclient"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- tools list ---

func TestToolsListCommand(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/tools" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]ctlclient.Tool{
				{Name: "read_file", Server: "fs", Description: "Read a file"},
				{Name: "search", Server: "web", Description: "Web search"},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	out, err := executeCommand(ts, "tools", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "read_file")
	assert.Contains(t, out, "fs")
	assert.Contains(t, out, "search")
	assert.Contains(t, out, "web")
	assert.Contains(t, out, "NAME")
	assert.Contains(t, out, "SERVER")
	assert.Contains(t, out, "DESCRIPTION")
}

func TestToolsListCommand_Empty(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]ctlclient.Tool{})
	}))
	defer ts.Close()

	out, err := executeCommand(ts, "tools", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "No tools available")
}

func TestToolsListCommand_JSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]ctlclient.Tool{
			{Name: "echo", Server: "test", Description: "Echo input"},
		})
	}))
	defer ts.Close()

	out, err := executeCommand(ts, "tools", "list", "--json")
	require.NoError(t, err)

	var tools []ctlclient.Tool
	require.NoError(t, json.Unmarshal([]byte(out), &tools))
	assert.Len(t, tools, 1)
	assert.Equal(t, "echo", tools[0].Name)
}

func TestToolsListCommand_ServerFilter(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]ctlclient.Tool{
			{Name: "read_file", Server: "fs", Description: "Read a file"},
			{Name: "search", Server: "web", Description: "Web search"},
			{Name: "write_file", Server: "fs", Description: "Write a file"},
		})
	}))
	defer ts.Close()

	out, err := executeCommand(ts, "tools", "list", "--server", "fs")
	require.NoError(t, err)
	assert.Contains(t, out, "read_file")
	assert.Contains(t, out, "write_file")
	assert.NotContains(t, out, "search")
	assert.NotContains(t, out, "web")
}

func TestToolsListCommand_ServerFilter_NoMatch(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]ctlclient.Tool{
			{Name: "read_file", Server: "fs", Description: "Read a file"},
		})
	}))
	defer ts.Close()

	out, err := executeCommand(ts, "tools", "list", "--server", "nonexistent")
	require.NoError(t, err)
	assert.Contains(t, out, "No tools available")
}

// --- tools call ---

func TestToolsCallCommand(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/servers/myserver/call" && r.Method == "POST" {
			var req struct {
				Tool      string         `json:"tool"`
				Arguments map[string]any `json:"arguments"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			assert.Equal(t, "read_file", req.Tool)
			assert.Equal(t, "test.txt", req.Arguments["path"])

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(ctlclient.CallResult{
				Content: []ctlclient.ContentItem{
					{Type: "text", Text: "file contents here"},
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	out, err := executeCommand(ts, "tools", "call", "myserver__read_file", "--arg", "path=test.txt")
	require.NoError(t, err)
	assert.Contains(t, out, "file contents here")
}

func TestToolsCallCommand_MultipleArgs(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Tool      string         `json:"tool"`
			Arguments map[string]any `json:"arguments"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, "search", req.Tool)
		assert.Equal(t, "hello", req.Arguments["query"])
		assert.Equal(t, "10", req.Arguments["limit"])

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ctlclient.CallResult{
			Content: []ctlclient.ContentItem{{Type: "text", Text: "results"}},
		})
	}))
	defer ts.Close()

	out, err := executeCommand(ts, "tools", "call", "web__search", "--arg", "query=hello", "--arg", "limit=10")
	require.NoError(t, err)
	assert.Contains(t, out, "results")
}

func TestToolsCallCommand_CompactJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ctlclient.CallResult{
			Content: []ctlclient.ContentItem{{Type: "text", Text: "ok"}},
		})
	}))
	defer ts.Close()

	out, err := executeCommand(ts, "tools", "call", "srv__tool", "--json")
	require.NoError(t, err)

	var result ctlclient.CallResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	assert.Len(t, result.Content, 1)
	assert.Equal(t, "ok", result.Content[0].Text)
}

func TestToolsCallCommand_NoArgs(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Tool      string         `json:"tool"`
			Arguments map[string]any `json:"arguments"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, "ping", req.Tool)
		assert.Nil(t, req.Arguments)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ctlclient.CallResult{
			Content: []ctlclient.ContentItem{{Type: "text", Text: "pong"}},
		})
	}))
	defer ts.Close()

	out, err := executeCommand(ts, "tools", "call", "srv__ping")
	require.NoError(t, err)
	assert.Contains(t, out, "pong")
}

func TestToolsCallCommand_MissingArg(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer ts.Close()

	_, err := executeCommand(ts, "tools", "call")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg")
}

func TestToolsCallCommand_InvalidToolName(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer ts.Close()

	_, err := executeCommand(ts, "tools", "call", "no-separator")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected format server__tool")
}

func TestToolsCallCommand_InvalidArgFormat(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer ts.Close()

	_, err := executeCommand(ts, "tools", "call", "srv__tool", "--arg", "noequals")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected key=value")
}

// --- parseToolName unit tests ---

func TestParseToolName(t *testing.T) {
	tests := []struct {
		input      string
		wantServer string
		wantTool   string
		wantErr    bool
	}{
		{"srv__tool", "srv", "tool", false},
		{"my-server__read_file", "my-server", "read_file", false},
		{"a__b__c", "a", "b__c", false}, // SplitN with 2 keeps extra __ in tool name
		{"notool", "", "", true},
		{"__tool", "", "", true},
		{"srv__", "", "", true},
		{"", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			server, tool, err := parseToolName(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantServer, server)
				assert.Equal(t, tt.wantTool, tool)
			}
		})
	}
}

// --- parseKeyValueArgs unit tests ---

func TestParseKeyValueArgs(t *testing.T) {
	tests := []struct {
		name    string
		input   []string
		want    map[string]any
		wantErr bool
	}{
		{"empty", nil, nil, false},
		{"single", []string{"key=val"}, map[string]any{"key": "val"}, false},
		{"multiple", []string{"a=1", "b=2"}, map[string]any{"a": "1", "b": "2"}, false},
		{"value with equals", []string{"cmd=a=b"}, map[string]any{"cmd": "a=b"}, false},
		{"empty value", []string{"key="}, map[string]any{"key": ""}, false},
		{"no equals", []string{"bad"}, nil, true},
		{"empty key", []string{"=val"}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseKeyValueArgs(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}
