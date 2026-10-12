//go:build windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

const (
	createNewProcessGroup   = 0x00000200
	createNoWindow          = 0x08000000
	processQueryLimitedInfo = 0x1000
	stillActive             = 259
	errorInvalidParameter   = syscall.Errno(87)
)

// detach 让守护进程脱离当前控制台与进程组（Ctrl-C 不会传给它）。
//
// 用 CREATE_NO_WINDOW 而不是 DETACHED_PROCESS：前者给守护进程一个不可见的控制台，它启动的控制台
// 子进程（Python 工作进程、run_command 的 cmd.exe、taskkill、MCP stdio 服务）继承这个隐藏控制台；
// 后者让守护进程没有控制台，系统会为每个控制台子进程各新建一个可见窗口（XP-08）。
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup | createNoWindow}
}

// runForeground 在前台运行守护进程并等待它退出（Windows 没有 exec）。
func runForeground(bin string, args []string) (int, error) {
	cmd := exec.Command(bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	if err != nil {
		return 1, err
	}
	return 0, nil
}

// terminateProcess 强制结束整棵进程树（守护进程与它的工作进程）。
//
// Windows 上没有可用的优雅信号：gogoslingd 是没有窗口的控制台程序，不带 /F 的 taskkill 发出的
// WM_CLOSE 没有接收者、必然失败，所以不再先试一次。优雅关闭由调用方先经 POST /api/v1/system/shutdown
// 完成；只有该途径不可用或超时才走到这里。调用方在此之前已经确认 pid 属于句柄对应的守护进程。
// 强制结束时守护进程来不及删除 daemon.json，由调用方在确认进程退出后删除。
func terminateProcess(pid int) error {
	return exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").Run()
}

// processAlive 打开进程句柄并检查退出码是否为 STILL_ACTIVE。
func processAlive(pid int) bool {
	h, err := syscall.OpenProcess(processQueryLimitedInfo, false, uint32(pid))
	if err != nil {
		// 无权限打开也说明进程存在；参数无效说明进程不存在。
		return !errors.Is(err, errorInvalidParameter)
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}
