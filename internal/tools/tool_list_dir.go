package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// maxListEntries 是 list_dir 返回的最大条目数。
const maxListEntries = 500

// listDir 列出工作区内的目录。
type listDir struct{}

// NewListDir 创建 list_dir 工具。
func NewListDir() Tool { return listDir{} }

type listDirArgs struct {
	Path string `json:"path"`
}

func (listDir) Spec() model.ToolSpec {
	return model.ToolSpec{
		Name:        "list_dir",
		Description: "列出工作区内某个目录的直接子项（目录在前）。省略 path 表示工作区根目录。",
		InputSchema: objectSchema(map[string]any{
			"path": stringProp("工作区相对路径；省略或 \".\" 表示根目录"),
		}),
		Source: SourceBuiltin,
		Risk:   model.RiskRead,
	}
}

func (listDir) parse(raw json.RawMessage) (listDirArgs, error) {
	var a listDirArgs
	if err := decodeArgs(raw, &a); err != nil {
		return a, err
	}
	a.Path = cleanRelPath(a.Path)
	return a, nil
}

func (t listDir) Describe(raw json.RawMessage) (Description, error) {
	a, err := t.parse(raw)
	if err != nil {
		return Description{}, err
	}
	return Description{Summary: "列出目录 " + a.Path, Details: map[string]any{"path": a.Path}}, nil
}

func (t listDir) Execute(ctx context.Context, rc RunContext, raw json.RawMessage) (model.ToolResult, error) {
	a, err := t.parse(raw)
	if err != nil {
		return model.ToolResult{}, err
	}
	entries, err := rc.Workspace.List(ctx, a.Path)
	if err != nil {
		return model.ToolResult{}, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "目录 %s（%d 项）\n", a.Path, len(entries))
	for i, e := range entries {
		if i >= maxListEntries {
			fmt.Fprintf(&b, "… 还有 %d 项未列出\n", len(entries)-maxListEntries)
			break
		}
		if e.IsDir {
			b.WriteString(e.Name)
			b.WriteString("/\n")
			continue
		}
		fmt.Fprintf(&b, "%s\t%d\n", e.Name, e.Size)
	}
	return model.ToolResult{Content: b.String()}, nil
}
