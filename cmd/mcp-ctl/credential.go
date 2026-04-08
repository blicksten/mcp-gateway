package main

import "github.com/spf13/cobra"

func newCredentialCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "credential",
		Short: "Manage server credentials",
		Long:  "Import, list, and manage credentials for MCP Gateway servers.",
	}

	cmd.AddCommand(newCredentialImportCmd())
	cmd.AddCommand(newCredentialListCmd())

	return cmd
}
