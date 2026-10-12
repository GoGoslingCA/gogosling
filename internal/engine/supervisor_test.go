package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// ---- 测试辅助 ----

func fakeCommand(mode string) Command {
	return Command{
		Path: os.Args[0],
		Args: []string{"-test.run=^$"},
		Env:  map[string]string{fakeWorkerEnv: "1", fakeModeEnv: mode},
	}
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

func newTestSupervisor(t *testing.T, mode string, tools ToolExecutor, tweak func(*Config)) (*Supervisor, *logBuffer) {
	t.Helper()
	lb := &logBuffer{}
	cfg := Config{
		Command:          fakeCommand(mode),
		Logger:           slog.New(slog.NewTextHandler(lb, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Tools:            tools,
		PingInterval:     500 * time.Millisecond,
		PingTimeout:      3 * time.Second,
		HandshakeTimeout: 10 * time.Second,
		ShutdownGrace:    2 * time.Second,
		CancelGrace:      500 * time.Millisecond,
		BackoffMin:       50 * time.Millisecond,
		BackoffMax:       300 * time.Millisecond,
	}
	if tweak != nil {
		tweak(&cfg)
	}
	s := New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	if err := s.Start(ctx); err != nil {
		t.Fatalf("启动 engine 失败: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer stopCancel()
		if err := s.Stop(stopCtx); err != nil {
			t.Errorf("停止 engine 失败: %v", err)
		}
	})
	return s, lb
}

func waitReady(t *testing.T, s *Supervisor) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := s.WaitReady(ctx); err != nil {
		t.Fatalf("等待工作进程就绪失败: %v（状态 %+v）", err, s.Status())
	}
}

func eventually(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("超时仍未满足: %s", what)
}

type collector struct {
	mu       sync.Mutex
	events   []model.RunEvent
	terminal chan struct{}
	once     sync.Once
}

func newCollector() *collector {
	return &collector{terminal: make(chan struct{})}
}

func (c *collector) sink(ev model.RunEvent) {
	c.mu.Lock()
	c.events = append(c.events, ev)
	c.mu.Unlock()
	if isTerminal(ev.Type) {
		c.once.Do(func() { close(c.terminal) })
	}
}

func (c *collector) snapshot() []model.RunEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]model.RunEvent(nil), c.events...)
}

func (c *collector) types() []model.RunEventType {
	var out []model.RunEventType
	for _, ev := range c.snapshot() {
		out = append(out, ev.Type)
	}
	return out
}

func (c *collector) find(typ model.RunEventType) (model.RunEvent, bool) {
	for _, ev := range c.snapshot() {
		if ev.Type == typ {
			return ev, true
		}
	}
	return model.RunEvent{}, false
}

func (c *collector) waitTerminal(t *testing.T, timeout time.Duration) []model.RunEvent {
	t.Helper()
	select {
	case <-c.terminal:
	case <-time.After(timeout):
		t.Fatalf("等待终态事件超时，已收到: %v", c.types())
	}
	return c.snapshot()
}

type recordedCall struct {
	runID string
	call  model.ToolCall
}

// stubExecutor 是可配置的工具执行器：返回固定结果或错误，也可以阻塞直到 ctx 取消（模拟等待审批）。
type stubExecutor struct {
	mu        sync.Mutex
	calls     []recordedCall
	result    model.ToolResult
	err       error
	block     bool
	started   chan struct{}
	startOnce sync.Once
	ctxErr    error
}

func newStubExecutor() *stubExecutor {
	return &stubExecutor{started: make(chan struct{})}
}

func (e *stubExecutor) Execute(ctx context.Context, runID string, call model.ToolCall) (model.ToolResult, error) {
	e.mu.Lock()
	e.calls = append(e.calls, recordedCall{runID: runID, call: call})
	e.mu.Unlock()
	e.startOnce.Do(func() { close(e.started) })
	if e.block {
		<-ctx.Done()
		e.mu.Lock()
		e.ctxErr = ctx.Err()
		e.mu.Unlock()
		return model.ToolResult{}, model.Wrap(model.CodeCancelled, "等待审批时被取消", ctx.Err())
	}
	return e.result, e.err
}

func (e *stubExecutor) recorded() []recordedCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]recordedCall(nil), e.calls...)
}

func sampleRequest(runID string, tools ...model.ToolSpec) RunRequest {
	return RunRequest{
		RunID:       runID,
		SessionID:   "sess_1",
		Agent:       AgentSpec{ID: "agent_1", Name: "助理", SystemPrompt: "你是助理", Autonomy: model.AutonomyAskRisky},
		Credentials: model.ProviderCredentials{Kind: model.ProviderOpenAICompatible, BaseURL: "https://llm.example.test/v1", APIKey: "sk-test", Model: "gpt-test"},
		Messages:    []model.Message{model.TextMessage("sess_1", model.RoleUser, "你好")},
		Tools:       tools,
		Options:     RunOptions{MaxSteps: 4},
	}
}

var readFileTool = model.ToolSpec{Name: "read_file", Description: "读文件", InputSchema: json.RawMessage(`{"type":"object"}`), Source: "builtin", Risk: model.RiskRead}

// ---- 测试 ----

func TestHandshakeReportsReadyStatus(t *testing.T) {
	s, _ := newTestSupervisor(t, fakeModeNormal, nil, nil)
	waitReady(t, s)

	st := s.Status()
	if st.State != StateReady {
		t.Fatalf("状态 = %s，期望 ready", st.State)
	}
	if st.Version != "fake-0.1" || st.Python != "fake-3.12" {
		t.Errorf("版本信息 = %q/%q，期望 fake-0.1/fake-3.12", st.Version, st.Python)
	}
	if st.PID == 0 {
		t.Error("PID 未填充")
	}
	if len(st.Capabilities) != 2 {
		t.Errorf("能力列表 = %v，期望 2 项", st.Capabilities)
	}
	if st.Restarts != 0 || st.ActiveRuns != 0 || st.LastError != "" {
		t.Errorf("初始快照不干净: %+v", st)
	}
}

func TestRunEventsArriveInOrder(t *testing.T) {
	s, _ := newTestSupervisor(t, fakeModeNormal, nil, nil)
	waitReady(t, s)

	col := newCollector()
	if err := s.StartRun(context.Background(), sampleRequest("run_1"), col.sink); err != nil {
		t.Fatalf("StartRun 失败: %v", err)
	}
	events := col.waitTerminal(t, 10*time.Second)

	want := []model.RunEventType{model.EventRunStarted, model.EventTextDelta, model.EventTextDelta, model.EventUsage, model.EventRunCompleted}
	got := col.types()
	if strings.Join(typeStrings(got), ",") != strings.Join(typeStrings(want), ",") {
		t.Fatalf("事件顺序 = %v，期望 %v", got, want)
	}
	for _, ev := range events {
		if ev.RunID != "run_1" || ev.SessionID != "sess_1" || ev.AgentID != "agent_1" {
			t.Errorf("事件标识未补全: %+v", ev)
		}
		if ev.At.IsZero() {
			t.Errorf("事件时间未补全: %+v", ev)
		}
	}
	final := events[len(events)-1]
	if final.Message == nil || final.Message.Text() != "你好，世界" {
		t.Errorf("最终消息 = %+v，期望文本“你好，世界”", final.Message)
	}
	if final.Usage == nil || final.Usage.InputTokens != 10 {
		t.Errorf("累计用量 = %+v，期望 input 10", final.Usage)
	}
	eventually(t, 2*time.Second, "运行结束后从在途表移除", func() bool { return s.Status().ActiveRuns == 0 })
}

func TestReverseToolCallGoesThroughExecutor(t *testing.T) {
	exec := newStubExecutor()
	exec.result = model.ToolResult{Content: "file body"}
	s, _ := newTestSupervisor(t, fakeModeNormal, exec, nil)
	waitReady(t, s)

	col := newCollector()
	if err := s.StartRun(context.Background(), sampleRequest("run_tool", readFileTool), col.sink); err != nil {
		t.Fatalf("StartRun 失败: %v", err)
	}
	events := col.waitTerminal(t, 10*time.Second)

	calls := exec.recorded()
	if len(calls) != 1 {
		t.Fatalf("执行器被调用 %d 次，期望 1 次", len(calls))
	}
	if calls[0].runID != "run_tool" || calls[0].call.Name != "read_file" || calls[0].call.ID != "call_1" {
		t.Errorf("执行器收到的调用不对: %+v", calls[0])
	}
	if !bytes.Contains(calls[0].call.Arguments, []byte(`"notes.md"`)) {
		t.Errorf("参数未透传: %s", calls[0].call.Arguments)
	}
	if _, ok := col.find(model.EventToolCallStarted); !ok {
		t.Error("缺少 tool.call.started 事件")
	}
	done, ok := col.find(model.EventToolCallCompleted)
	if !ok || done.ToolResult == nil {
		t.Fatal("缺少 tool.call.completed 事件")
	}
	if done.ToolResult.CallID != "call_1" || done.ToolResult.Content != "file body" || done.ToolResult.IsError {
		t.Errorf("工具结果 = %+v，期望 call_1 / file body", done.ToolResult)
	}
	final := events[len(events)-1]
	if final.Type != model.EventRunCompleted || !strings.Contains(final.Message.Text(), "file body") {
		t.Errorf("最终消息未包含工具结果: %+v", final)
	}
}

func TestDeniedToolCallReturnsReasonToWorker(t *testing.T) {
	exec := newStubExecutor()
	exec.err = &model.Error{Code: model.CodeDenied, Message: "规则拒绝写入工作区外的文件", Err: errors.New("底层细节不应外传")}
	s, _ := newTestSupervisor(t, fakeModeNormal, exec, nil)
	waitReady(t, s)

	col := newCollector()
	if err := s.StartRun(context.Background(), sampleRequest("run_denied", readFileTool), col.sink); err != nil {
		t.Fatalf("StartRun 失败: %v", err)
	}
	col.waitTerminal(t, 10*time.Second)

	done, ok := col.find(model.EventToolCallCompleted)
	if !ok || done.ToolResult == nil {
		t.Fatal("缺少 tool.call.completed 事件")
	}
	if !done.ToolResult.IsError || done.ToolResult.Content != "规则拒绝写入工作区外的文件" {
		t.Errorf("拒绝原因未按 -32004 的 data.reason 回传: %+v", done.ToolResult)
	}
}

func TestCancelRunReleasesPendingToolCall(t *testing.T) {
	exec := newStubExecutor()
	exec.block = true
	s, _ := newTestSupervisor(t, fakeModeNormal, exec, nil)
	waitReady(t, s)

	if err := s.CancelRun(context.Background(), "nope"); !model.IsCode(err, model.CodeNotFound) {
		t.Errorf("取消未知运行的错误码 = %v，期望 not_found", err)
	}

	col := newCollector()
	if err := s.StartRun(context.Background(), sampleRequest("run_cancel", readFileTool), col.sink); err != nil {
		t.Fatalf("StartRun 失败: %v", err)
	}
	select {
	case <-exec.started:
	case <-time.After(10 * time.Second):
		t.Fatal("执行器迟迟没有收到工具调用")
	}

	if err := s.CancelRun(context.Background(), "run_cancel"); err != nil {
		t.Fatalf("CancelRun 失败: %v", err)
	}
	events := col.waitTerminal(t, 10*time.Second)
	if last := events[len(events)-1]; last.Type != model.EventRunCancelled {
		t.Errorf("终态 = %s，期望 run.cancelled", last.Type)
	}
	// 工作进程可能在守护进程释放工具调用之前就回了 run.cancelled（读循环先于 Call 的返回投递通知），
	// 执行器记录 ctx 错误与终态事件之间没有先后保证，因此要等待而不是立即断言。
	eventually(t, 2*time.Second, "执行器的 ctx 应被取消", func() bool {
		exec.mu.Lock()
		defer exec.mu.Unlock()
		return errors.Is(exec.ctxErr, context.Canceled)
	})
	eventually(t, 2*time.Second, "取消后从在途表移除", func() bool { return s.Status().ActiveRuns == 0 })
	if err := s.CancelRun(context.Background(), "run_cancel"); !model.IsCode(err, model.CodeNotFound) {
		t.Errorf("重复取消的错误码 = %v，期望 not_found", err)
	}
}

func TestCancelFallbackSynthesizesTerminalEvent(t *testing.T) {
	s, _ := newTestSupervisor(t, fakeModeCancelSilent, nil, nil)
	waitReady(t, s)

	col := newCollector()
	if err := s.StartRun(context.Background(), sampleRequest("run_silent"), col.sink); err != nil {
		t.Fatalf("StartRun 失败: %v", err)
	}
	eventually(t, 5*time.Second, "收到 run.started", func() bool {
		_, ok := col.find(model.EventRunStarted)
		return ok
	})
	if err := s.CancelRun(context.Background(), "run_silent"); err != nil {
		t.Fatalf("CancelRun 失败: %v", err)
	}
	events := col.waitTerminal(t, 5*time.Second)
	if last := events[len(events)-1]; last.Type != model.EventRunCancelled {
		t.Errorf("终态 = %s，期望守护进程补发的 run.cancelled", last.Type)
	}
	if s.Status().ActiveRuns != 0 {
		t.Error("补发终态后运行仍在在途表中")
	}
}

func TestCrashFailsInFlightRunsAndRestarts(t *testing.T) {
	s, _ := newTestSupervisor(t, fakeModeCrashOnRun, nil, nil)
	waitReady(t, s)

	col := newCollector()
	if err := s.StartRun(context.Background(), sampleRequest("run_crash"), col.sink); err != nil {
		t.Fatalf("StartRun 失败: %v", err)
	}
	events := col.waitTerminal(t, 10*time.Second)
	last := events[len(events)-1]
	if last.Type != model.EventRunFailed {
		t.Fatalf("终态 = %s，期望 run.failed", last.Type)
	}
	if last.Error != ErrorWorkerCrashed {
		t.Errorf("失败原因 = %q，期望机器常量 %q（细节只进日志，由传输层本地化）", last.Error, ErrorWorkerCrashed)
	}

	eventually(t, 15*time.Second, "崩溃后自动重启并再次就绪", func() bool {
		st := s.Status()
		return st.State == StateReady && st.Restarts >= 1
	})
	if s.Status().ActiveRuns != 0 {
		t.Error("崩溃后在途表未清空")
	}
}

func TestIncompatibleWorkerIsRejected(t *testing.T) {
	for _, mode := range []string{fakeModeHelloError, fakeModeHelloMismatch} {
		t.Run(mode, func(t *testing.T) {
			s, _ := newTestSupervisor(t, mode, nil, nil)
			eventually(t, 15*time.Second, "状态变为 unavailable 且说明版本不兼容", func() bool {
				st := s.Status()
				return st.State == StateUnavailable && strings.Contains(st.LastError, "协议版本不兼容")
			})
			err := s.StartRun(context.Background(), sampleRequest("run_x"), func(model.RunEvent) {})
			if !model.IsCode(err, model.CodeUnavailable) {
				t.Errorf("StartRun 错误码 = %v，期望 unavailable", err)
			}
			if s.Status().PID != 0 {
				t.Error("不兼容的工作进程应已被关闭")
			}
		})
	}
}

func TestStopShutsDownGracefullyAndFailsRuns(t *testing.T) {
	s, lb := newTestSupervisor(t, fakeModeSlowRun, nil, nil)
	waitReady(t, s)

	col := newCollector()
	if err := s.StartRun(context.Background(), sampleRequest("run_slow"), col.sink); err != nil {
		t.Fatalf("StartRun 失败: %v", err)
	}
	eventually(t, 5*time.Second, "收到 run.started", func() bool {
		_, ok := col.find(model.EventRunStarted)
		return ok
	})

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := s.Stop(ctx); err != nil {
		t.Fatalf("Stop 失败: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Stop 耗时 %v，期望工作进程收到 shutdown 后很快退出", elapsed)
	}
	if st := s.Status(); st.State != StateUnavailable || st.PID != 0 {
		t.Errorf("停止后的状态 = %+v，期望 unavailable 且无进程", st)
	}
	events := col.waitTerminal(t, 2*time.Second)
	if last := events[len(events)-1]; last.Type != model.EventRunFailed || last.Error != ErrorWorkerStopped {
		t.Errorf("停止时在途运行的终态 = %s/%q，期望 run.failed/%q", last.Type, last.Error, ErrorWorkerStopped)
	}
	if !strings.Contains(lb.String(), "exit status 0") {
		t.Errorf("日志里应记录工作进程以退出码 0 退出:\n%s", lb.String())
	}
	// 重复 Stop 与停止后的调用都应安全。
	if err := s.Stop(ctx); err != nil {
		t.Errorf("重复 Stop 失败: %v", err)
	}
	if err := s.StartRun(ctx, sampleRequest("after_stop"), col.sink); !model.IsCode(err, model.CodeUnavailable) {
		t.Errorf("停止后 StartRun 错误码 = %v，期望 unavailable", err)
	}
	if err := s.Start(ctx); !model.IsCode(err, model.CodeConflict) {
		t.Errorf("重复 Start 错误码 = %v，期望 conflict", err)
	}
}

func TestStopKillsWorkerThatIgnoresShutdown(t *testing.T) {
	s, lb := newTestSupervisor(t, fakeModeIgnoreShutdown, nil, func(c *Config) { c.ShutdownGrace = 300 * time.Millisecond })
	waitReady(t, s)

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := s.Stop(ctx); err != nil {
		t.Fatalf("Stop 失败: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 8*time.Second {
		t.Errorf("Stop 耗时 %v，期望宽限期后强制结束", elapsed)
	}
	if !strings.Contains(lb.String(), "强制结束") {
		t.Errorf("日志里应记录强制结束:\n%s", lb.String())
	}
}

func TestPingFailureRestartsWorker(t *testing.T) {
	s, _ := newTestSupervisor(t, fakeModePingHang, nil, func(c *Config) {
		c.PingInterval = 100 * time.Millisecond
		c.PingTimeout = 300 * time.Millisecond
	})
	waitReady(t, s)
	eventually(t, 15*time.Second, "健康检查失败后重启", func() bool { return s.Status().Restarts >= 1 })
}

func TestHandshakeTimeoutRetries(t *testing.T) {
	s, _ := newTestSupervisor(t, fakeModeHelloHang, nil, func(c *Config) { c.HandshakeTimeout = 300 * time.Millisecond })
	eventually(t, 15*time.Second, "握手超时后重试", func() bool {
		st := s.Status()
		return st.Restarts >= 1 && strings.Contains(st.LastError, "握手失败")
	})
}

func TestMissingWorkerReportsUnavailable(t *testing.T) {
	s, _ := newTestSupervisor(t, fakeModeNormal, nil, func(c *Config) {
		c.Command = Command{Path: filepath.Join(t.TempDir(), "missing-worker")}
	})
	eventually(t, 5*time.Second, "状态变为 unavailable", func() bool {
		st := s.Status()
		return st.State == StateUnavailable && strings.Contains(st.LastError, "启动工作进程失败")
	})
	err := s.StartRun(context.Background(), sampleRequest("run_x"), func(model.RunEvent) {})
	if !model.IsCode(err, model.CodeUnavailable) {
		t.Errorf("StartRun 错误码 = %v，期望 unavailable", err)
	}
	var me *model.Error
	if !errors.As(err, &me) || me.Message != ErrorWorkerUnavailable {
		t.Errorf("StartRun 的 Message 应为机器常量 %q（会写进 Run.Error），得到 %v", ErrorWorkerUnavailable, err)
	}
}

func TestDuplicateRunIDIsRejected(t *testing.T) {
	s, _ := newTestSupervisor(t, fakeModeSlowRun, nil, nil)
	waitReady(t, s)

	col := newCollector()
	if err := s.StartRun(context.Background(), sampleRequest("dup"), col.sink); err != nil {
		t.Fatalf("StartRun 失败: %v", err)
	}
	if err := s.StartRun(context.Background(), sampleRequest("dup"), col.sink); !model.IsCode(err, model.CodeConflict) {
		t.Errorf("重复 run_id 的错误码 = %v，期望 conflict", err)
	}
	if err := s.StartRun(context.Background(), RunRequest{}, col.sink); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("空 run_id 的错误码 = %v，期望 invalid", err)
	}
	if err := s.StartRun(context.Background(), sampleRequest("nosink"), nil); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("空 sink 的错误码 = %v，期望 invalid", err)
	}
}

func TestStderrIsForwardedToLog(t *testing.T) {
	s, lb := newTestSupervisor(t, fakeModeNormal, nil, nil)
	waitReady(t, s)
	eventually(t, 5*time.Second, "stderr 内容出现在日志里", func() bool {
		return strings.Contains(lb.String(), "fake worker started")
	})
}

func TestNotStartedSupervisorIsUnavailable(t *testing.T) {
	s := New(Config{Command: fakeCommand(fakeModeNormal)})
	if st := s.Status(); st.State != StateUnavailable {
		t.Errorf("未启动时状态 = %s，期望 unavailable", st.State)
	}
	err := s.StartRun(context.Background(), sampleRequest("x"), func(model.RunEvent) {})
	if !model.IsCode(err, model.CodeUnavailable) {
		t.Errorf("未启动时 StartRun 错误码 = %v，期望 unavailable", err)
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Errorf("未启动时 Stop 应为空操作: %v", err)
	}
}

func TestSinkPanicDoesNotKillReadLoop(t *testing.T) {
	s, lb := newTestSupervisor(t, fakeModeNormal, nil, nil)
	waitReady(t, s)

	col := newCollector()
	first := true
	sink := func(ev model.RunEvent) {
		if first {
			first = false
			panic("sink 故障")
		}
		col.sink(ev)
	}
	if err := s.StartRun(context.Background(), sampleRequest("run_panic"), sink); err != nil {
		t.Fatalf("StartRun 失败: %v", err)
	}
	col.waitTerminal(t, 10*time.Second)
	if !strings.Contains(lb.String(), "sink 发生 panic") {
		t.Error("sink 的 panic 应被记录")
	}
	if s.Status().State != StateReady {
		t.Error("sink 的 panic 不应影响工作进程连接")
	}
}

func TestToolCallErrorMapping(t *testing.T) {
	cases := []struct {
		err  error
		code int
		msg  string
	}{
		{model.NewError(model.CodeDenied, "不允许"), codePermissionDenied, "不允许"},
		{model.Wrap(model.CodeCancelled, "已取消", context.Canceled), codeRunCancelled, "已取消"},
		{model.NewError(model.CodeInternal, "沙箱失败"), codeInternalError, "沙箱失败"},
		{errors.New("raw"), codeInternalError, "工具执行失败"},
	}
	for _, c := range cases {
		got := toolCallError(c.err)
		if got.Code != c.code || got.Message != c.msg {
			t.Errorf("toolCallError(%v) = %d/%q，期望 %d/%q", c.err, got.Code, got.Message, c.code, c.msg)
		}
	}
	denied := toolCallError(model.NewError(model.CodeDenied, "原因"))
	var data toolCallErrorData
	if err := json.Unmarshal(denied.Data, &data); err != nil || data.Reason != "原因" {
		t.Errorf("-32004 的 data.reason = %q (%v)，期望 “原因”", data.Reason, err)
	}
}

func TestToModelErrorMapping(t *testing.T) {
	cases := []struct {
		err  error
		code model.Code
	}{
		{&RPCError{Code: codeDuplicateRun}, model.CodeConflict},
		{&RPCError{Code: codeRunCancelled}, model.CodeCancelled},
		{&RPCError{Code: codePermissionDenied}, model.CodeDenied},
		{&RPCError{Code: codeProviderFailed}, model.CodeUnavailable},
		{&RPCError{Code: codeInvalidParams}, model.CodeInvalid},
		{&RPCError{Code: codeInternalError}, model.CodeInternal},
		{context.DeadlineExceeded, model.CodeTimeout},
		{context.Canceled, model.CodeCancelled},
		{model.NewError(model.CodeUnavailable, "x"), model.CodeUnavailable},
		{errors.New("raw"), model.CodeInternal},
	}
	for _, c := range cases {
		if got := model.CodeOf(toModelError(c.err, "op")); got != c.code {
			t.Errorf("toModelError(%v) 错误码 = %s，期望 %s", c.err, got, c.code)
		}
	}
	if toModelError(nil, "op") != nil {
		t.Error("nil 应映射为 nil")
	}
}

func typeStrings(ts []model.RunEventType) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = string(t)
	}
	return out
}
