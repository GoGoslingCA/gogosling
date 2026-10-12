package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

func TestIsInboundName(t *testing.T) {
	cases := []struct {
		name           string
		fold, trimDots bool
		want           bool
	}{
		{"inbound", false, false, true},
		{"Inbound", false, false, false},
		{"Inbound", true, false, true},
		{"INBOUND", true, true, true},
		{"inbound.", true, false, false},
		{"inbound.", true, true, true},
		{"Inbound .", true, true, true},
		{"inbound2", true, true, false},
		{filepath.Join("inbound", "a.bin"), true, true, false},
	}
	for _, c := range cases {
		if got := isInboundName(c.name, c.fold, c.trimDots); got != c.want {
			t.Errorf("isInboundName(%q, fold=%v, trimDots=%v) = %v，期望 %v", c.name, c.fold, c.trimDots, got, c.want)
		}
	}
}

// XP-15：大小写不同的写法不能删掉 inbound/。在区分大小写的文件系统上 "Inbound" 是另一个（不存在的）目录，
// 无论哪种平台，真正的 inbound/ 与其中的附件都必须保留。
func TestRemoveCaseVariantKeepsInbound(t *testing.T) {
	_, ws := openTestWorkspace(t)
	ctx := context.Background()
	if err := ws.WriteFile(ctx, InboundDir+"/a.bin", []byte("x")); err != nil {
		t.Fatal(err)
	}
	for _, variant := range []string{"Inbound", "INBOUND", "Inbound/"} {
		err := ws.Remove(ctx, variant)
		if caseInsensitiveFS && !model.IsCode(err, model.CodeForbidden) {
			t.Errorf("不区分大小写的文件系统上删除 %q 应为 forbidden，得到 %v", variant, err)
		}
		if _, statErr := os.Stat(filepath.Join(ws.Root(), InboundDir, "a.bin")); statErr != nil {
			t.Fatalf("删除 %q 后 inbound/ 中的附件应保留: %v", variant, statErr)
		}
	}
}

// XP-15：即使名字比较放过了某种写法，删除前与真实 inbound/ 的同一文件比较也会拦住它。
func TestRemoveRefusesSameDirectoryAsInbound(t *testing.T) {
	_, ws := openTestWorkspace(t)
	fi, err := os.Lstat(filepath.Join(ws.Root(), InboundDir))
	if err != nil {
		t.Fatal(err)
	}
	if !ws.isProtectedDir(fi) {
		t.Fatal("inbound/ 本身应被识别为受保护目录")
	}
	if err := ws.WriteFile(context.Background(), "other/x.txt", []byte("x")); err != nil {
		t.Fatal(err)
	}
	oi, err := os.Lstat(filepath.Join(ws.Root(), "other"))
	if err != nil {
		t.Fatal(err)
	}
	if ws.isProtectedDir(oi) {
		t.Fatal("普通目录不应被视为受保护目录")
	}
}
