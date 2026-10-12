package session

import (
	"context"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

func TestNoteApprovalTracksAwaitingStateWithoutPublishing(t *testing.T) {
	h := newHarness(t, Options{})
	sess := h.session()
	run := h.send(sess.ID, "需要审批的命令")

	// queued 时收到审批事件不做迁移（状态机不允许 queued → awaiting_approval）。
	if err := h.svc.NoteApproval(run.ID, true); err != nil {
		t.Fatalf("NoteApproval 失败: %v", err)
	}
	if got := mustRun(t, h.store, run.ID); got.Status != model.RunQueued {
		t.Fatalf("queued 运行不应进入 awaiting_approval，得到 %s", got.Status)
	}

	h.rt.emit(t, run.ID, model.RunEvent{Type: model.EventRunStarted})
	sub := h.subscribe()
	if err := h.svc.NoteApproval(run.ID, true); err != nil {
		t.Fatalf("NoteApproval 失败: %v", err)
	}
	if got := mustRun(t, h.store, run.ID); got.Status != model.RunAwaitingApproval {
		t.Fatalf("审批挂起时数据库中的状态应为 awaiting_approval，得到 %s", got.Status)
	}
	if got, _ := h.svc.GetRun(context.Background(), run.ID); got.Status != model.RunAwaitingApproval {
		t.Fatalf("GetRun 应返回 awaiting_approval，得到 %s", got.Status)
	}
	// 重复的 requested 是空操作。
	if err := h.svc.NoteApproval(run.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.NoteApproval(run.ID, false); err != nil {
		t.Fatalf("NoteApproval 失败: %v", err)
	}
	if got := mustRun(t, h.store, run.ID); got.Status != model.RunRunning {
		t.Fatalf("审批结束后应回到 running，得到 %s", got.Status)
	}
	if evs := drain(sub); len(evs) != 0 {
		t.Fatalf("NoteApproval 不应发布任何事件（审批事件已由 permission 发布），得到 %v", eventTypes(evs))
	}

	// running 时收到 resolved 不改变状态。
	if err := h.svc.NoteApproval(run.ID, false); err != nil {
		t.Fatal(err)
	}
	if got := mustRun(t, h.store, run.ID); got.Status != model.RunRunning {
		t.Fatalf("running 时收到 resolved 应保持 running，得到 %s", got.Status)
	}

	// 终态（已从运行表移除）忽略；未知 runID 是 not_found。
	h.rt.emit(t, run.ID, model.RunEvent{Type: model.EventRunCompleted})
	if err := h.svc.NoteApproval(run.ID, false); err != nil {
		t.Fatalf("终态运行应被忽略，得到 %v", err)
	}
	if got := mustRun(t, h.store, run.ID); got.Status != model.RunCompleted {
		t.Fatalf("终态不应被改写，得到 %s", got.Status)
	}
	if err := h.svc.NoteApproval("no-such-run", true); !model.IsCode(err, model.CodeNotFound) {
		t.Fatalf("未知运行应返回 not_found，得到 %v", err)
	}
}

func TestNoteApprovalIgnoredAfterLocalTerminal(t *testing.T) {
	h := newHarness(t, Options{})
	h.rt.autoCancel = false
	sess := h.session()
	run := h.send(sess.ID, "x")
	h.rt.emit(t, run.ID, model.RunEvent{Type: model.EventRunStarted})
	if err := h.svc.NoteApproval(run.ID, true); err != nil {
		t.Fatal(err)
	}
	h.rt.emit(t, run.ID, model.RunEvent{Type: model.EventRunFailed, Error: "boom"})
	// 运行终结后审批才因取消而被 resolve：不得把失败的运行改回 running。
	if err := h.svc.NoteApproval(run.ID, false); err != nil {
		t.Fatalf("终态后的 resolved 应被忽略，得到 %v", err)
	}
	if got := mustRun(t, h.store, run.ID); got.Status != model.RunFailed {
		t.Fatalf("终态不应被改写，得到 %s", got.Status)
	}
}
