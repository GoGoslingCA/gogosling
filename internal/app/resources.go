package app

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"

	"github.com/GoGoslingCA/gogosling/internal/config"
)

// 本文件用到的进程级环境变量名统一定义在 config 包：config.EnvResources、config.EnvDev；
// 组合根另外读取 config.EnvSecretBackend 与 config.EnvAllowedOrigins（逗号分隔的额外 Origin，
// 作为 server.Options.AllowedOrigins 的静态白名单；设置项 allowed_origins 由 server 自己热更新）。

// splitList 按逗号切分并去掉空项。
func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// modulePath 用来确认开发时找到的 go.mod 属于本仓库。
const modulePath = "github.com/GoGoslingCA/gogosling"

// 资源目录的来源，写进启动日志便于排查"为什么没加载内置技能"。
const (
	resourcesFromEnv     = "env"
	resourcesFromInstall = "install"
	resourcesFromRepo    = "repo"
)

// resourceLocator 封装定位资源目录所需的环境，便于测试注入。
type resourceLocator struct {
	getenv func(string) string
	exe    string // 当前可执行文件的绝对路径
	cwd    string // 当前工作目录
}

// systemResourceLocator 使用真实的进程环境。
func systemResourceLocator() resourceLocator {
	exe, _ := os.Executable()
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	cwd, _ := os.Getwd()
	return resourceLocator{getenv: os.Getenv, exe: exe, cwd: cwd}
}

// locate 按优先级返回资源根目录与来源：
//  1. 环境变量 GOGOSLING_RESOURCES；
//  2. 安装布局 <exe>/../share/gogosling；
//  3. 开发布局：可执行文件位于 go-build 临时目录（go run / go test）或设置了 GOGOSLING_DEV 时，
//     从当前目录向上寻找属于本模块的 go.mod 所在目录。
//
// 都找不到返回空串；调用方跳过内置资产并记录日志，守护进程照常启动。
func (l resourceLocator) locate() (dir, source string) {
	if v := strings.TrimSpace(l.getenv(config.EnvResources)); v != "" {
		if abs, err := filepath.Abs(v); err == nil && isDir(abs) {
			return abs, resourcesFromEnv
		}
		return "", ""
	}
	if l.exe != "" {
		share := filepath.Join(filepath.Dir(l.exe), "..", "share", "gogosling")
		if isDir(share) {
			return filepath.Clean(share), resourcesFromInstall
		}
	}
	if l.getenv(config.EnvDev) != "" || inGoBuildDir(l.exe) {
		if root := findModuleRoot(l.cwd); root != "" {
			return root, resourcesFromRepo
		}
	}
	return "", ""
}

// bundledExtensionsDir 返回资源目录下的内置扩展目录；不存在时返回空串。
func bundledExtensionsDir(resources string) string {
	if resources == "" {
		return ""
	}
	dir := filepath.Join(resources, "extensions", "bundled")
	if !isDir(dir) {
		return ""
	}
	return dir
}

// inGoBuildDir 判断可执行文件是否位于 go run / go test 使用的 go-build 临时目录。
func inGoBuildDir(exe string) bool {
	if exe == "" {
		return false
	}
	for _, part := range strings.Split(filepath.ToSlash(exe), "/") {
		if strings.HasPrefix(part, "go-build") {
			return true
		}
	}
	return false
}

// findModuleRoot 从 start 向上寻找 module 行等于本模块路径的 go.mod；找不到返回空串。
func findModuleRoot(start string) string {
	if start == "" {
		return ""
	}
	dir := filepath.Clean(start)
	for {
		if moduleMatches(filepath.Join(dir, "go.mod")) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// moduleMatches 判断 go.mod 的 module 声明是否为本模块。
func moduleMatches(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if rest, ok := strings.CutPrefix(line, "module"); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`) == modulePath
		}
	}
	return false
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}
