package extension

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

func TestDigestFSIsStableAndSensitive(t *testing.T) {
	a := fstest.MapFS{
		"SKILL.md":        {Data: []byte("---\nname: x\ndescription: y\n---\n")},
		"ref/notes.md":    {Data: []byte("notes")},
		"ref/sub/a.txt":   {Data: []byte("a")},
		"empty-dir/.keep": {Data: []byte("")},
	}
	d1, stats, err := digestFS(a)
	if err != nil {
		t.Fatalf("digestFS 应成功: %v", err)
	}
	if stats.Files != 4 || stats.Bytes != int64(len("---\nname: x\ndescription: y\n---\n")+5+1) {
		t.Errorf("统计不符: %+v", stats)
	}
	if !strings.HasPrefix(d1, "sha256:") {
		t.Errorf("摘要应带前缀: %q", d1)
	}
	d2, _, _ := digestFS(a)
	if d1 != d2 {
		t.Errorf("同一内容摘要应稳定")
	}
	b := fstest.MapFS{}
	for k, v := range a {
		b[k] = v
	}
	b["ref/notes.md"] = &fstest.MapFile{Data: []byte("Notes")}
	if d3, _, _ := digestFS(b); d3 == d1 {
		t.Errorf("内容变化应改变摘要")
	}
	c := fstest.MapFS{}
	for k, v := range a {
		c[k] = v
	}
	c["ref/notes2.md"] = c["ref/notes.md"]
	delete(c, "ref/notes.md")
	if d4, _, _ := digestFS(c); d4 == d1 {
		t.Errorf("路径变化应改变摘要")
	}
	if empty, _, err := digestFS(fstest.MapFS{}); err != nil || !strings.HasPrefix(empty, "sha256:") {
		t.Errorf("空目录应有摘要: %q %v", empty, err)
	}
}

func TestDigestFSEnforcesLimits(t *testing.T) {
	many := fstest.MapFS{}
	for i := 0; i <= MaxExtensionFiles; i++ {
		many["f"+strings.Repeat("0", 5)+string(rune('a'+i%26))+strings.Repeat("x", i/26)] = &fstest.MapFile{Data: []byte("1")}
	}
	if _, _, err := digestFS(many); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("文件数超限应返回 CodeInvalid，得到 %v", err)
	}
	big := fstest.MapFS{"big.bin": {Data: make([]byte, MaxExtensionBytes+1)}}
	if _, _, err := digestFS(big); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("总大小超限应返回 CodeInvalid，得到 %v", err)
	}
}

func TestCopyFSPreservesExecBitAndPerms(t *testing.T) {
	skipOnWindows(t, "权限位在 Windows 上没有意义")
	src := fstest.MapFS{
		"run.sh":    {Data: []byte("#!/bin/sh\n"), Mode: 0o755},
		"data.txt":  {Data: []byte("x"), Mode: 0o644},
		"d/deep.md": {Data: []byte("y"), Mode: 0o600},
	}
	dst := filepath.Join(t.TempDir(), "out")
	if err := copyFS(src, dst); err != nil {
		t.Fatalf("copyFS 应成功: %v", err)
	}
	check := func(rel string, want os.FileMode) {
		t.Helper()
		info, err := os.Stat(filepath.Join(dst, rel))
		if err != nil {
			t.Fatalf("%s 应存在: %v", rel, err)
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s 权限应为 %o，得到 %o", rel, want, info.Mode().Perm())
		}
	}
	check("run.sh", 0o700)
	check("data.txt", 0o600)
	check("d/deep.md", 0o600)
	check("d", 0o700)
	check(".", 0o700)
	if err := copyFS(src, dst); err == nil {
		t.Errorf("目标已存在时应失败")
	}
}

func TestStageFilesRollsBackOnFailure(t *testing.T) {
	env := newTestEnv(t)
	bad := fstest.MapFS{"ok.txt": {Data: []byte("x")}, "dev": {Mode: os.ModeDevice}}
	if _, err := env.m.stageFiles(bad, "ID1"); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("特殊文件应返回 CodeInvalid，得到 %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(env.dataDir, "extensions"))
	if len(entries) != 0 {
		t.Errorf("失败后不应残留目录: %v", entries)
	}
}

func TestSeedBundledInstallsAndUpgrades(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	exe, _ := os.Executable()
	bundle := fstest.MapFS{
		"skills/git-commit-message/SKILL.md": {Data: []byte("---\nname: git-commit-message\ndescription: 写提交信息\nversion: 1.0.0\n---\n正文 v1\n")},
		"skills/_draft/SKILL.md":             {Data: []byte("---\nname: draft\ndescription: 草稿\n---\n")},
		"skills/broken/SKILL.md":             {Data: []byte("no frontmatter")},
		"skills/README.md":                   {Data: []byte("not a skill dir")},
		"plugins/hello/extension.json":       {Data: []byte(`{"name":"hello","version":"1.0.0","entry":"` + strings.ReplaceAll(exe, `\`, `\\`) + `","args":["` + testPluginArg + `"],"tools":[{"name":"echo","description":"echo"}]}`)},
	}
	report, err := env.m.SeedBundled(ctx, bundle)
	if err != nil {
		t.Fatalf("SeedBundled 应成功: %v", err)
	}
	if len(report.Installed) != 2 || len(report.Upgraded) != 0 {
		t.Errorf("应安装 1 个技能与 1 个插件: %+v", report)
	}
	if len(report.Failed) != 1 || !strings.Contains(report.Failed[0], "skills/broken") {
		t.Errorf("损坏的条目应记录失败且不影响其它: %+v", report.Failed)
	}
	skills, _ := env.m.List(ctx, model.ExtensionSkill)
	if len(skills) != 1 || skills[0].Source != "bundled:git-commit-message" || !skills[0].Enabled {
		t.Errorf("内置技能应启用且来源为 bundled:<name>: %+v", skills)
	}
	plugins, _ := env.m.List(ctx, model.ExtensionPlugin)
	if len(plugins) != 1 || plugins[0].Source != "bundled:hello" || plugins[0].Enabled {
		t.Errorf("内置插件应停用且来源为 bundled:<name>: %+v", plugins)
	}
	skillID := skills[0].ID

	// 再次播种：无变化。
	report, err = env.m.SeedBundled(ctx, bundle)
	if err != nil || len(report.Installed) != 0 || len(report.Upgraded) != 0 {
		t.Errorf("内容未变时不应有动作: %+v %v", report, err)
	}

	// 升级：内容变化，保留 ID 与用户的停用状态。
	if _, err := env.m.Disable(ctx, skillID); err != nil {
		t.Fatal(err)
	}
	bundle["skills/git-commit-message/SKILL.md"] = &fstest.MapFile{Data: []byte("---\nname: git-commit-message\ndescription: 写提交信息（新）\nversion: 1.1.0\n---\n正文 v2\n")}
	report, err = env.m.SeedBundled(ctx, bundle)
	if err != nil || len(report.Upgraded) != 1 || report.Upgraded[0] != skillID {
		t.Fatalf("应升级内置技能: %+v %v", report, err)
	}
	got, _ := env.m.Get(ctx, skillID)
	if got.Version != "1.1.0" || got.Enabled {
		t.Errorf("升级应更新版本并保留停用状态: %+v", got)
	}
	if _, err := env.m.Enable(ctx, skillID); err != nil {
		t.Fatal(err)
	}
	if body, _ := env.m.LoadSkill(ctx, skillID); body != "正文 v2\n" {
		t.Errorf("升级后正文应更新: %q", body)
	}

	// 用户用自己的来源覆盖后，内置版本不再回退。
	userDir := writeSkillDir(t, "git-commit-message", "用户版本", "用户正文")
	if _, err := env.m.InstallSkill(ctx, userDir); err != nil {
		t.Fatal(err)
	}
	bundle["skills/git-commit-message/SKILL.md"] = &fstest.MapFile{Data: []byte("---\nname: git-commit-message\ndescription: 又改了\nversion: 1.2.0\n---\n正文 v3\n")}
	report, err = env.m.SeedBundled(ctx, bundle)
	if err != nil || len(report.Upgraded) != 0 {
		t.Errorf("用户接管的扩展不应被内置版本覆盖: %+v %v", report, err)
	}
	if got, _ := env.m.Get(ctx, skillID); got.Source != userDir {
		t.Errorf("来源应保持为用户目录: %q", got.Source)
	}

	if _, err := env.m.SeedBundled(ctx, nil); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("nil 文件系统应返回 CodeInvalid")
	}
	if report, err := env.m.SeedBundled(ctx, fstest.MapFS{}); err != nil || len(report.Installed) != 0 {
		t.Errorf("空文件系统应无动作: %+v %v", report, err)
	}
}

func TestSeedBundledRepoSkill(t *testing.T) {
	// 仓库里的示例技能必须能被安装。
	root, err := filepath.Abs(filepath.Join("..", "..", "extensions", "bundled"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "skills", "git-commit-message", SkillFile)); err != nil {
		t.Skipf("仓库内置技能不存在: %v", err)
	}
	env := newTestEnv(t)
	report, err := env.m.SeedBundled(context.Background(), os.DirFS(root))
	if err != nil {
		t.Fatalf("播种仓库内置扩展应成功: %v", err)
	}
	if len(report.Failed) != 0 || len(report.Installed) == 0 {
		t.Fatalf("内置扩展应全部安装成功: %+v", report)
	}
	skills, _ := env.m.SkillsFor(context.Background(), testAgent(report.Installed...))
	found := false
	for _, s := range skills {
		if s.Name == "git-commit-message" && s.Description != "" {
			found = true
			body, err := env.m.LoadSkill(context.Background(), s.ID)
			if err != nil || !strings.Contains(body, "<type>(<scope>)") {
				t.Errorf("示例技能正文应可加载: %v", err)
			}
		}
	}
	if !found {
		t.Errorf("应包含 git-commit-message 技能: %+v", skills)
	}
}
