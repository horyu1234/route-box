package ssh

import (
	"os/exec"
	"syscall"
)

// Pdeathsig 는 RouteBox 가 SIGKILL 로 죽더라도 커널이 ssh 를 멈추게 한다.
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
}

func terminate(cmd *exec.Cmd) error {
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
}
