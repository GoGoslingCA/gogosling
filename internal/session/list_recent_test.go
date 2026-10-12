package session

import (
	"context"
	"errors"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

type listingStore struct {
	Store
	ctx    context.Context
	filter model.SessionFilter
	page   model.Page
	result []*model.Session
	err    error
	calls  int
}

func (s *listingStore) ListSessions(ctx context.Context, f model.SessionFilter, p model.Page) ([]*model.Session, error) {
	s.ctx, s.filter, s.page = ctx, f, p
	s.calls++
	return s.result, s.err
}

type recentListingStore struct {
	*listingStore
	recentCalls int
}

func (s *recentListingStore) ListRecentSessions(ctx context.Context, f model.SessionFilter, p model.Page) ([]*model.Session, error) {
	s.ctx, s.filter, s.page = ctx, f, p
	s.recentCalls++
	return s.result, s.err
}

func TestList优先最近会话能力且保留过滤分页(t *testing.T) {
	h := newHarness(t, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := model.SessionFilter{AgentID: h.agent.ID, Status: model.SessionArchived, Origin: model.OriginDesktop}
	p := model.Page{Limit: 50, Offset: 100}
	want := &model.Session{ID: "latest", AgentID: h.agent.ID}
	store := &recentListingStore{listingStore: &listingStore{Store: h.store, result: []*model.Session{want}}}
	h.svc.store = store
	got, err := h.svc.List(ctx, f, p)
	if err != nil || len(got) != 1 || got[0] != want {
		t.Fatalf("应原样返回最近会话，得到 %+v，错误 %v", got, err)
	}
	if store.recentCalls != 1 || store.calls != 0 {
		t.Fatalf("有最近会话能力时只应调用它，最近 %d 次，旧接口 %d 次", store.recentCalls, store.calls)
	}
	if store.ctx != ctx || store.filter != f || store.page != p {
		t.Fatalf("上下文、过滤和分页必须原样传入，过滤 %+v，分页 %+v", store.filter, store.page)
	}
}

func TestList最近会话错误不回退旧列表(t *testing.T) {
	h := newHarness(t, Options{})
	want := errors.New("list unavailable")
	store := &recentListingStore{listingStore: &listingStore{Store: h.store, err: want}}
	h.svc.store = store
	_, err := h.svc.List(context.Background(), model.SessionFilter{}, model.Page{})
	if !errors.Is(err, want) || store.recentCalls != 1 || store.calls != 0 {
		t.Fatalf("最近会话读取失败应原样返回错误，不静默切换排序，错误 %v", err)
	}
}

func TestList无最近能力时兼容旧列表(t *testing.T) {
	h := newHarness(t, Options{})
	f := model.SessionFilter{AgentID: h.agent.ID}
	p := model.Page{Limit: 2, Offset: 1}
	want := &model.Session{ID: "original-order"}
	store := &listingStore{Store: h.store, result: []*model.Session{want}}
	h.svc.store = store
	got, err := h.svc.List(context.Background(), f, p)
	if err != nil || len(got) != 1 || got[0] != want || store.calls != 1 {
		t.Fatalf("旧仓储应仍能列出会话，得到 %+v，错误 %v", got, err)
	}
	if store.filter != f || store.page != p {
		t.Fatalf("旧仓储应收到原过滤与分页，过滤 %+v，分页 %+v", store.filter, store.page)
	}
}
