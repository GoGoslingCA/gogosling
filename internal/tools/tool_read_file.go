package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// maxReadBytes 是 read_file 愿意从磁盘读取的最大字节数；更大的文件请用 start_line / max_lines 分段。
const maxReadBytes = 4 << 20

// readFile 读取工作区内的文本文件。
type readFile struct{}

// NewReadFile 创建 read_file 工具。
func NewReadFile() Tool { return readFile{} }

type readFileArgs struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	MaxLines  int    `json:"max_lines"`
}

func (readFile) Spec() model.ToolSpec {
	return model.ToolSpec{
		Name:        "read_file",
		Description: "读取工作区内一个文本文件的内容。大文件可用 start_line 与 max_lines 分段读取。二进制文件无法读取。",
		InputSchema: objectSchema(map[string]any{
			"path":       stringProp("工作区相对路径，例如 notes/todo.md"),
			"start_line": intProp("起始行号（从 1 开始）；省略表示从头开始", 1, 1<<30),
			"max_lines":  intProp("最多返回的行数；省略表示到文件末尾", 1, 1<<30),
		}, "path"),
		Source: SourceBuiltin,
		Risk:   model.RiskRead,
	}
}

func (readFile) parse(raw json.RawMessage) (readFileArgs, error) {
	var a readFileArgs
	if err := decodeArgs(raw, &a); err != nil {
		return a, err
	}
	a.Path = strings.TrimSpace(a.Path)
	if a.Path == "" {
		return a, model.NewError(model.CodeInvalid, "path 不能为空")
	}
	if a.StartLine < 0 || a.MaxLines < 0 {
		return a, model.NewError(model.CodeInvalid, "start_line 与 max_lines 不能为负数")
	}
	return a, nil
}

func (t readFile) Describe(raw json.RawMessage) (Description, error) {
	a, err := t.parse(raw)
	if err != nil {
		return Description{}, err
	}
	return Description{Summary: "读取文件 " + a.Path, Details: map[string]any{"path": a.Path}}, nil
}

func (t readFile) Execute(ctx context.Context, rc RunContext, raw json.RawMessage) (model.ToolResult, error) {
	a, err := t.parse(raw)
	if err != nil {
		return model.ToolResult{}, err
	}
	data, err := rc.Workspace.ReadFile(ctx, a.Path, maxReadBytes)
	if err != nil {
		return model.ToolResult{}, err
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return model.ToolResult{}, model.Errorf(model.CodeInvalid, "%s 是二进制文件（%d 字节），无法作为文本读取", a.Path, len(data))
	}
	text := string(data)
	var notes []string
	if !utf8.ValidString(text) {
		text = strings.ToValidUTF8(text, "�")
		notes = append(notes, "文件不是合法的 UTF-8，无法解码的字节已替换为 �")
	}
	if a.StartLine > 0 || a.MaxLines > 0 {
		text, note := sliceLines(text, a.StartLine, a.MaxLines)
		notes = append(notes, note)
		return model.ToolResult{Content: joinNotes(notes) + text}, nil
	}
	return model.ToolResult{Content: joinNotes(notes) + text}, nil
}

// sliceLines 取出 [start, start+max) 范围的行；start 从 1 开始，0 表示 1；max 为 0 表示到末尾。
func sliceLines(text string, start, max int) (string, string) {
	lines := strings.Split(text, "\n")
	// 以换行结尾的文本会多出一个空元素，不算作一行。
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	total := len(lines)
	if start < 1 {
		start = 1
	}
	if start > total {
		return "", fmt.Sprintf("[文件共 %d 行，起始行 %d 超出范围]", total, start)
	}
	end := total
	if max > 0 && start-1+max < end {
		end = start - 1 + max
	}
	out := strings.Join(lines[start-1:end], "\n")
	if end < total {
		out += "\n"
	}
	return out, fmt.Sprintf("[第 %d-%d 行，共 %d 行]", start, end, total)
}

func joinNotes(notes []string) string {
	var b strings.Builder
	for _, n := range notes {
		if n == "" {
			continue
		}
		b.WriteString(n)
		b.WriteString("\n")
	}
	return b.String()
}
