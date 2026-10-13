package tools

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// MaxDocumentChars 限制单次正文输出；最坏 JSON 转义后仍低于默认工具结果大小限制。
const MaxDocumentChars = 8000

type readDocument struct {
	reader DocumentReader
	locale string
}

// NewReadDocument 创建文档阅读工具；它不隐式扩大已有智能体的工具权限。
func NewReadDocument(reader DocumentReader, locale ...string) Tool {
	return readDocument{reader: reader, locale: toolLocale(locale)}
}

func (t readDocument) staticAvailable() bool { return t.reader != nil }

type readDocumentArgs struct {
	Path      string `json:"path"`
	StartChar int    `json:"start_char"`
	MaxChars  int    `json:"max_chars"`
	Version   string `json:"version"`
}

func (t readDocument) Spec() model.ToolSpec {
	return model.ToolSpec{
		Name: "read_document", Description: toolText(t.locale, "document_description", nil),
		InputSchema: objectSchema(map[string]any{
			"path":       stringProp(toolText(t.locale, "path_argument", nil)),
			"start_char": intProp(toolText(t.locale, "document_start_argument", nil), 1, 1<<30),
			"max_chars":  intProp(toolText(t.locale, "document_max_argument", nil), 1, MaxDocumentChars),
			"version":    stringProp(toolText(t.locale, "document_version_argument", nil)),
		}, "path"),
		Source: SourceBuiltin, Risk: model.RiskRead,
	}
}

func (t readDocument) parse(raw json.RawMessage) (readDocumentArgs, error) {
	var a readDocumentArgs
	if err := decodeArgs(raw, &a); err != nil {
		return a, err
	}
	a.Path = strings.TrimSpace(a.Path)
	if a.Path == "" || len(a.Path) > 4096 || strings.ContainsRune(a.Path, 0) {
		return a, model.NewError(model.CodeInvalid, toolText(t.locale, "path_required", nil))
	}
	if a.StartChar < 0 || a.StartChar > 1<<30 || a.MaxChars < 0 || a.MaxChars > MaxDocumentChars || len(a.Version) > 128 {
		return a, model.NewError(model.CodeInvalid, toolText(t.locale, "document_window_invalid", nil))
	}
	if a.StartChar == 0 {
		a.StartChar = 1
	}
	if a.MaxChars == 0 {
		a.MaxChars = MaxDocumentChars
	}
	if a.StartChar > 1 && a.Version == "" {
		return a, model.NewError(model.CodeInvalid, toolText(t.locale, "document_version_required", nil))
	}
	return a, nil
}

func (t readDocument) Describe(raw json.RawMessage) (Description, error) {
	a, err := t.parse(raw)
	if err != nil {
		return Description{}, err
	}
	return Description{
		Summary: toolText(t.locale, "document_summary", map[string]any{"path": a.Path}),
		Details: map[string]any{"path": a.Path, "start_char": a.StartChar, "max_chars": a.MaxChars},
	}, nil
}

func (t readDocument) Execute(ctx context.Context, rc RunContext, raw json.RawMessage) (model.ToolResult, error) {
	a, err := t.parse(raw)
	if err != nil {
		return model.ToolResult{}, err
	}
	if t.reader == nil || rc.Workspace == nil {
		return model.ToolResult{}, model.NewError(model.CodeUnavailable, toolText(t.locale, "document_unavailable", nil))
	}
	if err := ctx.Err(); err != nil {
		return model.ToolResult{}, err
	}
	doc, err := t.reader.ReadDocument(ctx, rc.Workspace, a.Path, DocumentWindow{StartChar: a.StartChar, MaxChars: a.MaxChars, Version: a.Version})
	if err != nil {
		return model.ToolResult{}, err
	}
	body, err := json.Marshal(doc)
	if err != nil {
		return model.ToolResult{}, err
	}
	return model.ToolResult{Content: string(body)}, nil
}
