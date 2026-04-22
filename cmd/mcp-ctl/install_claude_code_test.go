package main

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Phase 16.8 T16.8.5 — tests for the install-claude-code subcommand.
//
// Strategy: exec.Command is replaced by a fake commandRunner so tests
// never spawn real `claude` / `powershell` / `sh`. The gateway HTTP
// surface is mocked via httptest.Server.

// fakeCommand implements commandHandle. Tests set Stdout / Err per
// command-name match so a single test can simulate a sequence of
// succeed/fail subcommands.
type fakeCommand struct {
	stdout []byte
	err    error
}

func (f *fakeCommand) Run() error                        { return f.err }
func (f *fakeCommand) Output() ([]byte, error)           { return f.stdout, f.err }
func (f *fakeCommand) CombinedOutput() ([]byte, error)   { return f.stdout, f.err }

// fakeRunner records invocations + returns pre-seeded fakeCommand
// responses keyed by the concatenated argv string.
type fakeRunner struct {
	calls    [][]string
	responses map[string]*fakeCommand
	fallback *fakeCommand
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{
		responses: make(map[string]*fakeCommand),
		fallback:  &fakeCommand{}, // empty success by default
	}
}

func (fr *fakeRunner) respond(argvKey string, resp *fakeCommand) {
	fr.responses[argvKey] = resp
}

func (fr *fakeRunner) run(name string, args ...string) commandHandle {
	all := append([]string{name}, args...)
	fr.calls = append(fr.calls, all)
	key := strings.Join(all, " ")
	// Also allow prefix match for tests that don't want to match the full
	// argv (e.g. just "claude plugin install").
	for prefix, resp := range fr.responses {
		if strings.HasPrefix(key, prefix) {
			return resp
		}
	}
	return fr.fallback
}

func (fr *fakeRunner) callsFor(prefix string) int {
	n := 0
	for _, c := range fr.calls {
		if strings.HasPrefix(strings.Join(c, " "), prefix) {
			n++
		}
	}
	return n
}

// testInstaller wires a fakeRunner + mock gateway server and returns a
// ready-to-run installer. We register api-url + auth-token-file directly
// on the subcommand's Flags() (not PersistentFlags) so the defaults are
// returned from `cmd.Flags().GetString(...)` without going through the
// cobra parse pipeline. In production the root command defines these as
// persistent flags; tests bypass the root to keep the test surface small.
func testInstaller(t *testing.T, fr *fakeRunner, gatewayServer *httptest.Server) (*installer, *cobra.Command) {
	t.Helper()
	ins := &installer{runner: fr.run}
	cmd := newInstallClaudeCodeCmd()
	cmd.Flags().String("api-url", gatewayServer.URL, "")
	tokenPath := filepath.Join(t.TempDir(), "auth.token")
	require.NoError(t, os.WriteFile(tokenPath, []byte("integration-test-token"), 0o600))
	cmd.Flags().String("auth-token-file", tokenPath, "")
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(out)
	return ins, cmd
}

func mockGateway(t *testing.T, healthOK, syncOK bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/health":
			if healthOK {
				w.WriteHeader(http.StatusOK)
				fmt.Fprint(w, `{"status":"ok"}`)
			} else {
				w.WriteHeader(http.StatusServiceUnavailable)
			}
		case "/api/v1/claude-code/plugin-sync":
			if syncOK {
				w.WriteHeader(http.StatusOK)
				fmt.Fprint(w, `{"status":"synced","mcp_json_path":"/tmp/.mcp.json","entries_count":1}`)
			} else {
				w.WriteHeader(http.StatusConflict)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// extractExitCode returns the Code field from an installExitError, or -1
// if the error does not wrap one.
func extractExitCode(err error) int {
	var e *installExitError
	if errors.As(err, &e) {
		return e.Code
	}
	return -1
}

func TestInstallClaudeCode_DryRunPrintsPlanWithoutSideEffects(t *testing.T) {
	fr := newFakeRunner()
	fr.respond("claude --version", &fakeCommand{stdout: []byte("claude 1.0"), err: nil})
	srv := mockGateway(t, true, true)
	ins, cmd := testInstaller(t, fr, srv)

	err := ins.run(cmd, installOpts{Mode: "proxy", Scope: "workspace", DryRun: true})
	require.NoError(t, err)

	// Dry-run printed plan but did NOT run plugin install / apply.
	assert.Equal(t, 0, fr.callsFor("claude plugin install"), "dry-run must not install plugin")
	assert.Equal(t, 0, fr.callsFor("claude plugin marketplace"), "dry-run must not add marketplace")

	// Output contains plan markers.
	out := cmd.OutOrStdout().(*bytes.Buffer).String()
	assert.Contains(t, out, "[dry-run]")
	assert.Contains(t, out, "plugin-sync")
}

func TestInstallClaudeCode_GatewayDownReturnsExit2(t *testing.T) {
	fr := newFakeRunner()
	srv := mockGateway(t, false, false) // /health returns 503
	ins, cmd := testInstaller(t, fr, srv)

	err := ins.run(cmd, installOpts{Mode: "proxy", Scope: "workspace"})
	require.Error(t, err)
	assert.Equal(t, installExitGatewayDown, extractExitCode(err),
		"gateway-down path must exit with installExitGatewayDown=2")
}

func TestInstallClaudeCode_MissingClaudeCLIReturnsHelpfulError(t *testing.T) {
	fr := newFakeRunner()
	fr.respond("claude --version", &fakeCommand{err: errors.New("exec: claude: not found")})
	srv := mockGateway(t, true, true)
	ins, cmd := testInstaller(t, fr, srv)

	err := ins.run(cmd, installOpts{Mode: "proxy", Scope: "workspace"})
	require.Error(t, err)
	assert.Equal(t, installExitUsage, extractExitCode(err))
	assert.Contains(t, err.Error(), "claude CLI not found")
}

func TestInstallClaudeCode_EmptyAuthTokenFileIsRejected(t *testing.T) {
	fr := newFakeRunner()
	fr.respond("claude --version", &fakeCommand{})
	srv := mockGateway(t, true, true)
	_, cmd := testInstaller(t, fr, srv)

	// Overwrite the token file to empty.
	tokenPath, _ := cmd.Flags().GetString("auth-token-file")
	require.NoError(t, os.WriteFile(tokenPath, []byte("   \n"), 0o600))

	ins := &installer{runner: fr.run}
	err := ins.run(cmd, installOpts{Mode: "proxy", Scope: "workspace"})
	require.Error(t, err)
	assert.Equal(t, installExitUsage, extractExitCode(err))
	assert.Contains(t, err.Error(), "is empty")
}

func TestInstallClaudeCode_InvalidFlagsReturnUsageError(t *testing.T) {
	fr := newFakeRunner()
	srv := mockGateway(t, true, true)
	ins, cmd := testInstaller(t, fr, srv)

	err := ins.run(cmd, installOpts{Mode: "banana", Scope: "workspace"})
	require.Error(t, err)
	assert.Equal(t, installExitUsage, extractExitCode(err))
	assert.Contains(t, err.Error(), "invalid --mode")

	err = ins.run(cmd, installOpts{Mode: "proxy", Scope: "global"})
	require.Error(t, err)
	assert.Equal(t, installExitUsage, extractExitCode(err))
	assert.Contains(t, err.Error(), "invalid --scope")

	err = ins.run(cmd, installOpts{Mode: "proxy", Scope: "workspace", CheckOnly: true, DryRun: true})
	require.Error(t, err)
	assert.Equal(t, installExitUsage, extractExitCode(err))
	assert.Contains(t, err.Error(), "mutually exclusive")
}

func TestInstallClaudeCode_CheckOnly_AlignedTokenReturnsZero(t *testing.T) {
	fr := newFakeRunner()
	const token = "integration-test-token"
	pluginJSON := `{"plugins":[{"name":"mcp-gateway","user_config":{"auth_token":"` + token + `"}}]}`
	fr.respond("claude plugin list --json", &fakeCommand{stdout: []byte(pluginJSON)})
	srv := mockGateway(t, true, true)
	ins, cmd := testInstaller(t, fr, srv)

	err := ins.run(cmd, installOpts{Mode: "proxy", Scope: "workspace", CheckOnly: true})
	assert.NoError(t, err)
}

func TestInstallClaudeCode_CheckOnly_TokenDriftReturnsExit3(t *testing.T) {
	fr := newFakeRunner()
	// Plugin stored a DIFFERENT token than what's on disk.
	pluginJSON := `{"plugins":[{"name":"mcp-gateway","user_config":{"auth_token":"stale-token-v1"}}]}`
	fr.respond("claude plugin list --json", &fakeCommand{stdout: []byte(pluginJSON)})
	srv := mockGateway(t, true, true)
	ins, cmd := testInstaller(t, fr, srv)

	err := ins.run(cmd, installOpts{Mode: "proxy", Scope: "workspace", CheckOnly: true})
	require.Error(t, err)
	assert.Equal(t, installExitDriftUnresolved, extractExitCode(err))
	assert.Contains(t, err.Error(), "drift detected")
}

func TestInstallClaudeCode_CheckOnly_MissingPluginReturnsExit3(t *testing.T) {
	fr := newFakeRunner()
	// claude plugin list returns plugins[] with no mcp-gateway entry.
	fr.respond("claude plugin list --json", &fakeCommand{stdout: []byte(`{"plugins":[]}`)})
	srv := mockGateway(t, true, true)
	ins, cmd := testInstaller(t, fr, srv)

	err := ins.run(cmd, installOpts{Mode: "proxy", Scope: "workspace", CheckOnly: true})
	require.Error(t, err)
	assert.Equal(t, installExitDriftUnresolved, extractExitCode(err))
}

func TestInstallClaudeCode_PluginSyncConflictTriggersRollback(t *testing.T) {
	// Skip on Windows because the apply-script existence check short-
	// circuits on any path — but we want this test to get past the plugin-
	// sync call. Windows path with /bin/sh missing already tested elsewhere.
	if runtime.GOOS == "windows" {
		t.Skip("apply-script existence check blocks this path on Windows; covered on POSIX")
	}

	fr := newFakeRunner()
	fr.respond("claude --version", &fakeCommand{})
	fr.respond("claude plugin marketplace add", &fakeCommand{})
	fr.respond("claude plugin install", &fakeCommand{})
	fr.respond("claude plugin uninstall", &fakeCommand{}) // expected rollback step
	// /plugin-sync returns 409 — triggers rollback.
	srv := mockGateway(t, true, false)
	ins, cmd := testInstaller(t, fr, srv)

	err := ins.run(cmd, installOpts{Mode: "proxy", Scope: "workspace", NoPatch: true})
	require.Error(t, err)
	assert.Equal(t, installExitRollback, extractExitCode(err))
	assert.Equal(t, 1, fr.callsFor("claude plugin uninstall"),
		"rollback must invoke `claude plugin uninstall` once")
}

func TestInstallClaudeCode_NoPatchFlagSkipsApplyScript(t *testing.T) {
	fr := newFakeRunner()
	fr.respond("claude --version", &fakeCommand{})
	fr.respond("claude plugin marketplace add", &fakeCommand{})
	fr.respond("claude plugin install", &fakeCommand{})
	srv := mockGateway(t, true, true)
	ins, cmd := testInstaller(t, fr, srv)

	err := ins.run(cmd, installOpts{Mode: "proxy", Scope: "workspace", NoPatch: true})
	assert.NoError(t, err)

	// /bin/sh or powershell.exe must NOT be called when --no-patch is set.
	assert.Equal(t, 0, fr.callsFor("/bin/sh"))
	assert.Equal(t, 0, fr.callsFor("powershell.exe"))
}

// TestInstallClaudeCode_RunApplyPatch_ChildScopedEnv is the regression guard
// for architect-review findings A-FIN-01 + A-FIN-02 (checkpoint-finish-
// 44f45055). Previously, `runApplyPatch` mutated the PARENT process env
// via os.Setenv and used the wrong env var names. The fix puts env on
// *exec.Cmd.Env (child-scoped) and uses `MCP_GATEWAY_URL` +
// `MCP_GATEWAY_TOKEN_FILE` — the names the apply scripts actually read.
//
// This test constructs a real *exec.Cmd (not a fake) so Env assignment
// is observable, then verifies:
//   (1) the parent process env is NOT mutated by runApplyPatch
//   (2) the child's Env contains the correct MCP_GATEWAY_* variable
//       names — NOT the old GATEWAY_URL / GATEWAY_AUTH_TOKEN names
func TestInstallClaudeCode_RunApplyPatch_ChildScopedEnv(t *testing.T) {
	// Only runs when an apply script is on disk — otherwise runApplyPatch
	// short-circuits with a "missing script" error before reaching the env
	// setup. On the mcp-gateway repo the script IS committed at
	// installer/patches/apply-mcp-gateway.sh.
	scriptOverride := filepath.Join("..", "..", "installer", "patches", "apply-mcp-gateway.sh")
	if runtime.GOOS == "windows" {
		scriptOverride = filepath.Join("..", "..", "installer", "patches", "apply-mcp-gateway.ps1")
	}
	if _, err := os.Stat(scriptOverride); err != nil {
		t.Skipf("apply script not on disk at %s — skipping child-env regression guard", scriptOverride)
	}
	t.Setenv("GATEWAY_APPLY_SCRIPT", scriptOverride)

	// Snapshot parent env BEFORE calling runApplyPatch. A-FIN-01 asserts
	// these two variables are NOT set in the parent after the call.
	parentBefore := map[string]string{
		"MCP_GATEWAY_URL":        os.Getenv("MCP_GATEWAY_URL"),
		"MCP_GATEWAY_TOKEN_FILE": os.Getenv("MCP_GATEWAY_TOKEN_FILE"),
		"GATEWAY_URL":            os.Getenv("GATEWAY_URL"),
		"GATEWAY_AUTH_TOKEN":     os.Getenv("GATEWAY_AUTH_TOKEN"),
	}

	// Custom runner that captures the *exec.Cmd pointer so the test can
	// inspect .Env AFTER runApplyPatch returns (runApplyPatch sets the env
	// between runner return and CombinedOutput invocation).
	var capturedCmd *exec.Cmd
	capturingRunner := func(name string, args ...string) commandHandle {
		cmd := exec.Command(name, args...) // #nosec G204 — test-only, args come from runApplyPatch
		capturedCmd = cmd
		return cmd
	}
	ins := &installer{runner: capturingRunner}
	// We don't care whether the underlying shell succeeds — the script
	// may reject our fake token path — we only care about what Env landed
	// on the Cmd. The call returns an error on non-zero exit, which we
	// intentionally ignore.
	_ = ins.runApplyPatch("http://127.0.0.1:8765", "/tmp/fake-token-path", false)
	var capturedEnv []string
	if capturedCmd != nil {
		capturedEnv = capturedCmd.Env
	}

	// A-FIN-01: parent env untouched.
	for k, want := range parentBefore {
		got := os.Getenv(k)
		assert.Equal(t, want, got,
			"parent env var %s must not be mutated by runApplyPatch (A-FIN-01 regression)", k)
	}

	// A-FIN-02: child env carries the CORRECT names pointing at the
	// CLI-provided values, and NOT the old wrong names.
	if capturedEnv == nil {
		t.Skip("runner wasn't invoked (script existence check may have short-circuited)")
	}
	envMap := make(map[string]string, len(capturedEnv))
	for _, kv := range capturedEnv {
		if idx := strings.Index(kv, "="); idx > 0 {
			envMap[kv[:idx]] = kv[idx+1:]
		}
	}
	assert.Equal(t, "http://127.0.0.1:8765", envMap["MCP_GATEWAY_URL"],
		"child env must carry MCP_GATEWAY_URL — the var name apply-mcp-gateway.sh reads (A-FIN-02 regression)")
	assert.Equal(t, "/tmp/fake-token-path", envMap["MCP_GATEWAY_TOKEN_FILE"],
		"child env must carry MCP_GATEWAY_TOKEN_FILE as a PATH, not the old GATEWAY_AUTH_TOKEN name (A-FIN-02 regression)")
	_, hasOldURL := envMap["GATEWAY_URL"]
	_, hasOldToken := envMap["GATEWAY_AUTH_TOKEN"]
	// The old names are allowed IF they already existed in the parent env
	// (via os.Environ() in the child); the test's parentBefore guard
	// ensures we didn't set them. So in a clean test env they must be absent.
	if parentBefore["GATEWAY_URL"] == "" {
		assert.False(t, hasOldURL, "stale GATEWAY_URL must not leak into child env")
	}
	if parentBefore["GATEWAY_AUTH_TOKEN"] == "" {
		assert.False(t, hasOldToken, "stale GATEWAY_AUTH_TOKEN must not leak into child env")
	}
}

func TestInstallClaudeCode_ApplyScriptPath_WindowsVariant(t *testing.T) {
	// Cross-check the helper function; it ignores runtime.GOOS indirectly
	// via the env var override pathway, so we test via GATEWAY_APPLY_SCRIPT.
	t.Setenv("GATEWAY_APPLY_SCRIPT", "/custom/apply.sh")
	assert.Equal(t, "/custom/apply.sh", applyScriptPath())
	t.Setenv("GATEWAY_APPLY_SCRIPT", "")
	// Unset via re-Setenv; check that the default ends with platform suffix.
	got := applyScriptPath()
	if runtime.GOOS == "windows" {
		assert.True(t, strings.HasSuffix(got, "apply-mcp-gateway.ps1"), "got: %s", got)
	} else {
		assert.True(t, strings.HasSuffix(got, "apply-mcp-gateway.sh"), "got: %s", got)
	}
}
