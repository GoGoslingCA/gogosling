package extension

// 本文件提供测试辅助：内存版 ExtensionRepo、日志缓冲、技能 / 插件目录生成，
// 以及"测试二进制自我重执行充当插件"的 TestMain。

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/event"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

const testPluginArg = "gogosling-test-plugin"

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == testPluginArg {
		os.Exit(runTestPlugin())
	}
	os.Exit(m.Run())
}

// runTestPlugin 实现插件子进程协议：读一行请求，按工具名回应。
func runTestPlugin() int {
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		fmt.Fprintln(os.Stderr, "test plugin: no request")
		return 2
	}
	var req struct {
		Tool      string          `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(line), &req); err != nil {
		fmt.Fprintln(os.Stderr, "test plugin: bad request:", err)
		return 2
	}
	var args map[string]any
	_ = json.Unmarshal(req.Arguments, &args)
	respond := func(content any, isErr bool) int {
		out, _ := json.Marshal(map[string]any{"content": content, "is_error": isErr})
		fmt.Println(string(out))
		return 0
	}
	switch req.Tool {
	case "echo":
		text, _ := args["text"].(string)
		return respond("echo:"+text, false)
	case "fail":
		return respond("bad input", true)
	case "sleep":
		ms, _ := args["millis"].(float64)
		time.Sleep(time.Duration(ms) * time.Millisecond)
		return respond("slept", false)
	case "big":
		n, _ := args["bytes"].(float64)
		return respond(strings.Repeat("x", int(n)), false)
	case "env":
		name, _ := args["name"].(string)
		v, ok := os.LookupEnv(name)
		if !ok {
			return respond("<unset>", false)
		}
		return respond(v, false)
	case "json":
		return respond(map[string]any{"a": 1, "b": []int{1, 2}}, false)
	case "garbage":
		fmt.Println("this is not json")
		return 0
	case "silent":
		return 0
	case "crash":
		fmt.Fprintln(os.Stderr, "plugin exploded")
		return 3
	case "cwd":
		wd, _ := os.Getwd()
		return respond(wd, false)
	case "multiline":
		fmt.Println(`{"content":"first line","is_error":false}`)
		fmt.Println("trailing noise that must be ignored")
		return 0
	}
	return respond("unknown tool "+req.Tool, true)
}

// fakeRepo 是内存版 model.ExtensionRepo，返回副本以模拟真实仓储。
type fakeRepo struct {
	mu    sync.Mutex
	items map[string]*model.Extension
	fail  map[string]error // 方法名 → 注入的错误
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{items: map[string]*model.Extension{}, fail: map[string]error{}}
}

func cloneExt(e *model.Extension) *model.Extension {
	c := *e
	c.Manifest = append(json.RawMessage(nil), e.Manifest...)
	if e.MCP != nil {
		mc := *e.MCP
		mc.Args = append([]string(nil), e.MCP.Args...)
		if e.MCP.Env != nil {
			mc.Env = map[string]string{}
			for k, v := range e.MCP.Env {
				mc.Env[k] = v
			}
		}
		if e.MCP.Headers != nil {
			mc.Headers = map[string]string{}
			for k, v := range e.MCP.Headers {
				mc.Headers[k] = v
			}
		}
		c.MCP = &mc
	}
	return &c
}

func (r *fakeRepo) CreateExtension(_ context.Context, e *model.Extension) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.fail["create"]; err != nil {
		return err
	}
	if _, ok := r.items[e.ID]; ok {
		return model.NewError(model.CodeConflict, "重复的扩展 ID")
	}
	r.items[e.ID] = cloneExt(e)
	return nil
}

func (r *fakeRepo) GetExtension(_ context.Context, id string) (*model.Extension, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.items[id]
	if !ok {
		return nil, model.NotFound("extension", id)
	}
	return cloneExt(e), nil
}

func (r *fakeRepo) ListExtensions(_ context.Context, kind model.ExtensionKind) ([]*model.Extension, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*model.Extension
	for _, e := range r.items {
		if kind == "" || e.Kind == kind {
			out = append(out, cloneExt(e))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].InstalledAt.Equal(out[j].InstalledAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].InstalledAt.Before(out[j].InstalledAt)
	})
	return out, nil
}

func (r *fakeRepo) UpdateExtension(_ context.Context, e *model.Extension) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.fail["update"]; err != nil {
		return err
	}
	if _, ok := r.items[e.ID]; !ok {
		return model.NotFound("extension", e.ID)
	}
	r.items[e.ID] = cloneExt(e)
	return nil
}

func (r *fakeRepo) DeleteExtension(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.items[id]; !ok {
		return model.NotFound("extension", id)
	}
	delete(r.items, id)
	return nil
}

type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// testEnv 是一次测试的全部依赖。
type testEnv struct {
	m       *Manager
	repo    *fakeRepo
	dataDir string
	bus     *event.Bus
	logs    *logBuffer
	dials   int
	dialMu  sync.Mutex
	clients []*fakeMCP
}

// fakeMCP 是内存版 MCPClient。
type fakeMCP struct {
	mu     sync.Mutex
	specs  []model.ToolSpec
	done   chan struct{}
	closed bool
	calls  []string
	callFn func(name string, args json.RawMessage) (model.ToolResult, error)
}

func newFakeMCP(specs []model.ToolSpec) *fakeMCP {
	return &fakeMCP{specs: specs, done: make(chan struct{})}
}

func (f *fakeMCP) ListToolsPinned(_ context.Context, pinned string) ([]model.ToolSpec, string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	digest := toolsDigestForTest(f.specs)
	return append([]model.ToolSpec(nil), f.specs...), digest, pinned != "" && pinned != digest, nil
}

func (f *fakeMCP) CallTool(_ context.Context, name string, args json.RawMessage) (model.ToolResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, name)
	fn := f.callFn
	f.mu.Unlock()
	if fn != nil {
		return fn(name, args)
	}
	return model.ToolResult{Content: "mcp:" + name + ":" + string(args)}, nil
}

func (f *fakeMCP) Done() <-chan struct{} { return f.done }

func (f *fakeMCP) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.closed = true
		close(f.done)
	}
	return nil
}

// disconnect 模拟服务端断开。
func (f *fakeMCP) disconnect() { _ = f.Close() }

func (f *fakeMCP) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

func (f *fakeMCP) setSpecs(specs []model.ToolSpec) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.specs = specs
}

func newTestEnv(t *testing.T, opts ...Option) *testEnv {
	t.Helper()
	env := &testEnv{
		repo:    newFakeRepo(),
		dataDir: t.TempDir(),
		bus:     event.NewBus(256),
		logs:    &logBuffer{},
	}
	logger := slog.New(slog.NewTextHandler(env.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	dial := func(_ context.Context, ext *model.Extension) (MCPClient, error) {
		env.dialMu.Lock()
		defer env.dialMu.Unlock()
		env.dials++
		specs := []model.ToolSpec{
			{Name: "search", Description: "Search the web", InputSchema: json.RawMessage(`{"type":"object"}`), Source: model.ToolSource("mcp:" + ext.ID), Risk: model.RiskNetwork},
			{Name: "fetch page", Description: "Fetch a page", InputSchema: json.RawMessage(`{"type":"object"}`), Source: model.ToolSource("mcp:" + ext.ID), Risk: model.RiskNetwork},
		}
		c := newFakeMCP(specs)
		env.clients = append(env.clients, c)
		return c, nil
	}
	all := append([]Option{WithBus(env.bus), WithPluginTimeout(5 * time.Second)}, opts...)
	env.m = New(env.repo, env.dataDir, logger, dial, all...)
	t.Cleanup(func() { _ = env.m.Close() })
	return env
}

func (e *testEnv) lastClient() *fakeMCP {
	e.dialMu.Lock()
	defer e.dialMu.Unlock()
	if len(e.clients) == 0 {
		return nil
	}
	return e.clients[len(e.clients)-1]
}

func (e *testEnv) dialCount() int {
	e.dialMu.Lock()
	defer e.dialMu.Unlock()
	return e.dials
}

// writeSkillDir 在临时目录生成一个技能目录。
func writeSkillDir(t *testing.T, name, description, body string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Join(dir, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf("---\nname: %s\ndescription: %s\nversion: 1.0.0\n---\n\n%s\n", name, description, body)
	if err := os.WriteFile(filepath.Join(dir, SkillFile), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "references", "notes.md"), []byte("# notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// testPluginManifest 生成以测试二进制为入口的插件清单。
func testPluginManifest(t *testing.T, name string, perms Permissions, tools ...string) PluginManifest {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("无法定位测试二进制: %v", err)
	}
	m := PluginManifest{
		Name:        name,
		Version:     "0.1.0",
		Kind:        "plugin",
		Entry:       exe,
		Args:        []string{testPluginArg},
		Permissions: perms,
	}
	for _, tool := range tools {
		m.Tools = append(m.Tools, PluginTool{Name: tool, Description: "tool " + tool, InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`)})
	}
	return m
}

// writePluginDir 在临时目录生成插件目录。
func writePluginDir(t *testing.T, m PluginManifest) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), m.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestFile), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func testAgent(ids ...string) *model.Agent {
	return &model.Agent{ID: "agent-1", Name: "测试", Extensions: ids, Enabled: true}
}

func specNames(specs []model.ToolSpec) []string {
	out := make([]string, 0, len(specs))
	for _, s := range specs {
		out = append(out, s.Name)
	}
	return out
}

func skipOnWindows(t *testing.T, why string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip(why)
	}
}

// drainEvents 收集总线上已有的事件类型。
func drainEvents(t *testing.T, bus *event.Bus, since uint64) []string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := bus.Subscribe(ctx, since, 256, nil)
	defer sub.Cancel()
	var types []string
	for {
		select {
		case ev, ok := <-sub.C:
			if !ok {
				return types
			}
			types = append(types, ev.Type)
		case <-time.After(50 * time.Millisecond):
			return types
		}
	}
}
