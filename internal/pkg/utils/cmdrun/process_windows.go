//go:build windows

package cmdrun

import "os/exec"

// configureProcessGroup Windows 下没有进程组语义，保持默认行为。
func configureProcessGroup(cmd *exec.Cmd) {}

// terminateProcess Windows 下只终止直接子进程（SteamCMD 更新为异步窗口，另行处理）。
func terminateProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
