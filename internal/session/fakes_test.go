package session

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/event"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

// ---- 内存仓储 ------------------------------------------------------------

// memData 是内存仓储的全部状态；事务在它的深拷贝上执行，成功后整体替换。
type memData struct {
	sessions map[string]*model.Session
	messages map[string][]*model.Message // sessionID → 按 Seq 升序
	runs     map[string]*model.Run
}

func newMemData() *memData {
	return &memData{
		sessions: map[string]*model.Session{},
		messages: map[string][]*model.Message{},
		runs:     map[string]*model.Run{},
	}
}

func (d *memData) clone() *memData {
	c := newMemData()
	for k, v := range d.sessions {
		s := *v
		c.sessions[k] = &s
	}
	for k, list := range d.messages {
		out := make([]*model.Message, len(list))
		for i, m := range list {
			mm := *m
			out[i] = &mm
		}
		c.messages[k] = out
	}
	for k, v := range d.runs {
		r := *v
		c.runs[k] = &r
	}
	return c
}

// storeHooks 用于注入失败。
type storeHooks struct {
	mu            sync.Mutex
	failAppend    error
	failCreateRun error
	failUpdateRun error
	failTx        error
	// beforeList 在下一次 ListMessages 取锁之前调用一次（随后清空），用于在 Send 的提交之后插入操作。
	beforeList func()
}

// takeBeforeList 取出并清空 beforeList。
func (h *storeHooks) takeBeforeList() func() {
	h.mu.Lock()
	defer h.mu.Unlock()
	fn := h.beforeList
	h.beforeList = nil
	return fn
}

func (h *storeHooks) get(pick func(*storeHooks) error) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return pick(h)
}

// memStore 实现 model.Store 中本包用到的部分；未实现的方法由嵌入的 nil 接口承担，调用即 panic。
type memStore struct {
	model.Store
	mu    sync.Mutex
	data  *memData
	tx    bool
	hooks *storeHooks
}

func newMemStore() *memStore {
	return &memStore{data: newMemData(), hooks: &storeHooks{}}
}

func (s *memStore) do(fn func(d *memData) error) error {
	if s.tx {
		return fn(s.data)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return fn(s.data)
}

func (s *memStore) Tx(ctx context.Context, fn func(tx model.Store) error) error {
	if s.tx {
		return errors.New("memStore: 事务不可重入")
	}
	if err := s.hooks.get(func(h *storeHooks) error { return h.failTx }); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	clone := s.data.clone()
	view := &memStore{data: clone, tx: true, hooks: s.hooks}
	if err := fn(view); err != nil {
		return err
	}
	s.data = clone
	return nil
}

func (s *memStore) Close() error { return nil }

func (s *memStore) CreateSession(_ context.Context, in *model.Session) error {
	return s.do(func(d *memData) error {
		if _, ok := d.sessions[in.ID]; ok {
			return model.Errorf(model.CodeConflict, "会话 %s 已存在", in.ID)
		}
		for _, x := range d.sessions {
			if x.Status == model.SessionActive && x.Key == in.Key {
				return model.Errorf(model.CodeConflict, "路由键 %s 已被活动会话持有", in.Key)
			}
		}
		c := *in
		d.sessions[in.ID] = &c
		return nil
	})
}

func (s *memStore) GetSession(_ context.Context, id string) (*model.Session, error) {
	var out *model.Session
	err := s.do(func(d *memData) error {
		x, ok := d.sessions[id]
		if !ok {
			return model.NotFound("session", id)
		}
		c := *x
		out = &c
		return nil
	})
	return out, err
}

func (s *memStore) GetActiveSessionByKey(_ context.Context, key string) (*model.Session, error) {
	var out *model.Session
	err := s.do(func(d *memData) error {
		for _, x := range d.sessions {
			if x.Status == model.SessionActive && x.Key == key {
				c := *x
				out = &c
				return nil
			}
		}
		return model.NotFound("session", key)
	})
	return out, err
}

func (s *memStore) ListSessions(_ context.Context, f model.SessionFilter, p model.Page) ([]*model.Session, error) {
	var out []*model.Session
	err := s.do(func(d *memData) error {
		for _, x := range d.sessions {
			if f.AgentID != "" && x.AgentID != f.AgentID {
				continue
			}
			if f.Status != "" && x.Status != f.Status {
				continue
			}
			if f.Origin != "" && x.Origin != f.Origin {
				continue
			}
			c := *x
			out = append(out, &c)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
		if p.Offset > 0 && p.Offset < len(out) {
			out = out[p.Offset:]
		} else if p.Offset >= len(out) {
			out = nil
		}
		if p.Limit > 0 && len(out) > p.Limit {
			out = out[:p.Limit]
		}
		return nil
	})
	return out, err
}

func (s *memStore) UpdateSession(_ context.Context, in *model.Session) error {
	return s.do(func(d *memData) error {
		if _, ok := d.sessions[in.ID]; !ok {
			return model.NotFound("session", in.ID)
		}
		c := *in
		d.sessions[in.ID] = &c
		return nil
	})
}

func (s *memStore) DeleteSession(_ context.Context, id string) error {
	return s.do(func(d *memData) error {
		if _, ok := d.sessions[id]; !ok {
			return model.NotFound("session", id)
		}
		delete(d.sessions, id)
		delete(d.messages, id)
		for rid, r := range d.runs {
			if r.SessionID == id {
				delete(d.runs, rid)
			}
		}
		return nil
	})
}

func (s *memStore) AppendMessage(_ context.Context, m *model.Message) error {
	if err := s.hooks.get(func(h *storeHooks) error { return h.failAppend }); err != nil {
		return err
	}
	return s.do(func(d *memData) error {
		sess, ok := d.sessions[m.SessionID]
		if !ok {
			return model.NotFound("session", m.SessionID)
		}
		list := d.messages[m.SessionID]
		var seq int64 = 1
		if n := len(list); n > 0 {
			seq = list[n-1].Seq + 1
		}
		m.Seq = seq
		c := *m
		d.messages[m.SessionID] = append(list, &c)
		at := m.CreatedAt
		sess.LastMessageAt = &at
		return nil
	})
}

func (s *memStore) GetMessage(_ context.Context, id string) (*model.Message, error) {
	var out *model.Message
	err := s.do(func(d *memData) error {
		for _, list := range d.messages {
			for _, m := range list {
				if m.ID == id {
					c := *m
					out = &c
					return nil
				}
			}
		}
		return model.NotFound("message", id)
	})
	return out, err
}

func (s *memStore) ListMessages(ctx context.Context, sessionID string, afterSeq int64, limit int) ([]*model.Message, error) {
	if fn := s.hooks.takeBeforeList(); fn != nil {
		fn()
	}
	// 与 SQLite 的 QueryContext 一样尊重取消。
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out []*model.Message
	err := s.do(func(d *memData) error {
		for _, m := range d.messages[sessionID] {
			if m.Seq <= afterSeq {
				continue
			}
			if limit > 0 && len(out) >= limit {
				break
			}
			c := *m
			out = append(out, &c)
		}
		return nil
	})
	return out, err
}

func (s *memStore) CreateRun(_ context.Context, r *model.Run) error {
	if err := s.hooks.get(func(h *storeHooks) error { return h.failCreateRun }); err != nil {
		return err
	}
	return s.do(func(d *memData) error {
		if _, ok := d.runs[r.ID]; ok {
			return model.Errorf(model.CodeConflict, "运行 %s 已存在", r.ID)
		}
		c := *r
		d.runs[r.ID] = &c
		return nil
	})
}

func (s *memStore) GetRun(_ context.Context, id string) (*model.Run, error) {
	var out *model.Run
	err := s.do(func(d *memData) error {
		r, ok := d.runs[id]
		if !ok {
			return model.NotFound("run", id)
		}
		c := *r
		out = &c
		return nil
	})
	return out, err
}

func (s *memStore) UpdateRun(_ context.Context, r *model.Run) error {
	if err := s.hooks.get(func(h *storeHooks) error { return h.failUpdateRun }); err != nil {
		return err
	}
	return s.do(func(d *memData) error {
		if _, ok := d.runs[r.ID]; !ok {
			return model.NotFound("run", r.ID)
		}
		c := *r
		d.runs[r.ID] = &c
		return nil
	})
}

func (s *memStore) ListRuns(_ context.Context, sessionID string, p model.Page) ([]*model.Run, error) {
	var out []*model.Run
	err := s.do(func(d *memData) error {
		for _, r := range d.runs {
			if r.SessionID == sessionID {
				c := *r
				out = append(out, &c)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
		if p.Limit > 0 && len(out) > p.Limit {
			out = out[:p.Limit]
		}
		return nil
	})
	return out, err
}

func (s *memStore) ListActiveRuns(_ context.Context) ([]*model.Run, error) {
	var out []*model.Run
	err := s.do(func(d *memData) error {
		for _, r := range d.runs {
			if !r.Status.Terminal() {
				c := *r
				out = append(out, &c)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		return nil
	})
	return out, err
}

// ---- 假数据源 ------------------------------------------------------------

type fakeAgents struct {
	mu     sync.Mutex
	agents map[string]*model.Agent
}

func (f *fakeAgents) GetAgent(_ context.Context, id string) (*model.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.agents[id]
	if !ok {
		return nil, model.NotFound("agent", id)
	}
	c := *a
	return &c, nil
}

func (f *fakeAgents) set(a *model.Agent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.agents[a.ID] = a
}

type fakeCreds struct {
	mu    sync.Mutex
	creds model.ProviderCredentials
	err   error
	calls int
}

func (f *fakeCreds) Credentials(_ context.Context, _ model.ModelRef) (model.ProviderCredentials, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return model.ProviderCredentials{}, f.err
	}
	return f.creds, nil
}

type fakeTools struct {
	tools []model.ToolSpec
	err   error
}

func (f *fakeTools) ToolsFor(_ context.Context, _ *model.Agent) ([]model.ToolSpec, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.tools, nil
}

type fakePrompt struct{ suffix string }

func (p fakePrompt) BuildSystemPrompt(_ context.Context, agent *model.Agent, _ *model.Session) (string, error) {
	return agent.SystemPrompt + p.suffix, nil
}

// ---- 假运行时 ------------------------------------------------------------

// fakeRuntime 记录请求并把事件回调保存起来，由测试按需驱动；onStart 可在受理时同步发事件。
type fakeRuntime struct {
	mu       sync.Mutex
	requests []RunRequest
	// startCtxErrs 记录每次 StartRun 收到的 ctx 在调用时的 Err()。
	startCtxErrs []error
	cancels      []string
	sinks        map[string]func(model.RunEvent)
	startErr     error
	cancelErr    error
	// autoCancel 为 true 时 CancelRun 立刻回调 run.cancelled，模拟工作进程的行为。
	autoCancel bool
	onStart    func(req RunRequest, sink func(model.RunEvent))
}

func newFakeRuntime() *fakeRuntime {
	return &fakeRuntime{sinks: map[string]func(model.RunEvent){}, autoCancel: true}
}

func (f *fakeRuntime) StartRun(ctx context.Context, req RunRequest, sink func(model.RunEvent)) error {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.startCtxErrs = append(f.startCtxErrs, ctx.Err())
	if f.startErr != nil {
		err := f.startErr
		f.mu.Unlock()
		return err
	}
	f.sinks[req.RunID] = sink
	onStart := f.onStart
	f.mu.Unlock()
	if onStart != nil {
		onStart(req, sink)
	}
	return nil
}

func (f *fakeRuntime) CancelRun(_ context.Context, runID string) error {
	f.mu.Lock()
	f.cancels = append(f.cancels, runID)
	err := f.cancelErr
	sink := f.sinks[runID]
	auto := f.autoCancel
	f.mu.Unlock()
	if err != nil {
		return err
	}
	if auto && sink != nil {
		sink(model.RunEvent{Type: model.EventRunCancelled})
	}
	return nil
}

func (f *fakeRuntime) emit(t *testing.T, runID string, ev model.RunEvent) {
	t.Helper()
	f.mu.Lock()
	sink := f.sinks[runID]
	f.mu.Unlock()
	if sink == nil {
		t.Fatalf("运行 %s 没有注册事件回调", runID)
	}
	sink(ev)
}

func (f *fakeRuntime) lastRequest(t *testing.T) RunRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		t.Fatal("运行时没有收到任何请求")
	}
	return f.requests[len(f.requests)-1]
}

func (f *fakeRuntime) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *fakeRuntime) cancelCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.cancels)
}

// ---- 测试装配 ------------------------------------------------------------

type harness struct {
	t      *testing.T
	store  *memStore
	agents *fakeAgents
	creds  *fakeCreds
	tools  *fakeTools
	rt     *fakeRuntime
	bus    *event.Bus
	svc    *Service
	agent  *model.Agent
}

func newHarness(t *testing.T, opts Options) *harness {
	t.Helper()
	h := &harness{
		t:      t,
		store:  newMemStore(),
		agents: &fakeAgents{agents: map[string]*model.Agent{}},
		creds:  &fakeCreds{creds: model.ProviderCredentials{Kind: model.ProviderOpenAICompatible, APIKey: "test-key", Model: "m"}},
		tools:  &fakeTools{tools: []model.ToolSpec{{Name: "read_file", Source: "builtin", Risk: model.RiskRead}}},
		rt:     newFakeRuntime(),
		bus:    event.NewBus(1024),
	}
	h.agent = &model.Agent{
		ID:           model.NewID(),
		Name:         "助理",
		SystemPrompt: "你是助理",
		Model:        model.ModelRef{ProviderID: "p", Model: "m"},
		Enabled:      true,
	}
	h.agents.set(h.agent)
	h.svc = New(h.store, h.agents, h.creds, h.tools, h.rt, h.bus, slog.New(slog.DiscardHandler), opts)
	t.Cleanup(h.svc.Close)
	return h
}

func (h *harness) session() *model.Session {
	h.t.Helper()
	sess, err := h.svc.Create(context.Background(), h.agent.ID, model.OriginDesktop, "")
	if err != nil {
		h.t.Fatalf("创建会话失败: %v", err)
	}
	return sess
}

func (h *harness) send(sessionID, text string) *model.Run {
	h.t.Helper()
	run, err := h.svc.Send(context.Background(), SendRequest{SessionID: sessionID, Text: text})
	if err != nil {
		h.t.Fatalf("发送失败: %v", err)
	}
	return run
}

func (h *harness) subscribe() *event.Subscription {
	h.t.Helper()
	sub := h.bus.Subscribe(context.Background(), h.bus.LastSeq(), 1024, nil)
	h.t.Cleanup(sub.Cancel)
	return sub
}

// drain 取出订阅里已经投递的事件（发布是同步的，所以操作返回后事件就在通道里）。
func drain(sub *event.Subscription) []event.Event {
	var out []event.Event
	for {
		select {
		case ev, ok := <-sub.C:
			if !ok {
				return out
			}
			out = append(out, ev)
		default:
			return out
		}
	}
}

func eventTypes(evs []event.Event) []string {
	out := make([]string, len(evs))
	for i, ev := range evs {
		out[i] = ev.Type
	}
	return out
}

// waitEvent 阻塞等待某个类型的事件，最多 timeout。
func waitEvent(t *testing.T, sub *event.Subscription, typ string, timeout time.Duration) event.Event {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-sub.C:
			if !ok {
				t.Fatalf("等待 %s 时订阅已关闭", typ)
			}
			if ev.Type == typ {
				return ev
			}
		case <-deadline:
			t.Fatalf("等待事件 %s 超时", typ)
		}
	}
}

func runEventOf(t *testing.T, ev event.Event) model.RunEvent {
	t.Helper()
	re, ok := ev.Payload.(model.RunEvent)
	if !ok {
		t.Fatalf("事件 %s 的 payload 不是 RunEvent: %T", ev.Type, ev.Payload)
	}
	return re
}

func mustRun(t *testing.T, store *memStore, id string) *model.Run {
	t.Helper()
	r, err := store.GetRun(context.Background(), id)
	if err != nil {
		t.Fatalf("读取运行 %s 失败: %v", id, err)
	}
	return r
}

func mustMessages(t *testing.T, store *memStore, sessionID string) []*model.Message {
	t.Helper()
	msgs, err := store.ListMessages(context.Background(), sessionID, 0, 0)
	if err != nil {
		t.Fatalf("读取消息失败: %v", err)
	}
	return msgs
}
