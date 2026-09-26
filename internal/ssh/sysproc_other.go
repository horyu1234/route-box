//go:build !unix

package ssh

import (
	"os/exec"
	"syscall"
)

func sysProcAttr() *syscall.SysProcAttr { return nil }

func terminate(cmd *exec.Cmd) error { return cmd.Process.Kill() }
