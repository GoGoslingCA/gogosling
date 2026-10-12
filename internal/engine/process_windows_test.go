//go:build windows

package engine

import (
	"os/exec"
	"syscall"
	"testing"
)

func TestConfigureCmdHidesConsoleWindow(t *testing.T) {
	cmd := exec.Command("python.exe")
	configureCmd(cmd)
	if cmd.SysProcAttr == nil {
		t.Fatal("configureCmd 应设置 SysProcAttr")
	}
	flags := cmd.SysProcAttr.CreationFlags
	if flags&createNoWindow == 0 {
		t.Errorf("CreationFlags = %#x，期望包含 CREATE_NO_WINDOW，避免无控制台的守护进程为工作进程弹出窗口", flags)
	}
	if flags&syscall.CREATE_NEW_PROCESS_GROUP == 0 {
		t.Errorf("CreationFlags = %#x，期望包含 CREATE_NEW_PROCESS_GROUP", flags)
	}
}
