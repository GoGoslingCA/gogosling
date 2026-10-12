package permission

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/event"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

// memStore 是测试用的内存仓储，同时实现 model.ApprovalRepo 与 model.AuditRepo。
type memStore struct {
	mu        sync.Mutex
	approvals map[string]*model.ApprovalRequest
	order     []string
	audits    []*model.AuditEntry
	// failAudit / failCreate 用于注入基础设施故障。
	failAudit  error
	failCreate error
}

func newMemStore() *memStore {
	return &memStore{approvals: make(map[string]*model.ApprovalRequest)}
}

func cloneApproval(a *model.ApprovalRequest) *model.ApprovalRequest {
	cp := *a
	if a.ResolvedAt != nil {
		t := *a.ResolvedAt
		cp.ResolvedAt = &t
	}
	cp.Details = append(json.RawMessage(nil), a.Details...)
	return &cp
}

func (m *memStore) CreateApproval(_ context.Context, a *model.ApprovalRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failCreate != nil {
		return m.failCreate
	}
	if _, ok := m.approvals[a.ID]; ok {
		return model.Errorf(model.CodeConflict, "重复的审批 ID %s", a.ID)
	}
	m.approvals[a.ID] = cloneApproval(a)
	m.order = append(m.order, a.ID)
	return nil
}

func (m *memStore) GetApproval(_ context.Context, id string) (*model.ApprovalRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.approvals[id]
	if !ok {
		return nil, model.NotFound("approval", id)
	}
	return cloneApproval(a), nil
}

func (m *memStore) UpdateApproval(_ context.Context, a *model.ApprovalRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.approvals[a.ID]; !ok {
		return model.NotFound("approval", a.ID)
	}
	m.approvals[a.ID] = cloneApproval(a)
	return nil
}

func (m *memStore) ListApprovals(_ context.Context, status model.ApprovalStatus, p model.Page) ([]*model.ApprovalRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	limit := p.Limit
	if limit <= 0 {
		limit = 100
	}
	var all []*model.ApprovalRequest
	for _, id := range m.order {
		a := m.approvals[id]
		if status != "" && a.Status != status {
			continue
		}
		all = append(all, cloneApproval(a))
	}
	if p.Offset >= len(all) {
		return nil, nil
	}
	all = all[p.Offset:]
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

func (m *memStore) AppendAudit(_ context.Context, e *model.AuditEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failAudit != nil {
		return m.failAudit
	}
	cp := *e
	cp.Details = append(json.RawMessage(nil), e.Details...)
	m.audits = append(m.audits, &cp)
	return nil
}

func (m *memStore) ListAudit(_ context.Context, f model.AuditFilter, p model.Page) ([]*model.AuditEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*model.AuditEntry
	for i := len(m.audits) - 1; i >= 0; i-- {
		e := m.audits[i]
		if f.Action != "" && e.Action != f.Action {
			continue
		}
		if f.SessionID != "" && e.SessionID != f.SessionID {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// auditsOf 返回某动作的审计记录（按写入顺序）。
func (m *memStore) auditsOf(action string) []*model.AuditEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*model.AuditEntry
	for _, e := range m.audits {
		if e.Action == action {
			out = append(out, e)
		}
	}
	return out
}

func (m *memStore) setFailAudit(err error) {
	m.mu.Lock()
	m.failAudit = err
	m.mu.Unlock()
}

// fakeClock 是可手动推进的时钟。
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// testEnv 聚合一次测试需要的全部对象。
type testEnv struct {
	svc   *Service
	store *memStore
	bus   *event.Bus
	clock *fakeClock
	// dataDir 是数据目录；workspace 是其下的智能体工作区；outside 是数据目录之外的目录。
	dataDir   string
	workspace string
	outside   string
}

func newEnv(t *testing.T, mod func(*Deps)) *testEnv {
	t.Helper()
	store := newMemStore()
	bus := event.NewBus(1024)
	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	dataDir := t.TempDir()
	deps := Deps{
		Approvals: store,
		Audit:     store,
		Bus:       bus,
		RulesPath: DefaultRulesPath(dataDir),
		DataDir:   dataDir,
		Now:       clock.Now,
	}
	if mod != nil {
		mod(&deps)
	}
	svc, err := New(deps)
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	return &testEnv{
		svc:       svc,
		store:     store,
		bus:       bus,
		clock:     clock,
		dataDir:   dataDir,
		workspace: filepath.Join(dataDir, "agents", "a1"),
		outside:   t.TempDir(),
	}
}

// req 构造一条基础请求；details 为 nil 时不带 Details。
func (e *testEnv) req(tool string, risk model.ToolRisk, autonomy model.AutonomyLevel, details map[string]any) Request {
	r := Request{
		Principal: model.Principal{ID: "user-1", Kind: model.PrincipalLocalUser, Roles: []string{model.RoleAdmin}},
		AgentID:   "agent-1",
		Autonomy:  autonomy,
		SessionID: "sess-1",
		RunID:     "run-1",
		Tool:      model.ToolSpec{Name: tool, Risk: risk, Source: "builtin"},
		Call:      model.ToolCall{ID: "call-1", Name: tool, Arguments: json.RawMessage(`{}`)},
		Workspace: e.workspace,
	}
	if details != nil {
		raw, _ := json.Marshal(details)
		r.Details = raw
	}
	return r
}

type checkResult struct {
	v   Verdict
	err error
}

// checkAsync 在后台执行 Check。
func (e *testEnv) checkAsync(ctx context.Context, r Request) <-chan checkResult {
	ch := make(chan checkResult, 1)
	go func() {
		v, err := e.svc.Check(ctx, r)
		ch <- checkResult{v: v, err: err}
	}()
	return ch
}

// subscribe 订阅审批事件。
func (e *testEnv) subscribe(ctx context.Context, buffer int) *event.Subscription {
	return e.bus.Subscribe(ctx, 0, buffer, func(ev event.Event) bool {
		return ev.Type == string(model.EventApprovalRequested) || ev.Type == string(model.EventApprovalResolved)
	})
}

// waitEvent 等待指定类型的下一条事件并返回其中的审批请求。
func waitEvent(t *testing.T, sub *event.Subscription, typ model.RunEventType) *model.ApprovalRequest {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-sub.C:
			if !ok {
				t.Fatalf("订阅已关闭，未等到 %s", typ)
			}
			if ev.Type != string(typ) {
				continue
			}
			re, ok := ev.Payload.(model.RunEvent)
			if !ok {
				t.Fatalf("事件载荷不是 model.RunEvent: %T", ev.Payload)
			}
			if re.Approval == nil {
				t.Fatalf("%s 事件没有携带审批请求", typ)
			}
			return re.Approval
		case <-timeout:
			t.Fatalf("等待 %s 事件超时", typ)
		}
	}
}

// waitResult 等待 Check 返回。
func waitResult(t *testing.T, ch <-chan checkResult) checkResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("等待 Check 返回超时")
		return checkResult{}
	}
}

func detailsOf(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	m := map[string]any{}
	if len(raw) == 0 {
		return m
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("Details 不是合法 JSON: %v", err)
	}
	return m
}
