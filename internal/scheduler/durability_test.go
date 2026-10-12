package scheduler

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

func TestDuePersistenceFailureDefersTriggerUntilDurableRecovery(t *testing.T) {
	for _, firstTick := range []bool{true, false} {
		for _, catchUp := range []bool{false, true} {
			name := "按时触发"
			if catchUp {
				name = "错过补跑"
			}
			if firstTick {
				name += "首次节拍"
			} else {
				name += "唤醒节拍"
			}
			t.Run(name, func(t *testing.T) {
				env := newTestEnv(t, Options{})
				env.runner.block = make(chan struct{})
				t.Cleanup(func() {
					close(env.runner.block)
					env.svc.wg.Wait()
				})
				job := env.create(t, func(spec *JobSpec) { spec.MissedPolicy = model.MissedRunOnce })
				if !firstTick {
					env.svc.tickOnce(context.Background())
				}
				now := time.Date(2026, 1, 12, 9, 0, 50, 0, time.UTC)
				next := time.Date(2026, 1, 13, 9, 0, 0, 0, time.UTC)
				if catchUp {
					now = time.Date(2026, 1, 14, 9, 30, 0, 0, time.UTC)
					next = time.Date(2026, 1, 15, 9, 0, 0, 0, time.UTC)
				}
				env.repo.updateErr = errors.New("disk full")
				env.clock.Set(now)
				for range 2 {
					env.svc.tickOnce(context.Background())
					assertUnchangedJob(t, env, job)
				}
				env.runner.expectNoCall(t)
				if env.running(job.ID) || env.bus.LastSeq() != 0 {
					t.Fatal("触发认领未落库时不应标记执行中或发布 job.triggered")
				}

				env.repo.updateErr = nil
				env.svc.tickOnce(context.Background())
				ran := env.runner.next(t)
				if ran.ID != job.ID {
					t.Fatalf("恢复后应执行原到期任务 %s，得到 %s", job.ID, ran.ID)
				}
				mustEqualTime(t, "执行快照的 NextRunAt", ran.NextRunAt, next)
				stored := env.repo.get(t, job.ID)
				mustEqualTime(t, "Runner 执行前已落库的 NextRunAt", stored.NextRunAt, next)
				if stored.LastRunAt != nil {
					t.Fatal("阻塞的 Runner 尚未结束，不应写入执行完成记录")
				}
				env.svc.tickOnce(context.Background())
				env.runner.expectNoCall(t)
				if env.bus.LastSeq() != 1 {
					t.Fatalf("恢复后应只发布一次 job.triggered，得到 %d 次", env.bus.LastSeq())
				}

				// 模拟恢复后重启：使用已落库的认领，不能再次执行同一到期触发。
				restartedRunner := newFakeRunner()
				restarted, err := New(env.repo, restartedRunner, env.bus, nil, Options{
					Clock: env.clock, DefaultTimezone: "UTC",
				})
				if err != nil {
					t.Fatal(err)
				}
				if err := restarted.load(context.Background()); err != nil {
					t.Fatal(err)
				}
				restarted.tickOnce(context.Background())
				restartedRunner.expectNoCall(t)
				if env.bus.LastSeq() != 1 {
					t.Fatal("重启不应再次发布同一触发的事件")
				}
			})
		}
	}
}

func TestMissedSkipPersistenceFailurePreservesDueAndStatus(t *testing.T) {
	env := newTestEnv(t, Options{})
	job := env.create(t, func(spec *JobSpec) { spec.MissedPolicy = model.MissedSkip })
	env.repo.updateErr = errors.New("disk full")
	env.clock.Set(time.Date(2026, 1, 14, 9, 30, 0, 0, time.UTC))
	env.svc.tickOnce(context.Background())
	assertUnchangedJob(t, env, job)
	env.runner.expectNoCall(t)
	if env.bus.LastSeq() != 0 {
		t.Fatal("跳过状态未落库时不应发布触发事件")
	}

	env.repo.updateErr = nil
	env.svc.tickOnce(context.Background())
	stored := env.repo.get(t, job.ID)
	if stored.LastStatus != model.JobRunSkipped || !strings.Contains(stored.LastError, "错过 3 次") {
		t.Fatalf("恢复后应持久化原错过次数的跳过记录，得到 %+v", stored)
	}
	mustEqualTime(t, "恢复跳过后的 NextRunAt", stored.NextRunAt, time.Date(2026, 1, 15, 9, 0, 0, 0, time.UTC))
	updates := env.repo.updates
	env.svc.tickOnce(context.Background())
	if env.repo.updates != updates || env.bus.LastSeq() != 0 {
		t.Fatal("同一周期内不应再次记录跳过或发布触发")
	}
	env.runner.expectNoCall(t)
}

func TestRunningSkipPersistenceFailureRollsBackWithoutNewTrigger(t *testing.T) {
	env := newTestEnv(t, Options{})
	env.runner.block = make(chan struct{})
	t.Cleanup(func() {
		close(env.runner.block)
		env.svc.wg.Wait()
	})
	job := env.create(t, nil)
	if err := env.svc.TriggerNow(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	env.runner.next(t)
	env.repo.updateErr = errors.New("disk full")
	env.clock.Set(time.Date(2026, 1, 12, 9, 0, 5, 0, time.UTC))
	env.svc.tickOnce(context.Background())
	assertUnchangedJob(t, env, job)
	if !env.running(job.ID) || env.bus.LastSeq() != 1 {
		t.Fatal("跳过认领失败应保留原执行且不发布新的触发")
	}
	env.runner.expectNoCall(t)

	env.repo.updateErr = nil
	env.svc.tickOnce(context.Background())
	stored := env.repo.get(t, job.ID)
	if stored.LastStatus != model.JobRunSkipped || !strings.Contains(stored.LastError, "上一轮") {
		t.Fatalf("恢复后应记录上一轮仍执行的跳过状态，得到 %+v", stored)
	}
	mustEqualTime(t, "并发跳过后的 NextRunAt", stored.NextRunAt, time.Date(2026, 1, 13, 9, 0, 0, 0, time.UTC))
	env.runner.expectNoCall(t)
	if env.bus.LastSeq() != 1 {
		t.Fatal("并发跳过不应发布新的触发事件")
	}
}

func TestResyncPersistenceFailureRestoresJobUntilRecovery(t *testing.T) {
	for _, spec := range []string{"0 9 * * *", "@every 1h"} {
		t.Run(spec, func(t *testing.T) {
			env := newTestEnv(t, Options{})
			job := env.create(t, func(jobSpec *JobSpec) { jobSpec.Schedule = spec })
			// 模拟旧持久化记录偏晚；当前节拍尚未到期，应重算并在写入失败时回滚。
			job.NextRunAt = ptr(baseTime.Add(48 * time.Hour))
			env.repo.seed(job)
			env.svc.entries[job.ID].job = clone(job)
			env.repo.updateErr = errors.New("disk full")
			env.clock.Advance(time.Second)
			env.svc.tickOnce(context.Background())
			assertUnchangedJob(t, env, job)
			env.runner.expectNoCall(t)
			if env.bus.LastSeq() != 0 {
				t.Fatal("重算不能触发尚未到期任务")
			}

			// 制造下一次时钟回拨唤醒，重算仍可从原状态重试。
			env.repo.updateErr = nil
			env.clock.Set(baseTime)
			env.svc.tickOnce(context.Background())
			expected := time.Date(2026, 1, 12, 9, 0, 0, 0, time.UTC)
			if spec == "@every 1h" {
				expected = baseTime.Add(time.Hour)
			}
			mustEqualTime(t, "重算恢复后的持久化时间", env.repo.get(t, job.ID).NextRunAt, expected)
			mustEqualTime(t, "重算恢复后的内存时间", env.svc.entries[job.ID].job.NextRunAt, expected)
		})
	}
}

// assertUnchangedJob 断言写库失败后内存和仓储都保留完整的旧状态（包括状态和更新时间）。
func assertUnchangedJob(t *testing.T, env *testEnv, previous *model.Job) {
	t.Helper()
	env.svc.mu.Lock()
	inMemory := clone(env.svc.entries[previous.ID].job)
	env.svc.mu.Unlock()
	if !reflect.DeepEqual(inMemory, previous) {
		t.Errorf("失败后的内存任务应完整回滚，得到 %+v，原状态 %+v", inMemory, previous)
	}
	if persisted := env.repo.get(t, previous.ID); !reflect.DeepEqual(persisted, previous) {
		t.Errorf("失败后的持久化任务不应变化，得到 %+v，原状态 %+v", persisted, previous)
	}
}
