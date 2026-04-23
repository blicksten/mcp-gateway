package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"mcp-gateway/internal/ctlclient"
	"mcp-gateway/internal/pidfile"

	"github.com/spf13/cobra"
)

// spawnFunc is the spawn hook used in production and overridden in tests.
// Access goes through getSpawnFunc / setSpawnFunc so concurrent test
// overrides (including future t.Parallel callers) cannot race on assignment
// (CV-MEDIUM fix).
var (
	spawnFuncMu sync.RWMutex
	spawnFunc   = func(cmd *exec.Cmd) error {
		return spawnDetached(cmd)
	}
)

func getSpawnFunc() func(*exec.Cmd) error {
	spawnFuncMu.RLock()
	defer spawnFuncMu.RUnlock()
	return spawnFunc
}

// setSpawnFunc replaces the package-level spawn hook and returns a restore
// function suitable for t.Cleanup. Always call the restore function (via
// t.Cleanup) even when tests don't run in parallel — the locking ensures
// safety if t.Parallel is added later.
func setSpawnFunc(fn func(*exec.Cmd) error) func() {
	spawnFuncMu.Lock()
	prev := spawnFunc
	spawnFunc = fn
	spawnFuncMu.Unlock()
	return func() {
		spawnFuncMu.Lock()
		spawnFunc = prev
		spawnFuncMu.Unlock()
	}
}

func newDaemonStartCmd() *cobra.Command {
	var daemonPath string
	var wait time.Duration

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the mcp-gateway daemon",
		RunE: func(cmd *cobra.Command, _ []string) error {
			apiURL, _ := cmd.Flags().GetString("api-url")
			return runDaemonStart(cmd.Context(), cmd, apiURL, daemonPath, wait)
		},
	}

	// skipClient: daemon start cannot require an existing daemon connection.
	cmd.Annotations = map[string]string{skipClientAnnotation: "true"}

	cmd.Flags().StringVar(&daemonPath, "daemon-path", "",
		"Path to mcp-gateway binary (env: MCP_GATEWAY_BIN, then PATH)")
	cmd.Flags().DurationVar(&wait, "wait", 10*time.Second,
		"How long to wait for the daemon to become reachable after spawn")
	return cmd
}

// runDaemonStart contains the testable start logic.
func runDaemonStart(ctx context.Context, cmd *cobra.Command, apiURL, daemonPath string, wait time.Duration) error {
	healthURL := apiURL + "/api/v1/health"

	// Fast path: daemon already running.
	if pidfile.IsLive(healthURL) {
		pidStr, ver := quickHealthInfo(ctx, apiURL)
		fmt.Fprintf(cmd.OutOrStdout(), "daemon already running (pid=%s, version=%s)\n", pidStr, ver)
		return nil
	}

	// Resolve binary path.
	bin, err := resolveDaemonBin(daemonPath)
	if err != nil {
		return err
	}

	// Spawn detached.
	child := exec.Command(bin) // #nosec G204 — path resolved + validated by caller
	if err := getSpawnFunc()(child); err != nil {
		return fmt.Errorf("spawn daemon: %w", err)
	}

	// Poll until /health is reachable.
	deadline := time.Now().Add(wait)
	if !pollUntilLive(ctx, healthURL, deadline) {
		// Kill the spawned process if we can reach it.
		if child.Process != nil {
			_ = child.Process.Kill()
		}
		return fmt.Errorf("daemon did not become reachable within %s", wait)
	}

	pidStr, ver := quickHealthInfo(ctx, apiURL)
	fmt.Fprintf(cmd.OutOrStdout(), "daemon started (pid=%s, version=%s)\n", pidStr, ver)
	return nil
}

// resolveDaemonBin returns the path to the mcp-gateway executable.
// Priority: explicit --daemon-path > MCP_GATEWAY_BIN env > PATH lookup.
func resolveDaemonBin(explicit string) (string, error) {
	if explicit != "" {
		if err := validateExecutable(explicit); err != nil {
			return "", fmt.Errorf("--daemon-path: %w", err)
		}
		return explicit, nil
	}
	if envBin := os.Getenv("MCP_GATEWAY_BIN"); envBin != "" {
		if err := validateExecutable(envBin); err != nil {
			return "", fmt.Errorf("MCP_GATEWAY_BIN: %w", err)
		}
		return envBin, nil
	}
	bin, err := exec.LookPath("mcp-gateway")
	if err != nil {
		return "", fmt.Errorf("mcp-gateway not found in PATH; set --daemon-path or MCP_GATEWAY_BIN")
	}
	return bin, nil
}

// validateExecutable checks that path exists and is executable.
func validateExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %q: %w", path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("%q is a directory, not an executable", path)
	}
	return nil
}

// quickHealthInfo fetches /health and returns the pid + version strings.
// Uses an unauthenticated client — this is a best-effort display call only.
// Returns "?" strings on error.
func quickHealthInfo(ctx context.Context, apiURL string) (string, string) {
	if ctx == nil {
		ctx = context.Background()
	}
	h, err := ctlclient.New(apiURL).Health(ctx)
	if err != nil {
		return "?", "?"
	}
	pid := fmt.Sprintf("%d", h.PID)
	if h.PID == 0 {
		pid = "?"
	}
	ver := h.Version
	if ver == "" {
		ver = "?"
	}
	return pid, ver
}
