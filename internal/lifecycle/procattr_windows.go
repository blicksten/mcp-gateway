//go:build windows

package lifecycle

import (
	"os"
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

// terminateProcessGroup on Windows simply terminates the process; job
// objects handle grandchild cleanup (see job_windows.go).
func terminateProcessGroup(p *os.Process) error {
	if p == nil {
		return nil
	}
	return p.Kill()
}

// killProcessGroup is identical to terminateProcessGroup on Windows.
func killProcessGroup(p *os.Process) error {
	if p == nil {
		return nil
	}
	return p.Kill()
}
