//go:build windows

package permission

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// XP-04：\\?\ 设备路径前缀不能绕过数据目录防护。
func TestKernelDataDirVerbatimPrefixIsDenied(t *testing.T) {
	env := newEnv(t, nil)
	p := `\\?\` + filepath.Join(env.dataDir, "daemon.json")
	r := env.req("mcp_read", model.RiskRead, model.AutonomyAutoInSandbox, map[string]any{"path": p})
	v, err := env.svc.Check(context.Background(), r)
	if err != nil || v.Allowed || v.RuleID != "kernel-data-dir" {
		t.Fatalf(`\\?\ 前缀的内核数据文件应被硬拒绝: %+v %v`, v, err)
	}
}

// XP-03：8.3 短名（例如 SSH~1）展开为长名后再匹配规则。
func TestShortNameCannotBypassDenyRule(t *testing.T) {
	env := newEnv(t, nil)
	dir := filepath.Join(env.workspace, "proj", ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "id_rsa"), []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	long := filepath.Join(dir, "id_rsa")
	in, _ := syscall.UTF16PtrFromString(long)
	buf := make([]uint16, 1024)
	n, err := syscall.GetShortPathName(in, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 {
		t.Skipf("无法取得短名: %v", err)
	}
	short := syscall.UTF16ToString(buf[:n])
	if short == long {
		t.Skip("该卷未启用 8.3 短名")
	}
	r := env.req("read_file", model.RiskRead, model.AutonomyAskRisky, map[string]any{"path": short})
	v, err := env.svc.Check(context.Background(), r)
	if err != nil || v.Allowed || v.RuleID != "deny-ssh-keys" {
		t.Fatalf("短名 %q 应被 deny-ssh-keys 拒绝: %+v %v", short, v, err)
	}
}
