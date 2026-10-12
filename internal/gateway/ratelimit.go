package gateway

import (
	"sync"
	"time"
)

// limiter 是按键（对端 ID）分桶的令牌桶限流器；并发安全。nil 的 limiter 表示不限流。
//
// 桶在闲置到"重新装满"所需的时间后会被回收：那时桶的状态与新建的桶完全相同，回收不改变行为，
// 因此 map 的大小只与"最近活跃的对端数"相关，不会随时间无限增长。
type limiter struct {
	mu        sync.Mutex
	rate      float64 // 每秒补充的令牌数
	burst     float64 // 桶容量
	buckets   map[string]*bucket
	now       func() time.Time
	lastPrune time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// limiterPruneEvery 是两次回收之间的最短间隔；桶数超过 limiterPruneAbove 时立即回收。
const (
	limiterPruneEvery = time.Minute
	limiterPruneAbove = 4096
)

// newLimiter 创建限流器：perMinute 为每分钟允许的条数（不为正时返回 nil，表示不限流），burst 为突发容量。
func newLimiter(perMinute float64, burst int, now func() time.Time) *limiter {
	if perMinute <= 0 {
		return nil
	}
	if burst < 1 {
		burst = 1
	}
	if now == nil {
		now = time.Now
	}
	return &limiter{
		rate:      perMinute / 60,
		burst:     float64(burst),
		buckets:   make(map[string]*bucket),
		now:       now,
		lastPrune: now(),
	}
}

// Allow 为 key 取一个令牌；桶空时返回 false。
func (l *limiter) Allow(key string) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b := l.buckets[key]
	if b == nil {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	} else if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens = min(l.burst, b.tokens+elapsed*l.rate)
		b.last = now
	}
	l.pruneLocked(now)
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// pruneLocked 回收已经闲置到足以重新装满的桶。
func (l *limiter) pruneLocked(now time.Time) {
	if len(l.buckets) <= limiterPruneAbove && now.Sub(l.lastPrune) < limiterPruneEvery {
		return
	}
	l.lastPrune = now
	refill := time.Duration(l.burst / l.rate * float64(time.Second))
	for k, b := range l.buckets {
		if now.Sub(b.last) >= refill {
			delete(l.buckets, k)
		}
	}
}

// size 返回当前桶数（测试用）。
func (l *limiter) size() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}
