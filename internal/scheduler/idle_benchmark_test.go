package scheduler

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// BenchmarkIdleTick 对照未变更任务的正常空闲节拍；初始化、启动重算和实际等待不计入。
func BenchmarkIdleTick(b *testing.B) {
	for _, size := range []int{100, 1000, 10000} {
		for _, previous := range []bool{true, false} {
			name := "current"
			if previous {
				name = "previous"
			}
			b.Run(fmt.Sprintf("%s/jobs=%d", name, size), func(b *testing.B) {
				svc := benchmarkService(b, size)
				svc.tickOnce(context.Background())
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					if previous {
						previousTickOnce(svc)
					} else {
						svc.tickOnce(context.Background())
					}
				}
			})
		}
	}
}

// BenchmarkMixedDueTick 对照大量未来任务和少量到期任务；到期项按 skip 持久化，避免 goroutine 噪声。
// 每轮恢复原触发点，缓存失效与触发处理都计入，但不测磁盘（memRepo）。
func BenchmarkMixedDueTick(b *testing.B) {
	for _, previous := range []bool{true, false} {
		name := "current"
		if previous {
			name = "previous"
		}
		b.Run(name, func(b *testing.B) {
			svc := benchmarkService(b, 10000)
			svc.tickOnce(context.Background())
			due := baseTime.Add(-2 * time.Hour)
			var targets []*entry
			for i := range 10 {
				targets = append(targets, svc.entries[fmt.Sprintf("job-%05d", i)])
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				for _, e := range targets {
					e.job.NextRunAt = &due
				}
				svc.nextDueValid = false
				if previous {
					previousTickOnce(svc)
				} else {
					svc.tickOnce(context.Background())
				}
			}
		})
	}
}

func BenchmarkMissedFixedInterval(b *testing.B) {
	sched, err := ParseSchedule("@every 1m", "UTC", time.UTC)
	if err != nil {
		b.Fatal(err)
	}
	due, now := baseTime, baseTime.Add(48*time.Hour)
	for _, previous := range []bool{true, false} {
		name := "current"
		if previous {
			name = "previous"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				var count int
				if previous {
					count = iterativeMissedBetween(sched, due, now)
				} else {
					count = sched.missedBetween(due, now)
				}
				if count != maxMissedScan {
					b.Fatalf("错过计数应封顶于 %d，得到 %d", maxMissedScan, count)
				}
			}
		})
	}
}

func benchmarkService(b *testing.B, size int) *Service {
	b.Helper()
	repo := newMemRepo()
	svc, err := New(repo, newFakeRunner(), nil, nil, Options{Clock: newFakeClock(baseTime), DefaultTimezone: "UTC"})
	if err != nil {
		b.Fatal(err)
	}
	sched, err := ParseSchedule("@every 1h", "UTC", time.UTC)
	if err != nil {
		b.Fatal(err)
	}
	for i := range size {
		job := &model.Job{
			ID: fmt.Sprintf("job-%05d", i), AgentID: "a", Name: "任务", Input: "x", Schedule: "@every 1h",
			Kind: model.JobPushText, MissedPolicy: model.MissedSkip, Enabled: true,
			NextRunAt: ptr(baseTime.Add(time.Hour)), CreatedAt: baseTime.Add(time.Duration(i) * time.Second), UpdatedAt: baseTime,
		}
		svc.entries[job.ID] = &entry{job: job, sched: sched}
		repo.seed(job)
	}
	return svc
}

// previousTickOnce 保留旧版正常节拍的全量排序扫描；不用于生产，首次/唤醒由双方同样的初始化排除。
func previousTickOnce(s *Service) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastTick = now
	for _, id := range s.sortedIDsLocked() {
		e := s.entries[id]
		if !e.job.Enabled || e.sched == nil || e.job.NextRunAt == nil || e.job.NextRunAt.After(now) {
			continue
		}
		s.handleDueLocked(context.Background(), e, now)
	}
}
