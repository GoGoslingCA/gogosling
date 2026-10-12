package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/event"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

// fakeServer 记录收到的请求，按 handler 响应。
type fakeServer struct {
	*httptest.Server
	path    string
	headers http.Header
}

func newFakeServer(t *testing.T, status int, body string) *fakeServer {
	t.Helper()
	fs := &fakeServer{}
	fs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fs.path = r.URL.Path
		fs.headers = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(fs.Close)
	return fs
}

func TestTestOpenAICompatibleRefreshesModels(t *testing.T) {
	srv := newFakeServer(t, 200, `{"object":"list","data":[{"id":"gpt-b"},{"id":"gpt-a"},{"id":"gpt-a"},{"id":""}]}`)
	f := newFixture(t)
	p := mustCreate(t, f, Input{Name: "x", Kind: model.ProviderOpenAICompatible, BaseURL: srv.URL + "/v1", APIKey: "sk-1"})
	sub := f.bus.Subscribe(ctxBg(), 0, 8, func(e event.Event) bool { return e.Type == EventUpdated })
	defer sub.Cancel()

	res, err := f.svc.Test(ctxBg(), p.ID)
	if err != nil || !res.OK {
		t.Fatalf("Test = %+v, %v", res, err)
	}
	if strings.Join(res.Models, ",") != "gpt-a,gpt-b" {
		t.Errorf("模型应去重排序: %v", res.Models)
	}
	if srv.path != "/v1/models" {
		t.Errorf("请求路径 = %s，期望 /v1/models", srv.path)
	}
	if got := srv.headers.Get("Authorization"); got != "Bearer sk-1" {
		t.Errorf("Authorization = %q", got)
	}
	stored, _ := f.svc.Get(ctxBg(), p.ID)
	if strings.Join(stored.Models, ",") != "gpt-a,gpt-b" {
		t.Errorf("模型列表应写回记录: %v", stored.Models)
	}
	ev := <-sub.C
	if v, ok := ev.Payload.(View); !ok || len(v.Models) != 2 {
		t.Errorf("应发布 provider.updated: %#v", ev.Payload)
	}
}

func TestTestOllamaAndAnthropicEndpoints(t *testing.T) {
	ollama := newFakeServer(t, 200, `{"models":[{"name":"qwen3:8b","model":"qwen3:8b"},{"name":"llama3"}]}`)
	f := newFixture(t)
	p := mustCreate(t, f, Input{Name: "o", Kind: model.ProviderOllama, BaseURL: ollama.URL})
	res, err := f.svc.Test(ctxBg(), p.ID)
	if err != nil || !res.OK || strings.Join(res.Models, ",") != "llama3,qwen3:8b" {
		t.Fatalf("Ollama Test = %+v, %v", res, err)
	}
	if ollama.path != "/api/tags" {
		t.Errorf("Ollama 路径 = %s", ollama.path)
	}
	if ollama.headers.Get("Authorization") != "" {
		t.Error("无密钥时不应发送 Authorization")
	}

	anth := newFakeServer(t, 200, `{"data":[{"id":"claude-x","type":"model"}],"has_more":false}`)
	a := mustCreate(t, f, Input{Name: "a", Kind: model.ProviderAnthropic, BaseURL: anth.URL, APIKey: "ak"})
	res, err = f.svc.Test(ctxBg(), a.ID)
	if err != nil || !res.OK || len(res.Models) != 1 || res.Models[0] != "claude-x" {
		t.Fatalf("Anthropic Test = %+v, %v", res, err)
	}
	if anth.path != "/v1/models" {
		t.Errorf("Anthropic 路径 = %s", anth.path)
	}
	if anth.headers.Get("x-api-key") != "ak" || anth.headers.Get("anthropic-version") == "" {
		t.Errorf("Anthropic 头不符: %v", anth.headers)
	}
	if anth.headers.Get("Authorization") != "" {
		t.Error("Anthropic 不应使用 Bearer 头")
	}
	// base_url 已带 /v1 时不重复。
	if got := modelsEndpoint(model.ProviderAnthropic, "https://h/v1"); got != "https://h/v1/models" {
		t.Errorf("modelsEndpoint = %s", got)
	}
}

func TestTestFailuresAreReportedNotReturned(t *testing.T) {
	f := newFixture(t)
	bad := newFakeServer(t, 401, `{"error":{"message":"invalid key sk-leak"}}`)
	p := mustCreate(t, f, Input{Name: "x", Kind: model.ProviderOpenAICompatible, BaseURL: bad.URL, APIKey: "sk-leak", DefaultModel: "m"})
	res, err := f.svc.Test(ctxBg(), p.ID)
	if err != nil || res.OK {
		t.Fatalf("HTTP 401 应为 OK=false 且无 Go 错误: %+v, %v", res, err)
	}
	if !strings.Contains(res.Error, "HTTP 401") || !strings.Contains(res.Error, p.Name+"：") {
		t.Errorf("Error = %q，应包含供应商名称和 HTTP 401", res.Error)
	}
	if strings.Contains(res.Error, "sk-leak") {
		t.Errorf("错误信息泄露了密钥: %q", res.Error)
	}
	stored, _ := f.svc.Get(ctxBg(), p.ID)
	if len(stored.Models) != 0 {
		t.Error("失败时不应修改模型列表")
	}

	notJSON := newFakeServer(t, 200, `<html>`)
	q := mustCreate(t, f, Input{Name: "y", Kind: model.ProviderOpenAICompatible, BaseURL: notJSON.URL})
	res, _ = f.svc.Test(ctxBg(), q.ID)
	if res.OK || res.Error == "" {
		t.Errorf("非 JSON 响应应失败: %+v", res)
	}

	wrongShape := newFakeServer(t, 200, `{"items":[]}`)
	r := mustCreate(t, f, Input{Name: "z", Kind: model.ProviderOpenAICompatible, BaseURL: wrongShape.URL})
	res, _ = f.svc.Test(ctxBg(), r.ID)
	if res.OK {
		t.Errorf("缺少 data 字段应失败: %+v", res)
	}

	// 连接失败（端口已关闭）。
	closed := httptest.NewServer(http.NotFoundHandler())
	url := closed.URL
	closed.Close()
	s := mustCreate(t, f, Input{Name: "w", Kind: model.ProviderOpenAICompatible, BaseURL: url})
	res, err = f.svc.Test(ctxBg(), s.ID)
	if err != nil || res.OK || !strings.Contains(res.Error, "w：") || !strings.Contains(res.Error, "请求失败") {
		t.Errorf("连接失败应带供应商名称并报告请求失败: %+v, %v", res, err)
	}

	_, err = f.svc.Test(ctxBg(), "missing")
	if model.CodeOf(err) != model.CodeNotFound {
		t.Errorf("不存在的供应商应返回 not_found: %v", err)
	}
}

func TestProbeHonoursContextTimeout(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
		w.WriteHeader(200)
	}))
	defer slow.Close()
	f := newFixture(t)
	ctx, cancel := context.WithTimeout(ctxBg(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	res := f.svc.Probe(ctx, model.ProviderOpenAICompatible, slow.URL, "")
	if res.OK || res.Error != "连接超时" {
		t.Errorf("超时应报告连接超时: %+v", res)
	}
	if time.Since(start) > time.Second {
		t.Error("探测未在上下文超时后尽快返回")
	}
}

func TestProbeRejectsUnknownKind(t *testing.T) {
	f := newFixture(t)
	res := f.svc.Probe(ctxBg(), "nope", "http://127.0.0.1:1", "")
	if res.OK || res.Error == "" {
		t.Errorf("未知类型应失败: %+v", res)
	}
}

func TestDiscoverLocal(t *testing.T) {
	srv := newFakeServer(t, 200, `{"models":[{"name":"llama3:latest"}]}`)
	f := newFixture(t, WithLocalOllamaURL(srv.URL))
	d, err := f.svc.DiscoverLocal(ctxBg())
	if err != nil || !d.Found || d.Kind != model.ProviderOllama || d.BaseURL != srv.URL || len(d.Models) != 1 {
		t.Fatalf("DiscoverLocal = %+v, %v", d, err)
	}

	closed := httptest.NewServer(http.NotFoundHandler())
	url := closed.URL
	closed.Close()
	g := newFixture(t, WithLocalOllamaURL(url))
	d, err = g.svc.DiscoverLocal(ctxBg())
	if err != nil || d.Found {
		t.Fatalf("未运行时应 Found=false 且无错误: %+v, %v", d, err)
	}

	ctx, cancel := context.WithCancel(ctxBg())
	cancel()
	if _, err := g.svc.DiscoverLocal(ctx); model.CodeOf(err) != model.CodeCancelled {
		t.Errorf("已取消的 ctx 应返回 cancelled: %v", err)
	}
}

func TestExcerptAndRedact(t *testing.T) {
	long := strings.Repeat("字", 300)
	if got := excerptBody("  a \n b  "); got != "a b" {
		t.Errorf("excerptBody 应压缩空白: %q", got)
	}
	if got := excerptBody(long); len([]rune(got)) != maxErrorExcerpt+1 || !strings.HasSuffix(got, "…") {
		t.Errorf("excerptBody 应按字符截断: %d", len([]rune(got)))
	}
	if got := redact("key=abc; abc again", "abc"); got != "key=***; *** again" {
		t.Errorf("redact = %q", got)
	}
	if got := redact("x", ""); got != "x" {
		t.Errorf("空密钥不应改动文本: %q", got)
	}
}
