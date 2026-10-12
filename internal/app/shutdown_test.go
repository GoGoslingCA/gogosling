package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// recordingStore 模拟数据库：Close 之后的写入被记为"写到已关闭的数据库"。
type recordingStore struct {
	mu           sync.Mutex
	closed       bool
	writes       int
	lateWrites   []string
	closeCalls   int
	closedBefore []string // Close 时已经完成写回的循环
}

func (s *recordingStore) write(who string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		s.lateWrites = append(s.lateWrites, who)
		return
	}
	s.writes++
	s.closedBefore = append(s.closedBefore, who)
}

func (s *recordingStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.closeCalls++
	return nil
}

func (s *recordingStore) snapshot() (late []string, done []string, closes int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.lateWrites...), append([]string(nil), s.closedBefore...), s.closeCalls
}

// stuckGateway 模拟回复队列卡住的网关：Stop 一直等到 ctx 结束；ignoreCtx 时连 ctx 也不理会。
type stuckGateway struct {
	ignoreCtx bool
	release   chan struct{}

	mu     sync.Mutex
	budget time.Duration
}

func (g *stuckGateway) Stop(ctx context.Context) error {
	g.mu.Lock()
	if dl, ok := ctx.Deadline(); ok {
		g.budget = time.Until(dl)
	}
	g.mu.Unlock()
	if g.ignoreCtx {
		<-g.release
		return nil
	}
	<-ctx.Done()
	return model.Wrap(model.CodeTimeout, "回复队列未发完", ctx.Err())
}

func (g *stuckGateway) gotBudget() time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.budget
}

// slowEngine 模拟工作进程的优雅停止：需要 need 才能结束，结束前对在途运行写入终态（failRuns → session → 数据库）。
type slowEngine struct {
	need  time.Duration
	store *recordingStore

	once   sync.Once
	done   chan struct{}
	mu     sync.Mutex
	budget time.Duration
	calls  int
}

func (e *slowEngine) Stop(ctx context.Context) error {
	e.mu.Lock()
	e.calls++
	if dl, ok := ctx.Deadline(); ok && e.calls == 1 {
		e.budget = time.Until(dl)
	}
	e.mu.Unlock()
	e.once.Do(func() {
		go func() {
			time.Sleep(e.need)
			e.store.write("engine")
			close(e.done)
		}()
	})
	select {
	case <-e.done:
		return nil
	case <-ctx.Done():
		return model.Wrap(model.CodeTimeout, "等待工作进程停止超时", ctx.Err())
	}
}

func (e *slowEngine) firstBudget() time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.budget
}

// writeBackLoop 模拟调度循环：取消后还需要 need 才能把任务状态写回数据库。
func writeBackLoop(st *recordingStore, who string, need time.Duration) *bgLoop {
	return goLoop(context.Background(), func(ctx context.Context) {
		<-ctx.Done()
		time.Sleep(need)
		st.write(who)
	})
}

// CONC-5 回归：网关回复队列卡住时只占总预算的 1/4，调度器的写回仍在 store.Close 之前完成。
func TestShutdownGatewayStuckDoesNotStarveSchedulerWriteBack(t *testing.T) {
	st := &recordingStore{}
	gw := &stuckGateway{}
	budget := 400 * time.Millisecond
	rt := &runtimeState{
		log:     discardLogger(),
		store:   st,
		gateway: gw,
		sched:   writeBackLoop(st, "scheduler", 150*time.Millisecond),
	}

	start := time.Now()
	err := rt.shutdown(budget)
	elapsed := time.Since(start)

	if got := gw.gotBudget(); got <= 0 || got > budget/gatewayBudgetDivisor+20*time.Millisecond {
		t.Errorf("网关应最多拿到总预算的 1/%d（%v），实际 %v", gatewayBudgetDivisor, budget/gatewayBudgetDivisor, got)
	}
	if !model.IsCode(err, model.CodeTimeout) {
		t.Errorf("卡住的网关应作为关闭错误报告，得到 %v", err)
	}
	late, done, closes := st.snapshot()
	if len(late) != 0 {
		t.Fatalf("store.Close 之后不应再有写库：%v", late)
	}
	if len(done) != 1 || done[0] != "scheduler" || closes != 1 {
		t.Fatalf("调度器应在关闭数据库之前完成写回：done=%v closes=%d", done, closes)
	}
	if elapsed > budget+time.Second {
		t.Errorf("关闭不应远超预算：%v", elapsed)
	}
}

// 网关的 Stop 完全不理会 ctx、调度器写回比剩余预算还慢：组合根不被网关拖住，
// 并在关闭数据库前用固定上限再等一次调度器。
func TestShutdownDrainsWritersBeforeClosingStore(t *testing.T) {
	st := &recordingStore{}
	gw := &stuckGateway{ignoreCtx: true, release: make(chan struct{})}
	t.Cleanup(func() { close(gw.release) })
	budget := 200 * time.Millisecond
	rt := &runtimeState{
		log:     discardLogger(),
		store:   st,
		gateway: gw,
		sched:   writeBackLoop(st, "scheduler", 400*time.Millisecond),
		perm:    writeBackLoop(st, "permission", 10*time.Millisecond),
	}

	done := make(chan error, 1)
	go func() { done <- rt.shutdown(budget) }()
	select {
	case <-done:
	case <-time.After(drainCap + budget + 2*time.Second):
		t.Fatal("网关不理会 ctx 时关闭流程不应被它永久拖住")
	}
	late, wrote, closes := st.snapshot()
	if len(late) != 0 {
		t.Fatalf("store.Close 之后不应再有写库：%v", late)
	}
	if len(wrote) != 2 || closes != 1 {
		t.Fatalf("调度器与权限扫描都应在关闭数据库之前写回：%v closes=%d", wrote, closes)
	}
}

// 总预算几乎耗尽时，engine 仍至少拿到 ShutdownGrace + 强制结束等待，它对在途运行的终态写入先于 store.Close。
func TestShutdownGivesEngineItsFloor(t *testing.T) {
	st := &recordingStore{}
	eng := &slowEngine{need: 300 * time.Millisecond, store: st, done: make(chan struct{})}
	rt := &runtimeState{
		log:     discardLogger(),
		store:   st,
		gateway: &stuckGateway{},
		engine:  eng,
	}
	if err := rt.shutdown(80 * time.Millisecond); err != nil && !model.IsCode(err, model.CodeTimeout) {
		t.Fatalf("意外的关闭错误: %v", err)
	}
	if got := eng.firstBudget(); got < engineStopFloor-time.Second {
		t.Errorf("engine 应至少拿到 %v，实际 %v", engineStopFloor, got)
	}
	late, wrote, _ := st.snapshot()
	if len(late) != 0 || len(wrote) != 1 || wrote[0] != "engine" {
		t.Fatalf("engine 的终态写入应先于 store.Close：wrote=%v late=%v", wrote, late)
	}
}

// 未启动任何模块（启动在第一步就失败）时 shutdown 是空操作。
func TestShutdownWithNothingStarted(t *testing.T) {
	if err := (&runtimeState{log: discardLogger()}).shutdown(time.Second); err != nil {
		t.Fatalf("没有模块时关闭应成功，得到 %v", err)
	}
}

func TestCallWithinReturnsOnDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	block := make(chan struct{})
	defer close(block)
	err := callWithin(ctx, func(context.Context) error { <-block; return nil })
	if !model.IsCode(err, model.CodeTimeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("超时应返回 CodeTimeout，得到 %v", err)
	}
	want := errors.New("boom")
	if err := callWithin(context.Background(), func(context.Context) error { return want }); !errors.Is(err, want) {
		t.Fatalf("应原样返回 fn 的错误，得到 %v", err)
	}
}
