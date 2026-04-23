// Package main implements the mcp-ctl CLI for managing the MCP Gateway.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"mcp-gateway/internal/auth"
	"mcp-gateway/internal/config"
	"mcp-gateway/internal/ctlclient"

	"github.com/spf13/cobra"
)

// Overridden by ldflags at link time; source values are dev-build fallbacks.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

const (
	defaultAPIURL = "http://127.0.0.1:8765"
	envAPIURL     = "MCP_GATEWAY_URL"
)

// Exit codes.
const (
	exitOK          = 0
	exitError       = 1
	exitUnreachable = 2
)

// contextKey is used to store values in cobra command context.
type contextKey string

const clientKey contextKey = "client"
const envMapKey contextKey = "envMap"

// skipClientAnnotation marks commands that do not need a gateway client connection.
const skipClientAnnotation = "skipClient"

// newRootCmd builds a fresh command tree. Used by main() and tests.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "mcp-ctl",
		Short: "CLI for the MCP Gateway",
		Long:  "mcp-ctl manages MCP Gateway servers, tools, and logs via the REST API.",
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if cmd.Annotations[skipClientAnnotation] == "true" {
				return nil
			}
			apiURL, _ := cmd.Flags().GetString("api-url")

			// Token path: --auth-token-file flag > default ~/.mcp-gateway/auth.token.
			tokenPath, _ := cmd.Flags().GetString("auth-token-file")
			if tokenPath == "" {
				if dir, err := config.DefaultConfigDir(); err == nil {
					tokenPath = filepath.Join(dir, "auth.token")
				}
			}
			// Per-request provider: env wins over file; missing both → clear
			// error with actionable message.
			provider := func() (string, error) {
				header, err := auth.BuildHeader(tokenPath)
				if err != nil {
					return "", fmt.Errorf(
						"%w\n  hint: the daemon writes this file on first start; "+
							"if you are running mcp-ctl against a remote daemon, "+
							"copy the file or set the env var",
						err)
				}
				return header, nil
			}
			client := ctlclient.NewAuthed(apiURL, provider)
			ctx := setClient(cmd.Context(), client)

			// Load env file for ${VAR} expansion in CLI values.
			envFile, _ := cmd.Flags().GetString("env-file")
			if envFile == "" {
				envFile = os.Getenv("MCP_GATEWAY_ENV_FILE")
			}
			envMap, err := config.LoadEnvFile(envFile)
			if err != nil {
				return fmt.Errorf("load env file: %w", err)
			}
			ctx = setEnvMap(ctx, envMap)

			cmd.SetContext(ctx)
			return nil
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	// Determine default: env var takes precedence over hardcoded default.
	def := defaultAPIURL
	if envVal := os.Getenv(envAPIURL); envVal != "" {
		def = envVal
	}
	root.PersistentFlags().String("api-url", def, "Gateway API URL (env: MCP_GATEWAY_URL)")
	root.PersistentFlags().String("env-file", "", "Path to .env file for variable expansion (env: MCP_GATEWAY_ENV_FILE)")
	root.PersistentFlags().String("auth-token-file", "", "Path to Bearer auth token file (default ~/.mcp-gateway/auth.token; env: "+auth.EnvVarName+" overrides)")

	// Register subcommands.
	root.AddCommand(newHealthCmd())
	root.AddCommand(newDaemonCmd())
	root.AddCommand(newServersCmd())
	root.AddCommand(newToolsCmd())
	root.AddCommand(newLogsCmd())
	root.AddCommand(newCredentialCmd())

	root.AddCommand(newValidateCmd())

	// Phase 16.8 — Claude Code integration bootstrap.
	installCCCmd := newInstallClaudeCodeCmd()
	installCCCmd.Annotations = map[string]string{skipClientAnnotation: "true"}
	root.AddCommand(installCCCmd)

	versionCmd := newVersionCmd()
	versionCmd.Annotations = map[string]string{skipClientAnnotation: "true"}
	root.AddCommand(versionCmd)

	// Force early registration of Cobra's auto-generated completion command
	// so we can annotate it before Execute() runs.
	root.InitDefaultCompletionCmd()
	if completionCmd, _, err := root.Find([]string{"completion"}); err == nil && completionCmd.Name() == "completion" {
		completionCmd.Annotations = map[string]string{skipClientAnnotation: "true"}
	}

	return root
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(exitCode(err))
	}
}

// exitCode maps error types to exit codes.
func exitCode(err error) int {
	if err == nil {
		return exitOK
	}
	var connErr *ctlclient.ConnectionError
	if errors.As(err, &connErr) {
		return exitUnreachable
	}
	return exitError
}

// setClient stores the ctlclient in the context.
func setClient(ctx context.Context, c *ctlclient.Client) context.Context {
	return context.WithValue(ctx, clientKey, c)
}

// setEnvMap stores the env expansion map in the context.
func setEnvMap(ctx context.Context, m map[string]string) context.Context {
	return context.WithValue(ctx, envMapKey, m)
}

// getEnvMap retrieves the env expansion map from the command context.
func getEnvMap(cmd *cobra.Command) map[string]string {
	if m, ok := cmd.Context().Value(envMapKey).(map[string]string); ok {
		return m
	}
	return nil
}

// getClient retrieves the ctlclient from the command context.
// Returns an error if the client is not available (e.g. PersistentPreRunE was skipped).
func getClient(cmd *cobra.Command) (*ctlclient.Client, error) {
	ctx := cmd.Context()
	if ctx == nil {
		return nil, fmt.Errorf("command has no context — was PersistentPreRunE skipped?")
	}
	c, ok := ctx.Value(clientKey).(*ctlclient.Client)
	if !ok || c == nil {
		return nil, fmt.Errorf("client not in context — PersistentPreRunE did not run")
	}
	return c, nil
}
