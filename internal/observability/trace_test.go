package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestLogTracerParentChildAndRedaction(t *testing.T) {
	var buf bytes.Buffer
	tr := NewLogTracer(New(&buf, slog.LevelDebug))
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tr.now = func() time.Time { clock = clock.Add(10 * time.Millisecond); return clock }

	ctx, parent := tr.Start(context.Background(), "invoke_agent",
		slog.String(AttrGenAIOperationName, OpInvokeAgent),
		slog.String(AttrGenAIAgentID, "A1"),
	)
	if got, ok := SpanFromContext(ctx); !ok || got != parent {
		t.Fatal("Start 返回的 ctx 应携带跨度")
	}
	_, child := tr.Start(ctx, "chat",
		slog.String(AttrGenAIOperationName, OpChat),
		slog.String(AttrGenAIRequestModel, "gpt-x"),
		slog.String("api_key", "sk"),
	)
	child.SetAttributes(slog.Int64(AttrGenAIUsageInputTokens, 120), slog.String(AttrGenAIRequestModel, "gpt-y"))
	child.End(errors.New("upstream 500"))
	child.End(nil) // 第二次 End 不生效
	parent.End(nil)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("期望 2 条跨度日志，得到 %d:\n%s", len(lines), buf.String())
	}
	var childLog, parentLog map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &childLog); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &parentLog); err != nil {
		t.Fatal(err)
	}

	pc, cc := parent.SpanContext(), child.SpanContext()
	if len(pc.TraceID) != 32 || len(pc.SpanID) != 16 {
		t.Errorf("标识长度不对: %+v", pc)
	}
	if cc.TraceID != pc.TraceID || cc.SpanID == pc.SpanID {
		t.Errorf("子跨度应共享 trace_id 且拥有不同 span_id: %+v %+v", pc, cc)
	}
	if childLog["parent_span_id"] != pc.SpanID || childLog["trace_id"] != pc.TraceID {
		t.Errorf("子跨度日志应记录父跨度: %v", childLog)
	}
	if _, ok := parentLog["parent_span_id"]; ok {
		t.Error("根跨度不应有 parent_span_id")
	}
	if childLog["status"] != "error" || childLog["error"] != "upstream 500" || childLog["level"] != "WARN" {
		t.Errorf("失败跨度应为 WARN 且 status=error: %v", childLog)
	}
	if parentLog["status"] != "ok" || parentLog["level"] != "INFO" {
		t.Errorf("成功跨度应为 INFO 且 status=ok: %v", parentLog)
	}
	if childLog[AttrGenAIRequestModel] != "gpt-y" {
		t.Errorf("SetAttributes 应覆盖同名属性: %v", childLog[AttrGenAIRequestModel])
	}
	if childLog[AttrGenAIUsageInputTokens] != float64(120) {
		t.Errorf("token 计数不应被脱敏: %v", childLog[AttrGenAIUsageInputTokens])
	}
	if childLog["api_key"] != RedactedValue {
		t.Errorf("跨度属性也应经过脱敏: %v", childLog["api_key"])
	}
	// 时钟每次被读取前进 10ms：父开始、子开始、子结束 → 子跨度耗时 10ms。
	if childLog["duration_ms"] != float64(10) {
		t.Errorf("duration_ms 应由注入时钟计算（10ms），得到 %v", childLog["duration_ms"])
	}
	if childLog["span_name"] != "chat" || childLog["msg"] != "span" {
		t.Errorf("跨度日志字段不符: %v", childLog)
	}
}

func TestNopTracer(t *testing.T) {
	ctx, s := NopTracer{}.Start(context.Background(), "x")
	if _, ok := SpanFromContext(ctx); !ok {
		t.Error("NopTracer 也应把跨度放进 ctx")
	}
	s.SetAttributes(slog.String("a", "b"))
	s.End(nil)
	if s.SpanContext() != (SpanContext{}) {
		t.Error("NopTracer 的跨度没有标识")
	}
}

func TestLogTracerRandomFallback(t *testing.T) {
	tr := NewLogTracer(New(&bytes.Buffer{}, nil))
	tr.rand = failingReader{}
	_, s := tr.Start(context.Background(), "x")
	if len(s.SpanContext().TraceID) != 32 {
		t.Error("随机源失败时也应生成合法长度的标识")
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("no entropy") }
