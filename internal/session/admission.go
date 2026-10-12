package session

import (
	"context"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/i18n"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

// DefaultParallelRuns 是桌面组合根默认执行槽；领域零配置保留原接口行为。
const DefaultParallelRuns = 2

// DefaultQueuedRuns 是轻量待执行记录上限；排队时不装载完整历史或创建工作进程任务。
const DefaultQueuedRuns = 32

// admittedRun 只保存启动回调和既有在途身份，不持有历史正文或模型凭据。
type admittedRun struct {
	run        *activeRun
	start      func() error
	enqueuedAt time.Time
}

// ResourceState 是实际执行槽与队列状态；等待审批仍占一个执行槽，避免集中批准后突发超量。
type ResourceState struct {
	MaxParallel      int `json:"max_parallel"`
	MaxQueued        int `json:"max_queued"`
	Running          int `json:"running"`
	Queued           int `json:"queued"`
	AwaitingApproval int `json:"awaiting_approval"`
}

// Resources 读取真实资源状态；不在服务锁内等待运行锁。
func (s *Service) Resources() ResourceState {
	s.mu.Lock()
	state := ResourceState{MaxParallel: s.maxParallel, MaxQueued: s.maxQueued, Running: len(s.slots) + len(s.promptRuns), Queued: len(s.queue)}
	runs := make([]*activeRun, 0, len(s.slots))
	for id := range s.slots {
		if ar := s.active[id]; ar != nil {
			runs = append(runs, ar)
		}
	}
	s.mu.Unlock()
	for _, ar := range runs {
		if ar.snapshot().Status == model.RunAwaitingApproval {
			state.AwaitingApproval++
		}
	}
	return state
}

// SetParallelLimit 热调整后续派发；降低上限不强制中断在途工作。
func (s *Service) SetParallelLimit(limit int) error {
	if limit < 1 || limit > 8 {
		return model.NewError(model.CodeInvalid, i18n.T("en", "resources.invalid_limit", nil))
	}
	s.mu.Lock()
	s.maxParallel = limit
	if s.maxQueued < 1 {
		s.maxQueued = DefaultQueuedRuns
	}
	ready := s.takeReadyLocked()
	s.mu.Unlock()
	s.launchQueued(ready)
	return nil
}

func queueFullError() error {
	return &model.Error{Code: model.CodeConflict, Message: i18n.T("en", "resources.queue_full", nil), Details: map[string]any{"reason": "resource_queue_full"}}
}

// admit 在现有 Run 已落盘后决定立即启动或轻量排队；审批及文件边界不变。
func (s *Service) admit(ar *activeRun, start func() error) (bool, error) {
	s.mu.Lock()
	if s.closed || s.active[ar.id] != ar || ar.ctx.Err() != nil {
		s.mu.Unlock()
		return false, model.NewError(model.CodeCancelled, i18n.T("en", "resources.cancelled", nil))
	}
	if s.maxParallel == 0 || (len(s.slots)+len(s.promptRuns) < s.maxParallel && s.groups[ar.group] == 0 && len(s.queue) == 0) {
		s.slots[ar.id] = true
		s.groups[ar.group]++
		s.mu.Unlock()
		return true, nil
	}
	s.queue = append(s.queue, admittedRun{run: ar, start: start, enqueuedAt: s.now()})
	// 某工作区正在执行时，后来的独立工作区仍可使用空闲槽。
	ready := s.takeReadyLocked()
	s.mu.Unlock()
	s.launchQueued(ready)
	return false, nil
}

func (s *Service) takeReadyLocked() []admittedRun {
	ready := []admittedRun{}
	for !s.closed && len(s.queue) > 0 && (s.maxParallel == 0 || len(s.slots)+len(s.promptRuns) < s.maxParallel) {
		best := -1
		score := -1
		for i, item := range s.queue {
			if item.run.ctx.Err() != nil || s.active[item.run.id] != item.run {
				continue
			}
			if s.maxParallel > 0 && s.groups[item.run.group] > 0 {
				continue
			}
			priority := 1
			if item.run.trigger == model.TriggerUser {
				priority = 2
			}
			if item.run.trigger == model.TriggerJob {
				priority = 0
			}
			// 有界提升久候任务，避免定时任务被连续前台任务饿死。
			if s.now().Sub(item.enqueuedAt) >= 30*time.Second {
				priority = 3
			}
			if priority > score {
				best = i
				score = priority
			}
		}
		if best < 0 {
			break
		}
		next := s.queue[best]
		copy(s.queue[best:], s.queue[best+1:])
		s.queue[len(s.queue)-1] = admittedRun{}
		s.queue = s.queue[:len(s.queue)-1]
		s.slots[next.run.id] = true
		s.groups[next.run.group]++
		ready = append(ready, next)
	}
	return ready
}
func (s *Service) launchQueued(ready []admittedRun) {
	for _, next := range ready {
		go func(item admittedRun) { _ = item.start() }(next)
	}
}

// startAdmittedRun 只在拿到执行槽后读取历史并解析当前配置，排队记录不保留凭据副本。
func (s *Service) startAdmittedRun(ar *activeRun, sessionID string, inputSeq int64) error {
	if ar.endedOrCancelling() || ar.ctx.Err() != nil {
		s.finishRun(ar, model.RunCancelled, "")
		return nil
	}
	sess, err := s.store.GetSession(ar.ctx, sessionID)
	if err != nil {
		return s.startFailed(ar, err)
	}
	agent, err := s.agents.GetAgent(ar.ctx, sess.AgentID)
	if err != nil {
		return s.startFailed(ar, err)
	}
	if s.opts.WorkspaceGroup != nil {
		group, e := s.opts.WorkspaceGroup(ar.ctx, agent)
		if e != nil {
			return s.startFailed(ar, e)
		}
		if group != "" && group != ar.group {
			return s.startFailed(ar, model.NewError(model.CodeConflict, i18n.T("en", "resources.workspace_changed", nil)))
		}
	}
	if !agent.Enabled || sess.Status != model.SessionActive {
		return s.startFailed(ar, model.NewError(model.CodeConflict, i18n.T("en", "resources.unavailable", nil)))
	}
	creds, err := s.creds.Credentials(ar.ctx, agent.Model)
	if err != nil {
		return s.startFailed(ar, err)
	}
	tools, err := s.tools.ToolsFor(ar.ctx, agent)
	if err != nil {
		return s.startFailed(ar, err)
	}
	prompt, err := s.systemPrompt(ar.ctx, agent, sess)
	if err != nil {
		return s.startFailed(ar, err)
	}
	history, err := s.loadHistory(ar.ctx, sess.ID, inputSeq)
	if err != nil {
		return s.startFailed(ar, err)
	}
	if ar.endedOrCancelling() || ar.ctx.Err() != nil {
		s.finishRun(ar, model.RunCancelled, "")
		return nil
	}
	rr := RunRequest{RunID: ar.id, SessionID: sess.ID, Agent: agentWithPrompt(agent, prompt), Credentials: creds, Messages: assembleMessages(history), Tools: tools, Options: s.opts.Run}
	if err = s.runtime.StartRun(ar.ctx, rr, func(ev model.RunEvent) { s.handleEvent(ar, ev) }); err != nil {
		return s.startFailed(ar, err)
	}
	if ar.cancelPending() {
		ctx, cancel := context.WithTimeout(s.persistCtx(ar), cancelGrace)
		defer cancel()
		_ = s.runtime.CancelRun(ctx, ar.id)
	}
	return nil
}
func (s *Service) startFailed(ar *activeRun, err error) error {
	if ar.snapshot().Status == model.RunCancelled {
		s.finishRun(ar, model.RunCancelled, "")
		return nil
	}
	if model.IsCode(err, model.CodeCancelled) || ar.endedOrCancelling() {
		s.finishRun(ar, model.RunCancelled, "")
	} else {
		s.finishRun(ar, model.RunFailed, userError(err))
	}
	return err
}
