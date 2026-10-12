package mcp

import (
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// headerTransport 给发往配置端点的每个请求注入附加请求头（例如 Authorization）。
//
// 只注入到与端点同 scheme、同 host 的请求：http.Client 跟随重定向时也会经过这里，
// 若服务端把请求重定向到别的主机，令牌不能跟着过去。
type headerTransport struct {
	base    http.RoundTripper
	scheme  string
	host    string
	headers http.Header
}

// RoundTrip 实现 http.RoundTripper；按约定不修改传入的请求，而是在副本上设置请求头。
func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL == nil || !strings.EqualFold(req.URL.Scheme, t.scheme) || !strings.EqualFold(req.URL.Host, t.host) {
		return t.base.RoundTrip(req)
	}
	r := req.Clone(req.Context())
	for k, vs := range t.headers {
		r.Header.Del(k)
		for _, v := range vs {
			r.Header.Add(k, v)
		}
	}
	return t.base.RoundTrip(r)
}

// withHeaders 返回注入 headers 的 http.Client；headers 为空时原样返回 base（可能为 nil，即默认客户端）。
// base 不会被修改：复制一份并替换 Transport。
func withHeaders(base *http.Client, endpoint string, headers http.Header) (*http.Client, error) {
	if len(headers) == 0 {
		return base, nil
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, model.Wrap(model.CodeInvalid, "url 无法解析", err)
	}
	var c http.Client
	if base != nil {
		c = *base
	}
	rt := c.Transport
	if rt == nil {
		rt = http.DefaultTransport
	}
	c.Transport = &headerTransport{base: rt, scheme: u.Scheme, host: u.Host, headers: headers}
	return &c, nil
}

// headerNames 返回排序后的请求头名，用于日志（不含值）。
func headerNames(h http.Header) []string {
	names := make([]string, 0, len(h))
	for k := range h {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
