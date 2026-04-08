package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newServersUnsetHeaderCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unset-header <name> KEY [KEY ...]",
		Short: "Remove headers from a server",
		Long:  "Remove HTTP headers from a running server by key name. The server is restarted to pick up the changes.",
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
			if err := client.PatchServerHeaders(cmd.Context(), name, nil, keys); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Removed %d header(s) from %s\n", len(keys), name)
			return nil
		},
	}
}
