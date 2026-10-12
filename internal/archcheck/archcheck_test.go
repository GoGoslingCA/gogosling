// Package archcheck 用测试强制执行 docs/architecture.md §4 的依赖规则。
//
// 它用 go/parser 读取 cmd/ 与 internal/ 下每个 Go 文件的 import 声明，
// 对照下面的允许矩阵；任何越层导入都会让 go test ./... 失败。
// 规则变更先改架构文档，再改这里。
package archcheck

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const modulePath = "github.com/GoGoslingCA/gogosling/"

// leaf 是只允许依赖标准库与第三方库的包。
var leaf = []string{
	"internal/model", "internal/config", "internal/version", "internal/event",
	"internal/i18n", "internal/observability", "internal/secret",
}

// allowed 列出每个非叶子包除叶子包之外还可以导入的 internal 包。
// 未出现在 leaf 与 allowed 中的包只能导入叶子包。
var allowed = map[string][]string{
	"internal/store":        {},
	"internal/identity":     {},
	"internal/sandbox":      {},
	"internal/engine":       {},
	"internal/permission":   {},
	"internal/workspace":    {},
	"internal/provider":     {},
	"internal/agent":        {},
	"internal/protocol/mcp": {},
	"internal/protocol/acp": {},
	"internal/update":       {},
	"internal/extension":    {"internal/protocol/mcp"},
	"internal/tools":        {"internal/permission", "internal/workspace", "internal/sandbox", "internal/extension"},
	"internal/session":      {"internal/agent", "internal/provider", "internal/engine", "internal/permission"},
	"internal/scheduler":    {"internal/session"},
	"internal/gateway":      {"internal/session"},
	// 传输层可以使用任何领域包，但不能触碰组合根。
	"internal/server": {"*domain"},
	"internal/client": {"*domain"},
}

// composition 是组合根，可以导入任何包。
var composition = []string{"internal/app", "cmd/"}

func TestDependencyDirection(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	var violations []string
	for _, dir := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			pkg := filepath.ToSlash(filepath.Dir(rel))
			for _, imp := range importsOf(t, path) {
				if !strings.HasPrefix(imp, modulePath) {
					continue
				}
				target := strings.TrimPrefix(imp, modulePath)
				if !importAllowed(pkg, target) {
					violations = append(violations, filepath.ToSlash(rel)+" 导入了 "+target)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	sort.Strings(violations)
	for _, v := range violations {
		t.Error("违反依赖规则: " + v)
	}
}

func importsOf(t *testing.T, path string) []string {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := parser.ParseFile(token.NewFileSet(), path, src, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("解析 %s 失败: %v", path, err)
	}
	out := make([]string, 0, len(f.Imports))
	for _, spec := range f.Imports {
		out = append(out, strings.Trim(spec.Path.Value, `"`))
	}
	return out
}

func importAllowed(pkg, target string) bool {
	// 同一包内（含测试文件）与子包测试辅助不受限制。
	if pkg == target || strings.HasPrefix(target, pkg+"/") || strings.HasPrefix(pkg, target+"/") {
		return true
	}
	// 本包自身只是测试，不参与规则。
	if strings.HasPrefix(pkg, "internal/archcheck") {
		return true
	}
	for _, c := range composition {
		if strings.HasPrefix(pkg, c) {
			return true
		}
	}
	if isLeaf(pkg) {
		return false
	}
	if isLeaf(target) {
		return true
	}
	extra, known := allowed[packageKey(pkg)]
	if !known {
		return false
	}
	for _, e := range extra {
		if e == "*domain" {
			// 传输层：除组合根、传输层与基础层 store 之外都允许；
			// 只有测试装配子包 internal/server/servertest 可以直接打开 store。
			if strings.HasPrefix(target, "internal/store") {
				if strings.HasPrefix(pkg, "internal/server/servertest") {
					return true
				}
				continue
			}
			if !strings.HasPrefix(target, "internal/app") && !strings.HasPrefix(target, "cmd/") &&
				!strings.HasPrefix(target, "internal/server") && !strings.HasPrefix(target, "internal/client") {
				return true
			}
			continue
		}
		if target == e || strings.HasPrefix(target, e+"/") {
			return true
		}
	}
	return false
}

func isLeaf(pkg string) bool {
	for _, l := range leaf {
		if pkg == l || strings.HasPrefix(pkg, l+"/") {
			return true
		}
	}
	return false
}

// packageKey 把子包（例如 internal/store/migrations）折叠到规则表中的顶层包。
func packageKey(pkg string) string {
	best := ""
	for k := range allowed {
		if (pkg == k || strings.HasPrefix(pkg, k+"/")) && len(k) > len(best) {
			best = k
		}
	}
	return best
}
