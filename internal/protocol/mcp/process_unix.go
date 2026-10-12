//go:build !windows

package mcp

import (
	"os"
	"os/exec"
	"syscall"
)

// configureCmd 让 MCP 子进程成为独立进程组的组长，以便 killTree 把它派生的子进程一并结束。
func configureCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killTree 结束整个进程组（进程本身可能已经退出，只清理残留的子进程）；
// 进程组不可用时退化为只结束该进程。
func killTree(p *os.Process) {
	if p == nil {
		return
	}
	if err := syscall.Kill(-p.Pid, syscall.SIGKILL); err == nil {
		return
	}
	_ = p.Kill()
}
