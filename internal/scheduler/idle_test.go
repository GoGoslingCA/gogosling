package scheduler

import (
	"context"
	"errors"
	"math/rand"
	"reflect"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

func Test空闲循环新增修改启停后仍按时执行(t *testing.T) {
	env := newTestEnv(t, Options{Tick: time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- env.svc.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("调度循环应正常退出：%v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("调度循环未能退出")
		}
	})
	env.clock.waitTimers(t, 1)
	// 已缓存“没有任务”，新增后必须在下一次节拍识别，不能一直空闲。
	job := env.create(t, func(spec *JobSpec) { spec.Schedule = "@every 1h" })
	advance := func() {
		t.Helper()
		env.clock.Advance(time.Second)
		env.clock.waitTimers(t, 1)
	}
	advance()
	if _, err := env.svc.Update(ctx, job.ID, JobPatch{Schedule: ptr("0 9 * * *")}); err != nil {
		t.Fatal(err)
	}
	advance()
	if _, err := env.svc.Disable(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		advance()
	}
	if _, err := env.svc.Enable(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		advance()
	}
	select {
	case <-env.runner.calls:
		t.Fatal("未到 09:00 不能提前触发")
	default:
	}
	advance()
	if ran := env.runner.next(t); ran.ID != job.ID {
		t.Fatalf("到点应执行更新后的任务 %s，得到 %s", job.ID, ran.ID)
	}
	env.waitIdle(t, job.ID)
	for range 2 {
		advance()
	}
	env.runner.expectNoCall(t)
	if got := env.repo.get(t, job.ID); got.LastStatus != model.JobRunOK {
		t.Fatalf("任务应正常完成，得到 %q", got.LastStatus)
	}
}

func Test空闲缓存删除和忘记任务不影响剩余任务(t *testing.T) {
	env := newTestEnv(t, Options{})
	deleted := env.create(t, nil)
	forgotten := env.create(t, func(spec *JobSpec) { spec.AgentID = "forgotten" })
	kept := env.create(t, func(spec *JobSpec) { spec.AgentID = "kept" })
	env.svc.tickOnce(context.Background())
	if err := env.svc.Delete(context.Background(), deleted.ID); err != nil {
		t.Fatal(err)
	}
	if err := env.repo.DeleteJob(context.Background(), forgotten.ID); err != nil {
		t.Fatal(err)
	}
	env.svc.Forget("forgotten")
	env.clock.Advance(10 * time.Second)
	env.svc.tickOnce(context.Background())
	if ran := env.runner.next(t); ran.ID != kept.ID {
		t.Fatalf("只能触发保留任务 %s，得到 %s", kept.ID, ran.ID)
	}
	env.waitIdle(t, kept.ID)
	env.runner.expectNoCall(t)
}

func Test正常节拍写库失败不被空闲缓存吞掉(t *testing.T) {
	env := newTestEnv(t, Options{})
	job := env.create(t, nil)
	env.svc.tickOnce(context.Background())
	env.repo.updateErr = errors.New("disk full")
	env.clock.Advance(10 * time.Second)
	env.svc.tickOnce(context.Background())
	assertUnchangedJob(t, env, job)
	env.runner.expectNoCall(t)
	env.repo.updateErr = nil
	env.clock.Advance(time.Second)
	env.svc.tickOnce(context.Background())
	if ran := env.runner.next(t); ran.ID != job.ID {
		t.Fatalf("恢复后应重试原到期任务 %s，得到 %s", job.ID, ran.ID)
	}
	env.waitIdle(t, job.ID)
	env.svc.tickOnce(context.Background())
	env.runner.expectNoCall(t)
}

func Test按需载入较早任务会使空闲缓存失效(t *testing.T) {
	env := newTestEnv(t, Options{})
	env.create(t, func(spec *JobSpec) { spec.Schedule = "@every 1h" })
	env.svc.tickOnce(context.Background())
	job := &model.Job{ID: model.NewID(), AgentID: "a", Name: "已有任务", Schedule: "@daily", Kind: model.JobPushText,
		Input: "x", Enabled: true, MissedPolicy: model.MissedSkip, NextRunAt: ptr(baseTime.Add(10 * time.Second)), CreatedAt: baseTime, UpdatedAt: baseTime}
	env.repo.seed(job)
	if _, err := env.svc.Update(context.Background(), job.ID, JobPatch{Name: ptr("重命名")}); err != nil {
		t.Fatal(err)
	}
	env.clock.Advance(10 * time.Second)
	env.svc.tickOnce(context.Background())
	if ran := env.runner.next(t); ran.ID != job.ID {
		t.Fatalf("应执行按需载入的较早任务 %s，得到 %s", job.ID, ran.ID)
	}
	env.waitIdle(t, job.ID)
}

func Test到期子集排序保留创建时间和同时间ID顺序(t *testing.T) {
	env := newTestEnv(t, Options{})
	for _, job := range []*model.Job{
		{ID: "late", CreatedAt: baseTime.Add(time.Second), NextRunAt: ptr(baseTime)},
		{ID: "b", CreatedAt: baseTime, NextRunAt: ptr(baseTime)},
		{ID: "future", CreatedAt: baseTime.Add(-time.Hour), NextRunAt: ptr(baseTime.Add(time.Hour))},
		{ID: "a", CreatedAt: baseTime, NextRunAt: ptr(baseTime)},
	} {
		job.AgentID, job.Name, job.Input = "a", "任务", "x"
		job.Schedule, job.Kind, job.MissedPolicy, job.Enabled = "@every 1h", model.JobPushText, model.MissedSkip, true
		env.repo.seed(job)
	}
	if err := env.svc.load(context.Background()); err != nil {
		t.Fatal(err)
	}
	sub := env.bus.Subscribe(context.Background(), 0, 8, nil)
	defer sub.Cancel()
	env.svc.tickOnce(context.Background())
	var got []string
	for range 3 {
		select {
		case ev := <-sub.C:
			got = append(got, ev.Payload.(model.Job).ID)
		case <-time.After(3 * time.Second):
			t.Fatal("到期任务没有发布触发事件")
		}
		env.runner.next(t)
	}
	env.svc.wg.Wait()
	if !reflect.DeepEqual(got, []string{"a", "b", "late"}) {
		t.Fatalf("过滤未来任务后应保留原顺序，得到 %v", got)
	}
	env.runner.expectNoCall(t)
}

func Test固定间隔算术计数与原推演边界一致(t *testing.T) {
	specs := []string{"@every 1m", "@every 60.9s", "@every 90s", "@every 1h", "@every 8760h", "@every 2500000h"}
	rng := rand.New(rand.NewSource(73))
	for _, spec := range specs {
		sched, err := ParseSchedule(spec, "America/Toronto", time.UTC)
		if err != nil {
			t.Fatal(err)
		}
		for i := range 180 {
			due := time.Date(2026, time.Month(1+rng.Intn(12)), 1+rng.Intn(28), rng.Intn(24), rng.Intn(60), rng.Intn(60), rng.Intn(1e9), time.UTC)
			now := sched.Next(due)
			for range rng.Intn(1100) {
				now = sched.Next(now)
			}
			if i%3 == 0 {
				now = now.Add(-time.Nanosecond)
			} else if i%3 == 1 {
				now = now.Add(time.Nanosecond)
			}
			if got, want := sched.missedBetween(due, now), iterativeMissedBetween(sched, due, now); got != want {
				t.Fatalf("%s 在 %s 到 %s 应计数 %d，得到 %d", spec, due, now, want, got)
			}
		}
		for _, now := range []time.Time{baseTime.Add(-time.Second), baseTime, time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)} {
			if got, want := sched.missedBetween(baseTime, now), iterativeMissedBetween(sched, baseTime, now); got != want {
				t.Fatalf("%s 的倒退和超长时间边界应与旧实现一致：得到 %d，期望 %d", spec, got, want)
			}
		}
	}
}

// iterativeMissedBetween 保留原有有界推演，供算法差分测试及基准比较。
func iterativeMissedBetween(s *Schedule, due, now time.Time) int {
	count, next := 0, due
	for count < maxMissedScan {
		next = s.Next(next)
		if next.IsZero() || next.After(now) {
			break
		}
		count++
	}
	return count
}

func Test全部停用后重新启用不会被空缓存遗漏(t *testing.T) {
	env := newTestEnv(t, Options{})
	job := env.create(t, func(spec *JobSpec) { spec.Enabled = false })
	env.svc.tickOnce(context.Background())
	env.clock.Advance(time.Second)
	env.svc.tickOnce(context.Background())
	if _, err := env.svc.Enable(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	env.clock.Advance(8 * time.Second)
	env.svc.tickOnce(context.Background())
	env.runner.expectNoCall(t)
	env.clock.Advance(time.Second)
	env.svc.tickOnce(context.Background())
	if ran := env.runner.next(t); ran.ID != job.ID {
		t.Fatalf("应触发重新启用的任务 %s，得到 %s", job.ID, ran.ID)
	}
	env.waitIdle(t, job.ID)
}

func Test零时间旧记录写入失败后仍会重试(t *testing.T) {
	env := newTestEnv(t, Options{})
	job := env.create(t, func(spec *JobSpec) {
		spec.Schedule = "@every 1m"
		spec.MissedPolicy = model.MissedRunOnce
	})
	job.NextRunAt = ptr(time.Time{})
	env.repo.seed(job)
	if err := env.svc.load(context.Background()); err != nil {
		t.Fatal(err)
	}
	env.repo.updateErr = errors.New("disk full")
	env.svc.tickOnce(context.Background())
	assertUnchangedJob(t, env, job)
	env.repo.updateErr = nil
	env.clock.Advance(time.Second)
	env.svc.tickOnce(context.Background())
	if ran := env.runner.next(t); ran.ID != job.ID {
		t.Fatalf("时间零值不能误作没有待调度任务，应重试 %s，得到 %s", job.ID, ran.ID)
	}
	env.waitIdle(t, job.ID)
}
