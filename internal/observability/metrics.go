package observability

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// metricName 限定指标名为 snake_case：字母或下划线开头，只含小写字母、数字与下划线。
var metricName = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// DefaultDurationBuckets 是时长直方图的默认桶上界（秒），覆盖毫秒级工具调用到数分钟的模型调用。
var DefaultDurationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300}

// DefaultTokenBuckets 是 token 用量直方图的默认桶上界。
var DefaultTokenBuckets = []float64{100, 500, 1000, 2000, 5000, 10000, 20000, 50000, 100000, 200000}

// Registry 是进程内指标注册表；所有方法可并发调用。零值不可用，请用 NewRegistry 创建。
type Registry struct {
	mu         sync.RWMutex
	counters   map[string]*Counter
	gauges     map[string]*Gauge
	histograms map[string]*Histogram
	now        func() time.Time
}

// NewRegistry 创建空的注册表。
func NewRegistry() *Registry {
	return &Registry{
		counters:   map[string]*Counter{},
		gauges:     map[string]*Gauge{},
		histograms: map[string]*Histogram{},
		now:        time.Now,
	}
}

// Counter 是只增计数器；可并发使用。
type Counter struct {
	name, help string
	v          atomic.Int64
}

// Inc 加 1。
func (c *Counter) Inc() { c.v.Add(1) }

// Add 增加 n；n 为负数时忽略（计数器只增）。
func (c *Counter) Add(n int64) {
	if n > 0 {
		c.v.Add(n)
	}
}

// Value 返回当前值。
func (c *Counter) Value() int64 { return c.v.Load() }

// Gauge 是可增可减的瞬时值；可并发使用。
type Gauge struct {
	name, help string
	bits       atomic.Uint64
}

// Set 设置当前值。
func (g *Gauge) Set(v float64) { g.bits.Store(math.Float64bits(v)) }

// Add 在当前值上加 delta（可为负）。
func (g *Gauge) Add(delta float64) {
	for {
		old := g.bits.Load()
		next := math.Float64bits(math.Float64frombits(old) + delta)
		if g.bits.CompareAndSwap(old, next) {
			return
		}
	}
}

// Inc 加 1。
func (g *Gauge) Inc() { g.Add(1) }

// Dec 减 1。
func (g *Gauge) Dec() { g.Add(-1) }

// Value 返回当前值。
func (g *Gauge) Value() float64 { return math.Float64frombits(g.bits.Load()) }

// Histogram 是固定桶直方图：记录落入各个上界以下的观测次数、总次数与总和；可并发使用。
type Histogram struct {
	name, help string
	bounds     []float64
	mu         sync.Mutex
	counts     []uint64 // 与 bounds 等长，非累计
	count      uint64
	sum        float64
}

// Observe 记录一次观测值。NaN 被忽略。
func (h *Histogram) Observe(v float64) {
	if math.IsNaN(v) {
		return
	}
	i := sort.SearchFloat64s(h.bounds, v) // 第一个 bound >= v 的下标；v 大于全部上界时等于 len(bounds)
	h.mu.Lock()
	if i < len(h.counts) {
		h.counts[i]++
	}
	h.count++
	h.sum += v
	h.mu.Unlock()
}

// ObserveDuration 以秒为单位记录一段时长。
func (h *Histogram) ObserveDuration(d time.Duration) { h.Observe(d.Seconds()) }

// Counter 返回名为 name 的计数器，不存在时注册。名字不合法或已注册为其他类型时返回错误。
func (r *Registry) Counter(name, help string) (*Counter, error) {
	if err := validateMetricName(name); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.counters[name]; ok {
		return c, nil
	}
	if err := r.conflictLocked(name, "counter"); err != nil {
		return nil, err
	}
	c := &Counter{name: name, help: help}
	r.counters[name] = c
	return c, nil
}

// Gauge 返回名为 name 的瞬时值，不存在时注册。
func (r *Registry) Gauge(name, help string) (*Gauge, error) {
	if err := validateMetricName(name); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if g, ok := r.gauges[name]; ok {
		return g, nil
	}
	if err := r.conflictLocked(name, "gauge"); err != nil {
		return nil, err
	}
	g := &Gauge{name: name, help: help}
	r.gauges[name] = g
	return g, nil
}

// Histogram 返回名为 name 的直方图，不存在时按 buckets 注册。
// buckets 必须严格递增且有限；nil 表示 DefaultDurationBuckets。已存在时忽略 buckets 参数。
func (r *Registry) Histogram(name, help string, buckets []float64) (*Histogram, error) {
	if err := validateMetricName(name); err != nil {
		return nil, err
	}
	if buckets == nil {
		buckets = DefaultDurationBuckets
	}
	if len(buckets) == 0 {
		return nil, fmt.Errorf("observability: 直方图 %q 至少需要一个桶", name)
	}
	for i, b := range buckets {
		if math.IsNaN(b) || math.IsInf(b, 0) {
			return nil, fmt.Errorf("observability: 直方图 %q 的桶上界必须是有限数", name)
		}
		if i > 0 && b <= buckets[i-1] {
			return nil, fmt.Errorf("observability: 直方图 %q 的桶上界必须严格递增", name)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if h, ok := r.histograms[name]; ok {
		return h, nil
	}
	if err := r.conflictLocked(name, "histogram"); err != nil {
		return nil, err
	}
	bounds := append([]float64(nil), buckets...)
	h := &Histogram{name: name, help: help, bounds: bounds, counts: make([]uint64, len(bounds))}
	r.histograms[name] = h
	return h, nil
}

func (r *Registry) conflictLocked(name, want string) error {
	kind := ""
	switch {
	case r.counters[name] != nil:
		kind = "counter"
	case r.gauges[name] != nil:
		kind = "gauge"
	case r.histograms[name] != nil:
		kind = "histogram"
	}
	if kind != "" && kind != want {
		return fmt.Errorf("observability: 指标 %q 已注册为 %s，不能再注册为 %s", name, kind, want)
	}
	return nil
}

func validateMetricName(name string) error {
	if !metricName.MatchString(name) {
		return fmt.Errorf("observability: 指标名 %q 不合法，必须是 snake_case", name)
	}
	return nil
}

// Snapshot 是注册表某一时刻的只读快照，可直接 JSON 序列化。
type Snapshot struct {
	At         time.Time                    `json:"at"`
	Counters   map[string]CounterSnapshot   `json:"counters"`
	Gauges     map[string]GaugeSnapshot     `json:"gauges"`
	Histograms map[string]HistogramSnapshot `json:"histograms"`
}

// CounterSnapshot 是计数器快照。
type CounterSnapshot struct {
	Help  string `json:"help,omitempty"`
	Value int64  `json:"value"`
}

// GaugeSnapshot 是瞬时值快照。
type GaugeSnapshot struct {
	Help  string  `json:"help,omitempty"`
	Value float64 `json:"value"`
}

// HistogramSnapshot 是直方图快照。Buckets 只含有限上界，按上界升序、计数为累计值
// （与 Prometheus 一致）；超过最大上界的观测只计入 Count，避免 JSON 中出现 +Inf。
type HistogramSnapshot struct {
	Help    string   `json:"help,omitempty"`
	Count   uint64   `json:"count"`
	Sum     float64  `json:"sum"`
	Buckets []Bucket `json:"buckets"`
}

// Bucket 是直方图的一个累计桶：观测值 <= UpperBound 的次数。
type Bucket struct {
	UpperBound float64 `json:"le"`
	Count      uint64  `json:"count"`
}

// Snapshot 导出当前全部指标。
func (r *Registry) Snapshot() Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s := Snapshot{
		At:         r.now().UTC(),
		Counters:   make(map[string]CounterSnapshot, len(r.counters)),
		Gauges:     make(map[string]GaugeSnapshot, len(r.gauges)),
		Histograms: make(map[string]HistogramSnapshot, len(r.histograms)),
	}
	for name, c := range r.counters {
		s.Counters[name] = CounterSnapshot{Help: c.help, Value: c.Value()}
	}
	for name, g := range r.gauges {
		s.Gauges[name] = GaugeSnapshot{Help: g.help, Value: g.Value()}
	}
	for name, h := range r.histograms {
		s.Histograms[name] = h.snapshot()
	}
	return s
}

func (h *Histogram) snapshot() HistogramSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := HistogramSnapshot{Help: h.help, Count: h.count, Sum: h.sum, Buckets: make([]Bucket, len(h.bounds))}
	var cum uint64
	for i, b := range h.bounds {
		cum += h.counts[i]
		out.Buckets[i] = Bucket{UpperBound: b, Count: cum}
	}
	return out
}
