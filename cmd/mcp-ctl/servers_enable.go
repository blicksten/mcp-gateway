package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newServersEnableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "enable <name>",
		Short: "Enable a disabled backend server",
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
			if err := client.PatchServer(cmd.Context(), name, map[string]any{"disabled": false}); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Server %q enabled.\n", name)
			return nil
		},
	}
}
