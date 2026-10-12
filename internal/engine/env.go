package engine

import (
	"sort"
	"strings"

	"github.com/GoGoslingCA/gogosling/internal/config"
)

// 工作进程启动时的环境变量（protocol.md §6）。
const (
	envProtocolVersion = "GOGOSLING_PROTOCOL_VERSION"
	envLocale          = "GOGOSLING_LOCALE"
	envPythonUnbuffer  = "PYTHONUNBUFFERED"
	envPythonIOEnc     = "PYTHONIOENCODING"
)

// buildEnv 组装工作进程的环境：白名单变量（config.ChildEnvAllowlist，全仓唯一定义，含代理与证书变量）
// → 协议变量 → Command.Env 里的显式补充（按键名排序，可覆盖前两者）。
// lookup 通常是 os.LookupEnv，抽出来便于测试。
func buildEnv(locale string, extra map[string]string, lookup func(string) (string, bool)) []string {
	var order []string
	vars := make(map[string]string)
	set := func(k, v string) {
		if _, seen := vars[k]; !seen {
			order = append(order, k)
		}
		vars[k] = v
	}

	for _, k := range config.ChildEnvAllowlist() {
		if v, ok := lookup(k); ok {
			set(k, v)
		}
	}
	set(envProtocolVersion, ProtocolVersion)
	set(envLocale, locale)
	set(envPythonUnbuffer, "1")
	set(envPythonIOEnc, "utf-8")

	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		set(k, extra[k])
	}

	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, k+"="+vars[k])
	}
	return out
}

// egressSummary 从完整环境中挑出网络出口相关的变量（代理与证书），用于启动日志。
// 代理 URL 可能带 userinfo（用户名与口令），一律经 redactProxyURL 脱敏；NO_PROXY 与证书路径原样保留。
func egressSummary(env []string) []string {
	var out []string
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		upper := strings.ToUpper(k)
		switch {
		case upper == "NO_PROXY":
			out = append(out, kv)
		case strings.HasSuffix(upper, "_PROXY"):
			out = append(out, k+"="+redactProxyURL(v))
		case upper == "SSL_CERT_FILE" || upper == "SSL_CERT_DIR" || upper == "REQUESTS_CA_BUNDLE":
			out = append(out, kv)
		}
	}
	return out
}

// redactProxyURL 把代理地址中的 userinfo 替换为 "***"。
//
// 不依赖 url.Parse：用户写的代理地址常常不规范（没有 scheme、口令里有未转义的 '/' 或 '@'），
// 解析失败时更不能把原文写进日志。规则是"去掉 scheme 之后、最后一个 '@' 之前的全部内容"，
// 宁可多遮也不漏遮。
func redactProxyURL(v string) string {
	prefix, rest := "", v
	if i := strings.Index(v, "://"); i >= 0 {
		prefix, rest = v[:i+3], v[i+3:]
	}
	at := strings.LastIndex(rest, "@")
	if at < 0 {
		return v
	}
	return prefix + "***@" + rest[at+1:]
}
