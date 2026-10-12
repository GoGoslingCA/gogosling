package preview

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func packageInput(t *testing.T, name string, parts map[string]string) Input {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for n, text := range parts {
		f, e := w.Create(n)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.Write([]byte(text)); e != nil {
			t.Fatal(e)
		}
	}
	if e := w.Close(); e != nil {
		t.Fatal(e)
	}
	return Input{Name: name, Size: int64(b.Len()), Reader: bytes.NewReader(b.Bytes())}
}
func Test文档读取标题正文表格且不运行宏(t *testing.T) {
	in := packageInput(t, "plan.docm", map[string]string{"word/document.xml": `<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="w"><w:body><w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:rPr><w:b/></w:rPr><w:t>项目计划</w:t></w:r></w:p><w:tbl><w:tr><w:tc><w:p><w:r><w:t>第一项</w:t></w:r></w:p></w:tc></w:tr></w:tbl></w:body></w:document>`, "word/vbaProject.bin": "untrusted macro"})
	doc, e := parseWord(context.Background(), in)
	if e != nil {
		t.Fatal(e)
	}
	if len(doc.Blocks) != 2 || doc.Blocks[0].Kind != "heading" || doc.Blocks[0].Text != "项目计划" || !doc.Blocks[0].Runs[0].Bold || doc.Blocks[1].Rows[0][0] != "第一项" {
		t.Fatalf("内容丢失: %+v", doc)
	}
}
func Test演示文稿遵守逻辑顺序而非文件名(t *testing.T) {
	in := packageInput(t, "deck.pptx", map[string]string{"ppt/presentation.xml": `<p:presentation xmlns:p="p" xmlns:r="r"><p:sldIdLst><p:sldId r:id="second"/><p:sldId r:id="first"/></p:sldIdLst></p:presentation>`, "ppt/_rels/presentation.xml.rels": `<Relationships><Relationship Id="first" Target="slides/slide1.xml"/><Relationship Id="second" Target="slides/slide2.xml"/></Relationships>`, "ppt/slides/slide1.xml": `<sld><cSld><spTree><sp><p><r><t>后页</t></r></p></sp></spTree></cSld></sld>`, "ppt/slides/slide2.xml": `<sld><cSld><spTree><sp><p><r><t>首页</t></r></p></sp></spTree></cSld></sld>`})
	doc, e := parseSlides(context.Background(), in)
	if e != nil {
		t.Fatal(e)
	}
	if len(doc.Slides) != 2 || doc.Slides[0].Blocks[0].Text != "首页" {
		t.Fatalf("页序错误: %+v", doc)
	}
}
func Test工作表共享字符串稀疏坐标缓存值与公式不执行(t *testing.T) {
	in := packageInput(t, "data.xlsx", map[string]string{"xl/workbook.xml": `<workbook xmlns:r="r"><sheets><sheet name="数据" r:id="r1"/></sheets></workbook>`, "xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="r1" Target="/xl/worksheets/sheet1.xml"/></Relationships>`, "xl/sharedStrings.xml": `<sst><si><t>标题</t></si></sst>`, "xl/worksheets/sheet1.xml": `<worksheet><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="C1"><f>HYPERLINK("https://bad.invalid")</f></c></row><row r="2"><c r="B2"><f>1+1</f><v>2</v></c></row></sheetData></worksheet>`})
	doc, e := parseSheets(context.Background(), in)
	if e != nil {
		t.Fatal(e)
	}
	rows := doc.Sheets[0].Rows
	if rows[0][0] != "标题" || rows[0][1] != "" || !strings.HasPrefix(rows[0][2], "=HYPERLINK") || rows[1][1] != "2" {
		t.Fatalf("网格读取错误: %v", rows)
	}
}
func Test开放文档保留混合文字顺序并限制重复单元格(t *testing.T) {
	in := packageInput(t, "plan.odt", map[string]string{"content.xml": `<document-content><body><text><p>前<span>中</span>后</p></text></body></document-content>`})
	doc, e := parseOpenDocument(context.Background(), in)
	if e != nil || doc.Blocks[0].Text != "前中后" {
		t.Fatalf("文段顺序错误: %+v %v", doc, e)
	}
	in = packageInput(t, "data.ods", map[string]string{"content.xml": `<document-content><body><spreadsheet><table name="数据"><table-row number-rows-repeated="999999999"><table-cell number-columns-repeated="999999999"><p>值</p></table-cell></table-row></table></spreadsheet></body></document-content>`})
	doc, e = parseOpenDocument(context.Background(), in)
	if e != nil || len(doc.Sheets) != 1 || len(doc.Sheets[0].Rows) > 2000 || len(doc.Sheets[0].Rows[0]) > MaxColumns || !strings.Contains(strings.Join(doc.Warnings, ","), "truncated") {
		t.Fatalf("重复预算没有生效: %v", e)
	}
}
func Test文本UTF16与CSV转义换行(t *testing.T) {
	raw := []byte{0xff, 0xfe}
	for _, v := range []uint16{0x4e2d, 0x6587} {
		raw = binary.LittleEndian.AppendUint16(raw, v)
	}
	doc, e := parseText(context.Background(), Input{Name: "note.txt", Size: int64(len(raw)), Reader: bytes.NewReader(raw)})
	if e != nil || doc.Text != "中文" {
		t.Fatalf("UTF16读取失败: %+v %v", doc, e)
	}
	text := "name,note\nAlice,\"line 1\nline 2\"\n"
	doc, e = parseDelimited(context.Background(), Input{Name: "test.csv", Size: int64(len(text)), Reader: strings.NewReader(text)})
	if e != nil || doc.Sheets[0].Rows[1][1] != "line 1\nline 2" {
		t.Fatalf("CSV多行值错误: %+v %v", doc, e)
	}
}
func Test损坏与恶意包不进入其他格式(t *testing.T) {
	for _, parts := range []map[string]string{{"../escape": "x"}, {"a": "x", "/root": "y"}, {"word/document.xml": "<!DOCTYPE x [<!ENTITY a SYSTEM 'file:///etc/passwd'>]><document>&a;</document>"}, {"word/document.xml": strings.Repeat("<p>", 90) + strings.Repeat("</p>", 90)}} {
		in := packageInput(t, "bad.docx", parts)
		if _, e := parseWord(context.Background(), in); e == nil {
			t.Fatal("危险或损坏包不应成功")
		}
	}
	in := packageInput(t, "bomb.zip", map[string]string{"large": strings.Repeat("x", MaxExpanded+1)})
	if _, e := parseArchive(context.Background(), in); e == nil {
		t.Fatal("声明展开超限应拒绝")
	}
}
func Test压缩包只提供目录(t *testing.T) {
	doc, e := parseArchive(context.Background(), packageInput(t, "a.zip", map[string]string{"nested/file.txt": "内容"}))
	if e != nil || len(doc.Entries) != 1 || doc.Entries[0].Size != 6 {
		t.Fatalf("目录读取失败: %+v %v", doc, e)
	}
}

type fileSource struct{ path string }

func (s fileSource) Open(ctx context.Context, agentID, rel string) (File, error) {
	return os.Open(s.path)
}
func Test单个解析器失败取消或忙碌不阻断后续文件(t *testing.T) {
	path := t.TempDir() + "/test.custom"
	if e := os.WriteFile(path, []byte("safe"), 0o600); e != nil {
		t.Fatal(e)
	}
	entered := make(chan struct{})
	var once sync.Once
	parser := ParseFunc(func(ctx context.Context, in Input) (Document, error) {
		once.Do(func() { close(entered) })
		<-ctx.Done()
		return Document{}, ctx.Err()
	})
	svc := New(fileSource{path: path}, nil, Format{ID: "test", Kind: "text", Extensions: []string{".custom"}, Parser: parser})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, e := svc.Preview(ctx, "a", "test.custom"); done <- e }()
	<-entered
	if _, e := svc.Preview(context.Background(), "a", "test.custom"); e == nil {
		t.Fatal("不应创建无界等待队列")
	}
	cancel()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("取消必须可见")
		}
	case <-time.After(time.Second):
		t.Fatal("取消未及时释放")
	}
	svc = New(fileSource{path: path}, nil)
	doc, e := svc.Preview(context.Background(), "a", "test.custom")
	if e != nil || doc.Text != "safe" {
		t.Fatalf("后续文件应正常: %+v %v", doc, e)
	}
}
func Test包签名与文本二进制均拒绝伪装(t *testing.T) {
	for _, parse := range []ParseFunc{parseWord, parseSlides, parseSheets, validatePDF} {
		if _, e := parse(context.Background(), Input{Name: "fake", Size: 4, Reader: strings.NewReader("fake")}); e == nil {
			t.Fatal("签名不符应失败")
		}
	}
	if _, e := parseText(context.Background(), Input{Size: 3, Reader: bytes.NewReader([]byte{0, 1, 2})}); e == nil {
		t.Fatal("二进制不能伪装文本")
	}
}
func Test注册模块可复用内容描述(t *testing.T) {
	file := t.TempDir() + "/test.new"
	_ = os.WriteFile(file, []byte("test"), 0o600)
	svc := New(fileSource{path: file}, nil, Format{ID: "new", Kind: "text", Extensions: []string{".new"}, Parser: ParseFunc(func(context.Context, Input) (Document, error) { return Document{Text: "新格式"}, nil })})
	doc, e := svc.Preview(context.Background(), "a", "test.new")
	if e != nil || doc.Format != "new" || doc.Text != "新格式" {
		t.Fatal("独立模块注册失败")
	}
	b, e := json.Marshal(doc)
	if e != nil || !bytes.Contains(b, []byte(`"kind":"text"`)) {
		t.Fatal("输出契约错误")
	}
}

func Test统一错误不回显底层路径和正文(t *testing.T) {
	raw := fmt.Errorf("parse /private/sensitive/location: document contents")
	safe := SafeError(raw)
	if strings.Contains(safe.Error(), "sensitive") || strings.Contains(safe.Error(), "contents") {
		t.Fatal("统一错误不能泄漏原始异常")
	}
}
