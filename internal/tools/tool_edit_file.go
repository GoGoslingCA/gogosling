package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/GoGoslingCA/gogosling/internal/i18n"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

// editFile 对已存在的 UTF-8 文本文件执行精确片段替换。
type editFile struct{ locale string }

// NewEditFile 创建 edit_file 工具；语言省略时沿用内置工具的中文默认值。
func NewEditFile(locale ...string) Tool { return editFile{locale: toolLocale(locale)} }

func toolLocale(locale []string) string {
	if len(locale) > 0 && locale[0] != "" {
		return locale[0]
	}
	return i18n.LocaleZH
}

func toolText(locale, key string, args map[string]any) string {
	return i18n.T(locale, "file_tools."+key, args)
}

type editFileArgs struct {
	Path           string  `json:"path"`
	OldText        *string `json:"old_text"`
	NewText        *string `json:"new_text"`
	ReplaceAll     bool    `json:"replace_all"`
	ExpectedSHA256 string  `json:"expected_sha256"`
}

func (t editFile) Spec() model.ToolSpec {
	return model.ToolSpec{
		Name: "edit_file", Description: toolText(t.locale, "edit_description", nil),
		InputSchema: objectSchema(map[string]any{
			"path":            stringProp(toolText(t.locale, "path_argument", nil)),
			"old_text":        stringProp(toolText(t.locale, "old_text_argument", nil)),
			"new_text":        stringProp(toolText(t.locale, "new_text_argument", nil)),
			"replace_all":     boolProp(toolText(t.locale, "replace_all_argument", nil)),
			"expected_sha256": stringProp(toolText(t.locale, "sha256_argument", nil)),
		}, "path", "old_text", "new_text"),
		Source: SourceBuiltin, Risk: model.RiskWrite,
	}
}

func (t editFile) parse(raw json.RawMessage) (editFileArgs, error) {
	var a editFileArgs
	if err := decodeArgs(raw, &a); err != nil {
		return a, err
	}
	a.Path = strings.TrimSpace(a.Path)
	if a.Path == "" {
		return a, model.NewError(model.CodeInvalid, toolText(t.locale, "path_required", nil))
	}
	if a.OldText == nil || *a.OldText == "" {
		return a, model.NewError(model.CodeInvalid, toolText(t.locale, "old_text_required", nil))
	}
	if a.NewText == nil {
		return a, model.NewError(model.CodeInvalid, toolText(t.locale, "new_text_required", nil))
	}
	if len(*a.OldText) > maxWriteBytes || len(*a.NewText) > maxWriteBytes {
		return a, model.NewError(model.CodeInvalid, toolText(t.locale, "too_large", map[string]any{"limit": maxWriteBytes}))
	}
	if !utf8.ValidString(*a.OldText) || !utf8.ValidString(*a.NewText) || strings.ContainsRune(*a.NewText, 0) {
		return a, model.NewError(model.CodeInvalid, toolText(t.locale, "text_required", nil))
	}
	a.ExpectedSHA256 = strings.ToLower(strings.TrimSpace(a.ExpectedSHA256))
	if a.ExpectedSHA256 != "" {
		a.ExpectedSHA256 = strings.TrimPrefix(a.ExpectedSHA256, "sha256:")
		if rawHash, err := hex.DecodeString(a.ExpectedSHA256); err != nil || len(rawHash) != sha256.Size {
			return a, model.NewError(model.CodeInvalid, toolText(t.locale, "sha256_invalid", nil))
		}
	}
	return a, nil
}

func (t editFile) Describe(raw json.RawMessage) (Description, error) {
	a, err := t.parse(raw)
	if err != nil {
		return Description{}, err
	}
	return Description{
		Summary: toolText(t.locale, "edit_summary", map[string]any{"path": a.Path}),
		Details: map[string]any{"path": a.Path, "replace_all": a.ReplaceAll,
			"old_bytes": len(*a.OldText), "new_bytes": len(*a.NewText)},
	}, nil
}

func (t editFile) Execute(ctx context.Context, rc RunContext, raw json.RawMessage) (model.ToolResult, error) {
	a, err := t.parse(raw)
	if err != nil {
		return model.ToolResult{}, err
	}
	data, err := rc.Workspace.ReadFile(ctx, a.Path, maxReadBytes)
	if err != nil {
		return model.ToolResult{}, err
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return model.ToolResult{}, model.NewError(model.CodeInvalid, toolText(t.locale, "text_required", nil))
	}
	if a.ExpectedSHA256 != "" {
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != a.ExpectedSHA256 {
			return model.ToolResult{}, model.NewError(model.CodeConflict, toolText(t.locale, "file_changed", nil))
		}
	}
	text := string(data)
	count := strings.Count(text, *a.OldText)
	if count == 0 {
		return model.ToolResult{}, model.NewError(model.CodeConflict, toolText(t.locale, "match_missing", nil))
	}
	if count > 1 && !a.ReplaceAll {
		return model.ToolResult{}, model.NewError(model.CodeConflict, toolText(t.locale, "match_ambiguous", map[string]any{"count": count}))
	}
	resultBytes := int64(len(data)) + int64(count)*(int64(len(*a.NewText))-int64(len(*a.OldText)))
	if resultBytes > maxWriteBytes {
		return model.ToolResult{}, model.NewError(model.CodeInvalid, toolText(t.locale, "too_large", map[string]any{"limit": maxWriteBytes}))
	}
	updated := strings.ReplaceAll(text, *a.OldText, *a.NewText)
	if err := rc.Workspace.WriteFile(ctx, a.Path, []byte(updated)); err != nil {
		return model.ToolResult{}, err
	}
	return model.ToolResult{Content: toolText(t.locale, "edit_done", map[string]any{"path": a.Path, "count": count, "bytes": len(updated)})}, nil
}
