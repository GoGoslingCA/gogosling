package engine

// 本文件覆盖审查修复的回归：反向请求的收尾等待（CONC-4）、接受前取消（CONC-3）、
// 运行请求校验（C15）、失败原因的机器常量（C19）与默认语言。

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// lingeringExecutor 模拟"被取消后还要花一段时间收尾"的工具执行器（例如审批过期后写审计、更新数据库）。
type lingeringExecutor struct {
	started  chan struct{}
	once     sync.Once
	linger   time.Duration
	finished atomic.Bool
}

func newLingeringExecutor(linger time.Duration) *lingeringExecutor {
	return &lingeringExecutor{started: make(chan struct{}), linger: linger}
}

func (e *lingeringExecutor) Execute(ctx context.Context, _ string, _ model.ToolCall) (model.ToolResult, error) {
	e.once.Do(func() { close(e.started) })
	<-ctx.Done()
	time.Sleep(e.linger)
	e.finished.Store(true)
	return model.ToolResult{}, model.Wrap(model.CodeCancelled, "等待审批时被取消", ctx.Err())
}

func startToolRunAndWait(t *testing.T, s *Supervisor, exec *lingeringExecutor) {
	t.Helper()
	waitReady(t, s)
	col := newCollector()
	if err := s.StartRun(context.Background(), sampleRequest("run_linger", readFileTool), col.sink); err != nil {
		t.Fatalf("StartRun 失败: %v", err)
	}
	select {
	case <-exec.started:
	case <-time.After(10 * time.Second):
		t.Fatal("执行器迟迟没有收到工具调用")
	}
}

func TestStopWaitsForInflightToolCalls(t *testing.T) {
	exec := newLingeringExecutor(300 * time.Millisecond)
	s, _ := newTestSupervisor(t, fakeModeNormal, exec, nil)
	startToolRunAndWait(t, s, exec)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := s.Stop(ctx); err != nil {
		t.Fatalf("Stop 失败: %v", err)
	}
	if !exec.finished.Load() {
		t.Error("Stop 返回时工具执行仍在进行；期望 Stop 等反向请求收尾后再返回，之后才能安全关闭扩展与数据库")
	}
}

func TestStopBoundsWaitForStuckToolCalls(t *testing.T) {
	exec := newLingeringExecutor(5 * time.Second)
	s, lb := newTestSupervisor(t, fakeModeNormal, exec, func(c *Config) { c.inflightWait = 200 * time.Millisecond })
	startToolRunAndWait(t, s, exec)

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := s.Stop(ctx); err != nil {
		t.Fatalf("Stop 失败: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Errorf("Stop 耗时 %v；卡住的工具调用不应让停止无限期等待", elapsed)
	}
	if exec.finished.Load() {
		t.Error("执行器不应已经结束（它要 5 秒才收尾），测试前提不成立")
	}
	if !strings.Contains(lb.String(), "反向请求在宽限期内没有结束") {
		t.Errorf("等待超时应记录日志:\n%s", lb.String())
	}
}

func TestWaitInflightAfterClose(t *testing.T) {
	c := newConn(strings.NewReader(""), io.Discard, 1024, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.inflight.Add(1)
	c.closeWith(errors.New("测试关闭"))
	if c.waitInflight(50 * time.Millisecond) {
		t.Error("还有未结束的反向请求时 waitInflight 应超时返回 false")
	}
	c.inflight.Done()
	if !c.waitInflight(time.Second) {
		t.Error("反向请求结束后 waitInflight 应返回 true")
	}
	// 关闭之后到达的反向请求不再登记，否则 Wait 与 Add 会竞争。
	c.dispatch(&wireMessage{JSONRPC: jsonrpcVersion, ID: []byte("7"), Method: methodHostToolCall})
	if !c.waitInflight(50 * time.Millisecond) {
		t.Error("连接关闭后到达的反向请求不应被登记")
	}
}

func TestCancelBeforeAcceptMakesStartRunReturnCancelled(t *testing.T) {
	s, _ := newTestSupervisor(t, fakeModeSlowAccept, nil, func(c *Config) { c.CancelGrace = 50 * time.Millisecond })
	waitReady(t, s)

	col := newCollector()
	errCh := make(chan error, 1)
	go func() { errCh <- s.StartRun(context.Background(), sampleRequest("run_early_cancel"), col.sink) }()
	eventually(t, 5*time.Second, "运行已登记到在途表", func() bool { return s.Status().ActiveRuns == 1 })
	if err := s.CancelRun(context.Background(), "run_early_cancel"); err != nil {
		t.Fatalf("CancelRun 失败: %v", err)
	}

	var err error
	select {
	case err = <-errCh:
	case <-time.After(10 * time.Second):
		t.Fatal("StartRun 迟迟没有返回")
	}
	if !model.IsCode(err, model.CodeCancelled) {
		t.Fatalf("兜底收尾在接受前移除了运行，StartRun 应返回 cancelled，得到 %v", err)
	}
	var me *model.Error
	if !errors.As(err, &me) || me.Message != ErrorRunCancelled {
		t.Errorf("Message = %v，期望 %q", err, ErrorRunCancelled)
	}
	// 给可能的迟到事件一点时间：StartRun 返回错误后 sink 不得再收到任何事件。
	time.Sleep(fakeAcceptDelay)
	if evs := col.snapshot(); len(evs) != 0 {
		t.Errorf("StartRun 返回错误后 sink 仍收到事件: %v", col.types())
	}
	if n := s.Status().ActiveRuns; n != 0 {
		t.Errorf("在途运行数 = %d，期望 0", n)
	}
}

func TestTerminalEventBeforeAcceptIsDeliveredAndStartRunSucceeds(t *testing.T) {
	s, _ := newTestSupervisor(t, fakeModeEarlyTerminal, nil, nil)
	waitReady(t, s)

	col := newCollector()
	if err := s.StartRun(context.Background(), sampleRequest("run_early_done"), col.sink); err != nil {
		t.Fatalf("终态事件已投递时 StartRun 应返回 nil（保证恰好一个终态），得到 %v", err)
	}
	events := col.waitTerminal(t, 5*time.Second)
	if last := events[len(events)-1]; last.Type != model.EventRunCompleted {
		t.Errorf("终态 = %s，期望 run.completed", last.Type)
	}
	if n := s.Status().ActiveRuns; n != 0 {
		t.Errorf("在途运行数 = %d，期望 0", n)
	}
}

func TestStartRunRejectsOpenAICompatibleWithoutBaseURL(t *testing.T) {
	// 校验先于就绪检查：未启动的 Supervisor 也应给出 invalid 而不是 unavailable。
	s := New(Config{Command: fakeCommand(fakeModeNormal)})
	for _, base := range []string{"", "   "} {
		req := sampleRequest("run_nobase")
		req.Credentials.BaseURL = base
		err := s.StartRun(context.Background(), req, func(model.RunEvent) {})
		if !model.IsCode(err, model.CodeInvalid) {
			t.Errorf("base_url=%q 时错误码 = %v，期望 invalid（不得静默回退到默认服务商）", base, err)
		}
		var me *model.Error
		if !errors.As(err, &me) || me.Message != ErrorInvalidRequest {
			t.Errorf("Message = %v，期望 %q", err, ErrorInvalidRequest)
		}
	}
	// 其他供应商类型不受影响（由工作进程使用其官方默认地址）。
	req := sampleRequest("run_anthropic")
	req.Credentials = model.ProviderCredentials{Kind: model.ProviderAnthropic, APIKey: "k", Model: "m"}
	if err := s.StartRun(context.Background(), req, func(model.RunEvent) {}); !model.IsCode(err, model.CodeUnavailable) {
		t.Errorf("anthropic 无 base_url 应通过校验（随后因未启动返回 unavailable），得到 %v", err)
	}
}

func TestCrashDetailsGoToLogNotRunError(t *testing.T) {
	s, lb := newTestSupervisor(t, fakeModeCrashOnRun, nil, nil)
	waitReady(t, s)
	col := newCollector()
	if err := s.StartRun(context.Background(), sampleRequest("run_crash_log"), col.sink); err != nil {
		t.Fatalf("StartRun 失败: %v", err)
	}
	events := col.waitTerminal(t, 10*time.Second)
	if last := events[len(events)-1]; last.Error != ErrorWorkerCrashed {
		t.Errorf("run.failed.error = %q，期望 %q", last.Error, ErrorWorkerCrashed)
	}
	eventually(t, 5*time.Second, "日志里记录退出码等细节", func() bool {
		return strings.Contains(lb.String(), "exit status 3")
	})
}

func TestStartErrorMapping(t *testing.T) {
	cases := []struct {
		err  error
		code model.Code
		msg  string
	}{
		{&RPCError{Code: codeInvalidParams, Message: "bad"}, model.CodeInvalid, ErrorRunRejected},
		{&RPCError{Code: codeProviderFailed, Message: "no base_url"}, model.CodeUnavailable, ErrorRunRejected},
		{model.Wrap(model.CodeUnavailable, "工作进程连接已关闭", errWorkerEOF), model.CodeUnavailable, ErrorWorkerUnavailable},
		{ctxError(context.DeadlineExceeded, "等待"), model.CodeTimeout, ErrorWorkerUnavailable},
		{ctxError(context.Canceled, "等待"), model.CodeCancelled, ErrorRunCancelled},
	}
	for _, c := range cases {
		got := startError(c.err)
		var me *model.Error
		if !errors.As(got, &me) || me.Code != c.code || me.Message != c.msg {
			t.Errorf("startError(%v) = %v，期望 %s/%q", c.err, got, c.code, c.msg)
		}
		if !errors.Is(got, c.err) {
			t.Errorf("startError(%v) 应保留原因链", c.err)
		}
	}
}

func TestEmptyLocaleFallsBackToEnglish(t *testing.T) {
	if got := New(Config{}).cfg.Locale; got != "en" {
		t.Errorf("Locale 为空时 = %q，期望 en（组合根总会传入有效语言，这里只兜底）", got)
	}
	if got := New(Config{Locale: "zh"}).cfg.Locale; got != "zh" {
		t.Errorf("显式 Locale 被覆盖为 %q", got)
	}
}
