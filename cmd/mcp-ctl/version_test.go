package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// executeCommandNoServer builds a fresh command tree and runs without a gateway.
func executeCommandNoServer(args ...string) (string, error) {
	buf := new(bytes.Buffer)
	cmd := newRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs(args)
	_, err := cmd.ExecuteC()
	return buf.String(), err
}

func TestVersionCommand_NoServer(t *testing.T) {
	// Version command must work without a running gateway (skipClient annotation).
	out, err := executeCommandNoServer("version")
	require.NoError(t, err)
	assert.Contains(t, out, "mcp-ctl version")
}

func TestVersionCommand_LdflagsFormat(t *testing.T) {
	// When ldflags are injected (version != "dev"), output uses the compact format.
	oldVersion, oldCommit, oldDate := version, commit, date
	defer func() { version, commit, date = oldVersion, oldCommit, oldDate }()

	version = "1.2.3"
	commit = "abc1234"
	date = "2026-01-01T00:00:00Z"

	out, err := executeCommandNoServer("version")
	require.NoError(t, err)
	assert.Contains(t, out, "mcp-ctl version 1.2.3")
	assert.Contains(t, out, "commit: abc1234")
	assert.Contains(t, out, "built: 2026-01-01T00:00:00Z")
	// Should NOT contain "go:" line (that's the dev fallback format).
	assert.NotContains(t, out, "go:")
}

func TestVersionCommand_DevFallback(t *testing.T) {
	// When version == "dev", output uses debug.ReadBuildInfo() fallback.
	// The "go:" line depends on debug.ReadBuildInfo() being available,
	// which may not hold in all build modes (stripped binaries, go run).
	oldVersion := version
	defer func() { version = oldVersion }()

	version = "dev"

	out, err := executeCommandNoServer("version")
	require.NoError(t, err)
	assert.Contains(t, out, "mcp-ctl version")
}

func TestCompletionCommand_NoServer(t *testing.T) {
	// Completion command must work without a running gateway (skipClient annotation).
	// Output goes to os.Stdout (Cobra's completion behavior), not to cmd.OutOrStdout().
	_, err := executeCommandNoServer("completion", "bash")
	require.NoError(t, err)
}

func TestPersistentPreRunE_SkipAnnotation(t *testing.T) {
	// Commands with skipClient annotation should not have a client in context.
	cmd := newRootCmd()
	cmd.SetArgs([]string{"version"})
	_, err := cmd.ExecuteC()
	require.NoError(t, err)
}

func TestGetClient_ErrorOnMissingClient(t *testing.T) {
	// getClient returns error (not panic) when client is not in context.
	cmd := newRootCmd()
	_, err := getClient(cmd)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PersistentPreRunE")
}
