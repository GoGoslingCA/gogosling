package workspace

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

func openTestWorkspace(t *testing.T) (*Manager, *Workspace) {
	t.Helper()
	m := New(t.TempDir())
	ws, err := m.Open(context.Background(), "01AGENT", "")
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	return m, ws
}

func assertCode(t *testing.T, err error, code model.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("期望错误码 %s，实际没有错误", code)
	}
	if got := model.CodeOf(err); got != code {
		t.Fatalf("错误码 = %s，期望 %s（%v）", got, code, err)
	}
}

func TestOpenCreatesRootAndInbound(t *testing.T) {
	m, ws := openTestWorkspace(t)
	want := filepath.Join(m.AgentsDir(), "01AGENT")
	if ws.Root() != want {
		t.Fatalf("Root = %q，期望默认使用智能体标识 %q", ws.Root(), want)
	}
	for _, dir := range []string{ws.Root(), filepath.Join(ws.Root(), InboundDir)} {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			t.Fatalf("目录 %s 应已创建: %v", dir, err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
			t.Errorf("目录 %s 权限 = %o，期望 0700", dir, info.Mode().Perm())
		}
	}
	// 再次打开同一工作区应幂等。
	if _, err := m.Open(context.Background(), "01AGENT", "01AGENT"); err != nil {
		t.Fatalf("重复 Open 失败: %v", err)
	}
}

func TestOpenRelativeDirMustStayUnderAgents(t *testing.T) {
	m := New(t.TempDir())
	cases := []string{"../escape", "..", "a/../../b", `..\escape`, ".", "   "}
	if runtime.GOOS == "windows" {
		// Windows 上没有盘符的 "/x" 不是绝对路径，但也不允许当作相对目录。
		cases = append(cases, "/abs-looking", `\abs-looking`, "C:relative")
	}
	for _, dir := range cases {
		_, err := m.Open(context.Background(), "a", dir)
		if dir == "   " {
			// 空白等价于空：使用智能体标识，应当成功。
			if err != nil {
				t.Errorf("空白目录应回退到默认值，却失败: %v", err)
			}
			continue
		}
		assertCode(t, err, model.CodeInvalid)
	}
	ws, err := m.Open(context.Background(), "a", "nested/dir")
	if err != nil {
		t.Fatalf("嵌套相对目录应被接受: %v", err)
	}
	if ws.Root() != filepath.Join(m.AgentsDir(), "nested", "dir") {
		t.Errorf("Root = %q", ws.Root())
	}
}

func TestOpenAbsoluteDirUsedAsIs(t *testing.T) {
	m := New(t.TempDir())
	abs := filepath.Join(t.TempDir(), "custom")
	ws, err := m.Open(context.Background(), "a", abs)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	if ws.Root() != abs {
		t.Fatalf("Root = %q，期望原样使用绝对路径 %q", ws.Root(), abs)
	}
	root := string(filepath.Separator)
	if runtime.GOOS == "windows" {
		root = filepath.VolumeName(abs) + `\`
	}
	_, err = m.Open(context.Background(), "a", root)
	assertCode(t, err, model.CodeInvalid)
}

func TestOpenRejectsEmptyAgentAndCancelledContext(t *testing.T) {
	m := New(t.TempDir())
	_, err := m.Open(context.Background(), " ", "")
	assertCode(t, err, model.CodeInvalid)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = m.Open(ctx, "a", "")
	assertCode(t, err, model.CodeCancelled)
}

func TestResolveRejectsEscapes(t *testing.T) {
	_, ws := openTestWorkspace(t)
	forbidden := []string{
		"..", "../x", "a/../../x", "a/..", `..\x`, `a\..\..\x`,
		"/etc/passwd", `\windows`, "//server/share",
	}
	if runtime.GOOS == "windows" {
		forbidden = append(forbidden, `C:\Windows`, "C:relative")
	} else {
		forbidden = append(forbidden, "/tmp")
	}
	for _, rel := range forbidden {
		_, err := ws.Resolve(rel)
		if model.CodeOf(err) != model.CodeForbidden {
			t.Errorf("Resolve(%q) 应返回 forbidden，实际 %v", rel, err)
		}
	}
	_, err := ws.Resolve("a\x00b")
	assertCode(t, err, model.CodeInvalid)

	for _, rel := range []string{"", ".", "./", "a/./b/", "notes.md", "sub/dir/file"} {
		abs, err := ws.Resolve(rel)
		if err != nil {
			t.Errorf("Resolve(%q) 不应失败: %v", rel, err)
			continue
		}
		if !strings.HasPrefix(abs, ws.Root()) {
			t.Errorf("Resolve(%q) = %q 不在根目录下", rel, abs)
		}
	}
	if abs, _ := ws.Resolve("."); abs != ws.Root() {
		t.Errorf("Resolve(\".\") = %q，期望根目录", abs)
	}
}

func TestResolveRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 上创建符号链接需要额外权限")
	}
	_, ws := openTestWorkspace(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 目录链接指向外部。
	if err := os.Symlink(outside, filepath.Join(ws.Root(), "out")); err != nil {
		t.Fatal(err)
	}
	// 文件链接指向外部文件。
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(ws.Root(), "leak.txt")); err != nil {
		t.Fatal(err)
	}
	// 内部链接指向内部目录，应被允许。
	if err := os.Mkdir(filepath.Join(ws.Root(), "real"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(ws.Root(), "real"), filepath.Join(ws.Root(), "alias")); err != nil {
		t.Fatal(err)
	}
	// 悬空链接。
	if err := os.Symlink(filepath.Join(outside, "missing"), filepath.Join(ws.Root(), "dangling")); err != nil {
		t.Fatal(err)
	}

	for _, rel := range []string{"out", "out/secret.txt", "out/new/file", "leak.txt", "dangling", "dangling/x"} {
		_, err := ws.Resolve(rel)
		if model.CodeOf(err) != model.CodeForbidden {
			t.Errorf("Resolve(%q) 应因符号链接逃逸返回 forbidden，实际 %v", rel, err)
		}
	}
	for _, rel := range []string{"alias", "alias/new.txt", "real"} {
		if _, err := ws.Resolve(rel); err != nil {
			t.Errorf("Resolve(%q) 指向内部，不应失败: %v", rel, err)
		}
	}
	// 读写同样受阻。
	if _, err := ws.ReadFile(context.Background(), "leak.txt", 0); model.CodeOf(err) != model.CodeForbidden {
		t.Errorf("ReadFile 经链接读取外部文件应被拒绝: %v", err)
	}
	if err := ws.WriteFile(context.Background(), "out/x.txt", []byte("x")); model.CodeOf(err) != model.CodeForbidden {
		t.Errorf("WriteFile 经链接写入外部目录应被拒绝: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "x.txt")); err == nil {
		t.Error("外部目录不应出现被写入的文件")
	}
}

func TestOpenWorkspaceRootThroughSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 上创建符号链接需要额外权限")
	}
	// 工作区根目录本身是符号链接（例如 macOS 的 /var → /private/var）时，内部路径不应被误判为逃逸。
	target := filepath.Join(t.TempDir(), "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	ws, err := New(t.TempDir()).Open(context.Background(), "a", link)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	if err := ws.WriteFile(context.Background(), "a.txt", []byte("1")); err != nil {
		t.Fatalf("链接根目录下写文件失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "a.txt")); err != nil {
		t.Fatalf("文件应落在真实目录中: %v", err)
	}
}

func TestWriteFileAtomicAndReadBack(t *testing.T) {
	_, ws := openTestWorkspace(t)
	ctx := context.Background()
	if err := ws.WriteFile(ctx, "docs/notes.md", []byte("v1")); err != nil {
		t.Fatalf("WriteFile 失败: %v", err)
	}
	if err := ws.WriteFile(ctx, "docs/notes.md", []byte("v2")); err != nil {
		t.Fatalf("覆盖写失败: %v", err)
	}
	data, err := ws.ReadFile(ctx, "docs/notes.md", 1024)
	if err != nil || string(data) != "v2" {
		t.Fatalf("ReadFile = %q, %v，期望 v2", data, err)
	}
	entries, err := os.ReadDir(filepath.Join(ws.Root(), "docs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("写入完成后不应残留临时文件: %s", e.Name())
		}
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(filepath.Join(ws.Root(), "docs", "notes.md"))
		if info.Mode().Perm() != 0o600 {
			t.Errorf("文件权限 = %o，期望 0600", info.Mode().Perm())
		}
		info, _ = os.Stat(filepath.Join(ws.Root(), "docs"))
		if info.Mode().Perm() != 0o700 {
			t.Errorf("自动创建的父目录权限 = %o，期望 0700", info.Mode().Perm())
		}
	}

	// 根目录与目录不能作为文件写入。
	assertCode(t, ws.WriteFile(ctx, "", []byte("x")), model.CodeInvalid)
	assertCode(t, ws.WriteFile(ctx, "docs", []byte("x")), model.CodeInvalid)
	assertCode(t, ws.WriteFile(ctx, "../x", []byte("x")), model.CodeForbidden)
}

func TestReadFileLimits(t *testing.T) {
	_, ws := openTestWorkspace(t)
	ctx := context.Background()
	if err := ws.WriteFile(ctx, "big.bin", make([]byte, 100)); err != nil {
		t.Fatal(err)
	}
	_, err := ws.ReadFile(ctx, "big.bin", 99)
	assertCode(t, err, model.CodeInvalid)
	var e *model.Error
	if !asModelError(err, &e) || e.Details["max_bytes"] != int64(99) {
		t.Errorf("超限错误应带 max_bytes 细节: %+v", err)
	}
	if data, err := ws.ReadFile(ctx, "big.bin", 100); err != nil || len(data) != 100 {
		t.Errorf("刚好等于上限应可读取: %d, %v", len(data), err)
	}
	if data, err := ws.ReadFile(ctx, "big.bin", 0); err != nil || len(data) != 100 {
		t.Errorf("maxBytes 为 0 表示不限制: %d, %v", len(data), err)
	}
	_, err = ws.ReadFile(ctx, "missing.txt", 10)
	assertCode(t, err, model.CodeNotFound)
	// 把文件当目录穿过去，对调用方同样是"不存在"。
	_, err = ws.ReadFile(ctx, "big.bin/inner", 10)
	assertCode(t, err, model.CodeNotFound)
	_, err = ws.ReadFile(ctx, InboundDir, 10)
	assertCode(t, err, model.CodeInvalid)
}

func TestListAndStat(t *testing.T) {
	_, ws := openTestWorkspace(t)
	ctx := context.Background()
	for _, f := range []string{"b.txt", "a.txt", "sub/c.txt"} {
		if err := ws.WriteFile(ctx, f, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := ws.List(ctx, "")
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Path)
	}
	want := []string{InboundDir, "sub", "a.txt", "b.txt"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("List 顺序 = %v，期望目录在前再按名称 %v", got, want)
	}
	sub, err := ws.List(ctx, "sub/")
	if err != nil || len(sub) != 1 || sub[0].Path != "sub/c.txt" || sub[0].IsDir || sub[0].Size != 1 {
		t.Fatalf("子目录 List = %+v, %v", sub, err)
	}
	if _, err := ws.List(ctx, "a.txt"); model.CodeOf(err) != model.CodeInvalid {
		t.Errorf("对文件 List 应返回 invalid: %v", err)
	}
	if _, err := ws.List(ctx, "nope"); model.CodeOf(err) != model.CodeNotFound {
		t.Errorf("不存在的目录应返回 not_found: %v", err)
	}

	st, err := ws.Stat(ctx, "sub/c.txt")
	if err != nil || st.Path != "sub/c.txt" || st.Name != "c.txt" || st.Size != 1 || st.ModTime.IsZero() {
		t.Fatalf("Stat = %+v, %v", st, err)
	}
	root, err := ws.Stat(ctx, "")
	if err != nil || root.Path != "." || !root.IsDir {
		t.Fatalf("根目录 Stat = %+v, %v", root, err)
	}
	ok, err := ws.Exists(ctx, "sub/c.txt")
	if err != nil || !ok {
		t.Fatalf("Exists 应为 true: %v, %v", ok, err)
	}
	ok, err = ws.Exists(ctx, "sub/none")
	if err != nil || ok {
		t.Fatalf("Exists 应为 false: %v, %v", ok, err)
	}
}

func TestRemove(t *testing.T) {
	_, ws := openTestWorkspace(t)
	ctx := context.Background()
	assertCode(t, ws.Remove(ctx, ""), model.CodeForbidden)
	assertCode(t, ws.Remove(ctx, "."), model.CodeForbidden)
	assertCode(t, ws.Remove(ctx, InboundDir), model.CodeForbidden)
	assertCode(t, ws.Remove(ctx, InboundDir+"/"), model.CodeForbidden)
	assertCode(t, ws.Remove(ctx, "missing"), model.CodeNotFound)

	if err := ws.WriteFile(ctx, "dir/x.txt", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := ws.WriteFile(ctx, InboundDir+"/a.bin", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := ws.Remove(ctx, InboundDir+"/a.bin"); err != nil {
		t.Fatalf("删除附件文件应被允许: %v", err)
	}
	if err := ws.Remove(ctx, "dir"); err != nil {
		t.Fatalf("删除目录树失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws.Root(), "dir")); !os.IsNotExist(err) {
		t.Errorf("目录应已删除: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws.Root(), InboundDir)); err != nil {
		t.Errorf("inbound 目录应保留: %v", err)
	}
}

func TestOperationsRespectContext(t *testing.T) {
	_, ws := openTestWorkspace(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ws.ReadFile(ctx, "a", 1); model.CodeOf(err) != model.CodeCancelled {
		t.Errorf("ReadFile 应返回 cancelled: %v", err)
	}
	if err := ws.WriteFile(ctx, "a", nil); model.CodeOf(err) != model.CodeCancelled {
		t.Errorf("WriteFile 应返回 cancelled: %v", err)
	}
	if _, err := ws.List(ctx, ""); model.CodeOf(err) != model.CodeCancelled {
		t.Errorf("List 应返回 cancelled: %v", err)
	}
	if _, err := ws.SaveInbound(ctx, "a", strings.NewReader("x"), 0); model.CodeOf(err) != model.CodeCancelled {
		t.Errorf("SaveInbound 应返回 cancelled: %v", err)
	}
}

func asModelError(err error, target **model.Error) bool {
	for err != nil {
		if e, ok := err.(*model.Error); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
