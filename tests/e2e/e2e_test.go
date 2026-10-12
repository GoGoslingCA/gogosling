//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/app"
	"github.com/GoGoslingCA/gogosling/internal/client"
	"github.com/GoGoslingCA/gogosling/internal/config"
	"github.com/GoGoslingCA/gogosling/internal/engine"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/permission"
	"github.com/GoGoslingCA/gogosling/internal/secret"
)

const (
	fakeAPIKey  = "sk-e2e-not-a-real-key"
	fakeModel   = "fake-model"
	toolCommand = "echo hi"
)

// fakeOpenAI 是 OpenAI 兼容的 /chat/completions 假服务：第一次请求流式返回一个 run_command 工具调用，
// 之后（请求里已有 tool 结果）流式返回最终文本。两次都在最后一个 chunk 里带 usage。
type fakeOpenAI struct {
	mu       sync.Mutex
	requests []map[string]any
	auth     []string
}

func (f *fakeOpenAI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/chat/completions") {
		http.NotFound(w, r)
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.requests = append(f.requests, body)
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.mu.Unlock()

	toolResult := ""
	if msgs, ok := body["messages"].([]any); ok {
		for _, m := range msgs {
			if mm, ok := m.(map[string]any); ok && mm["role"] == "tool" {
				toolResult, _ = mm["content"].(string)
			}
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	fl := w.(http.Flusher)
	send := func(v any) {
		raw, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", raw)
		fl.Flush()
	}
	chunk := func(delta map[string]any, finish any) map[string]any {
		return map[string]any{
			"id": "chatcmpl-e2e", "object": "chat.completion.chunk", "model": fakeModel,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
		}
	}
	usage := map[string]any{"id": "chatcmpl-e2e", "object": "chat.completion.chunk", "model": fakeModel, "choices": []any{},
		"usage": map[string]any{"prompt_tokens": 21, "completion_tokens": 7, "total_tokens": 28}}

	if toolResult == "" {
		args, _ := json.Marshal(map[string]string{"command": toolCommand})
		send(chunk(map[string]any{"role": "assistant", "content": "我来运行命令。"}, nil))
		send(chunk(map[string]any{"tool_calls": []any{map[string]any{
			"index": 0, "id": "call_e2e_1", "type": "function",
			"function": map[string]any{"name": "run_command", "arguments": ""},
		}}}, nil))
		// 参数分两段到达，覆盖工作进程的增量拼接。
		half := len(args) / 2
		for _, part := range []string{string(args[:half]), string(args[half:])} {
			send(chunk(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{"arguments": part}}}}, nil))
		}
		send(chunk(map[string]any{}, "tool_calls"))
		send(usage)
	} else {
		send(chunk(map[string]any{"role": "assistant", "content": "命令输出是："}, nil))
		send(chunk(map[string]any{"content": strings.TrimSpace(toolResult)}, nil))
		send(chunk(map[string]any{}, "stop"))
		send(usage)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	fl.Flush()
}

// repoRoot 从当前目录向上找到本模块的 go.mod。
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if raw, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil && strings.Contains(string(raw), "module github.com/GoGoslingCA/gogosling") {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("找不到仓库根目录")
		}
		dir = parent
	}
}

// testWriter 把守护进程日志写进测试输出（只在失败或 -v 时显示）。
type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

func TestRealWorkerToolCallWithApproval(t *testing.T) {
	uv, err := exec.LookPath("uv")
	if err != nil {
		t.Skip("本机没有 uv，跳过真实工作进程的端到端测试")
	}
	root := repoRoot(t)
	t.Setenv(engine.EnvWorkerCmd, strconv.Quote(uv)+" run --project "+strconv.Quote(filepath.Join(root, "worker"))+" gogosling-worker")

	llm := &fakeOpenAI{}
	llmSrv := httptest.NewServer(llm)
	defer llmSrv.Close()

	home := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	secrets, err := secret.OpenWith(ctx, home, nil, secret.Options{DisableKeyring: true})
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelInfo}))
	a, err := app.New(config.Config{DataDir: home, ListenAddr: "127.0.0.1:0"}, logger,
		app.WithSecretStore(secrets), app.WithWorkerStatusInterval(200*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	runDone := make(chan error, 1)
	go func() { runDone <- a.Run(ctx) }()
	select {
	case <-a.Ready():
	case err := <-runDone:
		t.Fatalf("守护进程启动失败: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("守护进程就绪超时")
	}
	defer func() {
		cancel()
		select {
		case err := <-runDone:
			if err != nil {
				t.Errorf("守护进程退出返回错误: %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Error("守护进程退出超时")
		}
		if _, err := os.Stat(filepath.Join(home, "daemon.json")); !os.IsNotExist(err) {
			t.Errorf("退出后 daemon.json 应被删除: %v", err)
		}
	}()

	cctx, ccancel := context.WithTimeout(ctx, 3*time.Minute)
	defer ccancel()
	cl, err := client.Discover(cctx, home, client.WithLocale("zh"))
	if err != nil {
		t.Fatalf("Discover 失败: %v", err)
	}
	// 首次运行时 uv 可能需要同步依赖，给足时间。
	deadline := time.Now().Add(2 * time.Minute)
	for {
		v, err := cl.Version(cctx)
		if err == nil && v.Worker.Status == "ready" {
			t.Logf("工作进程就绪：version=%s python=%s", v.Worker.Version, v.Worker.Python)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("真实工作进程没有就绪：%+v / %v", v, err)
		}
		time.Sleep(200 * time.Millisecond)
	}

	prov, err := cl.CreateProvider(cctx, client.ProviderCreate{
		Name: "假 OpenAI", Kind: model.ProviderOpenAICompatible, BaseURL: llmSrv.URL + "/v1", APIKey: fakeAPIKey, DefaultModel: fakeModel,
	})
	if err != nil {
		t.Fatalf("创建供应商失败: %v", err)
	}
	agent, err := cl.CreateAgent(cctx, client.AgentCreate{Name: "端到端助理", Model: model.ModelRef{ProviderID: prov.ID, Model: fakeModel}})
	if err != nil {
		t.Fatalf("创建智能体失败: %v", err)
	}
	if agent.Autonomy != model.AutonomyAskRisky {
		t.Fatalf("默认档位应为 ask_risky，得到 %s", agent.Autonomy)
	}
	sess, err := cl.CreateSession(cctx, agent.ID, "")
	if err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}
	evs, _ := cl.Events(cctx, 0, sess.ID)

	run, err := cl.SendMessage(cctx, sess.ID, "请运行 echo hi 并告诉我输出", nil)
	if err != nil {
		t.Fatalf("发送失败: %v", err)
	}

	var types []string
	approved := false
	timeout := time.After(2 * time.Minute)
loop:
	for {
		select {
		case ev, ok := <-evs:
			if !ok {
				t.Fatalf("事件流意外关闭；已收到 %v", types)
			}
			if ev.RunID != run.ID {
				continue
			}
			types = append(types, ev.Type)
			var re model.RunEvent
			_ = client.DecodePayload(ev, &re)
			switch model.RunEventType(ev.Type) {
			case model.EventApprovalRequested:
				if re.Approval == nil || re.Approval.ToolCall.Name != "run_command" {
					t.Fatalf("应请求 run_command 的审批，得到 %+v", re.Approval)
				}
				pending, err := cl.ListApprovals(cctx, model.ApprovalPending, 0, 0)
				if err != nil || len(pending) != 1 {
					t.Fatalf("GET /approvals 应有一条待审批，得到 %+v / %v", pending, err)
				}
				if _, err := cl.DecideApproval(cctx, re.Approval.ID, model.Decision{Approve: true, Scope: model.ScopeOnce}); err != nil {
					t.Fatalf("批准失败: %v", err)
				}
				approved = true
			case model.EventRunFailed:
				t.Fatalf("运行失败: %s；事件 %v", re.Error, types)
			case model.EventRunCompleted:
				break loop
			}
		case <-timeout:
			t.Fatalf("等待运行完成超时；已收到 %v", types)
		}
	}
	t.Logf("事件序列：%v", types)
	if !approved {
		t.Fatal("run_command 在 ask_risky 下应请求审批")
	}
	assertOrdered(t, types, "run.started", "tool.call.started", "approval.requested", "approval.resolved", "tool.call.completed", "text.delta", "run.completed")

	msgs, err := cl.ListMessages(cctx, sess.ID, 0, 0)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("应有用户与助手两条消息，得到 %d / %v", len(msgs), err)
	}
	var parts []string
	var toolOut, text string
	for _, p := range msgs[1].Parts {
		parts = append(parts, string(p.Type))
		if p.Type == model.PartToolResult && p.ToolResult != nil {
			toolOut = p.ToolResult.Content
		}
		if p.Type == model.PartText {
			text += p.Text
		}
	}
	t.Logf("助手消息片段：%v；工具输出：%q；文本：%q", parts, toolOut, text)
	for _, want := range []string{"tool_call", "tool_result", "text"} {
		if !contains(parts, want) {
			t.Fatalf("助手消息应含 %s 片段，得到 %v", want, parts)
		}
	}
	if !strings.Contains(toolOut, "hi") || !strings.Contains(text, "hi") {
		t.Fatalf("工具输出与最终文本都应包含 hi，得到 %q / %q", toolOut, text)
	}

	final, err := cl.GetRun(cctx, run.ID)
	if err != nil || final.Status != model.RunCompleted || final.Usage.InputTokens == 0 {
		t.Fatalf("运行应完成并记录用量，得到 %+v / %v", final, err)
	}

	audit, err := cl.ListAudit(cctx, client.AuditQuery{RunID: run.ID})
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for _, e := range audit {
		actions = append(actions, e.Action)
	}
	if !contains(actions, permission.ActionToolCheck) || !contains(actions, permission.ActionApprovalDecide) {
		t.Fatalf("审计应包含 tool.check 与 approval.decide，得到 %v", actions)
	}

	llm.mu.Lock()
	defer llm.mu.Unlock()
	if len(llm.requests) != 2 {
		t.Fatalf("模型服务应收到两次请求（工具调用前后），得到 %d", len(llm.requests))
	}
	for _, a := range llm.auth {
		if a != "Bearer "+fakeAPIKey {
			t.Fatalf("工作进程应用 secret 存储里的密钥鉴权，得到 %q", a)
		}
	}
	// 系统提示只经 agent.system_prompt 注入一次。
	systems := 0
	for _, m := range llm.requests[0]["messages"].([]any) {
		if m.(map[string]any)["role"] == "system" {
			systems++
		}
	}
	if systems > 1 {
		t.Fatalf("系统提示被注入了 %d 次", systems)
	}
	// 密钥不应出现在日志、数据库或事件里：检查数据库文件。
	db, _ := os.ReadFile(filepath.Join(home, "gogosling.db"))
	if strings.Contains(string(db), fakeAPIKey) {
		t.Fatal("数据库里不应出现明文密钥")
	}
}

func assertOrdered(t *testing.T, got []string, want ...string) {
	t.Helper()
	i := 0
	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
		}
	}
	if i != len(want) {
		t.Fatalf("事件应按顺序包含 %v，得到 %v", want, got)
	}
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
