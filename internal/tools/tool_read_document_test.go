package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

type fakeDocumentReader struct {
	calls  int
	ws     Workspace
	window DocumentWindow
}

func (r *fakeDocumentReader) ReadDocument(ctx context.Context, ws Workspace, path string, window DocumentWindow) (DocumentText, error) {
	r.calls++
	r.ws, r.window = ws, window
	return DocumentText{Format: "word", Version: "v1", TotalChars: 2, Segments: []DocumentSegment{{Location: "block:1", StartChar: 1, Text: "内容"}}}, ctx.Err()
}

func Test文档工具保留权限路径和批准的工作区(t *testing.T) {
	reader := &fakeDocumentReader{}
	f := newFixture(t, NewReadDocument(reader))
	e := newExecutor(t, f, 0)
	result, err := e.Execute(context.Background(), "01RUN", call("read_document", `{"path":"notes/plan.docx"}`))
	if err != nil || result.IsError || !json.Valid([]byte(result.Content)) {
		t.Fatalf("应返回结构化阅读结果：%+v / %v", result, err)
	}
	request := f.checker.last(t)
	var details map[string]any
	_ = json.Unmarshal(request.Details, &details)
	if request.Tool.Risk != model.RiskRead || details["path"] != "notes/plan.docx" || reader.ws != f.ws || reader.window.StartChar != 1 || reader.window.MaxChars != MaxDocumentChars {
		t.Fatalf("文档读取必须沿用审批路径及原工作区：%+v / %+v", request, reader)
	}
	f.checker.verdict = Verdict{Allowed: false}
	result, err = e.Execute(context.Background(), "01RUN", call("read_document", `{"path":"private.docx"}`))
	if err != nil || !result.IsError || reader.calls != 1 {
		t.Fatalf("拒绝后不得打开文件：%+v / %v / %d", result, err, reader.calls)
	}
}

func Test文档工具验证翻页版本预算和取消(t *testing.T) {
	reader := &fakeDocumentReader{}
	tool := NewReadDocument(reader)
	ws := newFakeWorkspace(t)
	for _, args := range []string{`{}`, `{"path":"x","start_char":2}`, `{"path":"x","start_char":-1}`, `{"path":"x","max_chars":8001}`, `{"path":"x","max_chars":-1}`} {
		if _, err := tool.Execute(context.Background(), RunContext{Workspace: ws}, json.RawMessage(args)); !model.IsCode(err, model.CodeInvalid) {
			t.Fatalf("非法窗口不得进入解析：%s / %v", args, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tool.Execute(ctx, RunContext{Workspace: ws}, []byte(`{"path":"x"}`)); !errors.Is(err, context.Canceled) || reader.calls != 0 {
		t.Fatalf("取消后不得触发解析：%v / %d", err, reader.calls)
	}
	if _, err := tool.Execute(context.Background(), RunContext{Workspace: ws}, []byte(`{"path":"x","start_char":5,"max_chars":9,"version":"v1"}`)); err != nil || reader.window.Version != "v1" || reader.window.StartChar != 5 {
		t.Fatalf("翻页参数应完整转交解析器：%v / %+v", err, reader.window)
	}
}

func Test文档工具不扩大已有工具列表(t *testing.T) {
	r, err := NewBuiltinRegistry(BuiltinOptions{Documents: &fakeDocumentReader{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Lookup(testAgent([]string{"read_file"}), "read_document"); ok {
		t.Fatal("已有显式工具列表不得自动获得文档工具")
	}
	if _, ok := r.Lookup(testAgent([]string{"read_document"}), "read_document"); !ok {
		t.Fatal("明确启用后应可读取文档")
	}
	legacy, _ := NewBuiltinRegistry(BuiltinOptions{})
	if _, ok := legacy.Get("read_document"); ok {
		t.Fatal("没有解析器的宿主不应宣告文档能力")
	}
}

func Test文档能力静态探测不执行或连接扩展(t *testing.T) {
	reader := &fakeDocumentReader{}
	f := newFixture(t, NewReadDocument(reader))
	e := newExecutor(t, f, 0)
	if !e.HasBuiltinTool("read_document") || e.HasBuiltinTool("missing") || reader.calls != 0 {
		t.Fatal("能力探测必须只查看登记信息，不执行文档读取")
	}
	empty := newExecutor(t, newFixture(t, NewReadDocument(nil)), 0)
	if empty.HasBuiltinTool("read_document") {
		t.Fatal("手动登记但没有阅读器时不得宣告能力")
	}
	var absent *Executor
	if absent.HasBuiltinTool("read_document") {
		t.Fatal("旧宿主未配置执行器时不得宣告能力")
	}
}
