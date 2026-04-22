//go:build windows

package lifecycle

import (
	"os"
	"os/exec"
	"syscall"
)

// CREATE_NO_WINDOW suppresses console window allocation for console child
// processes (not exported by syscall on Windows; raw value from winbase.h).
const createNoWindow = 0x08000000

// configureSysProcAttr sets Windows-specific process creation flags.
// CREATE_NEW_PROCESS_GROUP gives the child its own console process group,
// preventing Ctrl+C propagation from parent to child.
// CREATE_NO_WINDOW prevents each stdio MCP child (uv.exe, node.exe, ...)
// from popping a visible console window on every spawn. stdio pipes are
// still inherited normally because Cmd.Stdin/Stdout/Stderr are wired
// explicitly by the lifecycle manager.
func configureSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | createNoWindow,
		HideWindow:    true,
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
