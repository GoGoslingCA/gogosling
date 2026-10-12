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
	"sync"
	"testing"

	"github.com/zalando/go-keyring"
)

// fakeKeyring 是内存中的假系统凭据存储，可注入错误。测试绝不触碰真实的系统凭据存储。
type fakeKeyring struct {
	mu     sync.Mutex
	items  map[string]string // service + "\x00" + user → value
	setErr error
	getErr error
}

func newFakeKeyring() *fakeKeyring { return &fakeKeyring{items: map[string]string{}} }

func (f *fakeKeyring) key(service, user string) string { return service + "\x00" + user }

func (f *fakeKeyring) Set(service, user, password string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setErr != nil {
		return f.setErr
	}
	f.items[f.key(service, user)] = password
	return nil
}

func (f *fakeKeyring) Get(service, user string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return "", f.getErr
	}
	v, ok := f.items[f.key(service, user)]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return v, nil
}

func (f *fakeKeyring) Delete(service, user string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.items[f.key(service, user)]; !ok {
		return keyring.ErrNotFound
	}
	delete(f.items, f.key(service, user))
	return nil
}

func (f *fakeKeyring) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.items)
}

func TestOpenPrefersKeyring(t *testing.T) {
	kr := newFakeKeyring()
	var logs bytes.Buffer
	s, err := OpenWith(context.Background(), t.TempDir(), slog.New(slog.NewTextHandler(&logs, nil)), Options{Keyring: kr})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	if BackendName(s) != "keyring" {
		t.Errorf("系统凭据存储可用时应选择它，得到 %s", BackendName(s))
	}
	if kr.count() != 0 {
		t.Error("探测记录应在探测后被删除")
	}
	if !strings.Contains(logs.String(), "backend=keyring") {
		t.Errorf("日志应记录后端: %s", logs.String())
	}
}

func TestOpenFallsBackToFileAndWarns(t *testing.T) {
	kr := newFakeKeyring()
	kr.setErr = errors.New("dbus: no secret service")
	dir := t.TempDir()
	var logs bytes.Buffer
	s, err := OpenWith(context.Background(), dir, slog.New(slog.NewTextHandler(&logs, nil)), Options{Keyring: kr})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	if BackendName(s) != "file" {
		t.Errorf("系统凭据存储不可用时应回退到文件，得到 %s", BackendName(s))
	}
	out := logs.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "回退") || !strings.Contains(out, "no secret service") {
		t.Errorf("应记录带原因的警告: %s", out)
	}
	if err := s.Set(context.Background(), "p", "very-secret-value"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "very-secret-value") {
		t.Error("日志不得包含凭据值")
	}
	for _, name := range []string{FileName, KeyFileName} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("应创建 %s: %v", name, err)
		}
	}
}

func TestOpenDisableKeyring(t *testing.T) {
	kr := newFakeKeyring()
	s, err := OpenWith(context.Background(), t.TempDir(), nil, Options{Keyring: kr, DisableKeyring: true})
	if err != nil {
		t.Fatal(err)
	}
	if BackendName(s) != "file" || kr.count() != 0 {
		t.Error("禁用系统凭据存储时不应触碰它")
	}
}

func TestOpenRespectsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := OpenWith(ctx, t.TempDir(), nil, Options{Keyring: newFakeKeyring()}); !errors.Is(err, context.Canceled) {
		t.Errorf("已取消的 ctx 应直接返回取消错误，得到 %v", err)
	}
}

func TestKeyringStoreMapping(t *testing.T) {
	ctx := context.Background()
	kr := newFakeKeyring()
	s := NewKeyringStore(kr)
	if _, err := s.Get(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("缺失应返回 ErrNotFound，得到 %v", err)
	}
	if err := s.Set(ctx, "a", "1"); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Get(ctx, "a"); v != "1" {
		t.Errorf("读回值不符: %q", v)
	}
	if ok, _ := s.Has(ctx, "a"); !ok {
		t.Error("Has 应为 true")
	}
	if err := s.Delete(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "a"); err != nil {
		t.Errorf("重复删除不应报错: %v", err)
	}
	if ok, _ := s.Has(ctx, "a"); ok {
		t.Error("删除后 Has 应为 false")
	}
	kr.setErr = keyring.ErrSetDataTooBig
	if err := s.Set(ctx, "big", "x"); !errors.Is(err, ErrTooLarge) {
		t.Errorf("过大的值应映射为 ErrTooLarge，得到 %v", err)
	}
	kr.getErr = errors.New("keychain locked")
	if _, err := s.Get(ctx, "a"); err == nil || errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "keychain locked") {
		t.Errorf("其他错误应带上下文原样返回，得到 %v", err)
	}
	if _, err := s.Get(ctx, "bad name"); !errors.Is(err, ErrInvalidName) {
		t.Errorf("非法名称应返回 ErrInvalidName，得到 %v", err)
	}
}

func TestFileStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := OpenFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("空存储应返回 ErrNotFound，得到 %v", err)
	}
	if err := s.Set(ctx, "provider:1:api_key", "sk-live-123"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set(ctx, "channel.tg", "bot:token"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set(ctx, "provider:1:api_key", "sk-live-456"); err != nil {
		t.Fatal(err)
	}
	if v, err := s.Get(ctx, "provider:1:api_key"); err != nil || v != "sk-live-456" {
		t.Errorf("覆盖写入后应读到新值，得到 %q, %v", v, err)
	}
	if err := s.Delete(ctx, "channel.tg"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.Has(ctx, "channel.tg"); ok {
		t.Error("删除后不应存在")
	}
	if err := s.Delete(ctx, "channel.tg"); err != nil {
		t.Errorf("重复删除不应报错: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, plain := range []string{"sk-live", "provider:1", "bot:token"} {
		if bytes.Contains(raw, []byte(plain)) {
			t.Errorf("加密文件不得包含明文 %q", plain)
		}
	}
	if runtime.GOOS != "windows" {
		for _, name := range []string{FileName, KeyFileName} {
			info, _ := os.Stat(filepath.Join(dir, name))
			if info.Mode().Perm() != 0o600 {
				t.Errorf("%s 权限应为 0600，得到 %o", name, info.Mode().Perm())
			}
		}
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("不应残留临时文件: %s", e.Name())
		}
	}

	// 重新打开：同一密钥能解密。
	again, err := OpenFileStore(dir)
	if err != nil {
		t.Fatalf("重新打开失败: %v", err)
	}
	if v, _ := again.Get(ctx, "provider:1:api_key"); v != "sk-live-456" {
		t.Errorf("重新打开后应读到持久化的值，得到 %q", v)
	}
}

func TestFileStoreTamperAndKeyMismatch(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := OpenFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set(ctx, "a", "1"); err != nil {
		t.Fatal(err)
	}
	encPath := filepath.Join(dir, FileName)
	raw, _ := os.ReadFile(encPath)

	// 篡改密文中的一个字节。
	idx := bytes.Index(raw, []byte(`"ciphertext":"`)) + len(`"ciphertext":"`) + 3
	tampered := append([]byte(nil), raw...)
	if tampered[idx] == 'A' {
		tampered[idx] = 'B'
	} else {
		tampered[idx] = 'A'
	}
	if err := os.WriteFile(encPath, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "a"); !errors.Is(err, ErrCorrupt) {
		t.Errorf("篡改后应返回 ErrCorrupt，得到 %v", err)
	}
	if err := os.WriteFile(encPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	// 换一把密钥：打开时就应失败，而不是等到第一次 Get。
	keyPath := filepath.Join(dir, KeyFileName)
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFileStore(dir); !errors.Is(err, ErrKeyMissing) {
		t.Errorf("密钥丢失而加密文件存在时应返回 ErrKeyMissing，得到 %v", err)
	}
	other := t.TempDir()
	if _, err := OpenFileStore(other); err != nil {
		t.Fatal(err)
	}
	otherKey, _ := os.ReadFile(filepath.Join(other, KeyFileName))
	if err := os.WriteFile(keyPath, otherKey, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFileStore(dir); !errors.Is(err, ErrCorrupt) {
		t.Errorf("密钥不匹配时打开应返回 ErrCorrupt，得到 %v", err)
	}

	// 非法的密钥文件内容。
	if err := os.WriteFile(keyPath, []byte("not base64!\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFileStore(dir); err == nil {
		t.Error("密钥文件格式错误应返回错误")
	}
}

func TestFileStoreTightensKeyPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 没有 POSIX 权限位")
	}
	dir := t.TempDir()
	if _, err := OpenFileStore(dir); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, KeyFileName)
	if err := os.Chmod(keyPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFileStore(dir); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(keyPath)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("被放宽的密钥文件权限应收回 0600，得到 %o", info.Mode().Perm())
	}
}

func TestFileStoreConcurrent(t *testing.T) {
	ctx := context.Background()
	s, err := OpenFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := "n" + string(rune('a'+i))
			if err := s.Set(ctx, name, name); err != nil {
				t.Errorf("并发写入失败: %v", err)
			}
		}(i)
	}
	wg.Wait()
	for i := 0; i < 16; i++ {
		name := "n" + string(rune('a'+i))
		if v, err := s.Get(ctx, name); err != nil || v != name {
			t.Errorf("并发写入丢失 %s: %q %v", name, v, err)
		}
	}
}

func TestValidateName(t *testing.T) {
	good := []string{"a", "provider:01ABC:api_key", "channel.telegram.bot-token", "A1", strings.Repeat("x", MaxNameLen)}
	bad := []string{"", ".a", "-a", "a b", "a/b", "a\x00", "中文", strings.Repeat("x", MaxNameLen+1), "__probe__"}
	for _, n := range good {
		if err := ValidateName(n); err != nil {
			t.Errorf("%q 应合法: %v", n, err)
		}
	}
	for _, n := range bad {
		if err := ValidateName(n); !errors.Is(err, ErrInvalidName) {
			t.Errorf("%q 应不合法，得到 %v", n, err)
		}
	}
}

func TestBackendNameUnknown(t *testing.T) {
	if got := BackendName(nil); got != "unknown" {
		t.Errorf("未知实现应返回 unknown，得到 %s", got)
	}
}
