//go:build !windows

package api

// verifyClaudeExePid is a no-op stub on non-Windows platforms.
// The unfreeze-button feature is Windows-only v1; the register-pid
// endpoint simply accepts all PIDs on other platforms.
func verifyClaudeExePid(_ uint32) bool {
	return true
}
