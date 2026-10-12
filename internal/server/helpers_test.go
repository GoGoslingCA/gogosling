package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/identity"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/permission"
	"github.com/GoGoslingCA/gogosling/internal/server"
	"github.com/GoGoslingCA/gogosling/internal/server/servertest"
)

// testHost 是测试请求使用的 Host 头（回环地址，满足 Host 校验）。
const testHost = "127.0.0.1:46750"

// syncBuffer 是可并发写入的日志缓冲，用于断言日志里不出现令牌与密钥。
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type harnessConfig struct {
	env servertest.Options
	srv server.Options
}

type harness struct {
	t    *testing.T
	env  *servertest.Env
	srv  *server.Server
	h    http.Handler
	logs *syncBuffer
}

// newHarness 装配真实依赖（SQLite、文件凭据、假运行时）并创建 Server。
func newHarness(t *testing.T, mods ...func(*harnessConfig)) *harness {
	t.Helper()
	logs := &syncBuffer{}
	logger := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg := harnessConfig{
		env: servertest.Options{Logger: logger},
		srv: server.Options{ListenAddr: "127.0.0.1:0"},
	}
	for _, m := range mods {
		m(&cfg)
	}
	ctx := context.Background()
	env, err := servertest.NewEnv(ctx, t.TempDir(), cfg.env)
	if err != nil {
		t.Fatalf("装配测试依赖失败: %v", err)
	}
	t.Cleanup(func() { _ = env.Close() })
	srv, err := server.New(env.Deps, cfg.srv)
	if err != nil {
		t.Fatalf("创建 Server 失败: %v", err)
	}
	return &harness{t: t, env: env, srv: srv, h: srv.Handler(), logs: logs}
}

// withAuth 等请求修饰器。
func withHeader(k, v string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set(k, v) }
}

func withoutAuth() func(*http.Request) {
	return func(r *http.Request) { r.Header.Del("Authorization") }
}

func withHost(host string) func(*http.Request) {
	return func(r *http.Request) { r.Host = host }
}

// do 直接驱动 Handler；body 为 string / []byte 时原样发送，其他值编码为 JSON。
func (h *harness) do(method, path string, body any, mods ...func(*http.Request)) *httptest.ResponseRecorder {
	h.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	case []byte:
		rd = bytes.NewReader(b)
	default:
		data, err := json.Marshal(b)
		if err != nil {
			h.t.Fatalf("编码请求体失败: %v", err)
		}
		rd = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Host = testHost
	req.Header.Set("Authorization", "Bearer "+h.env.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, m := range mods {
		m(req)
	}
	rec := httptest.NewRecorder()
	h.h.ServeHTTP(rec, req)
	return rec
}

// mustStatus 断言状态码，失败时打印响应体。
func mustStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("状态码 = %d，期望 %d；响应体: %s", rec.Code, want, rec.Body.String())
	}
}

func decodeBody[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("响应体不是合法 JSON: %v；响应体: %s", err, rec.Body.String())
	}
	return v
}

// errorEnvelope 是错误信封的测试侧镜像。
type errorEnvelope struct {
	Error struct {
		Code    model.Code     `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details"`
	} `json:"error"`
	Run *model.Run `json:"run"`
}

// mustError 断言状态码与错误码，并返回信封。
func mustError(t *testing.T, rec *httptest.ResponseRecorder, status int, code model.Code) errorEnvelope {
	t.Helper()
	mustStatus(t, rec, status)
	env := decodeBody[errorEnvelope](t, rec)
	if env.Error.Code != code {
		t.Fatalf("错误码 = %q，期望 %q；响应体: %s", env.Error.Code, code, rec.Body.String())
	}
	if env.Error.Message == "" {
		t.Fatalf("错误信封缺少 message: %s", rec.Body.String())
	}
	return env
}

type items[T any] struct {
	Items []T `json:"items"`
}

// sessionView 是会话响应的测试侧镜像。
type sessionView struct {
	model.Session
	ActiveRunID string `json:"active_run_id"`
}

// seedAgent 经领域服务创建供应商与智能体。
func (h *harness) seedAgent(name string) *model.Agent {
	h.t.Helper()
	a, err := h.env.SeedAgent(context.Background(), name)
	if err != nil {
		h.t.Fatalf("创建智能体失败: %v", err)
	}
	return a
}

// seedSession 经 HTTP 创建会话。
func (h *harness) seedSession(agentID string) sessionView {
	h.t.Helper()
	rec := h.do(http.MethodPost, "/api/v1/sessions", map[string]any{"agent_id": agentID, "title": "测试会话"})
	mustStatus(h.t, rec, http.StatusCreated)
	return decodeBody[sessionView](h.t, rec)
}

// send 经 HTTP 发送消息并返回 202 的运行。
func (h *harness) send(sessionID, text string) model.Run {
	h.t.Helper()
	rec := h.do(http.MethodPost, "/api/v1/sessions/"+sessionID+"/messages", map[string]any{"text": text})
	mustStatus(h.t, rec, http.StatusAccepted)
	return decodeBody[model.Run](h.t, rec)
}

// waitRun 轮询运行直到满足条件（默认 5 秒超时）。
func (h *harness) waitRun(runID string, cond func(model.Run) bool, mods ...func(*http.Request)) model.Run {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		rec := h.do(http.MethodGet, "/api/v1/runs/"+runID, nil, mods...)
		mustStatus(h.t, rec, http.StatusOK)
		run := decodeBody[model.Run](h.t, rec)
		if cond(run) {
			return run
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("等待运行 %s 超时，最后状态 %s", runID, run.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func terminal(r model.Run) bool { return r.Status.Terminal() }

// eventually 轮询条件直到成立（默认 5 秒超时）。
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("等待超时: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// approvalCheckRequest 构造一次在 ask_all 档位下必然需要审批的权限判定请求。
func approvalCheckRequest(agentID, sessionID, runID string) permission.Request {
	return permission.Request{
		Principal: identity.LocalPrincipal(),
		AgentID:   agentID,
		Autonomy:  model.AutonomyAskAll,
		SessionID: sessionID,
		RunID:     runID,
		Tool: model.ToolSpec{
			Name: "write_file", Description: "write", Source: "builtin", Risk: model.RiskWrite,
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		Call:    model.ToolCall{ID: "call-1", Name: "write_file", Arguments: json.RawMessage(`{"path":"a.txt"}`)},
		Details: json.RawMessage(`{"path":"a.txt"}`),
	}
}
