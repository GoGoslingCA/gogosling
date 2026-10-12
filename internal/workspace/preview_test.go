package workspace

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func Test预览只读句柄限制目录符号链接和路径逃逸(t *testing.T) {
	ctx := context.Background()
	w, e := New(t.TempDir()).Open(ctx, "agent", "")
	if e != nil {
		t.Fatal(e)
	}
	if e = w.WriteFile(ctx, "note.txt", []byte("内容")); e != nil {
		t.Fatal(e)
	}
	f, e := w.OpenRead(ctx, "note.txt")
	if e != nil {
		t.Fatal(e)
	}
	data, e := io.ReadAll(f)
	f.Close()
	if e != nil || string(data) != "内容" {
		t.Fatal("正常文件无法读取")
	}
	for _, p := range []string{"../outside", "/etc/passwd", ".", "inbound"} {
		if f, e := w.OpenRead(ctx, p); e == nil {
			f.Close()
			t.Fatalf("路径不应作为预览文件打开: %s", p)
		}
	}
	if runtime.GOOS != "windows" {
		outside := filepath.Join(t.TempDir(), "private")
		_ = os.WriteFile(outside, []byte("secret"), 0o600)
		if e = os.Symlink(outside, filepath.Join(w.Root(), "escape")); e != nil {
			t.Fatal(e)
		}
		if f, e := w.OpenRead(ctx, "escape"); e == nil {
			f.Close()
			t.Fatal("不应读取越界符号链接")
		}
	}
	entries, e := w.PreviewEntries(ctx, "")
	if e != nil {
		t.Fatal(e)
	}
	for _, item := range entries {
		if item.Name == "escape" {
			t.Fatal("目录不应展示符号链接入口")
		}
	}
}
