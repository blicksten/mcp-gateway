//go:build !windows

package lifecycle

import "log/slog"

// jobHandle is a no-op placeholder on non-Windows platforms.
type jobHandle struct{}

// newJobObject is a no-op on non-Windows platforms.
func newJobObject() (jobHandle, error) { return jobHandle{}, nil }

// assignProcess is a no-op on non-Windows platforms.
func assignProcess(_ jobHandle, _ uint32) error { return nil }

// closeJobObject is a no-op on non-Windows platforms.
func closeJobObject(_ jobHandle) error { return nil }

// retryAssignProcess is a no-op on non-Windows platforms.
// On Windows it retries AssignProcessToJobObject with exponential backoff.
func retryAssignProcess(_ jobHandle, _ uint32, _ *slog.Logger, _ string) error { return nil }
