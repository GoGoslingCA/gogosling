package sandbox

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// newSeatbeltOrSkip 在非 darwin 或没有 sandbox-exec 的机器上跳过测试。
func newSeatbeltOrSkip(t *testing.T) *Seatbelt {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("Seatbelt 只在 macOS 上验证")
	}
	sb, err := NewSeatbelt(nil)
	if err != nil {
		t.Skipf("本机没有 sandbox-exec: %v", err)
	}
	return sb
}

func TestSeatbeltAllowsWritesOnlyInWritableDirs(t *testing.T) {
	sb := newSeatbeltOrSkip(t)
	allowed := t.TempDir()
	other := t.TempDir()

	res, err := sb.Run(context.Background(), Spec{
		Command:      []string{"/bin/sh", "-c", "echo ok > allowed.txt && cat allowed.txt"},
		Dir:          allowed,
		WritableDirs: []string{allowed},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || strings.TrimSpace(res.Stdout) != "ok" {
		t.Fatalf("写入允许目录应成功: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(allowed, "allowed.txt")); err != nil {
		t.Fatalf("文件应已写入: %v", err)
	}

	target := filepath.Join(other, "escape.txt")
	res, err = sb.Run(context.Background(), Spec{
		Command:      []string{"/bin/sh", "-c", "echo bad > '" + target + "'"},
		Dir:          allowed,
		WritableDirs: []string{allowed},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode == 0 {
		t.Fatalf("写入其他目录应失败: %+v", res)
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("其他目录不应出现文件")
	}
	if !strings.Contains(strings.ToLower(res.Stderr), "permitted") && !strings.Contains(strings.ToLower(res.Stderr), "denied") {
		t.Logf("stderr: %q", res.Stderr)
	}
}

func TestSeatbeltProvidesPrivateTmpDir(t *testing.T) {
	sb := newSeatbeltOrSkip(t)
	dir := t.TempDir()
	res, err := sb.Run(context.Background(), Spec{
		Command: []string{"/bin/sh", "-c", `f="$TMPDIR/probe.txt"; echo hi > "$f" && cat "$f" && echo "$TMPDIR"`},
		Dir:     dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || !strings.HasPrefix(res.Stdout, "hi\n") {
		t.Fatalf("应能写入私有 TMPDIR: %+v", res)
	}
	tmp := strings.TrimSpace(strings.TrimPrefix(res.Stdout, "hi\n"))
	if !strings.Contains(tmp, "gogosling-sb-") {
		t.Fatalf("TMPDIR 应是每次运行的私有目录，得到 %q", tmp)
	}
	if _, err := os.Stat(tmp); err == nil {
		t.Fatal("运行结束后私有临时目录应被删除")
	}
}

func TestSeatbeltDeniesNetworkByDefault(t *testing.T) {
	sb := newSeatbeltOrSkip(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	probe := []string{"/usr/bin/nc", "-z", "-w", "2", "127.0.0.1", strconv.Itoa(port)}
	if _, err := os.Stat(probe[0]); err != nil {
		t.Skip("本机没有 nc")
	}
	dir := t.TempDir()

	res, err := sb.Run(context.Background(), Spec{Command: probe, Dir: dir, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode == 0 {
		t.Fatalf("默认应禁止网络: %+v", res)
	}
	res, err = sb.Run(context.Background(), Spec{Command: probe, Dir: dir, Timeout: 5 * time.Second, AllowNetwork: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("AllowNetwork 时应能连接回环地址: %+v", res)
	}
}

func TestSeatbeltRejectsMissingWritableDir(t *testing.T) {
	sb := newSeatbeltOrSkip(t)
	_, err := sb.Run(context.Background(), Spec{
		Command:      []string{"/bin/sh", "-c", "true"},
		Dir:          t.TempDir(),
		WritableDirs: []string{filepath.Join(t.TempDir(), "missing")},
	})
	if err == nil {
		t.Fatal("不存在的可写目录应报错")
	}
}
