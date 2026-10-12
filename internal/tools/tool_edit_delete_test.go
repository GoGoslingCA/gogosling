package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/workspace"
)

// realToolWorkspace 把真实工作区接入工具，确保路径与删除保护不是由测试替身提供。
type realToolWorkspace struct{ *workspace.Workspace }

func (w realToolWorkspace) List(ctx context.Context, rel string) ([]Entry, error) {
	entries, err := w.Workspace.List(ctx, rel)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, len(entries))
	for i, e := range entries {
		out[i] = Entry(e)
	}
	return out, nil
}

func (w realToolWorkspace) Stat(ctx context.Context, rel string) (Entry, error) {
	entry, err := w.Workspace.Stat(ctx, rel)
	return Entry(entry), err
}

func newRealToolWorkspace(t *testing.T) realToolWorkspace {
	t.Helper()
	ws, err := workspace.New(t.TempDir()).Open(context.Background(), "test-agent", "")
	if err != nil {
		t.Fatal(err)
	}
	return realToolWorkspace{ws}
}

func TestEditFileReplacesOnlyExactFragmentAndChecksDigest(t *testing.T) {
	ctx := context.Background()
	ws := newRealToolWorkspace(t)
	original := "标题\r\n  保持缩进\r\n待更新内容\r\n尾部\r\n"
	if err := ws.WriteFile(ctx, "docs/notes.md", []byte(original)); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(original))
	args := map[string]any{"path": "docs/notes.md", "old_text": "待更新内容", "new_text": "新内容", "expected_sha256": hex.EncodeToString(sum[:])}
	tool := NewEditFile()
	res, err := tool.Execute(ctx, RunContext{Workspace: ws}, rawArgs(t, args))
	if err != nil || res.IsError {
		t.Fatalf("有效摘要与唯一片段应编辑成功：%+v / %v", res, err)
	}
	updated, _ := ws.ReadFile(ctx, "docs/notes.md", 0)
	if string(updated) != strings.Replace(original, "待更新内容", "新内容", 1) {
		t.Fatalf("编辑应保留其余内容、缩进与 CRLF：%q", updated)
	}
	args["old_text"], args["new_text"] = "新内容", ""
	if _, err := tool.Execute(ctx, RunContext{Workspace: ws}, rawArgs(t, args)); !model.IsCode(err, model.CodeConflict) {
		t.Fatalf("过期摘要应拒绝编辑：%v", err)
	}
	delete(args, "expected_sha256")
	if _, err := tool.Execute(ctx, RunContext{Workspace: ws}, rawArgs(t, args)); err != nil {
		t.Fatalf("显式空替换内容应删除片段：%v", err)
	}
	updated, _ = ws.ReadFile(ctx, "docs/notes.md", 0)
	if strings.Contains(string(updated), "新内容") {
		t.Fatalf("片段应被删除：%q", updated)
	}
}

func TestEditFileRejectsAmbiguousAndMissingMatchesWithoutWriting(t *testing.T) {
	ws := newFakeWorkspace(t)
	mustWrite(t, ws, "x.txt", "token token\n")
	tool := NewEditFile()
	for _, old := range []string{"token", "missing"} {
		args := rawArgs(t, map[string]any{"path": "x.txt", "old_text": old, "new_text": "changed"})
		if _, err := tool.Execute(context.Background(), RunContext{Workspace: ws}, args); !model.IsCode(err, model.CodeConflict) {
			t.Errorf("多处匹配与无匹配应为冲突：%v", err)
		}
		data, _ := ws.ReadFile(context.Background(), "x.txt", 0)
		if string(data) != "token token\n" {
			t.Fatalf("冲突不得覆盖文件：%q", data)
		}
	}
	res, err := tool.Execute(context.Background(), RunContext{Workspace: ws}, rawArgs(t, map[string]any{"path": "x.txt", "old_text": "token", "new_text": "value", "replace_all": true}))
	data, _ := ws.ReadFile(context.Background(), "x.txt", 0)
	if err != nil || string(data) != "value value\n" || !strings.Contains(res.Content, "2") {
		t.Fatalf("显式全量替换应更新两处：%q / %+v / %v", data, res, err)
	}
}

func TestEditFileRejectsInvalidArgumentsBinaryAndOversizedResults(t *testing.T) {
	ws := newFakeWorkspace(t)
	tool := NewEditFile()
	for _, args := range []map[string]any{
		{"path": "x.txt", "old_text": "x"},
		{"path": "x.txt", "old_text": "", "new_text": "a"},
		{"path": "x.txt", "old_text": "x", "new_text": "a\x00"},
		{"path": "x.txt", "old_text": "x", "new_text": "a", "expected_sha256": "wrong"},
		{"path": "x.txt", "old_text": "x", "new_text": "a", "expected_sha256": "sha256:"},
	} {
		if _, err := tool.Execute(context.Background(), RunContext{Workspace: ws}, rawArgs(t, args)); !model.IsCode(err, model.CodeInvalid) {
			t.Errorf("非法编辑参数应拒绝：%v / %v", args, err)
		}
	}
	for _, data := range []string{"a\x00b", "caf\xe9"} {
		mustWrite(t, ws, "binary", data)
		if _, err := tool.Execute(context.Background(), RunContext{Workspace: ws}, rawArgs(t, map[string]any{"path": "binary", "old_text": "a", "new_text": "b"})); !model.IsCode(err, model.CodeInvalid) {
			t.Fatalf("二进制与非法 UTF-8 不得编辑：%v", err)
		}
	}
	mustWrite(t, ws, "large.txt", strings.Repeat("x", maxWriteBytes))
	args := rawArgs(t, map[string]any{"path": "large.txt", "old_text": "x", "new_text": "xx", "replace_all": true})
	if _, err := tool.Execute(context.Background(), RunContext{Workspace: ws}, args); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("替换后的大小必须在分配与写入前限制：%v", err)
	}
	data, _ := ws.ReadFile(context.Background(), "large.txt", 0)
	if len(data) != maxWriteBytes {
		t.Fatal("超大结果拒绝后原文件不应改变")
	}
}

func TestDeleteFileRequiresExplicitRecursiveAndProtectsWorkspace(t *testing.T) {
	ctx := context.Background()
	ws := newRealToolWorkspace(t)
	if err := ws.WriteFile(ctx, "notes/nested/x.txt", []byte("x")); err != nil {
		t.Fatal(err)
	}
	tool := NewDeleteFile()
	rc := RunContext{Workspace: ws}
	if _, err := tool.Execute(ctx, rc, rawArgs(t, map[string]any{"path": "notes"})); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("省略 recursive 必须拒绝目录删除：%v", err)
	}
	if _, err := ws.ReadFile(ctx, "notes/nested/x.txt", 0); err != nil {
		t.Fatalf("拒绝后子文件应保留：%v", err)
	}
	for _, protected := range []string{".", "inbound", "../outside"} {
		if _, err := tool.Execute(ctx, rc, rawArgs(t, map[string]any{"path": protected, "recursive": true})); !model.IsCode(err, model.CodeForbidden) {
			t.Fatalf("根目录、附件目录和越界路径必须受到保护：%s / %v", protected, err)
		}
	}
	if _, err := tool.Execute(ctx, rc, rawArgs(t, map[string]any{"path": "notes", "recursive": true})); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.Stat(ctx, "notes"); !model.IsCode(err, model.CodeNotFound) {
		t.Fatalf("显式递归删除后目录不应存在：%v", err)
	}
	if _, err := tool.Execute(ctx, rc, rawArgs(t, map[string]any{"path": "missing"})); !model.IsCode(err, model.CodeNotFound) {
		t.Fatalf("不存在的文件应为 not_found：%v", err)
	}
	if _, err := tool.Execute(ctx, RunContext{Workspace: newFakeWorkspace(t)}, rawArgs(t, map[string]any{"path": "x"})); !model.IsCode(err, model.CodeUnavailable) {
		t.Fatalf("缺少可选删除能力时必须明确报错：%v", err)
	}
}

func TestEditAndDeleteFilePermissionDenialHasNoSideEffects(t *testing.T) {
	ctx := context.Background()
	ws := newRealToolWorkspace(t)
	if err := ws.WriteFile(ctx, "a.txt", []byte("original")); err != nil {
		t.Fatal(err)
	}
	f := newFixture(t, NewEditFile(), NewDeleteFile())
	f.rc.Workspace = ws
	f.checker.verdict = Verdict{Allowed: false, Reason: "denied"}
	e := newExecutor(t, f, 0)
	for _, invocation := range []model.ToolCall{
		call("edit_file", `{"path":"a.txt","old_text":"original","new_text":"changed"}`),
		call("delete_file", `{"path":"a.txt"}`),
	} {
		res, err := e.Execute(ctx, "01RUN", invocation)
		if err != nil || !res.IsError {
			t.Fatalf("权限拒绝应成为工具错误结果：%+v / %v", res, err)
		}
		data, err := ws.ReadFile(ctx, "a.txt", 0)
		if err != nil || string(data) != "original" {
			t.Fatalf("拒绝操作不得修改文件：%q / %v", data, err)
		}
		request := f.checker.last(t)
		if request.Tool.Name == "delete_file" && request.Tool.Risk != model.RiskDestructive {
			t.Fatal("删除工具必须向权限层声明破坏性风险")
		}
	}
	f.checker.verdict = Verdict{Allowed: true}
	res, err := e.Execute(ctx, "01RUN", call("delete_file", `{"path":"a.txt"}`))
	if err != nil || res.IsError {
		t.Fatalf("批准后应完成删除：%+v / %v", res, err)
	}
	if _, err := ws.Stat(ctx, "a.txt"); !model.IsCode(err, model.CodeNotFound) {
		t.Fatal("批准后的文件应被删除")
	}
}

func TestFileToolsRejectSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 创建符号链接需要额外权限")
	}
	ctx := context.Background()
	ws := newRealToolWorkspace(t)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(ws.Root(), "link.txt")); err != nil {
		t.Fatal(err)
	}
	for _, invocation := range []struct {
		tool Tool
		args map[string]any
	}{
		{NewEditFile(), map[string]any{"path": "link.txt", "old_text": "original", "new_text": "changed"}},
		{NewDeleteFile(), map[string]any{"path": "link.txt"}},
		{NewSearchFiles(), map[string]any{"query": "original", "path": "../"}},
	} {
		if _, err := invocation.tool.Execute(ctx, RunContext{Workspace: ws}, rawArgs(t, invocation.args)); !model.IsCode(err, model.CodeForbidden) {
			t.Fatalf("越界与符号链接逃逸必须拒绝：%s / %v", invocation.tool.Spec().Name, err)
		}
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "original" {
		t.Fatalf("工作区外文件不应被修改或删除：%q / %v", data, err)
	}
}

func TestFileToolLocalization(t *testing.T) {
	for _, tool := range []Tool{NewEditFile("en"), NewDeleteFile("en")} {
		if strings.Contains(tool.Spec().Description, "工作区") || strings.Contains(tool.Spec().Description, "file_tools.") {
			t.Fatalf("英文工具说明必须取自英文语言包：%s", tool.Spec().Description)
		}
		desc, err := tool.(Describer).Describe(rawArgs(t, map[string]any{"path": "a.txt", "old_text": "x", "new_text": "y"}))
		if err != nil || !strings.Contains(desc.Summary, "a.txt") || strings.Contains(desc.Summary, "file_tools.") {
			t.Fatalf("审批摘要必须正确本地化：%+v / %v", desc, err)
		}
	}
}
