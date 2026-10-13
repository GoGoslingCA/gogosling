package preview

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// countedCancellation 在解析已读入内存的 XML 时确定性地触发取消，避免依赖机器时序。
type countedCancellation struct {
	context.Context
	checks int
}

func (c *countedCancellation) Err() error {
	c.checks++
	if c.checks >= 3 {
		return context.Canceled
	}
	return nil
}

func TestXML内存解析期间取消不会继续遍历全部节点(t *testing.T) {
	ctx := &countedCancellation{Context: context.Background()}
	raw := []byte("<document>" + strings.Repeat("<p><r><t>sample</t></r></p>", 8000) + "</document>")
	if _, err := parseXML(ctx, raw); !errors.Is(err, context.Canceled) {
		t.Fatalf("内存解析应响应取消而非完成全部节点: %v", err)
	}
	if ctx.checks != 3 {
		t.Fatalf("取消后不应继续解析: 检查次数=%d", ctx.checks)
	}
}

func Test普通文段不重复发送正文而混合格式保留顺序(t *testing.T) {
	in := packageInput(t, "plain.docx", map[string]string{"word/document.xml": `<document><body>
<p><r><t>before </t></r><r><t>plain</t></r></p>
<p><r><t>before </t></r><r><rPr><b/></rPr><t>bold</t></r><r><t> after</t></r></p>
<p><r><rPr><i/></rPr><t>first</t></r><r><t> after</t></r></p>
</body></document>`})
	doc, err := parseWord(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Blocks[0].Text != "before plain" || len(doc.Blocks[0].Runs) != 0 {
		t.Fatalf("普通文段应只发送完整正文: %+v", doc.Blocks[0])
	}
	if got := doc.Blocks[1]; got.Text != "before bold after" || len(got.Runs) != 3 || got.Runs[0].Text != "before " || !got.Runs[1].Bold || got.Runs[2].Text != " after" {
		t.Fatalf("首次格式之前与之后的文字应保持顺序: %+v", got)
	}
	if got := doc.Blocks[2]; len(got.Runs) != 2 || !got.Runs[0].Italic || got.Runs[1].Text != " after" {
		t.Fatalf("开头格式应保持原样: %+v", got)
	}
}
