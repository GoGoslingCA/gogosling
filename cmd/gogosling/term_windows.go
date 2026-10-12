//go:build windows

package main

import (
	"os"
	"syscall"
)

// Windows 控制台模式位。
const (
	// enableEchoInput 是输入句柄的 ENABLE_ECHO_INPUT。
	enableEchoInput = 0x0004
	// enableVirtualTerminalProcessing 是输出句柄的 ENABLE_VIRTUAL_TERMINAL_PROCESSING（与上面同值，作用于不同句柄）。
	enableVirtualTerminalProcessing = 0x0004
)

// setConsoleMode 是 kernel32!SetConsoleMode（syscall 包只导出了 GetConsoleMode）。
var setConsoleMode = syscall.NewLazyDLL("kernel32.dll").NewProc("SetConsoleMode")

// disableEcho 关闭控制台回显并返回恢复函数；不是控制台时什么也不做。
func disableEcho() func() {
	h := syscall.Handle(os.Stdin.Fd())
	var mode uint32
	if err := syscall.GetConsoleMode(h, &mode); err != nil {
		return func() {}
	}
	if r, _, _ := setConsoleMode.Call(uintptr(h), uintptr(mode&^enableEchoInput)); r == 0 {
		return func() {}
	}
	return func() { _, _, _ = setConsoleMode.Call(uintptr(h), uintptr(mode)) }
}

// enableVT 为控制台输出句柄开启 VT 处理，使 ANSI 转义序列（暗色等样式）被解释而不是原样显示。
// Windows Terminal 默认已开启；传统控制台（conhost）需要显式开启。不是控制台或开启失败
// （Windows 10 1511 之前的系统）时返回 false，调用方据此不输出任何 ANSI 转义。
func enableVT(f *os.File) bool {
	h := syscall.Handle(f.Fd())
	var mode uint32
	if err := syscall.GetConsoleMode(h, &mode); err != nil {
		return false
	}
	if mode&enableVirtualTerminalProcessing != 0 {
		return true
	}
	r, _, _ := setConsoleMode.Call(uintptr(h), uintptr(mode|enableVirtualTerminalProcessing))
	return r != 0
}
