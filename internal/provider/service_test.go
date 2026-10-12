package provider

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/event"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

// fakeRepo 是内存版 model.ProviderRepo。
type fakeRepo struct {
	mu    sync.Mutex
	items map[string]*model.Provider
	// failCreate / failUpdate 用于模拟仓储故障。
	failCreate, failUpdate bool
}

func newFakeRepo() *fakeRepo { return &fakeRepo{items: map[string]*model.Provider{}} }

func clone(p *model.Provider) *model.Provider {
	c := *p
	c.Models = append([]string(nil), p.Models...)
	return &c
}

func (r *fakeRepo) CreateProvider(_ context.Context, p *model.Provider) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failCreate {
		return model.NewError(model.CodeInternal, "模拟写入失败")
	}
	if _, ok := r.items[p.ID]; ok {
		return model.NewError(model.CodeConflict, "重复")
	}
	r.items[p.ID] = clone(p)
	return nil
}

func (r *fakeRepo) GetProvider(_ context.Context, id string) (*model.Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.items[id]
	if !ok {
		return nil, model.NotFound("provider", id)
	}
	return clone(p), nil
}

func (r *fakeRepo) ListProviders(_ context.Context) ([]*model.Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*model.Provider, 0, len(r.items))
	for _, p := range r.items {
		out = append(out, clone(p))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (r *fakeRepo) UpdateProvider(_ context.Context, p *model.Provider) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failUpdate {
		return model.NewError(model.CodeInternal, "模拟更新失败")
	}
	if _, ok := r.items[p.ID]; !ok {
		return model.NotFound("provider", p.ID)
	}
	r.items[p.ID] = clone(p)
	return nil
}

func (r *fakeRepo) DeleteProvider(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.items[id]; !ok {
		return model.NotFound("provider", id)
	}
	delete(r.items, id)
	return nil
}

// fakeSecrets 是内存版 SecretStore。
type fakeSecrets struct {
	mu   sync.Mutex
	data map[string]string
}

func newFakeSecrets() *fakeSecrets { return &fakeSecrets{data: map[string]string{}} }

func (s *fakeSecrets) Get(_ context.Context, ref string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.data[ref]
	if !ok {
		return "", model.NotFound("secret", ref)
	}
	return v, nil
}

func (s *fakeSecrets) Set(_ context.Context, ref, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[ref] = value
	return nil
}

func (s *fakeSecrets) Delete(_ context.Context, ref string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[ref]; !ok {
		return model.NotFound("secret", ref)
	}
	delete(s.data, ref)
	return nil
}

func (s *fakeSecrets) Has(_ context.Context, ref string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.data[ref]
	return ok, nil
}

type fakeAgents struct{ agents []*model.Agent }

func (f *fakeAgents) ListAgents(context.Context) ([]*model.Agent, error) { return f.agents, nil }

type fixture struct {
	repo    *fakeRepo
	secrets *fakeSecrets
	bus     *event.Bus
	agents  *fakeAgents
	svc     *Service
}

func newFixture(t *testing.T, opts ...Option) *fixture {
	t.Helper()
	f := &fixture{repo: newFakeRepo(), secrets: newFakeSecrets(), bus: event.NewBus(64), agents: &fakeAgents{}}
	all := append([]Option{WithAgentLister(f.agents)}, opts...)
	f.svc = New(f.repo, f.secrets, f.bus, nil, nil, all...)
	return f
}

func boolPtr(b bool) *bool    { return &b }
func strPtr(s string) *string { return &s }
func ctxBg() context.Context  { return context.Background() }
func mustCreate(t *testing.T, f *fixture, in Input) *model.Provider {
	t.Helper()
	p, err := f.svc.Create(ctxBg(), in)
	if err != nil {
		t.Fatalf("Create 失败: %v", err)
	}
	return p
}

func TestCreateStoresSecretOutOfRecord(t *testing.T) {
	f := newFixture(t)
	sub := f.bus.Subscribe(ctxBg(), 0, 8, nil)
	defer sub.Cancel()

	p := mustCreate(t, f, Input{Name: " OpenAI ", Kind: model.ProviderOpenAICompatible, BaseURL: "https://api.openai.com/v1/", APIKey: " sk-secret ", DefaultModel: "gpt-x"})
	if p.Name != "OpenAI" || p.BaseURL != "https://api.openai.com/v1" || !p.Enabled || !model.IsID(p.ID) {
		t.Fatalf("记录字段不符: %+v", p)
	}
	if p.SecretRef != SecretRef(p.ID) {
		t.Fatalf("SecretRef = %q，期望 provider:<id>", p.SecretRef)
	}
	if v := f.secrets.data[p.SecretRef]; v != "sk-secret" {
		t.Fatalf("密钥应写入 secret 存储（去掉首尾空白），实际 %q", v)
	}
	// 记录里任何字段都不得包含密钥。
	raw, _ := json.Marshal(p)
	if strings.Contains(string(raw), "sk-secret") {
		t.Fatalf("Provider 记录泄露了密钥: %s", raw)
	}
	ev := <-sub.C
	if ev.Type != EventCreated {
		t.Fatalf("事件类型 = %s", ev.Type)
	}
	view, ok := ev.Payload.(View)
	if !ok || !view.HasSecret || view.ID != p.ID {
		t.Fatalf("事件 Payload 应为含 HasSecret 的 View: %#v", ev.Payload)
	}
	if raw, _ := json.Marshal(view); strings.Contains(string(raw), "sk-secret") || strings.Contains(string(raw), "secret_ref") {
		t.Fatalf("事件视图泄露了密钥或引用名: %s", raw)
	}

	got, err := f.svc.Get(ctxBg(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	has, err := f.svc.HasSecret(ctxBg(), got)
	if err != nil || !has {
		t.Fatalf("HasSecret = %v, %v", has, err)
	}
	v, err := f.svc.View(ctxBg(), got)
	if err != nil || !v.HasSecret {
		t.Fatalf("View.HasSecret = %v, %v", v.HasSecret, err)
	}
}

func TestCreateWithoutKeyAndDefaults(t *testing.T) {
	f := newFixture(t)
	p := mustCreate(t, f, Input{Name: "本机", Kind: model.ProviderOllama, Enabled: boolPtr(false)})
	if p.SecretRef != "" || p.BaseURL != DefaultOllamaURL || p.Enabled {
		t.Fatalf("无密钥的 Ollama 记录不符: %+v", p)
	}
	if len(f.secrets.data) != 0 {
		t.Fatal("不应写入任何密钥")
	}
	has, _ := f.svc.HasSecret(ctxBg(), p)
	if has {
		t.Fatal("HasSecret 应为 false")
	}
	a := mustCreate(t, f, Input{Name: "A", Kind: model.ProviderAnthropic, BaseURL: "https://proxy.example.com/"})
	if a.BaseURL != "https://proxy.example.com" {
		t.Fatalf("末尾斜杠应去掉: %q", a.BaseURL)
	}
}

func TestCreateValidation(t *testing.T) {
	f := newFixture(t)
	cases := []Input{
		{Name: "", Kind: model.ProviderOllama},
		{Name: strings.Repeat("名", 65), Kind: model.ProviderOllama},
		{Name: "x", Kind: "unknown"},
		{Name: "x", Kind: model.ProviderOpenAICompatible}, // openai_compatible 不允许省略 base_url（不得静默回退到某个服务商）
		{Name: "x", Kind: model.ProviderOpenAICompatible, BaseURL: "ftp://host"},
		{Name: "x", Kind: model.ProviderOpenAICompatible, BaseURL: "not a url"},
		{Name: "x", Kind: model.ProviderOpenAICompatible, BaseURL: "/relative"},
		{Name: "x", Kind: model.ProviderOpenAICompatible, BaseURL: "https://user:pw@host/v1"},
		{Name: "x", Kind: model.ProviderOpenAICompatible, BaseURL: "https://host/v1?x=1"},
		{Name: "x", Kind: model.ProviderOpenAICompatible, DefaultModel: strings.Repeat("m", 129)},
	}
	for i, in := range cases {
		_, err := f.svc.Create(ctxBg(), in)
		if model.CodeOf(err) != model.CodeInvalid {
			t.Errorf("用例 %d 应返回 invalid，实际 %v", i, err)
		}
	}
	if len(f.repo.items) != 0 || len(f.secrets.data) != 0 {
		t.Fatal("校验失败不应留下记录或密钥")
	}
}

func TestCreateRollsBackSecretWhenRepoFails(t *testing.T) {
	f := newFixture(t)
	f.repo.failCreate = true
	_, err := f.svc.Create(ctxBg(), Input{Name: "x", Kind: model.ProviderOpenAICompatible, BaseURL: "https://llm.example/v1", APIKey: "k"})
	if err == nil {
		t.Fatal("应返回仓储错误")
	}
	if model.CodeOf(err) != model.CodeInternal {
		t.Errorf("应保留底层错误码: %v", err)
	}
	if len(f.secrets.data) != 0 {
		t.Fatal("仓储失败后应删除已写入的密钥")
	}
}

func TestUpdateRotatesAndClearsSecret(t *testing.T) {
	f := newFixture(t)
	p := mustCreate(t, f, Input{Name: "x", Kind: model.ProviderOpenAICompatible, BaseURL: "https://llm.example/v1", APIKey: "old"})

	// nil 不动密钥。
	u, err := f.svc.Update(ctxBg(), p.ID, UpdateInput{Name: strPtr("y"), Enabled: boolPtr(false)})
	if err != nil || u.Name != "y" || u.Enabled || f.secrets.data[p.SecretRef] != "old" {
		t.Fatalf("普通更新不应影响密钥: %+v, %v", u, err)
	}
	// 轮换。
	u, err = f.svc.Update(ctxBg(), p.ID, UpdateInput{APIKey: strPtr("new")})
	if err != nil || f.secrets.data[p.SecretRef] != "new" || u.SecretRef != p.SecretRef {
		t.Fatalf("轮换失败: %+v, %v, %v", u, err, f.secrets.data)
	}
	// 空字符串清除。
	u, err = f.svc.Update(ctxBg(), p.ID, UpdateInput{APIKey: strPtr("")})
	if err != nil || u.SecretRef != "" {
		t.Fatalf("清除失败: %+v, %v", u, err)
	}
	if _, ok := f.secrets.data[p.SecretRef]; ok {
		t.Fatal("清除后 secret 存储中不应保留密钥")
	}
	// 清除之后再设置：新引用仍为 provider:<id>。
	u, err = f.svc.Update(ctxBg(), p.ID, UpdateInput{APIKey: strPtr("again")})
	if err != nil || u.SecretRef != SecretRef(p.ID) || f.secrets.data[u.SecretRef] != "again" {
		t.Fatalf("重新设置失败: %+v, %v", u, err)
	}
	// 改类型时按新类型重新规范化地址。
	kind := model.ProviderAnthropic
	u, err = f.svc.Update(ctxBg(), p.ID, UpdateInput{Kind: &kind, BaseURL: strPtr("")})
	if err != nil || u.Kind != kind || u.BaseURL != "https://api.anthropic.com" {
		t.Fatalf("改类型后地址应取新默认值: %+v, %v", u, err)
	}
	_, err = f.svc.Update(ctxBg(), "missing", UpdateInput{})
	if model.CodeOf(err) != model.CodeNotFound {
		t.Errorf("更新不存在的供应商应返回 not_found: %v", err)
	}
}

func TestUpdateRollbackOnRepoFailure(t *testing.T) {
	f := newFixture(t)
	p := mustCreate(t, f, Input{Name: "x", Kind: model.ProviderOpenAICompatible, BaseURL: "https://llm.example/v1"})
	f.repo.failUpdate = true
	_, err := f.svc.Update(ctxBg(), p.ID, UpdateInput{APIKey: strPtr("k")})
	if err == nil {
		t.Fatal("应返回错误")
	}
	if len(f.secrets.data) != 0 {
		t.Fatal("此前没有密钥时，记录更新失败应删除新写入的密钥")
	}
}

func TestDeleteChecksReferencesAndRemovesSecret(t *testing.T) {
	f := newFixture(t)
	p := mustCreate(t, f, Input{Name: "x", Kind: model.ProviderOpenAICompatible, BaseURL: "https://llm.example/v1", APIKey: "k"})
	f.agents.agents = []*model.Agent{{ID: "A1", Name: "助理", Model: model.ModelRef{ProviderID: p.ID, Model: "m"}}}

	err := f.svc.Delete(ctxBg(), p.ID)
	if model.CodeOf(err) != model.CodeConflict {
		t.Fatalf("被引用时应返回 conflict: %v", err)
	}
	var me *model.Error
	if !errors.As(err, &me) {
		t.Fatal("应是 *model.Error")
	}
	refs, _ := me.Details["agents"].([]map[string]string)
	if len(refs) != 1 || refs[0]["id"] != "A1" {
		t.Fatalf("Details.agents 应列出引用者: %#v", me.Details)
	}
	if _, ok := f.secrets.data[p.SecretRef]; !ok {
		t.Fatal("冲突时不应删除密钥")
	}

	f.agents.agents = nil
	sub := f.bus.Subscribe(ctxBg(), 0, 8, func(e event.Event) bool { return e.Type == EventDeleted })
	defer sub.Cancel()
	if err := f.svc.Delete(ctxBg(), p.ID); err != nil {
		t.Fatalf("Delete 失败: %v", err)
	}
	if _, ok := f.secrets.data[p.SecretRef]; ok {
		t.Fatal("删除后密钥应一并清除")
	}
	if _, err := f.svc.Get(ctxBg(), p.ID); model.CodeOf(err) != model.CodeNotFound {
		t.Errorf("删除后应 not_found: %v", err)
	}
	ev := <-sub.C
	if payload, _ := ev.Payload.(map[string]string); payload["id"] != p.ID {
		t.Errorf("deleted 事件 Payload = %#v", ev.Payload)
	}
	if err := f.svc.Delete(ctxBg(), p.ID); model.CodeOf(err) != model.CodeNotFound {
		t.Errorf("重复删除应 not_found: %v", err)
	}
}

func TestCredentials(t *testing.T) {
	f := newFixture(t)
	p := mustCreate(t, f, Input{Name: "x", Kind: model.ProviderOpenAICompatible, BaseURL: "https://llm.example/v1", APIKey: "k", DefaultModel: "default-m"})

	c, err := f.svc.Credentials(ctxBg(), model.ModelRef{ProviderID: p.ID, Model: "explicit"})
	if err != nil || c.APIKey != "k" || c.Model != "explicit" || c.Kind != p.Kind || c.BaseURL != p.BaseURL {
		t.Fatalf("Credentials = %+v, %v", c, err)
	}
	c, err = f.svc.Credentials(ctxBg(), model.ModelRef{ProviderID: p.ID})
	if err != nil || c.Model != "default-m" {
		t.Fatalf("应回退到默认模型: %+v, %v", c, err)
	}

	// 密钥缺失。
	delete(f.secrets.data, p.SecretRef)
	_, err = f.svc.Credentials(ctxBg(), model.ModelRef{ProviderID: p.ID, Model: "m"})
	if model.CodeOf(err) != model.CodeUnavailable {
		t.Errorf("密钥缺失应 unavailable: %v", err)
	}
	// 停用。
	if _, err := f.svc.Update(ctxBg(), p.ID, UpdateInput{Enabled: boolPtr(false)}); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.Credentials(ctxBg(), model.ModelRef{ProviderID: p.ID, Model: "m"})
	if model.CodeOf(err) != model.CodeUnavailable {
		t.Errorf("停用应 unavailable: %v", err)
	}
	// 未指定模型。
	q := mustCreate(t, f, Input{Name: "q", Kind: model.ProviderOllama})
	_, err = f.svc.Credentials(ctxBg(), model.ModelRef{ProviderID: q.ID})
	if model.CodeOf(err) != model.CodeInvalid {
		t.Errorf("无模型应 invalid: %v", err)
	}
	c, err = f.svc.Credentials(ctxBg(), model.ModelRef{ProviderID: q.ID, Model: "llama"})
	if err != nil || c.APIKey != "" {
		t.Errorf("Ollama 无密钥应可用: %+v, %v", c, err)
	}
	// anthropic 必须有密钥。
	a := mustCreate(t, f, Input{Name: "a", Kind: model.ProviderAnthropic, DefaultModel: "claude"})
	_, err = f.svc.Credentials(ctxBg(), model.ModelRef{ProviderID: a.ID})
	if model.CodeOf(err) != model.CodeUnavailable {
		t.Errorf("anthropic 无密钥应 unavailable: %v", err)
	}
	_, err = f.svc.Credentials(ctxBg(), model.ModelRef{ProviderID: "missing", Model: "m"})
	if model.CodeOf(err) != model.CodeNotFound {
		t.Errorf("不存在的供应商应 not_found: %v", err)
	}
}

func TestListAndProviderExists(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f := newFixture(t, WithClock(func() time.Time { now = now.Add(time.Second); return now }))
	a := mustCreate(t, f, Input{Name: "a", Kind: model.ProviderOllama})
	b := mustCreate(t, f, Input{Name: "b", Kind: model.ProviderOllama})
	items, err := f.svc.List(ctxBg())
	if err != nil || len(items) != 2 || items[0].ID != a.ID || items[1].ID != b.ID {
		t.Fatalf("List = %v, %v", items, err)
	}
	ok, err := f.svc.ProviderExists(ctxBg(), a.ID)
	if err != nil || !ok {
		t.Fatalf("ProviderExists = %v, %v", ok, err)
	}
	ok, err = f.svc.ProviderExists(ctxBg(), "nope")
	if err != nil || ok {
		t.Fatalf("不存在应为 false: %v, %v", ok, err)
	}
}

func TestPresets(t *testing.T) {
	ps := Presets()
	if len(ps) < 8 {
		t.Fatalf("预设数量 = %d", len(ps))
	}
	seen := map[string]bool{}
	for _, p := range ps {
		if seen[p.ID] {
			t.Errorf("预设 ID 重复: %s", p.ID)
		}
		seen[p.ID] = true
		if err := validateKind(p.Kind); err != nil {
			t.Errorf("预设 %s 的类型不合法", p.ID)
		}
		if _, err := normalizeBaseURL(p.Kind, p.BaseURL); err != nil {
			t.Errorf("预设 %s 的地址不合法: %v", p.ID, err)
		}
		if p.Name == "" {
			t.Errorf("预设 %s 缺少名称", p.ID)
		}
	}
	for _, id := range []string{"openai", "deepseek", "dashscope", "moonshot", "zhipu", "minimax", "ollama", "anthropic"} {
		if _, ok := PresetByID(id); !ok {
			t.Errorf("缺少预设 %s", id)
		}
	}
	// 返回副本：修改不影响内置数据。
	ps[0].Name = "changed"
	if Presets()[0].Name == "changed" {
		t.Error("Presets 应返回副本")
	}
}
