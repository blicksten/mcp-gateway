package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"mcp-gateway/internal/models"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
)

// validateResult holds the result of a single validation step.
type validateResult struct {
	Step    string        `json:"step"`
	Passed  bool          `json:"passed"`
	Elapsed time.Duration `json:"elapsed"`
	Detail  string        `json:"detail,omitempty"`
	Error   string        `json:"error,omitempty"`
}

func newValidateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate an MCP server's stdio compliance",
		Long: `Validate that an MCP server correctly implements the stdio transport protocol.

Runs a series of checks: initialize handshake, tools/list, ping, and clean shutdown.
Reports pass/fail per step with timing.

Use --command to specify the server binary directly, or --config to extract
server config from a gateway config.json file.`,
		Annotations: map[string]string{skipClientAnnotation: "true"},
		RunE:        runValidate,
	}

	cmd.Flags().String("command", "", "Server command to run")
	cmd.Flags().StringSlice("args", nil, "Arguments for the server command")
	cmd.Flags().String("cwd", "", "Working directory for the server process")
	cmd.Flags().StringSlice("env", nil, "Environment variables (KEY=VALUE)")
	cmd.Flags().Duration("timeout", 30*time.Second, "Overall validation timeout")
	cmd.Flags().String("config", "", "Path to config.json (extracts first stdio server)")
	cmd.Flags().String("server", "", "Server name to extract from config.json (used with --config)")
	cmd.Flags().Bool("json", false, "Output results as JSON")

	return cmd
}

func runValidate(cmd *cobra.Command, _ []string) error {
	command, _ := cmd.Flags().GetString("command")
	argsFlag, _ := cmd.Flags().GetStringSlice("args")
	cwd, _ := cmd.Flags().GetString("cwd")
	envFlag, _ := cmd.Flags().GetStringSlice("env")
	timeout, _ := cmd.Flags().GetDuration("timeout")
	configPath, _ := cmd.Flags().GetString("config")
	serverName, _ := cmd.Flags().GetString("server")
	jsonOutput, _ := cmd.Flags().GetBool("json")

	// Mutual exclusion: --command and --config cannot both be set.
	if configPath != "" && command != "" {
		return fmt.Errorf("--command and --config are mutually exclusive")
	}

	// Resolve server config from --config file if provided.
	if configPath != "" {
		resolved, chosenName, err := resolveFromConfig(configPath, serverName)
		if err != nil {
			return fmt.Errorf("config extraction: %w", err)
		}
		if serverName == "" {
			fmt.Fprintf(cmd.ErrOrStderr(), "Using server %q from config\n", chosenName)
		}
		command = resolved.command
		argsFlag = resolved.args
		cwd = resolved.cwd
		envFlag = resolved.env
	}

	if command == "" {
		return fmt.Errorf("--command is required (or use --config to extract from config.json)")
	}

	// Validate env entries against dangerous keys blocklist.
	if err := models.ValidateEnvEntries(envFlag); err != nil {
		return fmt.Errorf("--env validation: %w", err)
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
	defer cancel()

	results := runValidation(ctx, command, argsFlag, cwd, envFlag)

	if jsonOutput {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(results)
	}

	// Pretty-print results.
	allPassed := true
	for _, r := range results {
		icon := "PASS"
		if !r.Passed {
			icon = "FAIL"
			allPassed = false
		}
		line := fmt.Sprintf("  [%s] %-20s %s", icon, r.Step, r.Elapsed.Truncate(time.Millisecond))
		if r.Detail != "" {
			line += "  " + r.Detail
		}
		if r.Error != "" {
			line += "  error: " + r.Error
		}
		fmt.Fprintln(cmd.OutOrStdout(), line)
	}

	if allPassed {
		fmt.Fprintln(cmd.OutOrStdout(), "\nAll checks passed.")
		return nil
	}
	return fmt.Errorf("validation failed — see results above")
}

// resolvedConfig holds fields extracted from a config.json.
type resolvedConfig struct {
	command string
	args    []string
	cwd     string
	env     []string
}

// resolveFromConfig reads a gateway config.json and extracts the specified
// server's config. If serverName is empty, uses the first stdio server.
func resolveFromConfig(path, serverName string) (*resolvedConfig, string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, "", err
	}
	if !info.Mode().IsRegular() {
		return nil, "", fmt.Errorf("config path %q is not a regular file", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}

	var cfg models.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, "", fmt.Errorf("parse config: %w", err)
	}

	if serverName != "" {
		sc, ok := cfg.Servers[serverName]
		if !ok {
			return nil, "", fmt.Errorf("server %q not found in config", serverName)
		}
		if sc.Command == "" {
			return nil, "", fmt.Errorf("server %q is not a stdio server (no command)", serverName)
		}
		return configToResolved(sc), serverName, nil
	}

	// Find first stdio server (sorted by name for deterministic selection).
	names := make([]string, 0, len(cfg.Servers))
	for name, sc := range cfg.Servers {
		if sc.Command != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil, "", fmt.Errorf("no stdio server found in config")
	}
	sort.Strings(names)
	return configToResolved(cfg.Servers[names[0]]), names[0], nil
}

func configToResolved(sc *models.ServerConfig) *resolvedConfig {
	return &resolvedConfig{
		command: sc.Command,
		args:    sc.Args,
		cwd:     sc.Cwd,
		env:     sc.Env,
	}
}

// runValidation performs the MCP validation steps and returns results.
func runValidation(ctx context.Context, command string, args []string, cwd string, env []string) []validateResult {
	var results []validateResult

	execCmd := exec.CommandContext(ctx, command, args...)
	if cwd != "" {
		execCmd.Dir = cwd
	}
	if len(env) > 0 {
		execCmd.Env = append(os.Environ(), env...)
	}

	client := mcp.NewClient(&mcp.Implementation{
		Name:    "mcp-ctl-validate",
		Version: "1.0.0",
	}, nil)

	transport := &mcp.CommandTransport{Command: execCmd}

	// Step 1: Connect (initialize handshake).
	connectCtx, connectCancel := context.WithTimeout(ctx, 10*time.Second)
	start := time.Now()
	session, err := client.Connect(connectCtx, transport, nil)
	elapsed := time.Since(start)
	connectCancel()

	if err != nil {
		killProcess(execCmd)
		results = append(results, validateResult{
			Step:    "connect",
			Passed:  false,
			Elapsed: elapsed,
			Error:   classifyConnectError(err),
		})
		return results
	}

	results = append(results, validateResult{
		Step:    "connect",
		Passed:  true,
		Elapsed: elapsed,
	})

	// Step 2: ListTools.
	listCtx, listCancel := context.WithTimeout(ctx, 5*time.Second)
	start = time.Now()
	toolsResult, err := session.ListTools(listCtx, nil)
	elapsed = time.Since(start)
	listCancel()

	if err != nil {
		results = append(results, validateResult{
			Step:    "list_tools",
			Passed:  false,
			Elapsed: elapsed,
			Error:   err.Error(),
		})
		_ = session.Close()
		killProcess(execCmd)
		return results
	}

	results = append(results, validateResult{
		Step:    "list_tools",
		Passed:  true,
		Elapsed: elapsed,
		Detail:  fmt.Sprintf("%d tools", len(toolsResult.Tools)),
	})

	// Step 3: Ping.
	pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
	start = time.Now()
	err = session.Ping(pingCtx, nil)
	elapsed = time.Since(start)
	pingCancel()

	if err != nil {
		results = append(results, validateResult{
			Step:    "ping",
			Passed:  false,
			Elapsed: elapsed,
			Error:   err.Error(),
		})
		_ = session.Close()
		killProcess(execCmd)
		return results
	}

	results = append(results, validateResult{
		Step:    "ping",
		Passed:  true,
		Elapsed: elapsed,
	})

	// Step 4: Clean shutdown.
	closeCtx, closeCancel := context.WithTimeout(ctx, 5*time.Second)
	start = time.Now()
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- session.Close()
	}()

	select {
	case err = <-closeDone:
		elapsed = time.Since(start)
	case <-closeCtx.Done():
		elapsed = time.Since(start)
		err = closeCtx.Err()
		// Kill process to unblock the Close goroutine.
		killProcess(execCmd)
		<-closeDone
	}
	closeCancel()

	if err != nil {
		results = append(results, validateResult{
			Step:    "close",
			Passed:  false,
			Elapsed: elapsed,
			Error:   err.Error(),
		})
	} else {
		results = append(results, validateResult{
			Step:    "close",
			Passed:  true,
			Elapsed: elapsed,
		})
	}

	return results
}

// killProcess sends SIGKILL to the child process if it is still running.
func killProcess(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// classifyConnectError distinguishes connection/process errors from protocol
// errors to help diagnose stdout pollution vs process failure.
func classifyConnectError(err error) string {
	msg := err.Error()
	lower := strings.ToLower(msg)

	switch {
	case strings.Contains(lower, "exec:") ||
		strings.Contains(lower, "executable file not found") ||
		strings.Contains(lower, "no such file or directory"):
		return "process error: " + msg

	case strings.Contains(lower, "exit status") ||
		strings.Contains(lower, "signal:"):
		return "process exited unexpectedly: " + msg

	case strings.Contains(lower, "context deadline exceeded") ||
		strings.Contains(lower, "context canceled"):
		return "timeout during connect: " + msg

	case strings.Contains(lower, "json") ||
		strings.Contains(lower, "unmarshal") ||
		strings.Contains(lower, "unexpected") ||
		strings.Contains(lower, "invalid character"):
		return "possible stdout pollution (non-JSON-RPC output detected): " + msg

	default:
		return "connect failed: " + msg
	}
}
