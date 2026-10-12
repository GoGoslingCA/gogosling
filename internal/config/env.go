package config

import (
	"net"
	"strings"
)

// childEnvAllowlist 是守护进程启动任何子进程（Python 工作进程、沙箱命令、MCP 服务、插件）时
// 允许透传的环境变量。它是全仓唯一的一份定义，与 api/worker-rpc/protocol.md §6 保持一致：
//   - 基础：解释器与系统定位所需；
//   - 网络出口：代理与证书设置不是凭据，但代理 URL 可能含 userinfo，写日志时必须脱敏。
//
// 守护进程自身环境里的其他变量（尤其是密钥类）一律不透传。
var childEnvAllowlist = []string{
	// 基础
	"PATH", "HOME", "TMPDIR", "TEMP", "TMP", "LANG", "LC_ALL",
	// Windows：cmd.exe、解释器与常见工具依赖这些定位变量
	"SystemRoot", "SystemDrive", "ComSpec", "PATHEXT", "USERPROFILE", "APPDATA", "LOCALAPPDATA",
	// 网络出口
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY",
	"http_proxy", "https_proxy", "no_proxy", "all_proxy",
	"SSL_CERT_FILE", "SSL_CERT_DIR", "REQUESTS_CA_BUNDLE",
}

// ChildEnvAllowlist 返回子进程环境变量白名单的副本。
func ChildEnvAllowlist() []string {
	return append([]string(nil), childEnvAllowlist...)
}

// IsLoopbackAddr 判断 "host:port" 形式的监听地址是否只绑定回环接口。
//
// 接受：127.0.0.0/8 内的任意地址、::1、localhost（不区分大小写）、以及主机为空（视为不安全，返回 false）。
// 这是全仓唯一的回环判定，app 与 server 都用它，避免两套规则不一致。
func IsLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return false
	}
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
