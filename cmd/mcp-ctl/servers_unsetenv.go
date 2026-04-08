package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newServersUnsetEnvCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unset-env <name> KEY [KEY ...]",
		Short: "Remove environment variables from a server",
		Long:  "Remove environment variables from a running server by key name. The server is restarted to pick up the changes.",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := validateServerName(args[0])
			if err != nil {
				return err
			}

			keys := args[1:]
			client, err := getClient(cmd)
			if err != nil {
				return err
			}
			if err := client.PatchServerEnv(cmd.Context(), name, nil, keys); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Removed %d env key(s) from %s\n", len(keys), name)
			return nil
		},
	}
}
