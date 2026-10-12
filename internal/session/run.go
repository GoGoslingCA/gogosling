package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/i18n"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

// 运行失败原因（Run.Error）的机器可读取值；传输层可按它们映射本地化文案。
const (
	// ErrorDaemonRestarted 表示运行因守护进程重启而丢失。
	ErrorDaemonRestarted = "daemon restarted"
	// ErrorRunTimeout 表示运行超过 Options.RunTimeout。
	ErrorRunTimeout = "run timeout"
	// ErrorPersistFailed 表示运行结果无法写入数据库。
	ErrorPersistFailed = "persist failed"
	// ErrorRunFailed 是运行时未提供原因时的占位。
	ErrorRunFailed = "run failed"
)

// 消息 Meta 中由本包写入的键。
const (
	// MetaTrigger 记录触发运行的入口（model.RunTrigger）。
	MetaTrigger = "trigger"
	// MetaPrincipalID / MetaPrincipalKind 记录发送者身份。
	MetaPrincipalID   = "principal_id"
	MetaPrincipalKind = "principal_kind"
	// MetaUntrusted 为 true 表示输入来自不可信来源，权限规则可据此收紧。
	MetaUntrusted = "untrusted"
	// MetaPartial 为 true 表示该助手消息是运行失败或取消时保留的部分输出。
	MetaPartial = "partial"
)

// cancelGrace 是超时或关闭时向运行时发送取消请求的等待上限。
const cancelGrace = 5 * time.Second

// SendRequest 是一次用户输入。
type SendRequest struct {
	SessionID   string
	Text        string
	Attachments []model.Attachment
	// MaterialSources 是服务器校验后的材料 ID 与摘要，作为非推理元数据持久化。
	MaterialSources json.RawMessage
	// Trigger 为空时视为 TriggerUser。
	Trigger model.RunTrigger
	// Principal 是发送者身份，写入消息 Meta 便于审计与展示。
	Principal model.Principal
	// Untrusted 标记输入来自不可信来源（IM 消息、网页内容等），写入消息 Meta 供权限规则收紧。
	Untrusted bool
}

// activeRun 是一个在途运行的进程内状态。
type activeRun struct {
	id        string
	sessionID string
	agent     *model.Agent
	group     string
	trigger   model.RunTrigger
	// ctx 覆盖整个运行；cancel 在运行终结或服务关闭时调用。
	ctx    context.Context
	cancel context.CancelFunc
	// done 在运行终结（或启动失败释放）时关闭，供超时监视 goroutine 退出。
	done     chan struct{}
	doneOnce sync.Once

	// mu 保护下面的字段；运行时的事件回调与本包的取消/超时路径都经由它串行化。
	mu              sync.Mutex
	run             model.Run
	text            strings.Builder // 累积的 text.delta，用于失败或取消时保留部分输出
	usage           model.Usage     // 累积的 usage 事件
	terminal        bool
	cancelRequested bool
}

// snapshot 返回运行记录的拷贝。
func (ar *activeRun) snapshot() *model.Run {
	ar.mu.Lock()
	defer ar.mu.Unlock()
	r := ar.run
	return &r
}

// Send 追加用户消息并发起一次运行，返回新建的运行记录（queued 或更晚的状态）。
//
// 前置条件：会话 active、智能体 enabled、同会话没有未结束的运行（否则 CodeConflict）。
// 凭据、工具与系统提示在写入任何记录之前解析：配置错误直接返回，用户修正后重发不会留下重复消息。
// 用户消息与运行记录在同一事务内写入；之后发布 message.created，再把 RunRequest 交给 Runtime。
// 一旦记录已写入，后续错误都会把运行标记为 failed 并发布 run.failed；此时第一个返回值是失败的运行，
// 第二个返回值是原因。记录写入之后只使用运行自己的上下文，调用方的 ctx 结束不再影响运行；
// 运行在交给 Runtime 之前就被取消时不再启动，返回 cancelled 的记录与 nil。
func (s *Service) Send(ctx context.Context, req SendRequest) (*model.Run, error) {
	if req.SessionID == "" {
		return nil, model.NewError(model.CodeInvalid, "缺少会话 ID")
	}
	if strings.TrimSpace(req.Text) == "" && len(req.Attachments) == 0 {
		return nil, model.NewError(model.CodeInvalid, "消息内容为空")
	}
	trigger := req.Trigger
	if trigger == "" {
		trigger = model.TriggerUser
	}

	sess, err := s.store.GetSession(ctx, req.SessionID)
	if err != nil {
		return nil, err
	}
	if sess.Status != model.SessionActive {
		return nil, &model.Error{
			Code:    model.CodeConflict,
			Message: "会话已归档",
			Details: map[string]any{"session_id": sess.ID},
		}
	}
	agent, err := s.agents.GetAgent(ctx, sess.AgentID)
	if err != nil {
		return nil, err
	}
	if !agent.Enabled {
		return nil, &model.Error{
			Code:    model.CodeConflict,
			Message: "智能体已停用",
			Details: map[string]any{"agent_id": agent.ID},
		}
	}

	ar, err := s.reserve(ctx, sess, agent, trigger)
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			s.release(ar)
		}
	}()

	_, err = s.creds.Credentials(ctx, agent.Model)
	if err != nil {
		return nil, fmt.Errorf("解析模型凭据: %w", err)
	}
	_, err = s.tools.ToolsFor(ctx, agent)
	if err != nil {
		return nil, fmt.Errorf("解析可用工具: %w", err)
	}
	_, err = s.systemPrompt(ctx, agent, sess)
	if err != nil {
		return nil, fmt.Errorf("生成系统提示: %w", err)
	}

	now := s.now()
	userMsg := &model.Message{
		ID:        model.NewID(),
		SessionID: sess.ID,
		Role:      model.RoleUser,
		RunID:     ar.id,
		Parts:     userParts(req.Text, req.Attachments),
		Meta:      userMeta(trigger, req.Principal, req.Untrusted),
		CreatedAt: now,
	}
	if len(req.MaterialSources) > 0 {
		var metadata map[string]any
		_ = json.Unmarshal(userMsg.Meta, &metadata)
		var sources []map[string]any
		if err := json.Unmarshal(req.MaterialSources, &sources); err != nil {
			return nil, model.NewError(model.CodeInvalid, i18n.T("en", "workbench.invalid_fields", nil))
		}
		metadata["material_sources"] = sources
		userMsg.Meta = mustJSON(metadata)
	}
	run := ar.snapshot()
	run.InputMessageID = userMsg.ID
	titled := false
	err = s.store.Tx(ctx, func(tx model.Store) error {
		// 项目关联可能在文件校验之后变化，消息提交前必须再次核实所选资料的归属。
		if len(req.MaterialSources) > 0 {
			var selected []struct {
				ID        string `json:"id"`
				ProjectID string `json:"project_id"`
				AgentID   string `json:"agent_id"`
				SHA256    string `json:"sha256"`
			}
			if err := json.Unmarshal(req.MaterialSources, &selected); err != nil {
				return model.NewError(model.CodeInvalid, i18n.T("en", "workbench.invalid_fields", nil))
			}
			current, err := tx.GetSession(ctx, sess.ID)
			if err != nil {
				return err
			}
			projects, ok := tx.(model.ProjectRepo)
			if !ok {
				return model.NewError(model.CodeUnavailable, i18n.T("en", "workbench.unavailable", nil))
			}
			for _, source := range selected {
				material, err := projects.GetProjectMaterial(ctx, source.ID)
				if err != nil {
					return err
				}
				if current.ProjectID == "" || source.ProjectID != current.ProjectID || source.AgentID != current.AgentID || material.ProjectID != current.ProjectID || material.AgentID != current.AgentID || material.SHA256 != source.SHA256 {
					return &model.Error{Code: model.CodeConflict, Message: i18n.T("en", "workbench.scope_mismatch", nil), Details: map[string]any{"reason": "scope_mismatch"}}
				}
			}
		}
		if err := tx.AppendMessage(ctx, userMsg); err != nil {
			return fmt.Errorf("追加用户消息: %w", err)
		}
		if err := tx.CreateRun(ctx, run); err != nil {
			return fmt.Errorf("创建运行记录: %w", err)
		}
		if sess.Title == "" {
			if t := autoTitle(req.Text); t != "" {
				sess.Title = t
				sess.UpdatedAt = now
				if err := tx.UpdateSession(ctx, sess); err != nil {
					return fmt.Errorf("设置会话标题: %w", err)
				}
				titled = true
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	committed = true
	ar.mu.Lock()
	ar.run.InputMessageID = userMsg.ID
	ar.mu.Unlock()

	s.publishRun(model.RunEvent{
		Type: model.EventMessageCreated, RunID: ar.id, SessionID: sess.ID, AgentID: agent.ID, At: now, Message: userMsg,
	})
	if titled {
		s.publishSession(EventSessionUpdated, sess)
	}

	// 记录已经落盘：此后的步骤只用运行自己的上下文（reserve 中由 WithoutCancel 派生，随运行结束而取消），
	// 发起请求的客户端断开不能让已受理的运行失败。
	queuedSessionID, queuedInputSeq := sess.ID, userMsg.Seq
	launch := func() error { return s.startAdmittedRun(ar, queuedSessionID, queuedInputSeq) }
	immediate, err := s.admit(ar, launch)
	if err != nil {
		s.finishRun(ar, model.RunCancelled, "")
		return ar.snapshot(), nil
	}
	if immediate {
		if err = launch(); err != nil {
			return ar.snapshot(), fmt.Errorf("start run: %w", err)
		}
	}
	return ar.snapshot(), nil
}

// Cancel 取消运行。幂等：已是终态时直接返回当前记录；在途时转发给 Runtime，终态由随后的
// run.cancelled 事件确认。运行时不认识该运行（CodeNotFound）时在本地标记为 cancelled。
func (s *Service) Cancel(ctx context.Context, runID string) (*model.Run, error) {
	ar := s.lookup(runID)
	if ar == nil {
		run, err := s.store.GetRun(ctx, runID)
		if err != nil {
			return nil, err
		}
		if run.Status.Terminal() {
			return run, nil
		}
		// 数据库里非终态但本进程不认识：是上次进程遗留且尚未恢复的记录，没有任何东西在执行它。
		now := s.now()
		run.Status = model.RunCancelled
		run.EndedAt = &now
		if err := s.store.UpdateRun(ctx, run); err != nil {
			return nil, fmt.Errorf("更新运行状态: %w", err)
		}
		s.publishRun(model.RunEvent{Type: model.EventRunCancelled, RunID: run.ID, SessionID: run.SessionID, AgentID: run.AgentID, At: now})
		return run, nil
	}

	ar.mu.Lock()
	if ar.terminal {
		r := ar.run
		ar.mu.Unlock()
		return &r, nil
	}
	ar.cancelRequested = true
	ar.mu.Unlock()
	s.mu.Lock()
	waiting := !s.slots[runID]
	s.mu.Unlock()
	if waiting {
		s.finishRun(ar, model.RunCancelled, "")
		return ar.snapshot(), nil
	}

	err := s.runtime.CancelRun(ctx, runID)
	switch {
	case err == nil:
	case model.IsCode(err, model.CodeNotFound):
		s.finishRun(ar, model.RunCancelled, "")
	default:
		return ar.snapshot(), fmt.Errorf("取消运行: %w", err)
	}
	return ar.snapshot(), nil
}

// GetRun 返回运行记录；在途运行返回进程内的最新状态。
func (s *Service) GetRun(ctx context.Context, runID string) (*model.Run, error) {
	if ar := s.lookup(runID); ar != nil {
		return ar.snapshot(), nil
	}
	return s.store.GetRun(ctx, runID)
}

// ActiveRunID 返回会话当前在途运行的 ID；没有时 ok 为 false。
func (s *Service) ActiveRunID(sessionID string) (runID string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	runID, ok = s.bySession[sessionID]
	return runID, ok
}

// RecoverActiveRuns 在守护进程启动时调用：把数据库中所有非终态、且本进程不认识的运行标记为 failed
// （原因 ErrorDaemonRestarted）并发布 run.failed。返回处理的条数；个别更新失败不影响其余记录。
func (s *Service) RecoverActiveRuns(ctx context.Context) (int, error) {
	runs, err := s.store.ListActiveRuns(ctx)
	if err != nil {
		return 0, fmt.Errorf("列出未结束的运行: %w", err)
	}
	var errs []error
	n := 0
	for _, r := range runs {
		if s.lookup(r.ID) != nil {
			continue
		}
		now := s.now()
		r.Status = model.RunFailed
		r.Error = ErrorDaemonRestarted
		r.EndedAt = &now
		if err := s.store.UpdateRun(ctx, r); err != nil {
			errs = append(errs, fmt.Errorf("标记运行 %s 失败: %w", r.ID, err))
			continue
		}
		n++
		s.publishRun(model.RunEvent{
			Type: model.EventRunFailed, RunID: r.ID, SessionID: r.SessionID, AgentID: r.AgentID, At: now, Error: ErrorDaemonRestarted,
		})
	}
	return n, errors.Join(errs...)
}

// Close 停止接受新的 Send，并取消所有在途运行的上下文；运行时随后发出的终态事件仍会被正常处理。
// 来不及收到终态的运行留给下次启动的 RecoverActiveRuns 修正。
func (s *Service) Close() {
	s.mu.Lock()
	s.closed = true
	ars := make([]*activeRun, 0, len(s.active))
	for _, ar := range s.active {
		ars = append(ars, ar)
	}
	promptCancels := make([]context.CancelFunc, 0, len(s.promptRuns))
	for _, cancel := range s.promptRuns {
		promptCancels = append(promptCancels, cancel)
	}
	s.mu.Unlock()
	for _, cancel := range promptCancels {
		cancel()
	}
	for _, ar := range ars {
		ar.cancel()
		s.mu.Lock()
		waiting := !s.slots[ar.id]
		s.mu.Unlock()
		if waiting {
			s.finishRun(ar, model.RunCancelled, "")
		}
	}
}

// reserve 为会话登记一个在途运行；会话已有运行或智能体正在被删除时返回 CodeConflict。
func (s *Service) reserve(ctx context.Context, sess *model.Session, agent *model.Agent, trigger model.RunTrigger) (*activeRun, error) {
	group := agent.ID
	if s.opts.WorkspaceGroup != nil {
		next, err := s.opts.WorkspaceGroup(ctx, agent)
		if err != nil {
			return nil, err
		}
		if next != "" {
			group = next
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, model.NewError(model.CodeUnavailable, "会话服务已关闭")
	}
	// 尚在配置预检的记录也计入等待容量，避免同一工作区排队时突破上限。
	if s.maxParallel > 0 && (len(s.active) >= s.maxParallel+s.maxQueued || len(s.active)-len(s.slots) >= s.maxQueued) {
		return nil, queueFullError()
	}
	if _, deleting := s.deleting[agent.ID]; deleting {
		return nil, &model.Error{
			Code:    model.CodeConflict,
			Message: "智能体正在删除",
			Details: map[string]any{"agent_id": agent.ID},
		}
	}
	if runID, busy := s.bySession[sess.ID]; busy {
		return nil, &model.Error{
			Code:    model.CodeConflict,
			Message: "会话已有未结束的运行",
			Details: map[string]any{"run_id": runID},
		}
	}
	id := model.NewID()
	// 运行要活得比触发它的请求（HTTP、IM 回调）更久，所以脱离调用方的取消，只保留它的值。
	base := context.WithoutCancel(ctx)
	var (
		rctx   context.Context
		cancel context.CancelFunc
	)
	if s.opts.RunTimeout > 0 {
		rctx, cancel = context.WithTimeout(base, s.opts.RunTimeout)
	} else {
		rctx, cancel = context.WithCancel(base)
	}
	ar := &activeRun{
		id:        id,
		sessionID: sess.ID,
		agent:     &model.Agent{ID: agent.ID},
		group:     group,
		trigger:   trigger,
		ctx:       rctx,
		cancel:    cancel,
		done:      make(chan struct{}),
		run: model.Run{
			ID:        id,
			SessionID: sess.ID,
			AgentID:   agent.ID,
			Status:    model.RunQueued,
			Trigger:   trigger,
			CreatedAt: s.now(),
		},
	}
	s.active[id] = ar
	s.bySession[sess.ID] = id
	if s.opts.RunTimeout > 0 {
		go s.watchTimeout(ar)
	}
	return ar, nil
}

// release 从运行表移除运行并结束它的上下文；可重复调用。
func (s *Service) release(ar *activeRun) {
	s.mu.Lock()
	delete(s.active, ar.id)
	if s.bySession[ar.sessionID] == ar.id {
		delete(s.bySession, ar.sessionID)
	}
	if s.slots[ar.id] {
		delete(s.slots, ar.id)
		s.groups[ar.group]--
		if s.groups[ar.group] <= 0 {
			delete(s.groups, ar.group)
		}
	}
	for i := 0; i < len(s.queue); {
		if s.queue[i].run == ar {
			copy(s.queue[i:], s.queue[i+1:])
			s.queue[len(s.queue)-1] = admittedRun{}
			s.queue = s.queue[:len(s.queue)-1]
		} else {
			i++
		}
	}
	ready := s.takeReadyLocked()
	s.mu.Unlock()
	ar.doneOnce.Do(func() { close(ar.done) })
	ar.cancel()
	s.launchQueued(ready)
}

// lookup 返回在途运行；不存在时为 nil。
func (s *Service) lookup(runID string) *activeRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active[runID]
}

// cancelPending 报告是否收到过取消请求。
func (ar *activeRun) cancelPending() bool {
	ar.mu.Lock()
	defer ar.mu.Unlock()
	return ar.cancelRequested
}

// endedOrCancelling 报告运行是否已在本地终结或已被请求取消。
func (ar *activeRun) endedOrCancelling() bool {
	ar.mu.Lock()
	defer ar.mu.Unlock()
	return ar.terminal || ar.cancelRequested
}

// NoteApproval 记录运行进入或离开审批等待：waiting 为 true 时 running → awaiting_approval，
// 为 false 时 awaiting_approval → running；其他状态下不做任何改变。
//
// approval.* 事件由 permission 包直接发布到总线（工作进程不发送，它们不经过运行时回调），
// 组合根订阅总线上带 run_id 的 approval.requested / approval.resolved 后调用本方法。
// 本方法只推进状态并持久化，不发布事件——事件已经在总线上，再发布一次会重复。
//
// 运行已是终态（包括已经结束并从运行表移除的运行）时忽略并返回 nil；
// 本进程与数据库都不认识的 runID 返回 CodeNotFound。
func (s *Service) NoteApproval(runID string, waiting bool) error {
	if ar := s.lookup(runID); ar != nil {
		ar.mu.Lock()
		defer ar.mu.Unlock()
		s.noteApprovalLocked(ar, waiting)
		return nil
	}
	// 不在运行表中：可能是刚结束的运行（审批在运行终结后才被取消或过期），也可能是未知 ID。
	// 读库没有调用方上下文可用，给它一个有界的时限。
	ctx, cancel := context.WithTimeout(context.Background(), cancelGrace)
	defer cancel()
	if _, err := s.store.GetRun(ctx, runID); err != nil {
		return err
	}
	return nil
}

// noteApprovalLocked 按审批等待状态推进运行状态并写库，不发布事件；返回状态是否改变。调用方持有 ar.mu。
func (s *Service) noteApprovalLocked(ar *activeRun, waiting bool) bool {
	if ar.terminal {
		return false
	}
	from, to := model.RunRunning, model.RunAwaitingApproval
	if !waiting {
		from, to = model.RunAwaitingApproval, model.RunRunning
	}
	if ar.run.Status != from {
		return false
	}
	ar.run.Status = to
	run := ar.run
	if err := s.store.UpdateRun(s.persistCtx(ar), &run); err != nil {
		s.log.Error("更新运行状态失败", "run_id", ar.id, "status", string(to), "error", err)
	}
	return true
}

// watchTimeout 在运行超时时请求运行时取消，并把运行标记为失败。
func (s *Service) watchTimeout(ar *activeRun) {
	select {
	case <-ar.done:
	case <-ar.ctx.Done():
		if !errors.Is(ar.ctx.Err(), context.DeadlineExceeded) {
			return
		}
		cctx, cancel := context.WithTimeout(context.Background(), cancelGrace)
		defer cancel()
		if err := s.runtime.CancelRun(cctx, ar.id); err != nil && !model.IsCode(err, model.CodeNotFound) {
			s.log.Warn("超时后取消运行失败", "run_id", ar.id, "error", err)
		}
		s.finishRun(ar, model.RunFailed, ErrorRunTimeout)
	}
}

// handleEvent 是交给 Runtime 的事件回调：补全路由字段、推进状态机、持久化终态并发布到总线。
// 终态之后的事件被忽略，因此重复的终态事件是无害的。
func (s *Service) handleEvent(ar *activeRun, ev model.RunEvent) {
	ar.mu.Lock()
	defer ar.mu.Unlock()
	if ar.terminal {
		s.log.Debug("忽略终态之后的运行事件", "run_id", ar.id, "type", string(ev.Type))
		return
	}
	ev.RunID, ev.SessionID, ev.AgentID = ar.id, ar.sessionID, ar.agent.ID
	if ev.At.IsZero() {
		ev.At = s.now()
	}
	switch ev.Type {
	case model.EventRunStarted:
		s.transitionLocked(ar, model.RunRunning, ev, func(r *model.Run) {
			if r.StartedAt == nil {
				t := ev.At
				r.StartedAt = &t
			}
		})
	case model.EventApprovalRequested, model.EventApprovalResolved:
		// 协议规定工作进程不发送 approval.*，正常路径是组合根经 NoteApproval 送来的总线事件；
		// 运行时若仍从回调送来，按同一规则推进状态，并因为它不在总线上而转发一次。
		s.noteApprovalLocked(ar, ev.Type == model.EventApprovalRequested)
		s.publishRun(ev)
	case model.EventTextDelta:
		ar.text.WriteString(ev.Delta)
		s.publishRun(ev)
	case model.EventUsage:
		if ev.Usage != nil {
			ar.usage.Add(*ev.Usage)
		}
		s.publishRun(ev)
	case model.EventRunCompleted:
		s.completeLocked(ar, ev)
	case model.EventRunFailed:
		if ev.Error == "" {
			ev.Error = ErrorRunFailed
		}
		s.finishLocked(ar, model.RunFailed, ev)
	case model.EventRunCancelled:
		s.finishLocked(ar, model.RunCancelled, ev)
	default:
		// tool.call.* 等过程事件只转发不落库：最终助手消息会带上完整的工具调用与结果片段。
		s.publishRun(ev)
	}
}

// transitionLocked 推进到一个非终态，写库并发布事件。不在状态机表内的迁移按事件执行并记录警告，
// 因为运行时是事实来源，拒绝事件只会让界面与实际状态脱节。
func (s *Service) transitionLocked(ar *activeRun, to model.RunStatus, ev model.RunEvent, mutate func(*model.Run)) {
	if from := ar.run.Status; from != to {
		if !CanTransition(from, to) {
			s.log.Warn("运行状态迁移不在状态机允许范围内，按事件执行",
				"run_id", ar.id, "from", string(from), "to", string(to), "event", string(ev.Type))
		}
		ar.run.Status = to
	}
	if mutate != nil {
		mutate(&ar.run)
	}
	run := ar.run
	if err := s.store.UpdateRun(s.persistCtx(ar), &run); err != nil {
		s.log.Error("更新运行状态失败", "run_id", ar.id, "status", string(to), "error", err)
	}
	s.publishRun(ev)
}

// completeLocked 处理 run.completed：在一个事务内写入最终助手消息与运行终态；
// 写入失败时运行降级为 failed（ErrorPersistFailed），因为没有保存下来的回复不算完成。
func (s *Service) completeLocked(ar *activeRun, ev model.RunEvent) {
	out := assistantMessage(ar, ev.Message, ev.At, false)
	run := ar.run
	run.Status = model.RunCompleted
	ended := ev.At
	run.EndedAt = &ended
	run.Usage = finalUsage(ar, ev.Usage)
	if out != nil {
		run.OutputMessageID = out.ID
	}
	pctx := s.persistCtx(ar)
	err := s.store.Tx(pctx, func(tx model.Store) error {
		if out != nil {
			if err := tx.AppendMessage(pctx, out); err != nil {
				return fmt.Errorf("追加助手消息: %w", err)
			}
		}
		if err := tx.UpdateRun(pctx, &run); err != nil {
			return fmt.Errorf("更新运行记录: %w", err)
		}
		return nil
	})
	if err != nil {
		s.log.Error("写入运行结果失败，运行标记为失败", "run_id", ar.id, "error", err)
		run.Status = model.RunFailed
		run.Error = ErrorPersistFailed
		run.OutputMessageID = ""
		if uerr := s.store.UpdateRun(pctx, &run); uerr != nil {
			s.log.Error("写入运行失败状态也未成功，留待启动恢复", "run_id", ar.id, "error", uerr)
		}
		ar.run = run
		usage := run.Usage
		s.finalizeLocked(ar, model.RunEvent{
			Type: model.EventRunFailed, RunID: ar.id, SessionID: ar.sessionID, AgentID: ar.agent.ID,
			At: ev.At, Error: ErrorPersistFailed, Usage: &usage,
		})
		return
	}
	ar.run = run
	if out != nil {
		s.publishRun(model.RunEvent{
			Type: model.EventMessageCreated, RunID: ar.id, SessionID: ar.sessionID, AgentID: ar.agent.ID, At: ev.At, Message: out,
		})
	}
	ev.Message = out
	usage := run.Usage
	ev.Usage = &usage
	s.finalizeLocked(ar, ev)
}

// finishLocked 处理 failed / cancelled：保留已经流出的部分文本（标记 partial），写入终态与用量。
// 失败与取消消耗的 token 同样计入用量。写库失败只记日志：运行在内存中已经终结，数据库由启动恢复修正。
func (s *Service) finishLocked(ar *activeRun, status model.RunStatus, ev model.RunEvent) {
	out := assistantMessage(ar, nil, ev.At, true)
	run := ar.run
	run.Status = status
	if status == model.RunFailed {
		run.Error = ev.Error
	}
	ended := ev.At
	run.EndedAt = &ended
	run.Usage = finalUsage(ar, ev.Usage)
	if out != nil {
		run.OutputMessageID = out.ID
	}
	pctx := s.persistCtx(ar)
	err := s.store.Tx(pctx, func(tx model.Store) error {
		if out != nil {
			if err := tx.AppendMessage(pctx, out); err != nil {
				return fmt.Errorf("追加部分输出: %w", err)
			}
		}
		if err := tx.UpdateRun(pctx, &run); err != nil {
			return fmt.Errorf("更新运行记录: %w", err)
		}
		return nil
	})
	if err != nil {
		s.log.Error("写入运行终态失败，留待启动恢复", "run_id", ar.id, "status", string(status), "error", err)
		run.OutputMessageID = ""
		out = nil
	}
	ar.run = run
	if out != nil {
		s.publishRun(model.RunEvent{
			Type: model.EventMessageCreated, RunID: ar.id, SessionID: ar.sessionID, AgentID: ar.agent.ID, At: ev.At, Message: out,
		})
	}
	usage := run.Usage
	ev.Usage = &usage
	s.finalizeLocked(ar, ev)
}

// finalizeLocked 标记终态、释放会话占位、发布终态事件与 session.updated。
// 先释放再发布：客户端收到终态事件后立刻发起的下一次 Send 不会再撞上冲突。
func (s *Service) finalizeLocked(ar *activeRun, ev model.RunEvent) {
	ar.terminal = true
	s.release(ar)
	s.publishRun(ev)
	if sess, err := s.store.GetSession(s.persistCtx(ar), ar.sessionID); err == nil {
		s.publishSession(EventSessionUpdated, sess)
	} else {
		s.log.Warn("运行结束后读取会话失败", "session_id", ar.sessionID, "error", err)
	}
}

// finishRun 从本包内部（启动失败、超时、本地取消）终结运行；已终结时无操作。
func (s *Service) finishRun(ar *activeRun, status model.RunStatus, reason string) {
	ar.mu.Lock()
	defer ar.mu.Unlock()
	if ar.terminal {
		return
	}
	ev := model.RunEvent{RunID: ar.id, SessionID: ar.sessionID, AgentID: ar.agent.ID, At: s.now()}
	switch status {
	case model.RunCancelled:
		ev.Type = model.EventRunCancelled
	default:
		status = model.RunFailed
		ev.Type = model.EventRunFailed
		if reason == "" {
			reason = ErrorRunFailed
		}
		ev.Error = reason
	}
	s.finishLocked(ar, status, ev)
}

// persistCtx 返回用于落盘的上下文：继承运行上下文的值但不随它取消，超时或关闭时仍能写入终态。
func (s *Service) persistCtx(ar *activeRun) context.Context {
	return context.WithoutCancel(ar.ctx)
}

// systemPrompt 返回最终系统提示：有 PromptBuilder 时交给它，否则直接用智能体的 SystemPrompt。
func (s *Service) systemPrompt(ctx context.Context, agent *model.Agent, sess *model.Session) (string, error) {
	if s.opts.PromptBuilder == nil {
		return agent.SystemPrompt, nil
	}
	return s.opts.PromptBuilder.BuildSystemPrompt(ctx, agent, sess)
}

// loadHistory 读取以 upToSeq 结尾的最近 HistoryLimit 条消息，并保证窗口从一个用户回合开始。
func (s *Service) loadHistory(ctx context.Context, sessionID string, upToSeq int64) ([]model.Message, error) {
	limit := s.opts.HistoryLimit
	after := upToSeq - int64(limit)
	if after < 0 {
		after = 0
	}
	msgs, err := s.store.ListMessages(ctx, sessionID, after, limit)
	if err != nil {
		return nil, err
	}
	out := make([]model.Message, 0, len(msgs))
	for _, m := range msgs {
		if m.Seq <= upToSeq {
			out = append(out, *m)
		}
	}
	return trimHistory(out), nil
}

// trimHistory 丢弃窗口开头的非用户消息：以 assistant 或 tool 消息开头的上下文会引用窗口外的
// 工具调用，多数模型服务会拒绝；从用户回合开始的窗口总是自洽的。
func trimHistory(msgs []model.Message) []model.Message {
	i := 0
	for i < len(msgs) && msgs[i].Role != model.RoleUser {
		i++
	}
	return msgs[i:]
}

// assembleMessages 返回交给运行时的消息列表：只有会话历史，不含 system 角色消息。
//
// 系统提示只通过 RunRequest.Agent.SystemPrompt 传递（见 api/worker-rpc/protocol.md §2 run.start），
// 工作进程据此构造自己的系统消息；若这里再放一条 system 消息，模型会收到两份相同的系统提示。
func assembleMessages(history []model.Message) []model.Message {
	return append([]model.Message(nil), history...)
}

// agentWithPrompt 返回把 SystemPrompt 替换为最终系统提示的智能体拷贝；
// 最终提示可能由 PromptBuilder 在智能体配置之上追加了技能清单等内容。
func agentWithPrompt(agent *model.Agent, prompt string) *model.Agent {
	a := *agent
	a.SystemPrompt = prompt
	return &a
}

// userParts 把文本与附件转换为消息片段；空白文本不产生文本片段。
func userParts(text string, attachments []model.Attachment) []model.Part {
	parts := make([]model.Part, 0, len(attachments)+1)
	if strings.TrimSpace(text) != "" {
		parts = append(parts, model.Part{Type: model.PartText, Text: text})
	}
	for i := range attachments {
		a := attachments[i]
		parts = append(parts, model.Part{Type: model.PartAttachment, Attachment: &a})
	}
	return parts
}

// userMeta 生成用户消息的 Meta：触发来源、发送者与是否可信。
func userMeta(trigger model.RunTrigger, principal model.Principal, untrusted bool) json.RawMessage {
	m := map[string]any{MetaTrigger: string(trigger), MetaUntrusted: untrusted}
	if principal.ID != "" {
		m[MetaPrincipalID] = principal.ID
	}
	if principal.Kind != "" {
		m[MetaPrincipalKind] = string(principal.Kind)
	}
	return mustJSON(m)
}

// assistantMessage 构造运行产出的助手消息：优先用运行时给出的 parts，否则用累积的流式文本；
// 两者都没有时返回 nil（不写空消息）。partial 为 true 时在 Meta 中标记。
func assistantMessage(ar *activeRun, src *model.Message, at time.Time, partial bool) *model.Message {
	var (
		parts []model.Part
		meta  json.RawMessage
	)
	if src != nil {
		parts = src.Parts
		meta = src.Meta
	}
	if len(parts) == 0 && ar.text.Len() > 0 {
		parts = []model.Part{{Type: model.PartText, Text: ar.text.String()}}
	}
	if len(parts) == 0 {
		return nil
	}
	if partial {
		meta = withMeta(meta, map[string]any{MetaPartial: true})
	}
	return &model.Message{
		ID:        model.NewID(),
		SessionID: ar.sessionID,
		Role:      model.RoleAssistant,
		RunID:     ar.id,
		Parts:     parts,
		Meta:      meta,
		CreatedAt: at,
	}
}

// finalUsage 决定终态记录的用量：运行时在终态事件里给出的累计用量优先，但若它小于本包
// 从 usage 事件累加的结果（运行时只报了最后一次调用），则以累加值为准，避免少记。
func finalUsage(ar *activeRun, reported *model.Usage) model.Usage {
	if reported == nil {
		return ar.usage
	}
	if reported.InputTokens+reported.OutputTokens < ar.usage.InputTokens+ar.usage.OutputTokens {
		return ar.usage
	}
	return *reported
}

// withMeta 在已有的 Meta JSON 上合并键值；已有内容不是对象时从空对象开始。
func withMeta(existing json.RawMessage, add map[string]any) json.RawMessage {
	m := map[string]any{}
	if len(existing) > 0 {
		if err := json.Unmarshal(existing, &m); err != nil {
			m = map[string]any{}
		}
	}
	for k, v := range add {
		m[k] = v
	}
	return mustJSON(m)
}

// mustJSON 序列化只含字符串、布尔与数字的 map；这类输入不会失败。
func mustJSON(m map[string]any) json.RawMessage {
	b, err := json.Marshal(m)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}

// userError 提取写入 Run.Error 的面向用户的原因：带错误码的错误只取 Message，不带底层细节。
func userError(err error) string {
	var me *model.Error
	if errors.As(err, &me) && me.Message != "" {
		return me.Message
	}
	return err.Error()
}
