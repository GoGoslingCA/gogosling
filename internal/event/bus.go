// Package event 是进程内事件总线，也是本地 API 事件流的数据来源。
//
// 设计要点：
//   - 每个事件获得全局递增的 Seq；订阅者可以从任意 Seq 之后开始接收，总线用环形缓冲区回放最近的事件。
//   - 慢订阅者不会阻塞发布者：订阅者的缓冲满了就关闭该订阅并标记丢失，
//     客户端凭最后收到的 Seq 重新订阅即可补齐（与 SSE 的 Last-Event-ID 配合）。
//   - 本包是叶子包：只定义传递机制，不理解事件的业务含义。
package event

import (
	"context"
	"sync"
	"time"
)

// Event 是总线上的一条事件。Payload 必须可以被 encoding/json 序列化。
type Event struct {
	Seq  uint64    `json:"seq"`
	Type string    `json:"type"`
	At   time.Time `json:"at"`
	// 下面三个字段用于订阅过滤与界面路由，可为空。
	AgentID   string `json:"agent_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	RunID     string `json:"run_id,omitempty"`
	Payload   any    `json:"payload,omitempty"`
}

// Filter 决定订阅者是否接收某条事件；nil 表示接收全部。
type Filter func(Event) bool

// Subscription 是一个活动订阅。
type Subscription struct {
	// C 按 Seq 升序投递事件；订阅被取消或因缓冲溢出被关闭时 C 会关闭。
	C <-chan Event

	bus    *Bus
	ch     chan Event
	filter Filter
	stop   func() bool // 解除上下文回调；关闭订阅后不再保留订阅和总线。
	closed bool
	lost   bool
}

// Cancel 取消订阅并关闭 C；可重复调用。
func (s *Subscription) Cancel() {
	s.bus.remove(s)
}

// Lost 在 C 关闭后指示订阅是否因为缓冲溢出而被总线关闭（而不是调用方取消）。
func (s *Subscription) Lost() bool {
	s.bus.mu.Lock()
	defer s.bus.mu.Unlock()
	return s.lost
}

// Bus 是并发安全的事件总线。零值不可用，请用 NewBus 创建。
type Bus struct {
	mu   sync.Mutex
	seq  uint64
	ring []Event
	head int // 下一次写入的位置
	size int // 已写入的数量（≤ len(ring)）
	subs map[*Subscription]struct{}
	now  func() time.Time
}

// NewBus 创建总线；capacity 是回放缓冲区可保留的事件数（至少为 1）。
func NewBus(capacity int) *Bus {
	if capacity < 1 {
		capacity = 1
	}
	return &Bus{ring: make([]Event, capacity), subs: make(map[*Subscription]struct{}), now: time.Now}
}

// Publish 为事件分配 Seq 与时间（若 At 为零值），写入回放缓冲区并投递给订阅者；返回填充后的事件。
func (b *Bus) Publish(ev Event) Event {
	b.mu.Lock()
	b.seq++
	ev.Seq = b.seq
	if ev.At.IsZero() {
		ev.At = b.now()
	}
	b.ring[b.head] = ev
	b.head = (b.head + 1) % len(b.ring)
	if b.size < len(b.ring) {
		b.size++
	}

	for s := range b.subs {
		if s.filter != nil && !s.filter(ev) {
			continue
		}
		select {
		case s.ch <- ev:
		default:
			s.lost = true
			b.removeLocked(s)
		}
	}
	b.mu.Unlock()
	return ev
}

// LastSeq 返回最近一次发布的 Seq；尚未发布任何事件时为 0。
func (b *Bus) LastSeq() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.seq
}

// Subscribe 创建订阅：先回放缓冲区中 Seq 大于 since 的事件，再投递后续事件。
//
// buffer 是订阅者的缓冲长度（至少为 1）。若 since 之后的事件已经不在缓冲区里
// （客户端离线太久），返回的订阅会立即标记 Lost 并关闭，调用方应改为全量刷新。
// ctx 结束时订阅自动取消。
func (b *Bus) Subscribe(ctx context.Context, since uint64, buffer int, filter Filter) *Subscription {
	if buffer < 1 {
		buffer = 1
	}
	s := &Subscription{bus: b, filter: filter}

	b.mu.Lock()
	gap := b.size > 0 && since < b.oldestSeqLocked()-1
	// 历史有缺口时不回放，也无需扫描、过滤或复制整个环形缓冲区。
	var replay []Event
	if !gap {
		replay = b.replayLocked(since, filter)
	}
	if len(replay) > buffer {
		buffer = len(replay)
	}
	s.ch = make(chan Event, buffer)
	s.C = s.ch
	if gap {
		s.lost = true
		s.closed = true
		close(s.ch)
		b.mu.Unlock()
		return s
	}
	for _, ev := range replay {
		s.ch <- ev
	}
	b.subs[s] = struct{}{}
	if ctx.Done() != nil {
		// 标准取消上下文无需常驻看守 goroutine；手动取消或溢出也会解除关联。
		// 在锁内登记，避免上下文取消与发布事件先关闭订阅而漏掉清理。
		s.stop = context.AfterFunc(ctx, func() { b.remove(s) })
	}
	b.mu.Unlock()
	return s
}

// replayLocked 返回缓冲区中 Seq 大于 since 且通过过滤的事件，按 Seq 升序。
func (b *Bus) replayLocked(since uint64, filter Filter) []Event {
	if b.size == 0 || since >= b.seq {
		return nil
	}
	// Seq 连续递增，可直接定位待回放的尾段，复杂度取决于新事件数而非总容量。
	count := b.size
	if newer := b.seq - since; newer < uint64(count) {
		count = int(newer)
	}
	start := (b.head - count + len(b.ring)) % len(b.ring)
	if filter == nil {
		out := make([]Event, count)
		first := copy(out, b.ring[start:min(start+count, len(b.ring))])
		copy(out[first:], b.ring[:count-first])
		return out
	}
	var out []Event
	for i := 0; i < count; i++ {
		ev := b.ring[(start+i)%len(b.ring)]
		if !filter(ev) {
			continue
		}
		out = append(out, ev)
	}
	return out
}

func (b *Bus) oldestSeqLocked() uint64 {
	start := (b.head - b.size + len(b.ring)) % len(b.ring)
	return b.ring[start].Seq
}

func (b *Bus) remove(s *Subscription) {
	b.mu.Lock()
	b.removeLocked(s)
	b.mu.Unlock()
}

func (b *Bus) removeLocked(s *Subscription) {
	if s.closed {
		return
	}
	s.closed = true
	if s.stop != nil {
		s.stop()
		s.stop = nil
	}
	delete(b.subs, s)
	close(s.ch)
}
