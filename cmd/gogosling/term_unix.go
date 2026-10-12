//go:build !windows

package main

import (
	"os"
	"os/exec"
)

// disableEcho 关闭终端回显并返回恢复函数。用系统自带的 stty 而不是引入终端库；
// stty 不可用时退化为回显输入（只影响观感，不影响安全：输入不会被记录）。
func disableEcho() func() {
	off := exec.Command("stty", "-echo")
	off.Stdin = os.Stdin
	if err := off.Run(); err != nil {
		return func() {}
	}
	return func() {
		on := exec.Command("stty", "echo")
		on.Stdin = os.Stdin
		_ = on.Run()
	}
}

// enableVT 报告终端能否解释 ANSI 转义序列。POSIX 终端总是可以。
func enableVT(*os.File) bool { return true }
