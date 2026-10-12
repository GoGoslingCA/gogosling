package session

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

func waitForDispatch(t *testing.T, h *harness, count int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for h.rt.requestCount() != count && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := h.rt.requestCount(); got != count {
		t.Fatalf("期望派发 %d 次，实际 %d", count, got)
	}
}
func isolatedSession(t *testing.T, h *harness, group string) *model.Session {
	t.Helper()
	a := *h.agent
	a.ID = model.NewID()
	a.WorkspaceDir = group
	h.agents.set(&a)
	s, err := h.svc.Create(context.Background(), a.ID, model.OriginDesktop, "")
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func groupByWorkspace(_ context.Context, a *model.Agent) (string, error) { return a.WorkspaceDir, nil }

type historyCounter struct {
	Store
	reads atomic.Int32
}

func (s *historyCounter) ListMessages(ctx context.Context, id string, after int64, limit int) ([]*model.Message, error) {
	s.reads.Add(1)
	return s.Store.ListMessages(ctx, id, after, limit)
}

func Test资源队列只在派发时装载历史并限制等待数量(t *testing.T) {
	h := newHarness(t, Options{MaxParallelRuns: 2, MaxQueuedRuns: 2, WorkspaceGroup: groupByWorkspace})
	counter := &historyCounter{Store: h.store}
	h.svc.store = counter
	a, b, c, d, e := isolatedSession(t, h, "a"), isolatedSession(t, h, "b"), isolatedSession(t, h, "c"), isolatedSession(t, h, "d"), isolatedSession(t, h, "e")
	first := h.send(a.ID, "a")
	h.send(b.ID, "b")
	reads := counter.reads.Load()
	h.send(c.ID, "c")
	h.send(d.ID, "d")
	if h.rt.requestCount() != 2 || counter.reads.Load() != reads {
		t.Fatal("等待中的任务不得启动引擎或装载历史")
	}
	if r, err := h.svc.Send(context.Background(), SendRequest{SessionID: e.ID, Text: "e"}); r != nil || !model.IsCode(err, model.CodeConflict) {
		t.Fatalf("超出容量应在落盘前拒绝：%+v %v", r, err)
	}
	msgs, _ := h.store.ListMessages(context.Background(), e.ID, 0, 10)
	if len(msgs) != 0 {
		t.Fatal("排队拒绝不能写入用户消息")
	}
	h.rt.emit(t, first.ID, model.RunEvent{Type: model.EventRunCompleted})
	waitForDispatch(t, h, 3)
	if h.rt.lastRequest(t).SessionID != c.ID {
		t.Fatal("同优先级应按入队顺序执行")
	}
}
func Test共享工作区串行且不阻塞其他工作区(t *testing.T) {
	h := newHarness(t, Options{MaxParallelRuns: 2, MaxQueuedRuns: 2, WorkspaceGroup: groupByWorkspace})
	a, b, c := isolatedSession(t, h, "shared"), isolatedSession(t, h, "shared"), isolatedSession(t, h, "other")
	first := h.send(a.ID, "a")
	h.send(b.ID, "b")
	h.send(c.ID, "c")
	waitForDispatch(t, h, 2)
	if h.rt.lastRequest(t).SessionID != c.ID {
		t.Fatal("独立工作区应可使用空闲执行槽")
	}
	h.rt.emit(t, first.ID, model.RunEvent{Type: model.EventRunCompleted})
	waitForDispatch(t, h, 3)
	if h.rt.lastRequest(t).SessionID != b.ID {
		t.Fatal("工作区释放后应派发等待任务")
	}
}
func Test同一工作区也不能突破等待上限(t *testing.T) {
	h := newHarness(t, Options{MaxParallelRuns: 8, MaxQueuedRuns: 2, WorkspaceGroup: groupByWorkspace})
	for i := 0; i < 3; i++ {
		h.send(isolatedSession(t, h, "shared").ID, "任务")
	}
	if _, err := h.svc.Send(context.Background(), SendRequest{SessionID: isolatedSession(t, h, "shared").ID, Text: "超量"}); !model.IsCode(err, model.CodeConflict) {
		t.Fatalf("等待上限应独立于并发数：%v", err)
	}
	if got := h.svc.Resources(); got.Running != 1 || got.Queued != 2 {
		t.Fatalf("计数不准确：%+v", got)
	}
}
func Test取消排队不调用运行时且关闭服务释放队列(t *testing.T) {
	h := newHarness(t, Options{MaxParallelRuns: 1})
	first := h.send(h.session().ID, "先执行")
	second := h.send(h.session().ID, "排队")
	r, err := h.svc.Cancel(context.Background(), second.ID)
	if err != nil || r.Status != model.RunCancelled {
		t.Fatalf("取消失败：%v %+v", err, r)
	}
	if h.rt.cancelCount() != 0 || h.rt.requestCount() != 1 || h.svc.Resources().Queued != 0 {
		t.Fatal("排队取消不得启动或取消引擎任务")
	}
	third := h.send(h.session().ID, "关机前排队")
	h.svc.Close()
	r, _ = h.svc.GetRun(context.Background(), third.ID)
	if r.Status != model.RunCancelled {
		t.Fatal("关机应终结排队状态")
	}
	if h.rt.requestCount() != 1 {
		t.Fatal("关机不能再派发等待任务")
	}
	h.rt.emit(t, first.ID, model.RunEvent{Type: model.EventRunCancelled})
}
func Test降低上限不中断任务且审批占用执行槽(t *testing.T) {
	h := newHarness(t, Options{MaxParallelRuns: 2})
	a, b, c := isolatedSession(t, h, "a"), isolatedSession(t, h, "b"), isolatedSession(t, h, "c")
	first := h.send(a.ID, "a")
	second := h.send(b.ID, "b")
	h.send(c.ID, "c")
	h.rt.emit(t, first.ID, model.RunEvent{Type: model.EventRunStarted})
	_ = h.svc.NoteApproval(first.ID, true)
	if err := h.svc.SetParallelLimit(1); err != nil {
		t.Fatal(err)
	}
	if got := h.svc.Resources(); got.Running != 2 || got.AwaitingApproval != 1 || h.rt.cancelCount() != 0 {
		t.Fatalf("调低上限不得中断在途任务：%+v", got)
	}
	h.rt.emit(t, second.ID, model.RunEvent{Type: model.EventRunCompleted})
	if h.rt.requestCount() != 2 {
		t.Fatal("审批中任务仍占槽，不应派发新任务")
	}
	h.rt.emit(t, first.ID, model.RunEvent{Type: model.EventRunCompleted})
	waitForDispatch(t, h, 3)
}
func Test前台优先与久候提升(t *testing.T) {
	for _, aged := range []bool{false, true} {
		t.Run(map[bool]string{false: "前台优先", true: "久候提升"}[aged], func(t *testing.T) {
			var seconds atomic.Int64
			h := newHarness(t, Options{MaxParallelRuns: 1, Now: func() time.Time { return time.Unix(seconds.Load(), 0) }})
			first := h.send(isolatedSession(t, h, "first").ID, "first")
			job, _ := h.svc.Send(context.Background(), SendRequest{SessionID: isolatedSession(t, h, "job").ID, Text: "job", Trigger: model.TriggerJob})
			if aged {
				seconds.Store(31)
			}
			user := h.send(isolatedSession(t, h, "user").ID, "user")
			h.rt.emit(t, first.ID, model.RunEvent{Type: model.EventRunCompleted})
			waitForDispatch(t, h, 2)
			want := user.ID
			if aged {
				want = job.ID
			}
			if h.rt.lastRequest(t).RunID != want {
				t.Fatal("优先级顺序不正确")
			}
		})
	}
}
func Test排队启动失败释放槽供后续任务使用(t *testing.T) {
	h := newHarness(t, Options{MaxParallelRuns: 1})
	first := h.send(isolatedSession(t, h, "a").ID, "a")
	second := h.send(isolatedSession(t, h, "b").ID, "b")
	third := h.send(isolatedSession(t, h, "c").ID, "c")
	h.rt.mu.Lock()
	h.rt.startErr = errors.New("模拟启动失败")
	h.rt.mu.Unlock()
	h.rt.emit(t, first.ID, model.RunEvent{Type: model.EventRunCompleted})
	waitForDispatch(t, h, 3)
	deadline := time.Now().Add(2 * time.Second)
	for h.svc.Resources().Running != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	for _, id := range []string{second.ID, third.ID} {
		r, _ := h.svc.GetRun(context.Background(), id)
		if r.Status != model.RunFailed {
			t.Fatalf("应保存失败状态：%+v", r)
		}
	}
	if h.svc.Resources().Running != 0 || h.svc.Resources().Queued != 0 {
		t.Fatal("失败不得泄漏执行槽")
	}
}
