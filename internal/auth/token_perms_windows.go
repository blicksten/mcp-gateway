//go:build windows

package auth

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// applyTokenFilePerms replaces the DACL on the token file with an
// explicitly restrictive ACL: exactly one ALLOW ACE granting full control
// to the current user SID; no ACEs for Everyone, BUILTIN\Users, or any
// other group. The DACL is marked Protected so it does not inherit ACEs
// from the parent directory.
//
// Rationale, alternatives considered, and test tiering are documented in
// ADR-0003 §dacl-rationale. Resolves CRITICAL 12A-3.
//
// Deny-by-default property: the DACL contains no ALLOW ACE for any
// principal other than the current user. If SetNamedSecurityInfo fails
// (permission denied, invalid handle, disk error), the function returns
// a wrapped error — the file is left in whatever state Windows put it,
// but the caller will not proceed to serve requests from a file it cannot
// lock down. This prevents a "failed to restrict, but serve anyway" class
// of vulnerability.
func applyTokenFilePerms(path string) error {
	sid, err := currentUserSID()
	if err != nil {
		return fmt.Errorf("lookup current user SID: %w", err)
	}

	// Build a Protected DACL with exactly one ALLOW ACE (File All) for the
	// current user. "D:P" = DACL, Protected (no inheritance).
	// "(A;;FA;;;%s)" = ALLOW ACE, no flags, File All rights, no object
	// GUID, no inherit GUID, SID.
	sddl := fmt.Sprintf("D:P(A;;FA;;;%s)", sid.String())
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return fmt.Errorf("parse DACL SDDL: %w", err)
	}

	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("extract DACL: %w", err)
	}

	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, // owner — unchanged
		nil, // group — unchanged
		dacl,
		nil, // SACL — unchanged
	); err != nil {
		return fmt.Errorf("SetNamedSecurityInfo(%s): %w", path, err)
	}

	return nil
}

// currentUserSID returns the SID of the process's primary token user.
func currentUserSID() (*windows.SID, error) {
	tok := windows.GetCurrentProcessToken()
	user, err := tok.GetTokenUser()
	if err != nil {
		return nil, err
	}
	// The returned SID references the user token buffer; clone it so it
	// outlives the token handle if it is later closed.
	return user.User.Sid.Copy()
}
