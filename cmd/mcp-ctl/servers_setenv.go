package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"mcp-gateway/internal/models"
)

func newServersSetEnvCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set-env <name> KEY=VALUE [KEY=VALUE ...]",
		Short: "Set environment variables on a server",
		Long:  "Add or update environment variables for a running server. The server is restarted to pick up the changes.",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := validateServerName(args[0])
			if err != nil {
				return err
			}

			envPairs := args[1:]
			if err := models.ValidateEnvEntries(envPairs); err != nil {
				return fmt.Errorf("invalid env entry: %w", err)
			}

			client, err := getClient(cmd)
			if err != nil {
				return err
			}
			if err := client.PatchServerEnv(cmd.Context(), name, envPairs, nil); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Set %d env var(s) on %s\n", len(envPairs), name)
			return nil
		},
	}
}
