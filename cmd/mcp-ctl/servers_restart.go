package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newServersRestartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restart <name>",
		Short: "Restart a backend server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := validateServerName(args[0])
			if err != nil {
				return err
			}
			client, err := getClient(cmd)
			if err != nil {
				return err
			}
			if err := client.RestartServer(cmd.Context(), name); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Server %q restarted.\n", name)
			return nil
		},
	}
}
