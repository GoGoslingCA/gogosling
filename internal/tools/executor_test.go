package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

type execFixture struct {
	ws       *fakeWorkspace
	registry *Registry
	checker  *fakeChecker
	external *fakeExternal
	rc       RunContext
}

func newExecutor(t *testing.T, f *execFixture, maxBytes int) *Executor {
	t.Helper()
	deps := Deps{Registry: f.registry, Resolver: fakeResolver{rc: f.rc}, Permissions: f.checker, MaxResultBytes: maxBytes}
	if f.external != nil {
		deps.External = f.external
	}
	e, err := NewExecutor(deps)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func newFixture(t *testing.T, tools ...Tool) *execFixture {
	t.Helper()
	ws := newFakeWorkspace(t)
	r := NewRegistry()
	for _, tt := range tools {
		if err := r.Register(tt); err != nil {
			t.Fatal(err)
		}
	}
	return &execFixture{ws: ws, registry: r, checker: allowAll(), rc: testRunContext(t, ws, nil)}
}

func call(name string, args string) model.ToolCall {
	return model.ToolCall{ID: "call_1", Name: name, Arguments: json.RawMessage(args)}
}

func TestNewExecutorRequiresDeps(t *testing.T) {
	if _, err := NewExecutor(Deps{}); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("缺少依赖应为 CodeInvalid，得到 %v", err)
	}
}

func TestExecuteUnknownToolIsErrorResult(t *testing.T) {
	f := newFixture(t, &stubTool{name: "known"})
	e := newExecutor(t, f, 0)
	res, err := e.Execute(context.Background(), "01RUN", call("missing", `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content, string(model.CodeNotFound)) || res.CallID != "call_1" {
		t.Fatalf("未知工具应返回 not_found 的错误结果: %+v", res)
	}
	if len(f.checker.reqs) != 0 {
		t.Fatal("未知工具不应进入权限判定")
	}
}

func TestExecuteDisabledToolIsNotFound(t *testing.T) {
	tool := &stubTool{name: "alpha"}
	f := newFixture(t, tool)
	f.rc.Agent = testAgent([]string{"beta"})
	e := newExecutor(t, f, 0)
	res, err := e.Execute(context.Background(), "01RUN", call("alpha", `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || tool.calls != 0 {
		t.Fatalf("未对智能体启用的工具不应执行: %+v calls=%d", res, tool.calls)
	}
}

func TestExecuteInvalidArgumentsIsErrorResult(t *testing.T) {
	f := newFixture(t, NewReadFile())
	e := newExecutor(t, f, 0)
	for _, args := range []string{`[1,2]`, `{"path": 5}`, `{"path": ""}`, `not json`} {
		res, err := e.Execute(context.Background(), "01RUN", call("read_file", args))
		if err != nil {
			t.Fatalf("args=%s: %v", args, err)
		}
		if !res.IsError || !strings.Contains(res.Content, string(model.CodeInvalid)) {
			t.Fatalf("args=%s 应返回 invalid 的错误结果: %+v", args, res)
		}
	}
	if len(f.checker.reqs) != 0 {
		t.Fatal("参数非法不应进入权限判定")
	}
}

func TestExecuteDeniedByVerdictIsErrorResult(t *testing.T) {
	tool := &stubTool{name: "alpha"}
	f := newFixture(t, tool)
	f.checker = &fakeChecker{verdict: Verdict{Allowed: false, Reason: "规则 r1 拒绝"}}
	e := newExecutor(t, f, 0)
	res, err := e.Execute(context.Background(), "01RUN", call("alpha", `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content, "denied") || !strings.Contains(res.Content, "规则 r1 拒绝") {
		t.Fatalf("拒绝应转成给模型的说明: %+v", res)
	}
	if tool.calls != 0 {
		t.Fatal("被拒绝的工具不应执行")
	}
}

func TestExecuteDeniedByErrorIsErrorResult(t *testing.T) {
	tool := &stubTool{name: "alpha"}
	f := newFixture(t, tool)
	f.checker = &fakeChecker{err: model.NewError(model.CodeDenied, "用户拒绝了审批")}
	e := newExecutor(t, f, 0)
	res, err := e.Execute(context.Background(), "01RUN", call("alpha", `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content, "用户拒绝了审批") || tool.calls != 0 {
		t.Fatalf("CodeDenied 错误应转成错误结果: %+v", res)
	}
}

func TestExecutePermissionInfrastructureFailure(t *testing.T) {
	tool := &stubTool{name: "alpha"}
	f := newFixture(t, tool)
	f.checker = &fakeChecker{err: errors.New("db down")}
	e := newExecutor(t, f, 0)
	res, err := e.Execute(context.Background(), "01RUN", call("alpha", `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || strings.Contains(res.Content, "db down") || tool.calls != 0 {
		t.Fatalf("权限基础设施故障应转成不泄露内部细节的错误结果: %+v", res)
	}
}

func TestExecutePassesContextToPermissionChecker(t *testing.T) {
	f := newFixture(t, NewWriteFile())
	f.rc.Untrusted = true
	e := newExecutor(t, f, 0)
	res, err := e.Execute(context.Background(), "01RUN", call("write_file", `{"path":"a/b.txt","content":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("应成功: %+v", res)
	}
	req := f.checker.last(t)
	if req.AgentID != "01AGENT" || req.RunID != "01RUN" || req.SessionID != "01SESSION" || req.Autonomy != model.AutonomyAskRisky {
		t.Errorf("上下文字段不完整: %+v", req)
	}
	if req.Workspace != f.ws.Root() || !req.UntrustedInput || req.Principal.ID != "u1" {
		t.Errorf("工作区 / 不可信标记 / 身份未传递: %+v", req)
	}
	if req.Tool.Name != "write_file" || req.Tool.Risk != model.RiskWrite || req.Call.ID != "call_1" {
		t.Errorf("工具声明或调用未传递: %+v", req)
	}
	if !strings.Contains(req.Summary, "a/b.txt") {
		t.Errorf("摘要应包含路径: %q", req.Summary)
	}
	var details map[string]any
	if err := json.Unmarshal(req.Details, &details); err != nil || details["path"] != "a/b.txt" {
		t.Errorf("细节应包含 path: %s", req.Details)
	}
}

func TestExecuteTruncatesLongResults(t *testing.T) {
	long := strings.Repeat("甲", 50_000) // 150 KB
	tool := &stubTool{name: "big", execute: func(context.Context, RunContext, json.RawMessage) (model.ToolResult, error) {
		return model.ToolResult{Content: long}, nil
	}}
	f := newFixture(t, tool)
	e := newExecutor(t, f, 0)
	res, err := e.Execute(context.Background(), "01RUN", call("big", `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Content) > DefaultMaxResultBytes+200 || !strings.Contains(res.Content, "[结果已截断") {
		t.Fatalf("结果应被截断到约 64 KiB 并附说明，长度 %d", len(res.Content))
	}
	if !strings.HasPrefix(res.Content, "甲") || strings.Contains(res.Content, "�") {
		t.Fatal("截断不应破坏多字节字符")
	}

	e = newExecutor(t, f, 100)
	res, _ = e.Execute(context.Background(), "01RUN", call("big", `{}`))
	if !strings.Contains(res.Content, "仅保留前 99 字节") {
		t.Fatalf("自定义上限应生效: %q", res.Content[:min(len(res.Content), 200)])
	}
}

func TestExecuteToolErrorBecomesErrorResult(t *testing.T) {
	tool := &stubTool{name: "boom", execute: func(context.Context, RunContext, json.RawMessage) (model.ToolResult, error) {
		return model.ToolResult{}, model.Wrap(model.CodeInternal, "磁盘写入失败", errors.New("/abs/secret/path: EIO"))
	}}
	f := newFixture(t, tool)
	e := newExecutor(t, f, 0)
	res, err := e.Execute(context.Background(), "01RUN", call("boom", `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content, "磁盘写入失败") || strings.Contains(res.Content, "/abs/secret") {
		t.Fatalf("工具错误应只暴露消息而不暴露底层错误: %+v", res)
	}

	plain := &stubTool{name: "plain", execute: func(context.Context, RunContext, json.RawMessage) (model.ToolResult, error) {
		return model.ToolResult{}, errors.New("raw internal detail")
	}}
	f = newFixture(t, plain)
	e = newExecutor(t, f, 0)
	res, _ = e.Execute(context.Background(), "01RUN", call("plain", `{}`))
	if !res.IsError || strings.Contains(res.Content, "raw internal detail") {
		t.Fatalf("非领域错误不应把原文交给模型: %+v", res)
	}
}

func TestExecuteCancellationIsAnError(t *testing.T) {
	tool := &stubTool{name: "slow", execute: func(ctx context.Context, _ RunContext, _ json.RawMessage) (model.ToolResult, error) {
		return model.ToolResult{}, model.Wrap(model.CodeCancelled, "被取消", context.Canceled)
	}}
	f := newFixture(t, tool)
	e := newExecutor(t, f, 0)
	if _, err := e.Execute(context.Background(), "01RUN", call("slow", `{}`)); !model.IsCode(err, model.CodeCancelled) {
		t.Fatalf("工具报告取消应作为 error 返回，得到 %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.Execute(ctx, "01RUN", call("slow", `{}`)); !model.IsCode(err, model.CodeCancelled) {
		t.Fatalf("已取消的 ctx 应返回 CodeCancelled，得到 %v", err)
	}
}

func TestExecuteResolverAndCallIDErrors(t *testing.T) {
	f := newFixture(t, &stubTool{name: "alpha"})
	e, err := NewExecutor(Deps{Registry: f.registry, Resolver: fakeResolver{err: model.NotFound("run", "x")}, Permissions: f.checker})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Execute(context.Background(), "x", call("alpha", `{}`)); !model.IsCode(err, model.CodeNotFound) {
		t.Fatalf("无法解析运行应返回 error，得到 %v", err)
	}
	e = newExecutor(t, f, 0)
	if _, err := e.Execute(context.Background(), "01RUN", model.ToolCall{Name: "alpha"}); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("缺少调用 ID 应返回 CodeInvalid，得到 %v", err)
	}
	f.rc.Agent = nil
	e = newExecutor(t, f, 0)
	if _, err := e.Execute(context.Background(), "01RUN", call("alpha", `{}`)); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("缺少智能体的上下文应返回 CodeInvalid，得到 %v", err)
	}
}

func TestExecuteRoutesExternalTools(t *testing.T) {
	f := newFixture(t, &stubTool{name: "alpha"})
	f.external = &fakeExternal{
		specs: []model.ToolSpec{{Name: "mcp_echo", Description: "e", InputSchema: json.RawMessage(`{"type":"object"}`), Source: "mcp:ext1", Risk: model.RiskNetwork}},
		res:   model.ToolResult{Content: "from mcp"},
	}
	e := newExecutor(t, f, 0)
	res, err := e.Execute(context.Background(), "01RUN", call("mcp_echo", `{"q":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || res.Content != "from mcp" || res.CallID != "call_1" {
		t.Fatalf("扩展工具应被调用: %+v", res)
	}
	if len(f.external.calls) != 1 || f.external.calls[0].Name != "mcp_echo" {
		t.Fatalf("CallTool 应收到调用: %+v", f.external.calls)
	}
	req := f.checker.last(t)
	if req.Tool.Name != "mcp_echo" || req.Tool.Risk != model.RiskNetwork || req.Summary == "" {
		t.Fatalf("扩展工具也应经过权限判定: %+v", req)
	}

	specs, err := e.ToolsFor(context.Background(), f.rc.Agent)
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 2 || specs[0].Name != "alpha" || specs[1].Name != "mcp_echo" {
		t.Fatalf("ToolsFor 应合并内置与扩展工具: %+v", specs)
	}

	f.external.specs = append(f.external.specs, model.ToolSpec{Name: "alpha", Description: "dup", Source: "mcp:ext1", Risk: model.RiskRead})
	specs, _ = e.ToolsFor(context.Background(), f.rc.Agent)
	if len(specs) != 2 {
		t.Fatalf("与内置重名的扩展工具应被跳过: %+v", specs)
	}

	f.external.err = errors.New("mcp down")
	res, err = e.Execute(context.Background(), "01RUN", call("mcp_echo", `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("扩展不可用时应返回错误结果而不是中断")
	}
	if _, err := e.ToolsFor(context.Background(), f.rc.Agent); err == nil {
		t.Fatal("ToolsFor 应传递扩展错误")
	}
}

func TestClipResultKeepsShortContent(t *testing.T) {
	out, truncated := clipResult("short", 100)
	if truncated || out != "short" {
		t.Fatal("未超限不应截断")
	}
	s, cut := truncateUTF8("ab世界", 3)
	if !cut || s != "ab" {
		t.Fatalf("应在字符边界截断，得到 %q", s)
	}
}
