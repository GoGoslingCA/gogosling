package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

func TestReadFile(t *testing.T) {
	ws := newFakeWorkspace(t)
	rc := testRunContext(t, ws, nil)
	mustWrite(t, ws, "docs/a.txt", "line1\nline2\nline3\nline4\n")
	tool := NewReadFile()

	res, err := tool.Execute(context.Background(), rc, rawArgs(t, map[string]any{"path": "docs/a.txt"}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "line1\nline2\nline3\nline4\n" || res.IsError {
		t.Fatalf("应原样返回内容: %+v", res)
	}

	res, err = tool.Execute(context.Background(), rc, rawArgs(t, map[string]any{"path": "docs/a.txt", "start_line": 2, "max_lines": 2}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "[第 2-3 行，共 4 行]\nline2\nline3\n" {
		t.Fatalf("分段读取结果不正确: %q", res.Content)
	}
	res, _ = tool.Execute(context.Background(), rc, rawArgs(t, map[string]any{"path": "docs/a.txt", "start_line": 9}))
	if !strings.Contains(res.Content, "超出范围") {
		t.Fatalf("越界起始行应给出说明: %q", res.Content)
	}

	if _, err := tool.Execute(context.Background(), rc, rawArgs(t, map[string]any{"path": "missing.txt"})); !model.IsCode(err, model.CodeNotFound) {
		t.Fatalf("不存在应为 CodeNotFound，得到 %v", err)
	}
	if _, err := tool.Execute(context.Background(), rc, rawArgs(t, map[string]any{"path": "../etc/passwd"})); !model.IsCode(err, model.CodeForbidden) {
		t.Fatalf("越界应为 CodeForbidden，得到 %v", err)
	}
	if _, err := tool.Execute(context.Background(), rc, rawArgs(t, map[string]any{"path": "docs"})); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("读目录应为 CodeInvalid，得到 %v", err)
	}

	mustWrite(t, ws, "bin.dat", "ab\x00cd")
	if _, err := tool.Execute(context.Background(), rc, rawArgs(t, map[string]any{"path": "bin.dat"})); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("二进制文件应为 CodeInvalid，得到 %v", err)
	}
	mustWrite(t, ws, "latin1.txt", "caf\xe9\n")
	res, err = tool.Execute(context.Background(), rc, rawArgs(t, map[string]any{"path": "latin1.txt"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "不是合法的 UTF-8") || !strings.Contains(res.Content, "caf\uFFFD") {
		t.Fatalf("非 UTF-8 应替换并说明: %q", res.Content)
	}

	d, err := tool.(Describer).Describe(rawArgs(t, map[string]any{"path": "docs/a.txt"}))
	if err != nil || d.Summary != "读取文件 docs/a.txt" || d.Details["path"] != "docs/a.txt" {
		t.Fatalf("Describe 不正确: %+v %v", d, err)
	}
	if _, err := tool.(Describer).Describe(rawArgs(t, map[string]any{"path": "x", "start_line": -1})); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("负数行号应为 CodeInvalid，得到 %v", err)
	}
}

func TestWriteFile(t *testing.T) {
	ws := newFakeWorkspace(t)
	rc := testRunContext(t, ws, nil)
	tool := NewWriteFile()

	res, err := tool.Execute(context.Background(), rc, rawArgs(t, map[string]any{"path": "new/dir/file.txt", "content": "hello"}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(res.Content, "5 字节") {
		t.Fatalf("写入结果不正确: %+v", res)
	}
	data, _ := os.ReadFile(filepath.Join(ws.Root(), "new", "dir", "file.txt"))
	if string(data) != "hello" {
		t.Fatalf("文件内容应为 hello，得到 %q", data)
	}

	res, err = tool.Execute(context.Background(), rc, rawArgs(t, map[string]any{"path": "new/dir/file.txt", "content": " world", "append": true}))
	if err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(ws.Root(), "new", "dir", "file.txt"))
	if string(data) != "hello world" || !strings.Contains(res.Content, "追加") {
		t.Fatalf("追加不正确: %q %+v", data, res)
	}
	if _, err := tool.Execute(context.Background(), rc, rawArgs(t, map[string]any{"path": "fresh.txt", "content": "x", "append": true})); err != nil {
		t.Fatalf("追加到不存在的文件应创建它: %v", err)
	}

	for name, args := range map[string]map[string]any{
		"空路径":  {"path": "", "content": "x"},
		"越界":   {"path": "../x", "content": "x"},
		"缺内容":  {"path": "a.txt"},
		"写到目录": {"path": "new", "content": "x"},
	} {
		_, err := tool.Execute(context.Background(), rc, rawArgs(t, args))
		if err == nil {
			t.Errorf("%s 应失败", name)
		}
	}
	if _, err := tool.Execute(context.Background(), rc, rawArgs(t, map[string]any{"path": "empty.txt", "content": ""})); err != nil {
		t.Fatalf("显式空字符串应允许写空文件: %v", err)
	}
	big := strings.Repeat("x", maxWriteBytes+1)
	if _, err := tool.Execute(context.Background(), rc, rawArgs(t, map[string]any{"path": "big.txt", "content": big})); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("超大内容应为 CodeInvalid，得到 %v", err)
	}

	d, err := tool.(Describer).Describe(rawArgs(t, map[string]any{"path": "a.txt", "content": "abc", "append": true}))
	if err != nil || !strings.Contains(d.Summary, "追加到文件 a.txt") || d.Details["bytes"] != 3 || d.Details["append"] != true {
		t.Fatalf("Describe 不正确: %+v %v", d, err)
	}
}

func TestListDir(t *testing.T) {
	ws := newFakeWorkspace(t)
	rc := testRunContext(t, ws, nil)
	mustWrite(t, ws, "b.txt", "bb")
	mustWrite(t, ws, "sub/c.txt", "c")
	mustWrite(t, ws, "a.txt", "a")
	tool := NewListDir()

	res, err := tool.Execute(context.Background(), rc, rawArgs(t, map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	want := "目录 .（3 项）\nsub/\na.txt\t1\nb.txt\t2\n"
	if res.Content != want {
		t.Fatalf("列表输出不正确:\n%q\n期望\n%q", res.Content, want)
	}
	res, err = tool.Execute(context.Background(), rc, rawArgs(t, map[string]any{"path": "sub"}))
	if err != nil || !strings.Contains(res.Content, "c.txt\t1") {
		t.Fatalf("子目录列表不正确: %+v %v", res, err)
	}
	if _, err := tool.Execute(context.Background(), rc, rawArgs(t, map[string]any{"path": "a.txt"})); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("列文件应为 CodeInvalid，得到 %v", err)
	}
	if _, err := tool.Execute(context.Background(), rc, rawArgs(t, map[string]any{"path": "nope"})); !model.IsCode(err, model.CodeNotFound) {
		t.Fatalf("不存在应为 CodeNotFound，得到 %v", err)
	}
	d, _ := tool.(Describer).Describe(rawArgs(t, map[string]any{"path": "sub"}))
	if d.Summary != "列出目录 sub" {
		t.Fatalf("Describe 不正确: %+v", d)
	}
}

func TestSearchFiles(t *testing.T) {
	ws := newFakeWorkspace(t)
	rc := testRunContext(t, ws, nil)
	mustWrite(t, ws, "main.go", "package main\n\nfunc main() { Hello() }\n")
	mustWrite(t, ws, "pkg/util.go", "package pkg\n// hello helper\nfunc Hello() {}\n")
	mustWrite(t, ws, "pkg/README.md", "# Hello\n")
	mustWrite(t, ws, "node_modules/x/index.js", "hello from deps\n")
	mustWrite(t, ws, "bin.dat", "hello\x00binary")
	tool := NewSearchFiles()
	run := func(args map[string]any) model.ToolResult {
		t.Helper()
		res, err := tool.Execute(context.Background(), rc, rawArgs(t, args))
		if err != nil {
			t.Fatalf("args=%v: %v", args, err)
		}
		return res
	}

	res := run(map[string]any{"glob": "*.go"})
	if !strings.Contains(res.Content, "main.go\n") || !strings.Contains(res.Content, "pkg/util.go\n") || strings.Contains(res.Content, "README") {
		t.Fatalf("glob 结果不正确:\n%s", res.Content)
	}
	res = run(map[string]any{"glob": "pkg/*"})
	if strings.Contains(res.Content, "main.go") || !strings.Contains(res.Content, "pkg/README.md") {
		t.Fatalf("带目录的 glob 应匹配完整路径:\n%s", res.Content)
	}

	res = run(map[string]any{"query": "hello"})
	for _, want := range []string{"main.go:3: func main() { Hello() }", "pkg/util.go:2: // hello helper", "pkg/util.go:3: func Hello() {}", "pkg/README.md:1: # Hello"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("内容搜索缺少 %q:\n%s", want, res.Content)
		}
	}
	if strings.Contains(res.Content, "node_modules") || strings.Contains(res.Content, "bin.dat") {
		t.Fatalf("应跳过依赖目录与二进制文件:\n%s", res.Content)
	}

	res = run(map[string]any{"query": "hello", "case_sensitive": true})
	if strings.Contains(res.Content, "Hello()") || !strings.Contains(res.Content, "hello helper") {
		t.Fatalf("区分大小写搜索不正确:\n%s", res.Content)
	}
	res = run(map[string]any{"query": "hello", "glob": "*.md"})
	if !strings.HasPrefix(res.Content, "找到 1 条结果") {
		t.Fatalf("glob 与 query 应同时生效:\n%s", res.Content)
	}
	res = run(map[string]any{"query": "hello", "max_results": 2})
	if !strings.HasPrefix(res.Content, "找到 2 条结果") || !strings.Contains(res.Content, "已达到上限 2 条") {
		t.Fatalf("max_results 应限制结果并提示:\n%s", res.Content)
	}
	res = run(map[string]any{"query": "nothing-matches-here"})
	if !strings.HasPrefix(res.Content, "找到 0 条结果") {
		t.Fatalf("无结果时应说明:\n%s", res.Content)
	}
	res = run(map[string]any{"glob": "*.go", "path": "pkg"})
	if strings.Contains(res.Content, "main.go") || !strings.Contains(res.Content, "pkg/util.go") {
		t.Fatalf("path 应限定搜索起点:\n%s", res.Content)
	}

	for name, args := range map[string]map[string]any{
		"都为空":   {},
		"非法正则":  {"query": "("},
		"非法通配":  {"glob": "[abc"},
		"起点不存在": {"glob": "*", "path": "missing"},
		"起点是文件": {"glob": "*", "path": "main.go"},
	} {
		if _, err := tool.Execute(context.Background(), rc, rawArgs(t, args)); err == nil {
			t.Errorf("%s 应失败", name)
		}
	}

	d, err := tool.(Describer).Describe(rawArgs(t, map[string]any{"glob": "*.go", "query": "x"}))
	if err != nil || !strings.Contains(d.Summary, "*.go") || d.Details["query"] != "x" || d.Details["path"] != "." {
		t.Fatalf("Describe 不正确: %+v %v", d, err)
	}
}

func TestSearchFilesSkipsSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 上创建符号链接需要额外权限")
	}
	ws := newFakeWorkspace(t)
	rc := testRunContext(t, ws, nil)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("hello secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(ws.Root(), "link.txt")); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, ws, "real.txt", "hello real\n")
	res, err := NewSearchFiles().Execute(context.Background(), rc, rawArgs(t, map[string]any{"query": "hello"}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Content, "secret") || !strings.Contains(res.Content, "real.txt:1") {
		t.Fatalf("符号链接不应被搜索:\n%s", res.Content)
	}
}

func TestGlobToRegexp(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		match   bool
	}{
		{"*.go", "main.go", true},
		{"*.go", "pkg/util.go", true},
		{"*.go", "main.gox", false},
		{"pkg/*.go", "pkg/util.go", true},
		{"pkg/*.go", "pkg/sub/x.go", false},
		{"pkg/**/*.go", "pkg/sub/x.go", true},
		{"pkg/**/*.go", "pkg/x.go", true},
		{"**/*.ts", "a/b/c.ts", true},
		{"**/*.ts", "c.ts", true},
		{"src/**", "src/a/b", true},
		{"file?.txt", "file1.txt", true},
		{"file?.txt", "file10.txt", false},
		{"[ab].txt", "a.txt", true},
		{"[!ab].txt", "a.txt", false},
		{"a.b", "axb", false},
		{"./*.md", "README.md", true},
		{`docs\*.md`, "docs/x.md", true},
	}
	for _, c := range cases {
		re, err := globToRegexp(c.pattern)
		if err != nil {
			t.Fatalf("%s: %v", c.pattern, err)
		}
		if got := re.MatchString(c.path); got != c.match {
			t.Errorf("glob %q 对 %q 应为 %v，得到 %v（正则 %s）", c.pattern, c.path, c.match, got, re)
		}
	}
	if _, err := globToRegexp("[abc"); err == nil {
		t.Fatal("未闭合的字符类应报错")
	}
}
