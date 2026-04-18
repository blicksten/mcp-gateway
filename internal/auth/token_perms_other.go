//go:build !windows

package auth

import (
	"fmt"
	"os"
)

// applyTokenFilePerms sets POSIX file permissions to 0600 on the token
// file. Follows the `procattr_other.go` precedent in internal/lifecycle/.
//
// Resolves CRITICAL 12A-3 — token must be unreadable by other local users.
// See ADR-0003 §dacl-rationale.
func applyTokenFilePerms(path string) error {
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("chmod 0600 %s: %w", path, err)
	}
	return nil
}
