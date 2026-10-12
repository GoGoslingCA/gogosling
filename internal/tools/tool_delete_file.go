package tools

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// WorkspaceRemover 是文件删除的可选工作区能力；实现必须保留根目录、附件目录与越界防护。
type WorkspaceRemover interface {
	Remove(ctx context.Context, rel string) error
}

// deleteFile 删除工作区文件；目录必须显式允许递归删除。
type deleteFile struct{ locale string }

// NewDeleteFile 创建 delete_file 工具；语言省略时使用中文。
func NewDeleteFile(locale ...string) Tool { return deleteFile{locale: toolLocale(locale)} }

type deleteFileArgs struct {
	Path      string `json:"path"`
	Recursive bool   `json:"recursive"`
}

func (t deleteFile) Spec() model.ToolSpec {
	return model.ToolSpec{
		Name: "delete_file", Description: toolText(t.locale, "delete_description", nil),
		InputSchema: objectSchema(map[string]any{
			"path":      stringProp(toolText(t.locale, "path_argument", nil)),
			"recursive": boolProp(toolText(t.locale, "recursive_argument", nil)),
		}, "path"),
		Source: SourceBuiltin, Risk: model.RiskDestructive,
	}
}

func (t deleteFile) parse(raw json.RawMessage) (deleteFileArgs, error) {
	var a deleteFileArgs
	if err := decodeArgs(raw, &a); err != nil {
		return a, err
	}
	a.Path = strings.TrimSpace(a.Path)
	if a.Path == "" {
		return a, model.NewError(model.CodeInvalid, toolText(t.locale, "path_required", nil))
	}
	return a, nil
}

func (t deleteFile) Describe(raw json.RawMessage) (Description, error) {
	a, err := t.parse(raw)
	if err != nil {
		return Description{}, err
	}
	return Description{
		Summary: toolText(t.locale, "delete_summary", map[string]any{"path": a.Path}),
		Details: map[string]any{"path": a.Path, "recursive": a.Recursive},
	}, nil
}

func (t deleteFile) Execute(ctx context.Context, rc RunContext, raw json.RawMessage) (model.ToolResult, error) {
	a, err := t.parse(raw)
	if err != nil {
		return model.ToolResult{}, err
	}
	remover, ok := rc.Workspace.(WorkspaceRemover)
	if !ok {
		return model.ToolResult{}, model.NewError(model.CodeUnavailable, toolText(t.locale, "delete_unavailable", nil))
	}
	entry, err := rc.Workspace.Stat(ctx, a.Path)
	if err != nil {
		return model.ToolResult{}, err
	}
	if entry.IsDir && !a.Recursive {
		return model.ToolResult{}, model.NewError(model.CodeInvalid, toolText(t.locale, "recursive_required", nil))
	}
	if err := remover.Remove(ctx, a.Path); err != nil {
		return model.ToolResult{}, err
	}
	return model.ToolResult{Content: toolText(t.locale, "delete_done", map[string]any{"path": a.Path})}, nil
}
