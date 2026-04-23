package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

func newDaemonStopCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop the mcp-gateway daemon",
		RunE: func(cmd *cobra.Command, _ []string) error {
			apiURL, _ := cmd.Flags().GetString("api-url")
			timeout, _ := cmd.Flags().GetDuration("timeout")
			client, err := getClient(cmd)
			if err != nil {
				return err
			}

			// stopDaemon is the single source of truth about unreachable state
			// — it polls internally on the REST path and kills via PID file on
			// the fallback path (CV-LOW fix: removed redundant second poll).
			if err := stopDaemon(cmd.Context(), client, apiURL, timeout); err != nil {
				return fmt.Errorf("stop daemon: %w", err)
			}

			fmt.Fprintln(cmd.OutOrStdout(), "daemon stopped")
			return nil
		},
	}
	cmd.Flags().Duration("timeout", 10*time.Second, "Maximum time to wait for the daemon to stop")
	return cmd
}
