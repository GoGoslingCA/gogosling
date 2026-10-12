package identity

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

func TestHandleFileRoundTrip(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	hf := NewHandleFile(filepath.Join(dir, "nested"))
	if hf.Path() != filepath.Join(dir, "nested", HandleFileName) {
		t.Errorf("路径不符: %s", hf.Path())
	}

	if _, err := hf.Read(ctx); !model.IsCode(err, model.CodeNotFound) {
		t.Errorf("文件不存在应返回 not_found，得到 %v", err)
	}

	token, _ := NewToken()
	started := time.Date(2026, 10, 10, 1, 2, 3, 0, time.FixedZone("CST", 8*3600))
	in := Handle{PID: 4242, Endpoint: "http://127.0.0.1:46750", Token: token, Version: "0.1.0", StartedAt: started}
	if err := hf.Write(ctx, in); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	out, err := hf.Read(ctx)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if out.PID != 4242 || out.Endpoint != in.Endpoint || out.Token != token || out.Version != "0.1.0" {
		t.Errorf("读回内容不符: %+v", out)
	}
	if !out.StartedAt.Equal(started) || out.StartedAt.Location() != time.UTC {
		t.Errorf("StartedAt 应以 UTC 读回且时刻不变: %v", out.StartedAt)
	}

	raw, _ := os.ReadFile(hf.Path())
	for _, want := range []string{`"pid": 4242`, `"endpoint"`, `"token"`, `"version"`, `"started_at": "2026-10-09T17:02:03Z"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("文件应包含 %s:\n%s", want, raw)
		}
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(hf.Path())
		if info.Mode().Perm() != 0o600 {
			t.Errorf("句柄文件权限应为 0600，得到 %o", info.Mode().Perm())
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(hf.Path()))
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("不应残留临时文件: %s", e.Name())
		}
	}

	// 覆盖写入：新内容完整替换旧内容。
	in.PID = 4343
	if err := hf.Write(ctx, in); err != nil {
		t.Fatal(err)
	}
	if out, _ := hf.Read(ctx); out.PID != 4343 {
		t.Errorf("覆盖写入后应读到新 pid，得到 %d", out.PID)
	}

	if err := hf.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	if err := hf.Remove(ctx); err != nil {
		t.Errorf("重复删除不应报错: %v", err)
	}
	if _, err := os.Stat(hf.Path()); !os.IsNotExist(err) {
		t.Error("删除后文件不应存在")
	}
}

func TestHandleFileWriteDefaultsAndValidation(t *testing.T) {
	ctx := context.Background()
	hf := NewHandleFile(t.TempDir())
	before := time.Now().UTC().Add(-time.Second)
	if err := hf.Write(ctx, Handle{PID: 1, Endpoint: "e", Token: "t"}); err != nil {
		t.Fatal(err)
	}
	out, _ := hf.Read(ctx)
	if out.StartedAt.Before(before) || out.StartedAt.After(time.Now().UTC().Add(time.Second)) {
		t.Errorf("StartedAt 为零时应填入当前时间，得到 %v", out.StartedAt)
	}

	for name, h := range map[string]Handle{
		"缺少 pid":      {Endpoint: "e", Token: "t"},
		"缺少 endpoint": {PID: 1, Token: "t"},
		"缺少 token":    {PID: 1, Endpoint: "e"},
	} {
		if err := hf.Write(ctx, h); !model.IsCode(err, model.CodeInvalid) {
			t.Errorf("%s 应返回 invalid，得到 %v", name, err)
		}
	}
	// 校验失败不应覆盖已有文件。
	if out, err := hf.Read(ctx); err != nil || out.PID != 1 {
		t.Errorf("非法写入不应破坏已有句柄: %+v %v", out, err)
	}
}

func TestHandleFileReadCorrupt(t *testing.T) {
	ctx := context.Background()
	hf := NewHandleFile(t.TempDir())
	if err := os.WriteFile(hf.Path(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := hf.Read(ctx); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("损坏的文件应返回 invalid，得到 %v", err)
	}
	if err := os.WriteFile(hf.Path(), []byte(`{"pid":0,"endpoint":"e","token":"t"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := hf.Read(ctx); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("字段不完整应返回 invalid，得到 %v", err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := hf.Read(cancelled); err != context.Canceled {
		t.Errorf("已取消的 ctx 应返回取消错误，得到 %v", err)
	}
}
