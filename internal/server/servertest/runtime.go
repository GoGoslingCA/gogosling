package servertest

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/GoGoslingCA/gogosling/internal/identity"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/permission"
	"github.com/GoGoslingCA/gogosling/internal/session"
)

// 用户消息以这些前缀开头时，FakeRuntime 执行对应的脚本；其他文本按"回声"完成。
const (
	// ScriptHold：发出 run.started 后一直等待，直到 CancelRun（发出 run.cancelled）。
	ScriptHold = "/hold"
	// ScriptFail：流出一段文本后以 session.ErrorRunTimeout 失败，用于验证错误文案本地化。
	ScriptFail = "/fail"
	// ScriptApprove：经 permission.Service 发起一次真实的审批（run_command，ask_all），
	// 按审批结果完成运行；审批前可被 CancelRun 取消。
	ScriptApprove = "/approve"
)

// EchoPrefix 是回声脚本回复文本的前缀。
const EchoPrefix = "echo: "

// FakeRuntime 是 session.Runtime 的脚本化实现：不启动工作进程，按用户消息的前缀回调事件。
// 并发安全；StartRun 立即返回，事件在独立 goroutine 中按顺序回调。
type FakeRuntime struct {
	// Permissions 供 ScriptApprove 发起审批；为 nil 时该脚本直接以失败结束。
	Permissions *permission.Service

	mu     sync.Mutex
	runs   map[string]*fakeRun
	starts int
}

type fakeRun struct {
	cancel chan struct{}
	once   sync.Once
}

// NewFakeRuntime 创建假运行时。
func NewFakeRuntime() *FakeRuntime {
	return &FakeRuntime{runs: map[string]*fakeRun{}}
}

// Starts 返回 StartRun 被调用的次数。
func (f *FakeRuntime) Starts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts
}

// StartRun 实现 session.Runtime。
func (f *FakeRuntime) StartRun(ctx context.Context, req session.RunRequest, sink func(model.RunEvent)) error {
	run := &fakeRun{cancel: make(chan struct{})}
	f.mu.Lock()
	f.starts++
	f.runs[req.RunID] = run
	f.mu.Unlock()
	go f.play(ctx, req, run, sink)
	return nil
}

// CancelRun 实现 session.Runtime：在途运行随后发出 run.cancelled；不认识的运行返回 CodeNotFound。
func (f *FakeRuntime) CancelRun(_ context.Context, runID string) error {
	f.mu.Lock()
	run := f.runs[runID]
	f.mu.Unlock()
	if run == nil {
		return model.NotFound("run", runID)
	}
	run.once.Do(func() { close(run.cancel) })
	return nil
}

func (f *FakeRuntime) play(ctx context.Context, req session.RunRequest, run *fakeRun, sink func(model.RunEvent)) {
	defer func() {
		f.mu.Lock()
		delete(f.runs, req.RunID)
		f.mu.Unlock()
	}()
	text := lastUserText(req.Messages)
	cancelled := func() bool {
		select {
		case <-run.cancel:
			sink(model.RunEvent{Type: model.EventRunCancelled})
			return true
		default:
			return false
		}
	}

	sink(model.RunEvent{Type: model.EventRunStarted})
	switch {
	case strings.HasPrefix(text, ScriptHold):
		select {
		case <-run.cancel:
			sink(model.RunEvent{Type: model.EventRunCancelled})
		case <-ctx.Done():
			// 运行上下文结束（超时或关闭）：由 session 包自行收尾。
		}
	case strings.HasPrefix(text, ScriptFail):
		sink(model.RunEvent{Type: model.EventTextDelta, Delta: "partial"})
		if cancelled() {
			return
		}
		sink(model.RunEvent{Type: model.EventRunFailed, Error: session.ErrorRunTimeout})
	case strings.HasPrefix(text, ScriptApprove):
		f.playApproval(ctx, req, run, sink)
	default:
		if cancelled() {
			return
		}
		sink(model.RunEvent{Type: model.EventTextDelta, Delta: EchoPrefix + text})
		usage := model.Usage{InputTokens: 3, OutputTokens: 5}
		sink(model.RunEvent{Type: model.EventUsage, Usage: &usage})
		sink(model.RunEvent{Type: model.EventRunCompleted, Usage: &usage})
	}
}

// playApproval 模拟一次需要审批的工具调用：审批通过则完成，拒绝则以拒绝说明完成，取消则发 run.cancelled。
func (f *FakeRuntime) playApproval(ctx context.Context, req session.RunRequest, run *fakeRun, sink func(model.RunEvent)) {
	if f.Permissions == nil {
		sink(model.RunEvent{Type: model.EventRunFailed, Error: "permissions not wired"})
		return
	}
	call := model.ToolCall{
		ID:        model.NewID(),
		Name:      "run_command",
		Arguments: json.RawMessage(`{"command":"echo hello"}`),
	}
	sink(model.RunEvent{Type: model.EventToolCallStarted, ToolCall: &call})

	cctx, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		select {
		case <-run.cancel:
			stop()
		case <-cctx.Done():
		}
	}()
	agentID := ""
	if req.Agent != nil {
		agentID = req.Agent.ID
	}
	verdict, err := f.Permissions.Check(cctx, permission.Request{
		Principal: identity.LocalPrincipal(),
		AgentID:   agentID,
		Autonomy:  model.AutonomyAskAll,
		SessionID: req.SessionID,
		RunID:     req.RunID,
		Tool: model.ToolSpec{
			Name:        "run_command",
			Description: "run a shell command",
			InputSchema: json.RawMessage(`{"type":"object"}`),
			Source:      "builtin",
			Risk:        model.RiskExecute,
		},
		Call:    call,
		Summary: "echo hello",
		Details: json.RawMessage(`{"command":"echo hello"}`),
	})
	select {
	case <-run.cancel:
		sink(model.RunEvent{Type: model.EventRunCancelled})
		return
	default:
	}
	result := model.ToolResult{CallID: call.ID, Content: "hello"}
	reply := "approved"
	if err != nil || !verdict.Allowed {
		result = model.ToolResult{CallID: call.ID, Content: "denied: " + verdict.Reason, IsError: true}
		reply = "denied"
	}
	sink(model.RunEvent{Type: model.EventToolCallCompleted, ToolResult: &result})
	sink(model.RunEvent{Type: model.EventTextDelta, Delta: reply})
	sink(model.RunEvent{Type: model.EventRunCompleted})
}

// lastUserText 返回消息历史中最后一条用户消息的文本。
func lastUserText(msgs []model.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == model.RoleUser {
			return strings.TrimSpace(msgs[i].Text())
		}
	}
	return ""
}
