//go:build !windows

package sandbox

import (
	"os"
	"os/exec"
	"syscall"
)

// configureCmd 让子进程成为新进程组的组长，以便 killTree 把它派生的后代一并结束。
// rawCmdLine（Spec.RawCommandLine）只在 Windows 上有意义，这里忽略：POSIX 上 argv 原样传给 execve，不存在转义问题。
func configureCmd(cmd *exec.Cmd, _ string) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killTree 结束整个进程组；进程组不可用时退化为只结束该进程。
func killTree(p *os.Process) error {
	if p == nil {
		return nil
	}
	if pgid, err := syscall.Getpgid(p.Pid); err == nil && pgid == p.Pid {
		if err := syscall.Kill(-pgid, syscall.SIGKILL); err == nil {
			return nil
		}
	}
	return p.Kill()
}
