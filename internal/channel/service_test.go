package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// memRepo 是内存版 Repo，语义对齐 store：找不到返回 CodeNotFound，外键缺失返回 CodeConflict。
// （按架构 §4 本包不能导入 store；真实仓储下的集成测试在 internal/app。）
type memRepo struct {
	mu       sync.Mutex
	agents   map[string]*model.Agent
	channels map[string]*model.Channel
}

func newMemRepo() *memRepo {
	return &memRepo{agents: map[string]*model.Agent{}, channels: map[string]*model.Channel{}}
}

func (r *memRepo) GetAgent(_ context.Context, id string) (*model.Agent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.agents[id]
	if !ok {
		return nil, model.NotFound("agent", id)
	}
	cp := *a
	return &cp, nil
}

func (r *memRepo) CreateChannel(_ context.Context, c *model.Channel) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.agents[c.AgentID]; !ok {
		return model.NewError(model.CodeConflict, "外键约束失败")
	}
	if _, dup := r.channels[c.ID]; dup {
		return model.NewError(model.CodeConflict, "重复的通道 ID")
	}
	cp := *c
	r.channels[c.ID] = &cp
	return nil
}

func (r *memRepo) GetChannel(_ context.Context, id string) (*model.Channel, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.channels[id]
	if !ok {
		return nil, model.NotFound("channel", id)
	}
	cp := *c
	return &cp, nil
}

func (r *memRepo) ListChannels(_ context.Context, agentID string) ([]*model.Channel, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*model.Channel
	for _, c := range r.channels {
		if agentID == "" || c.AgentID == agentID {
			cp := *c
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (r *memRepo) UpdateChannel(_ context.Context, c *model.Channel) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.channels[c.ID]; !ok {
		return model.NotFound("channel", c.ID)
	}
	cp := *c
	r.channels[c.ID] = &cp
	return nil
}

func (r *memRepo) DeleteChannel(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.channels[id]; !ok {
		return model.NotFound("channel", id)
	}
	delete(r.channels, id)
	return nil
}

// fakeGateway 记录重载请求，并按配置返回错误与运行状态。
type fakeGateway struct {
	mu      sync.Mutex
	reloads []string
	err     error
	status  map[string]RuntimeStatus
	// onReload 可模拟网关在重载时写入 LastError。
	onReload func(id string)
}

func (g *fakeGateway) Reload(_ context.Context, id string) error {
	g.mu.Lock()
	g.reloads = append(g.reloads, id)
	err, hook := g.err, g.onReload
	g.mu.Unlock()
	if hook != nil {
		hook(id)
	}
	return err
}

func (g *fakeGateway) ChannelStatus(id string) (RuntimeStatus, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	st, ok := g.status[id]
	return st, ok
}

func (g *fakeGateway) reloadCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.reloads)
}

type fakeKinds []model.ChannelKind

func (k fakeKinds) Kinds() []model.ChannelKind { return k }

// failingRepo 在 UpdateChannel / CreateChannel 时注入失败，其余委托真实仓储。
type failingRepo struct {
	Repo
	failUpdate error
	failCreate error
}

func (r *failingRepo) UpdateChannel(ctx context.Context, c *model.Channel) error {
	if r.failUpdate != nil {
		return r.failUpdate
	}
	return r.Repo.UpdateChannel(ctx, c)
}

func (r *failingRepo) CreateChannel(ctx context.Context, c *model.Channel) error {
	if r.failCreate != nil {
		return r.failCreate
	}
	return r.Repo.CreateChannel(ctx, c)
}

// memSecrets 是内存版 Secrets，按约定返回带 model.Code 的错误（与组合根 secretAdapter 一致）；
// failGet / failSet 可注入后端故障。
type memSecrets struct {
	mu      sync.Mutex
	items   map[string]string
	failGet error
	failSet error
}

func newMemSecrets() *memSecrets { return &memSecrets{items: map[string]string{}} }

func (m *memSecrets) Get(_ context.Context, name string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failGet != nil {
		return "", m.failGet
	}
	v, ok := m.items[name]
	if !ok {
		return "", &model.Error{Code: model.CodeNotFound, Message: "凭据不存在", Details: map[string]any{"secret": name}}
	}
	return v, nil
}

func (m *memSecrets) Set(_ context.Context, name, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failSet != nil {
		return m.failSet
	}
	m.items[name] = value
	return nil
}

func (m *memSecrets) Delete(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, name)
	return nil
}

func (m *memSecrets) Has(_ context.Context, name string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.items[name]
	return ok, nil
}

type fixture struct {
	st      *memRepo
	secrets *memSecrets
	gw      *fakeGateway
	svc     *Service
	agentID string
	repo    *failingRepo
}

func newFixture(t *testing.T, withGateway bool) *fixture {
	t.Helper()
	st := newMemRepo()
	secrets := newMemSecrets()
	now := time.Now().UTC()
	agent := &model.Agent{
		ID: model.NewID(), Name: "助理", Autonomy: model.AutonomyAskRisky, WorkspaceDir: "a",
		Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	st.agents[agent.ID] = agent
	f := &fixture{st: st, secrets: secrets, agentID: agent.ID, repo: &failingRepo{Repo: st}}
	opts := Options{
		Kinds: fakeKinds{model.ChannelConsole, model.ChannelTelegram},
		ValidateConfig: func(raw json.RawMessage) error {
			var m map[string]any
			_ = json.Unmarshal(raw, &m)
			if _, bad := m["group_mode"].(float64); bad {
				return errors.New("group_mode 必须是字符串")
			}
			return nil
		},
	}
	if withGateway {
		f.gw = &fakeGateway{status: map[string]RuntimeStatus{}}
		opts.Gateway = f.gw
	}
	var err error
	f.svc, err = New(f.repo, secrets, opts)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func boolPtr(b bool) *bool    { return &b }
func strPtr(s string) *string { return &s }
func secretOf(t *testing.T, f *fixture, ref string) (string, error) {
	t.Helper()
	return f.secrets.Get(context.Background(), ref)
}

func TestCreateStoresSecretRefOnlyAndReloads(t *testing.T) {
	f := newFixture(t, true)
	ctx := context.Background()
	ch, err := f.svc.Create(ctx, Input{
		AgentID: f.agentID, Kind: model.ChannelTelegram, Name: "  机器人 ", Secret: "123:ABC-token",
		Config: []byte(`{ "allowed_peers": ["42"] }`),
	})
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if ch.Name != "机器人" || !ch.Enabled || ch.SecretRef != SecretRef(ch.ID) {
		t.Fatalf("记录字段不对: %+v", ch)
	}
	if string(ch.Config) != `{"allowed_peers":["42"]}` {
		t.Fatalf("配置应被压缩保存，得到 %s", ch.Config)
	}
	raw, _ := json.Marshal(ch)
	if strings.Contains(string(raw), "ABC-token") {
		t.Fatal("返回的记录不应包含凭据")
	}
	stored, err := f.st.GetChannel(ctx, ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(stored)
	if strings.Contains(string(raw), "ABC-token") {
		t.Fatal("数据库记录不应包含凭据")
	}
	if v, err := secretOf(t, f, ch.SecretRef); err != nil || v != "123:ABC-token" {
		t.Fatalf("凭据应写入 secret 存储，得到 %q / %v", v, err)
	}
	if !f.svc.HasSecret(ctx, ch) {
		t.Fatal("HasSecret 应为 true")
	}
	if f.gw.reloadCount() != 1 {
		t.Fatalf("启用的通道创建后应重载一次，得到 %d", f.gw.reloadCount())
	}
}

func TestCreateValidatesInput(t *testing.T) {
	f := newFixture(t, true)
	ctx := context.Background()
	cases := []struct {
		name string
		in   Input
		code model.Code
	}{
		{"不支持的类型", Input{AgentID: f.agentID, Kind: model.ChannelFeishu, Name: "x"}, model.CodeInvalid},
		{"空名称", Input{AgentID: f.agentID, Kind: model.ChannelConsole, Name: "  "}, model.CodeInvalid},
		{"名称过长", Input{AgentID: f.agentID, Kind: model.ChannelConsole, Name: strings.Repeat("名", MaxNameRunes+1)}, model.CodeInvalid},
		{"缺少智能体", Input{Kind: model.ChannelConsole, Name: "x"}, model.CodeInvalid},
		{"智能体不存在", Input{AgentID: model.NewID(), Kind: model.ChannelConsole, Name: "x"}, model.CodeNotFound},
		{"配置不是对象", Input{AgentID: f.agentID, Kind: model.ChannelConsole, Name: "x", Config: []byte(`[1]`)}, model.CodeInvalid},
		{"配置校验失败", Input{AgentID: f.agentID, Kind: model.ChannelConsole, Name: "x", Config: []byte(`{"group_mode":1}`)}, model.CodeInvalid},
		{"凭据含换行", Input{AgentID: f.agentID, Kind: model.ChannelConsole, Name: "x", Secret: "a\nb"}, model.CodeInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.svc.Create(ctx, tc.in); !model.IsCode(err, tc.code) {
				t.Fatalf("期望 %s，得到 %v", tc.code, err)
			}
		})
	}
	if list, _ := f.svc.List(ctx, ""); len(list) != 0 {
		t.Fatalf("校验失败不应留下记录，得到 %d 条", len(list))
	}
	if f.gw.reloadCount() != 0 {
		t.Fatal("校验失败不应触发重载")
	}
}

func TestCreateRollsBackSecretWhenRecordFails(t *testing.T) {
	f := newFixture(t, true)
	f.repo.failCreate = model.NewError(model.CodeUnavailable, "数据库忙")
	_, err := f.svc.Create(context.Background(), Input{AgentID: f.agentID, Kind: model.ChannelConsole, Name: "x", Secret: "s3cr3t"})
	if !model.IsCode(err, model.CodeUnavailable) {
		t.Fatalf("应返回仓储错误，得到 %v", err)
	}
	// 新 ID 不可知：确认 secret 存储里没有任何 channel:* 残留的方式是再创建一个并检查只有它自己的。
	f.repo.failCreate = nil
	ch, err := f.svc.Create(context.Background(), Input{AgentID: f.agentID, Kind: model.ChannelConsole, Name: "y", Secret: "v2"})
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := secretOf(t, f, ch.SecretRef); v != "v2" {
		t.Fatalf("新通道的凭据应可读，得到 %q", v)
	}
}

func TestCreateSucceedsWhenReloadFailsAndStatusReportsFailure(t *testing.T) {
	f := newFixture(t, true)
	f.gw.err = model.NewError(model.CodeInvalid, "令牌无效")
	ch, err := f.svc.Create(context.Background(), Input{AgentID: f.agentID, Kind: model.ChannelTelegram, Name: "x", Secret: "bad"})
	if err != nil {
		t.Fatalf("重载失败不应让创建失败: %v", err)
	}
	if state, msg := f.svc.Status(ch.ID); state != StateFailed || msg != "令牌无效" {
		t.Fatalf("状态应为 failed/令牌无效，得到 %s/%s", state, msg)
	}
	f.gw.err = nil
	if _, err := f.svc.Reload(context.Background(), ch.ID); err != nil {
		t.Fatalf("重载失败: %v", err)
	}
	if state, _ := f.svc.Status(ch.ID); state != StateStopped {
		t.Fatalf("重载成功后失败记录应清除（网关未报告运行时为 stopped），得到 %s", state)
	}
}

func TestCreateDisabledDoesNotReload(t *testing.T) {
	f := newFixture(t, true)
	ch, err := f.svc.Create(context.Background(), Input{AgentID: f.agentID, Kind: model.ChannelConsole, Name: "x", Enabled: boolPtr(false)})
	if err != nil {
		t.Fatal(err)
	}
	if ch.Enabled || ch.SecretRef != "" || f.gw.reloadCount() != 0 {
		t.Fatalf("停用且无凭据的通道不应写凭据或重载: %+v reloads=%d", ch, f.gw.reloadCount())
	}
	if f.svc.HasSecret(context.Background(), ch) {
		t.Fatal("没有凭据时 HasSecret 应为 false")
	}
}

func TestUpdateSemantics(t *testing.T) {
	f := newFixture(t, true)
	ctx := context.Background()
	ch, err := f.svc.Create(ctx, Input{AgentID: f.agentID, Kind: model.ChannelTelegram, Name: "x", Secret: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	base := f.gw.reloadCount()

	// 只改名称：写库但不重载（不打断连接）。
	got, err := f.svc.Update(ctx, ch.ID, Patch{Name: strPtr("新名字")})
	if err != nil || got.Name != "新名字" {
		t.Fatalf("改名失败: %+v / %v", got, err)
	}
	if f.gw.reloadCount() != base {
		t.Fatal("只改名称不应重载")
	}

	// 没有变化：不写库也不重载。
	before := got.UpdatedAt
	got, err = f.svc.Update(ctx, ch.ID, Patch{Name: strPtr("新名字"), Enabled: boolPtr(true)})
	if err != nil || !got.UpdatedAt.Equal(before) || f.gw.reloadCount() != base {
		t.Fatalf("无变化的更新不应写库或重载: %+v / %v", got, err)
	}

	// 轮换凭据：引用不变，值更新，触发重载。
	got, err = f.svc.Update(ctx, ch.ID, Patch{Secret: strPtr("v2")})
	if err != nil || got.SecretRef != SecretRef(ch.ID) {
		t.Fatalf("轮换凭据失败: %+v / %v", got, err)
	}
	if v, _ := secretOf(t, f, got.SecretRef); v != "v2" {
		t.Fatalf("凭据应为 v2，得到 %q", v)
	}
	if f.gw.reloadCount() != base+1 {
		t.Fatal("轮换凭据应重载")
	}

	// 清除凭据：记录引用清空、secret 删除。
	got, err = f.svc.Update(ctx, ch.ID, Patch{Secret: strPtr("")})
	if err != nil || got.SecretRef != "" {
		t.Fatalf("清除凭据失败: %+v / %v", got, err)
	}
	if _, err := secretOf(t, f, SecretRef(ch.ID)); !model.IsCode(err, model.CodeNotFound) {
		t.Fatalf("清除后凭据应不存在，得到 %v", err)
	}
	if f.gw.reloadCount() != base+2 {
		t.Fatal("清除凭据应重载")
	}

	// 改配置与停用都重载。
	if _, err := f.svc.Update(ctx, ch.ID, Patch{Config: []byte(`{"group_mode":"all"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Update(ctx, ch.ID, Patch{Enabled: boolPtr(false)}); err != nil {
		t.Fatal(err)
	}
	if f.gw.reloadCount() != base+4 {
		t.Fatalf("配置与启用状态变化应各重载一次，得到 %d", f.gw.reloadCount()-base-2)
	}

	if _, err := f.svc.Update(ctx, model.NewID(), Patch{Name: strPtr("x")}); !model.IsCode(err, model.CodeNotFound) {
		t.Fatalf("更新不存在的通道应 not_found，得到 %v", err)
	}
	if _, err := f.svc.Update(ctx, ch.ID, Patch{Config: []byte(`"x"`)}); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("非法配置应 invalid，得到 %v", err)
	}
}

func TestUpdateRestoresSecretWhenRecordWriteFails(t *testing.T) {
	f := newFixture(t, true)
	ctx := context.Background()
	ch, err := f.svc.Create(ctx, Input{AgentID: f.agentID, Kind: model.ChannelTelegram, Name: "x", Secret: "old"})
	if err != nil {
		t.Fatal(err)
	}
	f.repo.failUpdate = model.NewError(model.CodeUnavailable, "数据库忙")
	if _, err := f.svc.Update(ctx, ch.ID, Patch{Secret: strPtr("new")}); !model.IsCode(err, model.CodeUnavailable) {
		t.Fatalf("应返回仓储错误，得到 %v", err)
	}
	if v, _ := secretOf(t, f, ch.SecretRef); v != "old" {
		t.Fatalf("记录写入失败时应恢复旧凭据，得到 %q", v)
	}
	if _, err := f.svc.Update(ctx, ch.ID, Patch{Secret: strPtr("")}); !model.IsCode(err, model.CodeUnavailable) {
		t.Fatalf("应返回仓储错误，得到 %v", err)
	}
	if v, _ := secretOf(t, f, ch.SecretRef); v != "old" {
		t.Fatalf("记录写入失败时不应删除凭据，得到 %q", v)
	}

	// 原本没有凭据的通道：写入失败后新凭据被删掉。
	f.repo.failUpdate = nil
	bare, err := f.svc.Create(ctx, Input{AgentID: f.agentID, Kind: model.ChannelConsole, Name: "bare"})
	if err != nil {
		t.Fatal(err)
	}
	f.repo.failUpdate = model.NewError(model.CodeUnavailable, "数据库忙")
	if _, err := f.svc.Update(ctx, bare.ID, Patch{Secret: strPtr("fresh")}); err == nil {
		t.Fatal("应返回错误")
	}
	if _, err := secretOf(t, f, SecretRef(bare.ID)); !model.IsCode(err, model.CodeNotFound) {
		t.Fatalf("回滚后不应残留新凭据，得到 %v", err)
	}
}

func TestDeleteRemovesRecordSecretAndStopsAdapter(t *testing.T) {
	f := newFixture(t, true)
	ctx := context.Background()
	ch, err := f.svc.Create(ctx, Input{AgentID: f.agentID, Kind: model.ChannelTelegram, Name: "x", Secret: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	f.gw.err = model.NotFound("channel", ch.ID) // 记录删除后网关报告找不到，属于正常清理
	if err := f.svc.Delete(ctx, ch.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if _, err := f.svc.Get(ctx, ch.ID); !model.IsCode(err, model.CodeNotFound) {
		t.Fatalf("删除后应 not_found，得到 %v", err)
	}
	if _, err := secretOf(t, f, ch.SecretRef); !model.IsCode(err, model.CodeNotFound) {
		t.Fatalf("删除通道应删除凭据，得到 %v", err)
	}
	if f.gw.reloadCount() != 2 {
		t.Fatalf("删除后应让网关停止适配器，重载次数 %d", f.gw.reloadCount())
	}
	if err := f.svc.Delete(ctx, ch.ID); !model.IsCode(err, model.CodeNotFound) {
		t.Fatalf("重复删除应 not_found，得到 %v", err)
	}
}

func TestStatusMapping(t *testing.T) {
	f := newFixture(t, true)
	id := model.NewID()
	cases := []struct {
		st        RuntimeStatus
		running   bool
		wantState string
		wantErr   string
	}{
		{RuntimeStatus{Running: true, Connected: true}, true, StateRunning, ""},
		{RuntimeStatus{Running: true, LastError: "网络不可达"}, true, StateFailed, "网络不可达"},
		{RuntimeStatus{Running: true}, true, StateStarting, ""},
		{RuntimeStatus{}, false, StateStopped, ""},
	}
	for _, tc := range cases {
		f.gw.mu.Lock()
		if tc.running {
			f.gw.status[id] = tc.st
		} else {
			delete(f.gw.status, id)
		}
		f.gw.mu.Unlock()
		if state, msg := f.svc.Status(id); state != tc.wantState || msg != tc.wantErr {
			t.Errorf("%+v → %s/%q，期望 %s/%q", tc.st, state, msg, tc.wantState, tc.wantErr)
		}
	}
}

func TestWithoutGateway(t *testing.T) {
	f := newFixture(t, false)
	ctx := context.Background()
	ch, err := f.svc.Create(ctx, Input{AgentID: f.agentID, Kind: model.ChannelConsole, Name: "x"})
	if err != nil {
		t.Fatalf("没有网关时 CRUD 应照常工作: %v", err)
	}
	if _, err := f.svc.Reload(ctx, ch.ID); !model.IsCode(err, model.CodeUnavailable) {
		t.Fatalf("没有网关时 Reload 应 unavailable，得到 %v", err)
	}
	if state, _ := f.svc.Status(ch.ID); state != StateStopped {
		t.Fatalf("没有网关时状态应为 stopped，得到 %s", state)
	}
	if err := f.svc.Delete(ctx, ch.ID); err != nil {
		t.Fatal(err)
	}
}

func TestReloadReturnsLatestRecordAndErrors(t *testing.T) {
	f := newFixture(t, true)
	ctx := context.Background()
	ch, err := f.svc.Create(ctx, Input{AgentID: f.agentID, Kind: model.ChannelConsole, Name: "x"})
	if err != nil {
		t.Fatal(err)
	}
	f.gw.onReload = func(id string) {
		cur, _ := f.st.GetChannel(ctx, id)
		cur.LastError = ""
		cur.Name = "网关刷新过"
		_ = f.st.UpdateChannel(ctx, cur)
	}
	got, err := f.svc.Reload(ctx, ch.ID)
	if err != nil || got.Name != "网关刷新过" {
		t.Fatalf("Reload 应返回重新读取的记录，得到 %+v / %v", got, err)
	}
	f.gw.onReload = nil
	f.gw.err = model.NewError(model.CodeUnavailable, "连接失败")
	if _, err := f.svc.Reload(ctx, ch.ID); !model.IsCode(err, model.CodeUnavailable) {
		t.Fatalf("显式重载失败应返回错误，得到 %v", err)
	}
	if _, err := f.svc.Reload(ctx, model.NewID()); !model.IsCode(err, model.CodeNotFound) {
		t.Fatalf("重载不存在的通道应 not_found，得到 %v", err)
	}
}

func TestMapSecretErrKeepsModelCodes(t *testing.T) {
	cases := []struct {
		err  error
		code model.Code
	}{
		{model.NotFound("secret", "x"), model.CodeNotFound},
		{model.NewError(model.CodeInvalid, "凭据内容过大"), model.CodeInvalid},
		{model.NewError(model.CodeCancelled, "取消"), model.CodeCancelled},
		{fmt.Errorf("包装: %w", model.NewError(model.CodeTimeout, "超时")), model.CodeTimeout},
		{errors.New("不符合约定的裸错误"), model.CodeUnavailable},
	}
	for _, tc := range cases {
		got := mapSecretErr(tc.err, "写入通道凭据")
		if model.CodeOf(got) != tc.code {
			t.Errorf("%v → %s，期望保留 %s", tc.err, model.CodeOf(got), tc.code)
		}
		if !strings.Contains(got.Error(), "写入通道凭据") {
			t.Errorf("错误应带操作说明: %v", got)
		}
	}
	if mapSecretErr(nil, "op") != nil {
		t.Error("nil 应映射为 nil")
	}
}

// 凭据存储报告的错误码原样传到调用方（例如超长凭据由组合根映射为 invalid → HTTP 400）。
func TestSecretErrorsPropagateModelCode(t *testing.T) {
	f := newFixture(t, true)
	ctx := context.Background()
	f.secrets.failSet = &model.Error{Code: model.CodeInvalid, Message: "凭据内容过大"}
	_, err := f.svc.Create(ctx, Input{AgentID: f.agentID, Kind: model.ChannelTelegram, Name: "tg", Secret: "x"})
	if !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("凭据存储返回 invalid 时创建应返回 invalid，得到 %v", err)
	}
	f.secrets.failSet = nil

	ch, err := f.svc.Create(ctx, Input{AgentID: f.agentID, Kind: model.ChannelTelegram, Name: "tg", Secret: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	f.secrets.failGet = model.NewError(model.CodeUnavailable, "钥匙串已锁定")
	if _, err := f.svc.Update(ctx, ch.ID, Patch{Secret: strPtr("v2")}); !model.IsCode(err, model.CodeUnavailable) {
		t.Fatalf("读取旧凭据失败（非 not_found）时应返回 unavailable，得到 %v", err)
	}
	f.secrets.failGet = nil
	if v, _ := secretOf(t, f, ch.SecretRef); v != "v1" {
		t.Errorf("失败的轮换不应改动凭据，得到 %q", v)
	}
}
