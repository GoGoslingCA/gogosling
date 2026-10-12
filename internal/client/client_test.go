package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/identity"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

func TestNewTrimsEndpoint(t *testing.T) {
	c := New(" http://127.0.0.1:46750/ ", "tok")
	if c.Endpoint() != "http://127.0.0.1:46750" {
		t.Fatalf("Endpoint() = %q", c.Endpoint())
	}
	c = New("http://x", "t", WithReconnectBackoff(time.Second, time.Minute))
	if c.backoffMax != DefaultBackoffMax || c.backoffMin != time.Second {
		t.Fatalf("退避上限应被限制在 5 秒: min=%v max=%v", c.backoffMin, c.backoffMax)
	}
}

func TestRequestHeadersAndErrorEnvelope(t *testing.T) {
	var mu sync.Mutex
	var got http.Header
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got, gotBody = r.Header.Clone(), string(b)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"code":"conflict","message":"The request conflicts.","details":{"run_id":"R1"}}}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "secret-token", WithLocale("en"), WithUserAgent("ua/1"))
	_, err := c.UpdateAgent(context.Background(), "A 1", AgentPatch{Name: Ptr("新名字")})
	var me *model.Error
	if !errors.As(err, &me) {
		t.Fatalf("错误应为 *model.Error，实际 %T %v", err, err)
	}
	if me.Code != model.CodeConflict || me.Message != "The request conflicts." || me.Details["run_id"] != "R1" {
		t.Fatalf("信封解析结果 = %+v", me)
	}
	mu.Lock()
	defer mu.Unlock()
	if got.Get("Authorization") != "Bearer secret-token" || got.Get("Accept-Language") != "en" ||
		got.Get("User-Agent") != "ua/1" || got.Get("Content-Type") != "application/json" {
		t.Fatalf("请求头 = %v", got)
	}
	if gotBody != `{"name":"新名字"}` {
		t.Fatalf("PATCH 请求体应只包含给出的字段，实际 %s", gotBody)
	}
}

func TestNonEnvelopeErrorsMapByStatus(t *testing.T) {
	status := http.StatusBadGateway
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte("<html>proxy error</html>"))
	}))
	defer srv.Close()
	c := New(srv.URL, "t")
	cases := map[int]model.Code{
		http.StatusBadGateway:          model.CodeUnavailable,
		http.StatusNotFound:            model.CodeNotFound,
		http.StatusUnauthorized:        model.CodeUnauthorized,
		http.StatusInternalServerError: model.CodeInternal,
		http.StatusGatewayTimeout:      model.CodeTimeout,
	}
	for st, want := range cases {
		status = st
		if _, err := c.GetAgent(context.Background(), "x"); model.CodeOf(err) != want {
			t.Errorf("HTTP %d 应映射为 %s，实际 %v", st, want, err)
		}
	}
}

func TestPatchEncodingDistinguishesAbsentNullAndEmpty(t *testing.T) {
	enc := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	var reset []string
	if got := enc(AgentPatch{}); got != "{}" {
		t.Errorf("空 Patch 应编码为 {}，实际 %s", got)
	}
	if got := enc(AgentPatch{Tools: &reset}); got != `{"tools":null}` {
		t.Errorf("指向 nil 切片应编码为 null，实际 %s", got)
	}
	if got := enc(AgentPatch{Tools: &[]string{}}); got != `{"tools":[]}` {
		t.Errorf("指向空切片应编码为 []，实际 %s", got)
	}
	if got := enc(ProviderPatch{APIKey: Ptr("")}); got != `{"api_key":""}` {
		t.Errorf("清除密钥应编码为空串，实际 %s", got)
	}
	if got := enc(AgentCreate{Name: "a"}); !strings.Contains(got, `"tools":null`) {
		t.Errorf("创建时 Tools 为 nil 应发送 null（使用默认工具集），实际 %s", got)
	}
}

func TestNetworkErrorIsUnavailable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	c := New("http://"+addr, "t")
	err = c.Health(context.Background())
	if !model.IsCode(err, model.CodeUnavailable) {
		t.Fatalf("连接失败应返回 unavailable，实际 %v", err)
	}
}

func TestCancelAndTimeoutAreDistinguished(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	err := New(srv.URL, "t").Health(ctx)
	if !model.IsCode(err, model.CodeCancelled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("调用方取消应返回 cancelled 且可用 errors.Is 判断，实际 %v", err)
	}
	err = New(srv.URL, "t", WithTimeout(50*time.Millisecond)).Health(context.Background())
	if !model.IsCode(err, model.CodeTimeout) {
		t.Fatalf("超时应返回 timeout，实际 %v", err)
	}
}

func TestSendMessageReturnsRunTogetherWithError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"code":"unavailable","message":"busy"},"run":{"id":"R9","status":"failed","error":"x"}}`))
	}))
	defer srv.Close()
	run, err := New(srv.URL, "t").SendMessage(context.Background(), "S", "hi", nil)
	if !model.IsCode(err, model.CodeUnavailable) {
		t.Fatalf("错误码 = %v", err)
	}
	if run == nil || run.ID != "R9" || run.Status != model.RunFailed {
		t.Fatalf("应同时返回落盘的运行，实际 %+v", run)
	}
}

func TestDiscover(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if _, err := Discover(ctx, dir); !model.IsCode(err, model.CodeUnavailable) {
		t.Fatalf("没有 daemon.json 时应返回 unavailable，实际 %v", err)
	}

	var healthAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		healthAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()
	hf := identity.NewHandleFile(dir)
	if err := hf.Write(ctx, identity.Handle{PID: 1, Endpoint: srv.URL, Token: "tok", Version: "v", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	c, err := Discover(ctx, dir)
	if err != nil {
		t.Fatalf("Discover 失败: %v", err)
	}
	if c.Endpoint() != srv.URL || c.token != "tok" {
		t.Fatalf("Discover 得到的端点/令牌不正确: %q", c.Endpoint())
	}
	if healthAuth != "" {
		t.Error("/healthz 是免鉴权接口，不应携带令牌")
	}

	srv.Close()
	if _, err := Discover(ctx, dir); !model.IsCode(err, model.CodeUnavailable) {
		t.Fatalf("守护进程无响应时应返回 unavailable，实际 %v", err)
	}
}

func TestShutdownCreateSessionWithAndSettings(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path+" "+string(b)+" "+r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/system/shutdown":
			w.WriteHeader(http.StatusAccepted)
		case "/api/v1/sessions":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"S1","agent_id":"A","origin":"cli","key":"A:cli:local","status":"active","active_run_id":"R9"}`))
		case "/api/v1/settings":
			_, _ = w.Write([]byte(`{"effective_locale":"zh","effective_timezone":"Asia/Shanghai","allowed_origins":[]}`))
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")
	ctx := context.Background()

	if err := c.Shutdown(ctx); err != nil {
		t.Fatalf("202 应视为成功: %v", err)
	}
	s, err := c.CreateSessionWith(ctx, SessionCreate{AgentID: "A", Origin: model.OriginCLI})
	if err != nil || s.Origin != model.OriginCLI || s.ActiveRunID != "R9" {
		t.Fatalf("CreateSessionWith = %+v, %v", s, err)
	}
	if _, err := c.CreateSession(ctx, "A", "t"); err != nil {
		t.Fatal(err)
	}
	st, err := c.GetSettings(ctx)
	if err != nil || st.Locale != "" || st.EffectiveLocale != "zh" || st.EffectiveTimezone != "Asia/Shanghai" {
		t.Fatalf("GetSettings = %+v, %v", st, err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{
		"POST /api/v1/system/shutdown  Bearer tok",
		`POST /api/v1/sessions {"agent_id":"A","origin":"cli"} Bearer tok`,
		`POST /api/v1/sessions {"agent_id":"A","title":"t"} Bearer tok`,
		"GET /api/v1/settings  Bearer tok",
	}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("请求序列 =\n%s\n期望\n%s", strings.Join(calls, "\n"), strings.Join(want, "\n"))
	}
	// SettingsPatch 不会把未设置的语言写成空串。
	if b, _ := json.Marshal(Settings{}); strings.Contains(string(b), `"locale"`) || strings.Contains(string(b), `"timezone"`) {
		t.Fatalf("未设置的 locale / timezone 应省略: %s", b)
	}
}
