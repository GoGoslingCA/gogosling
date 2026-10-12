package event

import (
	"context"
	"testing"
	"time"
)

func collect(t *testing.T, s *Subscription, n int) []Event {
	t.Helper()
	out := make([]Event, 0, n)
	timeout := time.After(2 * time.Second)
	for len(out) < n {
		select {
		case ev, ok := <-s.C:
			if !ok {
				t.Fatalf("订阅提前关闭，已收到 %d/%d", len(out), n)
			}
			out = append(out, ev)
		case <-timeout:
			t.Fatalf("等待事件超时，已收到 %d/%d", len(out), n)
		}
	}
	return out
}

func TestPublishAssignsIncreasingSeq(t *testing.T) {
	b := NewBus(8)
	a := b.Publish(Event{Type: "a"})
	c := b.Publish(Event{Type: "b"})
	if a.Seq != 1 || c.Seq != 2 {
		t.Fatalf("Seq = %d, %d，期望 1, 2", a.Seq, c.Seq)
	}
	if a.At.IsZero() {
		t.Error("At 未被填充")
	}
}

func TestSubscribeReplaysThenStreams(t *testing.T) {
	b := NewBus(8)
	b.Publish(Event{Type: "old1"})
	b.Publish(Event{Type: "old2"})

	s := b.Subscribe(context.Background(), 1, 4, nil)
	defer s.Cancel()
	b.Publish(Event{Type: "live"})

	got := collect(t, s, 2)
	if got[0].Type != "old2" || got[1].Type != "live" {
		t.Fatalf("顺序错误: %s, %s", got[0].Type, got[1].Type)
	}
}

func TestSubscribeFilter(t *testing.T) {
	b := NewBus(8)
	s := b.Subscribe(context.Background(), 0, 4, func(e Event) bool { return e.SessionID == "s1" })
	defer s.Cancel()

	b.Publish(Event{Type: "x", SessionID: "s2"})
	b.Publish(Event{Type: "y", SessionID: "s1"})

	got := collect(t, s, 1)
	if got[0].Type != "y" {
		t.Fatalf("收到了未通过过滤的事件: %s", got[0].Type)
	}
}

func TestSlowSubscriberIsDropped(t *testing.T) {
	b := NewBus(8)
	s := b.Subscribe(context.Background(), 0, 1, nil)
	b.Publish(Event{Type: "1"})
	b.Publish(Event{Type: "2"}) // 缓冲已满，订阅被关闭

	got := collect(t, s, 1)
	if got[0].Type != "1" {
		t.Fatalf("第一条事件错误: %s", got[0].Type)
	}
	if _, ok := <-s.C; ok {
		t.Fatal("订阅应已关闭")
	}
	if !s.Lost() {
		t.Fatal("应标记为 Lost")
	}
}

func TestSubscribeBeyondRingMarksLost(t *testing.T) {
	b := NewBus(2)
	for i := 0; i < 5; i++ {
		b.Publish(Event{Type: "e"})
	}
	s := b.Subscribe(context.Background(), 1, 4, nil) // Seq 2、3 已被覆盖
	if _, ok := <-s.C; ok {
		t.Fatal("存在缺口时订阅应立即关闭")
	}
	if !s.Lost() {
		t.Fatal("存在缺口时应标记为 Lost")
	}
}

func TestContextCancelClosesSubscription(t *testing.T) {
	b := NewBus(8)
	ctx, cancel := context.WithCancel(context.Background())
	s := b.Subscribe(ctx, 0, 4, nil)
	cancel()

	select {
	case _, ok := <-s.C:
		if ok {
			t.Fatal("取消后不应再收到事件")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("取消 ctx 后订阅未关闭")
	}
	if s.Lost() {
		t.Fatal("主动取消不应标记为 Lost")
	}
}
