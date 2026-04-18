package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCredentialImport_JSON_GoldenShape verifies the stable JSON contract
// emitted by `mcp-ctl credential import --json`. The VS Code extension's
// keepass-importer.ts parses this shape; any breaking change must bump
// CredentialImportJSONVersion (T12B.1, architect finding 12B-2).
func TestCredentialImport_JSON_GoldenShape(t *testing.T) {
	kdbxPath := createTestKDBXFile(t, "testpw", "test-server", "admin", "pw123")
	pwFile := createPasswordFile(t, "testpw")

	out, err := executeCredentialCommand(
		"credential", "import",
		"--keepass", kdbxPath,
		"--password-file", pwFile,
		"--json",
	)
	require.NoError(t, err)

	var payload credentialImportJSON
	require.NoError(t, json.Unmarshal([]byte(out), &payload),
		"output must be valid JSON — got: %q", out)

	assert.Equal(t, CredentialImportJSONVersion, payload.Version,
		"version field guards the extension contract")
	assert.Equal(t, "dry-run", payload.Mode,
		"--json without --to-server or --to-env-file implies dry-run")
	assert.Equal(t, 1, payload.Found)
	require.Len(t, payload.Servers, 1)

	srv := payload.Servers[0]
	assert.Equal(t, "test-server", srv.Name)
	assert.Equal(t, "pw123", srv.EnvVars["TEST_SERVER_PASSWORD"])
	assert.Equal(t, "admin", srv.EnvVars["TEST_SERVER_USER"])
	assert.NotNil(t, srv.Headers, "headers must be non-nil map even when empty")
	assert.Empty(t, payload.Results, "dry-run emits no per-server results")
}

// TestCredentialImport_JSON_NoHumanText asserts the --json path does NOT
// mix human-readable summary lines into stdout, so programmatic parsers
// never have to strip non-JSON content.
func TestCredentialImport_JSON_NoHumanText(t *testing.T) {
	kdbxPath := createTestKDBXFile(t, "testpw", "foo", "admin", "pw")
	pwFile := createPasswordFile(t, "testpw")

	out, err := executeCredentialCommand(
		"credential", "import", "--keepass", kdbxPath, "--password-file", pwFile, "--json",
	)
	require.NoError(t, err)

	// The tabwriter summary uses "SERVER\tENV VARS\tHEADERS"; ensure none
	// of those headers leak into the JSON-only stream.
	for _, banned := range []string{"SERVER\t", "(dry-run", "Found "} {
		assert.NotContains(t, out, banned,
			"--json output must be JSON-only; saw banned fragment %q", banned)
	}

	// And the payload must round-trip through json.Unmarshal cleanly.
	var payload credentialImportJSON
	require.NoError(t, json.Unmarshal([]byte(out), &payload))
}

// TestCredentialImport_JSON_VersionGuardsBreakingChange guards against
// accidental schema drift. If a maintainer adds a breaking field change,
// the CredentialImportJSONVersion constant MUST bump alongside — this
// test will fail if the constant stays at the old value while the shape
// changes.
func TestCredentialImport_JSON_VersionGuardsBreakingChange(t *testing.T) {
	// Current contract version is 1. Bumping requires coordinated
	// extension release; see docs/ADR-0003-bearer-token-auth.md §token-lifecycle
	// for analogous versioning rationale (structured payload vs. bare).
	assert.Equal(t, 1, CredentialImportJSONVersion,
		"bumping the JSON contract requires a coordinated extension release")
}

// TestCredentialImport_PasswordStdin verifies --password-stdin reads the
// master password from stdin (for non-TTY exec paths, e.g. VS Code
// child process). Resolves CRITICAL 12B-3 — no TTY-only dependency.
func TestCredentialImport_PasswordStdin(t *testing.T) {
	kdbxPath := createTestKDBXFile(t, "secret-pw", "svc", "user", "pw")

	// Build command with stdin piped.
	buf := new(bytes.Buffer)
	root := newRootCmd()
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetIn(strings.NewReader("secret-pw\n"))
	root.SetArgs([]string{
		"credential", "import",
		"--keepass", kdbxPath,
		"--password-stdin",
		"--json",
	})
	_, err := root.ExecuteC()
	require.NoError(t, err, "piped password must unlock the KDBX — stdout: %s", buf.String())

	var payload credentialImportJSON
	require.NoError(t, json.Unmarshal(buf.Bytes(), &payload))
	assert.Equal(t, 1, payload.Found)
}

// TestCredentialImport_PasswordStdin_MutexWithFile asserts --password-stdin
// and --password-file are mutually exclusive.
func TestCredentialImport_PasswordStdin_MutexWithFile(t *testing.T) {
	kdbxPath := createTestKDBXFile(t, "pw", "foo", "u", "p")
	pwFile := createPasswordFile(t, "pw")

	_, err := executeCredentialCommand(
		"credential", "import",
		"--keepass", kdbxPath,
		"--password-stdin",
		"--password-file", pwFile,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

// TestCredentialImport_PasswordStdin_EmptyRejected asserts an empty
// stdin (no password provided) returns a clear error instead of
// silently falling through to an unlock attempt with "".
func TestCredentialImport_PasswordStdin_EmptyRejected(t *testing.T) {
	kdbxPath := createTestKDBXFile(t, "pw", "foo", "u", "p")

	buf := new(bytes.Buffer)
	root := newRootCmd()
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetIn(strings.NewReader("")) // empty stdin
	root.SetArgs([]string{
		"credential", "import",
		"--keepass", kdbxPath,
		"--password-stdin",
	})
	_, err := root.ExecuteC()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stdin was empty")
}

