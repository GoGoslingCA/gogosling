package extension

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

func installTestPlugin(t *testing.T, env *testEnv, name string, perms Permissions, tools ...string) *model.Extension {
	t.Helper()
	dir := writePluginDir(t, testPluginManifest(t, name, perms, tools...))
	ext, err := env.m.InstallPlugin(context.Background(), dir)
	if err != nil {
		t.Fatalf("安装插件应成功: %v", err)
	}
	return ext
}

func TestInstallPluginRecordsManifest(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	ext := installTestPlugin(t, env, "demo", Permissions{Network: true}, "echo", "fail")
	if ext.Kind != model.ExtensionPlugin || ext.Enabled || ext.Version != "0.1.0" || ext.Name != "demo" {
		t.Errorf("插件应默认停用并保存版本: %+v", ext)
	}
	var m PluginManifest
	if err := json.Unmarshal(ext.Manifest, &m); err != nil || len(m.Tools) != 2 || !m.Permissions.Network {
		t.Errorf("Manifest 应保存规范化后的清单: %s %v", ext.Manifest, err)
	}
	if _, err := os.Stat(filepath.Join(ext.InstallDir, ManifestFile)); err != nil {
		t.Errorf("清单文件应被复制: %v", err)
	}
	// 未启用：不暴露工具，调用被拒绝。
	specs, _ := env.m.ToolsFor(ctx, testAgent(ext.ID))
	if len(specs) != 0 {
		t.Errorf("未启用的插件不应暴露工具: %v", specNames(specs))
	}
	if _, err := env.m.CallTool(ctx, testAgent(ext.ID), model.ToolCall{ID: "c", Name: "echo"}); !model.IsCode(err, model.CodeNotFound) {
		t.Errorf("未启用的插件工具应 NotFound，得到 %v", err)
	}
}

func TestInstallPluginRejectsInvalidManifest(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	bad := func(name string, mutate func(*PluginManifest)) {
		t.Helper()
		m := testPluginManifest(t, name, Permissions{}, "echo")
		mutate(&m)
		dir := writePluginDir(t, m)
		if _, err := env.m.InstallPlugin(ctx, dir); !model.IsCode(err, model.CodeInvalid) {
			t.Errorf("%s: 应返回 CodeInvalid，得到 %v", name, err)
		}
	}
	bad("no-entry", func(m *PluginManifest) { m.Entry = "" })
	bad("no-tools", func(m *PluginManifest) { m.Tools = nil })
	bad("bad-kind", func(m *PluginManifest) { m.Kind = "skill" })
	bad("bad-tool", func(m *PluginManifest) { m.Tools[0].Name = "中文名" })
	bad("dup-tool", func(m *PluginManifest) { m.Tools = append(m.Tools, m.Tools[0]) })
	bad("bad-schema", func(m *PluginManifest) { m.Tools[0].InputSchema = json.RawMessage(`[1]`) })
	bad("slash/name", func(m *PluginManifest) { m.Name = "a/b" })

	noManifest := filepath.Join(t.TempDir(), "empty")
	_ = os.MkdirAll(noManifest, 0o755)
	if _, err := env.m.InstallPlugin(ctx, noManifest); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("缺少 extension.json 应返回 CodeInvalid，得到 %v", err)
	}
	if n := len(env.repo.items); n != 0 {
		t.Errorf("失败的安装不应留下记录，得到 %d", n)
	}
}

func TestPluginToolsAndCalls(t *testing.T) {
	env := newTestEnv(t, WithPluginTimeout(2*time.Second), WithMaxPluginOutput(2048))
	ctx := context.Background()
	ext := installTestPlugin(t, env, "demo", Permissions{Shell: true}, "echo", "fail", "sleep", "big", "env", "json", "garbage", "silent", "crash", "cwd", "multiline")
	if _, err := env.m.Enable(ctx, ext.ID); err != nil {
		t.Fatalf("Enable 应成功: %v", err)
	}
	agent := testAgent(ext.ID)

	specs, err := env.m.ToolsFor(ctx, agent)
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 11 {
		t.Fatalf("应暴露全部工具，得到 %v", specNames(specs))
	}
	if specs[0].Source != model.ToolSource("extension:"+ext.ID) || specs[0].Risk != model.RiskExecute {
		t.Errorf("Source / Risk 不符: %+v", specs[0])
	}
	if !strings.Contains(string(specs[0].InputSchema), `"text"`) {
		t.Errorf("schema 应原样保留: %s", specs[0].InputSchema)
	}

	call := func(name string, args string) (model.ToolResult, error) {
		return env.m.CallTool(ctx, agent, model.ToolCall{ID: "id-" + name, Name: name, Arguments: json.RawMessage(args)})
	}

	res, err := call("echo", `{"text":"hi"}`)
	if err != nil || res.Content != "echo:hi" || res.IsError || res.CallID != "id-echo" {
		t.Errorf("echo 结果不符: %+v %v", res, err)
	}
	res, err = call("fail", `{}`)
	if err != nil || !res.IsError || res.Content != "bad input" {
		t.Errorf("is_error 应透传: %+v %v", res, err)
	}
	res, err = call("json", ``)
	if err != nil || res.Content != `{"a":1,"b":[1,2]}` {
		t.Errorf("非字符串 content 应紧凑序列化: %+v %v", res, err)
	}
	res, err = call("multiline", `{}`)
	if err != nil || res.Content != "first line" {
		t.Errorf("只取 stdout 的第一行: %+v %v", res, err)
	}
	res, err = call("cwd", `{}`)
	if err != nil {
		t.Fatalf("cwd 应成功: %v", err)
	}
	wantDir, _ := filepath.EvalSymlinks(ext.InstallDir)
	gotDir, _ := filepath.EvalSymlinks(res.Content)
	if gotDir != wantDir {
		t.Errorf("工作目录应为安装目录: %q != %q", gotDir, wantDir)
	}
	res, err = call("env", `{"name":"GOGOSLING_EXTENSION_ID"}`)
	if err != nil || res.Content != ext.ID {
		t.Errorf("应传入 GOGOSLING_EXTENSION_ID: %+v %v", res, err)
	}
	t.Setenv("GOGOSLING_PARENT_SECRET", "leak")
	res, err = call("env", `{"name":"GOGOSLING_PARENT_SECRET"}`)
	if err != nil || res.Content != "<unset>" {
		t.Errorf("白名单之外的父进程变量不应透传: %+v %v", res, err)
	}

	if _, err := call("sleep", `{"millis":10000}`); !model.IsCode(err, model.CodeTimeout) {
		t.Errorf("超时应返回 CodeTimeout，得到 %v", err)
	}
	if _, err := call("big", `{"bytes":4096}`); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("输出超限应返回 CodeInvalid，得到 %v", err)
	}
	if _, err := call("garbage", `{}`); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("非 JSON 响应应返回 CodeInvalid，得到 %v", err)
	}
	if _, err := call("silent", `{}`); !model.IsCode(err, model.CodeUnavailable) {
		t.Errorf("没有响应应返回 CodeUnavailable，得到 %v", err)
	}
	if err := func() error { _, err := call("crash", `{}`); return err }(); !model.IsCode(err, model.CodeUnavailable) || !strings.Contains(err.Error(), "plugin exploded") {
		t.Errorf("崩溃应返回 CodeUnavailable 并附带 stderr，得到 %v", err)
	}
	if _, err := call("echo", `not json`); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("非法参数应返回 CodeInvalid，得到 %v", err)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := env.m.CallTool(cctx, agent, model.ToolCall{ID: "x", Name: "sleep", Arguments: json.RawMessage(`{"millis":5000}`)}); !model.IsCode(err, model.CodeCancelled) {
		t.Errorf("取消应返回 CodeCancelled，得到 %v", err)
	}
}

func TestPluginRiskFromPermissions(t *testing.T) {
	cases := []struct {
		perms Permissions
		want  model.ToolRisk
	}{
		{Permissions{}, model.RiskRead},
		{Permissions{Filesystem: true}, model.RiskWrite},
		{Permissions{Network: true}, model.RiskNetwork},
		{Permissions{Filesystem: true, Network: true}, model.RiskNetwork},
		{Permissions{Shell: true}, model.RiskExecute},
		{Permissions{Shell: true, Network: true, Filesystem: true}, model.RiskExecute},
	}
	for _, tc := range cases {
		if got := tc.perms.Risk(); got != tc.want {
			t.Errorf("%+v 应映射为 %s，得到 %s", tc.perms, tc.want, got)
		}
	}
}

func TestPluginTamperDetection(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	ext := installTestPlugin(t, env, "demo", Permissions{}, "echo")
	if _, err := env.m.Enable(ctx, ext.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.m.Disable(ctx, ext.ID); err != nil {
		t.Fatal(err)
	}
	// 篡改安装目录里的文件。
	if err := os.WriteFile(filepath.Join(ext.InstallDir, "extra.txt"), []byte("injected"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := env.m.Enable(ctx, ext.ID); !model.IsCode(err, model.CodeConflict) {
		t.Errorf("文件被改动后 Enable 应返回 CodeConflict，得到 %v", err)
	}
	if !strings.Contains(env.logs.String(), "与安装时不一致") {
		t.Errorf("应记录警告日志")
	}
	accepted, err := env.m.AcceptTools(ctx, ext.ID)
	if err != nil {
		t.Fatalf("AcceptTools 应成功: %v", err)
	}
	if accepted.Digest == ext.Digest {
		t.Errorf("AcceptTools 应更新摘要")
	}
	if _, err := env.m.Enable(ctx, ext.ID); err != nil {
		t.Errorf("确认后 Enable 应成功: %v", err)
	}
}

func TestPluginRelativeEntryKeepsExecBit(t *testing.T) {
	skipOnWindows(t, "依赖 shell 脚本与可执行位")
	env := newTestEnv(t)
	ctx := context.Background()
	m := testPluginManifest(t, "script", Permissions{}, "echo")
	exe := m.Entry
	m.Entry = "./bin/run.sh"
	m.Args = nil
	dir := writePluginDir(t, m)
	_ = os.MkdirAll(filepath.Join(dir, "bin"), 0o755)
	script := "#!/bin/sh\nexec \"" + exe + "\" " + testPluginArg + "\n"
	if err := os.WriteFile(filepath.Join(dir, "bin", "run.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ext, err := env.m.InstallPlugin(ctx, dir)
	if err != nil {
		t.Fatalf("安装应成功: %v", err)
	}
	info, err := os.Stat(filepath.Join(ext.InstallDir, "bin", "run.sh"))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("可执行文件应复制为 0700: %v %v", info, err)
	}
	if _, err := env.m.Enable(ctx, ext.ID); err != nil {
		t.Fatal(err)
	}
	res, err := env.m.CallTool(ctx, testAgent(ext.ID), model.ToolCall{ID: "c", Name: "echo", Arguments: json.RawMessage(`{"text":"via script"}`)})
	if err != nil || res.Content != "echo:via script" {
		t.Errorf("相对入口应解析到安装目录: %+v %v", res, err)
	}
}

func TestResolveEntryRejectsEscape(t *testing.T) {
	dir := t.TempDir()
	if _, err := resolveEntry(dir, "../evil"); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("逃逸的相对入口应返回 CodeInvalid，得到 %v", err)
	}
	if _, err := resolveEntry(dir, "./missing"); !model.IsCode(err, model.CodeUnavailable) {
		t.Errorf("不存在的相对入口应返回 CodeUnavailable，得到 %v", err)
	}
	if _, err := resolveEntry(dir, "gogosling-definitely-missing-binary"); !model.IsCode(err, model.CodeUnavailable) {
		t.Errorf("PATH 中不存在的命令应返回 CodeUnavailable，得到 %v", err)
	}
	local := filepath.Join(dir, "tool")
	_ = os.WriteFile(local, []byte("x"), 0o700)
	if got, err := resolveEntry(dir, "tool"); err != nil || got != local {
		t.Errorf("裸名应优先解析到插件目录内的文件: %q %v", got, err)
	}
}

type recordingRunner struct {
	spec ProcessSpec
	out  []byte
}

func (r *recordingRunner) Run(_ context.Context, spec ProcessSpec) ([]byte, error) {
	r.spec = spec
	return r.out, nil
}

func TestProcessRunnerReceivesPermissions(t *testing.T) {
	runner := &recordingRunner{out: []byte(`{"content":"from sandbox"}` + "\n")}
	env := newTestEnv(t, WithProcessRunner(runner))
	ctx := context.Background()
	ext := installTestPlugin(t, env, "sandboxed", Permissions{Network: true, Filesystem: true}, "echo")
	if _, err := env.m.Enable(ctx, ext.ID); err != nil {
		t.Fatal(err)
	}
	res, err := env.m.CallTool(ctx, testAgent(ext.ID), model.ToolCall{ID: "c", Name: "echo", Arguments: json.RawMessage(`{"text":"x"}`)})
	if err != nil || res.Content != "from sandbox" {
		t.Fatalf("应使用注入的 ProcessRunner: %+v %v", res, err)
	}
	if !runner.spec.Permissions.Network || !runner.spec.Permissions.Filesystem || runner.spec.Permissions.Shell {
		t.Errorf("声明的权限应随 ProcessSpec 传给运行器: %+v", runner.spec.Permissions)
	}
	if runner.spec.Dir != ext.InstallDir || runner.spec.ExtensionID != ext.ID {
		t.Errorf("ProcessSpec 应携带目录与扩展 ID: %+v", runner.spec)
	}
	if !strings.Contains(string(runner.spec.Stdin), `"tool":"echo"`) || !strings.HasSuffix(string(runner.spec.Stdin), "\n") {
		t.Errorf("stdin 应是一行 JSON 请求: %q", runner.spec.Stdin)
	}
}
