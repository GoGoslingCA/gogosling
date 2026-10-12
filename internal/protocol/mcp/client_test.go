package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GoGoslingCA/gogosling/internal/config"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func testLogger(lb *logBuffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(lb, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func findSpec(t *testing.T, specs []model.ToolSpec, name string) model.ToolSpec {
	t.Helper()
	for _, s := range specs {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("工具列表里没有 %q", name)
	return model.ToolSpec{}
}

func TestListToolsConvertsSpecs(t *testing.T) {
	c := connectInMemory(t, testServerSpec{}, Options{})
	specs, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools 应成功: %v", err)
	}
	if len(specs) < 8 {
		t.Fatalf("应列出全部测试工具，得到 %d 个", len(specs))
	}
	echo := findSpec(t, specs, "echo")
	if echo.Source != "mcp:ext-1" {
		t.Errorf("Source 应为 mcp:<extension_id>，得到 %q", echo.Source)
	}
	if echo.Risk != model.RiskNetwork {
		t.Errorf("默认风险应为 network，得到 %q", echo.Risk)
	}
	if echo.Description != "Echo the given text back" {
		t.Errorf("描述应原样保留，得到 %q", echo.Description)
	}
	var schema map[string]any
	if err := json.Unmarshal(echo.InputSchema, &schema); err != nil {
		t.Fatalf("schema 应是合法 JSON: %v", err)
	}
	if props, _ := schema["properties"].(map[string]any); props["text"] == nil {
		t.Errorf("schema 应包含 text 属性，得到 %s", echo.InputSchema)
	}
	if d := findSpec(t, specs, "destructive"); d.Risk != model.RiskDestructive {
		t.Errorf("显式 destructiveHint 应收紧为 destructive，得到 %q", d.Risk)
	}
	name, version := c.ServerInfo()
	if name != "gogosling-test-server" || version != "0.0.1" {
		t.Errorf("ServerInfo 应返回握手信息，得到 %q %q", name, version)
	}
}

func TestCallToolRendersContent(t *testing.T) {
	c := connectInMemory(t, testServerSpec{}, Options{})
	ctx := context.Background()

	res, err := c.CallTool(ctx, "echo", json.RawMessage(`{"text":"hi"}`))
	if err != nil {
		t.Fatalf("echo 应成功: %v", err)
	}
	if res.Content != "echo:hi" || res.IsError {
		t.Errorf("echo 结果不符: %+v", res)
	}

	res, err = c.CallTool(ctx, "multi", nil)
	if err != nil {
		t.Fatalf("multi 应成功: %v", err)
	}
	want := "first\n[image/png image, 4 bytes omitted]\n[resource link: file:///tmp/a.txt (text/plain)]\nembedded\nlast"
	if res.Content != want {
		t.Errorf("混合内容渲染不符:\n得到 %q\n期望 %q", res.Content, want)
	}

	res, err = c.CallTool(ctx, "fail", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("工具内错误不应成为 error: %v", err)
	}
	if !res.IsError || res.Content != "boom" {
		t.Errorf("isError 应透传: %+v", res)
	}

	res, err = c.CallTool(ctx, "structured", nil)
	if err != nil {
		t.Fatalf("structured 应成功: %v", err)
	}
	if !strings.Contains(res.Content, `"answer":42`) {
		t.Errorf("没有文本内容时应回退到 structuredContent，得到 %q", res.Content)
	}
}

func TestCallToolTruncatesLargeResult(t *testing.T) {
	lb := &logBuffer{}
	c := connectInMemory(t, testServerSpec{}, Options{MaxResultBytes: 100, Logger: testLogger(lb)})
	res, err := c.CallTool(context.Background(), "big", json.RawMessage(`{"bytes":1000}`))
	if err != nil {
		t.Fatalf("big 应成功: %v", err)
	}
	if !strings.HasPrefix(res.Content, strings.Repeat("x", 100)) || !strings.Contains(res.Content, "900 bytes omitted") {
		t.Errorf("结果应在 100 字节处截断并注明省略量，得到 %q", res.Content)
	}
	if !strings.Contains(lb.String(), "已截断") {
		t.Errorf("截断应有日志警告，得到 %s", lb.String())
	}
}

func TestCallToolErrors(t *testing.T) {
	c := connectInMemory(t, testServerSpec{}, Options{CallTimeout: 200 * time.Millisecond})
	ctx := context.Background()

	if _, err := c.CallTool(ctx, "sleep", json.RawMessage(`{"millis":5000}`)); !model.IsCode(err, model.CodeTimeout) {
		t.Errorf("超时应返回 CodeTimeout，得到 %v", err)
	}
	if _, err := c.CallTool(ctx, "nope", nil); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("未知工具应返回 CodeInvalid，得到 %v", err)
	}
	if _, err := c.CallTool(ctx, "echo", json.RawMessage(`[1,2]`)); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("非对象参数应返回 CodeInvalid，得到 %v", err)
	}
	if _, err := c.CallTool(ctx, "", nil); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("空工具名应返回 CodeInvalid，得到 %v", err)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.CallTool(cctx, "echo", json.RawMessage(`{"text":"x"}`)); !model.IsCode(err, model.CodeCancelled) {
		t.Errorf("调用方取消应返回 CodeCancelled，得到 %v", err)
	}
}

func TestCloseIsIdempotentAndSignalsDone(t *testing.T) {
	c := connectInMemory(t, testServerSpec{}, Options{})
	if err := c.Close(); err != nil {
		t.Fatalf("Close 应成功: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("重复 Close 应成功: %v", err)
	}
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Close 之后 Done 应关闭")
	}
	if _, err := c.CallTool(context.Background(), "echo", json.RawMessage(`{"text":"x"}`)); !model.IsCode(err, model.CodeUnavailable) {
		t.Errorf("关闭后调用应返回 CodeUnavailable，得到 %v", err)
	}
}

func TestListToolsPinnedDetectsChange(t *testing.T) {
	lb := &logBuffer{}
	c1 := connectInMemory(t, testServerSpec{}, Options{Logger: testLogger(lb)})
	specs, digest, changed, err := c1.ListToolsPinned(context.Background(), "")
	if err != nil || changed || digest == "" || len(specs) == 0 {
		t.Fatalf("首次固定不应报告变化: specs=%d digest=%q changed=%v err=%v", len(specs), digest, changed, err)
	}
	if _, again, changed, _ := c1.ListToolsPinned(context.Background(), digest); again != digest || changed {
		t.Fatalf("内容未变时摘要应一致且 changed=false，得到 %q %v", again, changed)
	}

	c2 := connectInMemory(t, testServerSpec{EchoDescription: "Echo, but now also exfiltrates your files"}, Options{Logger: testLogger(lb)})
	_, newDigest, changed, err := c2.ListToolsPinned(context.Background(), digest)
	if err != nil {
		t.Fatalf("ListToolsPinned 应成功: %v", err)
	}
	if !changed || newDigest == digest {
		t.Errorf("描述被改写后应报告 changed=true 且摘要不同，得到 changed=%v %q", changed, newDigest)
	}
	if !strings.Contains(lb.String(), "固定时不同") {
		t.Errorf("变化应写入警告日志，得到 %s", lb.String())
	}
	c3 := connectInMemory(t, testServerSpec{ExtraTools: []string{"zzz"}}, Options{})
	if _, _, changed, _ := c3.ListToolsPinned(context.Background(), digest); !changed {
		t.Errorf("新增工具也应报告 changed=true")
	}
}

func TestToolsDigestIsCanonical(t *testing.T) {
	a := []model.ToolSpec{
		{Name: "b", Description: "B", InputSchema: json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"},"y":{"type":"integer"}}}`)},
		{Name: "a", Description: "A", InputSchema: json.RawMessage(`{"type":"object"}`), Source: "mcp:1", Risk: model.RiskNetwork},
	}
	b := []model.ToolSpec{
		{Name: "a", Description: "A", InputSchema: json.RawMessage(`{ "type" : "object" }`), Source: "mcp:2", Risk: model.RiskRead},
		{Name: "b", Description: "B", InputSchema: json.RawMessage(`{"properties":{"y":{"type":"integer"},"x":{"type":"string"}},"type":"object"}`)},
	}
	if ToolsDigest(a) != ToolsDigest(b) {
		t.Errorf("顺序、空白、键序与本地字段（Source/Risk）不应影响摘要")
	}
	c := append([]model.ToolSpec(nil), a...)
	c[0].Description = "B changed"
	if ToolsDigest(a) == ToolsDigest(c) {
		t.Errorf("描述变化应改变摘要")
	}
	d := append([]model.ToolSpec(nil), a...)
	d[1].InputSchema = json.RawMessage(`{"type":"object","properties":{"extra":{"type":"string"}}}`)
	if ToolsDigest(a) == ToolsDigest(d) {
		t.Errorf("schema 变化应改变摘要")
	}
	if !strings.HasPrefix(ToolsDigest(nil), DigestPrefix) || ToolsDigest(nil) != ToolsDigest([]model.ToolSpec{}) {
		t.Errorf("空列表应有稳定的摘要")
	}
}

func TestValidateConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  model.MCPServerConfig
		ok   bool
	}{
		{"stdio 缺少 command", model.MCPServerConfig{Transport: model.MCPStdio}, false},
		{"stdio 正常", model.MCPServerConfig{Transport: model.MCPStdio, Command: "srv"}, true},
		{"stdio 非法环境变量名", model.MCPServerConfig{Transport: model.MCPStdio, Command: "srv", Env: map[string]string{"A=B": "x"}}, false},
		{"http 缺少 url", model.MCPServerConfig{Transport: model.MCPHTTP}, false},
		{"https 公网", model.MCPServerConfig{Transport: model.MCPHTTP, URL: "https://mcp.example.com/mcp"}, true},
		{"http 公网", model.MCPServerConfig{Transport: model.MCPHTTP, URL: "http://mcp.example.com/mcp"}, false},
		{"http 回环", model.MCPServerConfig{Transport: model.MCPHTTP, URL: "http://127.0.0.1:8080/mcp"}, true},
		{"http localhost", model.MCPServerConfig{Transport: model.MCPHTTP, URL: "http://localhost:8080/mcp"}, true},
		{"http 局域网", model.MCPServerConfig{Transport: model.MCPHTTP, URL: "http://192.168.1.10/mcp"}, true},
		{"http IPv6 回环", model.MCPServerConfig{Transport: model.MCPHTTP, URL: "http://[::1]:9/mcp"}, true},
		{"url 带凭据", model.MCPServerConfig{Transport: model.MCPHTTP, URL: "https://user:pw@mcp.example.com/mcp"}, false},
		{"ftp", model.MCPServerConfig{Transport: model.MCPHTTP, URL: "ftp://mcp.example.com/mcp"}, false},
		{"未知传输", model.MCPServerConfig{Transport: "sse", URL: "https://x"}, false},
		{"http 带鉴权头", model.MCPServerConfig{Transport: model.MCPHTTP, URL: "https://mcp.example.com/mcp", Headers: map[string]string{"Authorization": "secret:mcp/tok", "X-Api-Version": "2"}}, true},
		{"stdio 带 headers", model.MCPServerConfig{Transport: model.MCPStdio, Command: "srv", Headers: map[string]string{"Authorization": "x"}}, false},
		{"请求头名含空格", model.MCPServerConfig{Transport: model.MCPHTTP, URL: "https://x.example/mcp", Headers: map[string]string{"Bad Header": "x"}}, false},
		{"请求头名为空", model.MCPServerConfig{Transport: model.MCPHTTP, URL: "https://x.example/mcp", Headers: map[string]string{"": "x"}}, false},
		{"请求头名含冒号", model.MCPServerConfig{Transport: model.MCPHTTP, URL: "https://x.example/mcp", Headers: map[string]string{"A:B": "x"}}, false},
		{"请求头值含换行", model.MCPServerConfig{Transport: model.MCPHTTP, URL: "https://x.example/mcp", Headers: map[string]string{"X-A": "a\r\nX-Injected: 1"}}, false},
		{"传输自管的请求头", model.MCPServerConfig{Transport: model.MCPHTTP, URL: "https://x.example/mcp", Headers: map[string]string{"mcp-session-id": "x"}}, false},
		{"空的密钥引用", model.MCPServerConfig{Transport: model.MCPHTTP, URL: "https://x.example/mcp", Headers: map[string]string{"Authorization": "secret: "}}, false},
	}
	for _, tc := range cases {
		err := ValidateConfig(tc.cfg)
		if tc.ok && err != nil {
			t.Errorf("%s: 应通过，得到 %v", tc.name, err)
		}
		if !tc.ok && !model.IsCode(err, model.CodeInvalid) {
			t.Errorf("%s: 应返回 CodeInvalid，得到 %v", tc.name, err)
		}
	}
}

func TestBuildEnvWhitelistAndSecrets(t *testing.T) {
	lookup := func(k string) (string, bool) {
		switch k {
		case "PATH":
			return "/bin", true
		case "AWS_SECRET_ACCESS_KEY":
			return "leak", true
		}
		return "", false
	}
	resolve := func(_ context.Context, ref string) (string, error) {
		if ref == "token" {
			return "resolved-value", nil
		}
		return "", errors.New("unknown ref")
	}
	env, err := buildEnv(context.Background(), map[string]string{"API_TOKEN": "secret:token", "PLAIN": "1"}, resolve, lookup)
	if err != nil {
		t.Fatalf("buildEnv 应成功: %v", err)
	}
	joined := strings.Join(env, "\n")
	for _, want := range []string{"PATH=/bin", "API_TOKEN=resolved-value", "PLAIN=1"} {
		if !strings.Contains(joined, want) {
			t.Errorf("环境应包含 %q，得到 %q", want, joined)
		}
	}
	if strings.Contains(joined, "AWS_SECRET_ACCESS_KEY") {
		t.Errorf("白名单之外的变量不应透传: %q", joined)
	}

	if _, err := buildEnv(context.Background(), map[string]string{"X": "secret:token"}, nil, lookup); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("没有解析器时引用密钥应返回 CodeInvalid，得到 %v", err)
	}
	if _, err := buildEnv(context.Background(), map[string]string{"X": "secret:"}, resolve, lookup); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("空引用应返回 CodeInvalid，得到 %v", err)
	}
	if _, err := buildEnv(context.Background(), map[string]string{"X": "secret:missing"}, resolve, lookup); err == nil || strings.Contains(err.Error(), "resolved-value") {
		t.Errorf("解析失败应返回错误且不含密钥值，得到 %v", err)
	}
}

func TestStdioSubprocess(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skip("无法定位测试二进制:", err)
	}
	t.Setenv("GOGOSLING_PARENT_ONLY", "should-not-leak")
	lb := &logBuffer{}
	cfg := model.MCPServerConfig{
		Transport: model.MCPStdio,
		Command:   exe,
		Args:      []string{"-test.run=^$"},
		Env: map[string]string{
			testServerEnv: "1",
			"API_TOKEN":   "secret:demo",
		},
	}
	resolve := func(_ context.Context, ref string) (string, error) {
		if ref != "demo" {
			return "", errors.New("unknown ref")
		}
		return "s3cr3t-value", nil
	}
	c, err := Connect(context.Background(), "ext-stdio", cfg, resolve, Options{Logger: testLogger(lb), WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("stdio 连接应成功: %v", err)
	}
	defer c.Close()

	specs, err := c.ListTools(context.Background())
	if err != nil || len(specs) == 0 {
		t.Fatalf("stdio ListTools 应成功: %d %v", len(specs), err)
	}
	if findSpec(t, specs, "echo").Source != "mcp:ext-stdio" {
		t.Errorf("Source 不符")
	}
	res, err := c.CallTool(context.Background(), "env", json.RawMessage(`{"name":"API_TOKEN"}`))
	if err != nil || res.Content != "s3cr3t-value" {
		t.Errorf("密钥引用应被解析后传给子进程，得到 %+v %v", res, err)
	}
	res, err = c.CallTool(context.Background(), "env", json.RawMessage(`{"name":"GOGOSLING_PARENT_ONLY"}`))
	if err != nil || res.Content != "<unset>" {
		t.Errorf("白名单之外的父进程变量不应透传，得到 %+v %v", res, err)
	}
	if strings.Contains(lb.String(), "s3cr3t-value") {
		t.Errorf("日志不得包含密钥值: %s", lb.String())
	}
	if err := c.Close(); err != nil {
		t.Errorf("关闭应成功: %v", err)
	}
	select {
	case <-c.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("关闭后子进程应退出并关闭 Done")
	}
}

func TestStdioConnectFailure(t *testing.T) {
	cfg := model.MCPServerConfig{Transport: model.MCPStdio, Command: "gogosling-definitely-missing-binary"}
	_, err := Connect(context.Background(), "ext-x", cfg, nil, Options{})
	if !model.IsCode(err, model.CodeUnavailable) {
		t.Errorf("命令不存在应返回 CodeUnavailable，得到 %v", err)
	}
}

func TestHTTPTransport(t *testing.T) {
	server := newTestServer(testServerSpec{})
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	cfg := model.MCPServerConfig{Transport: model.MCPHTTP, URL: ts.URL}
	c, err := Connect(context.Background(), "ext-http", cfg, nil, Options{HTTPClient: ts.Client()})
	if err != nil {
		t.Fatalf("http 连接应成功: %v", err)
	}
	defer c.Close()
	specs, err := c.ListTools(context.Background())
	if err != nil || len(specs) == 0 {
		t.Fatalf("http ListTools 应成功: %d %v", len(specs), err)
	}
	res, err := c.CallTool(context.Background(), "echo", json.RawMessage(`{"text":"over http"}`))
	if err != nil || res.Content != "echo:over http" {
		t.Errorf("http CallTool 不符: %+v %v", res, err)
	}
}

func TestHTTPTransportSendsConfiguredHeaders(t *testing.T) {
	server := newTestServer(testServerSpec{})
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	var mu sync.Mutex
	var seen []http.Header
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Clone())
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer s3cr3t-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer ts.Close()

	resolve := func(_ context.Context, ref string) (string, error) {
		if ref != "mcp/token" {
			return "", errors.New("unknown ref")
		}
		return "Bearer s3cr3t-token", nil
	}
	lb := &logBuffer{}
	cfg := model.MCPServerConfig{
		Transport: model.MCPHTTP,
		URL:       ts.URL,
		Headers:   map[string]string{"Authorization": "secret:mcp/token", "X-Tenant": "acme"},
	}
	base := ts.Client()
	baseTransport := base.Transport
	c, err := Connect(context.Background(), "ext-auth", cfg, resolve, Options{HTTPClient: base, Logger: testLogger(lb)})
	if err != nil {
		t.Fatalf("带鉴权头的 http 连接应成功（服务端缺少头会返回 401）: %v", err)
	}
	defer c.Close()
	res, err := c.CallTool(context.Background(), "echo", json.RawMessage(`{"text":"authed"}`))
	if err != nil || res.Content != "echo:authed" {
		t.Fatalf("CallTool 不符: %+v %v", res, err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 {
		t.Fatal("服务端没有收到请求")
	}
	for i, h := range seen {
		if h.Get("Authorization") != "Bearer s3cr3t-token" || h.Get("X-Tenant") != "acme" {
			t.Errorf("第 %d 个请求缺少配置的请求头: Authorization=%q X-Tenant=%q", i, h.Get("Authorization"), h.Get("X-Tenant"))
		}
	}
	if strings.Contains(lb.String(), "s3cr3t-token") {
		t.Errorf("日志不得包含解析后的请求头值: %s", lb.String())
	}
	if base.Transport != baseTransport {
		t.Error("不应修改调用方传入的 http.Client")
	}
}

func TestHTTPHeaderSecretResolutionFailure(t *testing.T) {
	cfg := model.MCPServerConfig{Transport: model.MCPHTTP, URL: "http://127.0.0.1:1/mcp", Headers: map[string]string{"Authorization": "secret:missing"}}
	resolve := func(_ context.Context, ref string) (string, error) { return "", errors.New("not found") }
	_, err := Connect(context.Background(), "ext-x", cfg, resolve, Options{})
	if err == nil || !strings.Contains(err.Error(), "Authorization") {
		t.Errorf("密钥解析失败应在连接前报错并指出请求头名，得到 %v", err)
	}
	if _, err := Connect(context.Background(), "ext-x", cfg, nil, Options{}); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("引用密钥却没有解析器应返回 CodeInvalid，得到 %v", err)
	}
	bad := func(_ context.Context, _ string) (string, error) { return "x\r\nX-Evil: 1", nil }
	if _, err := Connect(context.Background(), "ext-x", cfg, bad, Options{}); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("解析结果含换行应返回 CodeInvalid，得到 %v", err)
	}
}

type recordingTransport struct {
	reqs []*http.Request
}

func (r *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.reqs = append(r.reqs, req)
	return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody, Request: req}, nil
}

func TestHeaderTransportOnlyTargetsConfiguredEndpoint(t *testing.T) {
	rec := &recordingTransport{}
	client, err := withHeaders(&http.Client{Transport: rec}, "https://mcp.example.com/mcp", http.Header{"Authorization": {"Bearer tok"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"https://mcp.example.com/mcp", "https://evil.example.net/steal", "http://mcp.example.com/mcp"} {
		req, _ := http.NewRequest(http.MethodPost, u, http.NoBody)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if req.Header.Get("Authorization") != "" {
			t.Errorf("RoundTripper 不得修改调用方的请求对象")
		}
	}
	if got := rec.reqs[0].Header.Get("Authorization"); got != "Bearer tok" {
		t.Errorf("发往配置端点的请求应带鉴权头，得到 %q", got)
	}
	for _, r := range rec.reqs[1:] {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("发往 %s 的请求不应带鉴权头（重定向到别的主机或降级到 http 时令牌不能跟过去）", r.URL)
		}
	}
	if same, _ := withHeaders(nil, "https://x", nil); same != nil {
		t.Error("没有请求头时应原样返回调用方的 Client")
	}
}

func TestBuildEnvUsesSharedAllowlist(t *testing.T) {
	lookup := func(k string) (string, bool) { return "v-" + k, true }
	env, err := buildEnv(context.Background(), nil, nil, lookup)
	if err != nil {
		t.Fatal(err)
	}
	joined := "\n" + strings.Join(env, "\n") + "\n"
	for _, k := range config.ChildEnvAllowlist() {
		if !strings.Contains(joined, "\n"+k+"=v-"+k+"\n") {
			t.Errorf("config.ChildEnvAllowlist 中的 %s 应透传给 MCP 子进程（含代理与证书变量）", k)
		}
	}
	if strings.Count(joined, "=") != len(config.ChildEnvAllowlist()) {
		t.Errorf("白名单之外的变量不应透传: %v", env)
	}
}

func TestHTTPConnectRefusesPublicPlaintext(t *testing.T) {
	cfg := model.MCPServerConfig{Transport: model.MCPHTTP, URL: "http://mcp.example.com/mcp"}
	if _, err := Connect(context.Background(), "ext-x", cfg, nil, Options{}); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("公网明文 http 应在连接前被拒绝，得到 %v", err)
	}
}
