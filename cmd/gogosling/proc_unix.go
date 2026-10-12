//go:build !windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// detach 让子进程成为新会话的首进程，脱离当前终端与进程组（Ctrl-C 不会传给它）。
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// runForeground 用 exec 替换当前进程：守护进程直接继承终端、信号与退出码。
func runForeground(bin string, args []string) (int, error) {
	argv := append([]string{bin}, args...)
	err := syscall.Exec(bin, argv, os.Environ())
	// 只有 exec 失败才会走到这里。
	return 1, err
}

// terminateProcess 发送 SIGTERM，守护进程据此优雅关闭（优雅关闭优先经 API 请求，这是它不可用时的退路）。
func terminateProcess(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}

// processAlive 用 0 号信号探测进程：成功或没有权限（EPERM）都说明进程存在。
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
