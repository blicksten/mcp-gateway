//go:build !windows

package auth

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestApplyTokenFilePerms_POSIX_0600 asserts the POSIX branch sets the
// token file to mode 0600 (readable + writable by owner only).
// Resolves CRITICAL 12A-3 — see ADR-0003 §dacl-rationale.
func TestApplyTokenFilePerms_POSIX_0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.token")

	_, err := LoadOrCreate(path, "")
	require.NoError(t, err)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
		"POSIX token file must have mode 0600")
}
