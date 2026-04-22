//go:build !windows

package main

import "os/exec"

// hideChildWindow is a no-op on POSIX — no console-window concept.
func hideChildWindow(_ *exec.Cmd) {}
