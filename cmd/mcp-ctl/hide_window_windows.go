//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// CREATE_NO_WINDOW (winbase.h) suppresses console allocation for
// console-subsystem children. syscall doesn't export the constant.
const createNoWindow = 0x08000000

// hideChildWindow marks a console child to run without allocating a
// visible console window. Used so mcp-ctl subcommands don't flash
// terminals when invoked from a GUI (VSCode dashboard).
func hideChildWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
