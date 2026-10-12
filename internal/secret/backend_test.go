package secret

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func recordOf(t *testing.T, dir string) string {
	t.Helper()
	got, err := RecordedBackend(dir)
	if err != nil {
		t.Fatalf("读取后端记录失败: %v", err)
	}
	return got
}

func TestOpenRecordsFirstChoice(t *testing.T) {
	ctx := context.Background()

	dir := t.TempDir()
	if got := recordOf(t, dir); got != "" {
		t.Fatalf("新数据目录不应有记录，得到 %q", got)
	}
	if _, err := OpenWith(ctx, dir, nil, Options{Keyring: newFakeKeyring()}); err != nil {
		t.Fatal(err)
	}
	if got := recordOf(t, dir); got != BackendKeyring {
		t.Errorf("首次选中系统凭据存储后应记录 keyring，得到 %q", got)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, BackendFileName))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("记录文件权限应为 0600：%v %v", info.Mode().Perm(), err)
		}
	}

	dir = t.TempDir()
	broken := newFakeKeyring()
	broken.setErr = errors.New("dbus: no secret service")
	if _, err := OpenWith(ctx, dir, nil, Options{Keyring: broken}); err != nil {
		t.Fatal(err)
	}
	if got := recordOf(t, dir); got != BackendFile {
		t.Errorf("首次回退到加密文件后应记录 file，得到 %q", got)
	}
}

// 场景（XP-11）：GUI 会话里首次启动选中了 keyring；之后在没有 Secret Service 的 SSH/systemd 环境里启动。
// 不得静默回退到加密文件，而应返回 ErrBackendUnavailable 并保持记录不变。
func TestOpenDoesNotSilentlySwitchFromRecordedKeyring(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	kr := newFakeKeyring()
	s, err := OpenWith(ctx, dir, nil, Options{Keyring: kr})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set(ctx, "provider:p:api_key", "sk-gui"); err != nil {
		t.Fatal(err)
	}

	kr.setErr = errors.New("dbus: no session bus")
	_, err = OpenWith(ctx, dir, nil, Options{Keyring: kr})
	if !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("记录的 keyring 不可用时应返回 ErrBackendUnavailable，得到 %v", err)
	}
	if !strings.Contains(err.Error(), "no session bus") || strings.Contains(err.Error(), "sk-gui") {
		t.Errorf("错误应包含不可用原因且不含凭据值: %v", err)
	}
	for _, name := range []string{FileName, KeyFileName} {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("不应悄悄创建加密文件存储 %s: %v", name, err)
		}
	}
	if got := recordOf(t, dir); got != BackendKeyring {
		t.Errorf("失败时记录不应改变，得到 %q", got)
	}

	// 环境恢复后照常使用 keyring，原凭据仍在。
	kr.setErr = nil
	s, err = OpenWith(ctx, dir, nil, Options{Keyring: kr})
	if err != nil {
		t.Fatal(err)
	}
	if v, err := s.Get(ctx, "provider:p:api_key"); err != nil || v != "sk-gui" {
		t.Errorf("恢复后应读到原凭据，得到 (%q, %v)", v, err)
	}
}

func TestOpenRecordedFileIgnoresAvailableKeyring(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	broken := newFakeKeyring()
	broken.setErr = errors.New("locked")
	s, err := OpenWith(ctx, dir, nil, Options{Keyring: broken})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set(ctx, "channel:c:secret", "tok"); err != nil {
		t.Fatal(err)
	}

	kr := newFakeKeyring()
	s, err = OpenWith(ctx, dir, nil, Options{Keyring: kr})
	if err != nil {
		t.Fatal(err)
	}
	if BackendName(s) != BackendFile || kr.count() != 0 {
		t.Errorf("记录为 file 时即使 keyring 可用也应继续用文件且不触碰 keyring：backend=%s keyring 条目=%d", BackendName(s), kr.count())
	}
	if v, err := s.Get(ctx, "channel:c:secret"); err != nil || v != "tok" {
		t.Errorf("应读到文件中的凭据，得到 (%q, %v)", v, err)
	}
}

func TestOpenExplicitSwitchUpdatesRecord(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	kr := newFakeKeyring()
	if _, err := OpenWith(ctx, dir, nil, Options{Keyring: kr}); err != nil {
		t.Fatal(err)
	}

	// 记录为 keyring、keyring 不可用：显式选择 file 是用户主动切换。
	kr.setErr = errors.New("unavailable")
	var logs bytes.Buffer
	s, err := OpenWith(ctx, dir, slog.New(slog.NewTextHandler(&logs, nil)), Options{Keyring: kr, Backend: BackendFile})
	if err != nil {
		t.Fatal(err)
	}
	if BackendName(s) != BackendFile || recordOf(t, dir) != BackendFile {
		t.Errorf("显式切换到 file 后应使用并记录 file：backend=%s record=%s", BackendName(s), recordOf(t, dir))
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "from=keyring") {
		t.Errorf("切换后端应记录警告: %s", logs.String())
	}

	// 之后不再显式指定：沿用新的记录。
	s, err = OpenWith(ctx, dir, nil, Options{Keyring: kr})
	if err != nil || BackendName(s) != BackendFile {
		t.Fatalf("应沿用新记录 file，得到 (%v, %v)", BackendName(s), err)
	}

	// 显式切回 keyring，但它不可用：返回 ErrBackendUnavailable，记录不变。
	if _, err := OpenWith(ctx, dir, nil, Options{Keyring: kr, Backend: BackendKeyring}); !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("显式选择不可用的 keyring 应返回 ErrBackendUnavailable，得到 %v", err)
	}
	if recordOf(t, dir) != BackendFile {
		t.Error("失败的切换不应改写记录")
	}
	kr.setErr = nil
	s, err = OpenWith(ctx, dir, nil, Options{Keyring: kr, Backend: BackendKeyring})
	if err != nil || BackendName(s) != BackendKeyring || recordOf(t, dir) != BackendKeyring {
		t.Fatalf("keyring 恢复后显式切换应成功并更新记录：%v %v %v", BackendName(s), recordOf(t, dir), err)
	}
}

func TestOpenRejectsUnknownBackendAndRecord(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if _, err := OpenWith(ctx, dir, nil, Options{Keyring: newFakeKeyring(), Backend: "vault"}); err == nil {
		t.Error("未知的显式后端应返回错误")
	}

	if err := os.WriteFile(filepath.Join(dir, BackendFileName), []byte("tpm\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenWith(ctx, dir, nil, Options{Keyring: newFakeKeyring()}); !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("记录内容无法识别时应返回 ErrBackendUnavailable，得到 %v", err)
	}
	if _, err := RecordedBackend(dir); !errors.Is(err, ErrBackendUnavailable) {
		t.Errorf("RecordedBackend 应报告无法识别的记录，得到 %v", err)
	}
	// 显式选择可以修复损坏的记录。
	if _, err := OpenWith(ctx, dir, nil, Options{Keyring: newFakeKeyring(), DisableKeyring: true}); err != nil {
		t.Fatal(err)
	}
	if recordOf(t, dir) != BackendFile {
		t.Error("显式选择后应改写损坏的记录")
	}
}

func TestOpenCreatesMissingDataDirForRecord(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "home")
	if _, err := OpenWith(context.Background(), dir, nil, Options{Keyring: newFakeKeyring()}); err != nil {
		t.Fatal(err)
	}
	if recordOf(t, dir) != BackendKeyring {
		t.Error("数据目录不存在时应创建它并写入记录")
	}
}
