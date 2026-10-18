//go:build !windows

package cmdrun

import (
	"os/exec"
	"syscall"
)

// configureProcessGroup 让命令成为独立进程组组长，超时才能整组终止。
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminateProcess 终止整个进程组（负 pid 表示进程组）。
func terminateProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err == nil {
		return nil
	}
	return cmd.Process.Kill()
}
