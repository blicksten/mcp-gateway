package main

import (
	"fmt"
	"os"
	"time"

	"mcp-gateway/internal/pidfile"
)

// killByPIDFile is the shared logic: read the PID file and invoke killFn(pid).
// If the PID file is missing, we treat the daemon as already gone (no error).
func killByPIDFile(deadline time.Time, killFn func(int) error) error {
	pidPath := pidfile.DefaultPath()
	pid, err := pidfile.Read(pidPath)
	if err != nil {
		// PID file missing or unreadable — daemon may already be gone.
		return fmt.Errorf("cannot find daemon PID: %w", err)
	}
	if err := killFn(pid); err != nil {
		return fmt.Errorf("kill daemon (pid %d): %w", pid, err)
	}
	return nil
}

// killProcessByPID forcefully terminates the process with the given PID.
func killProcessByPID(pid int) error {
	proc, err := findProcess(pid)
	if err != nil {
		return err
	}
	return proc.Kill()
}

// findProcess wraps os.FindProcess.
var findProcess = func(pid int) (*os.Process, error) {
	return os.FindProcess(pid)
}
