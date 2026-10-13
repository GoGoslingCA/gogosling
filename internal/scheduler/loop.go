package scheduler

import (
	"context"
	"fmt"
	"runtime/debug"
	"sort"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// 本文件是调度循环：加载任务、按节拍找出到期任务、应用错过策略、执行并写回结果。
//
// 不用定时器而用轮询的原因：定时器基于单调时钟，设备休眠期间不走或走得不准，
// 唤醒后既不能可靠地"立刻补发"，也无法区分"刚到点"与"已经错过很多次"。
// 轮询把判断建立在持久化的绝对时间上，重启与休眠都不需要特殊恢复逻辑。

// wakeGapFactor 决定休眠判定阈值：相邻节拍间隔超过 Tick 的该倍数视为设备休眠过或时钟被调整。
const wakeGapFactor = 3

// Start 加载全部任务并运行调度循环，直到 ctx 结束；返回前等待在途的执行结束。
//
// 它是阻塞调用，组合根应在独立 goroutine 中运行。ctx 结束时返回 nil；加载失败或重复调用返回错误。
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return model.NewError(model.CodeConflict, "scheduler: Start 已被调用")
	}
	s.started = true
	s.runCtx = ctx
	s.mu.Unlock()

	if err := s.load(ctx); err != nil {
		return fmt.Errorf("scheduler: 加载任务失败: %w", err)
	}
	s.log.Info("调度循环已启动", "tick", s.tick, "default_timezone", s.defaultLoc.String())

	// 启动时立刻做一次节拍：进程停止期间错过的触发在这里按策略处理。
	s.tickOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			s.wg.Wait()
			s.log.Info("调度循环已停止")
			return nil
		case <-s.clock.After(s.tick):
			s.tickOnce(ctx)
		}
	}
}

// load 从仓储读取全部任务并建立内存副本；无法解析的启用任务被标记为 failed 且不调度。
func (s *Service) load(ctx context.Context) error {
	jobs, err := s.repo.ListJobs(ctx, "")
	if err != nil {
		return err
	}
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()
	entries := make(map[string]*entry, len(jobs))
	scheduled := 0
	for _, job := range jobs {
		e := &entry{job: job}
		entries[job.ID] = e
		sched, perr := ParseSchedule(job.Schedule, job.Timezone, s.defaultLoc)
		if perr != nil {
			if job.Enabled {
				s.log.Warn("定时任务的表达式或时区无法解析，已标记失败并跳过调度",
					"job_id", job.ID, "schedule", job.Schedule, "timezone", job.Timezone, "error", perr)
				job.NextRunAt = nil
				job.LastStatus = model.JobRunFailed
				job.LastError = errorText(perr)
				s.persistLocked(ctx, e)
			}
			continue
		}
		e.sched = sched
		if !job.Enabled {
			continue
		}
		scheduled++
		if job.NextRunAt == nil {
			job.NextRunAt = s.nextPtr(sched, now)
			s.persistLocked(ctx, e)
		}
	}
	s.entries = entries
	s.nextDueValid = false
	s.log.Info("定时任务已加载", "total", len(jobs), "scheduled", scheduled)
	return nil
}

// tickOnce 执行一次节拍：检测休眠、处理到期任务，并在首个节拍或唤醒后重算 NextRunAt。
func (s *Service) tickOnce(ctx context.Context) {
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()

	first := s.lastTick.IsZero()
	gap := now.Sub(s.lastTick)
	woke := !first && (gap > wakeGapFactor*s.tick || gap < 0)
	s.lastTick = now
	if woke {
		s.log.Info("检测到休眠唤醒或时钟跳变，将重算所有任务的下次触发时间", "gap", gap)
	}

	// 保留每个节拍的墙钟检查，不能因缓存跳过启动、休眠或回拨后的重算。
	if !first && !woke && s.nextDueValid && (!s.hasNextDue || s.nextDue.After(now)) {
		return
	}
	for _, e := range s.dueEntriesLocked(now) {
		s.handleDueLocked(ctx, e, now)
	}
	if first || woke {
		s.resyncLocked(ctx, now)
	}
	s.refreshNextDueLocked()
}

// dueEntriesLocked 只排序到期子集，保留创建时间和 ID 的触发顺序。调用方持有 s.mu。
func (s *Service) dueEntriesLocked(now time.Time) []*entry {
	var due []*entry
	for _, e := range s.entries {
		if e.job.Enabled && e.sched != nil && e.job.NextRunAt != nil && !e.job.NextRunAt.After(now) {
			due = append(due, e)
		}
	}
	if len(due) > 1 {
		sort.Slice(due, func(i, j int) bool {
			a, b := due[i].job, due[j].job
			if !a.CreatedAt.Equal(b.CreatedAt) {
				return a.CreatedAt.Before(b.CreatedAt)
			}
			return a.ID < b.ID
		})
	}
	return due
}

// refreshNextDueLocked 从权威副本更新最早时间；写库失败留下的到期项仍会在下个节拍重试。
// 调用方持有 s.mu。
func (s *Service) refreshNextDueLocked() {
	s.nextDue = time.Time{}
	s.hasNextDue = false
	for _, e := range s.entries {
		if !e.job.Enabled || e.sched == nil || e.job.NextRunAt == nil {
			continue
		}
		if !s.hasNextDue || e.job.NextRunAt.Before(s.nextDue) {
			s.nextDue = *e.job.NextRunAt
			s.hasNextDue = true
		}
	}
	s.nextDueValid = true
}

// handleDueLocked 处理一条到期任务：并发保护、错过策略、推进 NextRunAt 并启动执行。调用方持有 s.mu。
func (s *Service) handleDueLocked(ctx context.Context, e *entry, now time.Time) {
	previous := clone(e.job)
	job := e.job
	due := *job.NextRunAt
	missed := e.sched.missedBetween(due, now)
	job.NextRunAt = s.nextPtr(e.sched, now)
	if job.NextRunAt == nil {
		// 表达式在可预见的范围内没有下一次：本次照常处理，之后保持启用但不再调度。
		s.log.Warn("定时任务没有下一次触发时间", "job_id", job.ID, "schedule", job.Schedule)
	}

	reason := "scheduled"
	switch {
	case s.isRunningLocked(job.ID):
		job.LastStatus = model.JobRunSkipped
		job.LastError = "上一轮尚未结束，跳过本次触发"
		reason = "running_skip"
	case missed > 0 && job.MissedPolicy == model.MissedSkip:
		job.LastStatus = model.JobRunSkipped
		job.LastError = fmt.Sprintf("错过 %d 次触发（最早应于 %s 执行），按策略跳过",
			missed+1, due.In(e.sched.Location()).Format(time.RFC3339))
		reason = "missed_skip"
	case missed > 0:
		reason = "catch_up"
	}
	// 先持久化对本次触发的认领；失败时保留原到期时间，后续节拍或重启可以重试。
	// 未写入的状态不能发布触发事件，也不能交给 Runner 执行。
	if err := s.persistLocked(ctx, e); err != nil {
		e.job = previous
		return
	}
	switch reason {
	case "running_skip":
		s.log.Warn("定时任务上一轮尚未结束，跳过本次触发", "job_id", job.ID, "due", due)
	case "missed_skip":
		s.log.Info("定时任务错过触发，按策略跳过", "job_id", job.ID, "missed", missed+1, "due", due)
	case "catch_up":
		s.log.Info("定时任务错过触发，按策略补跑一次", "job_id", job.ID, "missed", missed+1, "due", due)
		s.launchLocked(e, now, "catch_up")
	default:
		s.launchLocked(e, now, "scheduled")
	}
}

// resyncLocked 重算所有未到期任务的 NextRunAt：墙上时钟对齐的表达式总是以"现在"重算
// （处理时钟倒退、时区数据变化）；@every 保留原有触发链，只在重算结果更早时采用。调用方持有 s.mu。
func (s *Service) resyncLocked(ctx context.Context, now time.Time) {
	for _, id := range s.sortedIDsLocked() {
		e := s.entries[id]
		if !e.job.Enabled || e.sched == nil || e.job.NextRunAt == nil || !e.job.NextRunAt.After(now) {
			// 仍到期的任务可能在本节拍写库失败，不能重算时间而吞掉待重试的触发。
			continue
		}
		candidate := e.sched.Next(now)
		if candidate.IsZero() {
			continue
		}
		stored := *e.job.NextRunAt
		if e.sched.Interval() && !candidate.Before(stored) {
			continue
		}
		if candidate.Equal(stored) {
			continue
		}
		s.log.Debug("重算下次触发时间", "job_id", id, "from", stored, "to", candidate)
		previous := clone(e.job)
		e.job.NextRunAt = &candidate
		if err := s.persistLocked(ctx, e); err != nil {
			e.job = previous
		}
	}
}

// launchLocked 标记任务为执行中、发布 job.triggered 并在新 goroutine 中执行。调用方持有 s.mu。
func (s *Service) launchLocked(e *entry, startedAt time.Time, reason string) {
	id := e.job.ID
	s.running[id] = struct{}{}
	snapshot := *clone(e.job)
	s.publish(snapshot)
	s.log.Info("触发定时任务", "job_id", id, "agent_id", snapshot.AgentID, "kind", snapshot.Kind, "reason", reason)

	ctx := s.runCtx
	s.wg.Add(1)
	go s.execute(ctx, snapshot, startedAt)
}

// execute 在独立 goroutine 中调用 Runner，并把结果写回。
func (s *Service) execute(ctx context.Context, job model.Job, startedAt time.Time) {
	defer s.wg.Done()
	if s.runTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.runTimeout)
		defer cancel()
	}
	err := s.safeRun(ctx, &job)
	s.finish(job.ID, startedAt, err)
}

// safeRun 调用 Runner 并把 panic 转成错误，避免一个适配器缺陷拖垮守护进程。
func (s *Service) safeRun(ctx context.Context, job *model.Job) (err error) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("定时任务执行时发生 panic", "job_id", job.ID, "panic", r, "stack", string(debug.Stack()))
			err = model.Errorf(model.CodeInternal, "执行时发生 panic: %v", r)
		}
	}()
	return s.runner.RunJob(ctx, job)
}

// finish 记录一次执行的结果。任务在执行期间被删除时只记日志。
func (s *Service) finish(id string, startedAt time.Time, runErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 写回脱离运行 ctx：守护进程关闭时也应把最后一次结果落库。
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.runCtx), bookkeepingTimeout)
	defer cancel()

	delete(s.running, id)
	e, ok := s.entries[id]
	if !ok {
		s.log.Debug("定时任务在执行期间被删除，结果不再写回", "job_id", id)
		return
	}
	started := startedAt
	e.job.LastRunAt = &started
	if runErr != nil {
		e.job.LastStatus = model.JobRunFailed
		e.job.LastError = errorText(runErr)
		s.log.Warn("定时任务执行失败", "job_id", id, "error", runErr)
	} else {
		e.job.LastStatus = model.JobRunOK
		e.job.LastError = ""
		s.log.Info("定时任务执行完成", "job_id", id, "elapsed", s.now().Sub(startedAt))
	}
	s.persistLocked(ctx, e)
}

// isRunningLocked 报告任务是否有在途的执行。调用方持有 s.mu。
func (s *Service) isRunningLocked(id string) bool {
	_, ok := s.running[id]
	return ok
}

// sortedIDsLocked 返回按创建时间（再按 ID）排序的任务 ID，使一次节拍内的处理顺序确定。调用方持有 s.mu。
func (s *Service) sortedIDsLocked() []string {
	ids := make([]string, 0, len(s.entries))
	for id := range s.entries {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, k int) bool {
		a, b := s.entries[ids[i]].job, s.entries[ids[k]].job
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return a.ID < b.ID
	})
	return ids
}
