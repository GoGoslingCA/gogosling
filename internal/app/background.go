package app

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/agent"
	"github.com/GoGoslingCA/gogosling/internal/engine"
	"github.com/GoGoslingCA/gogosling/internal/event"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/session"
)

// EventWorkerStatus 是工作进程状态变化事件的类型（OpenAPI Event 已列出）。
const EventWorkerStatus = "worker.status"

// WorkerStatus 是 worker.status 事件的载荷，形状与 OpenAPI VersionInfo.worker 一致。
type WorkerStatus struct {
	Status  string `json:"status"`
	Version string `json:"version,omitempty"`
	Python  string `json:"python,omitempty"`
}

// statusSource 是 *engine.Supervisor 的状态查询能力。
type statusSource interface {
	Status() engine.Status
}

// defaultWorkerStatusInterval 是轮询工作进程状态的间隔。
const defaultWorkerStatusInterval = 2 * time.Second

// watchWorkerStatus 周期读取引擎状态，状态、版本或 Python 版本变化时发布 worker.status；
// 第一次读取总会发布一次，让刚连上的客户端不必等待。ctx 结束时返回。
func watchWorkerStatus(ctx context.Context, src statusSource, bus *event.Bus, interval time.Duration) {
	if interval <= 0 {
		interval = defaultWorkerStatusInterval
	}
	var last *WorkerStatus
	check := func() {
		st := src.Status()
		cur := WorkerStatus{Status: string(st.State), Version: st.Version, Python: st.Python}
		if last != nil && *last == cur {
			return
		}
		last = &cur
		bus.Publish(event.Event{Type: EventWorkerStatus, Payload: cur})
	}
	check()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			check()
		}
	}
}

// followBus 从 since 之后订阅总线，对每条匹配 filter 的事件调用 handle，直到 ctx 结束。
//
// 订阅因慢消费被总线关闭时从当前位置重新订阅：回放缓冲区已覆盖 since 时 Subscribe 会立即关闭，
// 继续用旧位置只会空转，因此最多漏掉溢出期间的事件（记录警告）。handle 在同一个 goroutine 中按 Seq 顺序调用。
func followBus(ctx context.Context, bus *event.Bus, since uint64, name string, filter event.Filter, handle func(event.Event), logger *slog.Logger) {
	for ctx.Err() == nil {
		sub := bus.Subscribe(ctx, since, 256, filter)
		for ev := range sub.C {
			since = ev.Seq
			handle(ev)
		}
		if ctx.Err() == nil && sub.Lost() {
			logger.Warn("事件订阅溢出，从当前位置重新订阅；期间的事件可能被漏掉", "subscriber", name, "since", since)
			since = bus.LastSeq()
		}
	}
}

// grantRevoker 是 *permission.Service 撤销会话授权的能力。
type grantRevoker interface {
	RevokeSession(sessionID string)
}

// revokeGrantsOnSessionEnd 订阅会话生命周期事件：会话被删除或归档时撤销它的"本会话内允许"授权，
// 使授权的生命周期不超过会话本身（溢出时最多漏掉一次撤销；授权仍会随守护进程重启消失）。
func revokeGrantsOnSessionEnd(ctx context.Context, bus *event.Bus, since uint64, perms grantRevoker, logger *slog.Logger) {
	filter := func(ev event.Event) bool {
		return ev.Type == session.EventSessionDeleted || ev.Type == session.EventSessionUpdated
	}
	followBus(ctx, bus, since, "revoke_grants", filter, func(ev event.Event) {
		sess, ok := sessionOfPayload(ev.Payload)
		if !ok {
			return
		}
		if ev.Type == session.EventSessionDeleted || sess.Status == model.SessionArchived {
			perms.RevokeSession(sess.ID)
		}
	}, logger)
}

// sessionOfPayload 取出 session.* 事件载荷中的会话（session 包发布 session.View，兼容 model.Session）。
func sessionOfPayload(p any) (model.Session, bool) {
	switch v := p.(type) {
	case session.View:
		return v.Session, true
	case *session.View:
		if v != nil {
			return v.Session, true
		}
	case model.Session:
		return v, true
	case *model.Session:
		if v != nil {
			return *v, true
		}
	}
	return model.Session{}, false
}

// approvalNoter 是 *session.Service 记录审批等待状态的能力。
type approvalNoter interface {
	NoteApproval(runID string, waiting bool) error
}

// noteApprovals 把总线上带 run_id 的 approval.requested / approval.resolved 交给 session.NoteApproval，
// 让运行状态在 running 与 awaiting_approval 之间推进（approval.* 由 permission 直接发布到总线，
// 不经过运行时回调；session 只改状态并落库，不重复发布）。
func noteApprovals(ctx context.Context, bus *event.Bus, since uint64, sessions approvalNoter, logger *slog.Logger) {
	filter := func(ev event.Event) bool {
		if ev.RunID == "" {
			return false
		}
		t := model.RunEventType(ev.Type)
		return t == model.EventApprovalRequested || t == model.EventApprovalResolved
	}
	followBus(ctx, bus, since, "approval_notes", filter, func(ev event.Event) {
		waiting := model.RunEventType(ev.Type) == model.EventApprovalRequested
		if err := sessions.NoteApproval(ev.RunID, waiting); err != nil {
			level := slog.LevelWarn
			if model.IsCode(err, model.CodeNotFound) {
				level = slog.LevelDebug // 不属于任何运行的审批（例如测试或外部调用方直接发起）
			}
			logger.Log(ctx, level, "记录运行的审批等待状态失败", "run_id", ev.RunID, "waiting", waiting, "error", err)
		}
	}, logger)
}

// agentForgetter 是删除智能体后需要清理内存状态的模块（调度器的任务表、网关的通道适配器与回复路由）。
type agentForgetter interface {
	ForgetAgent(ctx context.Context, agentID string) error
}

// schedulerForgetter 把 *scheduler.Service 的 Forget 接成 agentForgetter。
type schedulerForgetter struct {
	s interface{ Forget(agentID string) }
}

func (f schedulerForgetter) ForgetAgent(_ context.Context, agentID string) error {
	f.s.Forget(agentID)
	return nil
}

// forgetTimeout 是单个模块清理一个已删除智能体的时间上限（网关可能要停止适配器、发完排队的回复）。
const forgetTimeout = 10 * time.Second

// forgetDeletedAgents 订阅 agent.deleted：数据库中该智能体的任务与通道已由外键级联删除，
// 这里让调度器与网关清理内存中的对应状态，避免已删除的任务继续触发、已删除通道的适配器继续收消息。
func forgetDeletedAgents(ctx context.Context, bus *event.Bus, since uint64, targets []agentForgetter, logger *slog.Logger) {
	filter := func(ev event.Event) bool { return ev.Type == agent.EventDeleted }
	followBus(ctx, bus, since, "agent_deleted", filter, func(ev event.Event) {
		agentID := ev.AgentID
		if agentID == "" {
			if a, ok := ev.Payload.(*model.Agent); ok && a != nil {
				agentID = a.ID
			}
		}
		if agentID == "" {
			return
		}
		for _, t := range targets {
			fctx, cancel := context.WithTimeout(ctx, forgetTimeout)
			if err := t.ForgetAgent(fctx, agentID); err != nil {
				logger.Warn("清理已删除智能体的内存状态失败", "agent_id", agentID, "error", err)
			}
			cancel()
		}
	}, logger)
}

// sessionCleaner 实现 agent.SessionCleaner：删除智能体前先由 session 删除它的会话（检查在途运行、
// 逐个发布 session.deleted）。agent.Service 先于 session.Service 构造（后者需要读取智能体），
// 因此会话服务在建好后经 bind 注入；绑定之前的调用返回 CodeUnavailable（此时本地 API 尚未监听）。
type sessionCleaner struct {
	mu sync.RWMutex
	s  agent.SessionCleaner
}

var _ agent.SessionCleaner = (*sessionCleaner)(nil)

// bind 注入会话服务。
func (c *sessionCleaner) bind(s agent.SessionCleaner) {
	c.mu.Lock()
	c.s = s
	c.mu.Unlock()
}

// DeleteByAgent 转交给已绑定的会话服务。
func (c *sessionCleaner) DeleteByAgent(ctx context.Context, agentID string) error {
	c.mu.RLock()
	s := c.s
	c.mu.RUnlock()
	if s == nil {
		return model.NewError(model.CodeUnavailable, "会话服务尚未就绪")
	}
	return s.DeleteByAgent(ctx, agentID)
}
