package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/sandbox"
	"github.com/GoGoslingCA/gogosling/internal/server"
	"github.com/GoGoslingCA/gogosling/internal/workbench/preview"
)

// officeConverter 只在显式请求版式或旧格式时启动单个转换进程，没有常驻服务。
type officeConverter struct {
	binary string
	gate   chan struct{}
	runner sandbox.Runner
}

func newPreviewConverter(configured string) server.PreviewConverter {
	if runtime.GOOS != "darwin" {
		return nil
	} // 其他平台必须先实现等价的只读范围隔离，不能降级为不隔离。
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		return nil
	}
	if configured == "" {
		configured = "/Applications/LibreOffice.app/Contents/MacOS/soffice"
	}
	if !filepath.IsAbs(configured) {
		return nil
	}
	binary, err := filepath.EvalSymlinks(configured)
	if err != nil {
		return nil
	}
	info, err := os.Stat(binary)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return nil
	}
	return &officeConverter{binary: binary, gate: make(chan struct{}, 1), runner: sandbox.NewDirect(slog.New(slog.DiscardHandler))}
}
func (c *officeConverter) Convert(ctx context.Context, name string, input io.Reader) ([]byte, error) {
	select {
	case c.gate <- struct{}{}:
		defer func() { <-c.gate }()
	default:
		return nil, preview.Error("busy")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp("", "gogosling-preview-*")
	if err != nil {
		return nil, preview.Error("unavailable")
	}
	defer os.RemoveAll(dir)
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, preview.Error("unavailable")
	}
	ext := strings.ToLower(filepath.Ext(name))
	target := filepath.Join(dir, "input"+ext)
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, preview.Error("unavailable")
	}
	n, err := io.Copy(file, io.LimitReader(input, preview.MaxStructured+1))
	closeErr := file.Close()
	if n > preview.MaxStructured {
		return nil, preview.Error("too_large")
	}
	if err != nil || closeErr != nil {
		return nil, preview.Error("unavailable")
	}
	profileDir := filepath.Join(dir, "profile")
	if os.MkdirAll(filepath.Join(profileDir, "user"), 0o700) != nil {
		return nil, preview.Error("unavailable")
	}
	// 独立用户配置，最高宏安全等级；关闭外链自动更新。沙箱另外阻止网络和无关本地读取。
	settings := `<?xml version="1.0"?><oor:items xmlns:oor="http://openoffice.org/2001/registry"><item oor:path="/org.openoffice.Office.Common/Security/Scripting"><prop oor:name="MacroSecurityLevel" oor:op="fuse"><value>3</value></prop></item><item oor:path="/org.openoffice.Office.Common/Load"><prop oor:name="UpdateLinks" oor:op="fuse"><value>0</value></prop></item></oor:items>`
	if os.WriteFile(filepath.Join(profileDir, "user", "registrymodifications.xcu"), []byte(settings), 0o600) != nil {
		return nil, preview.Error("unavailable")
	}
	profile, err := sandbox.BuildSeatbeltProfile(sandbox.SeatbeltProfile{WritableDirs: []string{dir}, AllowNetwork: false})
	if err != nil {
		return nil, preview.Error("converter_unavailable")
	}
	installation := filepath.Dir(c.binary)
	if i := strings.Index(c.binary, ".app/"); i >= 0 {
		installation = c.binary[:i+4]
	}
	readRule := `(allow file-read-metadata) (allow file-read* (literal "/") (subpath "/System") (subpath "/private/preboot") (subpath "/usr") (subpath "/bin") (subpath "/sbin") (subpath "/Library/Fonts") (subpath "/Library/Apple") (subpath "/private/var/db") (subpath "/private/etc") (subpath "/dev") (subpath ` + profileQuote(installation) + `) (subpath ` + profileQuote(dir) + `))`
	profile = strings.Replace(profile, "(allow file-read*)", readRule, 1)
	profileURL := (&url.URL{Scheme: "file", Path: profileDir}).String()
	command := []string{"/usr/bin/nice", "-n", "10", "sandbox-exec", "-p", profile, c.binary, "-env:UserInstallation=" + profileURL, "--headless", "--nologo", "--nodefault", "--norestore", "--nolockcheck", "--convert-to", "pdf", "--outdir", dir, target}
	result, err := c.runner.Run(ctx, sandbox.Spec{Command: command, Dir: dir, Env: map[string]string{"TMPDIR": dir, "SAL_USE_VCLPLUGIN": "svp"}, Timeout: 30 * time.Second, MaxOutputBytes: 4096})
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || result.TimedOut {
		return nil, preview.Error("timeout")
	}
	if ctx.Err() != nil {
		return nil, preview.Error("cancelled")
	}
	if err != nil || result.ExitCode != 0 || result.TimedOut {
		return nil, preview.Error("converter_unavailable")
	}
	out, err := os.Open(filepath.Join(dir, "input.pdf"))
	if err != nil {
		return nil, preview.Error("corrupt")
	}
	defer out.Close()
	data, err := io.ReadAll(io.LimitReader(out, preview.MaxFile+1))
	if err != nil || len(data) < 5 || string(data[:5]) != "%PDF-" {
		return nil, preview.Error("corrupt")
	}
	if len(data) > preview.MaxFile {
		return nil, preview.Error("too_large")
	}
	return data, nil
}
func profileQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
