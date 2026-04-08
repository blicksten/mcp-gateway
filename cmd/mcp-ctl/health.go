package main

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func newHealthCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "health",
		Aliases: []string{"status"},
		Short:   "Show gateway health status",
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := getClient(cmd)
			if err != nil {
				return err
			}
			h, err := client.Health(cmd.Context())
			if err != nil {
				return err
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintf(w, "STATUS\t%s\n", h.Status)
			fmt.Fprintf(w, "SERVERS\t%d\n", h.Servers)
			fmt.Fprintf(w, "RUNNING\t%d\n", h.Running)
			return w.Flush()
		},
	}
}
