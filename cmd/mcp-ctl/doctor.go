package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// patchMarkerPrefix is the comment string the apply scripts
// (installer/patches/apply-mcp-gateway.{sh,ps1}) prepend at the top of
// the patched webview/index.js. Single source of truth so detection
// here and emission there can never drift.
const patchMarkerPrefix = "MCP Gateway Patch v"

// Phase 7 — `mcp-ctl doctor` (closes B-16). Runs the same diagnostic
// checks the dashboard's Claude Code Integration panel performs but
// from a single CLI command, so operators can stitch together typical
// failure modes without manually invoking five subcommands.
//
// Checks (in order, each independent):
//   1. Gateway reachable     — GET /api/v1/health (3s timeout)
//   2. Auth token present    — file exists + non-empty
//   3. Claude CLI available  — `claude --version` succeeds
//   4. Plugin installed      — `claude plugin list --json` shows mcp-gateway
//   5. Webview patch applied — anthropic.claude-code-*/webview/index.js
//                              contains "MCP Gateway Patch v" marker
//
// Exit codes:
//   0 — every check passed
//   1 — one or more checks failed (full list still printed)

type checkStatus int

const (
	checkPass checkStatus = iota
	checkFail
	checkSkip
)

func (s checkStatus) symbol(noColor bool) string {
	switch s {
	case checkPass:
		if noColor {
			return "PASS"
		}
		return "\033[32m✔ PASS\033[0m"
	case checkFail:
		if noColor {
			return "FAIL"
		}
		return "\033[31m✘ FAIL\033[0m"
	case checkSkip:
		if noColor {
			return "SKIP"
		}
		return "\033[33m• SKIP\033[0m"
	}
	return "?"
}

// checkResult captures one diagnostic outcome.
type checkResult struct {
	name   string
	status checkStatus
	detail string
	hint   string // shown on FAIL/SKIP only
}

// doctor holds the checks' shared dependencies. The runner field lets
// tests inject a fakeRunner for `claude` calls without spawning real
// subprocesses; httpClient is overridable so tests can hit an
// httptest.Server or simulate timeout.
type doctor struct {
	runner          commandRunner
	httpClient      *http.Client
	extensionsDir   string // override via $GATEWAY_VSCODE_EXTENSIONS_DIR for tests
	apiURL          string
	tokenPath       string
	authToken       string // resolved from tokenPath at run() entry; empty when token file absent
	pluginInstalled bool   // set by the plugin-list check, consumed by hint text in patch check
}

// newDoctorCmd registers the `mcp-ctl doctor` subcommand.
//
// Annotated `skipClient = true` because doctor handles its own HTTP +
// auth wiring per check (PersistentPreRunE would short-circuit on
// missing token, but we want doctor to *report* on a missing token, not
// fail at boot).
func newDoctorCmd() *cobra.Command {
	var noColor bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Run diagnostic checks (gateway, auth, plugin, patch)",
		Long: `Runs five diagnostic checks against the local environment:

  1. gateway reachable     — REST GET /api/v1/health
  2. auth token present    — file readable + non-empty
  3. claude CLI available  — claude --version succeeds
  4. plugin installed      — claude plugin list --json contains mcp-gateway
  5. webview patch applied — anthropic.claude-code-*/webview/index.js patched

Exits 0 when every check passes; 1 if one or more fail. Each failure
includes a remediation hint pointing at the fix command.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			doc := &doctor{
				runner:     realCommand,
				httpClient: &http.Client{Timeout: 3 * time.Second},
			}
			results := doc.run(cmd)
			out := cmd.OutOrStdout()
			renderResults(out, results, noColor || !isTerminal(out))
			if anyFailed(results) {
				return exitErr(installExitUsage, fmt.Errorf("one or more diagnostic checks failed"))
			}
			return nil
		},
	}
	cmd.Annotations = map[string]string{skipClientAnnotation: "true"}
	cmd.Flags().BoolVar(&noColor, "no-color", false, "Disable ANSI color codes (auto-disabled when stdout is not a TTY)")
	return cmd
}

// run executes the five checks in order and returns the result slice.
// Side-effect-free aside from network + filesystem reads — never
// mutates extension or plugin state.
func (d *doctor) run(cmd *cobra.Command) []checkResult {
	d.apiURL, _ = cmd.Flags().GetString("api-url")
	d.tokenPath, _ = cmd.Flags().GetString("auth-token-file")
	if d.tokenPath == "" {
		if home, err := os.UserHomeDir(); err == nil {
			d.tokenPath = filepath.Join(home, ".mcp-gateway", "auth.token")
		}
	}

	results := make([]checkResult, 0, 5)
	results = append(results, d.checkGateway())
	results = append(results, d.checkAuthToken())
	results = append(results, d.checkClaudeCLI())
	results = append(results, d.checkPluginInstalled())
	results = append(results, d.checkPatchApplied())
	return results
}

// checkGateway pings GET /api/v1/health. Reports the URL on either
// branch so the operator can verify they're hitting the expected
// daemon.
func (d *doctor) checkGateway() checkResult {
	r := checkResult{name: "gateway reachable"}
	if d.apiURL == "" {
		r.status = checkFail
		r.detail = "no --api-url resolved"
		r.hint = "set MCP_GATEWAY_URL or pass --api-url"
		return r
	}
	resp, err := d.httpClient.Get(d.apiURL + "/api/v1/health")
	if err != nil {
		r.status = checkFail
		r.detail = fmt.Sprintf("%s — %v", d.apiURL, err)
		r.hint = "start the daemon: `mcp-ctl daemon start`"
		return r
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode == http.StatusOK {
		r.status = checkPass
		r.detail = fmt.Sprintf("%s (HTTP 200)", d.apiURL)
		return r
	}
	r.status = checkFail
	r.detail = fmt.Sprintf("%s — HTTP %d", d.apiURL, resp.StatusCode)
	if resp.StatusCode == http.StatusUnauthorized {
		r.hint = "auth token rejected — run `mcp-ctl install-claude-code --refresh-token`"
	} else {
		r.hint = "check daemon logs: `mcp-ctl logs --tail 50`"
	}
	return r
}

// checkAuthToken reads d.tokenPath and confirms non-empty contents.
// Stores the token in d.authToken for downstream checks (currently
// unused but kept symmetric with installer flow — future authed checks
// against e.g. /api/v1/servers can reuse it without re-reading the
// file).
func (d *doctor) checkAuthToken() checkResult {
	r := checkResult{name: "auth token"}
	if d.tokenPath == "" {
		r.status = checkFail
		r.detail = "could not resolve token path"
		r.hint = "pass --auth-token-file or ensure $HOME is set"
		return r
	}
	raw, err := os.ReadFile(d.tokenPath) // #nosec G304 — path resolved from user flag / default ~/.mcp-gateway/auth.token
	if err != nil {
		r.status = checkFail
		r.detail = fmt.Sprintf("%s — %v", d.tokenPath, err)
		r.hint = "the daemon writes this file on first start; if running against a remote daemon, copy it locally"
		return r
	}
	tok := strings.TrimSpace(string(raw))
	if tok == "" {
		r.status = checkFail
		r.detail = fmt.Sprintf("%s is empty", d.tokenPath)
		r.hint = "regenerate the token: stop the daemon, delete the file, restart"
		return r
	}
	d.authToken = tok
	r.status = checkPass
	r.detail = fmt.Sprintf("%s (%d chars)", d.tokenPath, len(tok))
	return r
}

// checkClaudeCLI verifies `claude --version` exits 0. The version
// string itself is not parsed because Claude CLI's output format has
// shifted across major versions; presence is sufficient for the
// downstream plugin-list check to make sense.
func (d *doctor) checkClaudeCLI() checkResult {
	r := checkResult{name: "claude CLI"}
	out, err := d.runner("claude", "--version").CombinedOutput()
	if err != nil {
		r.status = checkFail
		r.detail = "not found in PATH"
		r.hint = "install Claude Code (https://claude.com/claude-code)"
		return r
	}
	r.status = checkPass
	r.detail = strings.TrimSpace(string(out))
	if r.detail == "" {
		r.detail = "available"
	}
	return r
}

// checkPluginInstalled runs `claude plugin list --json` and looks for
// the `mcp-gateway` plugin entry. Skipped (not failed) when the claude
// CLI itself is missing — avoids redundant noise.
func (d *doctor) checkPluginInstalled() checkResult {
	r := checkResult{name: "plugin installed"}
	out, err := d.runner("claude", "plugin", "list", "--json").Output()
	if err != nil {
		r.status = checkSkip
		r.detail = "claude CLI unavailable"
		r.hint = "install Claude Code first"
		return r
	}
	var wrapper struct {
		Plugins []struct {
			Name string `json:"name"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(out, &wrapper); err != nil {
		r.status = checkFail
		r.detail = "could not parse `claude plugin list --json` output"
		r.hint = "verify the claude CLI is on a supported version"
		return r
	}
	for _, p := range wrapper.Plugins {
		if p.Name == "mcp-gateway" {
			d.pluginInstalled = true
			r.status = checkPass
			r.detail = "mcp-gateway present"
			return r
		}
	}
	r.status = checkFail
	r.detail = "mcp-gateway not in `claude plugin list`"
	r.hint = "run `mcp-ctl install-claude-code`"
	return r
}

// checkPatchApplied scans VSCode's anthropic.claude-code-* extension
// directories for a webview/index.js carrying the patchMarkerPrefix
// emitted by installer/patches/.
//
// Resolution order for the extensions root:
//   $GATEWAY_VSCODE_EXTENSIONS_DIR (test override) > $HOME/.vscode/extensions
//
// When more than one anthropic.claude-code-* dir exists (older +
// newer versions side by side), every match is inspected; the result
// reports the highest semver found with a marker. This handles
// upgrade-day where a stale older dir is still on disk — pass/fail is
// correct regardless, but the operator-facing detail line should name
// the active version, not whatever sorts first.
func (d *doctor) checkPatchApplied() checkResult {
	r := checkResult{name: "webview patch"}
	root := d.extensionsDir
	if root == "" {
		root = os.Getenv("GATEWAY_VSCODE_EXTENSIONS_DIR")
	}
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			r.status = checkFail
			r.detail = "could not resolve $HOME"
			r.hint = "set $HOME or $GATEWAY_VSCODE_EXTENSIONS_DIR"
			return r
		}
		root = filepath.Join(home, ".vscode", "extensions")
	}
	matches, err := filepath.Glob(filepath.Join(root, "anthropic.claude-code-*"))
	if err != nil || len(matches) == 0 {
		r.status = checkFail
		r.detail = fmt.Sprintf("no anthropic.claude-code-* dir under %s", root)
		r.hint = "install Claude Code first, then run `mcp-ctl install-claude-code`"
		return r
	}
	// Stable iteration order so test output is deterministic; we still
	// pick the highest semver below regardless of input order.
	sort.Strings(matches)
	bestExt, bestVer, found := "", "", false
	for _, ext := range matches {
		indexJS := filepath.Join(ext, "webview", "index.js")
		ver, ok := readPatchVersion(indexJS)
		if !ok {
			continue
		}
		if !found || compareSemver(ver, bestVer) > 0 {
			bestExt, bestVer, found = ext, ver, true
		}
	}
	if found {
		r.status = checkPass
		r.detail = fmt.Sprintf("%s (%s)", filepath.Base(bestExt), bestVer)
		return r
	}
	r.status = checkFail
	r.detail = fmt.Sprintf("no patched index.js under %s", root)
	if d.pluginInstalled {
		r.hint = "run `mcp-ctl install-claude-code` (omit --no-patch)"
	} else {
		r.hint = "run `mcp-ctl install-claude-code` to install plugin + patch in one step"
	}
	return r
}

// readPatchVersion looks for a "MCP Gateway Patch vX.Y.Z" comment line
// in the first 4 KiB of indexJS. The patch installer writes this
// marker into the bundled webview JS via the awk gsub in
// installer/patches/apply-mcp-gateway.sh (and equivalent ps1 path).
//
// Why first 4 KiB only: index.js is a webpack bundle, often >5 MB; the
// marker is added near the top by the apply script. A bounded read
// keeps the doctor command sub-100ms even on slow disks.
func readPatchVersion(path string) (string, bool) {
	f, err := os.Open(path) // #nosec G304 — path is anthropic.claude-code-*/webview/index.js (resolved from $HOME glob)
	if err != nil {
		return "", false
	}
	defer f.Close()
	buf := make([]byte, 4096)
	n, _ := io.ReadFull(f, buf)
	head := string(buf[:n])
	_, rest, ok := strings.Cut(head, patchMarkerPrefix)
	if !ok {
		return "", false
	}
	end := 0
	for end < len(rest) && isSemverByte(rest[end]) {
		end++
	}
	if end == 0 {
		return "", false
	}
	return "v" + strings.TrimRight(rest[:end], "."), true
}

// isSemverByte permits digit + dot, deliberately rejecting anything
// else so we don't accidentally absorb the rest of a JS comment line.
func isSemverByte(b byte) bool {
	return (b >= '0' && b <= '9') || b == '.'
}

// compareSemver returns -1 / 0 / 1 for "a < b", "a == b", "a > b" on
// dotted-numeric version strings (with or without a leading 'v').
// Components beyond the shorter input default to 0, so "v1.18" sorts
// equal to "v1.18.0". Non-numeric components compare as 0.
//
// Why a hand-rolled comparator instead of golang.org/x/mod/semver:
// adding a module dependency for one comparison in a CLI binary is
// gratuitous; the patch-marker version we control is always X.Y.Z
// digits, no pre-release / build metadata.
func compareSemver(a, b string) int {
	a = strings.TrimPrefix(a, "v")
	b = strings.TrimPrefix(b, "v")
	ap := strings.Split(a, ".")
	bp := strings.Split(b, ".")
	n := max(len(ap), len(bp))
	for i := range n {
		ai, bi := 0, 0
		if i < len(ap) {
			ai = atoiSafe(ap[i])
		}
		if i < len(bp) {
			bi = atoiSafe(bp[i])
		}
		if ai < bi {
			return -1
		}
		if ai > bi {
			return 1
		}
	}
	return 0
}

// atoiSafe parses an integer or returns 0 — used by compareSemver
// where we only ever see digits + dots from readPatchVersion.
func atoiSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return n
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// isTerminal reports whether w is a real character device (a TTY).
// Returns false for buffered writers, pipes, and redirected files —
// the cases where ANSI escape codes would render as gibberish.
//
// Implementation note: we deliberately avoid golang.org/x/term to
// keep mcp-ctl's dependency surface minimal. Querying the file
// mode's ModeCharDevice bit is the canonical Go idiom for TTY
// detection without an external dep, and it works on both Unix and
// Windows (where the underlying Win32 console handle reports as a
// character device).
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

func renderResults(out io.Writer, results []checkResult, noColor bool) {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	for _, r := range results {
		fmt.Fprintf(w, "%s\t%s\t%s\n", r.status.symbol(noColor), r.name, r.detail)
		if r.status != checkPass && r.hint != "" {
			fmt.Fprintf(w, "\t\t  hint: %s\n", r.hint)
		}
	}
	_ = w.Flush()
	if anyFailed(results) {
		fmt.Fprintln(out, "\nOne or more checks failed. See hints above.")
	} else {
		fmt.Fprintln(out, "\nAll checks passed.")
	}
}

func anyFailed(results []checkResult) bool {
	for _, r := range results {
		if r.status == checkFail {
			return true
		}
	}
	return false
}
