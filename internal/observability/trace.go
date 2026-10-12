package observability

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"sync"
	"time"
)

// SpanContext 标识一个跨度在追踪中的位置。
type SpanContext struct {
	// TraceID 是 32 位十六进制的追踪标识；同一次运行内的所有跨度共享。
	TraceID string
	// SpanID 是 16 位十六进制的跨度标识。
	SpanID string
}

// Span 是一个进行中的操作。End 必须被调用且只生效一次；SetAttributes 可在 End 之前多次调用。
type Span interface {
	// SetAttributes 追加属性；同名属性后者覆盖前者。
	SetAttributes(attrs ...slog.Attr)
	// End 结束跨度；err 非 nil 表示操作失败。
	End(err error)
	// SpanContext 返回追踪与跨度标识。
	SpanContext() SpanContext
}

// Tracer 创建跨度。实现必须可并发使用。
type Tracer interface {
	// Start 在 ctx 下开始一个名为 name 的跨度，返回携带该跨度的新 ctx。
	// ctx 中已有跨度时新跨度成为其子跨度。
	Start(ctx context.Context, name string, attrs ...slog.Attr) (context.Context, Span)
}

type spanKey struct{}

// ContextWithSpan 把跨度放进 ctx，供 Tracer 实现使用。
func ContextWithSpan(ctx context.Context, s Span) context.Context {
	return context.WithValue(ctx, spanKey{}, s)
}

// SpanFromContext 返回 ctx 中的当前跨度。
func SpanFromContext(ctx context.Context) (Span, bool) {
	s, ok := ctx.Value(spanKey{}).(Span)
	return s, ok
}

// NopTracer 不记录任何东西，供测试与关闭追踪时使用。
type NopTracer struct{}

// Start 实现 Tracer。
func (NopTracer) Start(ctx context.Context, _ string, _ ...slog.Attr) (context.Context, Span) {
	s := nopSpan{}
	return ContextWithSpan(ctx, s), s
}

type nopSpan struct{}

func (nopSpan) SetAttributes(...slog.Attr) {}
func (nopSpan) End(error)                  {}
func (nopSpan) SpanContext() SpanContext   { return SpanContext{} }

// LogTracer 是默认的 Tracer：每个跨度结束时写一条日志，字段为
// span_name、trace_id、span_id、parent_span_id、duration_ms、status 以及全部属性。
// 成功的跨度记为 Info（status=ok），失败的记为 Warn（status=error，附带 error 文本；
// 错误类别由调用方用 AttrErrorType 属性给出）。可并发使用。
type LogTracer struct {
	logger *slog.Logger
	now    func() time.Time
	rand   io.Reader
}

// NewLogTracer 创建以 logger 为输出的 Tracer；logger 为 nil 时使用 slog.Default()。
func NewLogTracer(logger *slog.Logger) *LogTracer {
	if logger == nil {
		logger = slog.Default()
	}
	return &LogTracer{logger: logger, now: time.Now, rand: rand.Reader}
}

// Start 实现 Tracer。
func (t *LogTracer) Start(ctx context.Context, name string, attrs ...slog.Attr) (context.Context, Span) {
	s := &logSpan{tracer: t, name: name, start: t.now()}
	if parent, ok := SpanFromContext(ctx); ok {
		pc := parent.SpanContext()
		if pc.TraceID != "" {
			s.sc.TraceID = pc.TraceID
			s.parentID = pc.SpanID
		}
	}
	if s.sc.TraceID == "" {
		s.sc.TraceID = t.randomHex(16)
	}
	s.sc.SpanID = t.randomHex(8)
	s.SetAttributes(attrs...)
	return ContextWithSpan(ctx, s), s
}

// randomHex 返回 n 字节随机数的十六进制；随机源失败时退化为基于时间的值，不中断业务。
func (t *LogTracer) randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := io.ReadFull(t.rand, buf); err != nil {
		ns := uint64(t.now().UnixNano())
		for i := range buf {
			buf[i] = byte(ns >> (8 * (i % 8)))
		}
	}
	return hex.EncodeToString(buf)
}

type logSpan struct {
	tracer   *LogTracer
	name     string
	start    time.Time
	sc       SpanContext
	parentID string

	mu    sync.Mutex
	attrs []slog.Attr
	ended bool
}

func (s *logSpan) SetAttributes(attrs ...slog.Attr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range attrs {
		if a.Key == "" {
			continue
		}
		replaced := false
		for i := range s.attrs {
			if s.attrs[i].Key == a.Key {
				s.attrs[i] = a
				replaced = true
				break
			}
		}
		if !replaced {
			s.attrs = append(s.attrs, a)
		}
	}
}

func (s *logSpan) SpanContext() SpanContext { return s.sc }

func (s *logSpan) End(err error) {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.ended = true
	attrs := make([]slog.Attr, 0, len(s.attrs)+8)
	attrs = append(attrs,
		slog.String("span_name", s.name),
		slog.String("trace_id", s.sc.TraceID),
		slog.String("span_id", s.sc.SpanID),
	)
	if s.parentID != "" {
		attrs = append(attrs, slog.String("parent_span_id", s.parentID))
	}
	elapsed := s.tracer.now().Sub(s.start)
	attrs = append(attrs, slog.Float64("duration_ms", float64(elapsed.Microseconds())/1000))
	level := slog.LevelInfo
	if err != nil {
		level = slog.LevelWarn
		attrs = append(attrs, slog.String("status", "error"), slog.String("error", err.Error()))
	} else {
		attrs = append(attrs, slog.String("status", "ok"))
	}
	attrs = append(attrs, s.attrs...)
	s.mu.Unlock()

	s.tracer.logger.LogAttrs(context.Background(), level, "span", attrs...)
}
