//go:build windows

package auth

import (
	"path/filepath"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// TestApplyTokenFilePerms_Windows_Structural verifies the DACL that
// applyTokenFilePerms sets on the token file.
//
// This test runs in the default `go test` path on windows-latest — it is
// a STRUCTURAL check, not an enforcement check. It asserts:
//   - DACL is Protected (no inheritance from parent directory)
//   - DACL contains exactly one ACE
//   - That ACE is ALLOW, targets the current-user SID
//   - No ACE targets well-known groups (Everyone, BUILTIN\Users,
//     Authenticated Users)
//
// Real enforcement (a second local account cannot open the file) is
// tested in `token_perms_integration_windows_test.go` under the
// `integration` build tag — see ADR-0003 §dacl-rationale (M-1, tiered).
func TestApplyTokenFilePerms_Windows_Structural(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.token")

	// Use LoadOrCreate so the file gets the perms applied the same way
	// the daemon startup path does.
	_, err := LoadOrCreate(path, "")
	require.NoError(t, err)

	sd, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION,
	)
	require.NoError(t, err, "read DACL back from file")

	dacl, _, err := sd.DACL()
	require.NoError(t, err)
	require.NotNil(t, dacl, "file must have an explicit DACL, not NULL (NULL DACL = full access)")

	control, _, err := sd.Control()
	require.NoError(t, err)
	assert.NotZero(t,
		control&windows.SE_DACL_PROTECTED,
		"DACL must be PROTECTED — no inheritance from parent directory")

	aceCount := int(dacl.AceCount)
	assert.Equal(t, 1, aceCount, "DACL must contain exactly one ACE (current-user ALLOW)")

	current, err := currentUserSID()
	require.NoError(t, err)

	// Forbidden well-known SIDs. Any ACE targeting these would allow
	// local attackers to read the token.
	everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	require.NoError(t, err)
	builtinUsers, err := windows.CreateWellKnownSid(windows.WinBuiltinUsersSid)
	require.NoError(t, err)
	authUsers, err := windows.CreateWellKnownSid(windows.WinAuthenticatedUserSid)
	require.NoError(t, err)
	forbidden := []*windows.SID{everyone, builtinUsers, authUsers}

	allowACEForCurrentUser := false
	for i := range aceCount {
		var aceHdr *windows.ACCESS_ALLOWED_ACE
		err := windows.GetAce(dacl, uint32(i), (**windows.ACCESS_ALLOWED_ACE)(&aceHdr))
		require.NoError(t, err, "read ACE #%d", i)

		// Extract the SID from the ACE header. SidStart marks the
		// first DWORD of the embedded SID; casting through
		// unsafe.Pointer is the documented way to retrieve it.
		aceSID := (*windows.SID)(unsafe.Pointer(&aceHdr.SidStart))

		for _, f := range forbidden {
			assert.False(t, windows.EqualSid(aceSID, f),
				"ACE #%d targets forbidden well-known SID %s — DACL must not grant any access to Everyone/Users/Authenticated Users", i, f.String())
		}

		if aceHdr.Header.AceType == windows.ACCESS_ALLOWED_ACE_TYPE && windows.EqualSid(aceSID, current) {
			allowACEForCurrentUser = true
		}
	}

	assert.True(t, allowACEForCurrentUser,
		"DACL must contain exactly one ALLOW ACE for the current user SID")
}

// TestApplyTokenFilePerms_Windows_IdempotentOnExisting verifies that
// calling LoadOrCreate on an existing token file does not weaken perms
// (the existing file is read; we should not rewrite with looser DACL).
func TestApplyTokenFilePerms_Windows_IdempotentOnExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.token")

	tok1, err := LoadOrCreate(path, "")
	require.NoError(t, err)

	tok2, err := LoadOrCreate(path, "")
	require.NoError(t, err)
	assert.Equal(t, tok1, tok2, "second call must return the same persisted token")

	// DACL should still be protected after the second call.
	sd, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION,
	)
	require.NoError(t, err)
	control, _, err := sd.Control()
	require.NoError(t, err)
	assert.NotZero(t, control&windows.SE_DACL_PROTECTED)
}
