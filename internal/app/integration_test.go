package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/client"
	"github.com/GoGoslingCA/gogosling/internal/event"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/permission"
)

// stream 包装事件订阅，便于按运行等待事件。
type stream struct {
	t   *testing.T
	evs <-chan event.Event
	// seen 记录收到的全部事件，便于失败时打印。
	seen []event.Event
}

func openStream(t *testing.T, ctx context.Context, cl *client.Client, sessionID string) *stream {
	t.Helper()
	evs, _ := cl.Events(ctx, 0, sessionID)
	return &stream{t: t, evs: evs}
}

// until 收集事件直到 match 返回 true，返回期间收到的属于 runID 的事件类型序列。
func (s *stream) until(runID string, match func(ev event.Event, re model.RunEvent) bool) ([]string, model.RunEvent) {
	s.t.Helper()
	var types []string
	deadline := time.After(20 * time.Second)
	for {
		select {
		case ev, ok := <-s.evs:
			if !ok {
				s.t.Fatalf("事件流意外关闭；已收到 %v", types)
			}
			s.seen = append(s.seen, ev)
			if runID != "" && ev.RunID != runID {
				continue
			}
			var re model.RunEvent
			_ = client.DecodePayload(ev, &re)
			types = append(types, ev.Type)
			if match(ev, re) {
				return types, re
			}
		case <-deadline:
			s.t.Fatalf("等待事件超时；本运行已收到 %v", types)
		}
	}
}

// waitRunStatus 轮询 GET /runs/{id} 直到运行处于 want 状态。
func waitRunStatus(t *testing.T, ctx context.Context, cl *client.Client, runID string, want model.RunStatus) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		got, err := cl.GetRun(ctx, runID)
		if err == nil && got.Status == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("运行 %s 应进入 %s，得到 %+v / %v", runID, want, got, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func isType(t model.RunEventType) func(event.Event, model.RunEvent) bool {
	return func(ev event.Event, _ model.RunEvent) bool { return ev.Type == string(t) }
}

func indexOf(list []string, v string) int {
	for i, x := range list {
		if x == v {
			return i
		}
	}
	return -1
}

// waitWorkerReady 轮询 /version 直到工作进程就绪。
func waitWorkerReady(t *testing.T, ctx context.Context, cl *client.Client) *client.VersionInfo {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		v, err := cl.Version(ctx)
		if err == nil && v.Worker.Status == "ready" {
			return v
		}
		if time.Now().After(deadline) {
			t.Fatalf("工作进程没有就绪：%+v / %v", v, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestConversationThroughLocalAPI(t *testing.T) {
	d := startDaemon(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cl, err := client.Discover(ctx, d.home, client.WithLocale("zh"))
	if err != nil {
		t.Fatalf("Discover 失败: %v", err)
	}

	v := waitWorkerReady(t, ctx, cl)
	if v.Worker.Version != "fake-0.1" || v.Worker.Python != "fake-3.12" {
		t.Fatalf("version 应带工作进程握手信息，得到 %+v", v.Worker)
	}

	prov, err := cl.CreateProvider(ctx, client.ProviderCreate{
		Name: "假供应商", Kind: model.ProviderOpenAICompatible, BaseURL: "http://127.0.0.1:9/v1", APIKey: "sk-integration-secret", DefaultModel: "m",
	})
	if err != nil {
		t.Fatalf("创建供应商失败: %v", err)
	}
	if !prov.HasSecret {
		t.Fatal("供应商应已配置密钥")
	}
	agent, err := cl.CreateAgent(ctx, client.AgentCreate{Name: "助理", Model: model.ModelRef{ProviderID: prov.ID, Model: "m"}})
	if err != nil {
		t.Fatalf("创建智能体失败: %v", err)
	}
	tools, err := cl.AgentTools(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	var toolNames []string
	for _, s := range tools {
		toolNames = append(toolNames, s.Name)
	}
	if indexOf(toolNames, "run_command") < 0 {
		t.Fatalf("默认工具集应包含 run_command，得到 %v", toolNames)
	}
	// LAY-03：本地 API 的工具清单与交给工作进程的清单是同一个 ToolsFor；没有技能的智能体看不到 load_skill。
	if indexOf(toolNames, "load_skill") >= 0 {
		t.Fatalf("没有启用技能的智能体不应列出 load_skill，得到 %v", toolNames)
	}
	sess, err := cl.CreateSession(ctx, agent.ID, "")
	if err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}
	st := openStream(t, ctx, cl, sess.ID)

	// ---- 普通对话：run.started → text.delta → run.completed，消息历史里有助手消息。
	run, err := cl.SendMessage(ctx, sess.ID, "你好", nil)
	if err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	types, final := st.until(run.ID, isType(model.EventRunCompleted))
	for _, want := range []string{"run.started", "text.delta", "message.created", "run.completed"} {
		if indexOf(types, want) < 0 {
			t.Fatalf("事件序列应包含 %s，得到 %v", want, types)
		}
	}
	if indexOf(types, "run.started") > indexOf(types, "text.delta") {
		t.Fatalf("run.started 应先于 text.delta，得到 %v", types)
	}
	if final.Usage == nil || final.Usage.InputTokens != 12 {
		t.Fatalf("run.completed 应带用量，得到 %+v", final.Usage)
	}
	msgs, err := cl.ListMessages(ctx, sess.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].Role != model.RoleUser || msgs[1].Role != model.RoleAssistant || msgs[1].Text() != "你好，世界" {
		t.Fatalf("消息历史应为用户 + 助手，得到 %+v", msgs)
	}
	got, err := cl.GetRun(ctx, run.ID)
	if err != nil || got.Status != model.RunCompleted || got.OutputMessageID != msgs[1].ID {
		t.Fatalf("运行记录应为 completed 并关联助手消息，得到 %+v / %v", got, err)
	}

	// ---- 工具调用：run_command 在 ask_risky 下需要审批 → 批准 → 执行 → 完成。
	run, err = cl.SendMessage(ctx, sess.ID, "#tool 跑个命令", nil)
	if err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	_, req := st.until(run.ID, isType(model.EventApprovalRequested))
	if req.Approval == nil || req.Approval.ToolCall.Name != "run_command" || req.Approval.Risk != model.RiskExecute {
		t.Fatalf("应请求 run_command 的审批，得到 %+v", req.Approval)
	}
	// C2：审批挂起期间运行状态是 awaiting_approval（由组合根把总线上的 approval.* 交给 session.NoteApproval）。
	waitRunStatus(t, ctx, cl, run.ID, model.RunAwaitingApproval)
	pending, err := cl.ListApprovals(ctx, model.ApprovalPending, 0, 0)
	if err != nil || len(pending) != 1 || pending[0].ID != req.Approval.ID {
		t.Fatalf("GET /approvals 应列出待审批请求，得到 %+v / %v", pending, err)
	}
	decided, err := cl.DecideApproval(ctx, req.Approval.ID, model.Decision{Approve: true, Scope: model.ScopeOnce})
	if err != nil || decided.Status != model.ApprovalApproved {
		t.Fatalf("批准失败: %+v / %v", decided, err)
	}
	types, final = st.until(run.ID, isType(model.EventRunCompleted))
	if got, err := cl.GetRun(ctx, run.ID); err != nil || got.Status != model.RunCompleted {
		t.Fatalf("批准并完成后运行应为 completed，得到 %+v / %v", got, err)
	}
	if indexOf(types, "tool.call.completed") < 0 {
		t.Fatalf("批准后应执行工具，得到 %v", types)
	}
	msgs, _ = cl.ListMessages(ctx, sess.ID, 2, 0)
	if len(msgs) != 2 {
		t.Fatalf("应新增用户与助手两条消息，得到 %d", len(msgs))
	}
	var parts []string
	for _, p := range msgs[1].Parts {
		parts = append(parts, string(p.Type))
		if p.Type == model.PartToolResult && (p.ToolResult == nil || p.ToolResult.IsError || !strings.Contains(p.ToolResult.Content, "hi")) {
			t.Fatalf("工具结果应包含命令输出 hi，得到 %+v", p.ToolResult)
		}
	}
	if strings.Join(parts, ",") != "tool_call,tool_result,text" {
		t.Fatalf("助手消息应含 tool_call / tool_result / text，得到 %v", parts)
	}
	audit, err := cl.ListAudit(ctx, client.AuditQuery{RunID: run.ID})
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for _, e := range audit {
		actions = append(actions, e.Action)
	}
	if indexOf(actions, permission.ActionToolCheck) < 0 || indexOf(actions, permission.ActionApprovalDecide) < 0 {
		t.Fatalf("审计应包含 tool.check 与 approval.decide，得到 %v", actions)
	}

	// ---- 取消：挂起的运行被取消；在途时再次发送冲突。
	run, err = cl.SendMessage(ctx, sess.ID, "#slow", nil)
	if err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	st.until(run.ID, isType(model.EventRunStarted))
	if _, err := cl.SendMessage(ctx, sess.ID, "插队", nil); !model.IsCode(err, model.CodeConflict) {
		t.Fatalf("会话有在途运行时发送应 conflict，得到 %v", err)
	}
	if _, err := cl.CancelRun(ctx, run.ID); err != nil {
		t.Fatalf("取消失败: %v", err)
	}
	st.until(run.ID, isType(model.EventRunCancelled))
	got, err = cl.GetRun(ctx, run.ID)
	if err != nil || got.Status != model.RunCancelled {
		t.Fatalf("运行应为 cancelled，得到 %+v / %v", got, err)
	}

	// 密钥只在 secret 存储里：数据库文件与日志之外的任何响应都不应含它。
	provs, _ := cl.ListProviders(ctx)
	for _, p := range provs {
		if strings.Contains(p.BaseURL+p.Name, "sk-integration-secret") {
			t.Fatal("供应商视图不应包含密钥")
		}
	}
	// 事件流里应出现过 worker.status（全局事件随会话过滤一起投递）。
	sawWorker := false
	for _, ev := range st.seen {
		if ev.Type == EventWorkerStatus {
			sawWorker = true
		}
	}
	if !sawWorker {
		t.Error("事件流应包含 worker.status")
	}
}

func TestChannelAndJobsThroughLocalAPI(t *testing.T) {
	d := startDaemon(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cl, err := client.Discover(ctx, d.home)
	if err != nil {
		t.Fatal(err)
	}
	waitWorkerReady(t, ctx, cl)
	prov, err := cl.CreateProvider(ctx, client.ProviderCreate{Name: "p", Kind: model.ProviderOpenAICompatible, BaseURL: "http://127.0.0.1:9/v1", DefaultModel: "m"})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := cl.CreateAgent(ctx, client.AgentCreate{Name: "a", Model: model.ModelRef{ProviderID: prov.ID, Model: "m"}})
	if err != nil {
		t.Fatal(err)
	}

	// 通道：凭据只写 secret 存储，视图里只有 has_secret；console 通道由网关启动。
	ch, err := cl.CreateChannel(ctx, client.ChannelCreate{AgentID: agent.ID, Kind: model.ChannelConsole, Name: "控制台", Secret: "console-secret"})
	if err != nil {
		t.Fatalf("创建通道失败: %v", err)
	}
	if !ch.HasSecret || ch.Status != "running" {
		t.Fatalf("通道应已配置凭据并在运行，得到 %+v", ch)
	}
	if _, err := cl.CreateChannel(ctx, client.ChannelCreate{AgentID: agent.ID, Kind: model.ChannelFeishu, Name: "x"}); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("网关不支持的类型应 invalid，得到 %v", err)
	}
	ch2, err := cl.UpdateChannel(ctx, ch.ID, client.ChannelPatch{Enabled: client.Ptr(false)})
	if err != nil || ch2.Enabled || ch2.Status != "stopped" {
		t.Fatalf("停用后应为 stopped，得到 %+v / %v", ch2, err)
	}
	if err := cl.DeleteChannel(ctx, ch.ID); err != nil {
		t.Fatal(err)
	}

	// 定时任务：push_text 立即触发后，默认桌面会话里出现推送消息。
	job, err := cl.CreateJob(ctx, client.JobCreate{AgentID: agent.ID, Name: "提醒", Schedule: "@daily", Kind: model.JobPushText, Input: "该喝水了"})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if err := cl.RunJob(ctx, job.ID); err != nil {
		t.Fatalf("触发任务失败: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		sessions, err := cl.ListSessions(ctx, client.SessionQuery{AgentID: agent.ID})
		if err != nil {
			t.Fatal(err)
		}
		if len(sessions) == 1 {
			msgs, _ := cl.ListMessages(ctx, sessions[0].ID, 0, 0)
			if len(msgs) == 1 && msgs[0].Role == model.RoleAssistant && msgs[0].Text() == "该喝水了" {
				if sessions[0].Key != agent.ID+":desktop:local" {
					t.Fatalf("推送应进入默认桌面会话，得到键 %q", sessions[0].Key)
				}
				break
			}
		}
		if time.Now().After(deadline) {
			j, _ := cl.GetJob(ctx, job.ID)
			t.Fatalf("推送消息没有出现；任务状态 %+v", j)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// run_agent：经模型发起一次运行，任务等到终态后记为 ok。
	job2, err := cl.CreateJob(ctx, client.JobCreate{AgentID: agent.ID, Name: "问候", Schedule: "@daily", Kind: model.JobRunAgent, Input: "早上好"})
	if err != nil {
		t.Fatal(err)
	}
	if err := cl.RunJob(ctx, job2.ID); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(15 * time.Second)
	for {
		j, err := cl.GetJob(ctx, job2.ID)
		if err == nil && j.LastStatus == model.JobRunOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run_agent 任务应成功完成，得到 %+v / %v", j, err)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// C5：删除智能体时先由 session 删除它的会话（逐个发布 session.deleted），
	// 之后组合根让调度器与网关忘掉它的任务与通道（数据库里已由外键级联删除）。
	if _, err := cl.CreateChannel(ctx, client.ChannelCreate{AgentID: agent.ID, Kind: model.ChannelConsole, Name: "控制台2"}); err != nil {
		t.Fatal(err)
	}
	sessions, err := cl.ListSessions(ctx, client.SessionQuery{AgentID: agent.ID})
	if err != nil || len(sessions) == 0 {
		t.Fatalf("删除前应有会话，得到 %v / %v", sessions, err)
	}
	evs, _ := cl.Events(ctx, 0, "")
	if err := cl.DeleteAgent(ctx, agent.ID); err != nil {
		t.Fatalf("删除智能体失败: %v", err)
	}
	deleted := map[string]bool{}
	waitDeleted := time.After(10 * time.Second)
	for len(deleted) < len(sessions) {
		select {
		case ev, ok := <-evs:
			if !ok {
				t.Fatal("事件流意外关闭")
			}
			if ev.Type == "session.deleted" {
				deleted[ev.SessionID] = true
			}
		case <-waitDeleted:
			t.Fatalf("删除智能体后应为每个会话发布 session.deleted，只收到 %v", deleted)
		}
	}
	if chs, err := cl.ListChannels(ctx, agent.ID); err != nil || len(chs) != 0 {
		t.Fatalf("智能体的通道应随之删除，得到 %v / %v", chs, err)
	}
}
