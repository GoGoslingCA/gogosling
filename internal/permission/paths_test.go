package permission

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

func TestStripVerbatimPrefix(t *testing.T) {
	cases := []struct{ in, want string }{
		{`\\?\C:\Users\x\.gogosling\daemon.json`, `C:\Users\x\.gogosling\daemon.json`},
		{`\\.\C:\x`, `C:\x`},
		{`\??\C:\x`, `C:\x`},
		{`//?/C:/x`, `C:/x`},
		{`\\?\UNC\srv\share\x`, `\\srv\share\x`},
		{`\\?\unc\srv\share`, `\\srv\share`},
		// 设备名不是盘符路径：保持原样（按绝对路径处理，因而越界），不能被剥成相对路径。
		{`\\.\PhysicalDrive0`, `\\.\PhysicalDrive0`},
		{`\\.\pipe\x`, `\\.\pipe\x`},
		{`\\?\UNC\`, `\\?\UNC\`},
		{`C:\x`, `C:\x`},
		{`/home/u/x`, `/home/u/x`},
		{`ab`, `ab`},
	}
	for _, c := range cases {
		if got := stripVerbatimPrefix(c.in); got != c.want {
			t.Errorf("stripVerbatimPrefix(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

func TestMatchGlobFold(t *testing.T) {
	if matchGlobFold("**/.ssh/**", "proj/.SSH/id_rsa", false) {
		t.Error("不折叠大小写时 .SSH 不应命中 .ssh")
	}
	for _, name := range []string{"proj/.SSH/id_rsa", "proj/.Ssh/ID_RSA", "/Users/Me/.ssh/config"} {
		if !matchGlobFold("**/.ssh/**", name, true) {
			t.Errorf("折叠大小写时 %q 应命中 **/.ssh/**", name)
		}
	}
	if !matchGlobFold("/Users/me/[A-Z]*.TXT", "/users/ME/notes.txt", true) {
		t.Error("字符类与字面量都应按小写比较")
	}
}

// XP-03：路径规则的大小写。deny 规则在所有平台上都不区分大小写；allow / ask 只在不区分大小写的文件系统上折叠。
func TestRuleMatchingFoldsCase(t *testing.T) {
	rs, err := compileRules([]Rule{
		{ID: "deny-ssh", Path: "**/.ssh/**", Effect: EffectDeny},
		{ID: "ask-env", Path: "**/.env", Effect: EffectAsk},
		{ID: "allow-docs", Path: "/srv/Docs/**", Effect: EffectAllow},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(t.TempDir(), "ws")
	f := func(p string) facts { return extractFacts([]byte(`{"path":`+jsonString(p)+`}`), nil, ws, "") }

	for _, fold := range []bool{false, true} {
		if r := rs.firstMatch(EffectDeny, "read_file", model.RiskRead, f("proj/.SSH/id_rsa"), fold); r == nil || r.ID != "deny-ssh" {
			t.Errorf("foldFS=%v：deny 规则应不区分大小写地命中 .SSH，得到 %+v", fold, r)
		}
	}
	if r := rs.firstMatch(EffectAsk, "read_file", model.RiskRead, f("proj/.ENV"), false); r != nil {
		t.Errorf("区分大小写的文件系统上 .ENV 是另一个文件，ask 规则不应命中: %+v", r)
	}
	if r := rs.firstMatch(EffectAsk, "read_file", model.RiskRead, f("proj/.ENV"), true); r == nil || r.ID != "ask-env" {
		t.Errorf("不区分大小写的文件系统上 .ENV 就是 .env，ask 规则应命中: %+v", r)
	}
	if r := rs.firstMatch(EffectAllow, "read_file", model.RiskRead, f("/srv/docs/a.md"), false); r != nil {
		t.Errorf("区分大小写时 allow 规则不应扩大到 docs: %+v", r)
	}
}

// XP-03：经 Service.Check 的端到端行为——.SSH 变体在任何平台都被内置 deny 规则拒绝；
// 在不区分大小写的文件系统上 .ENV 也要走 ask 规则。
func TestCaseVariantsCannotBypassDefaultRules(t *testing.T) {
	env := newEnv(t, nil)
	ctx := context.Background()
	for _, p := range []string{"proj/.SSH/id_rsa", "proj/.Ssh/id_rsa", "proj/.ssh/ID_RSA"} {
		r := env.req("read_file", model.RiskRead, model.AutonomyAskRisky, map[string]any{"path": p})
		v, err := env.svc.Check(ctx, r)
		if err != nil || v.Allowed || v.RuleID != "deny-ssh-keys" {
			t.Errorf("%s 应被 deny-ssh-keys 拒绝: %+v %v", p, v, err)
		}
	}
	env.svc.foldPaths = true // 模拟 darwin / windows
	r := env.req("read_file", model.RiskRead, model.AutonomyAskRisky, map[string]any{"path": "proj/.ENV"})
	if d := env.svc.evaluate(r, extractFacts(r.Details, nil, env.workspace, "")); d.effect != EffectAsk || d.ruleID != "ask-env-files" {
		t.Errorf("不区分大小写时 .ENV 应命中 ask-env-files: %+v", d)
	}
}

// XP-04：数据目录防护对大小写变体、经符号链接的路径同样生效；工作区豁免只在不区分大小写的文件系统上折叠。
func TestGuardPathsCaseAndSymlinkVariants(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, "daemon.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(dataDir, "agents", "a1")
	if err := os.MkdirAll(ws, 0o700); err != nil {
		t.Fatal(err)
	}
	mk := func(p string) facts { return extractFacts([]byte(`{"path": `+jsonString(p)+`}`), nil, ws, "") }

	upper := strings.ToUpper(filepath.Join(dataDir, "daemon.json"))
	for _, fold := range []bool{false, true} {
		if hit := guardPaths(mk(upper), dataDir, ws, fold); hit == nil || hit.ID != "kernel-data-dir" {
			t.Errorf("foldFS=%v：大小写变体 %q 应被数据目录防护拒绝", fold, upper)
		}
	}

	sibling := filepath.Join(dataDir, "agents", "A1", "notes.md")
	// Windows 的 canonicalPath 会把已存在的 A1 还原成磁盘上的 a1（同一目录），"区分大小写"的分支在那里没有意义。
	if canonicalPath(sibling) == sibling {
		if hit := guardPaths(mk(sibling), dataDir, ws, false); hit == nil {
			t.Error("区分大小写的文件系统上 agents/A1 是另一个智能体的工作区，应被拒绝")
		}
	}
	if hit := guardPaths(mk(sibling), dataDir, ws, true); hit != nil {
		t.Errorf("不区分大小写的文件系统上 agents/A1 就是本工作区，不应拦截: %+v", hit)
	}

	// 工作区外的符号链接指向数据目录：按真实路径判定。
	outside := t.TempDir()
	link := filepath.Join(outside, "link")
	if err := os.Symlink(dataDir, link); err != nil {
		t.Skipf("当前环境不能创建符号链接: %v", err)
	}
	if hit := guardPaths(mk(filepath.Join(link, "daemon.json")), dataDir, ws, caseInsensitivePaths); hit == nil {
		t.Error("经符号链接指向数据目录的路径应被拒绝")
	}
	// 数据目录本身以符号链接形式配置、调用方给出真实路径。
	real, err := filepath.EvalSymlinks(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if hit := guardPaths(mk(filepath.Join(real, "daemon.json")), link, "", caseInsensitivePaths); hit == nil {
		t.Error("数据目录经符号链接配置时，真实路径同样应被拒绝")
	}
}

// XP-04：经 Service.Check 的端到端行为。
func TestKernelDataDirCaseVariantIsDenied(t *testing.T) {
	env := newEnv(t, nil)
	if err := os.WriteFile(filepath.Join(env.dataDir, "daemon.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := strings.ToUpper(env.dataDir) + string(filepath.Separator) + "DAEMON.JSON"
	r := env.req("mcp_read", model.RiskRead, model.AutonomyAutoInSandbox, map[string]any{"path": p})
	v, err := env.svc.Check(context.Background(), r)
	if err != nil || v.Allowed || v.Source != SourceGuard || v.RuleID != "kernel-data-dir" {
		t.Fatalf("大小写变体的内核数据文件应被硬拒绝（而不是进入审批）: %+v %v", v, err)
	}
}

func TestResolveExistingKeepsMissingTail(t *testing.T) {
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := resolveExisting(filepath.Join(dir, "missing", "x"))
	if got != filepath.Join(real, "missing", "x") {
		t.Errorf("应解析已存在部分并拼回不存在的尾部，得到 %q", got)
	}
	if resolveExisting("rel/x") != "rel/x" {
		t.Error("相对路径应原样返回")
	}
}
