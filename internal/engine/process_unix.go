//go:build !windows

package engine

import (
	"os"
	"os/exec"
	"syscall"
)

// configureCmd 让工作进程成为独立进程组的组长，以便 killTree 把它派生的子进程一并结束。
func configureCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killTree 结束整个进程组；进程组不可用时退化为只结束该进程。
func killTree(p *os.Process) error {
	if pgid, err := syscall.Getpgid(p.Pid); err == nil && pgid == p.Pid {
		if err := syscall.Kill(-pgid, syscall.SIGKILL); err == nil {
			return nil
		}
	}
	return p.Kill()
}
