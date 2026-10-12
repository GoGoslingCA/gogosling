package workspace

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// pngHeader 是 PNG 文件的魔数，足以让内容嗅探识别出 image/png。
var pngHeader = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}

func TestSaveInboundSanitizesAndStores(t *testing.T) {
	_, ws := openTestWorkspace(t)
	ctx := context.Background()
	att, err := ws.SaveInbound(ctx, "../../evil/../re\x01port.png", bytes.NewReader(pngHeader), 1024)
	if err != nil {
		t.Fatalf("SaveInbound 失败: %v", err)
	}
	if att.Name != "report.png" {
		t.Errorf("Name = %q，期望去掉目录与控制字符后的 report.png", att.Name)
	}
	if att.Path != InboundDir+"/report.png" {
		t.Errorf("Path = %q，期望以 inbound/ 开头的相对路径", att.Path)
	}
	if att.MIME != "image/png" {
		t.Errorf("MIME = %q，期望 image/png", att.MIME)
	}
	if att.Size != int64(len(pngHeader)) {
		t.Errorf("Size = %d，期望 %d", att.Size, len(pngHeader))
	}
	data, err := os.ReadFile(filepath.Join(ws.Root(), InboundDir, "report.png"))
	if err != nil || !bytes.Equal(data, pngHeader) {
		t.Fatalf("落盘内容不符: %v", err)
	}
	// 不应残留临时文件。
	entries, _ := os.ReadDir(filepath.Join(ws.Root(), InboundDir))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("残留临时文件 %s", e.Name())
		}
	}
}

func TestSaveInboundDeduplicatesNames(t *testing.T) {
	_, ws := openTestWorkspace(t)
	ctx := context.Background()
	var names []string
	for i := 0; i < 3; i++ {
		att, err := ws.SaveInbound(ctx, "data.csv", strings.NewReader("a,b\n"), 0)
		if err != nil {
			t.Fatalf("第 %d 次保存失败: %v", i, err)
		}
		names = append(names, att.Name)
	}
	want := "data.csv,data (1).csv,data (2).csv"
	if got := strings.Join(names, ","); got != want {
		t.Fatalf("重名序号 = %s，期望 %s", got, want)
	}
	// 无扩展名与点开头的名字也能追加序号。
	for _, name := range []string{"README", ".env"} {
		first, err := ws.SaveInbound(ctx, name, strings.NewReader("1"), 0)
		if err != nil {
			t.Fatal(err)
		}
		second, err := ws.SaveInbound(ctx, name, strings.NewReader("2"), 0)
		if err != nil {
			t.Fatal(err)
		}
		if first.Name != name || second.Name != name+" (1)" {
			t.Errorf("%s 的重名序号 = %q, %q", name, first.Name, second.Name)
		}
	}
}

func TestSaveInboundEnforcesSizeLimit(t *testing.T) {
	_, ws := openTestWorkspace(t)
	ctx := context.Background()
	_, err := ws.SaveInbound(ctx, "big.bin", bytes.NewReader(make([]byte, 11)), 10)
	if model.CodeOf(err) != model.CodeInvalid {
		t.Fatalf("超限应返回 invalid: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(ws.Root(), InboundDir))
	if len(entries) != 0 {
		t.Errorf("超限后不应留下任何文件: %d 个", len(entries))
	}
	if _, err := ws.SaveInbound(ctx, "ok.bin", bytes.NewReader(make([]byte, 10)), 10); err != nil {
		t.Fatalf("刚好等于上限应成功: %v", err)
	}
}

func TestSaveInboundMIMEFallbacks(t *testing.T) {
	_, ws := openTestWorkspace(t)
	ctx := context.Background()
	att, err := ws.SaveInbound(ctx, "notes", strings.NewReader("plain text content\n"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if att.MIME != "text/plain" {
		t.Errorf("无扩展名的文本应嗅探为 text/plain（不带 charset），实际 %q", att.MIME)
	}
	att, err = ws.SaveInbound(ctx, "empty", strings.NewReader(""), 0)
	if err != nil {
		t.Fatal(err)
	}
	if att.MIME != octetStream || att.Size != 0 {
		t.Errorf("空文件应为 %s / 0 字节，实际 %q / %d", octetStream, att.MIME, att.Size)
	}
}

func TestSanitizeFilename(t *testing.T) {
	long := strings.Repeat("长", 100) + ".txt"
	cases := []struct{ in, want string }{
		{"report.pdf", "report.pdf"},
		{"  spaced name.txt  ", "spaced name.txt"},
		{"/etc/passwd", "passwd"},
		{`C:\Users\me\doc.docx`, "doc.docx"},
		{"a/b/../c.txt", "c.txt"},
		{"..", DefaultAttachmentName},
		{".", DefaultAttachmentName},
		{"", DefaultAttachmentName},
		{"\x00\x01\x1f\x7f", DefaultAttachmentName},
		{"tab\tname\n.txt", "tabname.txt"},
		{`q<u>o:t"e|s?*.md`, "q_u_o_t_e_s__.md"},
		{"trailing...", "trailing"},
		{"中文 文件名.pdf", "中文 文件名.pdf"},
		{"CON", "_CON"},
		{"con.txt", "_con.txt"},
		{"lpt9.log", "_lpt9.log"},
		{"console.txt", "console.txt"},
		{"bad\xffbytes.txt", "badbytes.txt"},
	}
	for _, c := range cases {
		if got := SanitizeFilename(c.in); got != c.want {
			t.Errorf("SanitizeFilename(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
	got := SanitizeFilename(long)
	if len(got) > maxFilenameBytes || !strings.HasSuffix(got, ".txt") || !strings.HasPrefix(got, "长") {
		t.Errorf("超长名字应在字符边界截断并保留扩展名: %q (%d 字节)", got, len(got))
	}
	for i := 0; i < len(got); {
		r := []rune(got[i:])[0]
		if r == '\uFFFD' {
			t.Fatalf("截断破坏了 UTF-8 字符: %q", got)
		}
		i += len(string(r))
	}
}
