//go:build !windows

package lifecycle

import (
	"os"
	"os/exec"
	"syscall"
)

// configureSysProcAttr configures POSIX-specific process creation.
// T13A.1/F-5: Setpgid=true places each child in its own process group
// whose PGID equals the child PID. This lets the daemon send signals
// to the whole group (child + its descendants) via `kill(-pgid, ...)`,
// preventing orphan grandchild processes if the MCP server spawns
// helpers and the daemon crashes or the child becomes wedged.
func configureSysProcAttr(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// terminateProcessGroup sends SIGTERM to the child's process group so
// any grandchildren are also signalled. Falls back to killing just the
// leader if the group signal fails (e.g. child already reaped).
func terminateProcessGroup(p *os.Process) error {
	if p == nil {
		return nil
	}
	// Negative PID == process group (POSIX). SIGTERM allows graceful
	// shutdown; the caller escalates to SIGKILL after a timeout.
	if err := syscall.Kill(-p.Pid, syscall.SIGTERM); err == nil {
		return nil
	}
	// Fallback — signal just the leader.
	return p.Signal(syscall.SIGTERM)
}

// killProcessGroup sends SIGKILL to the child's process group (used
// after the graceful-stop grace period expires).
func killProcessGroup(p *os.Process) error {
	if p == nil {
		return nil
	}
	if err := syscall.Kill(-p.Pid, syscall.SIGKILL); err == nil {
		return nil
	}
	return p.Kill()
}
