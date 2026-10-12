package server_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/i18n"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/server"
	"github.com/GoGoslingCA/gogosling/internal/server/servertest"
)

func TestNewRejectsNonLoopbackListenAddr(t *testing.T) {
	env, err := servertest.NewEnv(context.Background(), t.TempDir(), servertest.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = env.Close() })
	for _, addr := range []string{"0.0.0.0:0", ":0", "192.168.1.10:46750", "example.com:80"} {
		if _, err := server.New(env.Deps, server.Options{ListenAddr: addr}); !model.IsCode(err, model.CodeInvalid) {
			t.Errorf("监听地址 %q 应被拒绝（invalid），实际 %v", addr, err)
		}
	}
	// 与组合根同一套规则（config.IsLoopbackAddr）：127.0.0.0/8 全段都是回环（LAY-07）。
	for _, addr := range []string{"127.0.0.1:0", "localhost:0", "[::1]:0", "", "127.0.0.2:0", "[0:0:0:0:0:0:0:1]:0"} {
		if _, err := server.New(env.Deps, server.Options{ListenAddr: addr}); err != nil {
			t.Errorf("监听地址 %q 应被接受: %v", addr, err)
		}
	}
	if _, err := server.New(env.Deps, server.Options{AllowedOrigins: []string{"evil"}}); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("非法的 AllowedOrigins 应被拒绝，实际 %v", err)
	}
}

func TestNewRejectsMissingDeps(t *testing.T) {
	env, err := servertest.NewEnv(context.Background(), t.TempDir(), servertest.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = env.Close() })
	deps := env.Deps
	deps.Sessions = nil
	deps.Auth = nil
	_, err = server.New(deps, server.Options{})
	if !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("缺少依赖应返回 invalid，实际 %v", err)
	}
	// Bus、Logger、Gateway、Channels 可为 nil。
	deps = env.Deps
	deps.Bus, deps.Logger, deps.Gateway, deps.Channels = nil, nil, nil, nil
	if _, err := server.New(deps, server.Options{}); err != nil {
		t.Fatalf("可选依赖为 nil 时不应失败: %v", err)
	}
}

func TestListenServeAndGracefulShutdown(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr, err := h.srv.Listen(ctx)
	if err != nil {
		t.Fatalf("Listen 失败: %v", err)
	}
	if h.srv.Addr() == nil || h.srv.Addr().String() != addr.String() {
		t.Fatalf("Addr() = %v，期望 %v", h.srv.Addr(), addr)
	}
	if _, err := h.srv.Listen(ctx); !model.IsCode(err, model.CodeConflict) {
		t.Fatalf("重复 Listen 应返回 conflict，实际 %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- h.srv.Serve(ctx) }()

	base := "http://" + addr.String()
	resp, err := http.Get(base + "/healthz")
	if err != nil {
		t.Fatalf("请求 /healthz 失败: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/healthz 状态码 = %d", resp.StatusCode)
	}

	// 打开一个事件流：关闭时它必须及时结束，不能把关闭拖到 10 秒期限。
	req, _ := http.NewRequest(http.MethodGet, base+"/api/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+h.env.Token)
	stream, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("打开事件流失败: %v", err)
	}
	defer stream.Body.Close()
	line, err := bufio.NewReader(stream.Body).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, ":") {
		t.Fatalf("事件流首行应为注释，实际 %q, %v", line, err)
	}

	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve 返回错误: %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("Serve 没有在关闭期限内返回")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("优雅关闭耗时 %v，事件流没有及时结束", d)
	}
	if err := h.srv.Serve(context.Background()); !model.IsCode(err, model.CodeConflict) {
		t.Fatalf("重复 Serve 应返回 conflict，实际 %v", err)
	}
}

func TestAuthRequiredExceptHealthz(t *testing.T) {
	h := newHarness(t)
	rec := h.do(http.MethodGet, "/healthz", nil, withoutAuth())
	mustStatus(t, rec, http.StatusOK)
	if body := decodeBody[map[string]string](t, rec); body["status"] != "ok" {
		t.Fatalf("/healthz 响应 = %v", body)
	}

	rec = h.do(http.MethodGet, "/api/v1/agents", nil, withoutAuth())
	mustError(t, rec, http.StatusUnauthorized, model.CodeUnauthorized)
	if rec.Header().Get("WWW-Authenticate") == "" {
		t.Error("401 响应应带 WWW-Authenticate")
	}
	rec = h.do(http.MethodGet, "/api/v1/agents", nil, withHeader("Authorization", "Bearer wrong-token"))
	mustError(t, rec, http.StatusUnauthorized, model.CodeUnauthorized)
	rec = h.do(http.MethodGet, "/api/v1/agents", nil, withHeader("Authorization", "Basic abc"))
	mustError(t, rec, http.StatusUnauthorized, model.CodeUnauthorized)
	// 令牌不接受查询参数。
	rec = h.do(http.MethodGet, "/api/v1/agents?access_token="+h.env.Token, nil, withoutAuth())
	mustError(t, rec, http.StatusUnauthorized, model.CodeUnauthorized)
	// 不存在的路由也先要求鉴权，不泄露路由表。
	rec = h.do(http.MethodGet, "/api/v1/nope", nil, withoutAuth())
	mustError(t, rec, http.StatusUnauthorized, model.CodeUnauthorized)
	// POST /healthz 不在豁免范围内。
	rec = h.do(http.MethodPost, "/healthz", nil, withoutAuth())
	mustError(t, rec, http.StatusUnauthorized, model.CodeUnauthorized)

	rec = h.do(http.MethodGet, "/api/v1/agents", nil)
	mustStatus(t, rec, http.StatusOK)

	if strings.Contains(h.logs.String(), h.env.Token) {
		t.Fatal("访问令牌出现在日志里")
	}
	if !strings.Contains(h.logs.String(), `"principal_id":"local"`) {
		t.Error("请求日志应记录 principal_id")
	}
}

func TestHostValidation(t *testing.T) {
	h := newHarness(t)
	for _, host := range []string{"evil.com", "evil.com:46750", "attacker.localhost:1", "0.0.0.0:46750", "[::2]:1", "localhost.:1"} {
		rec := h.do(http.MethodGet, "/api/v1/agents", nil, withHost(host))
		env := mustError(t, rec, http.StatusForbidden, model.CodeForbidden)
		if env.Error.Details["reason"] != "host_not_allowed" {
			t.Errorf("Host %q 的拒绝原因 = %v", host, env.Error.Details["reason"])
		}
	}
	// DNS 重绑定场景下 /healthz 同样受 Host 约束。
	mustError(t, h.do(http.MethodGet, "/healthz", nil, withHost("evil.com")), http.StatusForbidden, model.CodeForbidden)
	// 监听在 127.0.0.2 等地址上时客户端发来的 Host 也必须被接受（与监听地址同一套回环规则）。
	for _, host := range []string{"localhost:1234", "[::1]:9", "127.0.0.1", "LOCALHOST:80", "127.0.0.2:46750"} {
		mustStatus(t, h.do(http.MethodGet, "/api/v1/agents", nil, withHost(host)), http.StatusOK)
	}
}

func TestOriginValidationAndNoCORSHeaders(t *testing.T) {
	h := newHarness(t, func(c *harnessConfig) {
		c.srv.AllowedOrigins = []string{"http://localhost:5173"}
	})
	cases := []struct {
		origin string
		ok     bool
	}{
		{"", true},
		{"app://gogosling", true},
		{"http://localhost:5173", true},
		{"HTTP://LOCALHOST:5173", true},
		{"http://evil.com", false},
		{"null", false},
		{"http://localhost:5174", false},
	}
	for _, c := range cases {
		var mods []func(*http.Request)
		if c.origin != "" {
			mods = append(mods, withHeader("Origin", c.origin))
		}
		rec := h.do(http.MethodGet, "/api/v1/agents", nil, mods...)
		if c.ok {
			mustStatus(t, rec, http.StatusOK)
		} else {
			mustError(t, rec, http.StatusForbidden, model.CodeForbidden)
		}
		for k := range rec.Header() {
			if strings.HasPrefix(strings.ToLower(k), "access-control-") {
				t.Errorf("Origin %q 的响应带了 CORS 头 %s", c.origin, k)
			}
		}
	}
	// 预检请求同样被拒绝，不会得到任何允许头。
	rec := h.do(http.MethodOptions, "/api/v1/agents", nil, withHeader("Origin", "http://evil.com"))
	mustError(t, rec, http.StatusForbidden, model.CodeForbidden)
}

func TestSettingsHotUpdateAffectsOriginCheck(t *testing.T) {
	h := newHarness(t)
	origin := withHeader("Origin", "http://127.0.0.1:3000")
	mustError(t, h.do(http.MethodGet, "/api/v1/agents", nil, origin), http.StatusForbidden, model.CodeForbidden)

	rec := h.do(http.MethodPatch, "/api/v1/settings", map[string]any{"allowed_origins": []string{"http://127.0.0.1:3000/"}})
	mustStatus(t, rec, http.StatusOK)
	mustStatus(t, h.do(http.MethodGet, "/api/v1/agents", nil, origin), http.StatusOK)

	// 设置持久化：重新创建 Server（模拟重启）后仍然生效。
	srv2, err := server.New(h.env.Deps, server.Options{})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
	req.Host = testHost
	req.Header.Set("Authorization", "Bearer "+h.env.Token)
	req.Header.Set("Origin", "http://127.0.0.1:3000")
	rr := httptest.NewRecorder()
	srv2.Handler().ServeHTTP(rr, req)
	mustStatus(t, rr, http.StatusOK)

	mustStatus(t, h.do(http.MethodPatch, "/api/v1/settings", map[string]any{"allowed_origins": []string{}}), http.StatusOK)
	mustError(t, h.do(http.MethodGet, "/api/v1/agents", nil, origin), http.StatusForbidden, model.CodeForbidden)
}

func TestErrorEnvelopeIsLocalized(t *testing.T) {
	h := newHarness(t)
	path := "/api/v1/agents/01ARZ3NDEKTSV4RRFFQ69G5FAV"

	rec := h.do(http.MethodGet, path, nil, withHeader("Accept-Language", "en-US,en;q=0.9"))
	env := mustError(t, rec, http.StatusNotFound, model.CodeNotFound)
	if want := i18n.ErrorMessage("en", "not_found", nil); env.Error.Message != want {
		t.Errorf("英文文案 = %q，期望 %q", env.Error.Message, want)
	}
	if env.Error.Details["id"] != "01ARZ3NDEKTSV4RRFFQ69G5FAV" {
		t.Errorf("not_found 的 details 应包含 id: %v", env.Error.Details)
	}

	rec = h.do(http.MethodGet, path, nil, withHeader("Accept-Language", "zh-CN"))
	env = mustError(t, rec, http.StatusNotFound, model.CodeNotFound)
	if want := i18n.ErrorMessage("zh", "not_found", nil); env.Error.Message != want {
		t.Errorf("中文文案 = %q，期望 %q", env.Error.Message, want)
	}

	// 没有任何语言信号且未提供 Options.DefaultLocale 时取 i18n.DefaultLocale（en）。
	env = mustError(t, h.do(http.MethodGet, path, nil), http.StatusNotFound, model.CodeNotFound)
	if want := i18n.ErrorMessage(i18n.DefaultLocale, "not_found", nil); env.Error.Message != want {
		t.Errorf("缺省文案 = %q，期望 %q", env.Error.Message, want)
	}

	// 鉴权失败发生在语言中间件之前，同样本地化。
	env = mustError(t, h.do(http.MethodGet, path, nil, withoutAuth(), withHeader("Accept-Language", "en")),
		http.StatusUnauthorized, model.CodeUnauthorized)
	if want := i18n.ErrorMessage("en", "unauthorized", nil); env.Error.Message != want {
		t.Errorf("401 文案 = %q，期望 %q", env.Error.Message, want)
	}

	// C7：Accept-Language 优先于设置项（OpenAPI：message 已按 Accept-Language 本地化）；
	// 没有 Accept-Language 时才用设置项。
	mustStatus(t, h.do(http.MethodPatch, "/api/v1/settings", map[string]any{"locale": "en"}), http.StatusOK)
	env = mustError(t, h.do(http.MethodGet, path, nil, withHeader("Accept-Language", "zh")), http.StatusNotFound, model.CodeNotFound)
	if want := i18n.ErrorMessage("zh", "not_found", nil); env.Error.Message != want {
		t.Errorf("Accept-Language: zh 应优先于设置项 locale=en，文案 = %q，期望 %q", env.Error.Message, want)
	}
	env = mustError(t, h.do(http.MethodGet, path, nil, withHeader("Accept-Language", "fr")), http.StatusNotFound, model.CodeNotFound)
	if want := i18n.ErrorMessage("fr", "not_found", nil); env.Error.Message != want {
		t.Errorf("Accept-Language: fr 应使用法文，文案 = %q，期望 %q", env.Error.Message, want)
	}
}

func TestErrorLocaleFallsBackToOptionsDefaultLocale(t *testing.T) {
	h := newHarness(t, func(c *harnessConfig) { c.srv.DefaultLocale = "zh-CN" })
	path := "/api/v1/agents/01ARZ3NDEKTSV4RRFFQ69G5FAV"
	env := mustError(t, h.do(http.MethodGet, path, nil), http.StatusNotFound, model.CodeNotFound)
	if want := i18n.ErrorMessage("zh", "not_found", nil); env.Error.Message != want {
		t.Errorf("没有 Accept-Language 与设置项时应取 Options.DefaultLocale=zh，文案 = %q，期望 %q", env.Error.Message, want)
	}
	// 设置项优先于 Options.DefaultLocale；清除设置项后回到 Options.DefaultLocale。
	mustStatus(t, h.do(http.MethodPatch, "/api/v1/settings", map[string]any{"locale": "en"}), http.StatusOK)
	env = mustError(t, h.do(http.MethodGet, path, nil), http.StatusNotFound, model.CodeNotFound)
	if want := i18n.ErrorMessage("en", "not_found", nil); env.Error.Message != want {
		t.Errorf("设置项 locale=en 应优先于 Options.DefaultLocale，文案 = %q，期望 %q", env.Error.Message, want)
	}
	mustStatus(t, h.do(http.MethodPatch, "/api/v1/settings", map[string]any{"locale": ""}), http.StatusOK)
	env = mustError(t, h.do(http.MethodGet, path, nil), http.StatusNotFound, model.CodeNotFound)
	if want := i18n.ErrorMessage("zh", "not_found", nil); env.Error.Message != want {
		t.Errorf("清除设置项后应回到 Options.DefaultLocale=zh，文案 = %q，期望 %q", env.Error.Message, want)
	}
}

func TestJSONResponsesHaveSecurityHeaders(t *testing.T) {
	h := newHarness(t)
	for _, rec := range []*httptest.ResponseRecorder{
		h.do(http.MethodGet, "/api/v1/agents", nil),
		h.do(http.MethodGet, "/api/v1/agents/missing", nil),
	} {
		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("缺少 nosniff 头，实际 %q", got)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("Content-Type = %q", ct)
		}
	}
}

func TestRequestBodyValidation(t *testing.T) {
	h := newHarness(t, func(c *harnessConfig) { c.srv.MaxBodyBytes = 64 })

	env := mustError(t, h.do(http.MethodPost, "/api/v1/agents", `{"name":`), http.StatusBadRequest, model.CodeInvalid)
	if env.Error.Details["reason"] != "malformed_json" {
		t.Errorf("语法错误的 reason = %v", env.Error.Details["reason"])
	}
	env = mustError(t, h.do(http.MethodPost, "/api/v1/agents", `{"name": 3}`), http.StatusBadRequest, model.CodeInvalid)
	if env.Error.Details["reason"] != "wrong_type" || env.Error.Details["field"] != "name" {
		t.Errorf("类型错误的 details = %v", env.Error.Details)
	}
	env = mustError(t, h.do(http.MethodPost, "/api/v1/agents", `{} {}`), http.StatusBadRequest, model.CodeInvalid)
	if env.Error.Details["reason"] != "trailing_data" {
		t.Errorf("尾随数据的 reason = %v", env.Error.Details["reason"])
	}
	env = mustError(t, h.do(http.MethodPost, "/api/v1/agents", ""), http.StatusBadRequest, model.CodeInvalid)
	if env.Error.Details["reason"] != "empty_body" {
		t.Errorf("空请求体的 reason = %v", env.Error.Details["reason"])
	}
	big := `{"name":"` + strings.Repeat("x", 200) + `"}`
	env = mustError(t, h.do(http.MethodPost, "/api/v1/agents", big), http.StatusBadRequest, model.CodeInvalid)
	if env.Error.Details["reason"] != "body_too_large" {
		t.Errorf("超限的 reason = %v", env.Error.Details["reason"])
	}
	env = mustError(t, h.do(http.MethodPost, "/api/v1/agents", `{}`, withHeader("Content-Type", "text/plain")),
		http.StatusBadRequest, model.CodeInvalid)
	if env.Error.Details["reason"] != "content_type" {
		t.Errorf("非 JSON Content-Type 的 reason = %v", env.Error.Details["reason"])
	}
}

func TestUnknownRouteAndMethod(t *testing.T) {
	h := newHarness(t)
	mustError(t, h.do(http.MethodGet, "/api/v1/nope", nil), http.StatusNotFound, model.CodeNotFound)
	rec := h.do(http.MethodPut, "/api/v1/agents", map[string]any{})
	mustError(t, rec, http.StatusMethodNotAllowed, model.CodeInvalid)
	if allow := rec.Header().Get("Allow"); !strings.Contains(allow, "GET") || !strings.Contains(allow, "POST") {
		t.Errorf("Allow = %q，期望包含 GET 与 POST", allow)
	}
}

func TestPanicRecoveryHidesDetails(t *testing.T) {
	h := newHarness(t)
	panicky := server.ChainForTest(h.srv, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("secret internal state")
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/boom", nil)
	req.Host = testHost
	req.Header.Set("Authorization", "Bearer "+h.env.Token)
	req.Header.Set("Accept-Language", "en")
	rec := httptest.NewRecorder()
	panicky.ServeHTTP(rec, req)
	env := mustError(t, rec, http.StatusInternalServerError, model.CodeInternal)
	if env.Error.Details != nil || strings.Contains(rec.Body.String(), "secret internal state") {
		t.Fatalf("internal 错误不应外泄细节: %s", rec.Body.String())
	}
	if env.Error.Message != i18n.ErrorMessage("en", "internal", nil) {
		t.Errorf("internal 文案 = %q", env.Error.Message)
	}
	if !strings.Contains(h.logs.String(), "secret internal state") {
		t.Error("panic 细节应记录在日志里")
	}
}

func TestVersionReportsWorkerStatus(t *testing.T) {
	h := newHarness(t, func(c *harnessConfig) { c.srv.StartedAt = "2026-10-10T00:00:00Z" })
	rec := h.do(http.MethodGet, "/api/v1/version", nil)
	mustStatus(t, rec, http.StatusOK)
	var v struct {
		Version   string `json:"version"`
		Commit    string `json:"commit"`
		StartedAt string `json:"started_at"`
		Worker    struct {
			Status string `json:"status"`
		} `json:"worker"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Version == "" || v.Commit == "" || v.StartedAt != "2026-10-10T00:00:00Z" {
		t.Errorf("版本信息不完整: %+v", v)
	}
	if v.Worker.Status != "unavailable" {
		t.Errorf("未启动的工作进程状态应为 unavailable，实际 %q", v.Worker.Status)
	}
}
