//go:build !windows

package lifecycle

import (
	"fmt"
	"os"
)

// applyManifestFilePerms sets POSIX file permissions to 0600 on the manifest
// temp file. Mirrors auth/token_perms_other.go.
func applyManifestFilePerms(path string) error {
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("chmod 0600 %s: %w", path, err)
	}
	return nil
}
