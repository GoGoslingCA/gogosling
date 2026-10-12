package scheduler

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

func TestTickTriggersDueJob(t *testing.T) {
	env := newTestEnv(t, Options{})
	job := env.create(t, nil) // 0 9 * * * UTC → 2026-01-12 09:00
	sub := env.bus.Subscribe(context.Background(), 0, 8, nil)
	defer sub.Cancel()

	env.clock.Set(time.Date(2026, 1, 12, 8, 59, 59, 0, time.UTC))
	env.svc.tickOnce(context.Background())
	env.runner.expectNoCall(t)

	fired := time.Date(2026, 1, 12, 9, 0, 5, 0, time.UTC)
	env.clock.Set(fired)
	env.svc.tickOnce(context.Background())
	ran := env.runner.next(t)
	if ran.ID != job.ID {
		t.Fatalf("应触发任务 %s，得到 %s", job.ID, ran.ID)
	}
	// 触发时先推进 NextRunAt 并落库，再执行。
	mustEqualTime(t, "触发后的 NextRunAt", ran.NextRunAt, time.Date(2026, 1, 13, 9, 0, 0, 0, time.UTC))
	select {
	case ev := <-sub.C:
		if ev.Type != EventJobTriggered || ev.Payload.(model.Job).ID != job.ID {
			t.Errorf("应发布 job.triggered，得到 %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("应发布 job.triggered 事件")
	}
	env.waitIdle(t, job.ID)
	stored := env.repo.get(t, job.ID)
	mustEqualTime(t, "LastRunAt", stored.LastRunAt, fired)
	mustEqualTime(t, "NextRunAt", stored.NextRunAt, time.Date(2026, 1, 13, 9, 0, 0, 0, time.UTC))
	if stored.LastStatus != model.JobRunOK {
		t.Errorf("应记录 ok，得到 %q", stored.LastStatus)
	}
	// 同一周期内再次节拍不会重复触发。
	env.clock.Set(fired.Add(15 * time.Second))
	env.svc.tickOnce(context.Background())
	env.runner.expectNoCall(t)
}

func TestLateWithinOnePeriodStillRuns(t *testing.T) {
	env := newTestEnv(t, Options{})
	job := env.create(t, nil)
	env.clock.Set(time.Date(2026, 1, 12, 14, 0, 0, 0, time.UTC)) // 迟到 5 小时，不到一天
	env.svc.tickOnce(context.Background())
	env.runner.next(t)
	env.waitIdle(t, job.ID)
	mustEqualTime(t, "NextRunAt", env.repo.get(t, job.ID).NextRunAt, time.Date(2026, 1, 13, 9, 0, 0, 0, time.UTC))
}

func TestMissedPolicySkip(t *testing.T) {
	env := newTestEnv(t, Options{})
	job := env.create(t, func(s *JobSpec) { s.MissedPolicy = model.MissedSkip })
	env.clock.Set(time.Date(2026, 1, 14, 9, 30, 0, 0, time.UTC)) // 错过 12 日与 13 日两次
	env.svc.tickOnce(context.Background())
	env.runner.expectNoCall(t)
	stored := env.repo.get(t, job.ID)
	if stored.LastStatus != model.JobRunSkipped {
		t.Errorf("应记录 skipped，得到 %q", stored.LastStatus)
	}
	if !strings.Contains(stored.LastError, "错过 3 次") {
		t.Errorf("LastError 应说明错过次数，得到 %q", stored.LastError)
	}
	if stored.LastRunAt != nil {
		t.Error("跳过不算执行，LastRunAt 应保持为空")
	}
	mustEqualTime(t, "NextRunAt", stored.NextRunAt, time.Date(2026, 1, 15, 9, 0, 0, 0, time.UTC))
}

func TestMissedPolicyRunOnce(t *testing.T) {
	env := newTestEnv(t, Options{})
	job := env.create(t, func(s *JobSpec) { s.MissedPolicy = model.MissedRunOnce })
	now := time.Date(2026, 1, 14, 9, 30, 0, 0, time.UTC)
	env.clock.Set(now)
	env.svc.tickOnce(context.Background())
	env.runner.next(t)
	env.waitIdle(t, job.ID)
	env.runner.expectNoCall(t) // 错过多次也只补跑一次
	stored := env.repo.get(t, job.ID)
	if stored.LastStatus != model.JobRunOK {
		t.Errorf("补跑成功应记录 ok，得到 %q", stored.LastStatus)
	}
	mustEqualTime(t, "LastRunAt", stored.LastRunAt, now)
	mustEqualTime(t, "NextRunAt", stored.NextRunAt, time.Date(2026, 1, 15, 9, 0, 0, 0, time.UTC))
}

func TestOverlappingRunIsSkipped(t *testing.T) {
	env := newTestEnv(t, Options{Tick: time.Minute})
	env.runner.block = make(chan struct{})
	job := env.create(t, func(s *JobSpec) { s.Schedule = "@every 1m" }) // 下一次 09:00:50

	env.clock.Set(time.Date(2026, 1, 12, 9, 0, 55, 0, time.UTC))
	env.svc.tickOnce(context.Background())
	env.runner.next(t)
	mustEqualTime(t, "执行中的 NextRunAt", env.repo.get(t, job.ID).NextRunAt, time.Date(2026, 1, 12, 9, 1, 55, 0, time.UTC))

	env.clock.Set(time.Date(2026, 1, 12, 9, 2, 0, 0, time.UTC))
	env.svc.tickOnce(context.Background())
	env.runner.expectNoCall(t)
	stored := env.repo.get(t, job.ID)
	if stored.LastStatus != model.JobRunSkipped || !strings.Contains(stored.LastError, "上一轮") {
		t.Errorf("上一轮未结束应记录 skipped，得到 %q / %q", stored.LastStatus, stored.LastError)
	}
	mustEqualTime(t, "跳过后的 NextRunAt", stored.NextRunAt, time.Date(2026, 1, 12, 9, 3, 0, 0, time.UTC))

	close(env.runner.block)
	env.waitIdle(t, job.ID)
	stored = env.repo.get(t, job.ID)
	if stored.LastStatus != model.JobRunOK {
		t.Errorf("上一轮结束后应记录其结果，得到 %q", stored.LastStatus)
	}
	mustEqualTime(t, "LastRunAt 应为第一次触发时间", stored.LastRunAt, time.Date(2026, 1, 12, 9, 0, 55, 0, time.UTC))
	mustEqualTime(t, "NextRunAt 不应被完成写回倒退", stored.NextRunAt, time.Date(2026, 1, 12, 9, 3, 0, 0, time.UTC))
}

func TestWakeRecomputesNextRun(t *testing.T) {
	env := newTestEnv(t, Options{})
	every := env.create(t, func(s *JobSpec) { s.Schedule = "@every 1h" }) // 09:59:50
	env.clock.Advance(time.Second)
	daily := env.create(t, nil) // 2026-01-12 09:00

	env.clock.Set(baseTime.Add(5 * time.Second))
	env.svc.tickOnce(context.Background())

	// 模拟持久化值与表达式不一致（例如时区数据或配置在重启间变化）。
	stale := time.Date(2026, 1, 14, 9, 0, 0, 0, time.UTC)
	env.svc.entries[daily.ID].job.NextRunAt = &stale

	// 节拍间隔远大于 3×Tick：视为休眠唤醒。
	env.clock.Set(baseTime.Add(10 * time.Minute))
	env.svc.tickOnce(context.Background())
	mustEqualTime(t, "墙上时钟表达式应以现在重算", env.repo.get(t, daily.ID).NextRunAt, time.Date(2026, 1, 13, 9, 0, 0, 0, time.UTC))
	mustEqualTime(t, "@every 应保留原有触发链", env.repo.get(t, every.ID).NextRunAt, baseTime.Add(time.Hour))

	// 时钟倒退同样触发重算；@every 只在结果更早时采用。
	env.clock.Set(baseTime.Add(-time.Hour))
	env.svc.tickOnce(context.Background())
	mustEqualTime(t, "时钟倒退后 daily", env.repo.get(t, daily.ID).NextRunAt, time.Date(2026, 1, 12, 9, 0, 0, 0, time.UTC))
	mustEqualTime(t, "时钟倒退后 @every", env.repo.get(t, every.ID).NextRunAt, baseTime)
	env.runner.expectNoCall(t)
}

func TestNoWakeWithinNormalGap(t *testing.T) {
	env := newTestEnv(t, Options{})
	every := env.create(t, func(s *JobSpec) { s.Schedule = "@every 1h" })
	env.svc.tickOnce(context.Background())
	env.svc.entries[every.ID].job.NextRunAt = ptr(baseTime.Add(2 * time.Hour))
	updates := env.repo.updates
	env.clock.Advance(20 * time.Second) // < 3×15s
	env.svc.tickOnce(context.Background())
	if env.repo.updates != updates {
		t.Error("正常节拍不应重算与写回")
	}
}

func TestLoadMarksUnparseableJobsAndFillsMissingNextRun(t *testing.T) {
	env := newTestEnv(t, Options{})
	broken := &model.Job{ID: model.NewID(), AgentID: "a", Name: "坏表达式", Schedule: "0 9 * *", Kind: model.JobPushText,
		Input: "x", MissedPolicy: model.MissedSkip, Enabled: true, CreatedAt: baseTime, UpdatedAt: baseTime}
	badTZ := &model.Job{ID: model.NewID(), AgentID: "a", Name: "坏时区", Schedule: "@daily", Timezone: "Nowhere/City", Kind: model.JobPushText,
		Input: "x", MissedPolicy: model.MissedSkip, Enabled: true, CreatedAt: baseTime, UpdatedAt: baseTime}
	noNext := &model.Job{ID: model.NewID(), AgentID: "a", Name: "缺 NextRunAt", Schedule: "0 9 * * *", Kind: model.JobPushText,
		Input: "x", MissedPolicy: model.MissedSkip, Enabled: true, CreatedAt: baseTime, UpdatedAt: baseTime}
	disabled := &model.Job{ID: model.NewID(), AgentID: "a", Name: "停用", Schedule: "bad", Kind: model.JobPushText,
		Input: "x", MissedPolicy: model.MissedSkip, Enabled: false, CreatedAt: baseTime, UpdatedAt: baseTime}
	for _, j := range []*model.Job{broken, badTZ, noNext, disabled} {
		env.repo.seed(j)
	}

	if err := env.svc.load(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{broken.ID, badTZ.ID} {
		got := env.repo.get(t, id)
		if got.LastStatus != model.JobRunFailed || got.LastError == "" || got.NextRunAt != nil || !got.Enabled {
			t.Errorf("无法解析的任务应标记失败、保持启用且不调度，得到 %+v", got)
		}
		if env.svc.entries[id].sched != nil {
			t.Error("无法解析的任务不应有解析结果")
		}
	}
	mustEqualTime(t, "缺失的 NextRunAt 应被补算", env.repo.get(t, noNext.ID).NextRunAt, time.Date(2026, 1, 12, 9, 0, 0, 0, time.UTC))
	if got := env.repo.get(t, disabled.ID); got.LastStatus != "" || got.LastError != "" {
		t.Errorf("停用的任务不应被改动，得到 %+v", got)
	}

	// 修复表达式后清除解析失败标记并重新调度。
	fixed, err := env.svc.Update(context.Background(), broken.ID, JobPatch{Schedule: ptr("0 9 * * *")})
	if err != nil {
		t.Fatal(err)
	}
	if fixed.LastStatus != "" || fixed.LastError != "" || fixed.NextRunAt == nil {
		t.Errorf("修复后应清除失败标记并计算 NextRunAt，得到 %+v", fixed)
	}
}

func TestStartCatchesUpMissedRunsAndLoopsOnClock(t *testing.T) {
	env := newTestEnv(t, Options{})
	due := time.Date(2026, 1, 11, 9, 0, 0, 0, time.UTC) // 进程停止期间错过
	missed := &model.Job{ID: model.NewID(), AgentID: "a", Name: "补跑", Schedule: "0 9 * * *", Kind: model.JobRunAgent,
		Input: "x", MissedPolicy: model.MissedRunOnce, Enabled: true, NextRunAt: &due, CreatedAt: due, UpdatedAt: due}
	env.repo.seed(missed)
	env.clock.Set(time.Date(2026, 1, 12, 10, 0, 0, 0, time.UTC))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- env.svc.Start(ctx) }()

	ran := env.runner.next(t)
	if ran.ID != missed.ID {
		t.Fatalf("启动时应补跑错过的任务，得到 %s", ran.ID)
	}
	env.waitIdle(t, missed.ID)
	mustEqualTime(t, "补跑后的 NextRunAt", env.repo.get(t, missed.ID).NextRunAt, time.Date(2026, 1, 13, 9, 0, 0, 0, time.UTC))

	if err := env.svc.Start(ctx); !model.IsCode(err, model.CodeConflict) {
		t.Errorf("重复 Start 应返回 conflict，得到 %v", err)
	}

	// 循环按时钟节拍工作：推进到下一次触发后 Runner 再次被调用。
	env.clock.waitTimers(t, 1)
	env.clock.Set(time.Date(2026, 1, 13, 9, 0, 1, 0, time.UTC))
	if ran := env.runner.next(t); ran.ID != missed.ID {
		t.Fatalf("节拍应触发任务，得到 %s", ran.ID)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("ctx 结束后 Start 应返回 nil，得到 %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Start 应在 ctx 结束后返回")
	}
	if env.running(missed.ID) {
		t.Error("Start 返回前应等待在途执行结束")
	}
	if err := env.svc.TriggerNow(context.Background(), missed.ID); !model.IsCode(err, model.CodeUnavailable) {
		t.Errorf("停止后手动触发应返回 unavailable，得到 %v", err)
	}
}

func TestStartWaitsForInFlightRunAndRecordsCancellation(t *testing.T) {
	env := newTestEnv(t, Options{})
	env.runner.block = make(chan struct{})
	job := env.create(t, nil)
	env.clock.Set(time.Date(2026, 1, 12, 9, 0, 1, 0, time.UTC))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- env.svc.Start(ctx) }()
	env.runner.next(t)

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Start 应在 ctx 结束后返回")
	}
	stored := env.repo.get(t, job.ID)
	if stored.LastStatus != model.JobRunFailed || !strings.Contains(stored.LastError, "context canceled") {
		t.Errorf("关闭时被取消的执行应记录失败，得到 %q / %q", stored.LastStatus, stored.LastError)
	}
}

func TestTickProcessesJobsInCreationOrder(t *testing.T) {
	env := newTestEnv(t, Options{})
	var ids []string
	for i := 0; i < 3; i++ {
		env.clock.Advance(time.Millisecond)
		ids = append(ids, env.create(t, nil).ID)
	}
	sub := env.bus.Subscribe(context.Background(), 0, 8, nil)
	defer sub.Cancel()
	env.clock.Set(time.Date(2026, 1, 12, 9, 0, 1, 0, time.UTC))
	env.svc.tickOnce(context.Background())
	// 执行在各自的 goroutine 里，Runner 收到调用的先后不确定；触发事件在锁内按顺序发布，以它为准。
	for i, want := range ids {
		select {
		case ev := <-sub.C:
			if got := ev.Payload.(model.Job).ID; got != want {
				t.Errorf("第 %d 个触发应为 %s，得到 %s", i, want, got)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("等待第 %d 个 job.triggered 事件超时", i)
		}
	}
	for range ids {
		env.runner.next(t)
	}
	for _, id := range ids {
		env.waitIdle(t, id)
	}
}

func TestForgetDropsDeletedAgentJobsFromLoop(t *testing.T) {
	env := newTestEnv(t, Options{})
	ctx := context.Background()
	a := env.create(t, nil)
	b := env.create(t, func(s *JobSpec) { s.Name = "第二个" })
	c := env.create(t, func(s *JobSpec) { s.AgentID = "agent-2" })

	// a 正在执行时智能体被删除。
	env.runner.block = make(chan struct{})
	if err := env.svc.TriggerNow(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if got := env.runner.next(t); got.ID != a.ID {
		t.Fatalf("应执行 %s，得到 %s", a.ID, got.ID)
	}
	// 数据库中的任务已由外键级联删除。
	for _, id := range []string{a.ID, b.ID} {
		if err := env.repo.DeleteJob(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	env.svc.Forget("agent-1")
	env.svc.Forget("no-such-agent")

	env.svc.mu.Lock()
	_, hasA := env.svc.entries[a.ID]
	_, hasB := env.svc.entries[b.ID]
	_, hasC := env.svc.entries[c.ID]
	env.svc.mu.Unlock()
	if hasA || hasB || !hasC {
		t.Fatalf("应只移除 agent-1 的任务：a=%v b=%v c=%v", hasA, hasB, hasC)
	}

	// 在途的一轮结束后不写回（任务已不存在），也不会复活内存副本。
	close(env.runner.block)
	env.waitIdle(t, a.ID)
	if _, err := env.repo.GetJob(ctx, a.ID); !model.IsCode(err, model.CodeNotFound) {
		t.Fatalf("已删除的任务不应被写回，得到 %v", err)
	}

	// 到点只触发其他智能体的任务。
	env.runner.block = nil
	env.clock.Set(time.Date(2026, 1, 12, 9, 0, 5, 0, time.UTC))
	env.svc.tickOnce(ctx)
	if got := env.runner.next(t); got.ID != c.ID {
		t.Fatalf("应只触发 agent-2 的任务 %s，得到 %s", c.ID, got.ID)
	}
	env.runner.expectNoCall(t)
	env.waitIdle(t, c.ID)
	if err := env.svc.TriggerNow(ctx, b.ID); !model.IsCode(err, model.CodeNotFound) {
		t.Fatalf("被移除的任务手动触发应 not_found，得到 %v", err)
	}
}
