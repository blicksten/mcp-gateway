package pidfile_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"mcp-gateway/internal/pidfile"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- Write tests ----

func TestWrite_FreshPath_SucceedsWithCorrectModeAndContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.pid")

	require.NoError(t, pidfile.Write(path, ""))

	// Mode check: on Unix the file must be 0600; Windows does not honour
	// Unix permission bits via os.Stat, so we skip the mode assertion there.
	fi, err := os.Stat(path)
	require.NoError(t, err)
	if os.PathSeparator != '\\' {
		// Mask to permission bits only (os.ModePerm = 0777).
		assert.Equal(t, os.FileMode(0600), fi.Mode()&os.ModePerm)
	}

	// Content must be the current PID.
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	require.NoError(t, err)
	assert.Equal(t, os.Getpid(), pid)
}

func TestWrite_ExistingLivePID_ReturnsErrAlreadyRunning(t *testing.T) {
	// Stand up a real HTTP server that returns 200 on /api/v1/health.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "test.pid")

	// Write a fake PID to the file (the actual PID value doesn't matter;
	// liveness is determined by HTTP, not process signals).
	require.NoError(t, os.WriteFile(path, []byte("99999\n"), 0600))

	// Full probe URL of the live stub server.
	err := pidfile.Write(path, ts.URL+"/api/v1/health")
	assert.ErrorIs(t, err, pidfile.ErrAlreadyRunning)
}

func TestWrite_ExistingStalePID_ReapsAndSucceeds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.pid")

	// Write a stale PID file (no HTTP server is listening on the addr).
	require.NoError(t, os.WriteFile(path, []byte("99999\n"), 0600))

	// Use an address where nothing is listening — IsLive will return false.
	err := pidfile.Write(path, "http://127.0.0.1:19999/api/v1/health")
	require.NoError(t, err, "stale-reap should succeed")

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	require.NoError(t, err)
	assert.Equal(t, os.Getpid(), pid)
}

func TestWrite_SymlinkAttack_ReturnsError(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("symlinks on Windows require elevated privileges — skipping")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "sensitive")
	linkPath := filepath.Join(dir, "test.pid")

	// Create target file and point the link at it.
	require.NoError(t, os.WriteFile(target, []byte("secret"), 0600))
	require.NoError(t, os.Symlink(target, linkPath))

	err := pidfile.Write(linkPath, "")
	// The write must fail, and the symlink/target must not be clobbered.
	require.Error(t, err)
	// Target file must still exist with original contents.
	data, readErr := os.ReadFile(target)
	require.NoError(t, readErr)
	assert.Equal(t, "secret", string(data))
}

// ---- DefaultPath tests ----

func TestDefaultPath_XDGSet_ReturnsXDGPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)

	got := pidfile.DefaultPath()
	assert.Equal(t, filepath.Join(dir, "mcp-gateway.pid"), got)
}

func TestDefaultPath_XDGUnset_ReturnsTempDir(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")

	got := pidfile.DefaultPath()
	assert.Equal(t, filepath.Join(os.TempDir(), "mcp-gateway.pid"), got)
}

// ---- Read tests ----

func TestRead_NormalContent_ParsesPID(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantPID int
		wantErr bool
	}{
		{name: "bare pid", content: "12345\n", wantPID: 12345},
		{name: "padded with spaces", content: "  12345  ", wantPID: 12345},
		{name: "no newline", content: "42", wantPID: 42},
		{name: "empty file", content: "", wantErr: true},
		{name: "non-numeric", content: "abc\n", wantErr: true},
		{name: "whitespace only", content: "   \n", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "test.pid")
			require.NoError(t, os.WriteFile(path, []byte(tc.content), 0600))

			pid, err := pidfile.Read(path)
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.wantPID, pid)
			}
		})
	}
}

func TestRead_MissingFile_ReturnsError(t *testing.T) {
	_, err := pidfile.Read("/nonexistent/path/test.pid")
	assert.Error(t, err)
}

// ---- IsLive tests ----

func TestIsLive_HTTP200_ReturnsTrue(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	assert.True(t, pidfile.IsLive(ts.URL+"/api/v1/health"))
}

func TestIsLive_HTTPS_SelfSigned_ReturnsTrue(t *testing.T) {
	// CV-MEDIUM fix: IsLive must accept https:// URLs with self-signed certs
	// so TLS-enabled daemons are correctly detected as live.
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	assert.True(t, pidfile.IsLive(ts.URL+"/api/v1/health"))
}

func TestIsLive_EmptyURL_ReturnsFalse(t *testing.T) {
	assert.False(t, pidfile.IsLive(""))
}

func TestIsLive_HTTP503_ReturnsFalse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	assert.False(t, pidfile.IsLive(ts.URL+"/api/v1/health"))
}

func TestIsLive_ConnectionRefused_ReturnsFalse(t *testing.T) {
	// Nothing listening on this port.
	assert.False(t, pidfile.IsLive("http://127.0.0.1:19998/api/v1/health"))
}

func TestIsLive_Timeout_ReturnsFalse(t *testing.T) {
	// Server that hangs for longer than the 500ms probe timeout.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	start := time.Now()
	result := pidfile.IsLive(ts.URL + "/api/v1/health")
	elapsed := time.Since(start)

	assert.False(t, result)
	// Should return within ~600ms (500ms timeout + small buffer).
	assert.Less(t, elapsed, 600*time.Millisecond,
		fmt.Sprintf("IsLive took %v, expected <600ms", elapsed))
}

// ---- Remove tests ----

func TestRemove_ExistingFile_DeletesIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.pid")
	require.NoError(t, os.WriteFile(path, []byte("1\n"), 0600))

	require.NoError(t, pidfile.Remove(path))
	_, err := os.Stat(path)
	assert.True(t, os.IsNotExist(err))
}

func TestRemove_MissingFile_ReturnsNil(t *testing.T) {
	require.NoError(t, pidfile.Remove("/nonexistent/path/test.pid"))
}
