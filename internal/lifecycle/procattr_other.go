//go:build !windows

package lifecycle

import "os/exec"

// configureSysProcAttr is a no-op on non-Windows platforms.
func configureSysProcAttr(_ *exec.Cmd) {}
