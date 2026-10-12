package agent

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/event"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

// fakeRepo 是内存版 model.AgentRepo。
type fakeRepo struct {
	mu         sync.Mutex
	items      map[string]*model.Agent
	failCreate bool
}

func newFakeRepo() *fakeRepo { return &fakeRepo{items: map[string]*model.Agent{}} }

func cloneAgent(a *model.Agent) *model.Agent {
	c := *a
	c.Tools = append([]string(nil), a.Tools...)
	c.Extensions = append([]string(nil), a.Extensions...)
	return &c
}

func (r *fakeRepo) CreateAgent(_ context.Context, a *model.Agent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failCreate {
		return model.NewError(model.CodeInternal, "模拟写入失败")
	}
	r.items[a.ID] = cloneAgent(a)
	return nil
}

func (r *fakeRepo) GetAgent(_ context.Context, id string) (*model.Agent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.items[id]
	if !ok {
		return nil, model.NotFound("agent", id)
	}
	return cloneAgent(a), nil
}

func (r *fakeRepo) ListAgents(_ context.Context) ([]*model.Agent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*model.Agent, 0, len(r.items))
	for _, a := range r.items {
		out = append(out, cloneAgent(a))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (r *fakeRepo) UpdateAgent(_ context.Context, a *model.Agent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.items[a.ID]; !ok {
		return model.NotFound("agent", a.ID)
	}
	r.items[a.ID] = cloneAgent(a)
	return nil
}

func (r *fakeRepo) DeleteAgent(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.items[id]; !ok {
		return model.NotFound("agent", id)
	}
	delete(r.items, id)
	return nil
}

type fakeProviders struct{ ids map[string]bool }

func (f *fakeProviders) ProviderExists(_ context.Context, id string) (bool, error) {
	return f.ids[id], nil
}

// fakeWorkspace 是内存文件系统，记录写入内容。
type fakeWorkspace struct {
	mu    sync.Mutex
	root  string
	files map[string][]byte
}

func (w *fakeWorkspace) Root() string { return w.root }

func (w *fakeWorkspace) Exists(_ context.Context, rel string) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.files[rel]
	return ok, nil
}

func (w *fakeWorkspace) ReadFile(_ context.Context, rel string, _ int64) ([]byte, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	data, ok := w.files[rel]
	if !ok {
		return nil, model.NotFound("file", rel)
	}
	return append([]byte(nil), data...), nil
}

func (w *fakeWorkspace) WriteFile(_ context.Context, rel string, data []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.files[rel] = append([]byte(nil), data...)
	return nil
}

// fakeOpener 按 workspaceDir 复用同一个内存工作区，并记录 Open 的参数。
type fakeOpener struct {
	mu     sync.Mutex
	spaces map[string]*fakeWorkspace
	opened []string
	fail   bool
}

func newFakeOpener() *fakeOpener { return &fakeOpener{spaces: map[string]*fakeWorkspace{}} }

func (o *fakeOpener) Open(_ context.Context, agentID, dir string) (Workspace, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.fail {
		return nil, model.NewError(model.CodeInternal, "模拟打开失败")
	}
	o.opened = append(o.opened, agentID+"|"+dir)
	ws, ok := o.spaces[dir]
	if !ok {
		ws = &fakeWorkspace{root: "/data/agents/" + dir, files: map[string][]byte{}}
		o.spaces[dir] = ws
	}
	return ws, nil
}

type fixture struct {
	repo      *fakeRepo
	providers *fakeProviders
	opener    *fakeOpener
	bus       *event.Bus
	svc       *Service
}

func newFixture(t *testing.T, opts ...Option) *fixture {
	t.Helper()
	f := &fixture{
		repo:      newFakeRepo(),
		providers: &fakeProviders{ids: map[string]bool{"P1": true}},
		opener:    newFakeOpener(),
		bus:       event.NewBus(64),
	}
	svc, err := New(f.repo, f.providers, f.opener, f.bus, nil, opts...)
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	f.svc = svc
	return f
}

var validModel = model.ModelRef{ProviderID: "P1", Model: "m"}

func ctxBg() context.Context  { return context.Background() }
func strPtr(s string) *string { return &s }
func boolPtr(b bool) *bool    { return &b }

func mustCreate(t *testing.T, f *fixture, in CreateInput) *model.Agent {
	t.Helper()
	a, err := f.svc.Create(ctxBg(), in)
	if err != nil {
		t.Fatalf("Create 失败: %v", err)
	}
	return a
}

func TestCreateAppliesDefaultsAndSeedsSoul(t *testing.T) {
	f := newFixture(t)
	sub := f.bus.Subscribe(ctxBg(), 0, 8, nil)
	defer sub.Cancel()

	a := mustCreate(t, f, CreateInput{Name: "  小助手 ", Model: validModel})
	if a.Name != "小助手" || !model.IsID(a.ID) || !a.Enabled {
		t.Fatalf("基本字段不符: %+v", a)
	}
	if strings.Join(a.Tools, ",") != strings.Join(DefaultTools(), ",") {
		t.Errorf("Tools 应为默认工具集: %v", a.Tools)
	}
	if a.Autonomy != model.DefaultAutonomy {
		t.Errorf("Autonomy = %s，期望默认值", a.Autonomy)
	}
	if a.WorkspaceDir != a.ID {
		t.Errorf("WorkspaceDir = %q，期望智能体 id", a.WorkspaceDir)
	}
	if a.SystemPrompt != "" {
		t.Errorf("未指定提示时 SystemPrompt 应为空（由工作进程使用内置默认），实际 %q", a.SystemPrompt)
	}
	if a.CreatedAt.IsZero() || a.CreatedAt.Location() != time.UTC {
		t.Errorf("时间戳应为 UTC: %v", a.CreatedAt)
	}
	ws := f.opener.spaces[a.ID]
	if ws == nil {
		t.Fatalf("应打开工作区 %s，实际打开了 %v", a.ID, f.opener.opened)
	}
	soul := string(ws.files[SoulFile])
	if !strings.Contains(soul, "小助手") || !strings.HasSuffix(soul, "\n") {
		t.Errorf("SOUL.md 应包含内置默认人格: %q", soul)
	}
	ev := <-sub.C
	if ev.Type != EventCreated || ev.AgentID != a.ID {
		t.Errorf("事件 = %+v", ev)
	}
	if got, _ := f.repo.GetAgent(ctxBg(), a.ID); got == nil || got.Name != "小助手" {
		t.Error("记录应已持久化")
	}
}

func TestCreateFromTemplate(t *testing.T) {
	f := newFixture(t)
	a := mustCreate(t, f, CreateInput{Name: "运维", Model: validModel, Template: "ops-engineer"})
	tpl, _ := f.svc.Templates().Get("ops-engineer")
	if a.Template != "ops-engineer" || a.SystemPrompt != tpl.SystemPrompt || a.Autonomy != tpl.Autonomy {
		t.Fatalf("模板字段未应用: %+v", a)
	}
	if strings.Join(a.Tools, ",") != strings.Join(tpl.Tools, ",") {
		t.Errorf("Tools = %v，期望模板工具 %v", a.Tools, tpl.Tools)
	}
	if got := strings.TrimSpace(string(f.opener.spaces[a.ID].files[SoulFile])); got != tpl.SystemPrompt {
		t.Errorf("SOUL.md 应写入模板提示")
	}

	// 显式字段优先于模板。
	b := mustCreate(t, f, CreateInput{
		Name: "自定义", Model: validModel, Template: "general-assistant",
		SystemPrompt: "我的提示", Tools: []string{"read_file"}, Autonomy: model.AutonomyAskAll,
	})
	if b.SystemPrompt != "我的提示" || len(b.Tools) != 1 || b.Autonomy != model.AutonomyAskAll {
		t.Errorf("显式字段应覆盖模板: %+v", b)
	}
	// 空切片表示不启用任何工具，与 nil 不同。
	c := mustCreate(t, f, CreateInput{Name: "无工具", Model: validModel, Template: "general-assistant", Tools: []string{}})
	if len(c.Tools) != 0 {
		t.Errorf("空切片应保持为空: %v", c.Tools)
	}
	_, err := f.svc.Create(ctxBg(), CreateInput{Name: "x", Model: validModel, Template: "missing"})
	if model.CodeOf(err) != model.CodeInvalid {
		t.Errorf("不存在的模板应 invalid: %v", err)
	}
}

func TestCreateAdoptsExistingSoul(t *testing.T) {
	f := newFixture(t)
	f.opener.spaces["/abs/existing"] = &fakeWorkspace{root: "/abs/existing", files: map[string][]byte{SoulFile: []byte("  已有人格\n")}}

	a := mustCreate(t, f, CreateInput{Name: "x", Model: validModel, WorkspaceDir: "/abs/existing", Template: "ops-engineer"})
	if a.SystemPrompt != "已有人格" {
		t.Errorf("未显式给出提示时应采用已有 SOUL.md: %q", a.SystemPrompt)
	}
	if string(f.opener.spaces["/abs/existing"].files[SoulFile]) != "  已有人格\n" {
		t.Error("已有的 SOUL.md 不应被覆盖")
	}
	b := mustCreate(t, f, CreateInput{Name: "y", Model: validModel, WorkspaceDir: "/abs/existing", SystemPrompt: "新提示"})
	if b.SystemPrompt != "新提示" || string(f.opener.spaces["/abs/existing"].files[SoulFile]) != "新提示\n" {
		t.Error("显式提示应覆盖已有 SOUL.md")
	}
}

func TestCreateValidation(t *testing.T) {
	f := newFixture(t)
	cases := []struct {
		name string
		in   CreateInput
		code model.Code
	}{
		{"空名称", CreateInput{Name: " ", Model: validModel}, model.CodeInvalid},
		{"超长名称", CreateInput{Name: strings.Repeat("名", 65), Model: validModel}, model.CodeInvalid},
		{"控制字符", CreateInput{Name: "a\x01b", Model: validModel}, model.CodeInvalid},
		{"缺模型", CreateInput{Name: "a", Model: model.ModelRef{ProviderID: "P1"}}, model.CodeInvalid},
		{"缺供应商", CreateInput{Name: "a", Model: model.ModelRef{Model: "m"}}, model.CodeInvalid},
		{"供应商不存在", CreateInput{Name: "a", Model: model.ModelRef{ProviderID: "nope", Model: "m"}}, model.CodeInvalid},
		{"非法自主等级", CreateInput{Name: "a", Model: validModel, Autonomy: "yolo"}, model.CodeInvalid},
		{"空工具名", CreateInput{Name: "a", Model: validModel, Tools: []string{" "}}, model.CodeInvalid},
		{"带空格的工具名", CreateInput{Name: "a", Model: validModel, Tools: []string{"read file"}}, model.CodeInvalid},
		{"空扩展标识", CreateInput{Name: "a", Model: validModel, Extensions: []string{""}}, model.CodeInvalid},
	}
	for _, c := range cases {
		_, err := f.svc.Create(ctxBg(), c.in)
		if model.CodeOf(err) != c.code {
			t.Errorf("%s: 错误码 = %v，期望 %s", c.name, err, c.code)
		}
	}
	if len(f.repo.items) != 0 || len(f.opener.opened) != 0 {
		t.Error("校验失败不应创建记录或工作区")
	}
	// 名称恰好 64 字符可以通过；工具去重。
	a := mustCreate(t, f, CreateInput{Name: strings.Repeat("名", 64), Model: validModel, Tools: []string{"a", " a ", "b"}, Extensions: []string{"e", "e"}})
	if strings.Join(a.Tools, ",") != "a,b" || strings.Join(a.Extensions, ",") != "e" {
		t.Errorf("去重失败: %v %v", a.Tools, a.Extensions)
	}
}

func TestCreateFailuresDoNotPersist(t *testing.T) {
	f := newFixture(t)
	f.opener.fail = true
	_, err := f.svc.Create(ctxBg(), CreateInput{Name: "a", Model: validModel})
	if err == nil || len(f.repo.items) != 0 {
		t.Fatalf("工作区打开失败不应创建记录: %v", err)
	}
	f.opener.fail = false
	f.repo.failCreate = true
	_, err = f.svc.Create(ctxBg(), CreateInput{Name: "a", Model: validModel})
	if model.CodeOf(err) != model.CodeInternal {
		t.Fatalf("应透传仓储错误码: %v", err)
	}
	ctx, cancel := context.WithCancel(ctxBg())
	cancel()
	if _, err := f.svc.Create(ctx, CreateInput{Name: "a", Model: validModel}); model.CodeOf(err) != model.CodeCancelled {
		t.Errorf("已取消的 ctx 应返回 cancelled: %v", err)
	}
}

func TestUpdatePartial(t *testing.T) {
	f := newFixture(t)
	f.providers.ids["P2"] = true
	a := mustCreate(t, f, CreateInput{Name: "a", Model: validModel, Description: "d"})
	sub := f.bus.Subscribe(ctxBg(), 0, 8, func(e event.Event) bool { return e.Type == EventUpdated })
	defer sub.Cancel()

	// 只改名称：其他字段不动。
	u, err := f.svc.Update(ctxBg(), a.ID, UpdateInput{Name: strPtr("b")})
	if err != nil || u.Name != "b" || u.Description != "d" || u.Model != validModel || len(u.Tools) != len(DefaultTools()) {
		t.Fatalf("部分更新破坏了其他字段: %+v, %v", u, err)
	}
	if !u.UpdatedAt.After(a.UpdatedAt) && !u.UpdatedAt.Equal(a.UpdatedAt) {
		t.Error("UpdatedAt 应被刷新")
	}
	ev := <-sub.C
	if got, _ := ev.Payload.(*model.Agent); got == nil || got.Name != "b" {
		t.Errorf("updated 事件 Payload = %#v", ev.Payload)
	}

	// 改模型、工具、扩展、自主等级、模板、启用状态。
	newModel := model.ModelRef{ProviderID: "P2", Model: "m2"}
	tools := []string{"read_file"}
	exts := []string{"ext1"}
	auto := model.AutonomyAutoInSandbox
	u, err = f.svc.Update(ctxBg(), a.ID, UpdateInput{Model: &newModel, Tools: &tools, Extensions: &exts, Autonomy: &auto, Template: strPtr("ops-engineer"), Enabled: boolPtr(false)})
	if err != nil || u.Model != newModel || len(u.Tools) != 1 || len(u.Extensions) != 1 || u.Autonomy != auto || u.Template != "ops-engineer" || u.Enabled {
		t.Fatalf("更新失败: %+v, %v", u, err)
	}
	// Tools 指向 nil 切片 → 重置为默认；Extensions 指向 nil → 清空。
	var nilTools, nilExts []string
	u, err = f.svc.Update(ctxBg(), a.ID, UpdateInput{Tools: &nilTools, Extensions: &nilExts, Template: strPtr("")})
	if err != nil || len(u.Tools) != len(DefaultTools()) || u.Extensions != nil || u.Template != "" {
		t.Fatalf("重置失败: %+v, %v", u, err)
	}

	// 校验错误。
	bad := model.ModelRef{ProviderID: "nope", Model: "m"}
	if _, err := f.svc.Update(ctxBg(), a.ID, UpdateInput{Model: &bad}); model.CodeOf(err) != model.CodeInvalid {
		t.Errorf("不存在的供应商应 invalid: %v", err)
	}
	if _, err := f.svc.Update(ctxBg(), a.ID, UpdateInput{Template: strPtr("missing")}); model.CodeOf(err) != model.CodeInvalid {
		t.Errorf("不存在的模板应 invalid: %v", err)
	}
	if _, err := f.svc.Update(ctxBg(), a.ID, UpdateInput{Name: strPtr("")}); model.CodeOf(err) != model.CodeInvalid {
		t.Errorf("空名称应 invalid: %v", err)
	}
	if _, err := f.svc.Update(ctxBg(), "missing", UpdateInput{}); model.CodeOf(err) != model.CodeNotFound {
		t.Errorf("不存在的智能体应 not_found: %v", err)
	}
	// 校验失败不应写入。
	got, _ := f.repo.GetAgent(ctxBg(), a.ID)
	if got.Model != newModel {
		t.Error("校验失败的更新不应落库")
	}
}

func TestUpdateSystemPromptSyncsSoul(t *testing.T) {
	f := newFixture(t)
	a := mustCreate(t, f, CreateInput{Name: "a", Model: validModel, SystemPrompt: "v1"})
	ws := f.opener.spaces[a.ID]
	if string(ws.files[SoulFile]) != "v1\n" {
		t.Fatalf("初始 SOUL.md = %q", ws.files[SoulFile])
	}
	if _, err := f.svc.Update(ctxBg(), a.ID, UpdateInput{SystemPrompt: strPtr(" v2 ")}); err != nil {
		t.Fatal(err)
	}
	if string(ws.files[SoulFile]) != "v2\n" {
		t.Errorf("SOUL.md 应同步为新提示: %q", ws.files[SoulFile])
	}
	// 清空提示时写回内置默认人格。
	if _, err := f.svc.Update(ctxBg(), a.ID, UpdateInput{SystemPrompt: strPtr("")}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ws.files[SoulFile]), "你是 a") {
		t.Errorf("清空后应写入默认人格: %q", ws.files[SoulFile])
	}
	// 未改动提示时不触碰工作区。
	before := len(f.opener.opened)
	if _, err := f.svc.Update(ctxBg(), a.ID, UpdateInput{Description: strPtr("x")}); err != nil {
		t.Fatal(err)
	}
	if len(f.opener.opened) != before {
		t.Error("未修改提示时不应打开工作区")
	}
	// 工作区不可用时更新仍然成功（数据库是事实来源）。
	f.opener.fail = true
	u, err := f.svc.Update(ctxBg(), a.ID, UpdateInput{SystemPrompt: strPtr("v3")})
	if err != nil || u.SystemPrompt != "v3" {
		t.Errorf("工作区失败不应阻止更新: %+v, %v", u, err)
	}
}

func TestGetListDelete(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f := newFixture(t, WithClock(func() time.Time { now = now.Add(time.Second); return now }))
	a := mustCreate(t, f, CreateInput{Name: "a", Model: validModel})
	b := mustCreate(t, f, CreateInput{Name: "b", Model: validModel})

	items, err := f.svc.List(ctxBg())
	if err != nil || len(items) != 2 || items[0].ID != a.ID || items[1].ID != b.ID {
		t.Fatalf("List = %v, %v", items, err)
	}
	got, err := f.svc.Get(ctxBg(), a.ID)
	if err != nil || got.Name != "a" {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if _, err := f.svc.Get(ctxBg(), "missing"); model.CodeOf(err) != model.CodeNotFound {
		t.Errorf("Get 不存在应 not_found: %v", err)
	}

	sub := f.bus.Subscribe(ctxBg(), 0, 8, func(e event.Event) bool { return e.Type == EventDeleted })
	defer sub.Cancel()
	if err := f.svc.Delete(ctxBg(), a.ID); err != nil {
		t.Fatalf("Delete 失败: %v", err)
	}
	ev := <-sub.C
	if ev.AgentID != a.ID {
		t.Errorf("deleted 事件 AgentID = %s", ev.AgentID)
	}
	if _, err := f.svc.Get(ctxBg(), a.ID); model.CodeOf(err) != model.CodeNotFound {
		t.Error("删除后应 not_found")
	}
	if err := f.svc.Delete(ctxBg(), a.ID); model.CodeOf(err) != model.CodeNotFound {
		t.Errorf("重复删除应 not_found: %v", err)
	}
	// 工作区文件保留。
	if _, ok := f.opener.spaces[a.ID].files[SoulFile]; !ok {
		t.Error("删除智能体不应删除工作区文件")
	}
}

func TestNewWithCustomTemplates(t *testing.T) {
	lib := NewLibrary(Template{ID: "custom", Name: "自定义", SystemPrompt: "p", Autonomy: model.AutonomyAskAll})
	f := newFixture(t, WithTemplates(lib))
	if f.svc.Templates().Len() != 1 {
		t.Fatalf("模板库应被替换: %d", f.svc.Templates().Len())
	}
	a := mustCreate(t, f, CreateInput{Name: "x", Model: validModel, Template: "custom"})
	if a.SystemPrompt != "p" || a.Autonomy != model.AutonomyAskAll || strings.Join(a.Tools, ",") != strings.Join(DefaultTools(), ",") {
		t.Errorf("模板无工具时应回退默认工具: %+v", a)
	}
}

// fakeCleaner 记录 DeleteByAgent 调用，并可注入错误。
type fakeCleaner struct {
	calls []string
	err   error
	// repo 用于断言调用发生时智能体记录仍然存在。
	repo       *fakeRepo
	agentExist []bool
}

func (c *fakeCleaner) DeleteByAgent(ctx context.Context, agentID string) error {
	c.calls = append(c.calls, agentID)
	_, err := c.repo.GetAgent(ctx, agentID)
	c.agentExist = append(c.agentExist, err == nil)
	return c.err
}

func TestDeleteCleansSessionsFirstAndStopsOnConflict(t *testing.T) {
	cleaner := &fakeCleaner{}
	f := newFixture(t, WithSessionCleaner(cleaner))
	cleaner.repo = f.repo
	a := mustCreate(t, f, CreateInput{Name: "a", Model: validModel})
	sub := f.bus.Subscribe(ctxBg(), f.bus.LastSeq(), 8, func(e event.Event) bool { return e.Type == EventDeleted })
	defer sub.Cancel()

	// 有在途运行：conflict 原样返回，智能体不被删除，也不发布 agent.deleted。
	cleaner.err = &model.Error{Code: model.CodeConflict, Message: "仍有在途运行", Details: map[string]any{"run_id": "R1"}}
	err := f.svc.Delete(ctxBg(), a.ID)
	var me *model.Error
	if !errors.As(err, &me) || me.Code != model.CodeConflict || me.Details["run_id"] != "R1" {
		t.Fatalf("会话清理冲突应原样返回，得到 %v", err)
	}
	if _, err := f.svc.Get(ctxBg(), a.ID); err != nil {
		t.Fatalf("冲突时不应删除智能体: %v", err)
	}
	select {
	case ev := <-sub.C:
		t.Fatalf("冲突时不应发布 agent.deleted，得到 %+v", ev)
	default:
	}

	// 其他错误被包装但保留错误码。
	cleaner.err = model.NewError(model.CodeInternal, "db down")
	if err := f.svc.Delete(ctxBg(), a.ID); model.CodeOf(err) != model.CodeInternal {
		t.Fatalf("会话清理失败应返回其错误码，得到 %v", err)
	}
	if _, err := f.svc.Get(ctxBg(), a.ID); err != nil {
		t.Fatalf("会话清理失败时不应删除智能体: %v", err)
	}

	cleaner.err = nil
	if err := f.svc.Delete(ctxBg(), a.ID); err != nil {
		t.Fatalf("Delete 失败: %v", err)
	}
	if len(cleaner.calls) != 3 || cleaner.calls[2] != a.ID || !cleaner.agentExist[2] {
		t.Fatalf("应在删除记录之前清理该智能体的会话，调用 %v，记录存在 %v", cleaner.calls, cleaner.agentExist)
	}
	if ev := <-sub.C; ev.AgentID != a.ID {
		t.Fatalf("应发布 agent.deleted，得到 %+v", ev)
	}

	// 不存在的智能体：not_found，不调用清理。
	if err := f.svc.Delete(ctxBg(), "missing"); model.CodeOf(err) != model.CodeNotFound || len(cleaner.calls) != 3 {
		t.Fatalf("不存在的智能体应 not_found 且不清理会话，得到 %v，调用 %v", err, cleaner.calls)
	}
}
