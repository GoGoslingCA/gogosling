package event

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"
)

func Test回放与顺序扫描在所有环绕位置一致(t *testing.T) {
	for capacity := 1; capacity <= 11; capacity++ {
		bus := NewBus(capacity)
		for published := 0; published <= capacity*3; published++ {
			if published > 0 {
				bus.Publish(Event{Type: fmt.Sprint(published)})
			}
			for since := uint64(0); since <= uint64(published+1); since++ {
				for _, filter := range []Filter{nil, func(ev Event) bool { return ev.Seq%2 == 0 }, func(Event) bool { return false }} {
					want := []uint64{}
					start := (bus.head - bus.size + len(bus.ring)) % len(bus.ring)
					for i := 0; i < bus.size; i++ {
						ev := bus.ring[(start+i)%len(bus.ring)]
						if ev.Seq > since && (filter == nil || filter(ev)) {
							want = append(want, ev.Seq)
						}
					}
					got := []uint64{}
					for _, ev := range bus.replayLocked(since, filter) {
						got = append(got, ev.Seq)
					}
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("容量 %d，已发布 %d，起点 %d：回放 %v，期望 %v", capacity, published, since, got, want)
					}
				}
			}
		}
		if got := bus.replayLocked(math.MaxUint64, nil); len(got) != 0 {
			t.Fatal("极大序号不应回放事件或发生整数溢出")
		}
	}
}

func Test回放缓冲不引用环形存储(t *testing.T) {
	bus := NewBus(3)
	for i := 0; i < 5; i++ {
		bus.Publish(Event{Type: "original"})
	}
	sub := bus.Subscribe(context.Background(), 2, 1, nil)
	defer sub.Cancel()
	for i := 0; i < 3; i++ {
		bus.Publish(Event{Type: "replacement"})
	}
	for _, ev := range collect(t, sub, 3) {
		if ev.Type != "original" {
			t.Fatal("后续发布不能覆盖已排队的回放事件")
		}
	}
}

func Test回放缺口不执行过滤或保留大缓冲(t *testing.T) {
	bus := benchmarkBus(4096)
	called := false
	sub := bus.Subscribe(context.Background(), 0, 1, func(Event) bool {
		called = true
		return true
	})
	if !sub.Lost() || called || cap(sub.C) != 1 {
		t.Fatalf("缺口应直接关闭订阅，不扫描或扩容：丢失 %v，过滤 %v，容量 %d", sub.Lost(), called, cap(sub.C))
	}
}

func Test取消背景订阅不遗留看守协程(t *testing.T) {
	bus := NewBus(8)
	before := runtime.NumGoroutine()
	for i := 0; i < 256; i++ {
		sub := bus.Subscribe(context.Background(), 0, 1, nil)
		sub.Cancel()
	}
	// 本包不并行测试；留出少量运行时协程余量，捕获每个订阅遗留一个协程的回归。
	if after := runtime.NumGoroutine(); after > before+8 {
		t.Fatalf("取消订阅后协程仍持续增长：之前 %d，之后 %d", before, after)
	}
}

func Test关闭订阅解除长期上下文关联(t *testing.T) {
	for _, overflow := range []bool{false, true} {
		t.Run(fmt.Sprintf("溢出_%v", overflow), func(t *testing.T) {
			ctx := &trackedContext{Context: context.Background(), done: make(chan struct{})}
			bus := NewBus(8)
			sub := bus.Subscribe(ctx, 0, 1, nil)
			if ctx.active != 1 {
				t.Fatal("订阅应登记取消回调")
			}
			if overflow {
				bus.Publish(Event{})
				bus.Publish(Event{})
			} else {
				sub.Cancel()
			}
			sub.Cancel()
			if ctx.active != 0 {
				t.Fatal("关闭后上下文仍引用订阅回调")
			}
			if sub.Lost() != overflow {
				t.Fatal("关闭原因与 Lost 标记不一致")
			}
		})
	}
}

// trackedContext 通过标准 AfterFunc 协议观察上下文关联，不依赖垃圾回收时机。
type trackedContext struct {
	context.Context
	done   chan struct{}
	active int
}

func (c *trackedContext) Done() <-chan struct{} { return c.done }

func (c *trackedContext) AfterFunc(func()) func() bool {
	c.active++
	stopped := false
	return func() bool {
		if stopped {
			return false
		}
		stopped = true
		c.active--
		return true
	}
}

func Test并发发布与两种取消均正常关闭(t *testing.T) {
	bus := NewBus(8)
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		sub := bus.Subscribe(ctx, bus.LastSeq(), 4, nil)
		wg.Add(3)
		go func() {
			defer wg.Done()
			for j := 0; j < 32; j++ {
				bus.Publish(Event{})
			}
		}()
		go func() { defer wg.Done(); sub.Cancel() }()
		go func() { defer wg.Done(); cancel() }()
	}
	completed := make(chan struct{})
	go func() { wg.Wait(); close(completed) }()
	select {
	case <-completed:
	case <-time.After(5 * time.Second):
		t.Fatal("并发关闭发生死锁")
	}
	bus.mu.Lock()
	defer bus.mu.Unlock()
	if len(bus.subs) != 0 {
		t.Fatal("取消后仍保留活动订阅")
	}
}

func Test已取消上下文不会保留订阅(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	bus := NewBus(2)
	sub := bus.Subscribe(ctx, 0, 1, nil)
	select {
	case _, ok := <-sub.C:
		if ok {
			t.Fatal("取消后不应生成事件")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("已取消上下文的订阅未关闭")
	}
	sub.Cancel()
	if sub.Lost() {
		t.Fatal("上下文取消不能标记为历史缺口")
	}
}

func Test批量溢出不会跳过其他订阅(t *testing.T) {
	bus := NewBus(8)
	slow := make([]*Subscription, 64)
	for i := range slow {
		slow[i] = bus.Subscribe(context.Background(), 0, 1, nil)
	}
	fast := bus.Subscribe(context.Background(), 0, 4, nil)
	defer fast.Cancel()
	bus.Publish(Event{Type: "first"})
	bus.Publish(Event{Type: "second"})
	for _, sub := range slow {
		if !sub.Lost() {
			t.Fatal("每个缓冲已满的订阅都应标记为 Lost")
		}
		if ev := collect(t, sub, 1)[0]; ev.Type != "first" {
			t.Fatal("溢出不能丢弃已经排队的第一条事件")
		}
		if _, ok := <-sub.C; ok {
			t.Fatal("溢出的订阅应关闭")
		}
	}
	if got := collect(t, fast, 2); got[0].Type != "first" || got[1].Type != "second" || fast.Lost() {
		t.Fatal("关闭慢订阅不能影响其他订阅的顺序或内容")
	}
}
