//go:build windows

package main

import (
	"os/exec"
	"syscall"
	"time"
)

// createNewProcessGroup and DETACHED_PROCESS ensure the spawned daemon
// keeps running after mcp-ctl exits and has no attached console.
const (
	detachedProcess    = 0x00000008
	createNewProcGroup = 0x00000200
)

// spawnDetached starts cmd as a fully detached child that survives
// mcp-ctl's exit. On Windows this requires DETACHED_PROCESS +
// CREATE_NEW_PROCESS_GROUP creation flags.
func spawnDetached(cmd *exec.Cmd) error {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= detachedProcess | createNewProcGroup
	return cmd.Start()
}

// killViaPIDFile reads the PID file and terminates the process.
// On Windows there is no SIGTERM equivalent, so we go straight to Kill.
func killViaPIDFile(deadline time.Time) error {
	return killByPIDFile(deadline, func(pid int) error {
		return killProcessByPID(pid)
	})
}
