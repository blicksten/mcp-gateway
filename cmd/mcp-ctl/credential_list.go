package main

import (
	"encoding/json"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func newCredentialListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List credential keys for servers",
		Long: `List environment variable keys and header keys configured on gateway servers.
Values are never shown — only key names are listed (masked as ********).

Without --server: lists all servers that have env vars or headers configured.
With --server: lists keys for that specific server only.`,
		RunE: runCredentialList,
	}

	cmd.Flags().String("server", "", "Show credentials for a specific server only")
	cmd.Flags().Bool("json", false, "Output as JSON")

	return cmd
}

// credentialEntry is used for JSON output of credential list.
type credentialEntry struct {
	Server     string   `json:"server"`
	EnvKeys    []string `json:"env_keys"`
	HeaderKeys []string `json:"header_keys"`
}

func runCredentialList(cmd *cobra.Command, _ []string) error {
	client, err := getClient(cmd)
	if err != nil {
		return err
	}

	serverFilter, _ := cmd.Flags().GetString("server")
	asJSON, _ := cmd.Flags().GetBool("json")

	if serverFilter != "" {
		serverFilter, err = validateServerName(serverFilter)
		if err != nil {
			return err
		}
		sv, err := client.GetServer(cmd.Context(), serverFilter)
		if err != nil {
			return err
		}
		if len(sv.EnvKeys) == 0 && len(sv.HeaderKeys) == 0 {
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode([]credentialEntry{})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "No credentials configured for server %q.\n", serverFilter)
			return nil
		}
		if asJSON {
			entry := credentialEntry{
				Server:     sv.Name,
				EnvKeys:    sv.EnvKeys,
				HeaderKeys: sv.HeaderKeys,
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode([]credentialEntry{entry})
		}
		return printCredentialTable(cmd, sv.Name, sv.EnvKeys, sv.HeaderKeys)
	}

	// List all servers.
	servers, err := client.ListServers(cmd.Context())
	if err != nil {
		return err
	}

	var entries []credentialEntry
	for _, sv := range servers {
		if len(sv.EnvKeys) > 0 || len(sv.HeaderKeys) > 0 {
			entries = append(entries, credentialEntry{
				Server:     sv.Name,
				EnvKeys:    sv.EnvKeys,
				HeaderKeys: sv.HeaderKeys,
			})
		}
	}

	if asJSON {
		if entries == nil {
			entries = []credentialEntry{}
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(entries)
	}

	if len(entries) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No servers have credentials configured.")
		return nil
	}

	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "SERVER\tTYPE\tKEY\tVALUE")
	for _, e := range entries {
		for _, k := range e.EnvKeys {
			fmt.Fprintf(w, "%s\tenv\t%s\t********\n", e.Server, k)
		}
		for _, k := range e.HeaderKeys {
			fmt.Fprintf(w, "%s\theader\t%s\t********\n", e.Server, k)
		}
	}
	return w.Flush()
}

func printCredentialTable(cmd *cobra.Command, server string, envKeys, headerKeys []string) error {
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "SERVER\tTYPE\tKEY\tVALUE")
	for _, k := range envKeys {
		fmt.Fprintf(w, "%s\tenv\t%s\t********\n", server, k)
	}
	for _, k := range headerKeys {
		fmt.Fprintf(w, "%s\theader\t%s\t********\n", server, k)
	}
	return w.Flush()
}
