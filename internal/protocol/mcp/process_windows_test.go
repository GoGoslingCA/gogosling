//go:build windows

package mcp

import (
	"os/exec"
	"syscall"
	"testing"
)

func TestConfigureCmdHidesConsoleWindow(t *testing.T) {
	cmd := exec.Command("node.exe")
	configureCmd(cmd)
	if cmd.SysProcAttr == nil {
		t.Fatal("configureCmd 应设置 SysProcAttr")
	}
	flags := cmd.SysProcAttr.CreationFlags
	if flags&createNoWindow == 0 || flags&syscall.CREATE_NEW_PROCESS_GROUP == 0 {
		t.Errorf("CreationFlags = %#x，期望包含 CREATE_NO_WINDOW 与 CREATE_NEW_PROCESS_GROUP，避免为 MCP 子进程弹出控制台窗口", flags)
	}
}
