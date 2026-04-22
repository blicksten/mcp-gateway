package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// Phase 16.8 — headless install path. Mirrors the dashboard
// [Activate for Claude Code] flow for CI + power-user automation.
//
// Exit codes (documented in README.md §Exit codes):
//
//	0 — success
//	1 — user-visible error (bad flags, rejected by gateway, etc.)
//	2 — gateway not running (refuse fast; user fixes by starting daemon)
//	3 — token-drift detection failed to auto-resolve (T16.8.6)
//	4 — rollback executed after partial install failure

// Install-subcommand exit codes. `installExitUsage` and `installExitGatewayDown` reuse
// the values from main.go (`exitError = 1`, `exitUnreachable = 2`); two
// new codes cover install-specific paths.
const (
	installExitUsage          = 1
	installExitGatewayDown    = 2
	installExitDriftUnresolved = 3
	installExitRollback       = 4
)

// commandRunner abstracts exec.Command so tests can inject fakes
// without spawning real `claude` / `apply-mcp-gateway.sh`.
type commandRunner func(name string, args ...string) commandHandle

type commandHandle interface {
	Run() error
	Output() ([]byte, error)
	CombinedOutput() ([]byte, error)
}

// realCommand is the production commandRunner — thin wrapper around
// os/exec.Command.
func realCommand(name string, args ...string) commandHandle {
	return exec.Command(name, args...) // #nosec G204 — args are hard-coded or operator-provided subcommands
}

// installer is the subcommand state. Exposed for tests.
type installer struct {
	runner commandRunner
	// rollbackStack runs in LIFO order when a later step fails.
	rollbackStack []func() error
	// stdout / stderr writers for controlled output in tests.
	out *cobra.Command
}

func newInstallClaudeCodeCmd() *cobra.Command {
	var (
		mode         string
		scope        string
		noPatch      bool
		dryRun       bool
		refreshToken bool
		checkOnly    bool
	)
	cmd := &cobra.Command{
		Use:   "install-claude-code",
		Short: "Install the Claude Code integration (plugin + webview patch)",
		Long: `Headless counterpart to the VSCode dashboard [Activate for Claude Code] button.

Verifies the gateway is running, reads ~/.mcp-gateway/auth.token, installs
the mcp-gateway Claude Code plugin, regenerates .mcp.json via the gateway's
plugin-sync endpoint, and (unless --no-patch) applies the webview patch.

Use --dry-run to print the plan without making changes, --refresh-token to
re-register the plugin with the current auth.token after a gateway token
rotation (REVIEW-16 M-03), and --check-only to diagnose token drift without
side effects.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ins := &installer{
				runner: realCommand,
				out:    cmd,
			}
			return ins.run(cmd, installOpts{
				Mode:         mode,
				Scope:        scope,
				NoPatch:      noPatch,
				DryRun:       dryRun,
				RefreshToken: refreshToken,
				CheckOnly:    checkOnly,
			})
		},
	}
	cmd.Flags().StringVar(&mode, "mode", "proxy", "Plugin mode: aggregate|proxy|both")
	cmd.Flags().StringVar(&scope, "scope", "workspace", "Plugin scope: user|workspace")
	cmd.Flags().BoolVar(&noPatch, "no-patch", false, "Skip webview patch installation")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print the resulting plan without writes")
	cmd.Flags().BoolVar(&refreshToken, "refresh-token", false, "Re-register plugin with current auth.token (REVIEW-16 M-03)")
	cmd.Flags().BoolVar(&checkOnly, "check-only", false, "Check token drift without making changes (exits 0 when aligned, 3 when drift detected)")
	return cmd
}

type installOpts struct {
	Mode         string
	Scope        string
	NoPatch      bool
	DryRun       bool
	RefreshToken bool
	CheckOnly    bool
}

func (ins *installer) run(cmd *cobra.Command, opts installOpts) error {
	// Validate flags.
	switch opts.Mode {
	case "aggregate", "proxy", "both":
	default:
		return exitErr(installExitUsage, fmt.Errorf("invalid --mode %q (want aggregate|proxy|both)", opts.Mode))
	}
	switch opts.Scope {
	case "user", "workspace":
	default:
		return exitErr(installExitUsage, fmt.Errorf("invalid --scope %q (want user|workspace)", opts.Scope))
	}
	if opts.CheckOnly && opts.DryRun {
		return exitErr(installExitUsage, fmt.Errorf("--check-only and --dry-run are mutually exclusive"))
	}

	// --check-only short-circuits after drift detection.
	if opts.CheckOnly {
		return ins.runCheckOnly(cmd)
	}

	// Step 1: gateway reachable?
	apiURL, _ := cmd.Flags().GetString("api-url")
	if err := ins.pingGateway(apiURL); err != nil {
		return exitErr(installExitGatewayDown, fmt.Errorf("gateway unreachable at %s: %w", apiURL, err))
	}
	ins.logf(cmd, "✓ gateway reachable at %s", apiURL)

	// Step 2: auth token present?
	tokenPath, _ := cmd.Flags().GetString("auth-token-file")
	if tokenPath == "" {
		home, _ := os.UserHomeDir()
		tokenPath = filepath.Join(home, ".mcp-gateway", "auth.token")
	}
	tokenBytes, err := os.ReadFile(tokenPath)
	if err != nil {
		return exitErr(installExitUsage, fmt.Errorf("auth token missing at %s: %w", tokenPath, err))
	}
	authToken := strings.TrimSpace(string(tokenBytes))
	if authToken == "" {
		return exitErr(installExitUsage, fmt.Errorf("auth token file %s is empty", tokenPath))
	}
	ins.logf(cmd, "✓ auth token loaded (%d chars)", len(authToken))

	// Step 3: claude CLI available?
	if !ins.claudeCLIPresent() {
		return exitErr(installExitUsage, fmt.Errorf("claude CLI not found in PATH — install Claude Code first"))
	}
	ins.logf(cmd, "✓ claude CLI detected")

	// Plan summary for --dry-run.
	if opts.DryRun {
		ins.logf(cmd, "\n[dry-run] would execute:")
		ins.logf(cmd, "  1. claude plugin marketplace add <repo>/installer/marketplace.json (idempotent)")
		ins.logf(cmd, "  2. claude plugin install mcp-gateway@mcp-gateway-local")
		ins.logf(cmd, "  3. POST %s/api/v1/claude-code/plugin-sync", apiURL)
		if !opts.NoPatch {
			applyScript := applyScriptPath()
			ins.logf(cmd, "  4. %s --auto (GATEWAY_URL=%s, token via env)", applyScript, apiURL)
		} else {
			ins.logf(cmd, "  4. (skipped — --no-patch)")
		}
		ins.logf(cmd, "\n[dry-run] no changes made. Re-run without --dry-run to proceed.")
		return nil
	}

	if opts.RefreshToken {
		return ins.runRefreshToken(cmd, apiURL, authToken, tokenPath)
	}

	// Normal install flow.
	if err := ins.runPluginMarketplaceAdd(cmd); err != nil {
		return ins.rollbackAndExit(cmd, err)
	}
	ins.logf(cmd, "✓ plugin marketplace ensured")

	if err := ins.runPluginInstall(cmd); err != nil {
		return ins.rollbackAndExit(cmd, err)
	}
	ins.pushRollback(func() error { return ins.runPluginUninstall() })
	ins.logf(cmd, "✓ plugin installed")

	if err := ins.triggerPluginSync(apiURL, authToken); err != nil {
		return ins.rollbackAndExit(cmd, err)
	}
	ins.logf(cmd, "✓ plugin .mcp.json synced with current backends")

	if !opts.NoPatch {
		if err := ins.runApplyPatch(apiURL, authToken, false); err != nil {
			return ins.rollbackAndExit(cmd, err)
		}
		ins.logf(cmd, "✓ webview patch applied — reload VSCode to activate")
	} else {
		ins.logf(cmd, "  (patch install skipped — --no-patch)")
	}

	ins.logf(cmd, "\nNext step: open Claude Code. If you see `plugin:mcp-gateway:<backend>` entries in /mcp, you're done.")
	return nil
}

// runCheckOnly checks token drift without side effects. Exit 0 when the
// token stored in the plugin userConfig matches auth.token; exit 3 when
// drift detected and not auto-resolvable.
func (ins *installer) runCheckOnly(cmd *cobra.Command) error {
	tokenPath, _ := cmd.Flags().GetString("auth-token-file")
	if tokenPath == "" {
		home, _ := os.UserHomeDir()
		tokenPath = filepath.Join(home, ".mcp-gateway", "auth.token")
	}
	diskBytes, err := os.ReadFile(tokenPath)
	if err != nil {
		return exitErr(installExitUsage, fmt.Errorf("auth token missing at %s: %w", tokenPath, err))
	}
	diskToken := strings.TrimSpace(string(diskBytes))

	pluginToken, err := ins.readPluginStoredToken()
	if err != nil {
		// If the plugin isn't installed, there is nothing to drift against —
		// that's a different error, treat as drift-unresolved.
		return exitErr(installExitDriftUnresolved, fmt.Errorf("cannot read plugin-stored token: %w", err))
	}
	if pluginToken == diskToken {
		ins.logf(cmd, "✓ token aligned (%d chars) — no drift", len(diskToken))
		return nil
	}
	return exitErr(installExitDriftUnresolved,
		fmt.Errorf("token drift detected: auth.token (%d chars) ≠ plugin-stored (%d chars); re-run with --refresh-token to fix",
			len(diskToken), len(pluginToken)))
}

// runRefreshToken implements the REVIEW-16 M-03 re-registration flow.
func (ins *installer) runRefreshToken(cmd *cobra.Command, apiURL, authToken, _ string) error {
	ins.logf(cmd, "refreshing plugin + patch with current auth.token…")
	// Re-invoke plugin install path which re-registers the user_config.auth_token.
	if err := ins.runPluginInstall(cmd); err != nil {
		return exitErr(installExitDriftUnresolved, fmt.Errorf("plugin re-registration failed: %w", err))
	}
	if err := ins.triggerPluginSync(apiURL, authToken); err != nil {
		return exitErr(installExitDriftUnresolved, fmt.Errorf("plugin-sync failed: %w", err))
	}
	// Re-apply patch so the inlined token in index.js is refreshed.
	if err := ins.runApplyPatch(apiURL, authToken, false); err != nil {
		return exitErr(installExitDriftUnresolved, fmt.Errorf("patch re-apply failed: %w", err))
	}
	ins.logf(cmd, "✓ plugin + patch refreshed with current token")
	return nil
}

// pingGateway does a best-effort GET /api/v1/health. Short timeout so we
// don't hang on a dead daemon for the full default HTTP client timeout.
func (ins *installer) pingGateway(apiURL string) error {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(apiURL + "/api/v1/health")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// claudeCLIPresent returns true iff `claude --version` runs without error.
// Uses the injected runner so tests can force the negative case.
func (ins *installer) claudeCLIPresent() bool {
	h := ins.runner("claude", "--version")
	_, err := h.CombinedOutput()
	return err == nil
}

// runPluginMarketplaceAdd runs `claude plugin marketplace add`. Idempotent
// — the subcommand exits 0 if already added.
func (ins *installer) runPluginMarketplaceAdd(_ *cobra.Command) error {
	// The marketplace.json path is resolved at the call site — for CI use
	// it's typically the path inside the gateway repo. Operators can
	// override via $GATEWAY_MARKETPLACE_JSON. We pass a sentinel path so
	// tests can observe the call shape.
	marketplace := os.Getenv("GATEWAY_MARKETPLACE_JSON")
	if marketplace == "" {
		marketplace = "installer/marketplace.json"
	}
	h := ins.runner("claude", "plugin", "marketplace", "add", marketplace)
	out, err := h.CombinedOutput()
	if err != nil {
		return fmt.Errorf("marketplace add failed: %w (output: %s)", err, string(out))
	}
	return nil
}

func (ins *installer) runPluginInstall(_ *cobra.Command) error {
	h := ins.runner("claude", "plugin", "install", "mcp-gateway@mcp-gateway-local")
	out, err := h.CombinedOutput()
	if err != nil {
		return fmt.Errorf("plugin install failed: %w (output: %s)", err, string(out))
	}
	return nil
}

func (ins *installer) runPluginUninstall() error {
	h := ins.runner("claude", "plugin", "uninstall", "mcp-gateway")
	_, err := h.CombinedOutput()
	return err
}

// triggerPluginSync hits POST /api/v1/claude-code/plugin-sync on the running
// gateway. This is the same endpoint the dashboard [Activate] button hits.
func (ins *installer) triggerPluginSync(apiURL, authToken string) error {
	req, err := http.NewRequest(http.MethodPost, apiURL+"/api/v1/claude-code/plugin-sync", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+authToken)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		return fmt.Errorf("gateway reports plugin directory not configured (409); set GATEWAY_PLUGIN_DIR on the daemon or install via `claude plugin install` first")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("plugin-sync returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// runApplyPatch runs apply-mcp-gateway.sh (Unix) or apply-mcp-gateway.ps1
// (Windows) with --auto. When uninstall=true, adds --uninstall.
func (ins *installer) runApplyPatch(apiURL, authToken string, uninstall bool) error {
	script := applyScriptPath()
	// Check script exists before invoking so the error is actionable.
	if _, err := os.Stat(script); err != nil {
		return fmt.Errorf("apply script missing at %s — ensure the mcp-gateway installer/patches/ directory is on disk (Phase 16.4 shipping): %w",
			script, err)
	}
	var argv []string
	var name string
	if runtime.GOOS == "windows" {
		name = "powershell.exe"
		argv = []string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script, "--auto"}
	} else {
		name = "/bin/sh"
		argv = []string{script, "--auto"}
	}
	if uninstall {
		argv = append(argv, "--uninstall")
	}
	// Token passed via env so it stays out of argv / shell history.
	_ = os.Setenv("GATEWAY_URL", apiURL)
	_ = os.Setenv("GATEWAY_AUTH_TOKEN", authToken)
	h := ins.runner(name, argv...)
	out, err := h.CombinedOutput()
	if err != nil {
		return fmt.Errorf("apply script failed: %w (output: %s)", err, string(out))
	}
	return nil
}

// applyScriptPath resolves the apply script path. CI uses repo-relative;
// operators can override via $GATEWAY_APPLY_SCRIPT.
func applyScriptPath() string {
	if p := os.Getenv("GATEWAY_APPLY_SCRIPT"); p != "" {
		return p
	}
	dir := "installer/patches"
	if runtime.GOOS == "windows" {
		return filepath.Join(dir, "apply-mcp-gateway.ps1")
	}
	return filepath.Join(dir, "apply-mcp-gateway.sh")
}

// readPluginStoredToken reads the plugin's stored auth_token via
// `claude plugin list --json`. Best-effort parse.
func (ins *installer) readPluginStoredToken() (string, error) {
	h := ins.runner("claude", "plugin", "list", "--json")
	out, err := h.Output()
	if err != nil {
		return "", err
	}
	// Schema tolerance: Claude CLI output shape is documented informally.
	// Try a few known places. The test fixture mirrors this shape.
	var wrapper struct {
		Plugins []struct {
			Name       string `json:"name"`
			UserConfig struct {
				AuthToken string `json:"auth_token"`
			} `json:"user_config"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(out, &wrapper); err != nil {
		return "", fmt.Errorf("parse plugin list: %w", err)
	}
	for _, p := range wrapper.Plugins {
		if p.Name == "mcp-gateway" {
			return p.UserConfig.AuthToken, nil
		}
	}
	return "", errors.New("mcp-gateway plugin not found in claude plugin list")
}

func (ins *installer) pushRollback(fn func() error) {
	ins.rollbackStack = append(ins.rollbackStack, fn)
}

func (ins *installer) rollbackAndExit(cmd *cobra.Command, origErr error) error {
	ins.logf(cmd, "\n✗ install failed: %v — rolling back…", origErr)
	for i := len(ins.rollbackStack) - 1; i >= 0; i-- {
		if err := ins.rollbackStack[i](); err != nil {
			ins.logf(cmd, "  rollback step %d failed: %v (continuing)", i, err)
		} else {
			ins.logf(cmd, "  rollback step %d ✓", i)
		}
	}
	return exitErr(installExitRollback, origErr)
}

func (ins *installer) logf(cmd *cobra.Command, format string, args ...any) {
	fmt.Fprintf(cmd.OutOrStdout(), format+"\n", args...)
}

// exitError carries an exit code alongside the message. Cobra's default
// error handling prints to stderr and exits 1; we intercept in main.go to
// honor the code. For tests: errors.As → exitError.Code.
// installExitError carries an exit code alongside the message. Named to
// avoid collision with the `exitError = 1` const already defined in main.go.
type installExitError struct {
	Code int
	Err  error
}

func (e *installExitError) Error() string { return e.Err.Error() }
func (e *installExitError) Unwrap() error { return e.Err }

func exitErr(code int, err error) error {
	return &installExitError{Code: code, Err: err}
}

// Compile-time guard: exec.Cmd implements commandHandle through embedded
// methods. Explicit var lets the compiler catch any interface drift.
var _ commandHandle = (*exec.Cmd)(nil)
