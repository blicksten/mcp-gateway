//go:build windows

package lifecycle

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// applyManifestFilePerms replaces the DACL on the manifest temp file with an
// explicitly restrictive ACL: exactly one ALLOW ACE granting full control to
// the current user SID; the DACL is Protected so it does not inherit parent
// directory ACEs. Mirrors auth/token_perms_windows.go.
func applyManifestFilePerms(path string) error {
	sid, err := currentManifestUserSID()
	if err != nil {
		return fmt.Errorf("lookup current user SID: %w", err)
	}
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
		nil, nil, dacl, nil,
	); err != nil {
		return fmt.Errorf("SetNamedSecurityInfo(%s): %w", path, err)
	}
	return nil
}

// currentManifestUserSID returns the SID of the process's primary token user.
// Copied from auth/token_perms_windows.go to avoid cross-package coupling.
func currentManifestUserSID() (*windows.SID, error) {
	tok := windows.GetCurrentProcessToken()
	user, err := tok.GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid.Copy()
}
