package permission

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

func TestWildcardMatch(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"", "anything", true},
		{"*", "mcp:ext/tool", true},
		{"read_*", "read_file", true},
		{"read_*", "write_file", false},
		{"*_file", "read_file", true},
		{"re?d_file", "read_file", true},
		{"mcp:*", "mcp:ext/tool", true},
		{"mcp:*/tool", "mcp:ext/tool", true},
		{"exact", "exact", true},
		{"exact", "exactly", false},
		{`\*`, "*", true},
		{`\*`, "x", false},
		{`a\?b`, "a?b", true},
		{`a\?b`, "axb", false},
		{`*\*`, "glob*", true},
		{escapeWildcard("we?rd*name"), "we?rd*name", true},
		{escapeWildcard("we?rd*name"), "weird-name", false},
	}
	for _, c := range cases {
		if got := wildcardMatch(c.pattern, c.name); got != c.want {
			t.Errorf("wildcardMatch(%q, %q) = %v，期望 %v", c.pattern, c.name, got, c.want)
		}
	}
}

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"**/.ssh/**", "/home/u/.ssh/id_rsa", true},
		{"**/.ssh/**", "/home/u/.ssh", true},
		{"**/.ssh/**", "/home/u/.sshx/a", false},
		{"**/.ssh/**", "C:/Users/u/.ssh/id_rsa", true},
		{"**/.env", "/w/.env", true},
		{"**/.env", "/w/.env.local", false},
		{"**/.env.*", "/w/.env.local", true},
		{"src/**/*.go", "src/a/b/c.go", true},
		{"src/**/*.go", "src/c.go", true},
		{"src/**/*.go", "lib/c.go", false},
		{"/abs/*", "/abs/x", true},
		{"/abs/*", "/abs/x/y", false},
		{"/ws/**", "/ws", true},
		{"/ws/**", "/ws/a/b", true},
		{"/ws/**", "/ws2/a", false},
		{"*.md", "README.md", true},
		{"*.md", "docs/README.md", false},
	}
	for _, c := range cases {
		if got := matchGlob(c.pattern, c.name); got != c.want {
			t.Errorf("matchGlob(%q, %q) = %v，期望 %v", c.pattern, c.name, got, c.want)
		}
	}
}

func TestExpandHome(t *testing.T) {
	home := filepath.Join("home", "u")
	if got := expandHome("~/.ssh/**", home); got != filepath.ToSlash(home)+"/.ssh/**" {
		t.Errorf("~/ 未展开: %q", got)
	}
	if got := expandHome("~", home); got != filepath.ToSlash(home) {
		t.Errorf("~ 未展开: %q", got)
	}
	if got := expandHome("~/x", ""); got != "~/x" {
		t.Errorf("没有主目录时应原样返回: %q", got)
	}
	if got := expandHome("/abs", home); got != "/abs" {
		t.Errorf("绝对路径不应改变: %q", got)
	}
}

func TestCompileRulesValidation(t *testing.T) {
	bad := []Rule{
		{Tool: "*", Effect: "maybe"},
		{Tool: "*", Risk: "huge", Effect: EffectAllow},
		{Tool: "*", Command: "(", Effect: EffectDeny},
		{Tool: "*", Path: "[", Effect: EffectDeny},
		{Effect: EffectAllow},
	}
	for i, r := range bad {
		_, err := compileRules([]Rule{r}, "")
		if !model.IsCode(err, model.CodeInvalid) {
			t.Errorf("规则 %d 应返回 invalid，得到 %v", i, err)
		}
	}
	good := []Rule{
		{Tool: "*", Effect: EffectAllow},
		{Tool: "x", Path: "**/.env", Effect: EffectAsk},
		{Tool: "", Command: "^ls", Effect: EffectAllow},
		{Tool: "", Risk: model.RiskNetwork, Effect: EffectAsk},
	}
	if _, err := compileRules(good, ""); err != nil {
		t.Fatalf("合法规则不应报错: %v", err)
	}
}

func TestDefaultRulesCompile(t *testing.T) {
	rules, err := DefaultRules()
	if err != nil || len(rules) == 0 {
		t.Fatalf("内置规则应可解析且非空: %d %v", len(rules), err)
	}
	if _, err := compileRules(rules, "/home/u"); err != nil {
		t.Fatalf("内置规则编译失败: %v", err)
	}
	seen := map[string]bool{}
	for _, r := range rules {
		if r.ID == "" {
			t.Errorf("内置规则必须有 ID: %+v", r)
		}
		if seen[r.ID] {
			t.Errorf("内置规则 ID 重复: %s", r.ID)
		}
		seen[r.ID] = true
		if r.Origin != "builtin" {
			t.Errorf("内置规则 %s 的 origin 应为 builtin", r.ID)
		}
	}
}

func TestRuleFileSeedAndRoundtrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "security", "rules.json")
	created, err := ensureRuleFile(p)
	if err != nil || !created {
		t.Fatalf("首次应创建规则文件: created=%v err=%v", created, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != ruleFilePerm {
			t.Errorf("规则文件权限 = %o，期望 %o", info.Mode().Perm(), ruleFilePerm)
		}
	}
	created, err = ensureRuleFile(p)
	if err != nil || created {
		t.Fatalf("已存在时不应重建: created=%v err=%v", created, err)
	}

	rf, err := loadRuleFile(p)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	defaults, _ := DefaultRules()
	if rf.Version != ruleFileVersion || len(rf.Rules) != len(defaults) {
		t.Fatalf("落盘的默认规则与内置不一致: version=%d n=%d", rf.Version, len(rf.Rules))
	}

	rf.Rules = append(rf.Rules, Rule{ID: "custom", Tool: "x", Effect: EffectDeny, Origin: "user"})
	if err := writeRuleFile(p, rf); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	again, err := loadRuleFile(p)
	if err != nil {
		t.Fatalf("重新加载失败: %v", err)
	}
	last := again.Rules[len(again.Rules)-1]
	if last.ID != "custom" || last.Effect != EffectDeny || last.Origin != "user" {
		t.Errorf("往返后的规则不一致: %+v", last)
	}
	entries, _ := os.ReadDir(filepath.Dir(p))
	for _, e := range entries {
		if e.Name() != "rules.json" {
			t.Errorf("不应留下临时文件: %s", e.Name())
		}
	}
}

func TestLoadRuleFileRejectsGarbageAndFutureVersion(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "rules.json")
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadRuleFile(p); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("非法 JSON 应返回 invalid: %v", err)
	}
	if err := os.WriteFile(p, []byte(`{"version": 99, "rules": []}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadRuleFile(p); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("未来版本应返回 invalid: %v", err)
	}
}

func TestRuleMatching(t *testing.T) {
	rs, err := compileRules([]Rule{
		{ID: "deny-ssh", Tool: "*", Path: "**/.ssh/**", Effect: EffectDeny},
		{ID: "allow-ls", Tool: "run_*", Risk: model.RiskExecute, Command: `^ls\b`, Effect: EffectAllow},
		{ID: "ask-net", Tool: "*", Risk: model.RiskNetwork, Effect: EffectAsk},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(t.TempDir(), "ws")
	sshFacts := extractFacts([]byte(`{"path": "/home/u/.ssh/id_rsa"}`), nil, ws, "")
	if r := rs.firstMatch(EffectDeny, "read_file", model.RiskRead, sshFacts, false); r == nil || r.ID != "deny-ssh" {
		t.Error("路径规则应命中 .ssh")
	}
	relFacts := extractFacts([]byte(`{"path": ".ssh/config"}`), nil, ws, "")
	if r := rs.firstMatch(EffectDeny, "read_file", model.RiskRead, relFacts, false); r == nil {
		t.Error("工作区相对路径也应命中 .ssh 规则")
	}
	lsFacts := extractFacts([]byte(`{"command": "ls -la"}`), nil, ws, "")
	if r := rs.firstMatch(EffectAllow, "run_command", model.RiskExecute, lsFacts, false); r == nil {
		t.Error("命令规则应命中 ls")
	}
	if r := rs.firstMatch(EffectAllow, "run_command", model.RiskWrite, lsFacts, false); r != nil {
		t.Error("风险等级不同不应命中")
	}
	if r := rs.firstMatch(EffectAllow, "other", model.RiskExecute, lsFacts, false); r != nil {
		t.Error("工具名不匹配不应命中")
	}
	noCmd := extractFacts(nil, nil, ws, "")
	if r := rs.firstMatch(EffectAllow, "run_command", model.RiskExecute, noCmd, false); r != nil {
		t.Error("没有命令时命令规则不应命中")
	}
	if r := rs.firstMatch(EffectAsk, "fetch", model.RiskNetwork, noCmd, false); r == nil || r.ID != "ask-net" {
		t.Error("只按风险的规则应命中")
	}
}
