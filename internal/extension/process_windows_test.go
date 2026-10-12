//go:build windows

package extension

import (
	"os/exec"
	"syscall"
	"testing"
)

func TestConfigurePluginCmdHidesConsoleWindow(t *testing.T) {
	cmd := exec.Command("plugin.exe")
	configurePluginCmd(cmd)
	if cmd.SysProcAttr == nil {
		t.Fatal("configurePluginCmd 应设置 SysProcAttr")
	}
	flags := cmd.SysProcAttr.CreationFlags
	if flags&createNoWindow == 0 || flags&syscall.CREATE_NEW_PROCESS_GROUP == 0 {
		t.Errorf("CreationFlags = %#x，期望包含 CREATE_NO_WINDOW 与 CREATE_NEW_PROCESS_GROUP，避免每次插件调用闪出控制台窗口", flags)
	}
}
