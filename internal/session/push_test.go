package session

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

func TestPushAppendsAssistantMessageAndPublishes(t *testing.T) {
	h := newHarness(t, Options{})
	sess := h.session()
	sub := h.subscribe()

	msg, err := h.svc.Push(context.Background(), sess.ID, "每日提醒：喝水")
	if err != nil {
		t.Fatalf("推送失败: %v", err)
	}
	if msg.Role != model.RoleAssistant || msg.RunID != "" || msg.Text() != "每日提醒：喝水" {
		t.Fatalf("推送消息应为无运行的助手文本消息，得到 %+v", msg)
	}
	var meta map[string]any
	if err := json.Unmarshal(msg.Meta, &meta); err != nil || meta[MetaPushed] != true {
		t.Fatalf("推送消息的 Meta 应标记 pushed=true，得到 %s", msg.Meta)
	}

	msgs := mustMessages(t, h.store, sess.ID)
	if len(msgs) != 1 || msgs[0].ID != msg.ID || msgs[0].Seq != 1 {
		t.Fatalf("应持久化一条推送消息，得到 %+v", msgs)
	}

	types := eventTypes(drain(sub))
	if len(types) != 2 || types[0] != string(model.EventMessageCreated) || types[1] != EventSessionUpdated {
		t.Fatalf("应依次发布 message.created 与 session.updated，得到 %v", types)
	}
}

func TestPushValidatesInputAndState(t *testing.T) {
	h := newHarness(t, Options{})
	ctx := context.Background()
	sess := h.session()

	if _, err := h.svc.Push(ctx, sess.ID, "  \n"); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("空文本应返回 invalid，得到 %v", err)
	}
	if _, err := h.svc.Push(ctx, "", "x"); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("缺少会话 ID 应返回 invalid，得到 %v", err)
	}
	if _, err := h.svc.Push(ctx, model.NewID(), "x"); !model.IsCode(err, model.CodeNotFound) {
		t.Fatalf("不存在的会话应返回 not_found，得到 %v", err)
	}

	run := h.send(sess.ID, "你好")
	_, err := h.svc.Push(ctx, sess.ID, "提醒")
	if !model.IsCode(err, model.CodeConflict) {
		t.Fatalf("有在途运行时推送应冲突，得到 %v", err)
	}
	var me *model.Error
	if !asModelError(err, &me) || me.Details["run_id"] != run.ID {
		t.Fatalf("冲突错误应带 run_id，得到 %v", err)
	}
	h.rt.emit(t, run.ID, model.RunEvent{Type: model.EventRunCompleted})

	archived := model.SessionArchived
	if _, err := h.svc.Update(ctx, sess.ID, SessionUpdate{Status: &archived}); err != nil {
		t.Fatalf("归档失败: %v", err)
	}
	if _, err := h.svc.Push(ctx, sess.ID, "提醒"); !model.IsCode(err, model.CodeConflict) {
		t.Fatalf("向已归档会话推送应冲突，得到 %v", err)
	}

	h.svc.Close()
	if _, err := h.svc.Push(ctx, sess.ID, "提醒"); !model.IsCode(err, model.CodeUnavailable) {
		t.Fatalf("关闭后推送应返回 unavailable，得到 %v", err)
	}
}

func TestPushedMessageJoinsNextRunHistory(t *testing.T) {
	h := newHarness(t, Options{})
	sess := h.session()
	first := h.send(sess.ID, "你好")
	h.rt.emit(t, first.ID, model.RunEvent{
		Type:    model.EventRunCompleted,
		Message: &model.Message{Parts: []model.Part{{Type: model.PartText, Text: "你好！"}}},
	})
	if _, err := h.svc.Push(context.Background(), sess.ID, "提醒：开会"); err != nil {
		t.Fatalf("推送失败: %v", err)
	}
	h.send(sess.ID, "刚才提醒了什么？")
	req := h.rt.lastRequest(t)
	var texts []string
	for _, m := range req.Messages {
		texts = append(texts, string(m.Role)+":"+m.Text())
	}
	want := []string{"user:你好", "assistant:你好！", "assistant:提醒：开会", "user:刚才提醒了什么？"}
	if len(texts) != len(want) {
		t.Fatalf("下一次运行的历史应包含推送消息，得到 %v", texts)
	}
	for i := range want {
		if texts[i] != want[i] {
			t.Fatalf("历史第 %d 条 = %q，期望 %q（全部：%v）", i, texts[i], want[i], texts)
		}
	}
}

// asModelError 是 errors.As 的薄包装，避免在测试里重复类型断言。
func asModelError(err error, target **model.Error) bool {
	for err != nil {
		if me, ok := err.(*model.Error); ok {
			*target = me
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
