package app

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/GoGoslingCA/gogosling/internal/i18n"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/tools"
	"github.com/GoGoslingCA/gogosling/internal/workbench/preview"
)

const maxDocumentSegments = 64

// documentReader 连接文档工具与现有解析服务，不启动转换器或后台索引。
type documentReader struct {
	previews *preview.Service
	dataDir  string
	locale   string
}

func (r documentReader) ReadDocument(ctx context.Context, approved tools.Workspace, rel string, window tools.DocumentWindow) (tools.DocumentText, error) {
	if err := ctx.Err(); err != nil {
		return tools.DocumentText{}, err
	}
	ws, ok := approved.(workspaceAdapter)
	if !ok || ws.ws == nil || r.previews == nil {
		return tools.DocumentText{}, model.NewError(model.CodeUnavailable, i18n.T(r.locale, "file_tools.document_unavailable", nil))
	}
	if !previewRootAllowed(ws.Root(), r.dataDir) {
		return tools.DocumentText{}, model.NewError(model.CodeForbidden, "preview_access")
	}
	switch r.previews.Detect(rel).ID {
	case "markdown", "unknown", "csv", "word", "slides", "sheets", "opendocument":
	default:
		return tools.DocumentText{}, model.NewError(model.CodeUnavailable, i18n.T(r.locale, "file_tools.document_unsupported", nil))
	}
	f, err := ws.ws.OpenRead(ctx, rel)
	if err != nil {
		return tools.DocumentText{}, err
	}
	defer f.Close()
	doc, err := r.previews.PreviewFile(ctx, f)
	if err != nil {
		return tools.DocumentText{}, err
	}
	if window.Version != "" && window.Version != doc.Version {
		return tools.DocumentText{}, preview.Error("changed")
	}
	return documentExcerpt(ctx, doc, window)
}

// documentExcerpt 按 Unicode 字符分页，只保留当前页，位置使用稳定的序号而不是额外复制文件内容。
func documentExcerpt(ctx context.Context, doc preview.Document, window tools.DocumentWindow) (tools.DocumentText, error) {
	out := tools.DocumentText{Format: doc.Format, Version: doc.Version, Segments: []tools.DocumentSegment{}, Warnings: doc.Warnings}
	remaining, position := window.MaxChars, 1
	add := func(location, text string) {
		if text == "" || ctx.Err() != nil {
			return
		}
		// 每段末尾的换行也是游标的一部分，分页后拼接正文不会粘连段落。
		if !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		count := utf8.RuneCountInString(text)
		partStart, partEnd := position, position+count
		position = partEnd
		if partEnd <= window.StartChar || remaining == 0 || len(out.Segments) >= maxDocumentSegments {
			return
		}
		start := max(window.StartChar-partStart, 0)
		length := min(count-start, remaining)
		if length > 0 {
			out.Segments = append(out.Segments, tools.DocumentSegment{Location: location, StartChar: partStart + start, Text: runeSlice(text, start, length)})
			remaining -= length
		}
	}
	blocks := func(prefix string, blocks []preview.Block) {
		for n, block := range blocks {
			location := fmt.Sprintf("%sblock:%d", prefix, n+1)
			if len(block.Rows) > 0 {
				for row, cells := range block.Rows {
					add(fmt.Sprintf("%s/row:%d", location, row+1), strings.Join(cells, "\t"))
				}
			} else {
				add(location, block.Text)
				if block.Kind == "image" && !containsDocumentWarning(out.Warnings, "non_text_omitted") {
					out.Warnings = append(out.Warnings, "non_text_omitted")
				}
			}
		}
	}
	add("text", doc.Text)
	blocks("", doc.Blocks)
	for n, slide := range doc.Slides {
		blocks(fmt.Sprintf("slide:%d/", n+1), slide.Blocks)
	}
	for n, sheet := range doc.Sheets {
		for row, cells := range sheet.Rows {
			add(fmt.Sprintf("sheet:%d/row:%d", n+1, row+1), strings.Join(cells, "\t"))
		}
	}
	if err := ctx.Err(); err != nil {
		return tools.DocumentText{}, err
	}
	out.TotalChars = position - 1
	if len(out.Segments) > 0 {
		last := out.Segments[len(out.Segments)-1]
		next := last.StartChar + utf8.RuneCountInString(last.Text)
		if next <= out.TotalChars {
			out.NextStartChar = next
		}
	}
	out.Truncated = out.NextStartChar > 0
	for _, warning := range doc.Warnings {
		if warning == "truncated" {
			out.Truncated = true
		}
	}
	return out, nil
}

func containsDocumentWarning(warnings []string, want string) bool {
	for _, warning := range warnings {
		if warning == want {
			return true
		}
	}
	return false
}

// runeSlice 不创建整段 []rune，返回字符边界内的子串。
func runeSlice(text string, start, length int) string {
	startByte, count := 0, 0
	for offset := range text {
		if count == start {
			startByte = offset
		}
		if count == start+length {
			return text[startByte:offset]
		}
		count++
	}
	return text[startByte:]
}
