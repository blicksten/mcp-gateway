package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockServerBinary is set by TestMain after building the mock server.
var mockServerBinary string

func TestMain(m *testing.M) {
	// Build the mock server binary into a temp directory.
	tmpDir, err := os.MkdirTemp("", "mcp-ctl-validate-test")
	if err != nil {
		panic("failed to create temp dir: " + err.Error())
	}
	defer os.RemoveAll(tmpDir)

	binary := filepath.Join(tmpDir, "mock-server")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}

	cmd := exec.Command("go", "build", "-o", binary, "mcp-gateway/internal/testutil")
	cmd.Dir = filepath.Join("..", "..") // module root
	out, err := cmd.CombinedOutput()
	if err != nil {
		panic("failed to build mock server: " + string(out))
	}
	mockServerBinary = binary

	os.Exit(m.Run())
}

func TestValidateCommand_Success(t *testing.T) {
	root := newRootCmd()
	buf := new(bytes.Buffer)
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"validate", "--command", mockServerBinary})

	err := root.Execute()
	require.NoError(t, err, "validate should succeed for valid server")

	output := buf.String()
	assert.Contains(t, output, "[PASS] connect")
	assert.Contains(t, output, "[PASS] list_tools")
	assert.Contains(t, output, "[PASS] ping")
	assert.Contains(t, output, "[PASS] close")
	assert.Contains(t, output, "All checks passed")
}

func TestValidateCommand_JSONOutput(t *testing.T) {
	root := newRootCmd()
	buf := new(bytes.Buffer)
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"validate", "--command", mockServerBinary, "--json"})

	err := root.Execute()
	require.NoError(t, err)

	var results []validateResult
	err = json.Unmarshal(buf.Bytes(), &results)
	require.NoError(t, err, "output should be valid JSON")
	require.Len(t, results, 4, "expected 4 validation steps")

	for _, r := range results {
		assert.True(t, r.Passed, "step %s should pass", r.Step)
	}
	assert.Equal(t, "connect", results[0].Step)
	assert.Equal(t, "list_tools", results[1].Step)
	assert.Equal(t, "ping", results[2].Step)
	assert.Equal(t, "close", results[3].Step)
}

func TestValidateCommand_InvalidBinary(t *testing.T) {
	root := newRootCmd()
	buf := new(bytes.Buffer)
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"validate", "--command", "/nonexistent/binary"})

	err := root.Execute()
	require.Error(t, err, "validate should fail for invalid binary")

	output := buf.String()
	assert.Contains(t, output, "[FAIL] connect")
	assert.Contains(t, output, "process error")
}

func TestValidateCommand_ProcessExits(t *testing.T) {
	// Use a command that exits immediately.
	exitCmd := "true"
	if runtime.GOOS == "windows" {
		exitCmd = "cmd"
	}

	root := newRootCmd()
	buf := new(bytes.Buffer)
	root.SetOut(buf)
	root.SetErr(buf)

	args := []string{"validate", "--command", exitCmd, "--timeout", "5s"}
	if runtime.GOOS == "windows" {
		args = []string{"validate", "--command", exitCmd, "--args", "/c,exit", "--timeout", "5s"}
	}
	root.SetArgs(args)

	err := root.Execute()
	require.Error(t, err, "validate should fail when process exits immediately")

	output := buf.String()
	assert.Contains(t, output, "[FAIL] connect")
}

func TestValidateCommand_StdoutPollution(t *testing.T) {
	// Use echo to write non-JSON-RPC bytes to stdout.
	echoCmd := "echo"
	echoArgs := []string{"hello garbage"}
	if runtime.GOOS == "windows" {
		echoCmd = "cmd"
		echoArgs = []string{"/c", "echo", "hello garbage"}
	}

	root := newRootCmd()
	buf := new(bytes.Buffer)
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"validate", "--command", echoCmd, "--args", joinArgs(echoArgs), "--timeout", "5s"})

	err := root.Execute()
	require.Error(t, err, "validate should fail for stdout pollution")

	output := buf.String()
	assert.Contains(t, output, "[FAIL] connect")
}

func TestValidateCommand_NoCommand(t *testing.T) {
	root := newRootCmd()
	buf := new(bytes.Buffer)
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"validate"})

	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--command is required")
}

func TestValidateCommand_FromConfig(t *testing.T) {
	// Create a temp config file with the mock server.
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")
	cfg := map[string]any{
		"gateway": map[string]any{},
		"servers": map[string]any{
			"test-server": map[string]any{
				"command": mockServerBinary,
			},
		},
	}
	data, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0644))

	root := newRootCmd()
	buf := new(bytes.Buffer)
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"validate", "--config", configPath, "--server", "test-server"})

	err = root.Execute()
	require.NoError(t, err, "validate from config should succeed")

	output := buf.String()
	assert.Contains(t, output, "All checks passed")
}

func TestClassifyConnectError_Categories(t *testing.T) {
	tests := []struct {
		name     string
		errMsg   string
		contains string
	}{
		{"exec not found", "exec: \"foo\": executable file not found in %PATH%", "process error"},
		{"exit status", "exit status 1", "process exited unexpectedly"},
		{"timeout", "context deadline exceeded", "timeout during connect"},
		{"json error", "invalid character 'h' looking for beginning of value", "possible stdout pollution"},
		{"unmarshal", "json: cannot unmarshal string", "possible stdout pollution"},
		{"unknown", "some other error", "connect failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := classifyConnectError(fmt.Errorf("%s", tt.errMsg))
			assert.Contains(t, result, tt.contains)
		})
	}
}

// joinArgs joins args with commas for cobra's StringSlice format.
func joinArgs(args []string) string {
	return strings.Join(args, ",")
}
