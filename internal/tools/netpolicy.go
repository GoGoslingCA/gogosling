package tools

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// 本文件实现对外请求的 SSRF 防护：
//   - URL 只允许 http / https，必须有主机名，不允许内嵌用户名密码；
//   - 主机解析出的每个地址都不得是回环、私网、链路本地、组播、保留或未指定地址；
//   - 建立 TCP 连接时在 Dialer.Control 里对真正要连接的地址再查一次，封堵 DNS 重绑定；
//   - 不走环境变量代理：经代理的请求由代理去连内网，校验会被绕过。

// errPrivateAddr 是地址被拒绝时的底层错误。
var errPrivateAddr = errors.New("目标解析到回环、私网或保留地址")

// blockedV4 是除 Go 标准库 IsPrivate / IsLoopback / IsLinkLocal* 之外还要拒绝的 IPv4 段。
var blockedV4 = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // 本网络
	netip.MustParsePrefix("100.64.0.0/10"),   // CGNAT
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF 协议分配
	netip.MustParsePrefix("192.0.2.0/24"),    // TEST-NET-1
	netip.MustParsePrefix("198.18.0.0/15"),   // 基准测试
	netip.MustParsePrefix("198.51.100.0/24"), // TEST-NET-2
	netip.MustParsePrefix("203.0.113.0/24"),  // TEST-NET-3
	netip.MustParsePrefix("240.0.0.0/4"),     // 保留（含广播地址）
}

// blockedV6 是额外拒绝的 IPv6 段。
var blockedV6 = []netip.Prefix{
	netip.MustParsePrefix("2001:db8::/32"), // 文档
	netip.MustParsePrefix("64:ff9b::/96"),  // NAT64：内嵌 IPv4，可能指向内网
	netip.MustParsePrefix("::/128"),        // 未指定
}

// isPublicAddr 判断地址是否可以作为对外请求的目标。
func isPublicAddr(a netip.Addr) bool {
	if !a.IsValid() {
		return false
	}
	a = a.Unmap()
	if a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsInterfaceLocalMulticast() || a.IsMulticast() || a.IsUnspecified() {
		return false
	}
	if a.Is4() {
		for _, p := range blockedV4 {
			if p.Contains(a) {
				return false
			}
		}
		return true
	}
	for _, p := range blockedV6 {
		if p.Contains(a) {
			return false
		}
	}
	// 6to4（2002::/16）内嵌了一个 IPv4 地址，按内嵌地址判断。
	if six := a.As16(); six[0] == 0x20 && six[1] == 0x02 {
		return isPublicAddr(netip.AddrFrom4([4]byte{six[2], six[3], six[4], six[5]}))
	}
	return true
}

// validateFetchURL 校验 URL 的形式：只允许 http(s)，必须有主机，不允许用户信息。
func validateFetchURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, model.NewError(model.CodeInvalid, "url 不能为空")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, model.Wrap(model.CodeInvalid, "url 不合法", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, model.Errorf(model.CodeInvalid, "只支持 http 与 https，不支持 %q", u.Scheme)
	}
	u.Scheme = scheme
	if u.Hostname() == "" {
		return nil, model.NewError(model.CodeInvalid, "url 缺少主机名")
	}
	if u.User != nil {
		return nil, model.NewError(model.CodeInvalid, "url 不允许包含用户名或密码")
	}
	return u, nil
}

// lookupFunc 解析主机名；默认为 net.DefaultResolver.LookupNetIP。
type lookupFunc func(ctx context.Context, host string) ([]netip.Addr, error)

// defaultLookup 用系统解析器查询 A 与 AAAA 记录。
func defaultLookup(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// checkHost 解析主机名并要求全部地址都是公网地址；字面 IP 直接判断。
// 任一地址被拒绝就整体拒绝：攻击者可以让域名同时返回公网与内网地址，客户端会任选其一。
func checkHost(ctx context.Context, host string, lookup lookupFunc) error {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return model.NewError(model.CodeDenied, "拒绝访问本机地址 "+host)
	}
	if a, err := netip.ParseAddr(host); err == nil {
		if !isPublicAddr(a) {
			return model.Errorf(model.CodeDenied, "拒绝访问回环、私网或保留地址 %s", host)
		}
		return nil
	}
	addrs, err := lookup(ctx, host)
	if err != nil {
		return model.Wrap(model.CodeInvalid, "无法解析主机 "+host, err)
	}
	if len(addrs) == 0 {
		return model.Errorf(model.CodeInvalid, "主机 %s 没有解析到地址", host)
	}
	for _, a := range addrs {
		if !isPublicAddr(a) {
			return model.Errorf(model.CodeDenied, "拒绝访问：%s 解析到回环、私网或保留地址 %s", host, a)
		}
	}
	return nil
}

// dialControl 在真正建立连接前检查目标地址，封堵"校验时解析到公网、连接时解析到内网"的重绑定攻击。
func dialControl(_ string, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return err
	}
	if !isPublicAddr(ap.Addr()) {
		return errPrivateAddr
	}
	return nil
}

// newSafeHTTPClient 构造只能访问公网的 HTTP 客户端：Dialer.Control 校验地址、不走代理、
// 重定向最多 maxRedirects 次且每跳重新校验主机。
func newSafeHTTPClient(timeout time.Duration, maxRedirects int, lookup lookupFunc) *http.Client {
	dialer := &net.Dialer{Timeout: timeout, Control: dialControl}
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            dialer.DialContext,
		TLSHandshakeTimeout:    timeout,
		ResponseHeaderTimeout:  timeout,
		MaxResponseHeaderBytes: 64 << 10,
		DisableKeepAlives:      true,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return model.Errorf(model.CodeInvalid, "重定向超过 %d 次", maxRedirects)
			}
			if _, err := validateFetchURL(req.URL.String()); err != nil {
				return err
			}
			return checkHost(req.Context(), req.URL.Hostname(), lookup)
		},
	}
}
