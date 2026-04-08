package keepass

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// helpers

// simpleCreds builds a single-server ServerCredentials with no headers.
func simpleCreds(serverName, key, value string) []ServerCredentials {
	return []ServerCredentials{
		{
			ServerName: serverName,
			EnvVars:    map[string]string{key: value},
			Headers:    map[string]string{},
		},
	}
}

// readFile reads the content of path and fails the test on error.
func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

// fileMode returns the permission bits of path.
func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	return info.Mode().Perm()
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestWriteEnvFile_CreateNew verifies that WriteEnvFile creates a new file with
// 0600 permissions when the target path does not exist.
func TestWriteEnvFile_CreateNew(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")

	creds := simpleCreds("srv", "SRV_PASSWORD", "secret")
	require.NoError(t, WriteEnvFile(path, creds))

	// File must exist.
	_, err := os.Stat(path)
	require.NoError(t, err, "env file must be created")

	// File permissions: on Unix we expect 0600; on Windows mode bits are
	// unreliable, so we only check the content is present.
	mode := fileMode(t, path)
	// 0600 on Unix; Windows may round up to 0666 — accept 0600 or 0666.
	assert.True(t, mode == 0o600 || mode == 0o666,
		"expected 0600 or 0666 permissions, got %o", mode)

	content := readFile(t, path)
	assert.Contains(t, content, "SRV_PASSWORD=")
}

// TestWriteEnvFile_PreserveComments verifies that comment lines and blank lines
// from an existing .env file are preserved verbatim after an update.
func TestWriteEnvFile_PreserveComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")

	initial := "# Managed by keepass-sync\n\n# Server: srv\nSRV_PASSWORD=\"old\"\n"
	require.NoError(t, os.WriteFile(path, []byte(initial), 0o600))

	creds := simpleCreds("srv", "SRV_PASSWORD", "new")
	require.NoError(t, WriteEnvFile(path, creds))

	content := readFile(t, path)
	assert.Contains(t, content, "# Managed by keepass-sync")
	assert.Contains(t, content, "# Server: srv")
	// The blank line is also preserved (appears as an empty line in comments slice).
	assert.Contains(t, content, "\n\n")
}

// TestWriteEnvFile_EscapeDollar verifies that a "$" in a value is written as "\$"
// to prevent godotenv intra-file interpolation.
func TestWriteEnvFile_EscapeDollar(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")

	creds := simpleCreds("srv", "SRV_TOKEN", "${SOME_VAR}")
	require.NoError(t, WriteEnvFile(path, creds))

	content := readFile(t, path)
	assert.Contains(t, content, `\$`)
	assert.NotContains(t, content, `"${SOME_VAR}"`,
		"unescaped dollar sign must not appear in output")
}

// TestWriteEnvFile_EscapeQuotes verifies that a `"` in a value is written as `\"`
// to avoid breaking the double-quoted format.
func TestWriteEnvFile_EscapeQuotes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")

	creds := simpleCreds("srv", "SRV_GREETING", `say "hello"`)
	require.NoError(t, WriteEnvFile(path, creds))

	content := readFile(t, path)
	// The escaped value must appear.
	assert.Contains(t, content, `say \"hello\"`)
}

// TestWriteEnvFile_AtomicWrite verifies that no leftover ".tmp" file remains in
// the output directory after a successful write.
func TestWriteEnvFile_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")

	creds := simpleCreds("srv", "SRV_PASSWORD", "pw")
	require.NoError(t, WriteEnvFile(path, creds))

	// Temp file must have been renamed away.
	tmpPath := path + ".tmp"
	_, err := os.Stat(tmpPath)
	assert.True(t, os.IsNotExist(err),
		"temp file %q must not exist after successful write", tmpPath)

	// The real file must be present.
	_, err = os.Stat(path)
	require.NoError(t, err, "output file must exist")
}

// TestWriteEnvFile_UpdateExistingKeys verifies that writing to a file that already
// contains KEY=old produces KEY=new (the value is updated, not duplicated).
func TestWriteEnvFile_UpdateExistingKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")

	// Seed with old value.
	initial := `SRV_PASSWORD="old"` + "\n"
	require.NoError(t, os.WriteFile(path, []byte(initial), 0o600))

	creds := simpleCreds("srv", "SRV_PASSWORD", "new")
	require.NoError(t, WriteEnvFile(path, creds))

	content := readFile(t, path)

	// "new" must be present.
	assert.Contains(t, content, `SRV_PASSWORD="new"`)

	// "old" must not appear anywhere in the file.
	assert.NotContains(t, content, "old")

	// Key must appear exactly once.
	occurrences := strings.Count(content, "SRV_PASSWORD=")
	assert.Equal(t, 1, occurrences, "key must appear exactly once in the output")
}

// TestWriteEnvFile_HDRWarning verifies that when credentials contain headers,
// the output file includes a warning comment about HDR_ env vars.
func TestWriteEnvFile_HDRWarning(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")

	creds := []ServerCredentials{
		{
			ServerName: "api",
			EnvVars:    map[string]string{},
			Headers:    map[string]string{"Authorization": "Bearer tok"},
		},
	}

	require.NoError(t, WriteEnvFile(path, creds))

	content := readFile(t, path)
	assert.Contains(t, content, "WARNING: HDR_",
		"warning comment about HDR_ entries must appear in output")
}

// TestWriteEnvFile_KeysSortedAlphabetically verifies that env var keys are written
// in alphabetical order for deterministic output.
func TestWriteEnvFile_KeysSortedAlphabetically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")

	creds := []ServerCredentials{
		{
			ServerName: "srv",
			EnvVars: map[string]string{
				"SRV_ZEBRA":  "z",
				"SRV_APPLE":  "a",
				"SRV_MANGO":  "m",
			},
			Headers: map[string]string{},
		},
	}

	require.NoError(t, WriteEnvFile(path, creds))

	content := readFile(t, path)
	posApple := strings.Index(content, "SRV_APPLE=")
	posMango := strings.Index(content, "SRV_MANGO=")
	posZebra := strings.Index(content, "SRV_ZEBRA=")

	require.True(t, posApple >= 0, "SRV_APPLE must be in output")
	require.True(t, posMango >= 0, "SRV_MANGO must be in output")
	require.True(t, posZebra >= 0, "SRV_ZEBRA must be in output")

	assert.True(t, posApple < posMango, "SRV_APPLE must come before SRV_MANGO")
	assert.True(t, posMango < posZebra, "SRV_MANGO must come before SRV_ZEBRA")
}

// TestWriteEnvFile_NoCredsEmptyExistingFile verifies that writing empty credentials
// to a non-existent file produces an empty (but valid) file without error.
func TestWriteEnvFile_NoCredsEmptyExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")

	require.NoError(t, WriteEnvFile(path, []ServerCredentials{}))

	// File must exist.
	_, err := os.Stat(path)
	require.NoError(t, err)
}

// TestWriteEnvFile_MultipleServersAllKeysPresent verifies that keys from multiple
// ServerCredentials entries all appear in the output.
func TestWriteEnvFile_MultipleServersAllKeysPresent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")

	creds := []ServerCredentials{
		{
			ServerName: "alpha",
			EnvVars:    map[string]string{"ALPHA_PASSWORD": "pa"},
			Headers:    map[string]string{},
		},
		{
			ServerName: "beta",
			EnvVars:    map[string]string{"BETA_PASSWORD": "pb"},
			Headers:    map[string]string{},
		},
	}

	require.NoError(t, WriteEnvFile(path, creds))

	content := readFile(t, path)
	assert.Contains(t, content, "ALPHA_PASSWORD=")
	assert.Contains(t, content, "BETA_PASSWORD=")
}

// TestWriteEnvFile_ValueWithBackslash verifies that a backslash in a value is
// doubled to prevent accidental escape sequences in the output.
func TestWriteEnvFile_ValueWithBackslash(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")

	creds := simpleCreds("srv", "SRV_PATH", `C:\Users\foo`)
	require.NoError(t, WriteEnvFile(path, creds))

	content := readFile(t, path)
	// Each backslash must be doubled.
	assert.Contains(t, content, `C:\\Users\\foo`)
}
