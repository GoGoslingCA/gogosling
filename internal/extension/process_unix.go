//go:build !windows

package extension

import (
	"os/exec"
	"syscall"
)

// configurePluginCmd 让插件子进程成为独立进程组的组长；超时或取消时整组结束，不留孤儿。
func configurePluginCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
}
