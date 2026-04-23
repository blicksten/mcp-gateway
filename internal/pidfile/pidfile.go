// Package pidfile manages a PID file for the mcp-gateway daemon.
//
// Design choices:
//   - Write takes a separate `addr` parameter for HTTP liveness probing
//     so the caller controls the gateway address without this package
//     importing anything from the api layer (avoids circular deps).
//   - Stale detection is HTTP-based (IsLive), not signal-based, so the
//     logic works identically on Linux and Windows (AUDIT M-2).
//   - O_EXCL + post-write Lstat guard mitigates TOCTOU races on world-
//     writable /tmp (AUDIT M-1).
package pidfile

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ErrAlreadyRunning is returned by Write when an existing PID file points
// to a live daemon (HTTP /health reachable). Callers use errors.Is to
// distinguish this from other write failures.
var ErrAlreadyRunning = errors.New("another instance is already running")

// livenessProbTimeout is the HTTP client timeout for the liveness check.
// Short on purpose: if the daemon is not responding within half a second
// it is considered dead for PID-file purposes.
const livenessProbTimeout = 500 * time.Millisecond

// Write atomically acquires the PID file at path.
//
// On success the file contains the current process PID (mode 0600).
// probeURL is the full health-probe URL (e.g. "http://127.0.0.1:8765/api/v1/health"
// or "https://127.0.0.1:8765/api/v1/health") passed to IsLive when an
// existing file is found. An empty probeURL disables stale detection — if
// the file exists, Write returns ErrAlreadyRunning without probing.
//
// Stale-reap behaviour: if the file exists but IsLive(probeURL) returns false
// the file is removed and a single re-create attempt is made. If that
// second attempt also fails (race with another starter) the error is
// returned as-is.
func Write(path, probeURL string) error {
	pid := os.Getpid()
	content := []byte(strconv.Itoa(pid) + "\n")

	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600) //nolint:nosnakecase — os constants
	if err != nil {
		if !os.IsExist(err) {
			return fmt.Errorf("create pid file: %w", err)
		}
		// File already exists — check whether the owner is alive.
		if probeURL == "" || IsLive(probeURL) {
			return ErrAlreadyRunning
		}
		// Stale file: remove and try once more.
		if rmErr := os.Remove(path); rmErr != nil && !os.IsNotExist(rmErr) {
			return fmt.Errorf("remove stale pid file: %w", rmErr)
		}
		f, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600) //nolint:nosnakecase — os constants
		if err != nil {
			if os.IsExist(err) {
				return ErrAlreadyRunning
			}
			return fmt.Errorf("create pid file after stale-reap: %w", err)
		}
	}

	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return fmt.Errorf("write pid file: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("close pid file: %w", err)
	}

	// AUDIT M-1: verify the created file is not a symlink.
	// An adversary on a shared /tmp could race us with a symlink pointing
	// to a sensitive file. O_EXCL protects the create, but double-check.
	fi, err := os.Lstat(path)
	if err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("lstat pid file: %w", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		_ = os.Remove(path)
		return fmt.Errorf("pid file is a symlink — possible attack detected: %s", path)
	}

	return nil
}

// Remove deletes the PID file. Best-effort: returns nil if the file is
// already gone (idempotent on ENOENT).
func Remove(path string) error {
	err := os.Remove(path)
	if err != nil && os.IsNotExist(err) {
		return nil
	}
	return err
}

// DefaultPath returns the canonical PID file path for this installation.
//
// On Linux, if $XDG_RUNTIME_DIR is set and the directory is writable,
// the PID file is placed there (per-user, 0700 directory — eliminates
// the world-writable /tmp attack surface). On all other platforms, or
// when XDG_RUNTIME_DIR is not writable, falls back to os.TempDir().
func DefaultPath() string {
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		// Probe writability by attempting a stat; accept it if the dir exists.
		if info, err := os.Stat(xdg); err == nil && info.IsDir() {
			// Best-effort write probe: try creating a temp file.
			probe := filepath.Join(xdg, ".mcp-gateway-probe")
			if f, err := os.OpenFile(probe, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600); err == nil { //nolint:nosnakecase — os constants
				_ = f.Close()
				_ = os.Remove(probe)
				return filepath.Join(xdg, "mcp-gateway.pid")
			}
		}
	}
	return filepath.Join(os.TempDir(), "mcp-gateway.pid")
}

// Read reads and parses the PID stored in path. Tolerates leading/trailing
// whitespace. Returns an error if the file is missing, empty, or contains
// a non-numeric value.
func Read(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read pid file: %w", err)
	}
	s := strings.TrimSpace(string(data))
	if s == "" {
		return 0, fmt.Errorf("pid file is empty: %s", path)
	}
	pid, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("pid file contains non-numeric value %q: %w", s, err)
	}
	return pid, nil
}

// IsLive probes the daemon's health endpoint.
//
// probeURL must be a full URL (e.g. "http://127.0.0.1:8765/api/v1/health"
// or "https://127.0.0.1:8765/api/v1/health"); empty URL returns false.
// HTTPS probes use InsecureSkipVerify because the probe only targets the
// local loopback and self-signed certs are common in dev deployments —
// this is an *existence* probe, not a security-sensitive trust check.
// Returns true only when the endpoint responds with HTTP 200. Any error
// (connection refused, timeout, non-200) returns false.
//
// This is intentionally HTTP-based rather than signal-based because
// os.FindProcess + Signal(0) is unreliable on Windows (AUDIT M-2).
func IsLive(probeURL string) bool {
	if probeURL == "" {
		return false
	}
	transport := &http.Transport{}
	if strings.HasPrefix(probeURL, "https://") {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec — local loopback existence probe only
	}
	client := &http.Client{Timeout: livenessProbTimeout, Transport: transport}
	resp, err := client.Get(probeURL) //nolint:noctx — intentionally no context; timeout is set on the client
	if err != nil {
		return false
	}
	defer resp.Body.Close() //nolint:errcheck — discard body, close is best-effort
	return resp.StatusCode == http.StatusOK
}
