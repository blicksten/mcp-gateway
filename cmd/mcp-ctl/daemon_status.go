package main

import (
	"errors"
	"fmt"
	"text/tabwriter"

	"mcp-gateway/internal/ctlclient"

	"github.com/spf13/cobra"
)

func newDaemonStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "status",
		Aliases: []string{"info"},
		Short:   "Show daemon status (pid, version, uptime)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := getClient(cmd)
			if err != nil {
				return err
			}
			h, err := client.Health(cmd.Context())
			if err != nil {
				var connErr *ctlclient.ConnectionError
				if errors.As(err, &connErr) {
					w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
					fmt.Fprintf(w, "STATUS\t%s\n", "offline")
					_ = w.Flush()
					return err // exitCode maps ConnectionError → exitUnreachable
				}
				return err
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintf(w, "STATUS\t%s\n", h.Status)
			if h.PID != 0 {
				fmt.Fprintf(w, "PID\t%d\n", h.PID)
			}
			if h.Version != "" {
				fmt.Fprintf(w, "VERSION\t%s\n", h.Version)
			}
			if h.StartedAt != "" {
				fmt.Fprintf(w, "STARTED\t%s\n", h.StartedAt)
			}
			if h.UptimeSeconds > 0 {
				fmt.Fprintf(w, "UPTIME\t%s\n", formatUptime(h.UptimeSeconds))
			}
			fmt.Fprintf(w, "SERVERS\t%d\n", h.Servers)
			fmt.Fprintf(w, "RUNNING\t%d\n", h.Running)
			return w.Flush()
		},
	}
}
