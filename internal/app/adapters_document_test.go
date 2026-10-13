package app

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/tools"
	"github.com/GoGoslingCA/gogosling/internal/workbench/preview"
	"github.com/GoGoslingCA/gogosling/internal/workspace"
)

func documentTestReader(t *testing.T) (documentReader, workspaceAdapter) {
	t.Helper()
	dataDir := t.TempDir()
	ws, err := workspace.New(dataDir).Open(context.Background(), model.NewID(), "")
	if err != nil {
		t.Fatal(err)
	}
	return documentReader{previews: preview.New(nil, nil), dataDir: dataDir, locale: "en"}, workspaceAdapter{ws: ws}
}

func documentPackage(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	w := zip.NewWriter(&buffer)
	for name, text := range entries {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write([]byte(text)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func Test文档适配器读取Word并保持Unicode连续翻页(t *testing.T) {
	r, ws := documentTestReader(t)
	ctx := context.Background()
	content := documentPackage(t, map[string]string{"word/document.xml": `<document><body><p><r><t>中文🌿ABC</t></r></p><tbl><tr><tc><p><r><t>第二段</t></r></p></tc></tr></tbl></body></document>`})
	if err := ws.WriteFile(ctx, "plan.docx", content); err != nil {
		t.Fatal(err)
	}
	window := tools.DocumentWindow{StartChar: 1, MaxChars: 3}
	var body strings.Builder
	for n := 0; ; n++ {
		if n > 10 {
			t.Fatal("游标应有限次到达结尾")
		}
		page, err := r.ReadDocument(ctx, ws, "plan.docx", window)
		if err != nil || page.Format != "word" || page.Version == "" {
			t.Fatalf("应读取本地 Word 内容：%+v / %v", page, err)
		}
		count := 0
		for _, segment := range page.Segments {
			count += utf8.RuneCountInString(segment.Text)
			if !utf8.ValidString(segment.Text) || !strings.HasPrefix(segment.Location, "block:") {
				t.Fatalf("位置与字符边界必须保留：%+v", segment)
			}
			body.WriteString(segment.Text)
		}
		if count > 3 {
			t.Fatalf("正文超出页预算：%d", count)
		}
		if page.NextStartChar == 0 {
			break
		}
		window.StartChar, window.Version = page.NextStartChar, page.Version
	}
	if body.String() != "中文🌿ABC\n第二段\n" {
		t.Fatalf("连续翻页不得丢失或重复字符：%q", body.String())
	}
	if err := ws.WriteFile(ctx, "plan.docx", documentPackage(t, map[string]string{"word/document.xml": `<document><body><p><r><t>已经外部修改的内容</t></r></p></body></document>`})); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReadDocument(ctx, ws, "plan.docx", window); !model.IsCode(err, model.CodeConflict) {
		t.Fatalf("外部改动后旧游标必须拒绝：%v", err)
	}
}

func Test文档适配器区分支持格式和工作区边界(t *testing.T) {
	r, ws := documentTestReader(t)
	ctx := context.Background()
	window := tools.DocumentWindow{StartChar: 1, MaxChars: 100}
	if err := ws.WriteFile(ctx, "table.csv", []byte("name,value\nitem,42\n")); err != nil {
		t.Fatal(err)
	}
	page, err := r.ReadDocument(ctx, ws, "table.csv", window)
	if err != nil || len(page.Segments) != 2 || page.Segments[1].Location != "sheet:1/row:2" || page.Segments[1].Text != "item\t42\n" {
		t.Fatalf("表格应保留行定位与缓存值：%+v / %v", page, err)
	}
	for _, name := range []string{"paper.pdf", "old.doc", "web.html", "image.png", "pack.zip"} {
		if _, err := r.ReadDocument(ctx, ws, name, window); !model.IsCode(err, model.CodeUnavailable) {
			t.Fatalf("未实现的文本提取不得伪装成功：%s / %v", name, err)
		}
	}
	for _, name := range []string{"../outside.txt", filepath.Join(t.TempDir(), "outside.txt")} {
		if _, err := r.ReadDocument(ctx, ws, name, window); !model.IsCode(err, model.CodeForbidden) {
			t.Fatalf("不得越过工作区边界：%v", err)
		}
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(ws.Root(), "linked.txt")); err == nil {
		if _, err := r.ReadDocument(ctx, ws, "linked.txt", window); !model.IsCode(err, model.CodeForbidden) {
			t.Fatalf("不得读取工作区外符号链接：%v", err)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := r.ReadDocument(cancelled, ws, "table.csv", window); err == nil {
		t.Fatal("取消必须停止文件解析")
	}
}

func Test文档提取保留幻灯片行位置且不包含图片数据(t *testing.T) {
	doc := preview.Document{Format: "slides", Version: "v", Slides: []preview.Slide{
		{Blocks: []preview.Block{{Text: "首页"}, {Kind: "image", Image: "data:image/png;base64,fixture"}}},
		{Blocks: []preview.Block{{Text: "次页"}}},
	}, Sheets: []preview.Sheet{{Rows: [][]string{{"a", "b"}, {"", "2"}}}}}
	page, err := documentExcerpt(context.Background(), doc, tools.DocumentWindow{StartChar: 1, MaxChars: 100})
	if err != nil || len(page.Segments) != 4 || page.Segments[0].Location != "slide:1/block:1" || page.Segments[1].Location != "slide:2/block:1" || page.Segments[3].Location != "sheet:1/row:2" {
		t.Fatalf("幻灯片和表格行位置应保持稳定：%+v / %v", page, err)
	}
	encoded, _ := json.Marshal(page)
	if strings.Contains(string(encoded), "base64") || !containsDocumentWarning(page.Warnings, "non_text_omitted") {
		t.Fatal("图片不得混入文本结果，并须说明跳过非文本内容")
	}
}

func Test文档片段数量和序列化结果有界(t *testing.T) {
	doc := preview.Document{Format: "word", Version: "v", Warnings: []string{"truncated"}}
	for n := 0; n < 1000; n++ {
		doc.Blocks = append(doc.Blocks, preview.Block{Text: "<"})
	}
	page, err := documentExcerpt(context.Background(), doc, tools.DocumentWindow{StartChar: 1, MaxChars: tools.MaxDocumentChars})
	if err != nil || len(page.Segments) != maxDocumentSegments || page.NextStartChar != 129 || !page.Truncated {
		t.Fatalf("大量短段落也必须有界且可继续：%+v / %v", page, err)
	}
	doc.Blocks = []preview.Block{{Text: strings.Repeat("<", 10000)}}
	page, err = documentExcerpt(context.Background(), doc, tools.DocumentWindow{StartChar: 1, MaxChars: tools.MaxDocumentChars})
	encoded, _ := json.Marshal(page)
	if err != nil || len(encoded) >= tools.DefaultMaxResultBytes {
		t.Fatalf("JSON 转义后仍需低于结果上限：%d / %v", len(encoded), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := documentExcerpt(ctx, doc, tools.DocumentWindow{StartChar: 1, MaxChars: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("转换正文也必须尊重取消：%v", err)
	}
}
