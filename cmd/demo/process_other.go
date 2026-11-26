//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// Let the launcher handle terminal interrupts, then stop only its owned children.
func hideChild(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
