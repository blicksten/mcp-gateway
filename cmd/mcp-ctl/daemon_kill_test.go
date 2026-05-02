package main

import (
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"mcp-gateway/internal/pidfile"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- isDaemonBasename --------------------------------------------------

func TestIsDaemonBasename(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{"mcp-gateway", true},
		{"mcp-gateway.exe", true},
		{"MCP-Gateway.exe", true}, // Windows case-insensitive
		{"MCP-GATEWAY", true},
		{"/usr/local/bin/mcp-gateway", true}, // strips dir prefix
		{`C:\Program Files\MCP\mcp-gateway.exe`, true},
		{"notepad.exe", false},
		{"chrome.exe", false},
		{"mcp-gateway-helper", false}, // similar but different
		{"go", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got := isDaemonBasename(tc.input)
			assert.Equal(t, tc.want, got)
		})
	}
}

// --- killByPIDFile (B-NEW-21 verification) -----------------------------

// withTempPIDFile sets up a temporary PID file owned by the test, restores
// pidfile.DefaultPath via env override, and returns the cleanup function.
// We cannot mock pidfile.DefaultPath directly (package-level func) so we
// rely on the package's own test hooks: write to TMPDIR and verify path.
func writePIDFile(t *testing.T, pid int) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)        // POSIX
	t.Setenv("TMP", dir)            // Windows
	t.Setenv("TEMP", dir)           // Windows alt
	t.Setenv("XDG_RUNTIME_DIR", "") // disable XDG fast-path so tmp wins
	path := pidfile.DefaultPath()
	require.True(t, strings.HasPrefix(path, dir),
		"pidfile.DefaultPath should resolve under temp: got %s, want prefix %s", path, dir)
	require.NoError(t, os.WriteFile(path, []byte("12345\n"), 0o600))
	_ = pid // pid hard-coded in the file content above
	return path
}

func TestKillByPIDFile_RefusesNonGatewayProcess(t *testing.T) {
	// Tests B-NEW-21: PID-recycling defence. Even though the PID file
	// points to a live process, killByPIDFile must refuse to invoke
	// killFn when the process at that PID is NOT mcp-gateway.
	pidPath := writePIDFile(t, 12345)
	defer os.Remove(pidPath)

	// Inject verifier that reports a non-daemon basename.
	originalVerify := verifyExeBasenameFunc
	t.Cleanup(func() { verifyExeBasenameFunc = originalVerify })
	verifyExeBasenameFunc = func(_ int) (string, error) {
		return "notepad.exe", nil
	}

	killCalls := 0
	err := killByPIDFile(time.Time{}, func(_ int) error {
		killCalls++
		return nil
	})
	require.Error(t, err)
	assert.Equal(t, 0, killCalls, "killFn must NOT be invoked when verifier reports non-gateway process")
	assert.Contains(t, err.Error(), `belongs to "notepad.exe"`)
	// Pidfile must be left in place (operator notices stale state).
	_, statErr := os.Stat(pidPath)
	assert.NoError(t, statErr, "pidfile should NOT be removed when kill is refused")
}

func TestKillByPIDFile_AcceptsMcpGatewayBasename(t *testing.T) {
	pidPath := writePIDFile(t, 12345)
	defer os.Remove(pidPath)

	originalVerify := verifyExeBasenameFunc
	t.Cleanup(func() { verifyExeBasenameFunc = originalVerify })
	expectedBase := "mcp-gateway"
	if runtime.GOOS == "windows" {
		expectedBase = "mcp-gateway.exe"
	}
	verifyExeBasenameFunc = func(_ int) (string, error) {
		return expectedBase, nil
	}

	killCalls := 0
	receivedPID := 0
	err := killByPIDFile(time.Time{}, func(pid int) error {
		killCalls++
		receivedPID = pid
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 1, killCalls, "killFn must be invoked exactly once on positive verification")
	assert.Equal(t, 12345, receivedPID, "killFn must receive the pid from the pidfile")
	// Pidfile must be removed after successful kill.
	_, statErr := os.Stat(pidPath)
	assert.True(t, os.IsNotExist(statErr), "pidfile should be removed after successful kill")
}

func TestKillByPIDFile_RefusesOnVerifyError(t *testing.T) {
	// Conservative policy from PLAN_FILE: "On lookup failure, refuse + log".
	// Mirrors macOS behaviour (errVerifyUnsupported) and access-denied or
	// process-already-exited races on Windows/Linux.
	pidPath := writePIDFile(t, 12345)
	defer os.Remove(pidPath)

	originalVerify := verifyExeBasenameFunc
	t.Cleanup(func() { verifyExeBasenameFunc = originalVerify })
	verifyExeBasenameFunc = func(_ int) (string, error) {
		return "", errors.New("OpenProcess(12345): access denied")
	}

	killCalls := 0
	err := killByPIDFile(time.Time{}, func(_ int) error {
		killCalls++
		return nil
	})
	require.Error(t, err)
	assert.Equal(t, 0, killCalls, "killFn must NOT be invoked on verify error")
	assert.Contains(t, err.Error(), "access denied")
	// Pidfile must be left in place.
	_, statErr := os.Stat(pidPath)
	assert.NoError(t, statErr, "pidfile should NOT be removed on verify error")
}

func TestKillByPIDFile_PIDFileMissingTreatedAsAlreadyGone(t *testing.T) {
	// Existing behaviour preserved: when pidfile is missing, killByPIDFile
	// returns an error but verification is never attempted (we have no PID
	// to verify against). Verifies the verify-step doesn't trigger before
	// the read-step.
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	t.Setenv("TMP", dir)
	t.Setenv("TEMP", dir)
	t.Setenv("XDG_RUNTIME_DIR", "")
	// Do NOT create the pidfile.

	originalVerify := verifyExeBasenameFunc
	t.Cleanup(func() { verifyExeBasenameFunc = originalVerify })
	verifierCalled := false
	verifyExeBasenameFunc = func(_ int) (string, error) {
		verifierCalled = true
		return "mcp-gateway", nil
	}

	killCalls := 0
	err := killByPIDFile(time.Time{}, func(_ int) error {
		killCalls++
		return nil
	})
	require.Error(t, err)
	assert.False(t, verifierCalled, "verifier must not be called when pidfile is missing")
	assert.Equal(t, 0, killCalls)
	assert.Contains(t, err.Error(), "cannot find daemon PID")
}

// --- verifyExeBasename (platform-specific real-call test) ---------------

// On Linux and Windows, the real implementation should resolve the test
// binary's own PID to a valid basename. On other platforms (macOS, BSD)
// the fallback returns errVerifyUnsupported by design — this test
// documents and pins that contract.
func TestVerifyExeBasename_OwnProcess(t *testing.T) {
	pid := os.Getpid()
	basename, err := verifyExeBasename(pid)
	switch runtime.GOOS {
	case "linux", "windows":
		require.NoError(t, err, "verifyExeBasename should succeed for own pid on linux/windows")
		assert.NotEmpty(t, basename)
		// Test binary basename is something like "mcp-ctl.test" or
		// "mcp-ctl.test.exe" — does NOT match daemonExeBasenames, which
		// is exactly the safety property we want.
		assert.False(t, isDaemonBasename(basename),
			"test binary basename %q should NOT pass isDaemonBasename — guards against false-positive in identity check", basename)
	default:
		require.Error(t, err, "verifyExeBasename should refuse on non-linux/non-windows POSIX")
		assert.True(t,
			strings.Contains(err.Error(), "not implemented") || strings.Contains(err.Error(), "refusing kill"),
			"expected unsupported-platform error, got: %v", err)
	}
}
