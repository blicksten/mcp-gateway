package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func newToolsCallCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "call <server__tool> [--arg key=value ...]",
		Short: "Call a tool on a backend server",
		Long: `Call a tool using its namespaced name (server__tool).

The tool name must include the server prefix separated by double underscores.
Arguments are passed as key=value pairs via repeated --arg flags.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fullName := args[0]
			server, tool, err := parseToolName(fullName)
			if err != nil {
				return err
			}

			argPairs, _ := cmd.Flags().GetStringArray("arg")
			toolArgs, err := parseKeyValueArgs(argPairs)
			if err != nil {
				return err
			}

			client, err := getClient(cmd)
			if err != nil {
				return err
			}
			result, err := client.CallTool(cmd.Context(), server, tool, toolArgs)
			if err != nil {
				return err
			}

			asJSON, _ := cmd.Flags().GetBool("json")
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			}

			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(result)
		},
	}

	cmd.Flags().StringArray("arg", nil, "Tool argument as key=value (repeatable)")
	cmd.Flags().Bool("json", false, "Output as compact JSON (default is indented)")

	return cmd
}

// parseToolName splits a namespaced tool name "server__tool" into its components.
func parseToolName(fullName string) (server, tool string, err error) {
	parts := strings.SplitN(fullName, "__", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid tool name %q: expected format server__tool", fullName)
	}
	return parts[0], parts[1], nil
}

// parseKeyValueArgs converts ["key=value", ...] into a map.
func parseKeyValueArgs(pairs []string) (map[string]any, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	result := make(map[string]any, len(pairs))
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("invalid argument %q: expected key=value", p)
		}
		result[k] = v
	}
	return result, nil
}
