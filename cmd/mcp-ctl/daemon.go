package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"mcp-gateway/internal/ctlclient"
	"mcp-gateway/internal/pidfile"

	"github.com/spf13/cobra"
)

func newDaemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Manage the mcp-gateway daemon lifecycle",
	}
	cmd.AddCommand(
		newDaemonStartCmd(),
		newDaemonStopCmd(),
		newDaemonRestartCmd(),
		newDaemonStatusCmd(),
	)
	return cmd
}

// stopDaemon is the shared stop logic used by both daemon_stop and daemon_restart.
// It attempts a graceful REST shutdown, then falls back to PID-file kill.
// Returns nil when the daemon is confirmed unreachable, a non-nil error otherwise.
func stopDaemon(ctx context.Context, client *ctlclient.Client, apiURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	shutdownViaREST := true

	if err := client.Shutdown(ctx); err != nil {
		var connErr *ctlclient.ConnectionError
		if isConnectionError(err, &connErr) {
			// Daemon is already unreachable — nothing to stop.
			return nil
		}
		shutdownViaREST = false
	}

	if shutdownViaREST {
		// Poll until /health is unreachable.
		healthURL := apiURL + "/api/v1/health"
		for time.Now().Before(deadline) {
			if !pidfile.IsLive(healthURL) {
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
		}
	}

	// Fallback: kill via PID file.
	return killViaPIDFile(deadline)
}

// isConnectionError reports whether err wraps a *ctlclient.ConnectionError
// and fills *target when target is non-nil.
func isConnectionError(err error, target **ctlclient.ConnectionError) bool {
	var connErr *ctlclient.ConnectionError
	if errors.As(err, &connErr) {
		if target != nil {
			*target = connErr
		}
		return true
	}
	return false
}

// pollUntilUnreachable blocks until healthURL stops responding (returns true)
// or deadline is exceeded (returns false).
func pollUntilUnreachable(ctx context.Context, healthURL string, deadline time.Time) bool {
	for time.Now().Before(deadline) {
		if !pidfile.IsLive(healthURL) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(200 * time.Millisecond):
		}
	}
	return false
}

// pollUntilLive blocks until healthURL responds (returns true) or deadline is
// exceeded (returns false).
func pollUntilLive(ctx context.Context, healthURL string, deadline time.Time) bool {
	for time.Now().Before(deadline) {
		if pidfile.IsLive(healthURL) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(200 * time.Millisecond):
		}
	}
	return false
}

// formatUptime converts a duration in seconds to a human-readable string.
//
//	< 60s       → "Ns"
//	< 1h        → "Nm Ss"
//	< 24h       → "Nh Mm Ss"
//	>= 24h      → "Nd Hh Mm"
func formatUptime(seconds int64) string {
	if seconds < 0 {
		seconds = 0
	}
	s := seconds % 60
	m := (seconds / 60) % 60
	h := (seconds / 3600) % 24
	d := seconds / 86400

	switch {
	case d > 0:
		return fmt.Sprintf("%dd %dh %dm", d, h, m)
	case h > 0:
		return fmt.Sprintf("%dh %dm %ds", h, m, s)
	case m > 0:
		return fmt.Sprintf("%dm %ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}
