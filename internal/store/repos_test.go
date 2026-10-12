package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// ── agents ──────────────────────────────────────────────────────────────────

func TestAgentCRUD(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	a := mustCreateAgent(t, s)
	got, err := s.GetAgent(ctx, a.ID)
	if err != nil {
		t.Fatalf("GetAgent 应成功: %v", err)
	}
	assertSameJSON(t, "agent", a, got)

	// nil 与空切片必须区分：nil 表示默认工具集。
	b := newAgent()
	// 排序测试显式区分时间，不依赖 Windows 时钟的实际分辨率。
	b.CreatedAt = a.CreatedAt.Add(time.Second)
	b.UpdatedAt = b.CreatedAt
	b.Tools = nil
	b.Extensions = []string{}
	if err := s.CreateAgent(ctx, b); err != nil {
		t.Fatal(err)
	}
	gotB, err := s.GetAgent(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotB.Tools != nil {
		t.Errorf("Tools 为 nil 时读回也应为 nil，实际 %#v", gotB.Tools)
	}
	if gotB.Extensions == nil || len(gotB.Extensions) != 0 {
		t.Errorf("Extensions 为空切片时读回应为空切片，实际 %#v", gotB.Extensions)
	}

	list, err := s.ListAgents(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("ListAgents 应返回 2 条，实际 %d err=%v", len(list), err)
	}
	if list[0].ID != a.ID || list[1].ID != b.ID {
		t.Errorf("ListAgents 应按创建时间升序")
	}

	a.Name = "改名"
	a.Enabled = false
	a.Tools = nil
	a.UpdatedAt = ts()
	if err := s.UpdateAgent(ctx, a); err != nil {
		t.Fatalf("UpdateAgent 应成功: %v", err)
	}
	got, _ = s.GetAgent(ctx, a.ID)
	assertSameJSON(t, "updated agent", a, got)

	if err := s.DeleteAgent(ctx, a.ID); err != nil {
		t.Fatalf("DeleteAgent 应成功: %v", err)
	}
	_, err = s.GetAgent(ctx, a.ID)
	assertCode(t, err, model.CodeNotFound)
	assertCode(t, s.UpdateAgent(ctx, a), model.CodeNotFound)
	assertCode(t, s.DeleteAgent(ctx, a.ID), model.CodeNotFound)
	assertCode(t, s.CreateAgent(ctx, b), model.CodeConflict)
	assertCode(t, s.CreateAgent(ctx, &model.Agent{}), model.CodeInvalid)
}

// ── providers ───────────────────────────────────────────────────────────────

func TestProviderCRUD(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := ts()
	p := &model.Provider{
		ID: model.NewID(), Name: "OpenAI", Kind: model.ProviderOpenAICompatible, BaseURL: "https://api/v1",
		SecretRef: "provider/openai", DefaultModel: "gpt", Models: []string{"a", "b"}, Enabled: true,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.CreateProvider(ctx, p); err != nil {
		t.Fatalf("CreateProvider 应成功: %v", err)
	}
	got, err := s.GetProvider(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertSameJSON(t, "provider", p, got)

	p.Models = nil
	p.Enabled = false
	p.UpdatedAt = ts()
	if err := s.UpdateProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetProvider(ctx, p.ID)
	if got.Models != nil || got.Enabled {
		t.Errorf("更新后 Models 应为 nil、Enabled 为 false，实际 %#v %v", got.Models, got.Enabled)
	}

	list, err := s.ListProviders(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListProviders 应返回 1 条: %d err=%v", len(list), err)
	}
	if err := s.DeleteProvider(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	_, err = s.GetProvider(ctx, p.ID)
	assertCode(t, err, model.CodeNotFound)
	assertCode(t, s.DeleteProvider(ctx, p.ID), model.CodeNotFound)
}

// ── sessions ────────────────────────────────────────────────────────────────

func TestSessionCRUDAndActiveKeyUniqueness(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	agent := mustCreateAgent(t, s)

	sess := mustCreateSession(t, s, agent.ID, "a:desktop:me")
	got, err := s.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertSameJSON(t, "session", sess, got)

	byKey, err := s.GetActiveSessionByKey(ctx, sess.Key)
	if err != nil || byKey.ID != sess.ID {
		t.Fatalf("GetActiveSessionByKey 应找到 active 会话: %v", err)
	}

	// 同一 Key 已有 active 会话 → Conflict。
	dup := newSession(agent.ID, sess.Key)
	assertCode(t, s.CreateSession(ctx, dup), model.CodeConflict)

	// 归档后同一 Key 可以再建；旧会话不再是 active。
	sess.Status = model.SessionArchived
	sess.UpdatedAt = ts()
	sess.LastMessageAt = ptr(ts())
	if err := s.UpdateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(ctx, dup); err != nil {
		t.Fatalf("归档后应允许同 Key 新会话: %v", err)
	}
	byKey, err = s.GetActiveSessionByKey(ctx, sess.Key)
	if err != nil || byKey.ID != dup.ID {
		t.Fatalf("GetActiveSessionByKey 应返回新的 active 会话: %v", err)
	}
	_, err = s.GetActiveSessionByKey(ctx, "nope")
	assertCode(t, err, model.CodeNotFound)

	// 不存在的智能体 → 外键失败 → Conflict。
	orphan := newSession("missing", "x")
	assertCode(t, s.CreateSession(ctx, orphan), model.CodeConflict)

	_, err = s.GetSession(ctx, "missing")
	assertCode(t, err, model.CodeNotFound)
	assertCode(t, s.UpdateSession(ctx, newSession(agent.ID, "zzz")), model.CodeNotFound)
	assertCode(t, s.DeleteSession(ctx, "missing"), model.CodeNotFound)
}

func TestListSessionsFilterAndPaging(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a1 := mustCreateAgent(t, s)
	a2 := mustCreateAgent(t, s)

	var ids []string
	for i := 0; i < 5; i++ {
		sess := newSession(a1.ID, "k"+string(rune('a'+i)))
		sess.CreatedAt = time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC)
		if i%2 == 1 {
			sess.Status = model.SessionArchived
			sess.Origin = model.OriginChannel
		}
		if err := s.CreateSession(ctx, sess); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, sess.ID)
	}
	mustCreateSession(t, s, a2.ID, "other")

	all, err := s.ListSessions(ctx, model.SessionFilter{}, model.Page{})
	if err != nil || len(all) != 6 {
		t.Fatalf("无过滤应返回 6 条: %d err=%v", len(all), err)
	}
	byAgent, _ := s.ListSessions(ctx, model.SessionFilter{AgentID: a1.ID}, model.Page{})
	if len(byAgent) != 5 {
		t.Errorf("按智能体过滤应返回 5 条，实际 %d", len(byAgent))
	}
	for i, sess := range byAgent {
		if sess.ID != ids[i] {
			t.Errorf("列表应按 CreatedAt 升序")
		}
	}
	active, _ := s.ListSessions(ctx, model.SessionFilter{AgentID: a1.ID, Status: model.SessionActive}, model.Page{})
	if len(active) != 3 {
		t.Errorf("active 过滤应返回 3 条，实际 %d", len(active))
	}
	channel, _ := s.ListSessions(ctx, model.SessionFilter{Origin: model.OriginChannel}, model.Page{})
	if len(channel) != 2 {
		t.Errorf("origin 过滤应返回 2 条，实际 %d", len(channel))
	}
	page, _ := s.ListSessions(ctx, model.SessionFilter{AgentID: a1.ID}, model.Page{Limit: 2, Offset: 3})
	if len(page) != 2 || page[0].ID != ids[3] || page[1].ID != ids[4] {
		t.Errorf("分页 Limit=2 Offset=3 应返回第 4、5 条")
	}
}

// ── messages ────────────────────────────────────────────────────────────────

func TestAppendMessageAllocatesSeqAndTouchesSession(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	agent := mustCreateAgent(t, s)
	sess := mustCreateSession(t, s, agent.ID, "k")
	if sess.LastMessageAt != nil {
		t.Fatal("新会话 LastMessageAt 应为空")
	}

	m1 := model.TextMessage(sess.ID, model.RoleUser, "你好")
	if err := s.AppendMessage(ctx, &m1); err != nil {
		t.Fatalf("AppendMessage 应成功: %v", err)
	}
	if m1.Seq != 1 || m1.ID == "" || m1.CreatedAt.IsZero() {
		t.Errorf("第一条消息应得到 Seq=1 并填充 ID 与时间: %+v", m1)
	}

	m2 := &model.Message{
		ID: model.NewID(), SessionID: sess.ID, Role: model.RoleAssistant, RunID: "run1",
		Parts: []model.Part{
			{Type: model.PartText, Text: "好的"},
			{Type: model.PartToolCall, ToolCall: &model.ToolCall{ID: "c", Name: "fs.read", Arguments: json.RawMessage(`{"path":"a"}`)}},
		},
		Meta:      json.RawMessage(`{"platform_msg_id":"42"}`),
		CreatedAt: ts(),
	}
	if err := s.AppendMessage(ctx, m2); err != nil {
		t.Fatal(err)
	}
	if m2.Seq != 2 {
		t.Errorf("第二条消息 Seq 应为 2，实际 %d", m2.Seq)
	}
	got, err := s.GetMessage(ctx, m2.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertSameJSON(t, "message", m2, got)

	updated, _ := s.GetSession(ctx, sess.ID)
	if updated.LastMessageAt == nil || !updated.LastMessageAt.Equal(m2.CreatedAt) {
		t.Errorf("LastMessageAt 应更新为最后一条消息的时间: %v", updated.LastMessageAt)
	}

	m3 := model.TextMessage(sess.ID, model.RoleUser, "再来")
	if err := s.AppendMessage(ctx, &m3); err != nil {
		t.Fatal(err)
	}
	after1, err := s.ListMessages(ctx, sess.ID, 1, 0)
	if err != nil || len(after1) != 2 || after1[0].Seq != 2 || after1[1].Seq != 3 {
		t.Errorf("ListMessages afterSeq=1 应返回 Seq 2、3: %+v err=%v", after1, err)
	}
	limited, _ := s.ListMessages(ctx, sess.ID, 0, 1)
	if len(limited) != 1 || limited[0].Seq != 1 {
		t.Errorf("ListMessages limit=1 应只返回第一条")
	}

	// 错误映射。
	bad := model.TextMessage("missing", model.RoleUser, "x")
	assertCode(t, s.AppendMessage(ctx, &bad), model.CodeNotFound)
	invalid := model.TextMessage(sess.ID, model.RoleUser, "x")
	invalid.Meta = json.RawMessage(`{not json`)
	assertCode(t, s.AppendMessage(ctx, &invalid), model.CodeInvalid)
	noSession := model.TextMessage("", model.RoleUser, "x")
	assertCode(t, s.AppendMessage(ctx, &noSession), model.CodeInvalid)
	_, err = s.GetMessage(ctx, "missing")
	assertCode(t, err, model.CodeNotFound)
}

func TestSeqIsPerSession(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	agent := mustCreateAgent(t, s)
	s1 := mustCreateSession(t, s, agent.ID, "k1")
	s2 := mustCreateSession(t, s, agent.ID, "k2")
	for i := 0; i < 3; i++ {
		m := model.TextMessage(s1.ID, model.RoleUser, "a")
		if err := s.AppendMessage(ctx, &m); err != nil {
			t.Fatal(err)
		}
	}
	m := model.TextMessage(s2.ID, model.RoleUser, "b")
	if err := s.AppendMessage(ctx, &m); err != nil {
		t.Fatal(err)
	}
	if m.Seq != 1 {
		t.Errorf("Seq 应按会话独立计数，实际 %d", m.Seq)
	}
}

// ── runs ────────────────────────────────────────────────────────────────────

func TestRunCRUDAndActiveListing(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	agent := mustCreateAgent(t, s)
	sess := mustCreateSession(t, s, agent.ID, "k")

	r := mustCreateRun(t, s, sess)
	got, err := s.GetRun(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertSameJSON(t, "run", r, got)

	r.Status = model.RunRunning
	r.StartedAt = ptr(ts())
	r.InputMessageID = "m1"
	if err := s.UpdateRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	r2 := newRun(sess)
	r2.CreatedAt = r.CreatedAt.Add(time.Second)
	if err := s.CreateRun(ctx, r2); err != nil {
		t.Fatal(err)
	}
	r2.Status = model.RunCompleted
	r2.OutputMessageID = "m2"
	r2.Usage = model.Usage{InputTokens: 10, OutputTokens: 20, CacheReadTokens: 5}
	r2.EndedAt = ptr(ts())
	if err := s.UpdateRun(ctx, r2); err != nil {
		t.Fatal(err)
	}
	got2, _ := s.GetRun(ctx, r2.ID)
	assertSameJSON(t, "updated run", r2, got2)

	active, err := s.ListActiveRuns(ctx)
	if err != nil || len(active) != 1 || active[0].ID != r.ID {
		t.Errorf("ListActiveRuns 应只返回非终态运行: %+v err=%v", active, err)
	}
	all, err := s.ListRuns(ctx, sess.ID, model.Page{})
	if err != nil || len(all) != 2 || all[0].ID != r.ID {
		t.Errorf("ListRuns 应按创建时间返回 2 条: %+v err=%v", all, err)
	}
	page, err := s.ListRuns(ctx, sess.ID, model.Page{Limit: 1, Offset: 1})
	if err != nil || len(page) != 1 || page[0].ID != r2.ID {
		t.Errorf("ListRuns 分页应返回第二条: %+v err=%v", page, err)
	}

	_, err = s.GetRun(ctx, "missing")
	assertCode(t, err, model.CodeNotFound)
	assertCode(t, s.UpdateRun(ctx, &model.Run{ID: "missing"}), model.CodeNotFound)
	orphan := newRun(&model.Session{ID: "missing", AgentID: agent.ID})
	assertCode(t, s.CreateRun(ctx, orphan), model.CodeConflict)
	assertCode(t, s.CreateRun(ctx, r), model.CodeConflict)
}

func Test运行时间相同时按ID稳定分页(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	agent := mustCreateAgent(t, s)
	sess := mustCreateSession(t, s, agent.ID, "same-time")
	createdAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// 倒序插入，验证分页不偶然依赖插入顺序或随机 ULID。
	for _, id := range []string{"run-c", "run-b", "run-a"} {
		r := newRun(sess)
		r.ID, r.CreatedAt = id, createdAt
		if err := s.CreateRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	for i, want := range []string{"run-a", "run-b", "run-c"} {
		page, err := s.ListRuns(ctx, sess.ID, model.Page{Limit: 1, Offset: i})
		if err != nil || len(page) != 1 || page[0].ID != want {
			t.Fatalf("同时间分页第 %d 页应返回 %s: %+v err=%v", i, want, page, err)
		}
	}
	active, err := s.ListActiveRuns(ctx)
	if err != nil || len(active) != 3 {
		t.Fatalf("应返回三条活跃运行: %+v err=%v", active, err)
	}
	for i, want := range []string{"run-a", "run-b", "run-c"} {
		if active[i].ID != want {
			t.Errorf("同时间活跃运行第 %d 条应为 %s，实际 %s", i, want, active[i].ID)
		}
	}
}

// ── approvals ───────────────────────────────────────────────────────────────

func TestApprovalCRUD(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	agent := mustCreateAgent(t, s)
	sess := mustCreateSession(t, s, agent.ID, "k")
	run := mustCreateRun(t, s, sess)

	ap := &model.ApprovalRequest{
		ID: model.NewID(), RunID: run.ID, SessionID: sess.ID, AgentID: agent.ID,
		ToolCall: model.ToolCall{ID: "c1", Name: "shell", Arguments: json.RawMessage(`{"cmd":"rm"}`)},
		Risk:     model.RiskDestructive, Summary: "删除文件", Details: json.RawMessage(`{"path":"/x"}`),
		Status: model.ApprovalPending, CreatedAt: ts(), ExpiresAt: ts().Add(time.Minute),
	}
	if err := s.CreateApproval(ctx, ap); err != nil {
		t.Fatalf("CreateApproval 应成功: %v", err)
	}
	got, err := s.GetApproval(ctx, ap.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertSameJSON(t, "approval", ap, got)

	ap.Status = model.ApprovalApproved
	ap.Scope = model.ScopeSession
	ap.DecidedBy = "user"
	ap.ResolvedAt = ptr(ts())
	if err := s.UpdateApproval(ctx, ap); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetApproval(ctx, ap.ID)
	assertSameJSON(t, "updated approval", ap, got)

	pending := &model.ApprovalRequest{
		ID: model.NewID(), RunID: run.ID, SessionID: sess.ID, AgentID: agent.ID,
		ToolCall: model.ToolCall{ID: "c2", Name: "net", Arguments: json.RawMessage(`{}`)},
		Risk:     model.RiskNetwork, Status: model.ApprovalPending, CreatedAt: ts(), ExpiresAt: ts(),
	}
	if err := s.CreateApproval(ctx, pending); err != nil {
		t.Fatal(err)
	}
	list, _ := s.ListApprovals(ctx, model.ApprovalPending, model.Page{})
	if len(list) != 1 || list[0].ID != pending.ID {
		t.Errorf("按状态过滤应只返回 pending")
	}
	all, _ := s.ListApprovals(ctx, "", model.Page{})
	if len(all) != 2 {
		t.Errorf("空状态应返回全部 2 条，实际 %d", len(all))
	}

	_, err = s.GetApproval(ctx, "missing")
	assertCode(t, err, model.CodeNotFound)
	assertCode(t, s.UpdateApproval(ctx, &model.ApprovalRequest{ID: "missing"}), model.CodeNotFound)
	orphan := *pending
	orphan.ID = model.NewID()
	orphan.RunID = "missing"
	assertCode(t, s.CreateApproval(ctx, &orphan), model.CodeConflict)
	badArgs := *pending
	badArgs.ID = model.NewID()
	badArgs.ToolCall.Arguments = json.RawMessage(`{bad`)
	assertCode(t, s.CreateApproval(ctx, &badArgs), model.CodeInvalid)
}

// ── jobs ────────────────────────────────────────────────────────────────────

func TestJobCRUD(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a1 := mustCreateAgent(t, s)
	a2 := mustCreateAgent(t, s)

	now := ts()
	j := &model.Job{
		ID: model.NewID(), AgentID: a1.ID, Name: "晨报", Schedule: "0 8 * * *", Timezone: "Asia/Shanghai",
		Kind: model.JobRunAgent, Input: "总结新闻", SessionKey: a1.ID + ":job:x",
		MissedPolicy: model.MissedRunOnce, Enabled: true, NextRunAt: ptr(now.Add(time.Hour)),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.CreateJob(ctx, j); err != nil {
		t.Fatalf("CreateJob 应成功: %v", err)
	}
	got, err := s.GetJob(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertSameJSON(t, "job", j, got)

	j.LastRunAt = ptr(ts())
	j.LastStatus = model.JobRunFailed
	j.LastError = "超时"
	j.Enabled = false
	j.NextRunAt = nil
	j.UpdatedAt = ts()
	if err := s.UpdateJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetJob(ctx, j.ID)
	assertSameJSON(t, "updated job", j, got)

	j2 := *j
	j2.ID = model.NewID()
	j2.AgentID = a2.ID
	if err := s.CreateJob(ctx, &j2); err != nil {
		t.Fatal(err)
	}
	mine, _ := s.ListJobs(ctx, a1.ID)
	if len(mine) != 1 || mine[0].ID != j.ID {
		t.Errorf("ListJobs 按智能体过滤应返回 1 条")
	}
	all, _ := s.ListJobs(ctx, "")
	if len(all) != 2 {
		t.Errorf("ListJobs 空 agentID 应返回全部，实际 %d", len(all))
	}

	if err := s.DeleteJob(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	_, err = s.GetJob(ctx, j.ID)
	assertCode(t, err, model.CodeNotFound)
	assertCode(t, s.UpdateJob(ctx, j), model.CodeNotFound)
	assertCode(t, s.DeleteJob(ctx, j.ID), model.CodeNotFound)
	orphan := j2
	orphan.ID = model.NewID()
	orphan.AgentID = "missing"
	assertCode(t, s.CreateJob(ctx, &orphan), model.CodeConflict)
}

// ── extensions ──────────────────────────────────────────────────────────────

func TestExtensionCRUD(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := ts()
	mcp := &model.Extension{
		ID: model.NewID(), Kind: model.ExtensionMCPServer, Name: "fs", Version: "1.0.0", Source: "npm:x",
		InstallDir: "/data/extensions/fs", Manifest: json.RawMessage(`{"name":"fs"}`),
		MCP: &model.MCPServerConfig{Transport: model.MCPStdio, Command: "npx", Args: []string{"x"},
			Env: map[string]string{"TOKEN": "secret:mcp/fs"}},
		Enabled: true, Digest: "sha256:abc", InstalledAt: now, UpdatedAt: now,
	}
	if err := s.CreateExtension(ctx, mcp); err != nil {
		t.Fatalf("CreateExtension 应成功: %v", err)
	}
	skill := &model.Extension{
		ID: model.NewID(), Kind: model.ExtensionSkill, Name: "writer", Source: "/local/writer",
		InstallDir: "/data/extensions/writer", Enabled: true, InstalledAt: now.Add(time.Second), UpdatedAt: now,
	}
	if err := s.CreateExtension(ctx, skill); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetExtension(ctx, mcp.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertSameJSON(t, "extension", mcp, got)
	gotSkill, _ := s.GetExtension(ctx, skill.ID)
	if gotSkill.MCP != nil || gotSkill.Manifest != nil {
		t.Errorf("技能扩展的 MCP 与 Manifest 应为 nil")
	}

	only, _ := s.ListExtensions(ctx, model.ExtensionSkill)
	if len(only) != 1 || only[0].ID != skill.ID {
		t.Errorf("ListExtensions 按类型过滤应返回技能")
	}
	all, _ := s.ListExtensions(ctx, "")
	if len(all) != 2 || all[0].ID != mcp.ID {
		t.Errorf("ListExtensions 空类型应按安装时间返回全部")
	}

	mcp.Enabled = false
	mcp.MCP = nil
	mcp.Version = "1.1.0"
	mcp.UpdatedAt = ts()
	if err := s.UpdateExtension(ctx, mcp); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetExtension(ctx, mcp.ID)
	assertSameJSON(t, "updated extension", mcp, got)

	if err := s.DeleteExtension(ctx, mcp.ID); err != nil {
		t.Fatal(err)
	}
	_, err = s.GetExtension(ctx, mcp.ID)
	assertCode(t, err, model.CodeNotFound)
	assertCode(t, s.UpdateExtension(ctx, mcp), model.CodeNotFound)
	assertCode(t, s.DeleteExtension(ctx, mcp.ID), model.CodeNotFound)
	assertCode(t, s.CreateExtension(ctx, skill), model.CodeConflict)
	bad := *skill
	bad.ID = model.NewID()
	bad.Manifest = json.RawMessage(`nope`)
	assertCode(t, s.CreateExtension(ctx, &bad), model.CodeInvalid)
}

// ── channels ────────────────────────────────────────────────────────────────

func TestChannelCRUD(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a1 := mustCreateAgent(t, s)
	a2 := mustCreateAgent(t, s)
	now := ts()
	c := &model.Channel{
		ID: model.NewID(), AgentID: a1.ID, Kind: model.ChannelFeishu, Name: "飞书", SecretRef: "feishu/app",
		Config: json.RawMessage(`{"allow":["u1"]}`), Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.CreateChannel(ctx, c); err != nil {
		t.Fatalf("CreateChannel 应成功: %v", err)
	}
	got, err := s.GetChannel(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertSameJSON(t, "channel", c, got)

	c.LastError = "连接失败"
	c.Enabled = false
	c.Config = nil
	c.UpdatedAt = ts()
	if err := s.UpdateChannel(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetChannel(ctx, c.ID)
	assertSameJSON(t, "updated channel", c, got)

	c2 := *c
	c2.ID = model.NewID()
	c2.AgentID = a2.ID
	if err := s.CreateChannel(ctx, &c2); err != nil {
		t.Fatal(err)
	}
	mine, _ := s.ListChannels(ctx, a1.ID)
	if len(mine) != 1 || mine[0].ID != c.ID {
		t.Errorf("ListChannels 按智能体过滤应返回 1 条")
	}
	all, _ := s.ListChannels(ctx, "")
	if len(all) != 2 {
		t.Errorf("ListChannels 空 agentID 应返回全部，实际 %d", len(all))
	}

	if err := s.DeleteChannel(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	_, err = s.GetChannel(ctx, c.ID)
	assertCode(t, err, model.CodeNotFound)
	assertCode(t, s.UpdateChannel(ctx, c), model.CodeNotFound)
	assertCode(t, s.DeleteChannel(ctx, c.ID), model.CodeNotFound)
	orphan := c2
	orphan.ID = model.NewID()
	orphan.AgentID = "missing"
	assertCode(t, s.CreateChannel(ctx, &orphan), model.CodeConflict)
}

// ── audit ───────────────────────────────────────────────────────────────────

func TestAuditAppendAndQuery(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	var entries []*model.AuditEntry
	for i := 0; i < 5; i++ {
		e := &model.AuditEntry{
			ID: model.NewID(), At: base.Add(time.Duration(i) * time.Second), Actor: "user",
			AgentID: "a1", SessionID: "s1", RunID: "r1", Action: "tool.call", Resource: "fs.read",
			Outcome: "allowed", Details: json.RawMessage(`{"path":"x"}`),
		}
		if i == 2 {
			e.AgentID = "a2"
			e.Action = "approval.decide"
			e.Details = nil
		}
		if err := s.AppendAudit(ctx, e); err != nil {
			t.Fatalf("AppendAudit 应成功: %v", err)
		}
		entries = append(entries, e)
	}

	all, err := s.ListAudit(ctx, model.AuditFilter{}, model.Page{})
	if err != nil || len(all) != 5 {
		t.Fatalf("ListAudit 应返回 5 条: %d err=%v", len(all), err)
	}
	for i := range all {
		assertSameJSON(t, "audit", entries[4-i], all[i])
	}
	byAgent, _ := s.ListAudit(ctx, model.AuditFilter{AgentID: "a2"}, model.Page{})
	if len(byAgent) != 1 || byAgent[0].ID != entries[2].ID {
		t.Errorf("按 AgentID 过滤应返回第三条")
	}
	byAction, _ := s.ListAudit(ctx, model.AuditFilter{Action: "tool.call", SessionID: "s1"}, model.Page{})
	if len(byAction) != 4 {
		t.Errorf("按 Action+SessionID 过滤应返回 4 条，实际 %d", len(byAction))
	}
	since, _ := s.ListAudit(ctx, model.AuditFilter{Since: base.Add(3 * time.Second)}, model.Page{})
	if len(since) != 2 || since[0].ID != entries[4].ID {
		t.Errorf("Since 过滤应返回最后两条且降序")
	}
	page, _ := s.ListAudit(ctx, model.AuditFilter{}, model.Page{Limit: 2, Offset: 1})
	if len(page) != 2 || page[0].ID != entries[3].ID || page[1].ID != entries[2].ID {
		t.Errorf("分页应跳过最新一条后取两条")
	}

	assertCode(t, s.AppendAudit(ctx, &model.AuditEntry{At: base, Actor: "x", Action: "y", Outcome: "ok"}), model.CodeInvalid)
	assertCode(t, s.AppendAudit(ctx, &model.AuditEntry{ID: model.NewID(), Actor: "x", Action: "y", Outcome: "ok"}), model.CodeInvalid)
	assertCode(t, s.AppendAudit(ctx, &model.AuditEntry{
		ID: model.NewID(), At: base, Actor: "x", Action: "y", Outcome: "ok", Details: json.RawMessage(`{`),
	}), model.CodeInvalid)
	assertCode(t, s.AppendAudit(ctx, entries[0]), model.CodeConflict)
}

// ── settings ────────────────────────────────────────────────────────────────

func TestSettingsGetSet(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	_, err := s.GetSetting(ctx, "timezone")
	assertCode(t, err, model.CodeNotFound)

	if err := s.SetSetting(ctx, "timezone", `"Asia/Shanghai"`); err != nil {
		t.Fatalf("SetSetting 应成功: %v", err)
	}
	if v, err := s.GetSetting(ctx, "timezone"); err != nil || v != `"Asia/Shanghai"` {
		t.Errorf("GetSetting 应返回写入值: %q err=%v", v, err)
	}
	if err := s.SetSetting(ctx, "timezone", `"UTC"`); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.GetSetting(ctx, "timezone"); v != `"UTC"` {
		t.Errorf("SetSetting 应覆盖旧值，实际 %q", v)
	}
	_, err = s.GetSetting(ctx, "")
	assertCode(t, err, model.CodeInvalid)
	assertCode(t, s.SetSetting(ctx, "", "x"), model.CodeInvalid)
}
