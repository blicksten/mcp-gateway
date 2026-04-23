package main

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"time"

	"mcp-gateway/internal/ctlclient"

	"github.com/spf13/cobra"
)

func newDaemonRestartCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "restart",
		Short: "Restart the mcp-gateway daemon (stop then start)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			apiURL, _ := cmd.Flags().GetString("api-url")
			timeout, _ := cmd.Flags().GetDuration("timeout")
			daemonPath, _ := cmd.Flags().GetString("daemon-path")
			wait, _ := cmd.Flags().GetDuration("wait")
			client, err := getClient(cmd)
			if err != nil {
				return err
			}
			return runDaemonRestart(cmd.Context(), cmd.OutOrStdout(), client, apiURL, daemonPath, timeout, wait)
		},
	}
	cmd.Flags().Duration("timeout", 10*time.Second, "Maximum time to wait for the daemon to stop")
	cmd.Flags().String("daemon-path", "",
		"Path to mcp-gateway binary (env: MCP_GATEWAY_BIN, then PATH)")
	cmd.Flags().Duration("wait", 10*time.Second,
		"How long to wait for the daemon to become reachable after spawn")
	return cmd
}

// runDaemonRestart contains the testable restart logic.
func runDaemonRestart(
	ctx context.Context,
	out io.Writer,
	client *ctlclient.Client,
	apiURL, daemonPath string,
	timeout, wait time.Duration,
) error {
	healthURL := apiURL + "/api/v1/health"
	deadline := time.Now().Add(timeout)

	// Stop — a ConnectionError means the daemon is already down; proceed to start.
	err := stopDaemon(ctx, client, apiURL, timeout)
	if err != nil {
		var connErr *ctlclient.ConnectionError
		if !isConnectionError(err, &connErr) {
			return fmt.Errorf("stop daemon: %w", err)
		}
		// Daemon already unreachable — proceed directly to start.
	} else {
		// Wait until daemon is confirmed unreachable.
		if !pollUntilUnreachable(ctx, healthURL, deadline) {
			return fmt.Errorf("daemon did not stop within %s", timeout)
		}
	}

	// Start the daemon.
	bin, err := resolveDaemonBin(daemonPath)
	if err != nil {
		return err
	}

	child := exec.Command(bin) // #nosec G204 — path resolved + validated by resolveDaemonBin
	if err := getSpawnFunc()(child); err != nil {
		return fmt.Errorf("spawn daemon: %w", err)
	}

	startDeadline := time.Now().Add(wait)
	if !pollUntilLive(ctx, healthURL, startDeadline) {
		if child.Process != nil {
			_ = child.Process.Kill()
		}
		return fmt.Errorf("daemon did not become reachable within %s", wait)
	}

	pidStr, ver := quickHealthInfo(ctx, apiURL)
	fmt.Fprintf(out, "daemon restarted (pid=%s, version=%s)\n", pidStr, ver)
	return nil
}
