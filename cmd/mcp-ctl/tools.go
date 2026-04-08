package main

import (
	"encoding/json"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func newToolsCmd() *cobra.Command {
	tools := &cobra.Command{
		Use:   "tools",
		Short: "Manage tools exposed by backend servers",
	}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List all available tools",
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := getClient(cmd)
			if err != nil {
				return err
			}
			allTools, err := client.ListTools(cmd.Context())
			if err != nil {
				return err
			}

			serverFilter, _ := cmd.Flags().GetString("server")
			if serverFilter != "" {
				filtered := allTools[:0]
				for _, t := range allTools {
					if t.Server == serverFilter {
						filtered = append(filtered, t)
					}
				}
				allTools = filtered
			}

			asJSON, _ := cmd.Flags().GetBool("json")
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(allTools)
			}

			if len(allTools) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No tools available.")
				return nil
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tSERVER\tDESCRIPTION")
			for _, t := range allTools {
				fmt.Fprintf(w, "%s\t%s\t%s\n", t.Name, t.Server, t.Description)
			}
			return w.Flush()
		},
	}
	listCmd.Flags().String("server", "", "Filter tools by backend server name (client-side)")
	listCmd.Flags().Bool("json", false, "Output as JSON")

	tools.AddCommand(listCmd)
	tools.AddCommand(newToolsCallCmd())
	return tools
}
