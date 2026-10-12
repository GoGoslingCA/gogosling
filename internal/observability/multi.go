package observability

import (
	"context"
	"errors"
	"log/slog"
)

// multiHandler 把一条记录分发给多个 Handler（stderr 与日志文件）。
type multiHandler struct {
	handlers []slog.Handler
}

// NewMultiHandler 返回把记录依次交给每个 handler 的 Handler；nil 项被忽略。
// 只要有一个 handler 对某级别启用，整体就启用；分发时各自再按级别过滤。
func NewMultiHandler(handlers ...slog.Handler) slog.Handler {
	out := make([]slog.Handler, 0, len(handlers))
	for _, h := range handlers {
		if h != nil {
			out = append(out, h)
		}
	}
	if len(out) == 1 {
		return out[0]
	}
	return &multiHandler{handlers: out}
}

func (m *multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range m.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (m *multiHandler) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, h := range m.handlers {
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		// 每个 handler 拿到独立的副本：Record 内部有固定数组，共享会互相干扰。
		if err := h.Handle(ctx, r.Clone()); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (m *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		out[i] = h.WithAttrs(attrs)
	}
	return &multiHandler{handlers: out}
}

func (m *multiHandler) WithGroup(name string) slog.Handler {
	out := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		out[i] = h.WithGroup(name)
	}
	return &multiHandler{handlers: out}
}
