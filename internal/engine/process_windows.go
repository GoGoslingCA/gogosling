//go:build windows

package engine

import (
	"os"
	"os/exec"
	"syscall"
)

// createNoWindow 是 CreateProcess 的 CREATE_NO_WINDOW 标志（syscall 包没有导出它）。
const createNoWindow = 0x08000000

// configureCmd 让工作进程不创建控制台窗口，并放进新的进程组。
//
// 守护进程可能以 DETACHED_PROCESS 方式运行（没有控制台）；此时 Windows 会为每个控制台子进程
// （python.exe）新建一个可见的控制台窗口，用户关掉它就等于结束工作进程。CREATE_NO_WINDOW 让子进程
// 拿到一个不可见的控制台；CREATE_NEW_PROCESS_GROUP 避免控制台 Ctrl-C 事件在守护进程与子进程之间串扰。
// 用 Job Object 约束子进程树见包注释"待实现"。
func configureCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow | syscall.CREATE_NEW_PROCESS_GROUP}
}

// killTree 当前只结束直接子进程。
func killTree(p *os.Process) error {
	return p.Kill()
}
