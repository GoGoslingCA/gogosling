package mcp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/GoGoslingCA/gogosling/internal/config"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

// SecretPrefix 是 MCPServerConfig.Env / Headers 中密钥引用的前缀："secret:<ref>"。
const SecretPrefix = "secret:"

// EnvResolver 把密钥引用解析为实际值；由集成方（secret 包的适配）提供。
// 返回的值只进子进程环境或 HTTP 请求头，本包不记录、不返回。
type EnvResolver func(ctx context.Context, ref string) (string, error)

// reservedHeaders 是 streamable HTTP 传输自己管理的请求头（规范形式）；配置里写这些会破坏协议，一律拒绝。
var reservedHeaders = map[string]bool{
	"Host": true, "Content-Length": true, "Content-Type": true, "Accept": true,
	"Connection": true, "Transfer-Encoding": true, "Upgrade": true, "Te": true, "Trailer": true,
	"Keep-Alive": true, "Proxy-Connection": true,
	"Mcp-Session-Id": true, "Mcp-Protocol-Version": true, "Last-Event-Id": true,
}

// ValidateConfig 检查连接配置是否完整、安全：
//   - stdio 必须有 Command，Env 的键不能为空；stdio 不允许 Headers（请求头只对 http 有意义，
//     静默忽略会让用户以为鉴权已经配置）；
//   - http 必须是 https URL，或指向回环 / 局域网地址的 http URL（避免令牌明文上公网）；
//     Headers 的键必须是合法的 HTTP 字段名且不是传输自管的头，值不得含 CR/LF/NUL，
//     "secret:<ref>" 引用不得为空。
func ValidateConfig(cfg model.MCPServerConfig) error {
	switch cfg.Transport {
	case model.MCPStdio:
		if strings.TrimSpace(cfg.Command) == "" {
			return model.NewError(model.CodeInvalid, "stdio 传输必须指定 command")
		}
		for k := range cfg.Env {
			if strings.TrimSpace(k) == "" || strings.ContainsAny(k, "=\x00") {
				return model.Errorf(model.CodeInvalid, "非法的环境变量名 %q", k)
			}
		}
		if len(cfg.Headers) > 0 {
			return model.NewError(model.CodeInvalid, "stdio 传输不支持 headers（请求头只用于 http 传输）")
		}
		return nil
	case model.MCPHTTP:
		if err := validateHTTPURL(cfg.URL); err != nil {
			return err
		}
		return validateHeaders(cfg.Headers)
	default:
		return model.Errorf(model.CodeInvalid, "不支持的 MCP 传输方式 %q", cfg.Transport)
	}
}

// validateHeaders 校验 http 传输的附加请求头；错误信息只含键名，不含值（值可能是令牌）。
func validateHeaders(headers map[string]string) error {
	for k, v := range headers {
		if !validHeaderName(k) {
			return model.Errorf(model.CodeInvalid, "非法的请求头名 %q", k)
		}
		if reservedHeaders[http.CanonicalHeaderKey(k)] {
			return model.Errorf(model.CodeInvalid, "请求头 %s 由 MCP 传输自行管理，不能配置", k)
		}
		if strings.ContainsAny(v, "\r\n\x00") {
			return model.Errorf(model.CodeInvalid, "请求头 %s 的值含有换行或空字符", k)
		}
		if ref, ok := strings.CutPrefix(v, SecretPrefix); ok && strings.TrimSpace(ref) == "" {
			return model.Errorf(model.CodeInvalid, "请求头 %s 的密钥引用为空", k)
		}
	}
	return nil
}

// validHeaderName 判断是否为 RFC 9110 的 token（字段名只允许这些字符）。
func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// validateHTTPURL 校验 streamable HTTP 端点。
func validateHTTPURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return model.NewError(model.CodeInvalid, "http 传输必须指定 url")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return model.Wrap(model.CodeInvalid, "url 无法解析", err)
	}
	host := u.Hostname()
	if host == "" {
		return model.NewError(model.CodeInvalid, "url 缺少主机名")
	}
	if u.User != nil {
		return model.NewError(model.CodeInvalid, "url 中不允许携带用户名或密码")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isPrivateOrLocalHost(host) {
			return nil
		}
		return model.Errorf(model.CodeInvalid, "公网 MCP 服务 %q 必须使用 https", host)
	default:
		return model.Errorf(model.CodeInvalid, "url 只支持 http 或 https，收到 %q", u.Scheme)
	}
}

// isPrivateOrLocalHost 判断主机是否为回环、私有网段、链路本地地址或常见的本地主机名。
func isPrivateOrLocalHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "" {
		return false
	}
	switch h {
	case "localhost", "host.docker.internal", "gateway.docker.internal":
		return true
	}
	for _, suffix := range []string{".localhost", ".local", ".internal", ".test"} {
		if strings.HasSuffix(h, suffix) {
			return true
		}
	}
	ip := net.ParseIP(h)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
}

// buildEnv 组装 stdio 子进程的环境：白名单变量（config.ChildEnvAllowlist，全仓唯一定义）→
// 配置里的显式变量（"secret:<ref>" 经 resolve 替换）。lookup 通常是 os.LookupEnv，抽出来便于测试。
func buildEnv(ctx context.Context, extra map[string]string, resolve EnvResolver, lookup func(string) (string, bool)) ([]string, error) {
	if lookup == nil {
		lookup = os.LookupEnv
	}
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

	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v, err := resolveValue(ctx, "环境变量", k, extra[k], resolve)
		if err != nil {
			return nil, err
		}
		set(k, v)
	}

	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, k+"="+vars[k])
	}
	return out, nil
}

// resolveHeaders 解析 http 传输的附加请求头："secret:<ref>" 经 resolve 替换为实际值。
// 返回的值只进请求头；错误信息只含键名与引用名。
func resolveHeaders(ctx context.Context, headers map[string]string, resolve EnvResolver) (http.Header, error) {
	if len(headers) == 0 {
		return nil, nil
	}
	out := make(http.Header, len(headers))
	for k, raw := range headers {
		v, err := resolveValue(ctx, "请求头", k, raw, resolve)
		if err != nil {
			return nil, err
		}
		if strings.ContainsAny(v, "\r\n\x00") {
			return nil, model.Errorf(model.CodeInvalid, "请求头 %s 解析后的值含有换行或空字符", k)
		}
		out.Set(k, v)
	}
	return out, nil
}

// resolveValue 把字面量原样返回，把 "secret:<ref>" 经 resolve 解析为实际值。what 与 key 只用于错误信息。
func resolveValue(ctx context.Context, what, key, value string, resolve EnvResolver) (string, error) {
	ref, ok := strings.CutPrefix(value, SecretPrefix)
	if !ok {
		return value, nil
	}
	if resolve == nil {
		return "", model.Errorf(model.CodeInvalid, "%s %s 引用了密钥，但没有提供密钥解析器", what, key)
	}
	if strings.TrimSpace(ref) == "" {
		return "", model.Errorf(model.CodeInvalid, "%s %s 的密钥引用为空", what, key)
	}
	resolved, err := resolve(ctx, ref)
	if err != nil {
		// 只记录键名，不记录值。
		return "", fmt.Errorf("解析%s %s 的密钥引用失败: %w", what, key, err)
	}
	return resolved, nil
}
