package observability

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSetupWritesBothOutputsRedacted(t *testing.T) {
	dir := t.TempDir()
	var stderr bytes.Buffer
	logger, closeFn, err := Setup(Config{DataDir: dir, Stderr: &stderr, Level: slog.LevelDebug})
	if err != nil {
		t.Fatalf("Setup 失败: %v", err)
	}
	logger.Info("启动", "api_key", "sk-secret", "listen", "127.0.0.1:1")
	logger.Debug("调试", "providerToken", "tkv-plain")
	if err := closeFn(); err != nil {
		t.Fatalf("close 失败: %v", err)
	}

	if strings.Contains(stderr.String(), "sk-secret") || strings.Contains(stderr.String(), "tkv-plain") {
		t.Errorf("stderr 泄露了凭据: %s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "listen=127.0.0.1:1") {
		t.Errorf("stderr 应为文本格式并包含普通字段: %s", stderr.String())
	}

	data, err := os.ReadFile(LogPath(dir))
	if err != nil {
		t.Fatalf("读取日志文件失败: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("期望 2 条记录，得到 %d: %s", len(lines), data)
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("日志文件应为 JSON 行: %v", err)
	}
	if first["api_key"] != RedactedValue || first["listen"] != "127.0.0.1:1" || first["msg"] != "启动" {
		t.Errorf("文件记录不符合预期: %v", first)
	}
	ts, _ := first["time"].(string)
	if !strings.HasSuffix(ts, "Z") {
		t.Errorf("时间应为 UTC（以 Z 结尾），得到 %q", ts)
	}
	if strings.Contains(string(data), "tkv-plain") || strings.Contains(string(data), "providerToken") {
		t.Errorf("文件泄露了凭据或未规范化键名: %s", data)
	}

	if runtime.GOOS != "windows" {
		info, _ := os.Stat(filepath.Join(dir, LogDirName))
		if info.Mode().Perm() != 0o700 {
			t.Errorf("日志目录权限应为 0700，得到 %o", info.Mode().Perm())
		}
	}
}

func TestSetupWithoutDataDir(t *testing.T) {
	var stderr bytes.Buffer
	logger, closeFn, err := Setup(Config{Stderr: &stderr})
	if err != nil {
		t.Fatal(err)
	}
	logger.Warn("只到终端")
	logger.Debug("默认级别是 Info，不应输出")
	if err := closeFn(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "只到终端") || strings.Contains(stderr.String(), "不应输出") {
		t.Errorf("输出不符合预期: %s", stderr.String())
	}
}

func TestSetupFailsWhenLogDirIsAFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, LogDirName), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Setup(Config{DataDir: dir, Stderr: &bytes.Buffer{}}); err == nil {
		t.Error("logs 路径被文件占用时应返回错误")
	}
}
