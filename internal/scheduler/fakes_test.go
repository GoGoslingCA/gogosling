package scheduler

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/event"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

// baseTime 是所有测试的起点：2026-01-12（周一）08:59:50 UTC，一月避开夏令时。
var baseTime = time.Date(2026, 1, 12, 8, 59, 50, 0, time.UTC)

// memRepo 是 model.JobRepo 的内存实现。
type memRepo struct {
	mu        sync.Mutex
	jobs      map[string]*model.Job
	updateErr error
	updates   int
}

func newMemRepo() *memRepo { return &memRepo{jobs: make(map[string]*model.Job)} }

func (r *memRepo) CreateJob(_ context.Context, j *model.Job) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.jobs[j.ID]; ok {
		return model.Errorf(model.CodeConflict, "job %s 已存在", j.ID)
	}
	r.jobs[j.ID] = clone(j)
	return nil
}

func (r *memRepo) GetJob(_ context.Context, id string) (*model.Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	if !ok {
		return nil, model.NotFound("job", id)
	}
	return clone(j), nil
}

func (r *memRepo) ListJobs(_ context.Context, agentID string) ([]*model.Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*model.Job
	for _, j := range r.jobs {
		if agentID == "" || j.AgentID == agentID {
			out = append(out, clone(j))
		}
	}
	sort.Slice(out, func(i, k int) bool {
		if !out[i].CreatedAt.Equal(out[k].CreatedAt) {
			return out[i].CreatedAt.Before(out[k].CreatedAt)
		}
		return out[i].ID < out[k].ID
	})
	return out, nil
}

func (r *memRepo) UpdateJob(_ context.Context, j *model.Job) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.updateErr != nil {
		return r.updateErr
	}
	if _, ok := r.jobs[j.ID]; !ok {
		return model.NotFound("job", j.ID)
	}
	r.updates++
	r.jobs[j.ID] = clone(j)
	return nil
}

func (r *memRepo) DeleteJob(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.jobs[id]; !ok {
		return model.NotFound("job", id)
	}
	delete(r.jobs, id)
	return nil
}

// seed 直接写入一条记录，用于模拟进程重启前留下的数据。
func (r *memRepo) seed(j *model.Job) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.jobs[j.ID] = clone(j)
}

// get 读取记录，不存在时让测试失败。
func (r *memRepo) get(t *testing.T, id string) *model.Job {
	t.Helper()
	j, err := r.GetJob(context.Background(), id)
	if err != nil {
		t.Fatalf("读取任务 %s 失败: %v", id, err)
	}
	return j
}

// fakeRunner 记录每次 RunJob 调用，可配置阻塞、返回错误或 panic。
type fakeRunner struct {
	calls     chan model.Job
	err       error
	block     chan struct{}
	panicWith any
}

func newFakeRunner() *fakeRunner { return &fakeRunner{calls: make(chan model.Job, 16)} }

func (r *fakeRunner) RunJob(ctx context.Context, job *model.Job) error {
	r.calls <- *job
	if r.panicWith != nil {
		panic(r.panicWith)
	}
	if r.block != nil {
		select {
		case <-r.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return r.err
}

// next 等待下一次调用；超时则让测试失败。
func (r *fakeRunner) next(t *testing.T) model.Job {
	t.Helper()
	select {
	case j := <-r.calls:
		return j
	case <-time.After(3 * time.Second):
		t.Fatal("等待 Runner 被调用超时")
		return model.Job{}
	}
}

// expectNoCall 断言在短时间内 Runner 没有被调用。
func (r *fakeRunner) expectNoCall(t *testing.T) {
	t.Helper()
	select {
	case j := <-r.calls:
		t.Fatalf("Runner 不应被调用，却收到任务 %s", j.ID)
	case <-time.After(50 * time.Millisecond):
	}
}

// fakeClock 是可手动推进的时钟；Set/Advance 会触发所有到期的 After 通道。
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []fakeTimer
}

type fakeTimer struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock(now time.Time) *fakeClock { return &fakeClock{now: now} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.timers = append(c.timers, fakeTimer{at: c.now.Add(d), ch: ch})
	return ch
}

func (c *fakeClock) Advance(d time.Duration) { c.Set(c.Now().Add(d)) }

func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
	kept := c.timers[:0]
	for _, tm := range c.timers {
		if !tm.at.After(t) {
			tm.ch <- t
			continue
		}
		kept = append(kept, tm)
	}
	c.timers = kept
}

// waitTimers 等待至少 n 个未触发的 After 通道被注册（即调度循环已进入等待）。
func (c *fakeClock) waitTimers(t *testing.T, n int) {
	t.Helper()
	waitFor(t, "调度循环进入等待", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return len(c.timers) >= n
	})
}

// waitFor 轮询直到 cond 成立；超时让测试失败。
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待 %s 超时", what)
}

// testEnv 聚合一次测试需要的协作者。
type testEnv struct {
	svc    *Service
	repo   *memRepo
	runner *fakeRunner
	clock  *fakeClock
	bus    *event.Bus
}

func newTestEnv(t *testing.T, opts Options) *testEnv {
	t.Helper()
	repo := newMemRepo()
	runner := newFakeRunner()
	clock := newFakeClock(baseTime)
	bus := event.NewBus(64)
	if opts.Clock == nil {
		opts.Clock = clock
	}
	if opts.DefaultTimezone == "" {
		opts.DefaultTimezone = "UTC"
	}
	svc, err := New(repo, runner, bus, slog.New(slog.DiscardHandler), opts)
	if err != nil {
		t.Fatalf("创建服务失败: %v", err)
	}
	return &testEnv{svc: svc, repo: repo, runner: runner, clock: clock, bus: bus}
}

// create 用默认合法字段创建任务，覆盖 mutate 中的修改。
func (e *testEnv) create(t *testing.T, mutate func(*JobSpec)) *model.Job {
	t.Helper()
	spec := JobSpec{
		AgentID:  "agent-1",
		Name:     "每日汇报",
		Schedule: "0 9 * * *",
		Kind:     model.JobPushText,
		Input:    "早上好",
		Enabled:  true,
	}
	if mutate != nil {
		mutate(&spec)
	}
	job, err := e.svc.Create(context.Background(), spec)
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	return job
}

// running 报告任务是否仍在执行。
func (e *testEnv) running(id string) bool {
	e.svc.mu.Lock()
	defer e.svc.mu.Unlock()
	return e.svc.isRunningLocked(id)
}

// waitIdle 等待任务的在途执行结束并写回。
func (e *testEnv) waitIdle(t *testing.T, id string) {
	t.Helper()
	waitFor(t, "任务执行结束", func() bool { return !e.running(id) })
}

func ptr[T any](v T) *T { return &v }

func mustEqualTime(t *testing.T, what string, got *time.Time, want time.Time) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s 应为 %s，得到 nil", what, want)
	}
	if !got.Equal(want) {
		t.Fatalf("%s 应为 %s，得到 %s", what, want, *got)
	}
}
