package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/version"
)

// Config 的默认值。
const (
	DefaultPingInterval     = 30 * time.Second
	DefaultPingTimeout      = 10 * time.Second
	DefaultHandshakeTimeout = 15 * time.Second
	DefaultShutdownGrace    = 5 * time.Second
	DefaultCancelGrace      = 10 * time.Second
	DefaultBackoffMin       = time.Second
	DefaultBackoffMax       = 30 * time.Second
	DefaultMaxLineBytes     = 16 << 20

	// fallbackLocale 只在 Config.Locale 为空时使用；组合根总会传入解析后的有效语言，
	// 这里不再自带一份"默认语言"，避免与 config / i18n 的默认值分叉。
	fallbackLocale = "en"
	// drainGrace 是进程退出后留给读循环派发尾部消息的时间。
	drainGrace = time.Second
	// killWait 是强制结束后等待操作系统回收进程的上限，也是关闭连接后等待反向请求收尾的默认上限。
	killWait = 5 * time.Second
	// droppedCancelled 是 runState.dropped 的取值之一：运行在被接受前已被 CancelRun 的兜底收尾移除。
	droppedCancelled = "cancelled"
	// stderrMaxLine 是 stderr 单行保留的最大字节数，超出部分丢弃。
	stderrMaxLine = 64 << 10
)

// Config 是 Supervisor 的构造参数；零值字段取 Default* 常量。
type Config struct {
	// Command 为零值时在每次启动前调用 Locate()。
	Command Command
	// Locale 通过 worker.hello 与 GOGOSLING_LOCALE 告知工作进程；组合根应传入解析后的有效语言，
	// 为空时使用 "en"。
	Locale string
	// DaemonVersion 默认为 version.Version。
	DaemonVersion string
	// Tools 处理反向请求 host.tool.call；为 nil 时工具调用一律返回 -32603。
	Tools ToolExecutor
	// Logger 为 nil 时丢弃日志。
	Logger *slog.Logger

	PingInterval     time.Duration
	PingTimeout      time.Duration
	HandshakeTimeout time.Duration
	// ShutdownGrace 是发出 worker.shutdown 后等待进程自行退出的时间，超时则强制结束。
	ShutdownGrace time.Duration
	// CancelGrace 是 run.cancel 之后等待工作进程发出终态事件的时间，超时由守护进程补发 run.cancelled。
	CancelGrace time.Duration
	BackoffMin  time.Duration
	BackoffMax  time.Duration
	// MaxLineBytes 是协议流单行上限，超过视为协议错误并重启工作进程。
	MaxLineBytes int

	// inflightWait 是关闭连接后等待反向请求（工具调用）收尾的上限，默认 killWait；只供测试缩短。
	inflightWait time.Duration
}

func (c Config) withDefaults() Config {
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&c.PingInterval, DefaultPingInterval)
	def(&c.PingTimeout, DefaultPingTimeout)
	def(&c.HandshakeTimeout, DefaultHandshakeTimeout)
	def(&c.ShutdownGrace, DefaultShutdownGrace)
	def(&c.CancelGrace, DefaultCancelGrace)
	def(&c.BackoffMin, DefaultBackoffMin)
	def(&c.BackoffMax, DefaultBackoffMax)
	if c.BackoffMax < c.BackoffMin {
		c.BackoffMax = c.BackoffMin
	}
	def(&c.inflightWait, killWait)
	if c.MaxLineBytes <= 0 {
		c.MaxLineBytes = DefaultMaxLineBytes
	}
	if c.Locale == "" {
		c.Locale = fallbackLocale
	}
	if c.DaemonVersion == "" {
		c.DaemonVersion = version.Version
	}
	if c.Logger == nil {
		c.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return c
}

// Supervisor 实现 Runtime：监管一个 Python 工作进程，并把运行请求与事件在内核和它之间转发。
//
// 并发安全。生命周期：New → Start（非阻塞）→ … → Stop；停止后不能再次 Start。
type Supervisor struct {
	cfg Config
	log *slog.Logger
	now func() time.Time

	mu         sync.Mutex
	started    bool
	cancelLoop context.CancelFunc
	loopDone   chan struct{}
	state      State
	info       helloResult
	pid        int
	restarts   int
	lastErr    string
	conn       *conn
	readyCh    chan struct{}
	runs       map[string]*runState
}

// runState 是一个在途运行在守护进程侧的全部状态。
type runState struct {
	id        string
	sessionID string
	agentID   string
	sink      EventSink
	// ctx 在运行取消、结束或工作进程断开时取消；所有该运行的工具调用都挂在它下面。
	ctx    context.Context
	cancel context.CancelFunc
	// accepted 在 run.start 返回 accepted 之后为 true；只有被接受的运行才会在崩溃时补发 run.failed。
	accepted bool
	// dropped 记录运行在被接受前就被移除的原因，StartRun 据此返回错误而不是让调用方空等：
	// droppedCancelled 表示被 CancelRun 的兜底收尾移除，其余取值是 failRuns 给出的 Error* 常量。
	dropped string
	// terminated 表示工作进程已经为它发出终态事件（可能早于 run.start 的响应到达）并已投递给 sink。
	terminated bool
	// deliverMu 串行化对 sink 的调用，保证同一运行的事件不会并发投递。
	deliverMu sync.Mutex
}

var _ Runtime = (*Supervisor)(nil)

// New 创建 Supervisor；此时不启动任何进程。
func New(cfg Config) *Supervisor {
	cfg = cfg.withDefaults()
	return &Supervisor{
		cfg:     cfg,
		log:     cfg.Logger,
		now:     time.Now,
		state:   StateUnavailable,
		lastErr: "尚未启动",
		readyCh: make(chan struct{}),
		runs:    make(map[string]*runState),
	}
}

// Start 启动监管循环并立即返回；ctx 结束时循环优雅退出。重复调用返回 CodeConflict。
func (s *Supervisor) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return model.NewError(model.CodeConflict, "engine 已经启动")
	}
	loopCtx, cancel := context.WithCancel(ctx)
	s.started = true
	s.cancelLoop = cancel
	s.loopDone = make(chan struct{})
	s.state = StateStarting
	s.lastErr = ""
	go s.supervise(loopCtx)
	return nil
}

// Stop 请求优雅停止（worker.shutdown → 等待 ShutdownGrace → 强制结束）并等待监管循环退出；
// 监管循环退出前会等待在途的反向请求（工具调用）收尾，上限 5 秒，因此 Stop 返回 nil 后
// 调用方可以安全地关闭工具执行器依赖的资源（扩展连接、数据库）。
// ctx 先结束则返回 CodeTimeout，但停止仍会在后台继续。未启动时直接返回 nil。
func (s *Supervisor) Stop(ctx context.Context) error {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return nil
	}
	cancel, done := s.cancelLoop, s.loopDone
	s.mu.Unlock()

	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return model.Wrap(model.CodeTimeout, "等待工作进程停止超时", ctx.Err())
	}
}

// WaitReady 阻塞直到状态变为 ready；ctx 先结束返回 CodeTimeout / CodeCancelled，监管循环已退出返回 CodeUnavailable。
func (s *Supervisor) WaitReady(ctx context.Context) error {
	for {
		s.mu.Lock()
		ready := s.state == StateReady
		ch, done := s.readyCh, s.loopDone
		s.mu.Unlock()
		if ready {
			return nil
		}
		select {
		case <-ch:
		case <-done:
			return model.NewError(model.CodeUnavailable, "engine 已停止")
		case <-ctx.Done():
			return ctxError(ctx.Err(), "等待工作进程就绪")
		}
	}
}

// Status 返回状态快照。
func (s *Supervisor) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{
		State:      s.state,
		Version:    s.info.WorkerVersion,
		Python:     s.info.Python,
		PID:        s.pid,
		Restarts:   s.restarts,
		ActiveRuns: len(s.runs),
		LastError:  s.lastErr,
	}
	if len(s.info.Capabilities) > 0 {
		st.Capabilities = append([]string(nil), s.info.Capabilities...)
	}
	return st
}

// StartRun 实现 Runtime.StartRun。
func (s *Supervisor) StartRun(ctx context.Context, req RunRequest, sink EventSink) error {
	if err := validateRunRequest(req, sink); err != nil {
		return err
	}

	s.mu.Lock()
	if s.state != StateReady || s.conn == nil {
		reason := s.lastErr
		s.mu.Unlock()
		return &model.Error{Code: model.CodeUnavailable, Message: ErrorWorkerUnavailable, Details: map[string]any{"reason": reason}}
	}
	if _, dup := s.runs[req.RunID]; dup {
		s.mu.Unlock()
		return &model.Error{Code: model.CodeConflict, Message: ErrorInvalidRequest, Details: map[string]any{"reason": "duplicate run_id", "run_id": req.RunID}}
	}
	rctx, rcancel := context.WithCancel(context.Background())
	rs := &runState{id: req.RunID, sessionID: req.SessionID, agentID: req.Agent.ID, sink: sink, ctx: rctx, cancel: rcancel}
	s.runs[req.RunID] = rs
	c := s.conn
	s.mu.Unlock()

	params := runStartParams{
		RunID:     req.RunID,
		SessionID: req.SessionID,
		Agent:     req.Agent,
		Provider:  req.Credentials,
		Messages:  req.Messages,
		Tools:     req.Tools,
		Options:   req.Options,
	}
	if params.Messages == nil {
		params.Messages = []model.Message{}
	}
	if params.Tools == nil {
		params.Tools = []model.ToolSpec{}
	}

	var res runStartResult
	if err := c.Call(ctx, methodRunStart, params, &res); err != nil {
		s.forgetRun(rs)
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			// 请求可能已经送达：尽力让工作进程放弃这次运行，避免它在无人接收事件的情况下继续。
			go s.bestEffortCancel(c, req.RunID)
		}
		return startError(err)
	}
	if !res.Accepted {
		s.forgetRun(rs)
		return model.NewError(model.CodeUnavailable, ErrorRunRejected)
	}

	s.mu.Lock()
	switch {
	case rs.dropped == droppedCancelled:
		s.mu.Unlock()
		// CancelRun 的兜底已经在接受前移除了运行（不会再有终态事件）；工作进程刚刚接受了它，
		// 再让它放弃一次，免得它在无人接收事件的情况下继续消耗供应商额度。
		go s.bestEffortCancel(c, req.RunID)
		return model.NewError(model.CodeCancelled, ErrorRunCancelled)
	case rs.dropped != "":
		s.mu.Unlock()
		return model.NewError(model.CodeUnavailable, rs.dropped)
	case rs.terminated:
		// 工作进程在 run.start 的响应之前就发出了终态事件，sink 已经收到；按成功返回，
		// 维持"返回 nil ⇔ sink 恰好收到一个终态事件"的约定。
		s.mu.Unlock()
		return nil
	case s.runs[rs.id] != rs:
		// 运行已被移除却没有记录原因：不应发生，但不能返回 nil 让调用方空等一个不会到来的终态。
		s.mu.Unlock()
		s.log.Error("运行在被接受前从在途表中消失", "run_id", rs.id)
		go s.bestEffortCancel(c, req.RunID)
		return model.NewError(model.CodeInternal, ErrorRunRejected)
	}
	rs.accepted = true
	s.mu.Unlock()
	return nil
}

// validateRunRequest 检查运行请求本身是否合法；不合法的请求不提交给工作进程。
func validateRunRequest(req RunRequest, sink EventSink) error {
	invalid := func(reason string) error {
		return &model.Error{Code: model.CodeInvalid, Message: ErrorInvalidRequest, Details: map[string]any{"reason": reason}}
	}
	if req.RunID == "" {
		return invalid("empty run_id")
	}
	if sink == nil {
		return invalid("nil sink")
	}
	// 协议 §2：openai_compatible 的 base_url 必须非空，不得静默回退到某个默认服务商——
	// 否则用户为其他服务准备的密钥会被发给非预期的第三方。
	if req.Credentials.Kind == model.ProviderOpenAICompatible && strings.TrimSpace(req.Credentials.BaseURL) == "" {
		return invalid("openai_compatible provider requires base_url")
	}
	return nil
}

// startError 把提交 run.start 时的失败映射为带 Error* 常量的领域错误，保留错误码与原因链。
func startError(err error) error {
	mapped := toModelError(err, ErrorRunRejected)
	code := model.CodeOf(mapped)
	msg := ErrorRunRejected
	var re *RPCError
	if !errors.As(err, &re) {
		// 没有收到工作进程的回答：连接断开、超时或调用方取消。
		msg = ErrorWorkerUnavailable
		if code == model.CodeCancelled {
			msg = ErrorRunCancelled
		}
	}
	out := &model.Error{Code: code, Message: msg, Err: err}
	var me *model.Error
	if errors.As(mapped, &me) && me.Details != nil {
		out.Details = me.Details
	}
	return out
}

// CancelRun 实现 Runtime.CancelRun。
func (s *Supervisor) CancelRun(ctx context.Context, runID string) error {
	s.mu.Lock()
	rs := s.runs[runID]
	c := s.conn
	s.mu.Unlock()
	if rs == nil {
		return model.NotFound("run", runID)
	}
	if c == nil {
		return model.NewError(model.CodeUnavailable, ErrorWorkerUnavailable)
	}

	var res runCancelResult
	err := c.Call(ctx, methodRunCancel, runCancelParams{RunID: runID}, &res)
	// 无论工作进程怎么回答，都释放该运行上等待中的工具调用（例如挂起的审批），它们会以 -32003 结束。
	rs.cancel()
	if err != nil {
		return toModelError(err, "取消运行失败")
	}
	if !res.Cancelled {
		s.log.Debug("工作进程不认识要取消的运行，可能已经结束", "run_id", runID)
	}
	// 兜底：工作进程在宽限期内没有发出终态事件时，由守护进程补发 run.cancelled，保证状态收敛。
	time.AfterFunc(s.cfg.CancelGrace, func() {
		s.finishRun(rs, model.EventRunCancelled, "")
	})
	return nil
}

func (s *Supervisor) bestEffortCancel(c *conn, runID string) {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.PingTimeout)
	defer cancel()
	var res runCancelResult
	if err := c.Call(ctx, methodRunCancel, runCancelParams{RunID: runID}, &res); err != nil {
		s.log.Debug("尽力取消运行失败", "run_id", runID, "err", err)
	}
}

// forgetRun 移除一个尚未被接受的运行，不发任何事件。
func (s *Supervisor) forgetRun(rs *runState) {
	s.mu.Lock()
	if s.runs[rs.id] == rs {
		delete(s.runs, rs.id)
	}
	s.mu.Unlock()
	rs.cancel()
}

// finishRun 为仍在途的运行补发终态事件并移除它；运行已结束则什么也不做。
// 运行尚未被接受时不发事件，而是标记 dropped=cancelled，让仍在等待 run.start 响应的 StartRun
// 返回 CodeCancelled，由调用方收尾（否则 StartRun 会在运行已被移除后返回成功，之后的事件全部丢失）。
func (s *Supervisor) finishRun(rs *runState, typ model.RunEventType, errText string) {
	s.mu.Lock()
	if s.runs[rs.id] != rs {
		s.mu.Unlock()
		return
	}
	delete(s.runs, rs.id)
	accepted := rs.accepted
	if !accepted {
		rs.dropped = droppedCancelled
	}
	s.mu.Unlock()
	rs.cancel()
	if !accepted {
		return
	}
	s.deliver(rs, model.RunEvent{Type: typ, Error: errText})
}

// failRuns 在工作进程断开时把所有在途运行收尾：已接受的补发 run.failed，未接受的标记 dropped。
// reason 必须是 Error* 常量之一（会写进 Run.Error），细节由调用方写日志。
func (s *Supervisor) failRuns(reason string) {
	s.mu.Lock()
	var accepted []*runState
	for _, rs := range s.runs {
		if rs.accepted {
			accepted = append(accepted, rs)
		} else {
			rs.dropped = reason
		}
		rs.cancel()
	}
	s.runs = make(map[string]*runState)
	s.mu.Unlock()

	for _, rs := range accepted {
		s.deliver(rs, model.RunEvent{Type: model.EventRunFailed, Error: reason})
	}
}

// deliver 补全事件的标识字段并同步交给 sink；sink 的 panic 只记录日志，不影响读循环。
func (s *Supervisor) deliver(rs *runState, ev model.RunEvent) {
	ev.RunID = rs.id
	if ev.SessionID == "" {
		ev.SessionID = rs.sessionID
	}
	if ev.AgentID == "" {
		ev.AgentID = rs.agentID
	}
	if ev.At.IsZero() {
		ev.At = s.now().UTC()
	}
	rs.deliverMu.Lock()
	defer rs.deliverMu.Unlock()
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("事件 sink 发生 panic", "run_id", rs.id, "type", ev.Type, "panic", fmt.Sprint(r))
		}
	}()
	rs.sink(ev)
}

// ---- connHandler：通知与反向请求 ----

func (s *Supervisor) onNotify(method string, params json.RawMessage) {
	switch method {
	case notifyRunEvent:
		var p runEventParams
		if err := json.Unmarshal(params, &p); err != nil {
			s.log.Warn("run.event 参数无法解析", "err", err)
			return
		}
		s.dispatchEvent(p.RunID, p.Event)
	case notifyLog:
		var p logParams
		if err := json.Unmarshal(params, &p); err != nil {
			s.log.Warn("log 通知无法解析", "err", err)
			return
		}
		s.log.Log(context.Background(), workerLogLevel(p.Level), p.Message, "source", "worker", "fields", p.Fields)
	default:
		s.log.Debug("未知的工作进程通知，已忽略", "method", method)
	}
}

func (s *Supervisor) dispatchEvent(runID string, ev model.RunEvent) {
	if runID == "" {
		runID = ev.RunID
	}
	if ev.Type == "" {
		s.log.Warn("run.event 缺少类型，已忽略", "run_id", runID)
		return
	}
	s.mu.Lock()
	rs := s.runs[runID]
	if rs == nil {
		s.mu.Unlock()
		s.log.Debug("收到未知或已结束运行的事件，已忽略", "run_id", runID, "type", ev.Type)
		return
	}
	if isTerminal(ev.Type) {
		delete(s.runs, runID)
		rs.terminated = true
	}
	s.mu.Unlock()
	if isTerminal(ev.Type) {
		rs.cancel()
	}
	s.deliver(rs, ev)
}

func (s *Supervisor) onRequest(ctx context.Context, method string, params json.RawMessage) (any, *RPCError) {
	switch method {
	case methodHostToolCall:
		return s.handleToolCall(ctx, params)
	case methodHostSecretGet:
		return nil, &RPCError{Code: codeMethodNotFound, Message: "host.secret.get 在当前版本不提供"}
	default:
		return nil, &RPCError{Code: codeMethodNotFound, Message: "未知的反向请求: " + method}
	}
}

func (s *Supervisor) handleToolCall(connCtx context.Context, params json.RawMessage) (any, *RPCError) {
	var p toolCallParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &RPCError{Code: codeInvalidParams, Message: "host.tool.call 参数无法解析"}
	}
	s.mu.Lock()
	rs := s.runs[p.RunID]
	s.mu.Unlock()
	if rs == nil {
		return nil, &RPCError{Code: codeInvalidParams, Message: "未知的 run_id"}
	}
	if s.cfg.Tools == nil {
		return nil, &RPCError{Code: codeInternalError, Message: "守护进程未配置工具执行器"}
	}

	// 工具调用随运行取消或连接关闭而取消，二者取其先。
	ctx, cancel := context.WithCancel(rs.ctx)
	defer cancel()
	stop := context.AfterFunc(connCtx, cancel)
	defer stop()

	result, err := s.executeTool(ctx, p.RunID, p.Call)
	if rs.ctx.Err() != nil || connCtx.Err() != nil {
		return nil, &RPCError{Code: codeRunCancelled, Message: "运行已取消"}
	}
	if err != nil {
		return nil, toolCallError(err)
	}
	if result.CallID == "" {
		result.CallID = p.Call.ID
	}
	return toolCallResult{Result: result}, nil
}

func (s *Supervisor) executeTool(ctx context.Context, runID string, call model.ToolCall) (result model.ToolResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("工具执行器发生 panic", "run_id", runID, "tool", call.Name, "panic", fmt.Sprint(r))
			err = model.NewError(model.CodeInternal, "工具执行器内部错误")
		}
	}()
	return s.cfg.Tools.Execute(ctx, runID, call)
}

// toolCallError 把工具执行错误映射为协议错误码；只把面向用户/模型的 Message 发给工作进程，底层 Err 不外传。
func toolCallError(err error) *RPCError {
	var me *model.Error
	msg := "工具执行失败"
	if errors.As(err, &me) && me.Message != "" {
		msg = me.Message
	}
	switch model.CodeOf(err) {
	case model.CodeDenied:
		data, _ := json.Marshal(toolCallErrorData{Reason: msg})
		return &RPCError{Code: codePermissionDenied, Message: msg, Data: data}
	case model.CodeCancelled:
		return &RPCError{Code: codeRunCancelled, Message: msg}
	default:
		return &RPCError{Code: codeInternalError, Message: msg}
	}
}

// ---- 监管循环 ----

type outcomeKind int

const (
	outcomeLocate outcomeKind = iota
	outcomeSpawn
	outcomeIncompatible
	outcomeHandshake
	outcomeCrashed
	outcomeStopped
)

type sessionOutcome struct {
	kind outcomeKind
	err  error
}

func (s *Supervisor) supervise(ctx context.Context) {
	defer func() {
		s.mu.Lock()
		s.state = StateUnavailable
		s.conn = nil
		s.pid = 0
		s.lastErr = "engine 已停止"
		s.mu.Unlock()
		close(s.loopDone)
	}()

	backoff := s.cfg.BackoffMin
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			s.mu.Lock()
			s.restarts++
			s.mu.Unlock()
		}
		startedAt := s.now()
		outcome := s.runSession(ctx)
		if ctx.Err() != nil || outcome.kind == outcomeStopped {
			return
		}

		var delay time.Duration
		switch outcome.kind {
		case outcomeLocate, outcomeSpawn, outcomeIncompatible:
			// 这些情况重试没有意义，只按最大间隔重新检查（运行环境可能被安装或更新）。
			delay = s.cfg.BackoffMax
			backoff = s.cfg.BackoffMin
		default:
			if s.now().Sub(startedAt) >= s.cfg.BackoffMax {
				// 工作进程稳定运行了一段时间，这次失败不算连续失败。
				backoff = s.cfg.BackoffMin
			}
			delay = backoff
			backoff = min(backoff*2, s.cfg.BackoffMax)
		}
		s.log.Warn("工作进程会话结束，稍后重启", "reason", outcome.err, "delay", delay, "restarts", s.Status().Restarts)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

// runSession 启动并监管一个工作进程直到它结束；返回结束原因。
func (s *Supervisor) runSession(ctx context.Context) sessionOutcome {
	command := s.cfg.Command
	if command.Path == "" {
		located, err := Locate()
		if err != nil {
			s.setState(StateUnavailable, err.Error())
			return sessionOutcome{kind: outcomeLocate, err: err}
		}
		command = located
	}

	s.setState(StateStarting, "")
	env := buildEnv(s.cfg.Locale, command.Env, os.LookupEnv)
	proc, err := spawn(command, env)
	if err != nil {
		err = fmt.Errorf("启动工作进程失败: %w", err)
		s.setState(StateUnavailable, err.Error())
		return sessionOutcome{kind: outcomeSpawn, err: err}
	}
	// 网络出口设置（代理、证书）会影响工作进程能否连上模型服务，记下来便于排查；代理 URL 的 userinfo 已脱敏。
	s.log.Info("工作进程已启动", "pid", proc.pid(), "path", command.Path, "args", command.Args, "egress_env", egressSummary(env))

	c := newConn(proc.stdout, proc.stdin, s.cfg.MaxLineBytes, s, s.log)
	readDone := make(chan error, 1)
	go func() { readDone <- c.readLoop() }()
	go s.forwardStderr(proc.stderr, proc.pid())

	outcome, ok := s.handshake(ctx, proc, c)
	if !ok {
		s.finishSession(proc, c, readDone, outcome.err, ErrorWorkerUnavailable)
		return outcome
	}

	pingStop := make(chan struct{})
	go s.pingLoop(c, proc, pingStop)
	defer close(pingStop)

	var readErr error
	select {
	case <-ctx.Done():
		s.gracefulStop(proc, c)
		outcome = sessionOutcome{kind: outcomeStopped, err: errors.New("engine 停止")}
	case <-proc.exited:
		outcome = sessionOutcome{kind: outcomeCrashed}
	case readErr = <-readDone:
		outcome = sessionOutcome{kind: outcomeCrashed}
	}
	if outcome.kind == outcomeCrashed {
		// 无论先看到的是进程退出还是输出流结束，都等进程真正退出后再描述原因，这样 run.failed 里带有退出码。
		exited := s.ensureExit(proc)
		switch {
		case exited && readErr != nil && !errors.Is(readErr, errWorkerEOF):
			outcome.err = fmt.Errorf("工作进程退出: %s（协议错误: %w）", proc.exitDescription(), readErr)
		case exited:
			outcome.err = fmt.Errorf("工作进程退出: %s", proc.exitDescription())
		default:
			outcome.err = fmt.Errorf("工作进程连接断开且无法结束进程: %v", readErr)
		}
	}
	runErr := ErrorWorkerCrashed
	if outcome.kind == outcomeStopped {
		runErr = ErrorWorkerStopped
	}
	s.finishSession(proc, c, readDone, outcome.err, runErr)
	if outcome.kind != outcomeStopped {
		s.setState(StateStarting, outcome.err.Error())
	}
	return outcome
}

// ensureExit 等待进程退出；超过 drainGrace 仍未退出则强制结束并再等 killWait。返回进程是否已退出。
func (s *Supervisor) ensureExit(proc *process) bool {
	if s.waitExit(proc, drainGrace) {
		return true
	}
	proc.kill()
	if s.waitExit(proc, killWait) {
		return true
	}
	s.log.Error("工作进程在强制结束后仍未退出", "pid", proc.pid())
	return false
}

// handshake 执行 worker.hello 并校验协议版本；失败时返回原因，调用方负责收尾进程。
func (s *Supervisor) handshake(ctx context.Context, proc *process, c *conn) (sessionOutcome, bool) {
	hctx, cancel := context.WithTimeout(ctx, s.cfg.HandshakeTimeout)
	defer cancel()
	var hello helloResult
	err := c.Call(hctx, methodHello, helloParams{
		ProtocolVersion: ProtocolVersion,
		DaemonVersion:   s.cfg.DaemonVersion,
		Locale:          s.cfg.Locale,
	}, &hello)

	var rpcErr *RPCError
	switch {
	case err == nil && hello.ProtocolVersion == ProtocolVersion:
		s.mu.Lock()
		s.state = StateReady
		s.info = hello
		s.pid = proc.pid()
		s.conn = c
		s.lastErr = ""
		close(s.readyCh)
		s.mu.Unlock()
		s.log.Info("工作进程就绪", "pid", proc.pid(), "worker_version", hello.WorkerVersion, "python", hello.Python, "capabilities", hello.Capabilities)
		return sessionOutcome{}, true
	case err == nil || (errors.As(err, &rpcErr) && rpcErr.Code == codeIncompatibleVersion):
		reason := fmt.Errorf("协议版本不兼容: 守护进程 %q，工作进程 %q", ProtocolVersion, hello.ProtocolVersion)
		if err != nil {
			reason = fmt.Errorf("协议版本不兼容: 守护进程 %q，工作进程拒绝: %w", ProtocolVersion, err)
		}
		// 工作进程本身是健康的，给它机会正常退出。
		_ = c.Notify(methodShutdown, nil)
		s.waitExit(proc, s.cfg.ShutdownGrace)
		s.setState(StateUnavailable, reason.Error())
		return sessionOutcome{kind: outcomeIncompatible, err: reason}, false
	default:
		reason := fmt.Errorf("握手失败: %w", err)
		s.setState(StateUnavailable, reason.Error())
		return sessionOutcome{kind: outcomeHandshake, err: reason}, false
	}
}

// pingLoop 周期性发送 worker.ping；超时或回答不对就强制结束进程，交给监管循环重启。
// 看门狗计时器独立于 Call，即使写管道被塞满也能触发。
func (s *Supervisor) pingLoop(c *conn, proc *process, stop <-chan struct{}) {
	ticker := time.NewTicker(s.cfg.PingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-c.Done():
			return
		case <-ticker.C:
		}
		watchdog := time.AfterFunc(s.cfg.PingTimeout, func() {
			s.log.Warn("健康检查无响应，强制结束工作进程", "pid", proc.pid())
			proc.kill()
		})
		pctx, cancel := context.WithTimeout(context.Background(), s.cfg.PingTimeout)
		var pong string
		err := c.Call(pctx, methodPing, nil, &pong)
		cancel()
		if !watchdog.Stop() {
			return
		}
		if c.isClosed() {
			// 连接在等待 pong 期间已经关闭（进程退出或正在停止），由监管循环处理，不算健康检查失败。
			return
		}
		if err != nil || pong != "pong" {
			s.log.Warn("健康检查失败，强制结束工作进程", "pid", proc.pid(), "err", err, "reply", pong)
			proc.kill()
			return
		}
	}
}

// gracefulStop 发出 worker.shutdown，等待宽限期后强制结束。
func (s *Supervisor) gracefulStop(proc *process, c *conn) {
	if err := c.Notify(methodShutdown, nil); err != nil {
		s.log.Debug("发送 worker.shutdown 失败", "err", err)
	}
	if s.waitExit(proc, s.cfg.ShutdownGrace) {
		s.log.Info("工作进程已退出", "pid", proc.pid(), "exit", proc.exitDescription())
		return
	}
	s.log.Warn("工作进程未在宽限期内退出，强制结束", "pid", proc.pid(), "grace", s.cfg.ShutdownGrace)
	proc.kill()
}

// finishSession 确保进程已死、管道已关、连接已关，把在途运行收尾，并有界地等待反向请求结束。
//
// reason 是详细原因（写日志、作为连接关闭原因）；runErr 是写进 run.failed 的 Error* 常量。
// 关闭连接会取消所有反向请求的 ctx；这里等它们真正返回（上限 cfg.inflightWait），这样 Stop 返回后
// 不会还有工具调用在写审计、读写数据库或使用已关闭的 MCP 客户端。
func (s *Supervisor) finishSession(proc *process, c *conn, readDone <-chan error, reason error, runErr string) {
	s.ensureExit(proc)
	// 进程已退出：给读循环一点时间把尾部消息派发完，再关闭管道让它返回。
	select {
	case <-readDone:
	case <-time.After(drainGrace):
	}
	c.closeWith(reason)
	proc.closePipes()

	s.mu.Lock()
	if s.conn == c {
		s.conn = nil
	}
	s.pid = 0
	if s.state == StateReady {
		s.state = StateStarting
		s.readyCh = make(chan struct{})
	}
	pending := len(s.runs)
	s.mu.Unlock()

	if pending > 0 {
		s.log.Warn("工作进程会话结束，在途运行以失败收尾", "runs", pending, "error", runErr, "reason", reason)
	}
	s.failRuns(runErr)

	if !c.waitInflight(s.cfg.inflightWait) {
		s.log.Warn("反向请求在宽限期内没有结束，不再等待", "wait", s.cfg.inflightWait)
	}
}

func (s *Supervisor) waitExit(proc *process, d time.Duration) bool {
	select {
	case <-proc.exited:
		return true
	case <-time.After(d):
		return false
	}
}

// setState 更新状态；离开 ready 时重置就绪信号。
func (s *Supervisor) setState(state State, lastErr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == StateReady && state != StateReady {
		s.readyCh = make(chan struct{})
	}
	s.state = state
	s.lastErr = lastErr
}

// forwardStderr 把工作进程的 stderr 逐行写入日志，直到流结束。
func (s *Supervisor) forwardStderr(r io.Reader, pid int) {
	lr := newLineReader(r, stderrMaxLine, true)
	for {
		line, err := lr.ReadLine()
		if err != nil {
			return
		}
		if len(line) == 0 {
			continue
		}
		s.log.Info("工作进程 stderr", "pid", pid, "line", string(line))
	}
}

func workerLogLevel(level string) slog.Level {
	switch level {
	case "error":
		return slog.LevelError
	case "warn", "warning":
		return slog.LevelWarn
	case "info":
		return slog.LevelInfo
	default:
		return slog.LevelDebug
	}
}

func isTerminal(t model.RunEventType) bool {
	return t == model.EventRunCompleted || t == model.EventRunFailed || t == model.EventRunCancelled
}

// toModelError 把连接层或对端返回的错误映射为带 model.Code 的错误。
func toModelError(err error, msg string) error {
	if err == nil {
		return nil
	}
	var me *model.Error
	if errors.As(err, &me) {
		return err
	}
	var re *RPCError
	if errors.As(err, &re) {
		code := model.CodeInternal
		switch re.Code {
		case codeDuplicateRun:
			code = model.CodeConflict
		case codeRunCancelled:
			code = model.CodeCancelled
		case codePermissionDenied:
			code = model.CodeDenied
		case codeIncompatibleVersion, codeProviderFailed:
			code = model.CodeUnavailable
		case codeInvalidParams, codeInvalidRequest:
			code = model.CodeInvalid
		}
		return &model.Error{Code: code, Message: msg, Details: map[string]any{"rpc_code": re.Code, "rpc_message": re.Message}, Err: re}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return ctxError(err, msg)
	}
	return model.Wrap(model.CodeInternal, msg, err)
}
