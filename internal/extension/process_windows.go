//go:build windows

package extension

import (
	"os/exec"
	"syscall"
)

// createNoWindow 是 CreateProcess 的 CREATE_NO_WINDOW 标志（syscall 包没有导出它）。
const createNoWindow = 0x08000000

// configurePluginCmd 让插件子进程不创建可见的控制台窗口（守护进程没有控制台时，每次调用都会闪一个窗口），
// 并放进新的进程组。超时或取消时使用默认行为（只结束直接子进程）；用 Job Object 约束子进程树见包注释"待实现"。
func configurePluginCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow | syscall.CREATE_NEW_PROCESS_GROUP}
}
