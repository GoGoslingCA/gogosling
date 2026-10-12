package observability

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRegistryCounterGaugeHistogram(t *testing.T) {
	r := NewRegistry()
	r.now = func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }

	c, err := r.Counter("runs_total", "运行总数")
	if err != nil {
		t.Fatal(err)
	}
	c.Inc()
	c.Add(2)
	c.Add(-5)
	if c.Value() != 3 {
		t.Errorf("计数器应为 3（负数被忽略），得到 %d", c.Value())
	}
	again, _ := r.Counter("runs_total", "")
	if again != c {
		t.Error("同名计数器应返回同一个对象")
	}

	g, err := r.Gauge("workers_active", "活动工作进程")
	if err != nil {
		t.Fatal(err)
	}
	g.Set(2)
	g.Inc()
	g.Add(-0.5)
	if g.Value() != 2.5 {
		t.Errorf("瞬时值应为 2.5，得到 %v", g.Value())
	}

	h, err := r.Histogram("tool_duration_seconds", "工具耗时", []float64{0.1, 1, 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []float64{0.05, 0.1, 0.5, 5, 50} {
		h.Observe(v)
	}
	h.ObserveDuration(200 * time.Millisecond)

	snap := r.Snapshot()
	if snap.Counters["runs_total"].Value != 3 || snap.Counters["runs_total"].Help != "运行总数" {
		t.Errorf("计数器快照不符: %+v", snap.Counters)
	}
	if snap.Gauges["workers_active"].Value != 2.5 {
		t.Errorf("瞬时值快照不符: %+v", snap.Gauges)
	}
	hs := snap.Histograms["tool_duration_seconds"]
	if hs.Count != 6 || hs.Sum != 55.85 {
		t.Errorf("直方图计数/总和不符: %+v", hs)
	}
	wantCum := []uint64{2, 4, 5} // <=0.1: 0.05,0.1；<=1: +0.5,0.2；<=10: +5；50 只计入 Count
	for i, b := range hs.Buckets {
		if b.Count != wantCum[i] {
			t.Errorf("桶 %v 的累计计数 = %d，期望 %d", b.UpperBound, b.Count, wantCum[i])
		}
	}
	if !snap.At.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("快照时间应来自注入的时钟，得到 %v", snap.At)
	}

	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("快照应可 JSON 序列化: %v", err)
	}
	for _, want := range []string{`"runs_total":{"help":"运行总数","value":3}`, `"le":10,"count":5`, `"at":"2026-01-01T00:00:00Z"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("JSON 缺少 %s: %s", want, data)
		}
	}
}

func TestRegistryValidation(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Counter("Bad-Name", ""); err == nil {
		t.Error("非 snake_case 的指标名应被拒绝")
	}
	if _, err := r.Counter("dup", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Gauge("dup", ""); err == nil {
		t.Error("同名不同类型应被拒绝")
	}
	if _, err := r.Histogram("h", "", []float64{1, 1}); err == nil {
		t.Error("桶上界不严格递增应被拒绝")
	}
	if _, err := r.Histogram("h", "", []float64{}); err == nil {
		t.Error("空桶列表应被拒绝")
	}
	h, err := r.Histogram("h", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.bounds) != len(DefaultDurationBuckets) {
		t.Error("nil 桶列表应使用默认时长桶")
	}
}

func TestMetricsConcurrent(t *testing.T) {
	r := NewRegistry()
	c, _ := r.Counter("c", "")
	g, _ := r.Gauge("g", "")
	h, _ := r.Histogram("h", "", []float64{1, 2})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				c.Inc()
				g.Add(1)
				h.Observe(1.5)
			}
		}()
	}
	wg.Wait()
	snap := r.Snapshot()
	if snap.Counters["c"].Value != 8000 || snap.Gauges["g"].Value != 8000 || snap.Histograms["h"].Count != 8000 {
		t.Errorf("并发更新丢失: %+v", snap)
	}
}
