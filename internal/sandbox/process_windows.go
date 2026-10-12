//go:build windows

package sandbox

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// createNoWindow 是 CreateProcess 的 CREATE_NO_WINDOW 标志（标准库 syscall 未导出）。
// 守护进程可能没有控制台（例如由 CLI 以 DETACHED_PROCESS 拉起），此时控制台子进程默认会各自弹出
// 一个可见的控制台窗口；带上该标志后子进程拿到的是不可见的控制台。
const createNoWindow = 0x08000000

// configureCmd 把子进程放进新的进程组（避免控制台 Ctrl-C 事件在守护进程与子进程之间串扰），
// 不为它创建可见窗口；rawCmdLine 非空时作为完整命令行原样交给 CreateProcess，不经 Go 的参数转义。
func configureCmd(cmd *exec.Cmd, rawCmdLine string) {
	cmd.SysProcAttr = sysProcAttr(rawCmdLine)
}

// sysProcAttr 构造 Windows 子进程属性；单独抽出以便测试。
func sysProcAttr(rawCmdLine string) *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | createNoWindow,
		CmdLine:       rawCmdLine,
	}
}

// killTree 用 taskkill /T 结束整棵进程树，再确保直接子进程已结束。
// Job Object 方案见包注释"待实现"。
func killTree(p *os.Process) error {
	if p == nil {
		return nil
	}
	kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(p.Pid))
	kill.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	_ = kill.Run()
	if err := p.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}
