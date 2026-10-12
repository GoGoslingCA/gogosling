package session

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

func TestSendCompletesRun(t *testing.T) {
	h := newHarness(t, Options{PromptBuilder: fakePrompt{suffix: "\n可用技能：无"}})
	ctx := context.Background()
	sess := h.session()
	sub := h.subscribe()

	run, err := h.svc.Send(ctx, SendRequest{
		SessionID:   sess.ID,
		Text:        "你好",
		Attachments: []model.Attachment{{Name: "a.txt", Path: "inbound/a.txt", Size: 3}},
		Principal:   model.Principal{ID: "u1", Kind: model.PrincipalLocalUser},
	})
	if err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if run.Status != model.RunQueued || run.Trigger != model.TriggerUser || run.InputMessageID == "" {
		t.Fatalf("运行初始状态错误: %+v", run)
	}
	if id, ok := h.svc.ActiveRunID(sess.ID); !ok || id != run.ID {
		t.Fatalf("会话应登记在途运行 %s，得到 %s/%v", run.ID, id, ok)
	}

	// 运行请求：最终系统提示来自 PromptBuilder 且只放在 Agent.SystemPrompt（Messages 不含 system 消息，
	// 否则工作进程会重复注入），历史含本次用户消息，凭据与工具透传。
	req := h.rt.lastRequest(t)
	if req.RunID != run.ID || req.SessionID != sess.ID || req.Credentials.APIKey != "test-key" || len(req.Tools) != 1 {
		t.Fatalf("运行请求字段错误: %+v", req)
	}
	if req.Agent.SystemPrompt != "你是助理\n可用技能：无" {
		t.Fatalf("RunRequest.Agent.SystemPrompt 应为 PromptBuilder 的输出，得到 %q", req.Agent.SystemPrompt)
	}
	if len(req.Messages) != 1 {
		t.Fatalf("应只有用户消息（不含 system 消息），得到 %+v", req.Messages)
	}
	user := req.Messages[0]
	if user.Role != model.RoleUser || user.RunID != run.ID || len(user.Parts) != 2 || user.Parts[1].Type != model.PartAttachment {
		t.Fatalf("用户消息错误: %+v", user)
	}
	var meta map[string]any
	if err := json.Unmarshal(user.Meta, &meta); err != nil {
		t.Fatalf("Meta 不是 JSON: %v", err)
	}
	if meta[MetaTrigger] != "user" || meta[MetaPrincipalID] != "u1" || meta[MetaUntrusted] != false {
		t.Fatalf("Meta 应记录来源与身份: %v", meta)
	}
	if req.Options != DefaultRunOptions {
		t.Fatalf("未配置时应使用默认推理参数，得到 %+v", req.Options)
	}

	evs := drain(sub)
	if types := eventTypes(evs); len(types) != 2 || types[0] != string(model.EventMessageCreated) || types[1] != EventSessionUpdated {
		t.Fatalf("发送后应发布 message.created 与自动标题的 session.updated，得到 %v", types)
	}
	if got, _ := h.svc.Get(ctx, sess.ID); got.Title != "你好" {
		t.Fatalf("首条消息应自动成为标题，得到 %q", got.Title)
	}

	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	h.rt.emit(t, run.ID, model.RunEvent{Type: model.EventRunStarted, At: at})
	h.rt.emit(t, run.ID, model.RunEvent{Type: model.EventTextDelta, Delta: "你"})
	h.rt.emit(t, run.ID, model.RunEvent{Type: model.EventTextDelta, Delta: "好"})
	h.rt.emit(t, run.ID, model.RunEvent{Type: model.EventUsage, Usage: &model.Usage{InputTokens: 10, OutputTokens: 2}})
	final := &model.Message{Parts: []model.Part{{Type: model.PartText, Text: "你好！"}}}
	h.rt.emit(t, run.ID, model.RunEvent{Type: model.EventRunCompleted, Message: final, Usage: &model.Usage{InputTokens: 10, OutputTokens: 2, CacheReadTokens: 4}})

	evs = drain(sub)
	want := []string{"run.started", "text.delta", "text.delta", "usage", "message.created", "run.completed", EventSessionUpdated}
	if got := eventTypes(evs); len(got) != len(want) {
		t.Fatalf("事件序列错误: %v", got)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("事件序列错误: %v，期望 %v", got, want)
			}
		}
	}
	for _, ev := range evs[:6] {
		if ev.RunID != run.ID || ev.SessionID != sess.ID || ev.AgentID != h.agent.ID {
			t.Fatalf("运行事件应填好路由字段: %+v", ev)
		}
		if re := runEventOf(t, ev); re.RunID != run.ID {
			t.Fatalf("payload 的 RunID 应被补全: %+v", re)
		}
	}

	stored := mustRun(t, h.store, run.ID)
	if stored.Status != model.RunCompleted || stored.StartedAt == nil || !stored.StartedAt.Equal(at) || stored.EndedAt == nil {
		t.Fatalf("运行应完成并记录时间: %+v", stored)
	}
	if stored.Usage != (model.Usage{InputTokens: 10, OutputTokens: 2, CacheReadTokens: 4}) {
		t.Fatalf("应采用终态事件给出的累计用量，得到 %+v", stored.Usage)
	}
	msgs := mustMessages(t, h.store, sess.ID)
	if len(msgs) != 2 || msgs[1].Role != model.RoleAssistant || msgs[1].Text() != "你好！" || msgs[1].RunID != run.ID {
		t.Fatalf("应写入助手消息: %+v", msgs)
	}
	if stored.OutputMessageID != msgs[1].ID {
		t.Fatal("运行应关联输出消息")
	}
	if re := runEventOf(t, evs[5]); re.Usage == nil || re.Message == nil || re.Message.ID != msgs[1].ID {
		t.Fatalf("run.completed 应带用量与最终消息: %+v", re)
	}
	if _, ok := h.svc.ActiveRunID(sess.ID); ok {
		t.Fatal("完成后会话不应再有在途运行")
	}
	if got, _ := h.svc.GetRun(ctx, run.ID); got.Status != model.RunCompleted {
		t.Fatal("GetRun 应读到终态")
	}
}

func TestSendForwardsToolAndApprovalEvents(t *testing.T) {
	h := newHarness(t, Options{})
	sess := h.session()
	run := h.send(sess.ID, "列目录")
	sub := h.subscribe()

	call := &model.ToolCall{ID: "c1", Name: "read_file", Arguments: json.RawMessage(`{"path":"a"}`)}
	h.rt.emit(t, run.ID, model.RunEvent{Type: model.EventRunStarted})
	h.rt.emit(t, run.ID, model.RunEvent{Type: model.EventToolCallStarted, ToolCall: call})
	h.rt.emit(t, run.ID, model.RunEvent{Type: model.EventApprovalRequested, Approval: &model.ApprovalRequest{ID: "ap1", ToolCall: *call}})
	if got := mustRun(t, h.store, run.ID); got.Status != model.RunAwaitingApproval {
		t.Fatalf("审批请求后应为 awaiting_approval，得到 %s", got.Status)
	}
	h.rt.emit(t, run.ID, model.RunEvent{Type: model.EventApprovalResolved, Approval: &model.ApprovalRequest{ID: "ap1", Status: model.ApprovalApproved}})
	if got := mustRun(t, h.store, run.ID); got.Status != model.RunRunning {
		t.Fatalf("审批通过后应回到 running，得到 %s", got.Status)
	}
	h.rt.emit(t, run.ID, model.RunEvent{Type: model.EventToolCallCompleted, ToolResult: &model.ToolResult{CallID: "c1", Content: "ok"}})
	finalParts := []model.Part{
		{Type: model.PartToolCall, ToolCall: call},
		{Type: model.PartToolResult, ToolResult: &model.ToolResult{CallID: "c1", Content: "ok"}},
		{Type: model.PartText, Text: "完成"},
	}
	h.rt.emit(t, run.ID, model.RunEvent{Type: model.EventRunCompleted, Message: &model.Message{Parts: finalParts}})

	evs := drain(sub)
	want := []string{"run.started", "tool.call.started", "approval.requested", "approval.resolved", "tool.call.completed", "message.created", "run.completed", EventSessionUpdated}
	got := eventTypes(evs)
	if len(got) != len(want) {
		t.Fatalf("事件序列错误: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("事件序列错误: %v，期望 %v", got, want)
		}
	}
	if re := runEventOf(t, evs[1]); re.ToolCall == nil || re.ToolCall.ID != "c1" {
		t.Fatalf("tool.call.started 应原样转发 ToolCall: %+v", re)
	}
	msgs := mustMessages(t, h.store, sess.ID)
	if len(msgs) != 2 || len(msgs[1].Parts) != 3 || msgs[1].Parts[0].Type != model.PartToolCall {
		t.Fatalf("助手消息应包含完整的工具片段: %+v", msgs[1])
	}
}

func TestSendRunFailedKeepsPartialOutputAndUsage(t *testing.T) {
	h := newHarness(t, Options{})
	sess := h.session()
	run := h.send(sess.ID, "讲个故事")
	sub := h.subscribe()

	h.rt.emit(t, run.ID, model.RunEvent{Type: model.EventRunStarted})
	h.rt.emit(t, run.ID, model.RunEvent{Type: model.EventTextDelta, Delta: "从前"})
	h.rt.emit(t, run.ID, model.RunEvent{Type: model.EventUsage, Usage: &model.Usage{InputTokens: 5, OutputTokens: 1}})
	h.rt.emit(t, run.ID, model.RunEvent{Type: model.EventRunFailed, Error: "供应商超时"})
	// 终态之后的事件必须被忽略。
	h.rt.emit(t, run.ID, model.RunEvent{Type: model.EventRunCompleted, Message: &model.Message{Parts: []model.Part{{Type: model.PartText, Text: "迟到"}}}})

	stored := mustRun(t, h.store, run.ID)
	if stored.Status != model.RunFailed || stored.Error != "供应商超时" || stored.EndedAt == nil {
		t.Fatalf("运行应失败并记录原因: %+v", stored)
	}
	if stored.Usage.InputTokens != 5 || stored.Usage.OutputTokens != 1 {
		t.Fatalf("失败的运行也应记录用量，得到 %+v", stored.Usage)
	}
	msgs := mustMessages(t, h.store, sess.ID)
	if len(msgs) != 2 || msgs[1].Text() != "从前" {
		t.Fatalf("应保留部分输出: %+v", msgs)
	}
	var meta map[string]any
	_ = json.Unmarshal(msgs[1].Meta, &meta)
	if meta[MetaPartial] != true {
		t.Fatalf("部分输出应在 Meta 中标记 partial: %v", meta)
	}
	if stored.OutputMessageID != msgs[1].ID {
		t.Fatal("失败的运行应关联部分输出消息")
	}
	types := eventTypes(drain(sub))
	want := []string{"run.started", "text.delta", "usage", "message.created", "run.failed", EventSessionUpdated}
	if len(types) != len(want) {
		t.Fatalf("事件序列错误: %v", types)
	}
	if _, ok := h.svc.ActiveRunID(sess.ID); ok {
		t.Fatal("失败后会话应释放")
	}
}

func TestStartRunErrorMarksRunFailed(t *testing.T) {
	h := newHarness(t, Options{})
	h.rt.startErr = model.NewError(model.CodeUnavailable, "工作进程未就绪")
	sess := h.session()
	sub := h.subscribe()

	run, err := h.svc.Send(context.Background(), SendRequest{SessionID: sess.ID, Text: "hi"})
	if !model.IsCode(err, model.CodeUnavailable) {
		t.Fatalf("应透传运行时的错误码，得到 %v", err)
	}
	if run == nil || run.Status != model.RunFailed || run.Error != "工作进程未就绪" {
		t.Fatalf("应返回失败的运行记录: %+v", run)
	}
	stored := mustRun(t, h.store, run.ID)
	if stored.Status != model.RunFailed {
		t.Fatalf("数据库中的运行应为 failed，得到 %s", stored.Status)
	}
	types := eventTypes(drain(sub))
	if len(types) < 3 || types[0] != "message.created" || types[len(types)-2] != "run.failed" {
		t.Fatalf("应发布 message.created … run.failed，得到 %v", types)
	}
	if _, ok := h.svc.ActiveRunID(sess.ID); ok {
		t.Fatal("启动失败后会话应释放")
	}
	// 用户消息保留，随后可以重发。
	if msgs := mustMessages(t, h.store, sess.ID); len(msgs) != 1 {
		t.Fatalf("用户消息应保留，得到 %d 条", len(msgs))
	}
	h.rt.startErr = nil
	if _, err := h.svc.Send(context.Background(), SendRequest{SessionID: sess.ID, Text: "again"}); err != nil {
		t.Fatalf("恢复后重发应成功: %v", err)
	}
}

func TestSendFailsBeforePersistWhenCredentialsMissing(t *testing.T) {
	h := newHarness(t, Options{})
	h.creds.err = model.NewError(model.CodeNotFound, "供应商未配置")
	sess := h.session()

	if _, err := h.svc.Send(context.Background(), SendRequest{SessionID: sess.ID, Text: "hi"}); !model.IsCode(err, model.CodeNotFound) {
		t.Fatalf("凭据缺失应直接返回错误，得到 %v", err)
	}
	if msgs := mustMessages(t, h.store, sess.ID); len(msgs) != 0 {
		t.Fatal("配置错误不应留下用户消息")
	}
	if runs, _ := h.store.ListRuns(context.Background(), sess.ID, model.Page{}); len(runs) != 0 {
		t.Fatal("配置错误不应创建运行记录")
	}
	if _, ok := h.svc.ActiveRunID(sess.ID); ok {
		t.Fatal("占位应被释放")
	}
}

func TestSendRejectsInvalidStates(t *testing.T) {
	h := newHarness(t, Options{})
	ctx := context.Background()
	sess := h.session()

	if _, err := h.svc.Send(ctx, SendRequest{SessionID: sess.ID, Text: "  \n"}); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("空消息应 invalid，得到 %v", err)
	}
	if _, err := h.svc.Send(ctx, SendRequest{Text: "x"}); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("缺少会话 ID 应 invalid，得到 %v", err)
	}
	if _, err := h.svc.Send(ctx, SendRequest{SessionID: "missing", Text: "x"}); !model.IsCode(err, model.CodeNotFound) {
		t.Fatalf("会话不存在应 not_found，得到 %v", err)
	}
	archived := model.SessionArchived
	if _, err := h.svc.Update(ctx, sess.ID, SessionUpdate{Status: &archived}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Send(ctx, SendRequest{SessionID: sess.ID, Text: "x"}); !model.IsCode(err, model.CodeConflict) {
		t.Fatalf("归档会话应 conflict，得到 %v", err)
	}
	active := model.SessionActive
	if _, err := h.svc.Update(ctx, sess.ID, SessionUpdate{Status: &active}); err != nil {
		t.Fatal(err)
	}
	disabled := *h.agent
	disabled.Enabled = false
	h.agents.set(&disabled)
	if _, err := h.svc.Send(ctx, SendRequest{SessionID: sess.ID, Text: "x"}); !model.IsCode(err, model.CodeConflict) {
		t.Fatalf("智能体停用应 conflict，得到 %v", err)
	}
}

func TestConcurrentSendOnlyOneWins(t *testing.T) {
	h := newHarness(t, Options{})
	ctx := context.Background()
	sess := h.session()

	const n = 16
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		winners   []*model.Run
		conflicts int
		others    []error
	)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			run, err := h.svc.Send(ctx, SendRequest{SessionID: sess.ID, Text: "并发"})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				winners = append(winners, run)
			case model.IsCode(err, model.CodeConflict):
				conflicts++
			default:
				others = append(others, err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if len(winners) != 1 || conflicts != n-1 || len(others) != 0 {
		t.Fatalf("应恰好一个成功、其余 conflict：成功 %d，冲突 %d，其他 %v", len(winners), conflicts, others)
	}
	if msgs := mustMessages(t, h.store, sess.ID); len(msgs) != 1 {
		t.Fatalf("只应写入一条用户消息，得到 %d", len(msgs))
	}
	if _, err := h.svc.Send(ctx, SendRequest{SessionID: sess.ID, Text: "再来"}); !model.IsCode(err, model.CodeConflict) {
		t.Fatalf("运行未结束时再次发送应冲突，得到 %v", err)
	}
	h.rt.emit(t, winners[0].ID, model.RunEvent{Type: model.EventRunCompleted})
	if _, err := h.svc.Send(ctx, SendRequest{SessionID: sess.ID, Text: "再来"}); err != nil {
		t.Fatalf("运行结束后应能继续发送: %v", err)
	}
}

func TestCancelRun(t *testing.T) {
	h := newHarness(t, Options{})
	ctx := context.Background()
	sess := h.session()
	run := h.send(sess.ID, "慢任务")
	h.rt.emit(t, run.ID, model.RunEvent{Type: model.EventRunStarted})
	h.rt.emit(t, run.ID, model.RunEvent{Type: model.EventTextDelta, Delta: "正在"})
	sub := h.subscribe()

	got, err := h.svc.Cancel(ctx, run.ID)
	if err != nil {
		t.Fatalf("取消失败: %v", err)
	}
	if h.rt.cancelCount() != 1 {
		t.Fatalf("应转发一次取消，得到 %d", h.rt.cancelCount())
	}
	if got.Status != model.RunCancelled {
		t.Fatalf("工作进程回调 run.cancelled 后应为 cancelled，得到 %s", got.Status)
	}
	if stored := mustRun(t, h.store, run.ID); stored.Status != model.RunCancelled || stored.EndedAt == nil {
		t.Fatalf("数据库应记录 cancelled: %+v", stored)
	}
	if msgs := mustMessages(t, h.store, sess.ID); len(msgs) != 2 || msgs[1].Text() != "正在" {
		t.Fatalf("取消应保留部分输出: %+v", msgs)
	}
	types := eventTypes(drain(sub))
	if len(types) != 3 || types[0] != "message.created" || types[1] != "run.cancelled" || types[2] != EventSessionUpdated {
		t.Fatalf("事件序列错误: %v", types)
	}

	// 幂等：终态后再取消不再转发，返回当前记录。
	again, err := h.svc.Cancel(ctx, run.ID)
	if err != nil || again.Status != model.RunCancelled || h.rt.cancelCount() != 1 {
		t.Fatalf("重复取消应幂等: %v, %+v, cancels=%d", err, again, h.rt.cancelCount())
	}
	if _, err := h.svc.Cancel(ctx, "missing"); !model.IsCode(err, model.CodeNotFound) {
		t.Fatalf("不存在的运行应 not_found，得到 %v", err)
	}
}

func TestCancelWhenRuntimeDoesNotKnowRun(t *testing.T) {
	h := newHarness(t, Options{})
	h.rt.autoCancel = false
	h.rt.cancelErr = model.NotFound("run", "x")
	sess := h.session()
	run := h.send(sess.ID, "x")

	got, err := h.svc.Cancel(context.Background(), run.ID)
	if err != nil || got.Status != model.RunCancelled {
		t.Fatalf("运行时不认识该运行时应在本地标记 cancelled: %v, %+v", err, got)
	}
	if _, ok := h.svc.ActiveRunID(sess.ID); ok {
		t.Fatal("本地取消后会话应释放")
	}
}

func TestCancelRuntimeErrorIsReturned(t *testing.T) {
	h := newHarness(t, Options{})
	h.rt.cancelErr = errors.New("rpc 断开")
	sess := h.session()
	run := h.send(sess.ID, "x")

	got, err := h.svc.Cancel(context.Background(), run.ID)
	if err == nil || got == nil || got.Status != model.RunQueued {
		t.Fatalf("运行时取消失败应返回错误且运行保持在途: %v, %+v", err, got)
	}
	if _, ok := h.svc.ActiveRunID(sess.ID); !ok {
		t.Fatal("取消失败时运行仍应在途")
	}
}

func TestCancelRequestedBeforeAcceptIsResent(t *testing.T) {
	h := newHarness(t, Options{})
	h.rt.autoCancel = false
	sess := h.session()
	// 受理前（StartRun 返回前）发起取消：运行时不认识该运行，本包应在受理后补发。
	h.rt.onStart = func(req RunRequest, _ func(model.RunEvent)) {
		h.rt.mu.Lock()
		h.rt.cancelErr = model.NotFound("run", req.RunID)
		h.rt.mu.Unlock()
		if _, err := h.svc.Cancel(context.Background(), req.RunID); err != nil {
			t.Errorf("受理前取消失败: %v", err)
		}
		h.rt.mu.Lock()
		h.rt.cancelErr = nil
		h.rt.mu.Unlock()
	}
	run := h.send(sess.ID, "x")
	if run.Status != model.RunCancelled {
		t.Fatalf("受理前取消应让运行以 cancelled 结束，得到 %s", run.Status)
	}
	if h.rt.cancelCount() != 2 {
		t.Fatalf("应在受理后补发一次取消，共 2 次，得到 %d", h.rt.cancelCount())
	}
}

func TestSinkCompletesBeforeStartRunReturns(t *testing.T) {
	h := newHarness(t, Options{})
	h.rt.onStart = func(_ RunRequest, sink func(model.RunEvent)) {
		sink(model.RunEvent{Type: model.EventRunStarted})
		sink(model.RunEvent{Type: model.EventRunCompleted, Message: &model.Message{Parts: []model.Part{{Type: model.PartText, Text: "秒回"}}}})
	}
	sess := h.session()
	run := h.send(sess.ID, "快")
	if run.Status != model.RunCompleted {
		t.Fatalf("同步完成的运行应直接返回 completed，得到 %s", run.Status)
	}
	if msgs := mustMessages(t, h.store, sess.ID); len(msgs) != 2 || msgs[1].Text() != "秒回" {
		t.Fatalf("应写入助手消息: %+v", msgs)
	}
}

func TestHistoryWindowStartsAtUserTurn(t *testing.T) {
	ctx := context.Background()
	seed := func(h *harness, sess *model.Session) {
		for _, m := range []struct {
			role model.Role
			text string
		}{
			{model.RoleUser, "u1"}, {model.RoleAssistant, "a1"}, {model.RoleUser, "u2"}, {model.RoleAssistant, "a2"},
		} {
			msg := model.TextMessage(sess.ID, m.role, m.text)
			msg.ID = model.NewID()
			if err := h.store.AppendMessage(ctx, &msg); err != nil {
				t.Fatal(err)
			}
		}
	}
	texts := func(msgs []model.Message) []string {
		out := make([]string, len(msgs))
		for i, m := range msgs {
			out[i] = string(m.Role) + ":" + m.Text()
		}
		return out
	}

	t.Run("窗口足够时按条数截取", func(t *testing.T) {
		h := newHarness(t, Options{HistoryLimit: 3})
		sess := h.session()
		seed(h, sess)
		h.send(sess.ID, "u3")
		got := texts(h.rt.lastRequest(t).Messages)
		want := []string{"user:u2", "assistant:a2", "user:u3"}
		if len(got) != len(want) {
			t.Fatalf("历史窗口错误: %v", got)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("历史窗口错误: %v，期望 %v", got, want)
			}
		}
	})
	t.Run("窗口以助手消息开头时裁到用户回合", func(t *testing.T) {
		h := newHarness(t, Options{HistoryLimit: 2})
		sess := h.session()
		seed(h, sess)
		h.send(sess.ID, "u3")
		got := texts(h.rt.lastRequest(t).Messages)
		if len(got) != 1 || got[0] != "user:u3" {
			t.Fatalf("应丢弃开头的 assistant:a2，得到 %v", got)
		}
	})
	t.Run("没有系统提示时不发系统消息", func(t *testing.T) {
		h := newHarness(t, Options{})
		h.agent.SystemPrompt = ""
		h.agents.set(h.agent)
		sess := h.session()
		h.send(sess.ID, "hi")
		got := h.rt.lastRequest(t).Messages
		if len(got) != 1 || got[0].Role != model.RoleUser {
			t.Fatalf("应只有用户消息，得到 %v", texts(got))
		}
	})
}

func TestRecoverActiveRuns(t *testing.T) {
	h := newHarness(t, Options{})
	ctx := context.Background()
	sess := h.session()
	mk := func(status model.RunStatus) *model.Run {
		r := &model.Run{ID: model.NewID(), SessionID: sess.ID, AgentID: h.agent.ID, Status: status, Trigger: model.TriggerUser, CreatedAt: time.Now()}
		if err := h.store.CreateRun(ctx, r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	queued, running, waiting, done := mk(model.RunQueued), mk(model.RunRunning), mk(model.RunAwaitingApproval), mk(model.RunCompleted)
	// 本进程内仍在途的运行不能被当成遗留记录。
	live := h.send(h.session().ID, "live")
	sub := h.subscribe()

	n, err := h.svc.RecoverActiveRuns(ctx)
	if err != nil || n != 3 {
		t.Fatalf("应恢复 3 条遗留运行，得到 %d, %v", n, err)
	}
	for _, id := range []string{queued.ID, running.ID, waiting.ID} {
		r := mustRun(t, h.store, id)
		if r.Status != model.RunFailed || r.Error != ErrorDaemonRestarted || r.EndedAt == nil {
			t.Fatalf("遗留运行应标记为 failed(daemon restarted): %+v", r)
		}
	}
	if r := mustRun(t, h.store, done.ID); r.Status != model.RunCompleted {
		t.Fatal("已完成的运行不应被改动")
	}
	if r := mustRun(t, h.store, live.ID); r.Status != model.RunQueued {
		t.Fatalf("在途运行不应被恢复逻辑改动，得到 %s", r.Status)
	}
	evs := drain(sub)
	if len(evs) != 3 {
		t.Fatalf("应发布 3 条 run.failed，得到 %v", eventTypes(evs))
	}
	for _, ev := range evs {
		if ev.Type != "run.failed" || runEventOf(t, ev).Error != ErrorDaemonRestarted {
			t.Fatalf("事件错误: %+v", ev)
		}
	}
}

func TestCancelLeftoverRunNotInMemory(t *testing.T) {
	h := newHarness(t, Options{})
	ctx := context.Background()
	sess := h.session()
	leftover := &model.Run{ID: model.NewID(), SessionID: sess.ID, AgentID: h.agent.ID, Status: model.RunRunning, Trigger: model.TriggerUser, CreatedAt: time.Now()}
	if err := h.store.CreateRun(ctx, leftover); err != nil {
		t.Fatal(err)
	}
	got, err := h.svc.Cancel(ctx, leftover.ID)
	if err != nil || got.Status != model.RunCancelled || h.rt.cancelCount() != 0 {
		t.Fatalf("遗留记录应直接标记 cancelled 且不打扰运行时: %v, %+v, cancels=%d", err, got, h.rt.cancelCount())
	}
}

func TestRunTimeoutCancelsAndFails(t *testing.T) {
	h := newHarness(t, Options{RunTimeout: 50 * time.Millisecond})
	h.rt.autoCancel = false
	sess := h.session()
	sub := h.subscribe()
	run := h.send(sess.ID, "永远不回")
	h.rt.emit(t, run.ID, model.RunEvent{Type: model.EventRunStarted})

	ev := waitEvent(t, sub, "run.failed", 3*time.Second)
	if re := runEventOf(t, ev); re.Error != ErrorRunTimeout {
		t.Fatalf("超时原因错误: %+v", re)
	}
	if h.rt.cancelCount() != 1 {
		t.Fatalf("超时应向运行时发一次取消，得到 %d", h.rt.cancelCount())
	}
	if stored := mustRun(t, h.store, run.ID); stored.Status != model.RunFailed || stored.Error != ErrorRunTimeout {
		t.Fatalf("数据库应记录超时失败: %+v", stored)
	}
	// 迟到的终态事件被忽略。
	h.rt.emit(t, run.ID, model.RunEvent{Type: model.EventRunCompleted})
	if stored := mustRun(t, h.store, run.ID); stored.Status != model.RunFailed {
		t.Fatal("终态后的事件不应改变状态")
	}
}

func TestPersistFailureOnCompleteDegradesToFailed(t *testing.T) {
	h := newHarness(t, Options{})
	sess := h.session()
	run := h.send(sess.ID, "x")
	sub := h.subscribe()

	h.store.hooks.mu.Lock()
	h.store.hooks.failAppend = errors.New("磁盘已满")
	h.store.hooks.mu.Unlock()
	h.rt.emit(t, run.ID, model.RunEvent{Type: model.EventRunCompleted, Message: &model.Message{Parts: []model.Part{{Type: model.PartText, Text: "答案"}}}})

	stored := mustRun(t, h.store, run.ID)
	if stored.Status != model.RunFailed || stored.Error != ErrorPersistFailed || stored.OutputMessageID != "" {
		t.Fatalf("写入失败时运行应降级为 failed(persist failed): %+v", stored)
	}
	types := eventTypes(drain(sub))
	if len(types) != 2 || types[0] != "run.failed" || types[1] != EventSessionUpdated {
		t.Fatalf("应发布 run.failed 而不是 run.completed，得到 %v", types)
	}
	if _, ok := h.svc.ActiveRunID(sess.ID); ok {
		t.Fatal("会话应释放")
	}
}

func TestUsageFallsBackToAccumulatedWhenReportedIsSmaller(t *testing.T) {
	h := newHarness(t, Options{})
	sess := h.session()
	run := h.send(sess.ID, "x")
	h.rt.emit(t, run.ID, model.RunEvent{Type: model.EventUsage, Usage: &model.Usage{InputTokens: 100, OutputTokens: 10}})
	h.rt.emit(t, run.ID, model.RunEvent{Type: model.EventUsage, Usage: &model.Usage{InputTokens: 120, OutputTokens: 30}})
	// 终态只报了最后一次调用：以累加值为准。
	h.rt.emit(t, run.ID, model.RunEvent{Type: model.EventRunCompleted, Usage: &model.Usage{InputTokens: 120, OutputTokens: 30}})
	if got := mustRun(t, h.store, run.ID).Usage; got != (model.Usage{InputTokens: 220, OutputTokens: 40}) {
		t.Fatalf("应采用累加用量，得到 %+v", got)
	}
}

func TestSendAfterCloseIsRefused(t *testing.T) {
	h := newHarness(t, Options{})
	sess := h.session()
	run := h.send(sess.ID, "x")
	h.svc.Close()
	if _, err := h.svc.Send(context.Background(), SendRequest{SessionID: h.session().ID, Text: "y"}); !model.IsCode(err, model.CodeUnavailable) {
		t.Fatalf("关闭后发送应 unavailable，得到 %v", err)
	}
	// 关闭后运行时送达的终态仍被处理。
	h.rt.emit(t, run.ID, model.RunEvent{Type: model.EventRunCancelled})
	if stored := mustRun(t, h.store, run.ID); stored.Status != model.RunCancelled {
		t.Fatalf("关闭后到达的终态事件仍应落库，得到 %s", stored.Status)
	}
}

func TestUntrustedAndTriggerFlowIntoMeta(t *testing.T) {
	h := newHarness(t, Options{})
	sess, err := h.svc.EnsureByKey(context.Background(), h.agent.ID, MakeKey(h.agent.ID, model.OriginChannel, "tg:1"), model.OriginChannel)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Send(context.Background(), SendRequest{
		SessionID: sess.ID, Text: "来自 IM", Trigger: model.TriggerChannel, Untrusted: true,
		Principal: model.Principal{ID: "peer-1", Kind: model.PrincipalChannelPeer},
	}); err != nil {
		t.Fatal(err)
	}
	msgs := h.rt.lastRequest(t).Messages
	var meta map[string]any
	if err := json.Unmarshal(msgs[len(msgs)-1].Meta, &meta); err != nil {
		t.Fatal(err)
	}
	if meta[MetaUntrusted] != true || meta[MetaTrigger] != "channel" || meta[MetaPrincipalKind] != "channel_peer" {
		t.Fatalf("不可信来源应随消息 Meta 传给运行时: %v", meta)
	}
	if r := mustRun(t, h.store, msgs[len(msgs)-1].RunID); r.Trigger != model.TriggerChannel {
		t.Fatalf("运行应记录触发来源，得到 %s", r.Trigger)
	}
}

func TestSendSurvivesCallerCancelAfterCommit(t *testing.T) {
	h := newHarness(t, Options{})
	sess := h.session()
	// 调用方的请求上下文已经结束（客户端断开）。提交之前的依赖在测试替身里不看 ctx，
	// 提交之后的 loadHistory（ListMessages 尊重取消）与 StartRun 必须改用运行自己的上下文。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	run, err := h.svc.Send(ctx, SendRequest{SessionID: sess.ID, Text: "断开也要跑完"})
	if err != nil {
		t.Fatalf("记录落盘后客户端断开不应让运行失败: %v", err)
	}
	if run.Status == model.RunFailed {
		t.Fatalf("运行不应失败: %+v", run)
	}
	h.rt.mu.Lock()
	startErrs := append([]error(nil), h.rt.startCtxErrs...)
	h.rt.mu.Unlock()
	if len(startErrs) != 1 || startErrs[0] != nil {
		t.Fatalf("StartRun 应收到未取消的运行上下文，得到 %v", startErrs)
	}
	if req := h.rt.lastRequest(t); len(req.Messages) != 1 || req.Messages[0].Text() != "断开也要跑完" {
		t.Fatalf("历史应正常读取，得到 %+v", req.Messages)
	}
}

func TestSendDoesNotStartRunCancelledBeforeStart(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cancelErr error
	}{
		{"运行时不认识运行时本地终结", model.NotFound("run", "x")},
		{"运行时取消失败时由 Send 收尾", errors.New("rpc 断开")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, Options{})
			h.rt.autoCancel = false
			h.rt.cancelErr = tc.cancelErr
			sess := h.session()
			sub := h.subscribe()
			// 客户端收到 message.created 后立刻取消：发生在提交之后、StartRun 之前（读取历史时）。
			h.store.hooks.mu.Lock()
			h.store.hooks.beforeList = func() {
				runID, ok := h.svc.ActiveRunID(sess.ID)
				if !ok {
					t.Error("提交后运行应在途")
					return
				}
				_, _ = h.svc.Cancel(context.Background(), runID)
			}
			h.store.hooks.mu.Unlock()

			run, err := h.svc.Send(context.Background(), SendRequest{SessionID: sess.ID, Text: "马上取消"})
			if err != nil {
				t.Fatalf("已取消的运行不应作为错误返回: %v", err)
			}
			if run.Status != model.RunCancelled {
				t.Fatalf("运行应以 cancelled 结束，得到 %s", run.Status)
			}
			if n := h.rt.requestCount(); n != 0 {
				t.Fatalf("已取消的运行不应再交给运行时（run.start 含凭据），StartRun 调用了 %d 次", n)
			}
			if stored := mustRun(t, h.store, run.ID); stored.Status != model.RunCancelled {
				t.Fatalf("数据库应记录 cancelled，得到 %s", stored.Status)
			}
			if _, ok := h.svc.ActiveRunID(sess.ID); ok {
				t.Fatal("会话占位应已释放")
			}
			n := 0
			for _, ev := range drain(sub) {
				if ev.Type == string(model.EventRunCancelled) {
					n++
				}
			}
			if n != 1 {
				t.Fatalf("应恰好发布一次 run.cancelled，得到 %d", n)
			}
		})
	}
}
