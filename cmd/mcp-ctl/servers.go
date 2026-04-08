package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func newServersCmd() *cobra.Command {
	servers := &cobra.Command{
		Use:   "servers",
		Short: "Manage backend servers",
	}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List all backend servers",
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := getClient(cmd)
			if err != nil {
				return err
			}
			svrs, err := client.ListServers(cmd.Context())
			if err != nil {
				return err
			}

			asJSON, _ := cmd.Flags().GetBool("json")
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(svrs)
			}

			if len(svrs) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No servers configured.")
				return nil
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tSTATUS\tTRANSPORT\tTOOLS\tRESTARTS")
			for _, s := range svrs {
				fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\n",
					s.Name, s.Status, s.Transport, len(s.Tools), s.RestartCount)
			}
			return w.Flush()
		},
	}
	listCmd.Flags().Bool("json", false, "Output as JSON")

	getCmd := &cobra.Command{
		Use:   "get <name>",
		Short: "Show details for a backend server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient(cmd)
			if err != nil {
				return err
			}
			sv, err := client.GetServer(cmd.Context(), args[0])
			if err != nil {
				return err
			}

			asJSON, _ := cmd.Flags().GetBool("json")
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(sv)
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintf(w, "NAME\t%s\n", sv.Name)
			fmt.Fprintf(w, "STATUS\t%s\n", sv.Status)
			fmt.Fprintf(w, "TRANSPORT\t%s\n", sv.Transport)
			if sv.PID != 0 {
				fmt.Fprintf(w, "PID\t%d\n", sv.PID)
			}
			fmt.Fprintf(w, "RESTARTS\t%d\n", sv.RestartCount)
			if sv.LastError != "" {
				fmt.Fprintf(w, "LAST ERROR\t%s\n", sv.LastError)
			}
			if len(sv.Tools) > 0 {
				names := make([]string, len(sv.Tools))
				for i, t := range sv.Tools {
					names[i] = t.Name
				}
				fmt.Fprintf(w, "TOOLS\t%s\n", strings.Join(names, ", "))
			}
			return w.Flush()
		},
	}
	getCmd.Flags().Bool("json", false, "Output as JSON")

	servers.AddCommand(listCmd)
	servers.AddCommand(getCmd)
	servers.AddCommand(newServersAddCmd())
	servers.AddCommand(newServersRemoveCmd())
	servers.AddCommand(newServersEnableCmd())
	servers.AddCommand(newServersDisableCmd())
	servers.AddCommand(newServersRestartCmd())
	servers.AddCommand(newServersResetCircuitCmd())
	servers.AddCommand(newServersSetEnvCmd())
	servers.AddCommand(newServersSetHeaderCmd())
	servers.AddCommand(newServersUnsetEnvCmd())
	servers.AddCommand(newServersUnsetHeaderCmd())
	return servers
}
