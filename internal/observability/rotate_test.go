package observability

import (
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func listLogs(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestRotateBySizeAndPrune(t *testing.T) {
	dir := t.TempDir()
	w, err := openRotatingFile(filepath.Join(dir, "gogoslingd.log"), 100, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	// 固定时钟并每次前进 1 秒，备份名可预测。
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	step := 0
	w.now = func() time.Time { step++; return base.Add(time.Duration(step) * time.Second) }

	line := strings.Repeat("x", 60) + "\n"
	for i := 0; i < 6; i++ {
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatalf("第 %d 次写入失败: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	names := listLogs(t, dir)
	// 6 行 × 61 字节，每 100 字节轮转一次：当前文件 + 5 次轮转，只保留 2 个备份。
	if len(names) != 3 {
		t.Fatalf("期望 1 个当前文件 + 2 个备份，得到 %v", names)
	}
	var backups []string
	for _, n := range names {
		if n != "gogoslingd.log" {
			backups = append(backups, n)
		}
	}
	for _, b := range backups {
		if !strings.HasPrefix(b, "gogoslingd-2026-01-02T03-04-") || !strings.HasSuffix(b, ".log") {
			t.Errorf("备份文件名格式不对: %s", b)
		}
	}
	// 保留的是最新的两个（最后两次轮转发生在 step 4、5）。
	if !strings.Contains(backups[0], "T03-04-09") && !strings.Contains(backups[1], "T03-04-09") {
		t.Errorf("应保留最新的备份，得到 %v", backups)
	}
}

func TestRotateCompress(t *testing.T) {
	dir := t.TempDir()
	w, err := openRotatingFile(filepath.Join(dir, "gogoslingd.log"), 50, 5, true)
	if err != nil {
		t.Fatal(err)
	}
	payload := strings.Repeat("abc", 20) + "\n"
	if _, err := w.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("second\n")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil { // Close 等待后台压缩完成
		t.Fatal(err)
	}
	names := listLogs(t, dir)
	var gz string
	for _, n := range names {
		if strings.HasSuffix(n, ".log.gz") {
			gz = n
		}
		if strings.HasPrefix(n, "gogoslingd-") && strings.HasSuffix(n, ".log") {
			t.Errorf("压缩后不应残留明文备份: %s", n)
		}
	}
	if gz == "" {
		t.Fatalf("应生成 .gz 备份，得到 %v", names)
	}
	f, err := os.Open(filepath.Join(dir, gz))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != payload {
		t.Errorf("压缩内容与原文不一致: %q", data)
	}
	cur, _ := os.ReadFile(filepath.Join(dir, "gogoslingd.log"))
	if string(cur) != "second\n" {
		t.Errorf("轮转后当前文件应只含新内容，得到 %q", cur)
	}
}

func TestRotateOversizedSingleWrite(t *testing.T) {
	dir := t.TempDir()
	w, err := openRotatingFile(filepath.Join(dir, "gogoslingd.log"), 10, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("y", 100)
	if _, err := w.Write([]byte(big)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("z")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("after close")); err == nil {
		t.Error("关闭后写入应返回错误")
	}
	names := listLogs(t, dir)
	if len(names) != 2 {
		t.Fatalf("超长的单条记录应完整写入并在下一次写入前轮转，得到 %v", names)
	}
}

func TestRotateReopensExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gogoslingd.log")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := openRotatingFile(path, 1<<20, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if w.size != 4 {
		t.Errorf("打开已有文件时应读取当前大小，得到 %d", w.size)
	}
	if _, err := w.Write([]byte("new\n")); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	data, _ := os.ReadFile(path)
	if string(data) != "old\nnew\n" {
		t.Errorf("应追加而不是截断，得到 %q", data)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0o600 {
			t.Errorf("日志文件权限应为 0600，得到 %o", info.Mode().Perm())
		}
	}
}
