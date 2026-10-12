package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/agent"
	"github.com/GoGoslingCA/gogosling/internal/config"
	"github.com/GoGoslingCA/gogosling/internal/event"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/secret"
)

// eventually 轮询 cond 直到为真或超时。
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("等待超时：%s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type noteCall struct {
	runID   string
	waiting bool
}

type fakeNoter struct {
	mu    sync.Mutex
	calls []noteCall
	err   error
}

func (f *fakeNoter) NoteApproval(runID string, waiting bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, noteCall{runID, waiting})
	return f.err
}

func (f *fakeNoter) got() []noteCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]noteCall(nil), f.calls...)
}

// C2：只有带 run_id 的 approval.requested / approval.resolved 被交给 session.NoteApproval，按发布顺序。
func TestNoteApprovalsBridgesBusToSession(t *testing.T) {
	bus := event.NewBus(64)
	// NoteApproval 的错误只记日志，不影响后续事件。
	noter := &fakeNoter{err: model.NotFound("run", "x")}
	// 订阅起点必须在启动 goroutine 之前取得（组合根同样如此），否则会漏掉其间发布的事件。
	since := bus.LastSeq()
	l := goLoop(context.Background(), func(ctx context.Context) {
		noteApprovals(ctx, bus, since, noter, discardLogger())
	})
	defer l.halt(context.Background())

	bus.Publish(event.Event{Type: string(model.EventApprovalRequested), RunID: "r1"})
	bus.Publish(event.Event{Type: string(model.EventApprovalRequested)}) // 无 run_id：忽略
	bus.Publish(event.Event{Type: string(model.EventToolCallStarted), RunID: "r1"})
	bus.Publish(event.Event{Type: string(model.EventApprovalResolved), RunID: "r1"})
	bus.Publish(event.Event{Type: string(model.EventApprovalRequested), RunID: "r2"})

	want := []noteCall{{"r1", true}, {"r1", false}, {"r2", true}}
	eventually(t, "三次 NoteApproval", func() bool { return len(noter.got()) >= len(want) })
	if got := noter.got(); !reflect.DeepEqual(got, want) {
		t.Fatalf("NoteApproval 调用 = %v，期望 %v", got, want)
	}
}

type fakeForgetter struct {
	mu  sync.Mutex
	ids []string
	err error
}

func (f *fakeForgetter) ForgetAgent(ctx context.Context, agentID string) error {
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("应带时限")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ids = append(f.ids, agentID)
	return f.err
}

func (f *fakeForgetter) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ids...)
}

type fakeScheduler struct {
	mu  sync.Mutex
	ids []string
}

func (f *fakeScheduler) Forget(agentID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ids = append(f.ids, agentID)
}

func (f *fakeScheduler) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ids...)
}

// C5：agent.deleted 之后调度器与网关都被告知忘掉该智能体；一个模块失败不影响另一个。
func TestForgetDeletedAgents(t *testing.T) {
	bus := event.NewBus(64)
	sched := &fakeScheduler{}
	gw := &fakeForgetter{err: errors.New("适配器停止失败")}
	since := bus.LastSeq()
	l := goLoop(context.Background(), func(ctx context.Context) {
		forgetDeletedAgents(ctx, bus, since, []agentForgetter{gw, schedulerForgetter{s: sched}}, discardLogger())
	})
	defer l.halt(context.Background())

	bus.Publish(event.Event{Type: "agent.updated", AgentID: "a0"})
	bus.Publish(event.Event{Type: agent.EventDeleted, AgentID: "a1", Payload: &model.Agent{ID: "a1"}})
	bus.Publish(event.Event{Type: agent.EventDeleted, Payload: &model.Agent{ID: "a2"}}) // 路由字段缺失时取载荷

	eventually(t, "两个智能体都被清理", func() bool { return len(sched.got()) >= 2 })
	if got := sched.got(); !reflect.DeepEqual(got, []string{"a1", "a2"}) {
		t.Fatalf("调度器 Forget = %v", got)
	}
	if got := gw.got(); !reflect.DeepEqual(got, []string{"a1", "a2"}) {
		t.Fatalf("网关 ForgetAgent = %v", got)
	}
}

type fakeCleaner struct{ ids []string }

func (f *fakeCleaner) DeleteByAgent(_ context.Context, agentID string) error {
	f.ids = append(f.ids, agentID)
	if agentID == "busy" {
		return &model.Error{Code: model.CodeConflict, Message: "有在途运行", Details: map[string]any{"run_id": "r"}}
	}
	return nil
}

func TestSessionCleanerLateBinding(t *testing.T) {
	c := &sessionCleaner{}
	if err := c.DeleteByAgent(context.Background(), "a"); !model.IsCode(err, model.CodeUnavailable) {
		t.Fatalf("绑定之前应返回 unavailable，得到 %v", err)
	}
	inner := &fakeCleaner{}
	c.bind(inner)
	if err := c.DeleteByAgent(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteByAgent(context.Background(), "busy"); !model.IsCode(err, model.CodeConflict) {
		t.Fatalf("会话服务的 conflict 应原样返回（agent.Delete 据此拒绝删除），得到 %v", err)
	}
	if !reflect.DeepEqual(inner.ids, []string{"a", "busy"}) {
		t.Fatalf("应转交给会话服务，得到 %v", inner.ids)
	}
}

// XP-11：组合根按 GOGOSLING_SECRET_BACKEND 显式选择后端，并把选择记进数据目录；未知取值是参数错误。
// （不测 keyring 分支：测试绝不触碰真实的系统凭据存储。）
func TestOpenSecretsHonorsExplicitBackend(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == config.EnvSecretBackend {
				return v
			}
			return ""
		}
	}
	if _, err := openSecrets(ctx, dir, env("vault"), discardLogger()); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("未知后端应 invalid，得到 %v", err)
	}
	s, err := openSecrets(ctx, dir, env(" FILE "), discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if secret.BackendName(s) != secret.BackendFile {
		t.Fatalf("应使用加密文件后端，得到 %s", secret.BackendName(s))
	}
	if rec, err := secret.RecordedBackend(dir); err != nil || rec != secret.BackendFile {
		t.Fatalf("应记录 file 后端，得到 %q / %v", rec, err)
	}

	// 记录损坏且没有显式选择：unavailable，提示如何显式切换。
	if err := os.WriteFile(filepath.Join(dir, secret.BackendFileName), []byte("???"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = openSecrets(ctx, dir, env(""), discardLogger())
	if !model.IsCode(err, model.CodeUnavailable) || !strings.Contains(err.Error(), config.EnvSecretBackend+"=file") {
		t.Fatalf("记录的后端不可用时应返回带切换提示的 unavailable，得到 %v", err)
	}
}

func TestSecretAdapterBackendUnavailableHint(t *testing.T) {
	err := mapSecretErr(errors.Join(secret.ErrBackendUnavailable, errors.New("dbus")), "")
	var me *model.Error
	if !errors.As(err, &me) || me.Code != model.CodeUnavailable || me.Details["env"] != config.EnvSecretBackend {
		t.Fatalf("应映射为带环境变量提示的 unavailable，得到 %#v", err)
	}
	if _, has := me.Details["secret"]; has {
		t.Error("没有凭据名时 Details 不应带空的 secret 字段")
	}
	if !errors.Is(err, secret.ErrBackendUnavailable) {
		t.Error("映射后仍应能用 errors.Is 匹配原哨兵错误")
	}
}
