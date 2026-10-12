package gateway

import (
	"testing"
	"time"
)

func TestLimiterBurstAndRefill(t *testing.T) {
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	l := newLimiter(60, 3, c.now) // 每秒补 1 个，容量 3
	for i := 0; i < 3; i++ {
		if !l.Allow("u") {
			t.Fatalf("突发容量内第 %d 次应放行", i+1)
		}
	}
	if l.Allow("u") {
		t.Fatal("桶空后应拒绝")
	}
	if !l.Allow("v") {
		t.Fatal("不同对端互不影响")
	}
	c.advance(500 * time.Millisecond)
	if l.Allow("u") {
		t.Fatal("半个令牌不够")
	}
	c.advance(500 * time.Millisecond)
	if !l.Allow("u") {
		t.Fatal("补满一个令牌后应放行")
	}
	c.advance(time.Hour)
	for i := 0; i < 3; i++ {
		if !l.Allow("u") {
			t.Fatalf("长时间闲置后应恢复到满容量，第 %d 次被拒", i+1)
		}
	}
	if l.Allow("u") {
		t.Fatal("容量不应超过 burst")
	}
}

func TestLimiterNilAndDefaults(t *testing.T) {
	var l *limiter
	if !l.Allow("x") || l.size() != 0 {
		t.Fatal("nil 限流器应放行一切")
	}
	if newLimiter(0, 5, nil) != nil || newLimiter(-1, 5, nil) != nil {
		t.Fatal("速率不为正时应返回 nil")
	}
	l = newLimiter(60, 0, nil)
	if l.burst != 1 {
		t.Fatalf("突发容量至少为 1，得到 %v", l.burst)
	}
}

func TestLimiterPrunesIdleBuckets(t *testing.T) {
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	l := newLimiter(60, 2, c.now) // 重新装满需要 2 秒
	l.Allow("a")
	l.Allow("b")
	if l.size() != 2 {
		t.Fatalf("应有 2 个桶，得到 %d", l.size())
	}
	c.advance(90 * time.Second)
	l.Allow("c") // 触发回收：a、b 早已装满
	if l.size() != 1 {
		t.Fatalf("闲置桶应被回收，得到 %d", l.size())
	}
	// 回收不改变行为：a 再来仍享有完整突发容量。
	if !l.Allow("a") || !l.Allow("a") || l.Allow("a") {
		t.Fatal("回收后的桶应等同于新桶")
	}
}
