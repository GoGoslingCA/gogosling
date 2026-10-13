package event

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func BenchmarkReplayRecent(b *testing.B) {
	for _, capacity := range []int{4096, 65536} {
		b.Run(fmt.Sprintf("capacity_%d", capacity), func(b *testing.B) {
			bus := benchmarkBus(capacity)
			since := bus.LastSeq() - 8
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if events := bus.replayLocked(since, nil); len(events) != 8 {
					b.Fatal("应只回放最后八条事件")
				}
			}
		})
	}
}

func BenchmarkReplayFull(b *testing.B) {
	bus := benchmarkBus(4096)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if events := bus.replayLocked(4096, nil); len(events) != 4096 {
			b.Fatal("应回放所有保留的事件")
		}
	}
}

func BenchmarkSubscribeExpired(b *testing.B) {
	bus := benchmarkBus(4096)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		sub := bus.Subscribe(context.Background(), 1, 16, nil)
		if !sub.Lost() {
			b.Fatal("已覆盖的历史应返回缺口")
		}
	}
}

func BenchmarkSubscribeCancel(b *testing.B) {
	bus := NewBus(8)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		ctx, cancel := context.WithCancel(context.Background())
		sub := bus.Subscribe(ctx, 0, 16, nil)
		sub.Cancel()
		cancel()
	}
}

func benchmarkBus(capacity int) *Bus {
	bus := NewBus(capacity)
	// 写满后绕回一次，覆盖真实的环形索引路径；固定时间排除时钟读取成本。
	for i := 0; i < capacity*2; i++ {
		bus.Publish(Event{Type: "text.delta", At: time.Unix(1, 0)})
	}
	return bus
}
