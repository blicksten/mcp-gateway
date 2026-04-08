package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gokeepasslib "github.com/tobischo/gokeepasslib/v3"
	w "github.com/tobischo/gokeepasslib/v3/wrappers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createTestKDBXFile creates a minimal KDBX database in a temp directory and
// returns the path. The database contains one entry with the given title,
// username, and password, protected under a single password credential.
func createTestKDBXFile(t *testing.T, password, title, username, entryPassword string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "test.kdbx")

	db := gokeepasslib.NewDatabase()
	db.Credentials = gokeepasslib.NewPasswordCredentials(password)

	entry := gokeepasslib.NewEntry()
	entry.Values = []gokeepasslib.ValueData{
		{Key: "Title", Value: gokeepasslib.V{Content: title}},
		{Key: "UserName", Value: gokeepasslib.V{Content: username}},
		{Key: "Password", Value: gokeepasslib.V{Content: entryPassword, Protected: w.NewBoolWrapper(true)}},
	}

	rootGroup := gokeepasslib.NewGroup()
	rootGroup.Name = "Root"
	rootGroup.Entries = []gokeepasslib.Entry{entry}
	db.Content.Root.Groups = []gokeepasslib.Group{rootGroup}

	require.NoError(t, db.LockProtectedEntries())

	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()

	require.NoError(t, gokeepasslib.NewEncoder(f).Encode(db))
	return path
}

// createPasswordFile writes a password string to a temp file and returns its path.
func createPasswordFile(t *testing.T, password string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "password.txt")
	require.NoError(t, os.WriteFile(path, []byte(password), 0600))
	return path
}

// executeCredentialCommand builds a fresh command tree (no test server needed —
// credential import in --to-env-file mode makes no API calls) and runs with the
// given args. Returns combined stdout+stderr output and the execution error.
func executeCredentialCommand(args ...string) (string, error) {
	buf := new(bytes.Buffer)
	root := newRootCmd()
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs(args)
	_, err := root.ExecuteC()
	return buf.String(), err
}

// TestCredentialImport_DryRun verifies that --dry-run prints the summary and
// the dry-run notice without creating or modifying any file.
func TestCredentialImport_DryRun(t *testing.T) {
	kdbxPath := createTestKDBXFile(t, "testpw", "test-server", "admin", "pw123")
	pwFile := createPasswordFile(t, "testpw")

	out, err := executeCredentialCommand(
		"credential", "import",
		"--keepass", kdbxPath,
		"--password-file", pwFile,
		"--dry-run",
	)

	require.NoError(t, err)
	assert.Contains(t, out, "(dry-run: no changes made)")
	// Summary table should still be printed.
	assert.Contains(t, out, "test-server")
}

// TestCredentialImport_DryRun_NoEnvFileCreated ensures that --dry-run does
// not create the default .env file in the working directory.
func TestCredentialImport_DryRun_NoEnvFileCreated(t *testing.T) {
	// Run from a temp directory so we can check for .env creation safely.
	tmpDir := t.TempDir()
	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(tmpDir))
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	kdbxPath := createTestKDBXFile(t, "testpw", "test-server", "admin", "pw123")
	pwFile := createPasswordFile(t, "testpw")

	_, err = executeCredentialCommand(
		"credential", "import",
		"--keepass", kdbxPath,
		"--password-file", pwFile,
		"--dry-run",
	)
	require.NoError(t, err)

	_, statErr := os.Stat(filepath.Join(tmpDir, ".env"))
	assert.True(t, os.IsNotExist(statErr), "dry-run must not create .env file")
}

// TestCredentialImport_ToEnvFile verifies that credentials are written to the
// specified env file in KEY="VALUE" format.
func TestCredentialImport_ToEnvFile(t *testing.T) {
	kdbxPath := createTestKDBXFile(t, "testpw", "test-server", "admin", "pw123")
	pwFile := createPasswordFile(t, "testpw")

	envFile := filepath.Join(t.TempDir(), "out.env")

	out, err := executeCredentialCommand(
		"credential", "import",
		"--keepass", kdbxPath,
		"--password-file", pwFile,
		"--to-env-file", envFile,
	)

	require.NoError(t, err)
	assert.Contains(t, out, envFile)

	content, readErr := os.ReadFile(envFile)
	require.NoError(t, readErr)

	lines := string(content)
	assert.Contains(t, lines, `TEST_SERVER_PASSWORD="pw123"`)
	assert.Contains(t, lines, `TEST_SERVER_USER="admin"`)
}

// TestCredentialImport_MissingKeepass verifies that omitting --keepass returns
// an error about the required flag.
func TestCredentialImport_MissingKeepass(t *testing.T) {
	_, err := executeCredentialCommand("credential", "import")

	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "keepass")
}

// TestCredentialImport_ToServerAndToEnvFile verifies that providing both
// --to-server and --to-env-file returns a mutually-exclusive error.
func TestCredentialImport_ToServerAndToEnvFile(t *testing.T) {
	kdbxPath := createTestKDBXFile(t, "testpw", "test-server", "admin", "pw123")
	pwFile := createPasswordFile(t, "testpw")
	envFile := filepath.Join(t.TempDir(), "out.env")

	_, err := executeCredentialCommand(
		"credential", "import",
		"--keepass", kdbxPath,
		"--password-file", pwFile,
		"--to-server",
		"--to-env-file", envFile,
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

// TestCredentialImport_NonTTYNoPassword verifies that when neither
// --password-file nor --key-file is provided in a non-TTY environment (tests
// always run without a TTY) the command returns the expected error.
func TestCredentialImport_NonTTYNoPassword(t *testing.T) {
	kdbxPath := createTestKDBXFile(t, "testpw", "test-server", "admin", "pw123")

	_, err := executeCredentialCommand(
		"credential", "import",
		"--keepass", kdbxPath,
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "stdin is not a terminal")
}

// TestCredentialImport_NonexistentKeepass verifies that a non-existent KDBX
// path produces an error rather than a panic.
func TestCredentialImport_NonexistentKeepass(t *testing.T) {
	pwFile := createPasswordFile(t, "testpw")

	_, err := executeCredentialCommand(
		"credential", "import",
		"--keepass", "/nonexistent/path/db.kdbx",
		"--password-file", pwFile,
	)

	require.Error(t, err)
}

// TestCredentialImport_WrongPassword verifies that a wrong master password
// returns a decode error.
func TestCredentialImport_WrongPassword(t *testing.T) {
	kdbxPath := createTestKDBXFile(t, "correctpassword", "test-server", "admin", "pw123")
	pwFile := createPasswordFile(t, "wrongpassword")

	_, err := executeCredentialCommand(
		"credential", "import",
		"--keepass", kdbxPath,
		"--password-file", pwFile,
		"--to-env-file", filepath.Join(t.TempDir(), "out.env"),
	)

	require.Error(t, err)
}

// --- credential list ---

func TestCredentialList_SingleServer(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/servers/my-srv", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"name":        "my-srv",
			"status":      "running",
			"transport":   "stdio",
			"env_keys":    []string{"API_KEY", "SECRET"},
			"header_keys": []string{"Authorization"},
		})
	}))
	defer ts.Close()

	out, err := executeCommand(ts, "credential", "list", "--server", "my-srv")
	require.NoError(t, err)
	assert.Contains(t, out, "API_KEY")
	assert.Contains(t, out, "SECRET")
	assert.Contains(t, out, "Authorization")
	assert.Contains(t, out, "********")
	assert.Contains(t, out, "env")
	assert.Contains(t, out, "header")
}

func TestCredentialList_SingleServer_Empty(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"name":      "my-srv",
			"status":    "running",
			"transport": "stdio",
		})
	}))
	defer ts.Close()

	out, err := executeCommand(ts, "credential", "list", "--server", "my-srv")
	require.NoError(t, err)
	assert.Contains(t, out, "No credentials configured")
}

func TestCredentialList_AllServers(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/servers", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{
			{
				"name":      "srv-a",
				"status":    "running",
				"transport": "stdio",
				"env_keys":  []string{"FOO"},
			},
			{
				"name":      "srv-b",
				"status":    "running",
				"transport": "http",
			},
			{
				"name":        "srv-c",
				"status":      "running",
				"transport":   "http",
				"header_keys": []string{"X-Token"},
			},
		})
	}))
	defer ts.Close()

	out, err := executeCommand(ts, "credential", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "srv-a")
	assert.Contains(t, out, "FOO")
	assert.NotContains(t, out, "srv-b") // no credentials
	assert.Contains(t, out, "srv-c")
	assert.Contains(t, out, "X-Token")
}

func TestCredentialList_AllServers_NoneConfigured(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{
			{
				"name":      "srv-a",
				"status":    "running",
				"transport": "stdio",
			},
		})
	}))
	defer ts.Close()

	out, err := executeCommand(ts, "credential", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "No servers have credentials configured")
}

func TestCredentialList_JSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"name":        "my-srv",
			"status":      "running",
			"transport":   "stdio",
			"env_keys":    []string{"KEY1"},
			"header_keys": []string{"Auth"},
		})
	}))
	defer ts.Close()

	out, err := executeCommand(ts, "credential", "list", "--server", "my-srv", "--json")
	require.NoError(t, err)

	var entries []credentialEntry
	require.NoError(t, json.Unmarshal([]byte(out), &entries))
	require.Len(t, entries, 1)
	assert.Equal(t, "my-srv", entries[0].Server)
	assert.Equal(t, []string{"KEY1"}, entries[0].EnvKeys)
	assert.Equal(t, []string{"Auth"}, entries[0].HeaderKeys)
}

func TestCredentialList_JSON_Empty(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"name":      "my-srv",
			"status":    "running",
			"transport": "stdio",
		})
	}))
	defer ts.Close()

	out, err := executeCommand(ts, "credential", "list", "--server", "my-srv", "--json")
	require.NoError(t, err)

	var entries []credentialEntry
	require.NoError(t, json.Unmarshal([]byte(out), &entries))
	assert.Empty(t, entries)
}

func TestCredentialList_ServerNotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "server not found"})
	}))
	defer ts.Close()

	_, err := executeCommand(ts, "credential", "list", "--server", "nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "server not found")
}
