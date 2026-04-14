package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"mcp-gateway/internal/models"
)

func newServersSetHeaderCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set-header <name> KEY VALUE",
		Short: "Set a header on a server",
		Long:  "Add or update an HTTP header for a running server. The server is restarted to pick up the change.",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := validateServerName(args[0])
			if err != nil {
				return err
			}

			key := args[1]
			value := args[2]

			headers := map[string]string{key: value}
			if err := models.ValidateHeaderEntries(headers); err != nil {
				return fmt.Errorf("invalid header: %w", err)
			}

			client, err := getClient(cmd)
			if err != nil {
				return err
			}
			if err := client.PatchServerHeaders(cmd.Context(), name, headers, nil); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Set header %s on %s\n", key, name)
			return nil
		},
	}
}
