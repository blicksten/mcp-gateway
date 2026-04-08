package main

import (
	"fmt"
	"strings"

	"mcp-gateway/internal/config"
	"mcp-gateway/internal/ctlclient"

	"github.com/spf13/cobra"
)

func newServersAddCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add a new backend server",
		Long: `Add a new backend server to the gateway.

Specify either --command (for stdio transport) or --url (for HTTP/SSE transport).
Both cannot be specified at the same time.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := validateServerName(args[0])
			if err != nil {
				return err
			}

			command, _ := cmd.Flags().GetString("command")
			urlFlag, _ := cmd.Flags().GetString("url")

			if command == "" && urlFlag == "" {
				return fmt.Errorf("either --command or --url is required")
			}
			if command != "" && urlFlag != "" {
				return fmt.Errorf("--command and --url cannot both be specified")
			}

			// Resolve ${VAR} in all CLI values using env file (if loaded).
			// The CLI sends resolved (final) values — the daemon receives concrete
			// values, not ${VAR} templates. No double-expansion risk because the
			// daemon only expands values read from config.json, not API-submitted values.
			envMap := getEnvMap(cmd)

			if envMap != nil {
				command = config.ExpandVar(command, envMap)
				urlFlag = config.ExpandVar(urlFlag, envMap)
			}

			cfg := ctlclient.ServerConfig{
				Command: command,
				URL:     urlFlag,
			}

			if argsFlag, _ := cmd.Flags().GetStringSlice("args"); len(argsFlag) > 0 {
				if envMap != nil {
					for i, a := range argsFlag {
						argsFlag[i] = config.ExpandVar(a, envMap)
					}
				}
				cfg.Args = argsFlag
			}
			if cwd, _ := cmd.Flags().GetString("cwd"); cwd != "" {
				if envMap != nil {
					cwd = config.ExpandVar(cwd, envMap)
				}
				cfg.Cwd = cwd
			}

			if envFlags, _ := cmd.Flags().GetStringSlice("env"); len(envFlags) > 0 {
				for i, e := range envFlags {
					key, val, ok := strings.Cut(e, "=")
					if !ok || key == "" {
						return fmt.Errorf("--env value %q must be in KEY=VALUE format", e)
					}
					if envMap != nil {
						envFlags[i] = key + "=" + config.ExpandVar(val, envMap)
					}
				}
				cfg.Env = envFlags
			}

			if hdrFlags, _ := cmd.Flags().GetStringArray("headers"); len(hdrFlags) > 0 {
				hdrs := make(map[string]string, len(hdrFlags))
				for _, h := range hdrFlags {
					key, val, ok := strings.Cut(h, "=")
					if !ok || key == "" {
						return fmt.Errorf("--headers value %q must be in KEY=VALUE format", h)
					}
					if strings.ContainsAny(key, "\r\n\x00") {
						return fmt.Errorf("--headers key %q contains illegal characters", key)
					}
					if envMap != nil {
						val = config.ExpandVar(val, envMap)
					}
					if strings.ContainsAny(val, "\r\n\x00") {
						return fmt.Errorf("--headers value for key %q contains illegal characters", key)
					}
					hdrs[key] = val
				}
				cfg.Headers = hdrs
			}

			client, err := getClient(cmd)
			if err != nil {
				return err
			}
			if err := client.AddServer(cmd.Context(), name, cfg); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Server %q added.\n", name)
			return nil
		},
	}

	cmd.Flags().String("command", "", "Command to spawn (stdio transport)")
	cmd.Flags().StringSlice("args", nil, "Command arguments (comma-separated)")
	cmd.Flags().String("cwd", "", "Working directory for the command")
	cmd.Flags().StringSlice("env", nil, "Environment variables (KEY=VALUE, comma-separated)")
	cmd.Flags().String("url", "", "URL for HTTP/SSE transport")
	cmd.Flags().StringArray("headers", nil, "HTTP headers (KEY=VALUE, repeatable)")

	return cmd
}
