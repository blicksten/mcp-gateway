//go:build windows

package lifecycle

import (
	"os/exec"
	"syscall"
)

// configureSysProcAttr sets Windows-specific process creation flags.
// CREATE_NEW_PROCESS_GROUP gives the child its own console process group,
// preventing Ctrl+C propagation from parent to child.
func configureSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}
