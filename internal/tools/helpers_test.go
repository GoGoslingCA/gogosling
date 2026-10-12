package tools

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// fakeWorkspace 是基于临时目录的 Workspace 实现，行为对齐 workspace 包：
// 相对路径、拒绝绝对路径与 ".."、CodeNotFound / CodeInvalid 的映射。
type fakeWorkspace struct {
	root string
}

func newFakeWorkspace(t *testing.T) *fakeWorkspace {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &fakeWorkspace{root: root}
}

func (w *fakeWorkspace) Root() string { return w.root }

func (w *fakeWorkspace) clean(rel string) (string, error) {
	norm := strings.ReplaceAll(rel, "\\", "/")
	if norm == "" || norm == "." {
		return ".", nil
	}
	if strings.HasPrefix(norm, "/") || filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" {
		return "", model.Errorf(model.CodeForbidden, "不允许使用绝对路径 %q", rel)
	}
	for _, seg := range strings.Split(norm, "/") {
		if seg == ".." {
			return "", model.Errorf(model.CodeForbidden, "路径 %q 越出工作区", rel)
		}
	}
	return filepath.Clean(filepath.FromSlash(norm)), nil
}

func (w *fakeWorkspace) Resolve(rel string) (string, error) {
	c, err := w.clean(rel)
	if err != nil {
		return "", err
	}
	if c == "." {
		return w.root, nil
	}
	return filepath.Join(w.root, c), nil
}

func (w *fakeWorkspace) ReadFile(_ context.Context, rel string, maxBytes int64) ([]byte, error) {
	abs, err := w.Resolve(rel)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fsErr(rel, err)
	}
	if info.IsDir() {
		return nil, model.Errorf(model.CodeInvalid, "%s 是目录", rel)
	}
	if maxBytes > 0 && info.Size() > maxBytes {
		return nil, model.Errorf(model.CodeInvalid, "%s 超过大小限制", rel)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fsErr(rel, err)
	}
	return data, nil
}

func (w *fakeWorkspace) WriteFile(_ context.Context, rel string, data []byte) error {
	abs, err := w.Resolve(rel)
	if err != nil {
		return err
	}
	if abs == w.root {
		return model.NewError(model.CodeInvalid, "不能把工作区根目录当作文件写入")
	}
	if info, err := os.Stat(abs); err == nil && info.IsDir() {
		return model.Errorf(model.CodeInvalid, "%s 是目录", rel)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return model.Wrap(model.CodeInternal, "创建父目录失败", err)
	}
	return os.WriteFile(abs, data, 0o600)
}

func (w *fakeWorkspace) List(_ context.Context, rel string) ([]Entry, error) {
	abs, err := w.Resolve(rel)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fsErr(rel, err)
	}
	if !info.IsDir() {
		return nil, model.Errorf(model.CodeInvalid, "%s 不是目录", rel)
	}
	dirents, err := os.ReadDir(abs)
	if err != nil {
		return nil, fsErr(rel, err)
	}
	out := make([]Entry, 0, len(dirents))
	for _, d := range dirents {
		fi, err := d.Info()
		if err != nil {
			continue
		}
		e := Entry{Name: fi.Name(), Path: fi.Name(), IsDir: fi.IsDir(), ModTime: fi.ModTime()}
		if !fi.IsDir() {
			e.Size = fi.Size()
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (w *fakeWorkspace) Stat(_ context.Context, rel string) (Entry, error) {
	c, err := w.clean(rel)
	if err != nil {
		return Entry{}, err
	}
	abs, _ := w.Resolve(rel)
	fi, err := os.Stat(abs)
	if err != nil {
		return Entry{}, fsErr(rel, err)
	}
	e := Entry{Name: fi.Name(), Path: filepath.ToSlash(c), IsDir: fi.IsDir(), ModTime: fi.ModTime()}
	if !fi.IsDir() {
		e.Size = fi.Size()
	}
	return e, nil
}

func fsErr(rel string, err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return model.NotFound("file", rel)
	}
	return model.Wrap(model.CodeInternal, "文件操作失败: "+rel, err)
}

// mustWrite 在工作区内写一个文件（创建父目录）。
func mustWrite(t *testing.T, ws *fakeWorkspace, rel, content string) {
	t.Helper()
	if err := ws.WriteFile(context.Background(), rel, []byte(content)); err != nil {
		t.Fatal(err)
	}
}

// fakeChecker 记录收到的请求并按配置返回结果。
type fakeChecker struct {
	mu      sync.Mutex
	reqs    []CheckRequest
	verdict Verdict
	err     error
}

func (c *fakeChecker) Check(_ context.Context, req CheckRequest) (Verdict, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reqs = append(c.reqs, req)
	if c.err != nil {
		return Verdict{}, c.err
	}
	return c.verdict, nil
}

func (c *fakeChecker) last(t *testing.T) CheckRequest {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.reqs) == 0 {
		t.Fatal("权限检查没有被调用")
	}
	return c.reqs[len(c.reqs)-1]
}

func allowAll() *fakeChecker { return &fakeChecker{verdict: Verdict{Allowed: true}} }

// fakeResolver 对任意 runID 返回固定的上下文，或返回错误。
type fakeResolver struct {
	rc  RunContext
	err error
}

func (r fakeResolver) ResolveRunContext(_ context.Context, runID string) (RunContext, error) {
	if r.err != nil {
		return RunContext{}, r.err
	}
	rc := r.rc
	rc.RunID = runID
	return rc, nil
}

// fakeExternal 是可配置的 ExternalTools。
type fakeExternal struct {
	specs []model.ToolSpec
	err   error
	calls []model.ToolCall
	res   model.ToolResult
}

func (f *fakeExternal) ToolsFor(_ context.Context, _ *model.Agent) ([]model.ToolSpec, error) {
	return f.specs, f.err
}

func (f *fakeExternal) CallTool(_ context.Context, _ *model.Agent, call model.ToolCall) (model.ToolResult, error) {
	f.calls = append(f.calls, call)
	return f.res, nil
}

// stubTool 是行为可编程的 Tool。
type stubTool struct {
	name    string
	risk    model.ToolRisk
	execute func(ctx context.Context, rc RunContext, args json.RawMessage) (model.ToolResult, error)
	calls   int
}

func (s *stubTool) Spec() model.ToolSpec {
	risk := s.risk
	if risk == "" {
		risk = model.RiskRead
	}
	return model.ToolSpec{
		Name:        s.name,
		Description: "stub",
		InputSchema: objectSchema(map[string]any{"x": stringProp("x")}),
		Source:      SourceBuiltin,
		Risk:        risk,
	}
}

func (s *stubTool) Execute(ctx context.Context, rc RunContext, args json.RawMessage) (model.ToolResult, error) {
	s.calls++
	if s.execute == nil {
		return model.ToolResult{Content: "ok"}, nil
	}
	return s.execute(ctx, rc, args)
}

func testAgent(tools []string) *model.Agent {
	return &model.Agent{ID: "01AGENT", Name: "测试", Tools: tools, Autonomy: model.AutonomyAskRisky, Enabled: true}
}

func testRunContext(t *testing.T, ws *fakeWorkspace, agent *model.Agent) RunContext {
	t.Helper()
	if agent == nil {
		agent = testAgent(nil)
	}
	return RunContext{
		RunID:     "01RUN",
		SessionID: "01SESSION",
		Agent:     agent,
		Workspace: ws,
		Principal: model.Principal{ID: "u1", Kind: model.PrincipalLocalUser, Roles: []string{model.RoleAdmin}},
	}
}

func rawArgs(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
