//go:build !windows

package main

import (
	"os/exec"
	"syscall"
	"time"
)

// spawnDetached starts cmd as a detached child via a new process group so the
// daemon outlives mcp-ctl. On POSIX, setting Setpgid is sufficient.
func spawnDetached(cmd *exec.Cmd) error {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	return cmd.Start()
}

// killViaPIDFile reads the PID file and sends SIGTERM, then SIGKILL after 2s.
func killViaPIDFile(deadline time.Time) error {
	return killByPIDFile(deadline, func(pid int) error {
		proc, err := findProcess(pid)
		if err != nil {
			return err
		}
		// Try SIGTERM first.
		if err := proc.Signal(syscall.SIGTERM); err != nil {
			return killProcessByPID(pid)
		}
		// Wait up to 2s for clean exit before force-kill.
		sigkillAt := time.Now().Add(2 * time.Second)
		if sigkillAt.After(deadline) {
			sigkillAt = deadline
		}
		for time.Now().Before(sigkillAt) {
			if proc.Signal(syscall.Signal(0)) != nil {
				return nil // process exited
			}
			time.Sleep(200 * time.Millisecond)
		}
		return killProcessByPID(pid)
	})
}
