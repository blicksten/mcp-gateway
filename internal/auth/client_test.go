package auth

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Env-var tests use t.Setenv for automatic restoration.

func TestBuildHeader_EnvWinsOverFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.token")
	fileTok, err := GenerateToken()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte(fileTok), 0o600))

	envTok, err := GenerateToken()
	require.NoError(t, err)
	t.Setenv(EnvVarName, envTok)

	got, err := BuildHeader(path)
	require.NoError(t, err)
	assert.Equal(t, "Bearer "+envTok, got)
	assert.NotEqual(t, "Bearer "+fileTok, got, "env override must win")
}

func TestBuildHeader_FileFallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.token")
	fileTok, err := GenerateToken()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte(fileTok), 0o600))

	// Ensure env is unset.
	t.Setenv(EnvVarName, "")

	got, err := BuildHeader(path)
	require.NoError(t, err)
	assert.Equal(t, "Bearer "+fileTok, got)
}

func TestBuildHeader_ErrorWhenBothAbsent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.token") // file does not exist
	t.Setenv(EnvVarName, "")

	_, err := BuildHeader(path)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNoToken))
	// Error message must name both fallbacks so operators can act on it.
	assert.Contains(t, err.Error(), EnvVarName)
	assert.Contains(t, err.Error(), path)
}

func TestBuildHeader_MalformedEnvRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.token")
	t.Setenv(EnvVarName, "too-short")

	_, err := BuildHeader(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "malformed")
}

func TestBuildHeader_MalformedFileFallsThroughToError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.token")
	// Write a file with wrong alphabet — tryReadToken rejects it, so
	// ResolveToken falls through to the no-token error (not "use a
	// corrupt token").
	require.NoError(t, os.WriteFile(path, []byte(strings.Repeat("@", 60)), 0o600))
	t.Setenv(EnvVarName, "")

	_, err := BuildHeader(path)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNoToken),
		"malformed file must surface as ErrNoToken so operator regenerates rather than patching the file")
}

func TestResolveToken_ReturnsBareTokenNoPrefix(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.token")
	tok, err := GenerateToken()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte(tok), 0o600))
	t.Setenv(EnvVarName, "")

	got, err := ResolveToken(path)
	require.NoError(t, err)
	assert.Equal(t, tok, got)
	assert.False(t, strings.HasPrefix(got, "Bearer"), "ResolveToken must return bare token")
}
