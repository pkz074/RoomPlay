//go:build process && windows

package integration_test

import (
	"os/exec"
	"syscall"
)

func hideTestProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
}
