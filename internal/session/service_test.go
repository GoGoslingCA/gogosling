package session

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

func TestCreateSessionBuildsKeyAndPublishes(t *testing.T) {
	h := newHarness(t, Options{})
	sub := h.subscribe()

	sess, err := h.svc.Create(context.Background(), h.agent.ID, model.OriginDesktop, "  第一次对话 ")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if sess.Key != MakeKey(h.agent.ID, model.OriginDesktop, DefaultPeer) {
		t.Fatalf("桌面会话的路由键应使用默认 peer，得到 %q", sess.Key)
	}
	if sess.Status != model.SessionActive || sess.Title != "第一次对话" || !model.IsID(sess.ID) {
		t.Fatalf("会话字段错误: %+v", sess)
	}
	evs := drain(sub)
	if len(evs) != 1 || evs[0].Type != EventSessionCreated || evs[0].SessionID != sess.ID || evs[0].AgentID != h.agent.ID {
		t.Fatalf("应发布一条 session.created，得到 %v", eventTypes(evs))
	}
	if v, ok := evs[0].Payload.(View); !ok || v.ID != sess.ID || v.ActiveRunID != "" {
		t.Fatalf("payload 应是 View 值且新会话没有在途运行，得到 %T %+v", evs[0].Payload, evs[0].Payload)
	}
}

func TestCreateRejectsBadInput(t *testing.T) {
	h := newHarness(t, Options{})
	ctx := context.Background()
	if _, err := h.svc.Create(ctx, "no-such-agent", model.OriginDesktop, ""); !model.IsCode(err, model.CodeNotFound) {
		t.Fatalf("不存在的智能体应返回 not_found，得到 %v", err)
	}
	if _, err := h.svc.Create(ctx, h.agent.ID, model.Origin("web"), ""); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("未知来源应返回 invalid，得到 %v", err)
	}
	if _, err := h.svc.Create(ctx, "a:b", model.OriginDesktop, ""); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("含冒号的智能体 ID 应返回 invalid，得到 %v", err)
	}
}

func TestCreateTakesOverKeyAndRekeysPrevious(t *testing.T) {
	h := newHarness(t, Options{})
	ctx := context.Background()
	first := h.session()
	sub := h.subscribe()

	second := h.session()
	if second.Key != first.Key {
		t.Fatalf("新会话应接管默认路由键 %q，得到 %q", first.Key, second.Key)
	}
	got, err := h.store.GetSession(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.SessionActive {
		t.Fatalf("被接管的旧会话应保持 active，得到 %s", got.Status)
	}
	if got.Key != MakeKey(h.agent.ID, model.OriginDesktop, first.ID) {
		t.Fatalf("旧会话的键应改写为以自身 ID 为 peer，得到 %q", got.Key)
	}
	byKey, err := h.store.GetActiveSessionByKey(ctx, first.Key)
	if err != nil || byKey.ID != second.ID {
		t.Fatalf("默认键应指向新会话，得到 %v, %v", byKey, err)
	}
	if types := eventTypes(drain(sub)); len(types) != 2 || types[0] != EventSessionUpdated || types[1] != EventSessionCreated {
		t.Fatalf("应先发布旧会话的 updated 再发布 created，得到 %v", types)
	}
}

func TestEnsureByKeyReusesOrCreates(t *testing.T) {
	h := newHarness(t, Options{})
	ctx := context.Background()
	key := MakeKey(h.agent.ID, model.OriginChannel, "tg:42")

	sub := h.subscribe()
	a, err := h.svc.EnsureByKey(ctx, h.agent.ID, key, model.OriginChannel)
	if err != nil {
		t.Fatalf("首次 EnsureByKey 失败: %v", err)
	}
	if a.Key != key || a.Origin != model.OriginChannel {
		t.Fatalf("会话应按键创建: %+v", a)
	}
	b, err := h.svc.EnsureByKey(ctx, h.agent.ID, key, "")
	if err != nil {
		t.Fatalf("再次 EnsureByKey 失败: %v", err)
	}
	if b.ID != a.ID {
		t.Fatalf("应复用 active 会话 %s，得到 %s", a.ID, b.ID)
	}
	if types := eventTypes(drain(sub)); len(types) != 1 {
		t.Fatalf("复用不应再发事件，得到 %v", types)
	}

	archived := model.SessionArchived
	if _, err := h.svc.Update(ctx, a.ID, SessionUpdate{Status: &archived}); err != nil {
		t.Fatalf("归档失败: %v", err)
	}
	c, err := h.svc.EnsureByKey(ctx, h.agent.ID, key, model.OriginChannel)
	if err != nil {
		t.Fatalf("归档后 EnsureByKey 失败: %v", err)
	}
	if c.ID == a.ID {
		t.Fatal("归档的会话不应被复用，应新建")
	}
}

func TestEnsureByKeyValidatesOwnership(t *testing.T) {
	h := newHarness(t, Options{})
	ctx := context.Background()
	other := &model.Agent{ID: model.NewID(), Name: "另一个", Enabled: true}
	h.agents.set(other)
	key := MakeKey(other.ID, model.OriginChannel, "x")

	if _, err := h.svc.EnsureByKey(ctx, h.agent.ID, key, model.OriginChannel); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("键属于其他智能体时应返回 invalid，得到 %v", err)
	}
	if _, err := h.svc.EnsureByKey(ctx, other.ID, key, model.OriginJob); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("来源不一致时应返回 invalid，得到 %v", err)
	}
	if _, err := h.svc.EnsureByKey(ctx, other.ID, "garbage", ""); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("非法键应返回 invalid，得到 %v", err)
	}
}

func TestUpdateTitleArchiveAndUnarchive(t *testing.T) {
	h := newHarness(t, Options{})
	ctx := context.Background()
	sess := h.session()
	sub := h.subscribe()

	title := "新标题"
	got, err := h.svc.Update(ctx, sess.ID, SessionUpdate{Title: &title})
	if err != nil || got.Title != "新标题" {
		t.Fatalf("改标题失败: %v, %+v", err, got)
	}
	if types := eventTypes(drain(sub)); len(types) != 1 || types[0] != EventSessionUpdated {
		t.Fatalf("应发布 session.updated，得到 %v", types)
	}
	// 没有变化时不写库不发事件。
	if _, err := h.svc.Update(ctx, sess.ID, SessionUpdate{Title: &title}); err != nil {
		t.Fatal(err)
	}
	if evs := drain(sub); len(evs) != 0 {
		t.Fatalf("无变化不应发事件，得到 %v", eventTypes(evs))
	}

	archived := model.SessionArchived
	if _, err := h.svc.Update(ctx, sess.ID, SessionUpdate{Status: &archived}); err != nil {
		t.Fatalf("归档失败: %v", err)
	}
	// 键空出来后另一个会话接管；此时取消归档必须冲突。
	other := h.session()
	active := model.SessionActive
	if _, err := h.svc.Update(ctx, sess.ID, SessionUpdate{Status: &active}); !model.IsCode(err, model.CodeConflict) {
		t.Fatalf("键被 %s 持有时取消归档应冲突，得到 %v", other.ID, err)
	}
	if _, err := h.svc.Update(ctx, other.ID, SessionUpdate{Status: &archived}); err != nil {
		t.Fatal(err)
	}
	if got, err := h.svc.Update(ctx, sess.ID, SessionUpdate{Status: &active}); err != nil || got.Status != model.SessionActive {
		t.Fatalf("键空出后应能取消归档: %v, %+v", err, got)
	}
	bad := model.SessionStatus("gone")
	if _, err := h.svc.Update(ctx, sess.ID, SessionUpdate{Status: &bad}); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("未知状态应返回 invalid，得到 %v", err)
	}
	if _, err := h.svc.Update(ctx, "missing", SessionUpdate{Title: &title}); !model.IsCode(err, model.CodeNotFound) {
		t.Fatalf("不存在的会话应返回 not_found，得到 %v", err)
	}
}

func TestArchiveAndDeleteRefuseWhileRunActive(t *testing.T) {
	h := newHarness(t, Options{})
	ctx := context.Background()
	sess := h.session()
	run := h.send(sess.ID, "你好")

	archived := model.SessionArchived
	if _, err := h.svc.Update(ctx, sess.ID, SessionUpdate{Status: &archived}); !model.IsCode(err, model.CodeConflict) {
		t.Fatalf("有在途运行时归档应冲突，得到 %v", err)
	}
	if err := h.svc.Delete(ctx, sess.ID); !model.IsCode(err, model.CodeConflict) {
		t.Fatalf("有在途运行时删除应冲突，得到 %v", err)
	}
	h.rt.emit(t, run.ID, model.RunEvent{Type: model.EventRunCompleted})

	sub := h.subscribe()
	if err := h.svc.Delete(ctx, sess.ID); err != nil {
		t.Fatalf("运行结束后删除失败: %v", err)
	}
	if _, err := h.svc.Get(ctx, sess.ID); !model.IsCode(err, model.CodeNotFound) {
		t.Fatalf("删除后应 not_found，得到 %v", err)
	}
	if _, err := h.store.GetRun(ctx, run.ID); !model.IsCode(err, model.CodeNotFound) {
		t.Fatal("删除会话应级联删除运行记录")
	}
	if types := eventTypes(drain(sub)); len(types) != 1 || types[0] != EventSessionDeleted {
		t.Fatalf("应发布 session.deleted，得到 %v", types)
	}
	if err := h.svc.Delete(ctx, sess.ID); !model.IsCode(err, model.CodeNotFound) {
		t.Fatalf("重复删除应 not_found，得到 %v", err)
	}
}

func TestListSessionsFilters(t *testing.T) {
	h := newHarness(t, Options{})
	ctx := context.Background()
	a := h.session()
	b, err := h.svc.EnsureByKey(ctx, h.agent.ID, MakeKey(h.agent.ID, model.OriginChannel, "c1"), model.OriginChannel)
	if err != nil {
		t.Fatal(err)
	}
	all, err := h.svc.List(ctx, model.SessionFilter{AgentID: h.agent.ID}, model.Page{})
	if err != nil || len(all) != 2 {
		t.Fatalf("应列出 2 个会话，得到 %d, %v", len(all), err)
	}
	ch, err := h.svc.List(ctx, model.SessionFilter{Origin: model.OriginChannel}, model.Page{})
	if err != nil || len(ch) != 1 || ch[0].ID != b.ID {
		t.Fatalf("按来源过滤错误: %v, %v（桌面会话 %s）", ch, err, a.ID)
	}
}

func TestMessagesPagination(t *testing.T) {
	h := newHarness(t, Options{})
	ctx := context.Background()
	sess := h.session()
	for i := 0; i < 5; i++ {
		m := model.TextMessage(sess.ID, model.RoleUser, "m")
		m.ID = model.NewID()
		if err := h.store.AppendMessage(ctx, &m); err != nil {
			t.Fatal(err)
		}
	}
	page, err := h.svc.Messages(ctx, sess.ID, 2, 2)
	if err != nil || len(page) != 2 || page[0].Seq != 3 || page[1].Seq != 4 {
		t.Fatalf("after_seq=2 limit=2 应返回 Seq 3、4，得到 %v, %v", page, err)
	}
	rest, err := h.svc.Messages(ctx, sess.ID, 4, 0)
	if err != nil || len(rest) != 1 || rest[0].Seq != 5 {
		t.Fatalf("limit=0 应使用默认条数，得到 %v, %v", rest, err)
	}
	if _, err := h.svc.Messages(ctx, "missing", 0, 10); !model.IsCode(err, model.CodeNotFound) {
		t.Fatalf("不存在的会话应 not_found，得到 %v", err)
	}
}

func TestCreateCLISessionUsesLocalPeerAndKeepsDesktopKey(t *testing.T) {
	h := newHarness(t, Options{})
	ctx := context.Background()
	desktop := h.session()

	cli, err := h.svc.Create(ctx, h.agent.ID, model.OriginCLI, "")
	if err != nil {
		t.Fatalf("创建 CLI 会话失败: %v", err)
	}
	if want := h.agent.ID + ":cli:local"; cli.Key != want || cli.Origin != model.OriginCLI {
		t.Fatalf("CLI 会话的路由键应为 %q，得到 %q（origin %s）", want, cli.Key, cli.Origin)
	}
	got, err := h.store.GetSession(ctx, desktop.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Key != h.agent.ID+":desktop:local" {
		t.Fatalf("新建 CLI 会话不应改写桌面会话的键，得到 %q", got.Key)
	}

	// 再建一个 CLI 会话：接管 CLI 默认键，旧 CLI 会话改键为以自身 ID 为 peer。
	cli2, err := h.svc.Create(ctx, h.agent.ID, model.OriginCLI, "")
	if err != nil {
		t.Fatal(err)
	}
	old, err := h.store.GetSession(ctx, cli.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cli2.Key != h.agent.ID+":cli:local" || old.Key != MakeKey(h.agent.ID, model.OriginCLI, cli.ID) {
		t.Fatalf("CLI 会话的接管规则应与桌面一致，新 %q，旧 %q", cli2.Key, old.Key)
	}
}

func TestMessagesLimitMatchesContractMaximum(t *testing.T) {
	if MaxMessageLimit != 500 {
		t.Fatalf("消息分页上限应与契约的 maximum 500 一致，得到 %d", MaxMessageLimit)
	}
	h := newHarness(t, Options{})
	ctx := context.Background()
	sess := h.session()
	for i := 0; i < 520; i++ {
		m := model.TextMessage(sess.ID, model.RoleUser, "m")
		m.ID = model.NewID()
		if err := h.store.AppendMessage(ctx, &m); err != nil {
			t.Fatal(err)
		}
	}
	page, err := h.svc.Messages(ctx, sess.ID, 0, 500)
	if err != nil || len(page) != 500 {
		t.Fatalf("limit=500 应返回 500 条，得到 %d, %v", len(page), err)
	}
	page, err = h.svc.Messages(ctx, sess.ID, 0, 900)
	if err != nil || len(page) != 500 {
		t.Fatalf("超过上限的 limit 应截到 500，得到 %d, %v", len(page), err)
	}
}

func TestSessionEventPayloadCarriesActiveRunID(t *testing.T) {
	h := newHarness(t, Options{})
	sess := h.session()
	sub := h.subscribe()

	run := h.send(sess.ID, "第一条消息生成标题")
	var titled *View
	for _, ev := range drain(sub) {
		if ev.Type != EventSessionUpdated {
			continue
		}
		v, ok := ev.Payload.(View)
		if !ok {
			t.Fatalf("session.updated 的 payload 应是 View，得到 %T", ev.Payload)
		}
		titled = &v
	}
	if titled == nil || titled.Title == "" {
		t.Fatal("首条消息应发布自动标题的 session.updated")
	}
	if titled.ActiveRunID != run.ID {
		t.Fatalf("运行登记后发布的 session.updated 应带 active_run_id=%s，得到 %q", run.ID, titled.ActiveRunID)
	}
	raw, err := json.Marshal(*titled)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["active_run_id"] != run.ID || wire["id"] != sess.ID || wire["title"] != titled.Title {
		t.Fatalf("JSON 应是扁平的 Session 字段加 active_run_id，得到 %s", raw)
	}

	h.rt.emit(t, run.ID, model.RunEvent{Type: model.EventRunCompleted})
	ev := waitEvent(t, sub, EventSessionUpdated, time.Second)
	if v := ev.Payload.(View); v.ActiveRunID != "" {
		t.Fatalf("运行结束后的 session.updated 不应带 active_run_id，得到 %q", v.ActiveRunID)
	}
	if v := h.svc.ViewOf(nil); v.ID != "" || v.ActiveRunID != "" {
		t.Fatalf("ViewOf(nil) 应返回零值，得到 %+v", v)
	}
}

func TestDeleteByAgentRemovesAllSessionsAndPublishes(t *testing.T) {
	h := newHarness(t, Options{})
	ctx := context.Background()
	other := &model.Agent{ID: model.NewID(), Name: "别人", Model: h.agent.Model, Enabled: true}
	h.agents.set(other)

	a1 := h.session()
	a2, err := h.svc.EnsureByKey(ctx, h.agent.ID, MakeKey(h.agent.ID, model.OriginChannel, "telegram:1"), model.OriginChannel)
	if err != nil {
		t.Fatal(err)
	}
	archived := model.SessionArchived
	if _, err := h.svc.Update(ctx, a2.ID, SessionUpdate{Status: &archived}); err != nil {
		t.Fatal(err)
	}
	keep, err := h.svc.Create(ctx, other.ID, model.OriginDesktop, "")
	if err != nil {
		t.Fatal(err)
	}

	// 在途运行阻止删除，且不删除任何会话。
	run := h.send(a1.ID, "还在跑")
	err = h.svc.DeleteByAgent(ctx, h.agent.ID)
	var me *model.Error
	if !errors.As(err, &me) || me.Code != model.CodeConflict || me.Details["run_id"] != run.ID {
		t.Fatalf("有在途运行时应返回带 run_id 的 conflict，得到 %v", err)
	}
	if _, err := h.store.GetSession(ctx, a1.ID); err != nil {
		t.Fatalf("冲突时不应删除任何会话: %v", err)
	}
	h.rt.emit(t, run.ID, model.RunEvent{Type: model.EventRunCompleted})

	sub := h.subscribe()
	if err := h.svc.DeleteByAgent(ctx, h.agent.ID); err != nil {
		t.Fatalf("删除智能体的会话失败: %v", err)
	}
	deleted := map[string]bool{}
	for _, ev := range drain(sub) {
		if ev.Type != EventSessionDeleted {
			t.Fatalf("只应发布 session.deleted，得到 %s", ev.Type)
		}
		v, ok := ev.Payload.(View)
		if !ok || ev.AgentID != h.agent.ID {
			t.Fatalf("session.deleted 的 payload 应是 View 且路由到该智能体，得到 %T / %s", ev.Payload, ev.AgentID)
		}
		deleted[v.ID] = true
	}
	if len(deleted) != 2 || !deleted[a1.ID] || !deleted[a2.ID] {
		t.Fatalf("应为两个会话（含归档的）各发布一次 session.deleted，得到 %v", deleted)
	}
	for _, id := range []string{a1.ID, a2.ID} {
		if _, err := h.store.GetSession(ctx, id); !model.IsCode(err, model.CodeNotFound) {
			t.Fatalf("会话 %s 应已删除，得到 %v", id, err)
		}
	}
	if _, err := h.store.GetRun(ctx, run.ID); !model.IsCode(err, model.CodeNotFound) {
		t.Fatal("会话的运行记录应一并删除")
	}
	if _, err := h.store.GetSession(ctx, keep.ID); err != nil {
		t.Fatalf("其他智能体的会话不应受影响: %v", err)
	}

	// 没有会话时是空操作；空 ID 是 invalid。
	if err := h.svc.DeleteByAgent(ctx, h.agent.ID); err != nil {
		t.Fatalf("没有会话时应返回 nil，得到 %v", err)
	}
	if err := h.svc.DeleteByAgent(ctx, ""); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("空智能体 ID 应返回 invalid，得到 %v", err)
	}
}

func TestDeleteByAgentBlocksNewRunsWhileDeleting(t *testing.T) {
	h := newHarness(t, Options{})
	ctx := context.Background()
	sess := h.session()

	// 删除进行期间（列出会话时）尝试为该智能体发起运行：应被拒绝，而不是在即将删除的会话上留下运行。
	var sendErr error
	lister := &listHookStore{memStore: h.store, before: func() {
		_, sendErr = h.svc.Send(ctx, SendRequest{SessionID: sess.ID, Text: "插队"})
	}}
	h.svc.store = lister
	if err := h.svc.DeleteByAgent(ctx, h.agent.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if !model.IsCode(sendErr, model.CodeConflict) {
		t.Fatalf("删除进行期间发起运行应返回 conflict，得到 %v", sendErr)
	}
	if h.rt.requestCount() != 0 {
		t.Fatal("删除进行期间不应启动任何运行")
	}
	if _, ok := h.svc.ActiveRunID(sess.ID); ok {
		t.Fatal("被拒绝的发送不应留下占位")
	}
}

// listHookStore 在第一次 ListSessions 之前调用 before。
type listHookStore struct {
	*memStore
	once   sync.Once
	before func()
}

func (s *listHookStore) ListSessions(ctx context.Context, f model.SessionFilter, p model.Page) ([]*model.Session, error) {
	s.once.Do(s.before)
	return s.memStore.ListSessions(ctx, f, p)
}
