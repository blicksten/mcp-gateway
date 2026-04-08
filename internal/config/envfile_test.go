package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadEnvFile_EmptyPath(t *testing.T) {
	m, err := LoadEnvFile("")
	require.NoError(t, err)
	assert.NotNil(t, m)
	assert.Empty(t, m)
}

func TestLoadEnvFile_ValidFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	require.NoError(t, os.WriteFile(path, []byte("FOO=bar\nBAZ=qux\n"), 0o640))

	m, err := LoadEnvFile(path)
	require.NoError(t, err)
	assert.Equal(t, "bar", m["FOO"])
	assert.Equal(t, "qux", m["BAZ"])
}

func TestLoadEnvFile_MissingFile(t *testing.T) {
	_, err := LoadEnvFile("/nonexistent/.env")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "env file")
}

func TestLoadEnvFile_QuotedValues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	require.NoError(t, os.WriteFile(path, []byte(`KEY="hello world"`+"\n"), 0o640))

	m, err := LoadEnvFile(path)
	require.NoError(t, err)
	assert.Equal(t, "hello world", m["KEY"])
}

func TestLoadEnvFile_Comments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "# This is a comment\nFOO=bar\n# Another comment\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o640))

	m, err := LoadEnvFile(path)
	require.NoError(t, err)
	assert.Equal(t, "bar", m["FOO"])
	assert.Len(t, m, 1)
}

func TestLoadEnvFile_NoInterpolationFromProcessEnv(t *testing.T) {
	// Regression guard: godotenv.Read() interpolates ${VAR} from os.Environ(),
	// bypassing the allowlist in expand.go. LoadEnvFile uses godotenv.Parse()
	// instead, which does NOT pull from os.Environ().
	// godotenv.Parse still resolves undefined vars to "" within its own scope,
	// but the critical property is: process secrets do NOT leak into the map.
	t.Setenv("PROCESS_SECRET", "leaked")

	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	require.NoError(t, os.WriteFile(path, []byte("KEY=${PROCESS_SECRET}\n"), 0o640))

	m, err := LoadEnvFile(path)
	require.NoError(t, err)
	// Must NOT be "leaked" (the process env value).
	// godotenv.Parse resolves undefined ${VAR} references to "" within its own scope.
	assert.Equal(t, "", m["KEY"], "undefined vars in .env must resolve to empty, not process env value")
}

func TestLoadEnvFile_IntraFileInterpolation(t *testing.T) {
	// godotenv.Parse performs intra-file interpolation: a variable defined
	// earlier in the .env file can be referenced by a later variable.
	// This is intentional and documented in LoadEnvFile's comment.
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "BASE=http://localhost:3000\nURL=${BASE}/mcp\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o640))

	m, err := LoadEnvFile(path)
	require.NoError(t, err)
	assert.Equal(t, "http://localhost:3000", m["BASE"])
	assert.Equal(t, "http://localhost:3000/mcp", m["URL"])
}
