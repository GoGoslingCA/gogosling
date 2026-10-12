package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// withTZFiles 把系统时区的探测文件替换为临时路径。
func withTZFiles(t *testing.T, localtime, timezone string) {
	t.Helper()
	oldL, oldT := localtimePath, timezonePath
	localtimePath, timezonePath = localtime, timezone
	t.Cleanup(func() { localtimePath, timezonePath = oldL, oldT })
}

func requireTZData(t *testing.T) {
	t.Helper()
	if _, err := time.LoadLocation("Asia/Shanghai"); err != nil {
		t.Skip("本机没有时区数据库：", err)
	}
}

func TestSystemTimezoneFromTZ(t *testing.T) {
	requireTZData(t)
	dir := t.TempDir()
	withTZFiles(t, filepath.Join(dir, "none"), filepath.Join(dir, "none"))

	for in, want := range map[string]string{
		"Asia/Shanghai":                      "Asia/Shanghai",
		":America/New_York":                  "America/New_York",
		" UTC ":                              "UTC",
		":/usr/share/zoneinfo/Europe/Berlin": "Europe/Berlin",
		"Local":                              "",
		"Mars/Base":                          "",
		"../../etc/passwd":                   "",
		"":                                   "",
	} {
		if got := SystemTimezone(envOf(map[string]string{"TZ": in})); got != want {
			t.Errorf("TZ=%q 时 SystemTimezone = %q，期望 %q", in, got, want)
		}
	}
}

func TestSystemTimezoneFromFiles(t *testing.T) {
	requireTZData(t)
	if runtime.GOOS == "windows" {
		t.Skip("Windows 上创建符号链接需要特权")
	}
	dir := t.TempDir()
	link := filepath.Join(dir, "localtime")
	if err := os.Symlink("/usr/share/zoneinfo/Asia/Tokyo", link); err != nil {
		t.Fatal(err)
	}
	tzFile := filepath.Join(dir, "timezone")
	if err := os.WriteFile(tzFile, []byte("Europe/Paris\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	withTZFiles(t, link, tzFile)
	if got := SystemTimezone(envOf(nil)); got != "Asia/Tokyo" {
		t.Errorf("应从 /etc/localtime 的链接目标得到 Asia/Tokyo，得到 %q", got)
	}
	if got := SystemTimezone(envOf(map[string]string{"TZ": "UTC"})); got != "UTC" {
		t.Errorf("TZ 应优先于 /etc/localtime，得到 %q", got)
	}

	withTZFiles(t, filepath.Join(dir, "missing"), tzFile)
	if got := SystemTimezone(envOf(nil)); got != "Europe/Paris" {
		t.Errorf("没有 /etc/localtime 链接时应读 /etc/timezone，得到 %q", got)
	}

	withTZFiles(t, filepath.Join(dir, "missing"), filepath.Join(dir, "missing"))
	if got := SystemTimezone(envOf(nil)); got != "" {
		t.Errorf("无法确定时应返回空串，得到 %q", got)
	}
}
