package store

import (
	"context"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

func recentIDs(sessions []*model.Session) []string {
	ids := make([]string, len(sessions))
	for i, sess := range sessions {
		ids[i] = sess.ID
	}
	return ids
}

func assertRecentIDs(t *testing.T, sessions []*model.Session, want ...string) {
	t.Helper()
	got := recentIDs(sessions)
	if len(got) != len(want) {
		t.Fatalf("会话应按活动时间排序，期望 %v，得到 %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("会话应按活动时间稳定排序，期望 %v，得到 %v", want, got)
		}
	}
}

func TestListRecentSessions按活动时间稳定分页(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a1 := mustCreateAgent(t, s)
	a2 := mustCreateAgent(t, s)
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	create := func(id string, agentID string, created int, last *time.Time, status model.SessionStatus, origin model.Origin) {
		t.Helper()
		sess := newSession(agentID, id)
		sess.ID = id
		sess.CreatedAt = base.Add(time.Duration(created) * time.Hour)
		sess.UpdatedAt = sess.CreatedAt
		sess.LastMessageAt = last
		sess.Status = status
		sess.Origin = origin
		if err := s.CreateSession(ctx, sess); err != nil {
			t.Fatal(err)
		}
	}
	last := base.Add(10 * time.Hour)
	create("old-active", a1.ID, 1, nil, model.SessionActive, model.OriginDesktop)
	create("new-empty", a1.ID, 5, nil, model.SessionActive, model.OriginDesktop)
	create("older-replied", a1.ID, 0, &last, model.SessionActive, model.OriginDesktop)
	create("tie-a", a1.ID, 2, &last, model.SessionActive, model.OriginChannel)
	create("tie-b", a1.ID, 2, &last, model.SessionArchived, model.OriginChannel)
	create("other-agent", a2.ID, 20, nil, model.SessionActive, model.OriginDesktop)
	filter := model.SessionFilter{AgentID: a1.ID}
	all, err := s.ListRecentSessions(ctx, filter, model.Page{})
	if err != nil {
		t.Fatal(err)
	}
	assertRecentIDs(t, all, "tie-b", "tie-a", "older-replied", "new-empty", "old-active")
	var paged []*model.Session
	for offset := 0; offset < len(all); offset += 2 {
		page, err := s.ListRecentSessions(ctx, filter, model.Page{Limit: 2, Offset: offset})
		if err != nil {
			t.Fatal(err)
		}
		paged = append(paged, page...)
	}
	assertRecentIDs(t, paged, "tie-b", "tie-a", "older-replied", "new-empty", "old-active")
	page, err := s.ListRecentSessions(ctx, filter, model.Page{Limit: 2, Offset: len(all)})
	if err != nil || len(page) != 0 {
		t.Fatalf("超过末页应返回空列表，得到 %v，错误 %v", recentIDs(page), err)
	}
	active, err := s.ListRecentSessions(ctx, model.SessionFilter{AgentID: a1.ID, Status: model.SessionActive}, model.Page{})
	if err != nil {
		t.Fatal(err)
	}
	assertRecentIDs(t, active, "tie-a", "older-replied", "new-empty", "old-active")
	archived, err := s.ListRecentSessions(ctx, model.SessionFilter{AgentID: a1.ID, Status: model.SessionArchived, Origin: model.OriginChannel}, model.Page{})
	if err != nil {
		t.Fatal(err)
	}
	assertRecentIDs(t, archived, "tie-b")
	oldOrder, err := s.ListSessions(ctx, filter, model.Page{})
	if err != nil {
		t.Fatal(err)
	}
	assertRecentIDs(t, oldOrder, "older-replied", "old-active", "tie-a", "tie-b", "new-empty")
}

func TestListRecentSessions归档不改变顺序消息改变活动时间(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a := mustCreateAgent(t, s)
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	older := newSession(a.ID, "older")
	older.CreatedAt = base
	newer := newSession(a.ID, "newer")
	newer.CreatedAt = base.Add(time.Hour)
	for _, sess := range []*model.Session{older, newer} {
		if err := s.CreateSession(ctx, sess); err != nil {
			t.Fatal(err)
		}
	}
	for _, status := range []model.SessionStatus{model.SessionArchived, model.SessionActive} {
		older.Status = status
		older.UpdatedAt = base.Add(48 * time.Hour)
		if err := s.UpdateSession(ctx, older); err != nil {
			t.Fatal(err)
		}
		all, err := s.ListRecentSessions(ctx, model.SessionFilter{AgentID: a.ID}, model.Page{})
		if err != nil {
			t.Fatal(err)
		}
		assertRecentIDs(t, all, newer.ID, older.ID)
	}
	msg := model.TextMessage(older.ID, model.RoleUser, "继续旧会话")
	msg.CreatedAt = base.Add(2 * time.Hour)
	if err := s.AppendMessage(ctx, &msg); err != nil {
		t.Fatal(err)
	}
	all, err := s.ListRecentSessions(ctx, model.SessionFilter{AgentID: a.ID}, model.Page{})
	if err != nil {
		t.Fatal(err)
	}
	assertRecentIDs(t, all, older.ID, newer.ID)
	if all[0].LastMessageAt == nil || !all[0].LastMessageAt.Equal(msg.CreatedAt) {
		t.Fatalf("列表应保留消息活动时间，得到 %v", all[0].LastMessageAt)
	}
}
