package tools

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// rewriteTransport 把任何请求都发到 target（httptest 服务器），保留原始 Host 供路由。
type rewriteTransport struct {
	target *url.URL
}

func (r rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = r.target.Scheme
	clone.URL.Host = r.target.Host
	clone.Host = req.URL.Host
	return http.DefaultTransport.RoundTrip(clone)
}

// fakeLookup 按主机名返回地址；未配置的主机报错。
func fakeLookup(table map[string][]string) lookupFunc {
	return func(_ context.Context, host string) ([]netip.Addr, error) {
		raw, ok := table[host]
		if !ok {
			return nil, errors.New("no such host: " + host)
		}
		out := make([]netip.Addr, 0, len(raw))
		for _, r := range raw {
			out = append(out, netip.MustParseAddr(r))
		}
		return out, nil
	}
}

func newFetchServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/html", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><head><title>标题</title><style>p{}</style><script>alert(1)</script></head>
<body><h1>Hello &amp; welcome</h1><p>第一段   文本</p><!-- 注释 --><ul><li>一</li><li>二</li></ul><a href="/x">链接</a></body></html>`))
	})
	mux.HandleFunc("/json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/bin", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{0x89, 'P', 'N', 'G', 0, 1, 2})
	})
	mux.HandleFunc("/big", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(strings.Repeat("a", 5000)))
	})
	mux.HandleFunc("/gbk", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=gbk")
		_, _ = w.Write([]byte("\xc4\xe3\xba\xc3"))
	})
	mux.HandleFunc("/missing", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	})
	mux.HandleFunc("/redirect-private", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://internal.test/admin", http.StatusFound)
	})
	mux.HandleFunc("/redirect-ok", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://public.test/json", http.StatusFound)
	})
	mux.HandleFunc("/redirect-ftp", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "ftp://public.test/x", http.StatusFound)
	})
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://public.test/loop", http.StatusFound)
	})
	mux.HandleFunc("/slow", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
		_, _ = w.Write([]byte("late"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// newTestFetch 构造一个把所有请求转到 httptest 服务器、主机解析可控的 fetch_url。
func newTestFetch(t *testing.T, srv *httptest.Server, hosts map[string][]string) fetchURL {
	t.Helper()
	target, _ := url.Parse(srv.URL)
	lookup := fakeLookup(hosts)
	client := newSafeHTTPClient(fetchTimeout, fetchMaxRedirects, lookup)
	client.Transport = rewriteTransport{target: target}
	return newFetchURL(fetchOptions{client: client, lookup: lookup})
}

var publicHosts = map[string][]string{
	"public.test":   {"93.184.216.34"},
	"internal.test": {"10.0.0.5"},
	"mixed.test":    {"93.184.216.34", "192.168.1.1"},
	"v6.test":       {"2606:2800:220:1:248:1893:25c8:1946"},
}

func TestFetchURLRendersHTMLAsText(t *testing.T) {
	srv := newFetchServer(t)
	tool := newTestFetch(t, srv, publicHosts)
	res, err := tool.Execute(context.Background(), RunContext{}, rawArgs(t, map[string]any{"url": "http://public.test/html"}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("200 不应是错误: %+v", res)
	}
	for _, want := range []string{"状态: 200", "不可信数据", "标题", "Hello & welcome", "第一段 文本", "一\n二", "链接"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("结果缺少 %q:\n%s", want, res.Content)
		}
	}
	for _, bad := range []string{"alert(1)", "p{}", "注释", "<h1>", "href"} {
		if strings.Contains(res.Content, bad) {
			t.Errorf("结果不应包含 %q:\n%s", bad, res.Content)
		}
	}
}

func TestFetchURLPassesThroughJSONAndSummarisesBinary(t *testing.T) {
	srv := newFetchServer(t)
	tool := newTestFetch(t, srv, publicHosts)
	res, err := tool.Execute(context.Background(), RunContext{}, rawArgs(t, map[string]any{"url": "http://public.test/json"}))
	if err != nil || !strings.HasSuffix(res.Content, `{"ok":true}`) {
		t.Fatalf("JSON 应原样返回: %+v %v", res, err)
	}
	res, err = tool.Execute(context.Background(), RunContext{}, rawArgs(t, map[string]any{"url": "http://public.test/bin"}))
	if err != nil || !strings.Contains(res.Content, "[非文本内容：image/png，7 字节") || strings.Contains(res.Content, "PNG") {
		t.Fatalf("二进制应只给摘要: %+v %v", res, err)
	}
	res, err = tool.Execute(context.Background(), RunContext{}, rawArgs(t, map[string]any{"url": "http://public.test/gbk"}))
	if err != nil || !strings.Contains(res.Content, "声明的字符集为 gbk") || strings.Contains(res.Content, "\xc4") {
		t.Fatalf("非 UTF-8 字符集应提示并替换非法字节: %+v %v", res, err)
	}
	res, err = tool.Execute(context.Background(), RunContext{}, rawArgs(t, map[string]any{"url": "http://public.test/missing"}))
	if err != nil || !res.IsError || !strings.Contains(res.Content, "状态: 404") {
		t.Fatalf("4xx 应标记为错误但仍返回内容: %+v %v", res, err)
	}
	res, err = tool.Execute(context.Background(), RunContext{}, rawArgs(t, map[string]any{"url": "http://v6.test/json"}))
	if err != nil || res.IsError {
		t.Fatalf("公网 IPv6 应允许: %+v %v", res, err)
	}
}

func TestFetchURLTruncatesBody(t *testing.T) {
	srv := newFetchServer(t)
	tool := newTestFetch(t, srv, publicHosts)
	res, err := tool.Execute(context.Background(), RunContext{}, rawArgs(t, map[string]any{"url": "http://public.test/big", "max_bytes": 1000}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "[正文超过 1000 字节，已截断]") || !strings.HasSuffix(res.Content, "\n---\n"+strings.Repeat("a", 1000)) {
		t.Fatalf("正文应按 max_bytes 截断: %d 字节", len(res.Content))
	}
	if !res.Truncated {
		t.Fatal("正文被截断时 ToolResult.Truncated 应为 true")
	}
}

func TestFetchURLRejectsUnsafeTargets(t *testing.T) {
	srv := newFetchServer(t)
	tool := newTestFetch(t, srv, publicHosts)
	cases := map[string]model.Code{
		"":                                    model.CodeInvalid,
		"ftp://public.test/x":                 model.CodeInvalid,
		"file:///etc/passwd":                  model.CodeInvalid,
		"http:///nohost":                      model.CodeInvalid,
		"http://user:pw@public.test/":         model.CodeInvalid,
		"http://localhost/":                   model.CodeDenied,
		"http://app.localhost/":               model.CodeDenied,
		"http://127.0.0.1/":                   model.CodeDenied,
		"http://[::1]/":                       model.CodeDenied,
		"http://10.0.0.1/":                    model.CodeDenied,
		"http://192.168.1.1/":                 model.CodeDenied,
		"http://172.16.5.5/":                  model.CodeDenied,
		"http://169.254.169.254/latest/":      model.CodeDenied,
		"http://100.64.1.1/":                  model.CodeDenied,
		"http://0.0.0.0/":                     model.CodeDenied,
		"http://[::ffff:127.0.0.1]/":          model.CodeDenied,
		"http://[fe80::1]/":                   model.CodeDenied,
		"http://[fc00::1]/":                   model.CodeDenied,
		"http://[2002:7f00:1::1]/":            model.CodeDenied,
		"http://internal.test/":               model.CodeDenied,
		"http://mixed.test/":                  model.CodeDenied,
		"http://unknown.test/":                model.CodeInvalid,
		"http://public.test/redirect-private": model.CodeDenied,
		"http://public.test/redirect-ftp":     model.CodeInvalid,
		"http://public.test/loop":             model.CodeInvalid,
	}
	for raw, want := range cases {
		_, err := tool.Execute(context.Background(), RunContext{}, rawArgs(t, map[string]any{"url": raw}))
		if !model.IsCode(err, want) {
			t.Errorf("%q 应为 %s，得到 %v", raw, want, err)
		}
	}
	res, err := tool.Execute(context.Background(), RunContext{}, rawArgs(t, map[string]any{"url": "http://public.test/redirect-ok"}))
	if err != nil || res.IsError || !strings.HasSuffix(res.Content, `{"ok":true}`) {
		t.Fatalf("到公网主机的重定向应允许: %+v %v", res, err)
	}
}

func TestFetchURLHonoursContextAndTimeout(t *testing.T) {
	srv := newFetchServer(t)
	tool := newTestFetch(t, srv, publicHosts)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := tool.Execute(ctx, RunContext{}, rawArgs(t, map[string]any{"url": "http://public.test/slow"}))
	if !model.IsCode(err, model.CodeTimeout) {
		t.Fatalf("ctx 超时应为 CodeTimeout，得到 %v", err)
	}
}

func TestIsPublicAddr(t *testing.T) {
	allowed := []string{"93.184.216.34", "8.8.8.8", "1.1.1.1", "2606:2800:220:1:248:1893:25c8:1946", "2002:5db8:d822::1", "::ffff:8.8.8.8"}
	for _, a := range allowed {
		if !isPublicAddr(netip.MustParseAddr(a)) {
			t.Errorf("%s 应被允许", a)
		}
	}
	blocked := []string{
		"127.0.0.1", "127.255.255.255", "10.1.2.3", "172.31.0.1", "192.168.0.1", "169.254.1.1", "100.100.0.1",
		"0.0.0.0", "0.1.2.3", "192.0.0.1", "192.0.2.1", "198.18.0.1", "198.51.100.1", "203.0.113.1",
		"224.0.0.1", "240.0.0.1", "255.255.255.255",
		"::1", "::", "fe80::1", "fc00::1", "fd12::1", "ff02::1", "2001:db8::1", "64:ff9b::7f00:1",
		"::ffff:10.0.0.1", "::ffff:127.0.0.1", "2002:7f00:1::1", "2002:0a00:1::1",
	}
	for _, a := range blocked {
		if isPublicAddr(netip.MustParseAddr(a)) {
			t.Errorf("%s 应被拒绝", a)
		}
	}
	if isPublicAddr(netip.Addr{}) {
		t.Error("无效地址应被拒绝")
	}
}

func TestDialControlBlocksPrivateAtConnectTime(t *testing.T) {
	if err := dialControl("tcp", "127.0.0.1:80", nil); !errors.Is(err, errPrivateAddr) {
		t.Fatalf("回环地址应在连接前被拒绝，得到 %v", err)
	}
	if err := dialControl("tcp", "[::1]:80", nil); !errors.Is(err, errPrivateAddr) {
		t.Fatalf("IPv6 回环应被拒绝，得到 %v", err)
	}
	if err := dialControl("tcp", "93.184.216.34:443", nil); err != nil {
		t.Fatalf("公网地址应放行，得到 %v", err)
	}
	if err := dialControl("tcp", "garbage", nil); err == nil {
		t.Fatal("无法解析的地址应报错")
	}

	// 真实的安全客户端：即使绕过主机名校验直接拨号到回环地址，也会在 Control 阶段被拒绝。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	client := newSafeHTTPClient(2*time.Second, 1, defaultLookup)
	tr := client.Transport.(*http.Transport)
	if _, err := tr.DialContext(context.Background(), "tcp", ln.Addr().String()); !errors.Is(err, errPrivateAddr) {
		t.Fatalf("安全客户端应拒绝连接回环地址，得到 %v", err)
	}
	if tr.Proxy != nil {
		t.Fatal("安全客户端不应使用环境代理")
	}
}

func TestHTMLToText(t *testing.T) {
	cases := []struct{ in, want string }{
		{"<p>a</p><p>b</p>", "a\nb"},
		{"<div>x<br>y</div>", "x\ny"},
		{"<script>var a = '<p>';</script>text", "text"},
		{"<STYLE>p{}</STYLE><B>bold</B> &lt;tag&gt;", "bold <tag>"},
		{"<table><tr><td>1</td><td>2</td></tr></table>", "1 2"},
		{"a\n\n\n\nb", "a\nb"},
		{"<!-- c --><h1> Title </h1><p>  x   y </p>", "Title\nx y"},
		{"<p>unclosed", "unclosed"},
		{"<script>never closed", ""},
		{"<a href='x>y'>bad</a>", "y'>bad"},
	}
	for _, c := range cases {
		if got := htmlToText(c.in); got != c.want {
			t.Errorf("htmlToText(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
	if !looksLikeHTML("  <!DOCTYPE html><html>") || looksLikeHTML("{\"a\":1}") {
		t.Error("looksLikeHTML 判断不正确")
	}
}

func TestFetchURLDescribe(t *testing.T) {
	srv := newFetchServer(t)
	tool := newTestFetch(t, srv, publicHosts)
	d, err := tool.Describe(rawArgs(t, map[string]any{"url": " http://public.test/a "}))
	if err != nil || d.Summary != "获取网页 http://public.test/a" || d.Details["url"] != "http://public.test/a" {
		t.Fatalf("Describe 不正确: %+v %v", d, err)
	}
	if _, err := tool.Describe(rawArgs(t, map[string]any{"url": "javascript:alert(1)"})); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("非 http 方案应在 Describe 阶段就拒绝，得到 %v", err)
	}
}
