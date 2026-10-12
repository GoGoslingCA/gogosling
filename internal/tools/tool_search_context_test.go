package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

func TestSearchFilesReturnsBoundedContextAndTruncation(t *testing.T) {
	ws := newFakeWorkspace(t)
	mustWrite(t, ws, "notes.md", "before\ntarget\nafter\nfar away\ntarget\n")
	res, err := NewSearchFiles().Execute(context.Background(), testRunContext(t, ws, nil), rawArgs(t, map[string]any{"query": "target", "max_results": 1, "context_lines": 1}))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"notes.md-1- before", "notes.md:2: target", "notes.md-3- after"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("搜索上下文缺少 %q：%s", want, res.Content)
		}
	}
	if strings.Contains(res.Content, "far away") || strings.Contains(res.Content, "notes.md:5") || !res.Truncated {
		t.Fatalf("上下文和结果都必须有界，达到上限时声明截断：%+v", res)
	}
	for _, contextLines := range []int{-1, maxSearchContextLines + 1} {
		if _, err := NewSearchFiles().Execute(context.Background(), testRunContext(t, ws, nil), rawArgs(t, map[string]any{"query": "target", "context_lines": contextLines})); !model.IsCode(err, model.CodeInvalid) {
			t.Fatalf("非法上下文行数应被拒绝：%v", err)
		}
	}
}

func TestSearchFilesUnicodeGlobAndOversizedContent(t *testing.T) {
	ws := newFakeWorkspace(t)
	mustWrite(t, ws, "文档/计划.md", "需要执行的任务\n")
	mustWrite(t, ws, "large.txt", strings.Repeat("任务", maxSearchFileBytes))
	res, err := NewSearchFiles().Execute(context.Background(), testRunContext(t, ws, nil), rawArgs(t, map[string]any{"glob": "文档/计?.md", "query": "任务"}))
	if err != nil || !strings.Contains(res.Content, "文档/计划.md:1:") {
		t.Fatalf("Unicode glob 必须按字符而不是 UTF-8 字节匹配：%+v / %v", res, err)
	}
	res, err = NewSearchFiles().Execute(context.Background(), testRunContext(t, ws, nil), rawArgs(t, map[string]any{"query": "任务"}))
	if err != nil || strings.Contains(res.Content, "large.txt") || !strings.Contains(res.Content, "文档/计划.md") {
		t.Fatalf("内容搜索应跳过超大文件：%+v / %v", res, err)
	}
}
