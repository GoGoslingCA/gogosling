package permission

import (
	"math/rand/v2"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

// legacyMatchSegments 保留旧匹配器，仅用于差分测试与同输入基准。
func legacyMatchSegments(p, n []string) bool {
	for len(p) > 0 {
		if p[0] == "**" {
			for len(p) > 0 && p[0] == "**" {
				p = p[1:]
			}
			if len(p) == 0 {
				return true
			}
			for i := 0; i <= len(n); i++ {
				if legacyMatchSegments(p, n[i:]) {
					return true
				}
			}
			return false
		}
		if len(n) == 0 {
			return false
		}
		ok, err := path.Match(p[0], n[0])
		if err != nil || !ok {
			return false
		}
		p, n = p[1:], n[1:]
	}
	return len(n) == 0
}

func Test路径通配迭代匹配与旧规则一致(t *testing.T) {
	patterns := []string{"", "a", "b", "**", "*", "?", "a*", "[ab]", "[^a]", `\*`, "中文", "[", `\`}
	names := []string{"", "a", "b", "abc", "*", "中文", "é", "["}
	random := rand.New(rand.NewPCG(27, 43))
	for range 30000 {
		pattern := make([]string, random.IntN(10))
		name := make([]string, random.IntN(12))
		for i := range pattern {
			pattern[i] = patterns[random.IntN(len(patterns))]
		}
		for i := range name {
			name[i] = names[random.IntN(len(names))]
		}
		want := legacyMatchSegments(pattern, name)
		if got := matchSegments(pattern, name); got != want {
			t.Fatalf("模式 %q 匹配 %q 应与旧逻辑一致：期望 %v，得到 %v", pattern, name, want, got)
		}
	}
}

func Test路径通配长路径与连续双星保持匹配边界(t *testing.T) {
	pattern := strings.Repeat("**/a/", 100) + "**/secret"
	name := strings.Repeat("a/", 10000) + "public"
	if matchGlob(pattern, name) {
		t.Fatal("长路径结尾不匹配时不得放宽规则")
	}
	if !matchGlob(pattern, strings.TrimSuffix(name, "public")+"secret") {
		t.Fatal("长路径结尾匹配时应保留既有规则")
	}
	for _, tc := range []struct {
		pattern, name string
		want          bool
	}{
		{"**/**/**", "", true},
		{"**/a/**/b/**", "a/b", true},
		{"**/a/**/b", "a/x/a/b/c", false},
		{"/root/**/**/file", "/root/file", true},
		{"/root/**/file", "/root/file/", false},
		{`**/\*/secret`, "a/*/secret", true},
		{`**/\*/secret`, "a/x/secret", false},
	} {
		if got := matchGlob(tc.pattern, tc.name); got != tc.want {
			t.Errorf("%q 匹配 %q 应为 %v，得到 %v", tc.pattern, tc.name, tc.want, got)
		}
	}
}

func Test路径通配短输入穷举与大小写折叠不变(t *testing.T) {
	combinations := func(choices []string, depth int) [][]string {
		out := [][]string{nil}
		frontier := out
		for range depth {
			var next [][]string
			for _, prefix := range frontier {
				for _, choice := range choices {
					next = append(next, append(append([]string(nil), prefix...), choice))
				}
			}
			out = append(out, next...)
			frontier = next
		}
		return out
	}
	patterns := combinations([]string{"", "a", "**", "*", "?", "[a-z]", "[", `\*`}, 3)
	names := combinations([]string{"", "a", "b", "*"}, 4)
	for _, pattern := range patterns {
		for _, name := range names {
			if got, want := matchSegments(pattern, name), legacyMatchSegments(pattern, name); got != want {
				t.Fatalf("穷举模式 %q 匹配 %q 应为 %v，得到 %v", pattern, name, want, got)
			}
		}
	}
	for _, pattern := range []string{"**/.SSH/**", "**/[A-Z]*/**/FILE?.TXT", "**/A/**/B/**", "**/[/x"} {
		for _, name := range []string{"/Home/u/.Ssh/key", "root/MIXED/深层/file1.txt", "a/x/A/b/B", "a/other"} {
			for _, fold := range []bool{false, true} {
				p, n := pattern, name
				if fold {
					p, n = strings.ToLower(p), strings.ToLower(n)
				}
				want := legacyMatchSegments(strings.Split(p, "/"), strings.Split(filepath.ToSlash(n), "/"))
				if got := matchGlobFold(pattern, name, fold); got != want {
					t.Errorf("模式 %q 路径 %q 折叠=%v 应为 %v，得到 %v", pattern, name, fold, want, got)
				}
			}
		}
	}
}

func BenchmarkPermissionPathSegments(b *testing.B) {
	for _, tc := range []struct{ name, pattern, path string }{
		{"common-deny", "**/.ssh/**", "/home/user/project/src/main.go"},
		{"common-match", "src/**/*.go", "src/internal/worker/main.go"},
		{"trailing-glob", "src/**", "src/internal/worker/main.go"},
		{"repeated-glob-miss", strings.Repeat("**/a/", 6) + "z", strings.Repeat("a/", 20) + "b"},
	} {
		pattern, name := strings.Split(tc.pattern, "/"), strings.Split(tc.path, "/")
		for _, impl := range []struct {
			name  string
			match func([]string, []string) bool
		}{{"before", legacyMatchSegments}, {"after", matchSegments}} {
			b.Run(tc.name+"/"+impl.name, func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					_ = impl.match(pattern, name)
				}
			})
		}
	}
}
