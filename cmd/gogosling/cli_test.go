package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/identity"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

// fakeAPI 是内存版 api：只实现测试用到的行为，其余方法返回 not_found。
type fakeAPI struct {
	api // 未覆盖的方法调用即 panic，提醒测试补实现

	agents    []model.Agent
	providers []providerView
	created   []providerInput
	decisions []model.Decision
	settings  map[string]any
	jobs      []jobInput
	channels  []channelInput
	mcp       []model.MCPServerConfig
	accepted  bool
}

func (f *fakeAPI) Version(context.Context) (versionInfo, error) {
	v := versionInfo{Version: "1.2.3", Commit: "abc", StartedAt: "2026-10-10T00:00:00Z"}
	v.Worker.Status = "ready"
	return v, nil
}
func (f *fakeAPI) ListAgents(context.Context) ([]model.Agent, error) { return f.agents, nil }
func (f *fakeAPI) GetAgent(_ context.Context, id string) (model.Agent, error) {
	for _, a := range f.agents {
		if a.ID == id {
			return a, nil
		}
	}
	return model.Agent{}, model.NotFound("agent", id)
}
func (f *fakeAPI) ListProviders(context.Context) ([]providerView, error) { return f.providers, nil }
func (f *fakeAPI) CreateProvider(_ context.Context, in providerInput) (providerView, error) {
	f.created = append(f.created, in)
	return providerView{ID: model.NewID(), Name: in.Name, Kind: in.Kind, HasSecret: in.APIKey != ""}, nil
}
func (f *fakeAPI) CreateAgent(_ context.Context, in agentInput) (model.Agent, error) {
	a := model.Agent{ID: model.NewID(), Name: in.Name, Model: in.Model, Autonomy: in.Autonomy}
	f.agents = append(f.agents, a)
	return a, nil
}
func (f *fakeAPI) DecideApproval(_ context.Context, id string, d model.Decision) (model.ApprovalRequest, error) {
	f.decisions = append(f.decisions, d)
	status := model.ApprovalDenied
	if d.Approve {
		status = model.ApprovalApproved
	}
	return model.ApprovalRequest{ID: id, Status: status, Scope: d.Scope}, nil
}
func (f *fakeAPI) GetSettings(context.Context) (map[string]any, error) { return f.settings, nil }
func (f *fakeAPI) UpdateSettings(_ context.Context, patch map[string]any) (map[string]any, error) {
	for k, v := range patch {
		f.settings[k] = v
	}
	return f.settings, nil
}
func (f *fakeAPI) CreateJob(_ context.Context, in jobInput) (model.Job, error) {
	f.jobs = append(f.jobs, in)
	return model.Job{ID: "job1", Name: in.Name}, nil
}
func (f *fakeAPI) CreateChannel(_ context.Context, in channelInput) (channelView, error) {
	f.channels = append(f.channels, in)
	return channelView{Channel: model.Channel{ID: "ch1", Name: in.Name}}, nil
}
func (f *fakeAPI) ExtensionStatus(_ context.Context, id string) (extensionStatus, error) {
	if id != "ext1" {
		return extensionStatus{}, model.NotFound("extension", id)
	}
	return extensionStatus{ID: id, Connected: true, ToolCount: 3, ToolsChanged: !f.accepted}, nil
}
func (f *fakeAPI) AcceptExtensionTools(ctx context.Context, id string) (extensionStatus, error) {
	f.accepted = true
	return f.ExtensionStatus(ctx, id)
}
func (f *fakeAPI) RegisterMCP(_ context.Context, name string, cfg model.MCPServerConfig) (model.Extension, error) {
	f.mcp = append(f.mcp, cfg)
	return model.Extension{ID: "ext1", Name: name, Kind: model.ExtensionMCPServer}, nil
}

type harness struct {
	c      *cli
	out    *bytes.Buffer
	errOut *bytes.Buffer
	api    *fakeAPI
	dialed int
}

func newHarness(t *testing.T, stdin string, env map[string]string) *harness {
	t.Helper()
	h := &harness{out: &bytes.Buffer{}, errOut: &bytes.Buffer{}, api: &fakeAPI{settings: map[string]any{}}}
	if env == nil {
		env = map[string]string{}
	}
	if _, ok := env["LANG"]; !ok {
		env["LANG"] = "zh_CN.UTF-8"
	}
	h.c = &cli{
		stdin:  strings.NewReader(stdin),
		out:    h.out,
		errOut: h.errOut,
		getenv: func(k string) string { return env[k] },
		dial: func(context.Context, string, string) (api, error) {
			h.dialed++
			return h.api, nil
		},
		daemonCtl:  &fakeDaemon{},
		interrupts: func() (<-chan os.Signal, func()) { return make(chan os.Signal), func() {} },
		home:       t.TempDir(),
	}
	return h
}

func (h *harness) run(args ...string) int {
	return h.c.run(context.Background(), args)
}

func TestTableAlignsWideCharacters(t *testing.T) {
	tb := newTable("ID", "名称", "状态")
	tb.add("1", "助理", "运行中")
	tb.add("22", "assistant", "ok")
	var buf bytes.Buffer
	if err := tb.render(&buf); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("应有表头加两行，得到 %q", buf.String())
	}
	// 第三列在每一行的显示位置必须相同（中文按 2 列计）。
	col := func(line, cell string) int { return displayWidth(line[:strings.Index(line, cell)]) }
	if a, b, c := col(lines[0], "状态"), col(lines[1], "运行中"), col(lines[2], "ok"); a != b || b != c {
		t.Fatalf("第三列未对齐：%d %d %d\n%s", a, b, c, buf.String())
	}
	if displayWidth("中a") != 3 || runeWidth('é') != 1 {
		t.Fatal("显示宽度计算错误")
	}
	if got := truncateWidth("一二三四五", 5); got != "一二…" {
		t.Fatalf("按宽度截断错误: %q", got)
	}
	tb = newTable("A")
	tb.add("x\ny")
	buf.Reset()
	_ = tb.render(&buf)
	if strings.Count(buf.String(), "\n") != 2 {
		t.Fatalf("单元格内换行应压成一行，得到 %q", buf.String())
	}
}

func TestHelpUnknownCommandAndLanguage(t *testing.T) {
	h := newHarness(t, "", nil)
	if code := h.run(); code != exitOK || !strings.Contains(h.out.String(), "用法") {
		t.Fatalf("无参数应打印中文帮助，得到 %d %q", code, h.out.String())
	}
	h = newHarness(t, "", nil)
	if code := h.run("--lang", "en", "help"); code != exitOK || !strings.Contains(h.out.String(), "Usage") {
		t.Fatalf("--lang en 应打印英文帮助，得到 %q", h.out.String())
	}
	h = newHarness(t, "", map[string]string{"LANG": "en_US.UTF-8"})
	if code := h.run("frobnicate"); code != exitUsage || !strings.Contains(h.errOut.String(), "unknown command frobnicate") {
		t.Fatalf("未知命令应返回 2，得到 %d %q", code, h.errOut.String())
	}
	h = newHarness(t, "", nil)
	if code := h.run("agent", "fly"); code != exitUsage || !strings.Contains(h.errOut.String(), "fly") {
		t.Fatalf("未知子命令应返回 2，得到 %d %q", code, h.errOut.String())
	}
	h = newHarness(t, "", nil)
	if code := h.run("agent"); code != exitUsage {
		t.Fatalf("缺少子命令应返回 2，得到 %d", code)
	}
	if h.dialed != 0 {
		t.Fatal("参数错误不应连接守护进程")
	}
}

func TestDaemonNotRunningExitCode(t *testing.T) {
	h := newHarness(t, "", nil)
	h.c.dial = func(context.Context, string, string) (api, error) { return nil, errDaemonNotRunning }
	if code := h.run("agent", "list"); code != exitNoDaemon || !strings.Contains(h.errOut.String(), "守护进程未运行") {
		t.Fatalf("守护进程未运行应返回 3 并提示，得到 %d %q", code, h.errOut.String())
	}
}

func TestAgentListTableAndJSON(t *testing.T) {
	h := newHarness(t, "", nil)
	h.api.agents = []model.Agent{{ID: "01A", Name: "助理", Model: model.ModelRef{ProviderID: "p1", Model: "gpt"}, Autonomy: model.AutonomyAskRisky, Enabled: true}}
	if code := h.run("agent", "list"); code != exitOK {
		t.Fatalf("退出码 %d：%s", code, h.errOut.String())
	}
	out := h.out.String()
	for _, want := range []string{"名称", "助理", "p1/gpt", "ask_risky", "✓"} {
		if !strings.Contains(out, want) {
			t.Errorf("表格应包含 %q，得到:\n%s", want, out)
		}
	}

	// 全局参数可以出现在子命令之后。
	h.out.Reset()
	if code := h.run("agent", "list", "--json"); code != exitOK {
		t.Fatal(code)
	}
	var got struct {
		Items []model.Agent `json:"items"`
	}
	if err := json.Unmarshal(h.out.Bytes(), &got); err != nil || len(got.Items) != 1 || got.Items[0].Name != "助理" {
		t.Fatalf("--json 应输出 {items:[...]}，得到 %s / %v", h.out.String(), err)
	}

	h.api.agents = nil
	h.out.Reset()
	h.run("agent", "list")
	if !strings.Contains(h.out.String(), "（无）") {
		t.Fatalf("空列表应提示（无），得到 %q", h.out.String())
	}
}

func TestAgentCreateResolvesProviderByName(t *testing.T) {
	h := newHarness(t, "", nil)
	h.api.providers = []providerView{{ID: "01P", Name: "OpenAI", DefaultModel: "gpt-x"}}
	if code := h.run("agent", "create", "--name", "写作助理", "--provider", "openai", "--autonomy", "ask_all"); code != exitOK {
		t.Fatalf("退出码 %d：%s", code, h.errOut.String())
	}
	a := h.api.agents[0]
	if a.Model.ProviderID != "01P" || a.Model.Model != "gpt-x" || a.Autonomy != model.AutonomyAskAll {
		t.Fatalf("应按名称解析供应商并使用默认模型，得到 %+v", a)
	}
	h = newHarness(t, "", nil)
	if code := h.run("agent", "create", "--provider", "x"); code != exitUsage || !strings.Contains(h.errOut.String(), "--name") {
		t.Fatalf("缺少 --name 应返回 2，得到 %d %q", code, h.errOut.String())
	}
}

func TestProviderAddKeySources(t *testing.T) {
	// 环境变量优先于交互输入；密钥不出现在输出里。
	h := newHarness(t, "", map[string]string{envAPIKey: "sk-from-env"})
	if code := h.run("provider", "add", "--name", "x", "--kind", "openai_compatible", "--base-url", "http://127.0.0.1:1/v1"); code != exitOK {
		t.Fatalf("退出码 %d：%s", code, h.errOut.String())
	}
	if h.api.created[0].APIKey != "sk-from-env" {
		t.Fatalf("应使用环境变量中的密钥，得到 %+v", h.api.created[0])
	}
	if strings.Contains(h.out.String(), "sk-from-env") {
		t.Fatal("输出不应包含密钥")
	}

	// 终端上交互输入（测试里用 stdinTTY 模拟，回显关闭由 disableEcho 负责）。
	h = newHarness(t, "sk-typed\n", nil)
	h.c.stdinTTY = true
	if code := h.run("provider", "add", "--name", "y"); code != exitOK {
		t.Fatalf("退出码 %d：%s", code, h.errOut.String())
	}
	if h.api.created[0].APIKey != "sk-typed" {
		t.Fatalf("应读取终端输入的密钥，得到 %+v", h.api.created[0])
	}
	if strings.Contains(h.out.String(), "sk-typed") {
		t.Fatal("输出不应回显密钥")
	}

	// 非终端且没有环境变量：不阻塞等待输入，按无密钥创建。
	h = newHarness(t, "", nil)
	if code := h.run("provider", "add", "--name", "ollama", "--preset", "ollama"); code != exitOK {
		t.Fatalf("退出码 %d：%s", code, h.errOut.String())
	}
	if in := h.api.created[0]; in.APIKey != "" || in.Kind != model.ProviderOllama || in.BaseURL == "" {
		t.Fatalf("预设应填充类型与地址，得到 %+v", in)
	}
}

func TestApprovalsDecideValidation(t *testing.T) {
	h := newHarness(t, "", nil)
	if code := h.run("approvals", "decide", "ap1"); code != exitUsage {
		t.Fatalf("缺少 --approve/--deny 应返回 2，得到 %d", code)
	}
	if code := h.run("approvals", "decide", "ap1", "--approve", "--deny"); code != exitUsage {
		t.Fatalf("同时给出 --approve 与 --deny 应返回 2，得到 %d", code)
	}
	if code := h.run("approvals", "decide", "ap1", "--approve", "--scope", "forever"); code != exitUsage {
		t.Fatalf("非法 scope 应返回 2，得到 %d", code)
	}
	if code := h.run("approvals", "decide", "--deny", "ap1", "--reason", "不行"); code != exitOK {
		t.Fatalf("退出码 %d：%s", code, h.errOut.String())
	}
	if d := h.api.decisions[0]; d.Approve || d.Scope != model.ScopeOnce || d.Reason != "不行" {
		t.Fatalf("决定字段错误: %+v", d)
	}
	if !strings.Contains(h.out.String(), "denied") {
		t.Fatalf("应输出审批结果，得到 %q", h.out.String())
	}
}

func TestSettingsSetParsesValues(t *testing.T) {
	h := newHarness(t, "", nil)
	if code := h.run("settings", "set", "timezone", "Asia/Shanghai"); code != exitOK {
		t.Fatalf("退出码 %d：%s", code, h.errOut.String())
	}
	if code := h.run("settings", "set", "allowed_origins", "http://localhost:5173, http://127.0.0.1:5173"); code != exitOK {
		t.Fatal(code)
	}
	if h.api.settings["timezone"] != "Asia/Shanghai" {
		t.Fatalf("时区应按字符串发送，得到 %#v", h.api.settings["timezone"])
	}
	if got, ok := h.api.settings["allowed_origins"].([]string); !ok || len(got) != 2 {
		t.Fatalf("allowed_origins 应按逗号拆成列表，得到 %#v", h.api.settings["allowed_origins"])
	}
	h.out.Reset()
	if code := h.run("settings", "get", "timezone"); code != exitOK || strings.TrimSpace(h.out.String()) != "Asia/Shanghai" {
		t.Fatalf("settings get key 应输出值，得到 %q", h.out.String())
	}
}

func TestJobChannelAndMCPCommands(t *testing.T) {
	h := newHarness(t, "", map[string]string{envChannelSecret: "bot-token"})
	h.api.agents = []model.Agent{{ID: "01A", Name: "助理"}}
	if code := h.run("job", "create", "--agent", "助理", "--name", "日报", "--schedule", "0 9 * * *", "--kind", "push_text", "--input", "早上好", "--disabled"); code != exitOK {
		t.Fatalf("退出码 %d：%s", code, h.errOut.String())
	}
	if j := h.api.jobs[0]; j.AgentID != "01A" || j.Kind != model.JobPushText || j.Enabled == nil || *j.Enabled {
		t.Fatalf("任务输入错误: %+v", j)
	}
	if code := h.run("channel", "add", "--agent", "01A", "--kind", "telegram", "--name", "tg", "--config", `{"group_mode":"all"}`); code != exitOK {
		t.Fatalf("退出码 %d：%s", code, h.errOut.String())
	}
	if ch := h.api.channels[0]; ch.Secret != "bot-token" || string(ch.Config) != `{"group_mode":"all"}` {
		t.Fatalf("通道输入错误: %+v", ch)
	}
	if code := h.run("channel", "add", "--agent", "01A", "--kind", "telegram", "--name", "tg", "--config", `{bad`); code != exitUsage {
		t.Fatalf("非法 JSON 配置应返回 2，得到 %d", code)
	}
	if code := h.run("extension", "mcp", "add", "--name", "fs", "--command", "npx", "--arg", "-y", "--arg", "server", "--env", "TOKEN=secret:fs-token"); code != exitOK {
		t.Fatalf("退出码 %d：%s", code, h.errOut.String())
	}
	if cfg := h.api.mcp[0]; cfg.Transport != model.MCPStdio || len(cfg.Args) != 2 || cfg.Env["TOKEN"] != "secret:fs-token" {
		t.Fatalf("MCP 配置错误: %+v", cfg)
	}
	if code := h.run("extension", "mcp", "add", "--name", "x", "--command", "a", "--url", "https://x"); code != exitUsage {
		t.Fatalf("--command 与 --url 互斥，得到 %d", code)
	}
}

// fakeDaemon 是可编程的守护进程控制器。
type fakeDaemon struct {
	mu         sync.Mutex
	bin        string
	binErr     error
	alivePIDs  map[int]bool
	healthErr  error
	terminated []int
	spawned    [][]string
	onSpawn    func() (int, chan struct{})
	// identifyErr 是 identify 的结果；shutdownErr 是 shutdown 的结果。
	identifyErr error
	shutdownErr error
	// exitOnShutdown 为 true 时 shutdown 成功后进程随即退出（模拟优雅关闭）。
	exitOnShutdown bool
	identified     int
	shutdowns      int
}

func (f *fakeDaemon) binary() (string, error) { return f.bin, f.binErr }
func (f *fakeDaemon) spawn(_ string, args []string) (int, <-chan struct{}, func() string, error) {
	f.spawned = append(f.spawned, args)
	pid, exited := 4242, make(chan struct{})
	if f.onSpawn != nil {
		pid, exited = f.onSpawn()
	}
	return pid, exited, func() string { return "exit status 3" }, nil
}
func (f *fakeDaemon) foreground(string, []string) (int, error) { return 0, nil }
func (f *fakeDaemon) terminate(pid int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.terminated = append(f.terminated, pid)
	delete(f.alivePIDs, pid)
	return nil
}
func (f *fakeDaemon) alive(pid int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.alivePIDs[pid]
}
func (f *fakeDaemon) health(context.Context, string) error { return f.healthErr }
func (f *fakeDaemon) identify(context.Context, identity.Handle) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.identified++
	return f.identifyErr
}
func (f *fakeDaemon) shutdown(_ context.Context, h identity.Handle) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shutdowns++
	if f.shutdownErr == nil && f.exitOnShutdown {
		delete(f.alivePIDs, h.PID)
	}
	return f.shutdownErr
}

func TestDaemonStatusAndStopWithoutHandle(t *testing.T) {
	h := newHarness(t, "", nil)
	if code := h.run("daemon", "status"); code != exitNoDaemon || !strings.Contains(h.out.String(), "未运行") {
		t.Fatalf("没有 daemon.json 时 status 应返回 3，得到 %d %q", code, h.out.String())
	}
	h.out.Reset()
	if code := h.run("daemon", "stop"); code != exitOK || !strings.Contains(h.out.String(), "未运行") {
		t.Fatalf("未运行时 stop 应幂等成功，得到 %d %q", code, h.out.String())
	}
}

func TestDaemonStartReportsEarlyExit(t *testing.T) {
	h := newHarness(t, "", nil)
	fd := h.c.daemonCtl.(*fakeDaemon)
	fd.bin = "/bin/gogoslingd"
	fd.onSpawn = func() (int, chan struct{}) {
		ch := make(chan struct{})
		close(ch) // 立刻退出（例如锁冲突）
		return 4242, ch
	}
	code := h.run("daemon", "start")
	if code != exitFailure || !strings.Contains(h.errOut.String(), "exit status 3") {
		t.Fatalf("守护进程立即退出应报告退出状态，得到 %d %q", code, h.errOut.String())
	}
	if len(fd.spawned) != 1 || fd.spawned[0][0] != "-home" || fd.spawned[0][1] != h.c.home {
		t.Fatalf("应以 -home 传递数据目录，得到 %v", fd.spawned)
	}

	fd.binErr = errors.New("not found")
	h.errOut.Reset()
	if code := h.run("daemon", "start"); code != exitFailure || !strings.Contains(h.errOut.String(), "找不到 gogoslingd") {
		t.Fatalf("找不到二进制应提示，得到 %d %q", code, h.errOut.String())
	}
}

func TestVersionWithoutDaemon(t *testing.T) {
	h := newHarness(t, "", nil)
	h.c.dial = func(context.Context, string, string) (api, error) { return nil, errDaemonNotRunning }
	if code := h.run("version"); code != exitOK || !strings.Contains(h.out.String(), "CLI") || !strings.Contains(h.errOut.String(), "守护进程未运行") {
		t.Fatalf("守护进程未运行时 version 仍应打印 CLI 版本，得到 %d %q %q", code, h.out.String(), h.errOut.String())
	}
	h = newHarness(t, "", nil)
	if code := h.run("version"); code != exitOK || !strings.Contains(h.out.String(), "1.2.3") || !strings.Contains(h.out.String(), "ready") {
		t.Fatalf("应打印守护进程与工作进程版本，得到 %q", h.out.String())
	}
}

func TestEnvLocale(t *testing.T) {
	cases := map[string]string{"zh_CN.UTF-8": "zh", "en_US.UTF-8": "en", "C": "en", "": "en", "zh_TW": "zh"}
	for v, want := range cases {
		got := envLocale(func(k string) string {
			if k == "LANG" {
				return v
			}
			return ""
		})
		if got != want {
			t.Errorf("LANG=%q → %s，期望 %s", v, got, want)
		}
	}
	_ = time.Second
}

// C17：extension status / accept-tools。
func TestExtensionStatusAndAcceptTools(t *testing.T) {
	h := newHarness(t, "", nil)
	if code := h.run("extension", "status", "ext1"); code != exitOK {
		t.Fatalf("status 退出码 %d：%s", code, h.errOut.String())
	}
	out := h.out.String()
	for _, want := range []string{"tools_changed", "tool_count", "3", "connected"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status 输出应包含 %q，得到:\n%s", want, out)
		}
	}
	h.out.Reset()
	if code := h.run("extension", "accept-tools", "ext1", "--json"); code != exitOK {
		t.Fatalf("accept-tools 退出码 %d：%s", code, h.errOut.String())
	}
	var st extensionStatus
	if err := json.Unmarshal(h.out.Bytes(), &st); err != nil || st.ID != "ext1" || st.ToolsChanged {
		t.Fatalf("accept-tools 后 tools_changed 应为 false，得到 %q (%v)", h.out.String(), err)
	}
	if !h.api.accepted {
		t.Fatal("accept-tools 应调用 AcceptExtensionTools")
	}
	if code := h.run("extension", "status"); code != exitUsage {
		t.Fatalf("缺少扩展 ID 应为参数错误，得到 %d", code)
	}
	if code := h.run("extension", "status", "nope"); code != exitFailure {
		t.Fatalf("不存在的扩展应失败，得到 %d", code)
	}
}

// XP-06：真实的 identify 用句柄里的令牌访问 /api/v1/version 并比对 started_at。
func TestSystemControllerIdentifyAndShutdown(t *testing.T) {
	startedAt := time.Date(2026, 10, 10, 8, 0, 0, 0, time.UTC)
	var shutdowns int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"no"}}`))
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/version":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"version":"v","started_at":"2026-10-10T08:00:00Z","worker":{"status":"ready"}}`))
		case "POST /api/v1/system/shutdown":
			mu.Lock()
			shutdowns++
			mu.Unlock()
			w.WriteHeader(http.StatusAccepted)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	ctl := systemDaemonController{}
	ctx := context.Background()
	h := identity.Handle{PID: 1, Endpoint: srv.URL, Token: "tok", StartedAt: startedAt}
	if err := ctl.identify(ctx, h); err != nil {
		t.Fatalf("令牌与 started_at 一致时应确认身份: %v", err)
	}
	other := h
	other.StartedAt = startedAt.Add(time.Hour)
	if err := ctl.identify(ctx, other); !errors.Is(err, errHandleMismatch) {
		t.Fatalf("started_at 不一致应返回 errHandleMismatch，实际 %v", err)
	}
	bad := h
	bad.Token = "stale"
	if err := ctl.identify(ctx, bad); !model.IsCode(err, model.CodeUnauthorized) {
		t.Fatalf("令牌被拒应返回 unauthorized，实际 %v", err)
	}
	if err := ctl.shutdown(ctx, h); err != nil {
		t.Fatalf("202 应视为成功: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if shutdowns != 1 {
		t.Fatalf("应发出一次关闭请求，实际 %d", shutdowns)
	}
}
