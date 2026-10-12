package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// maxWriteBytes 是单次写入的内容上限。
const maxWriteBytes = 4 << 20

// writeFile 在工作区内创建或覆盖文本文件。
type writeFile struct{}

// NewWriteFile 创建 write_file 工具。
func NewWriteFile() Tool { return writeFile{} }

// writeFileArgs 的 Content 用指针区分"没传"与"传了空字符串"：漏传时写出空文件会静默清空原内容。
type writeFileArgs struct {
	Path    string  `json:"path"`
	Content *string `json:"content"`
	Append  bool    `json:"append"`
}

func (a writeFileArgs) content() string {
	if a.Content == nil {
		return ""
	}
	return *a.Content
}

func (writeFile) Spec() model.ToolSpec {
	return model.ToolSpec{
		Name:        "write_file",
		Description: "把文本内容写入工作区内的文件：默认覆盖整个文件，append 为 true 时追加到末尾。父目录不存在时自动创建。",
		InputSchema: objectSchema(map[string]any{
			"path":    stringProp("工作区相对路径，例如 notes/todo.md"),
			"content": stringProp("要写入的完整文本内容"),
			"append":  boolProp("为 true 时追加到文件末尾而不是覆盖；默认 false"),
		}, "path", "content"),
		Source: SourceBuiltin,
		Risk:   model.RiskWrite,
	}
}

func (writeFile) parse(raw json.RawMessage) (writeFileArgs, error) {
	var a writeFileArgs
	if err := decodeArgs(raw, &a); err != nil {
		return a, err
	}
	a.Path = strings.TrimSpace(a.Path)
	if a.Path == "" {
		return a, model.NewError(model.CodeInvalid, "path 不能为空")
	}
	if a.Content == nil {
		return a, model.NewError(model.CodeInvalid, "content 不能缺失；要写空文件请传空字符串")
	}
	if len(*a.Content) > maxWriteBytes {
		return a, model.Errorf(model.CodeInvalid, "内容超过 %d 字节上限", maxWriteBytes)
	}
	return a, nil
}

func (t writeFile) Describe(raw json.RawMessage) (Description, error) {
	a, err := t.parse(raw)
	if err != nil {
		return Description{}, err
	}
	verb := "写入文件"
	if a.Append {
		verb = "追加到文件"
	}
	return Description{
		Summary: fmt.Sprintf("%s %s（%d 字节）", verb, a.Path, len(a.content())),
		Details: map[string]any{"path": a.Path, "bytes": len(a.content()), "append": a.Append},
	}, nil
}

func (t writeFile) Execute(ctx context.Context, rc RunContext, raw json.RawMessage) (model.ToolResult, error) {
	a, err := t.parse(raw)
	if err != nil {
		return model.ToolResult{}, err
	}
	data := []byte(a.content())
	if a.Append {
		existing, err := rc.Workspace.ReadFile(ctx, a.Path, maxWriteBytes)
		if err != nil && !model.IsCode(err, model.CodeNotFound) {
			return model.ToolResult{}, err
		}
		data = append(append(make([]byte, 0, len(existing)+len(data)), existing...), data...)
	}
	if err := rc.Workspace.WriteFile(ctx, a.Path, data); err != nil {
		return model.ToolResult{}, err
	}
	if a.Append {
		return model.ToolResult{Content: fmt.Sprintf("已追加 %d 字节到 %s（文件现为 %d 字节）", len(a.content()), a.Path, len(data))}, nil
	}
	return model.ToolResult{Content: fmt.Sprintf("已写入 %s（%d 字节）", a.Path, len(data))}, nil
}
