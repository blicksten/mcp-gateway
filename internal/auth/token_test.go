package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateToken_ShapeAndEntropy(t *testing.T) {
	tok1, err := GenerateToken()
	require.NoError(t, err)
	tok2, err := GenerateToken()
	require.NoError(t, err)

	assert.GreaterOrEqual(t, len(tok1), MinTokenLen)
	assert.Equal(t, 43, len(tok1), "32 random bytes encode to 43 base64url chars")
	assert.True(t, looksLikeToken(tok1), "generated token must pass structural check")
	assert.NotEqual(t, tok1, tok2, "two consecutive generations must differ")
}

func TestLoadOrCreate_EnvOverrideWins(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.token")

	envTok, err := GenerateToken()
	require.NoError(t, err)

	got, err := LoadOrCreate(path, envTok)
	require.NoError(t, err)
	assert.Equal(t, envTok, got, "env override must win verbatim")

	// Env path must not touch disk.
	_, err = os.Stat(path)
	assert.True(t, os.IsNotExist(err), "env override must not write the file")
}

func TestLoadOrCreate_MalformedEnvRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.token")

	_, err := LoadOrCreate(path, "too-short")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "malformed")
}

func TestLoadOrCreate_RegenIfAbsent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.token")

	tok, err := LoadOrCreate(path, "")
	require.NoError(t, err)
	assert.True(t, looksLikeToken(tok))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, tok, strings.TrimSpace(string(data)), "file content must equal returned token")
}

func TestLoadOrCreate_ReadIfExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.token")

	existing, err := GenerateToken()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte(existing), 0o600))

	got, err := LoadOrCreate(path, "")
	require.NoError(t, err)
	assert.Equal(t, existing, got, "existing well-formed token must be reused verbatim")
}

func TestLoadOrCreate_ReadTolerantOfTrailingWhitespace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.token")

	existing, err := GenerateToken()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte(existing+"\n"), 0o600))

	got, err := LoadOrCreate(path, "")
	require.NoError(t, err)
	assert.Equal(t, existing, got, "trailing newline must be stripped, token reused")
}

func TestLoadOrCreate_ShortFileRegenerated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.token")
	require.NoError(t, os.WriteFile(path, []byte("short"), 0o600))

	tok, err := LoadOrCreate(path, "")
	require.NoError(t, err)
	assert.True(t, looksLikeToken(tok))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, tok, strings.TrimSpace(string(data)))
	assert.NotEqual(t, "short", strings.TrimSpace(string(data)))
}

func TestLoadOrCreate_CorruptFileRegenerated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.token")
	// Sufficient length but contains characters outside the base64url alphabet.
	badTok := strings.Repeat("@#$%", 12) // 48 chars
	require.NoError(t, os.WriteFile(path, []byte(badTok), 0o600))

	tok, err := LoadOrCreate(path, "")
	require.NoError(t, err)
	assert.True(t, looksLikeToken(tok))
	assert.NotEqual(t, badTok, tok)
}

func TestLoadOrCreate_AtomicWriteLeavesNoPartialFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.token")

	_, err := LoadOrCreate(path, "")
	require.NoError(t, err)

	// Only the final token file should remain — no stray .auth-token-*.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "atomic write must not leave temp files")
	assert.Equal(t, "auth.token", entries[0].Name())
}

func TestLoadOrCreate_CreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	// Intentionally point into a non-existent subdirectory.
	path := filepath.Join(dir, "nested", "auth.token")

	tok, err := LoadOrCreate(path, "")
	require.NoError(t, err)
	assert.True(t, looksLikeToken(tok))

	_, err = os.Stat(path)
	assert.NoError(t, err, "LoadOrCreate must create intermediate directories")
}

func TestLooksLikeToken(t *testing.T) {
	good, err := GenerateToken()
	require.NoError(t, err)

	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"generated_token", good, true},
		{"empty", "", false},
		{"too_short", "abc", false},
		{"right_length_bad_alphabet", strings.Repeat("@", MinTokenLen), false},
		{"right_length_good_alphabet", strings.Repeat("A", MinTokenLen), true},
		{"contains_padding_equals", strings.Repeat("A", MinTokenLen-1) + "=", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, looksLikeToken(tc.in))
		})
	}
}

func TestDefaultTokenPath(t *testing.T) {
	got := DefaultTokenPath("/tmp/cfg")
	assert.Equal(t, filepath.Join("/tmp/cfg", "auth.token"), got)
}
