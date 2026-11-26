//go:build process && !windows

package integration_test

import "os/exec"

func hideTestProcess(cmd *exec.Cmd) {}
