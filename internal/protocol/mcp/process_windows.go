//go:build windows

package mcp

import (
	"os"
	"os/exec"
	"syscall"
)

// createNoWindow 是 CreateProcess 的 CREATE_NO_WINDOW 标志（syscall 包没有导出它）。
const createNoWindow = 0x08000000

// configureCmd 让 MCP 子进程（node.exe、python.exe 等控制台程序）不创建可见的控制台窗口，
// 并放进新的进程组。守护进程以 DETACHED_PROCESS 运行（没有控制台）时，不设 CREATE_NO_WINDOW
// 会为每个子进程弹出一个窗口，用户关掉它就等于结束 MCP 服务。
// 用 Job Object 约束子进程树见包注释"待实现"。
func configureCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow | syscall.CREATE_NEW_PROCESS_GROUP}
}

// killTree 当前只结束直接子进程。
func killTree(p *os.Process) {
	if p == nil {
		return
	}
	_ = p.Kill()
}
