package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/event"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

// fakeChannels 是内存版 model.ChannelRepo，记录每次 UpdateChannel。
type fakeChannels struct {
	mu      sync.Mutex
	items   map[string]*model.Channel
	updates int
	listErr error
}

func newFakeChannels() *fakeChannels {
	return &fakeChannels{items: make(map[string]*model.Channel)}
}

func (f *fakeChannels) CreateChannel(_ context.Context, c *model.Channel) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.items[c.ID]; ok {
		return model.NewError(model.CodeConflict, "channel 已存在")
	}
	cp := *c
	f.items[c.ID] = &cp
	return nil
}

func (f *fakeChannels) GetChannel(_ context.Context, id string) (*model.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.items[id]
	if !ok {
		return nil, model.NotFound("channel", id)
	}
	cp := *c
	return &cp, nil
}

func (f *fakeChannels) ListChannels(_ context.Context, agentID string) ([]*model.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []*model.Channel
	for _, c := range f.items {
		if agentID == "" || c.AgentID == agentID {
			cp := *c
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (f *fakeChannels) UpdateChannel(_ context.Context, c *model.Channel) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.items[c.ID]; !ok {
		return model.NotFound("channel", c.ID)
	}
	cp := *c
	f.items[c.ID] = &cp
	f.updates++
	return nil
}

func (f *fakeChannels) DeleteChannel(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.items, id)
	return nil
}

// get 返回通道副本（不存在时返回 nil）。
func (f *fakeChannels) get(id string) *model.Channel {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.items[id]
	if !ok {
		return nil
	}
	cp := *c
	return &cp
}

// fakeSender 记录 Send 请求并按路由键维护会话。
type fakeSender struct {
	mu        sync.Mutex
	sessions  map[string]*model.Session
	requests  []SendRequest
	runs      []*model.Run
	seq       int
	sendErr   error
	ensureErr error
}

func newFakeSender() *fakeSender {
	return &fakeSender{sessions: make(map[string]*model.Session)}
}

func (f *fakeSender) EnsureByKey(_ context.Context, agentID, key string, origin model.Origin) (*model.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ensureErr != nil {
		return nil, f.ensureErr
	}
	if origin != model.OriginChannel {
		return nil, model.Errorf(model.CodeInvalid, "来源应为 channel，得到 %s", origin)
	}
	if !strings.HasPrefix(key, agentID+":"+string(origin)+":") {
		return nil, model.Errorf(model.CodeInvalid, "路由键 %q 与智能体 %q 不匹配", key, agentID)
	}
	if s, ok := f.sessions[key]; ok {
		cp := *s
		return &cp, nil
	}
	f.seq++
	s := &model.Session{ID: fmt.Sprintf("S%d", f.seq), AgentID: agentID, Origin: origin, Key: key, Status: model.SessionActive}
	f.sessions[key] = s
	cp := *s
	return &cp, nil
}

func (f *fakeSender) Send(_ context.Context, req SendRequest) (*model.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return nil, f.sendErr
	}
	f.requests = append(f.requests, req)
	f.seq++
	run := &model.Run{ID: fmt.Sprintf("R%d", f.seq), SessionID: req.SessionID, Status: model.RunQueued, Trigger: req.Trigger}
	f.runs = append(f.runs, run)
	cp := *run
	return &cp, nil
}

func (f *fakeSender) sent() []SendRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]SendRequest(nil), f.requests...)
}

func (f *fakeSender) lastRun() *model.Run {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.runs) == 0 {
		return nil
	}
	cp := *f.runs[len(f.runs)-1]
	return &cp
}

func (f *fakeSender) session(key string) *model.Session {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.sessions[key]
	if s == nil {
		return nil
	}
	cp := *s
	return &cp
}

func (f *fakeSender) setSendErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendErr = err
}

// fakeSecrets 是内存凭据存储。
type fakeSecrets map[string]string

func (f fakeSecrets) Get(_ context.Context, name string) (string, error) {
	v, ok := f[name]
	if !ok {
		return "", model.NotFound("secret", name)
	}
	return v, nil
}

// failingAdapter 的 Start 总是失败，用于验证通道隔离。
type failingAdapter struct {
	kind model.ChannelKind
	err  error
}

func (a *failingAdapter) Kind() model.ChannelKind { return a.kind }
func (a *failingAdapter) Start(context.Context, *model.Channel, string, func(Inbound)) error {
	return a.err
}
func (a *failingAdapter) Send(context.Context, Outbound) error { return nil }
func (a *failingAdapter) Stop(context.Context) error           { return nil }

// gatedAdapter 的 Send 会阻塞到 gate 关闭（或 ctx 结束），用于验证停止时的排空行为。
type gatedAdapter struct {
	*ConsoleAdapter
	entered chan struct{}
	gate    chan struct{}
}

func (g *gatedAdapter) Kind() model.ChannelKind { return model.ChannelDiscord }

func (g *gatedAdapter) Send(ctx context.Context, out Outbound) error {
	select {
	case g.entered <- struct{}{}:
	default:
	}
	select {
	case <-g.gate:
	case <-ctx.Done():
		return model.Wrap(model.CodeCancelled, "gated: 发送被取消", ctx.Err())
	}
	return g.ConsoleAdapter.Send(ctx, out)
}

// clock 是可拨动的测试时钟。
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// harness 组装一个带假依赖的 Gateway。
type harness struct {
	t        *testing.T
	channels *fakeChannels
	sender   *fakeSender
	secrets  fakeSecrets
	bus      *event.Bus
	factory  *Factory
	clock    *clock
	gw       *Gateway
}

func newHarness(t *testing.T, opts Options) *harness {
	t.Helper()
	h := &harness{
		t:        t,
		channels: newFakeChannels(),
		sender:   newFakeSender(),
		secrets:  fakeSecrets{},
		bus:      event.NewBus(1024),
		factory:  NewFactory(),
		clock:    &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	}
	h.factory.Register(model.ChannelConsole, func() Adapter { return NewConsole() })
	if opts.Now == nil {
		opts.Now = h.clock.now
	}
	h.gw = New(h.channels, h.sender, h.secrets, h.bus, h.factory, nil, opts)
	return h
}

// addChannel 登记一个通道；config 为 JSON 文本，可为空。
func (h *harness) addChannel(id, agentID string, kind model.ChannelKind, config string, enabled bool) *model.Channel {
	h.t.Helper()
	ch := &model.Channel{ID: id, AgentID: agentID, Kind: kind, Name: id, Enabled: enabled, CreatedAt: h.clock.now(), UpdatedAt: h.clock.now()}
	if config != "" {
		ch.Config = json.RawMessage(config)
	}
	if err := h.channels.CreateChannel(context.Background(), ch); err != nil {
		h.t.Fatalf("登记通道失败: %v", err)
	}
	return ch
}

// start 启动网关并在测试结束时停止。
func (h *harness) start() {
	h.t.Helper()
	if err := h.gw.Start(context.Background()); err != nil {
		h.t.Fatalf("启动网关失败: %v", err)
	}
	h.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = h.gw.Stop(ctx)
	})
}

// console 返回通道当前的控制台适配器。
func (h *harness) console(channelID string) *ConsoleAdapter {
	h.t.Helper()
	a, ok := h.gw.Adapter(channelID)
	if !ok {
		h.t.Fatalf("通道 %s 未启动", channelID)
	}
	c, ok := a.(*ConsoleAdapter)
	if !ok {
		h.t.Fatalf("通道 %s 的适配器不是 console，而是 %T", channelID, a)
	}
	return c
}

// inject 注入一条入站消息。
func (h *harness) inject(channelID string, in Inbound) {
	h.t.Helper()
	if err := h.console(channelID).Inject(in); err != nil {
		h.t.Fatalf("注入消息失败: %v", err)
	}
}

// publishRun 像 session 包那样把运行事件发布到总线。
func (h *harness) publishRun(ev model.RunEvent) {
	if ev.At.IsZero() {
		ev.At = h.clock.now()
	}
	h.bus.Publish(event.Event{Type: string(ev.Type), At: ev.At, AgentID: ev.AgentID, SessionID: ev.SessionID, RunID: ev.RunID, Payload: ev})
}

// waitFor 轮询直到条件成立或超时。
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待超时: %s", what)
}

// settle 给异步路径一点时间，用于断言"没有发生"。
func settle() { time.Sleep(50 * time.Millisecond) }

// getterSender 在 fakeSender 之上实现 SessionGetter，并记录每次 Get。
type getterSender struct {
	*fakeSender
	gmu  sync.Mutex
	gets []string
}

func (g *getterSender) Get(_ context.Context, id string) (*model.Session, error) {
	g.gmu.Lock()
	g.gets = append(g.gets, id)
	g.gmu.Unlock()
	g.fakeSender.mu.Lock()
	defer g.fakeSender.mu.Unlock()
	for _, s := range g.fakeSender.sessions {
		if s.ID == id {
			cp := *s
			return &cp, nil
		}
	}
	return nil, model.NotFound("session", id)
}

func (g *getterSender) getCount() int {
	g.gmu.Lock()
	defer g.gmu.Unlock()
	return len(g.gets)
}

// withSessionGetter 让网关的 Sender 同时实现 SessionGetter（须在 start 之前调用）。
func (h *harness) withSessionGetter() *getterSender {
	h.t.Helper()
	gs := &getterSender{fakeSender: h.sender}
	h.gw = New(h.channels, gs, h.secrets, h.bus, h.factory, nil, h.gw.opts)
	return gs
}

// seedSession 直接登记一个会话（模拟守护进程重启前创建、或由定时任务写入的会话）。
func (h *harness) seedSession(id, agentID, key string, origin model.Origin) *model.Session {
	h.sender.mu.Lock()
	defer h.sender.mu.Unlock()
	s := &model.Session{ID: id, AgentID: agentID, Origin: origin, Key: key, Status: model.SessionActive}
	h.sender.sessions[key] = s
	cp := *s
	return &cp
}

// publishPushed 像 session.Push 那样发布一条推送消息的 message.created。
func (h *harness) publishPushed(sessionID, agentID, text string) {
	m := model.TextMessage(sessionID, model.RoleAssistant, text)
	m.Meta = json.RawMessage(`{"pushed":true}`)
	h.publishRun(model.RunEvent{Type: model.EventMessageCreated, SessionID: sessionID, AgentID: agentID, Message: &m})
}
