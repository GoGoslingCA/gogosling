package extension

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/protocol/mcp"
)

// toolsDigestForTest 与 mcp.ToolsDigest 一致，供假客户端使用。
func toolsDigestForTest(specs []model.ToolSpec) string { return mcp.ToolsDigest(specs) }

func TestInstall按目录区分技能与插件(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	if _, err := env.m.Install(ctx, "relative/skill"); model.CodeOf(err) != model.CodeInvalid {
		t.Fatalf("相对路径应拒绝: %v", err)
	}
	_, err := env.m.Install(ctx, t.TempDir())
	me, ok := err.(*model.Error)
	if !ok || me.Details["reason"] != "unknown_extension_layout" {
		t.Fatalf("空目录应说明无法识别布局: %v", err)
	}
	skill := writeSkillDir(t, "from-install", "由统一入口安装", "正文")
	if err := os.WriteFile(filepath.Join(skill, ManifestFile), []byte(`{"name":"ignored"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ext, err := env.m.Install(ctx, skill)
	if err != nil || ext.Kind != model.ExtensionSkill || ext.Name != "from-install" {
		t.Fatalf("同时存在时应按技能安装: %+v %v", ext, err)
	}
}

func TestInstallSkillCopiesAndRecords(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	src := writeSkillDir(t, "git-helper", "帮助撰写提交信息", "# 正文\n\n按步骤来。")

	ext, err := env.m.InstallSkill(ctx, src)
	if err != nil {
		t.Fatalf("安装技能应成功: %v", err)
	}
	if ext.Kind != model.ExtensionSkill || ext.Name != "git-helper" || ext.Version != "1.0.0" || !ext.Enabled {
		t.Errorf("记录字段不符: %+v", ext)
	}
	if !model.IsID(ext.ID) {
		t.Errorf("ID 应为 ULID，得到 %q", ext.ID)
	}
	want := filepath.Join(env.dataDir, "extensions", ext.ID)
	if ext.InstallDir != want {
		t.Errorf("安装目录应为 %s，得到 %s", want, ext.InstallDir)
	}
	if _, err := os.Stat(filepath.Join(want, "references", "notes.md")); err != nil {
		t.Errorf("资源文件应被复制: %v", err)
	}
	if !strings.HasPrefix(ext.Digest, "sha256:") {
		t.Errorf("应计算摘要，得到 %q", ext.Digest)
	}
	var manifest SkillManifest
	if err := json.Unmarshal(ext.Manifest, &manifest); err != nil || manifest.Description != "帮助撰写提交信息" {
		t.Errorf("Manifest 应保存 frontmatter: %s %v", ext.Manifest, err)
	}
	if ext.Source != src {
		t.Errorf("Source 应为来源目录，得到 %q", ext.Source)
	}
	if info, err := os.Stat(want); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o700) {
		t.Errorf("安装目录权限应为 0700: %v %v", info, err)
	}

	// 幂等：相同内容再次安装返回同一记录。
	again, err := env.m.InstallSkill(ctx, src)
	if err != nil || again.ID != ext.ID {
		t.Fatalf("重复安装应返回同一记录: %+v %v", again, err)
	}
	if n := len(env.repo.items); n != 1 {
		t.Errorf("应只有一条记录，得到 %d", n)
	}

	// 升级：内容变化时原地替换，保留 ID 与启用状态。
	if _, err := env.m.Disable(ctx, ext.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, SkillFile), []byte("---\nname: git-helper\ndescription: 新描述\nversion: 2.0.0\n---\n新正文\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	upgraded, err := env.m.InstallSkill(ctx, src)
	if err != nil {
		t.Fatalf("升级应成功: %v", err)
	}
	if upgraded.ID != ext.ID || upgraded.Version != "2.0.0" || upgraded.Digest == ext.Digest || upgraded.Enabled {
		t.Errorf("升级应保留 ID、更新版本与摘要、保留停用状态: %+v", upgraded)
	}
	if _, err := os.Stat(filepath.Join(env.dataDir, "extensions", ".old-"+ext.ID)); !os.IsNotExist(err) {
		t.Errorf("升级后不应残留旧目录")
	}
	if _, err := os.Stat(filepath.Join(env.dataDir, "extensions", ".tmp-"+ext.ID)); !os.IsNotExist(err) {
		t.Errorf("升级后不应残留临时目录")
	}
	types := drainEvents(t, env.bus, 0)
	if !containsString(types, EventInstalled) || !containsString(types, EventUpdated) {
		t.Errorf("应发布 installed 与 updated 事件，得到 %v", types)
	}
}

func TestInstallSkillRejectsInvalid(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	missing := filepath.Join(t.TempDir(), "no-skill")
	_ = os.MkdirAll(missing, 0o755)
	if _, err := env.m.InstallSkill(ctx, missing); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("缺少 SKILL.md 应返回 CodeInvalid，得到 %v", err)
	}

	noDesc := filepath.Join(t.TempDir(), "nodesc")
	_ = os.MkdirAll(noDesc, 0o755)
	_ = os.WriteFile(filepath.Join(noDesc, SkillFile), []byte("---\nname: nodesc\n---\nbody\n"), 0o644)
	if _, err := env.m.InstallSkill(ctx, noDesc); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("缺少 description 应返回 CodeInvalid，得到 %v", err)
	}

	if _, err := env.m.InstallSkill(ctx, filepath.Join(t.TempDir(), "nope")); !model.IsCode(err, model.CodeNotFound) {
		t.Errorf("目录不存在应返回 CodeNotFound，得到 %v", err)
	}

	file := filepath.Join(t.TempDir(), "file.txt")
	_ = os.WriteFile(file, []byte("x"), 0o644)
	if _, err := env.m.InstallSkill(ctx, file); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("来源不是目录应返回 CodeInvalid，得到 %v", err)
	}

	if n := len(env.repo.items); n != 0 {
		t.Errorf("失败的安装不应留下记录，得到 %d", n)
	}
	entries, _ := os.ReadDir(filepath.Join(env.dataDir, "extensions"))
	if len(entries) != 0 {
		t.Errorf("失败的安装不应留下目录，得到 %v", entries)
	}
}

func TestInstallSkillRejectsSymlink(t *testing.T) {
	skipOnWindows(t, "符号链接在 Windows 上需要额外权限")
	env := newTestEnv(t)
	src := writeSkillDir(t, "linky", "含符号链接", "body")
	if err := os.Symlink("/etc/hostname", filepath.Join(src, "leak")); err != nil {
		t.Skip("无法创建符号链接:", err)
	}
	if _, err := env.m.InstallSkill(context.Background(), src); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("含符号链接应返回 CodeInvalid，得到 %v", err)
	}
}

func TestInstallFromExtensionsRootIsRefused(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	ext, err := env.m.InstallSkill(ctx, writeSkillDir(t, "self", "自己装自己", "body"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.m.InstallSkill(ctx, ext.InstallDir); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("从扩展根目录内安装应被拒绝，得到 %v", err)
	}
}

func TestSkillsForAndLoadSkill(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	a, _ := env.m.InstallSkill(ctx, writeSkillDir(t, "alpha", "第一个技能", "# Alpha\n\n做 A。"))
	b, _ := env.m.InstallSkill(ctx, writeSkillDir(t, "beta", "第二个技能", "做 B。"))
	if _, err := env.m.Disable(ctx, b.ID); err != nil {
		t.Fatal(err)
	}

	skills, err := env.m.SkillsFor(ctx, testAgent(b.ID, a.ID, "missing", a.ID))
	if err != nil {
		t.Fatalf("SkillsFor 应成功: %v", err)
	}
	if len(skills) != 1 || skills[0].ID != a.ID || skills[0].Name != "alpha" || skills[0].Description != "第一个技能" || skills[0].Dir != a.InstallDir {
		t.Errorf("应只返回启用且存在的技能摘要: %+v", skills)
	}

	body, err := env.m.LoadSkill(ctx, a.ID)
	if err != nil {
		t.Fatalf("LoadSkill 应成功: %v", err)
	}
	if body != "# Alpha\n\n做 A。\n" {
		t.Errorf("正文应去掉 frontmatter，得到 %q", body)
	}
	if _, err := env.m.LoadSkill(ctx, b.ID); !model.IsCode(err, model.CodeDenied) {
		t.Errorf("未启用的技能应返回 CodeDenied，得到 %v", err)
	}
	if _, err := env.m.LoadSkill(ctx, "missing"); !model.IsCode(err, model.CodeNotFound) {
		t.Errorf("不存在的技能应返回 CodeNotFound，得到 %v", err)
	}
	if _, err := env.m.SkillsFor(ctx, nil); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("nil agent 应返回 CodeInvalid")
	}
}

func TestRegisterMCPValidation(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	cfg := model.MCPServerConfig{Transport: model.MCPStdio, Command: "srv", Env: map[string]string{"TOKEN": "secret:mcp/token"}}
	ext, err := env.m.RegisterMCP(ctx, "搜索服务", cfg)
	if err != nil {
		t.Fatalf("登记应成功: %v", err)
	}
	if ext.Kind != model.ExtensionMCPServer || ext.Enabled || ext.MCP == nil || ext.MCP.Env["TOKEN"] != "secret:mcp/token" {
		t.Errorf("记录不符: %+v", ext)
	}
	if ext.Digest != "" {
		t.Errorf("登记时不应有摘要（Enable 时才固定）")
	}
	if info, err := os.Stat(ext.InstallDir); err != nil || !info.IsDir() {
		t.Errorf("应创建工作目录: %v", err)
	}
	if _, err := env.m.RegisterMCP(ctx, "搜索服务", cfg); !model.IsCode(err, model.CodeConflict) {
		t.Errorf("同名登记应返回 CodeConflict，得到 %v", err)
	}
	if _, err := env.m.RegisterMCP(ctx, "", cfg); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("空名称应返回 CodeInvalid")
	}
	if _, err := env.m.RegisterMCP(ctx, "x", model.MCPServerConfig{Transport: model.MCPHTTP, URL: "http://mcp.example.com"}); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("公网明文 http 应返回 CodeInvalid，得到 %v", err)
	}
	if _, err := env.m.RegisterMCP(ctx, "x", model.MCPServerConfig{Transport: model.MCPStdio}); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("缺少 command 应返回 CodeInvalid")
	}
	// 原配置被修改不应影响记录。
	cfg.Env["TOKEN"] = "changed"
	got, _ := env.m.Get(ctx, ext.ID)
	if got.MCP.Env["TOKEN"] != "secret:mcp/token" {
		t.Errorf("记录应保存配置副本")
	}
}

func TestEnableDisableUninstallLifecycle(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	ext, err := env.m.RegisterMCP(ctx, "svc", model.MCPServerConfig{Transport: model.MCPStdio, Command: "srv"})
	if err != nil {
		t.Fatal(err)
	}

	enabled, err := env.m.Enable(ctx, ext.ID)
	if err != nil {
		t.Fatalf("Enable 应成功: %v", err)
	}
	if !enabled.Enabled || enabled.Digest == "" {
		t.Errorf("Enable 应启用并固定摘要: %+v", enabled)
	}
	if st := env.m.Status(ext.ID); !st.Connected || st.ToolCount != 2 || st.ToolsChanged {
		t.Errorf("启用后应有连接状态: %+v", st)
	}
	if again, err := env.m.Enable(ctx, ext.ID); err != nil || !again.Enabled {
		t.Errorf("重复 Enable 应幂等: %v", err)
	}
	if env.dialCount() != 1 {
		t.Errorf("连接应被复用，拨号次数 %d", env.dialCount())
	}

	disabled, err := env.m.Disable(ctx, ext.ID)
	if err != nil || disabled.Enabled {
		t.Fatalf("Disable 应成功: %+v %v", disabled, err)
	}
	if !env.lastClient().isClosed() {
		t.Errorf("Disable 应关闭连接")
	}
	if st := env.m.Status(ext.ID); st.Connected {
		t.Errorf("停用后不应有连接")
	}
	list, _ := env.m.List(ctx, model.ExtensionMCPServer)
	if len(list) != 1 || list[0].Enabled {
		t.Errorf("List 应反映停用状态: %+v", list)
	}
	if all, _ := env.m.List(ctx, ""); len(all) != 1 {
		t.Errorf("kind 为空应列出全部")
	}

	dir := ext.InstallDir
	if err := env.m.Uninstall(ctx, ext.ID); err != nil {
		t.Fatalf("Uninstall 应成功: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("卸载应删除目录")
	}
	if _, err := env.m.Get(ctx, ext.ID); !model.IsCode(err, model.CodeNotFound) {
		t.Errorf("卸载后应 NotFound，得到 %v", err)
	}
	if err := env.m.Uninstall(ctx, ext.ID); !model.IsCode(err, model.CodeNotFound) {
		t.Errorf("重复卸载应 NotFound，得到 %v", err)
	}
	types := drainEvents(t, env.bus, 0)
	if !containsString(types, EventRemoved) {
		t.Errorf("应发布 removed 事件，得到 %v", types)
	}
}

func TestUninstallRefusesForeignDir(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	victim := t.TempDir()
	_ = os.WriteFile(filepath.Join(victim, "keep.txt"), []byte("x"), 0o644)
	ext := &model.Extension{ID: model.NewID(), Kind: model.ExtensionSkill, Name: "evil", Source: "x", InstallDir: victim, Enabled: true, InstalledAt: env.m.now(), UpdatedAt: env.m.now()}
	if err := env.repo.CreateExtension(ctx, ext); err != nil {
		t.Fatal(err)
	}
	if err := env.m.Uninstall(ctx, ext.ID); err != nil {
		t.Fatalf("记录应被删除: %v", err)
	}
	if _, err := os.Stat(filepath.Join(victim, "keep.txt")); err != nil {
		t.Errorf("扩展根目录之外的目录不得被删除: %v", err)
	}
	if !strings.Contains(env.logs.String(), "拒绝删除") {
		t.Errorf("应记录拒绝删除的日志")
	}
}

func TestToolsForSkipsMissingDisabledAndSkills(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	skill, _ := env.m.InstallSkill(ctx, writeSkillDir(t, "sk", "技能", "body"))
	svc, _ := env.m.RegisterMCP(ctx, "svc", model.MCPServerConfig{Transport: model.MCPStdio, Command: "srv"})
	// 未启用的 MCP 服务不应被连接。
	specs, err := env.m.ToolsFor(ctx, testAgent(skill.ID, svc.ID, "missing"))
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 0 || env.dialCount() != 0 {
		t.Errorf("未启用的服务不应暴露工具也不应拨号: %v %d", specNames(specs), env.dialCount())
	}
	if _, err := env.m.Enable(ctx, svc.ID); err != nil {
		t.Fatal(err)
	}
	specs, err = env.m.ToolsFor(ctx, testAgent(skill.ID, svc.ID))
	if err != nil {
		t.Fatal(err)
	}
	names := specNames(specs)
	if len(names) != 2 || names[0] != "search" || names[1] != "fetch_page" {
		t.Errorf("应返回规范化后的 MCP 工具名: %v", names)
	}
	if specs[0].Source != model.ToolSource("mcp:"+svc.ID) {
		t.Errorf("Source 不符: %q", specs[0].Source)
	}
	// 调用按原名分发。
	res, err := env.m.CallTool(ctx, testAgent(skill.ID, svc.ID), model.ToolCall{ID: "c1", Name: "fetch_page", Arguments: json.RawMessage(`{"url":"x"}`)})
	if err != nil {
		t.Fatalf("CallTool 应成功: %v", err)
	}
	if res.CallID != "c1" || res.Content != `mcp:fetch page:{"url":"x"}` {
		t.Errorf("结果不符: %+v", res)
	}
	if _, err := env.m.CallTool(ctx, testAgent(skill.ID, svc.ID), model.ToolCall{ID: "c2", Name: "nope"}); !model.IsCode(err, model.CodeNotFound) {
		t.Errorf("未知工具应返回 CodeNotFound，得到 %v", err)
	}
	// 智能体没有选用该扩展时不得调用。
	if _, err := env.m.CallTool(ctx, testAgent(skill.ID), model.ToolCall{ID: "c3", Name: "search"}); !model.IsCode(err, model.CodeNotFound) {
		t.Errorf("未选用的扩展应返回 CodeNotFound，得到 %v", err)
	}
	if _, err := env.m.ToolsFor(ctx, nil); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("nil agent 应返回 CodeInvalid")
	}
}

func TestToolNameCollisionsAcrossExtensions(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	a, _ := env.m.RegisterMCP(ctx, "a", model.MCPServerConfig{Transport: model.MCPStdio, Command: "srv"})
	b, _ := env.m.RegisterMCP(ctx, "b", model.MCPServerConfig{Transport: model.MCPStdio, Command: "srv"})
	for _, id := range []string{a.ID, b.ID} {
		if _, err := env.m.Enable(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	specs, err := env.m.ToolsFor(ctx, testAgent(a.ID, b.ID))
	if err != nil {
		t.Fatal(err)
	}
	names := specNames(specs)
	want := []string{"search", "fetch_page", "search_2", "fetch_page_2"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("冲突的工具名应加后缀: %v", names)
	}
	res, err := env.m.CallTool(ctx, testAgent(a.ID, b.ID), model.ToolCall{ID: "c", Name: "search_2"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "mcp:search:" {
		t.Errorf("后缀名应分发到第二个服务的原名: %+v", res)
	}
	// 第二个服务收到的是原名。
	if calls := env.clients[1].calls; len(calls) != 1 || calls[0] != "search" {
		t.Errorf("第二个服务应收到原名 search，得到 %v", calls)
	}
}

func TestManagerWithoutDialer(t *testing.T) {
	repo := newFakeRepo()
	m := New(repo, t.TempDir(), nil, nil)
	ctx := context.Background()
	ext, err := m.RegisterMCP(ctx, "svc", model.MCPServerConfig{Transport: model.MCPStdio, Command: "srv"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Enable(ctx, ext.ID); !model.IsCode(err, model.CodeUnavailable) {
		t.Errorf("没有拨号器时 Enable 应返回 CodeUnavailable，得到 %v", err)
	}
}
