//go:build windows

package lifecycle

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestJobObject_KillOnClose(t *testing.T) {
	// Create a Job Object.
	job, err := newJobObject()
	if err != nil {
		t.Fatalf("newJobObject: %v", err)
	}

	// Spawn a long-running child process.
	cmd := exec.Command("cmd.exe", "/c", "ping -n 60 127.0.0.1 >nul")
	if err := cmd.Start(); err != nil {
		windows.CloseHandle(job)
		t.Fatalf("cmd.Start: %v", err)
	}
	pid := uint32(cmd.Process.Pid)

	// Assign child to Job Object.
	if err := assignProcess(job, pid); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		windows.CloseHandle(job)
		t.Fatalf("assignProcess: %v", err)
	}

	// Verify child is still running.
	proc, err := os.FindProcess(int(pid))
	if err != nil {
		t.Fatalf("FindProcess: %v", err)
	}

	// Close the Job Object handle — this should kill the child.
	if err := closeJobObject(job); err != nil {
		t.Fatalf("closeJobObject: %v", err)
	}

	// Wait for the child to exit (should happen immediately).
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case <-done:
		// Child exited — Job Object kill worked.
	case <-time.After(5 * time.Second):
		_ = proc.Kill()
		t.Fatal("child process did not exit within 5s after Job Object close")
	}
}

func TestJobObject_AssignInvalidPID(t *testing.T) {
	job, err := newJobObject()
	if err != nil {
		t.Fatalf("newJobObject: %v", err)
	}
	defer closeJobObject(job)

	// PID 0 is the System Idle Process; OpenProcess should fail.
	err = assignProcess(job, 0)
	if err == nil {
		t.Fatal("expected error assigning PID 0, got nil")
	}
}
