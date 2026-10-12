package engine

// 本文件实现"测试二进制自我重执行"的假工作进程：TestMain 发现 GOGOSLING_FAKE_WORKER=1 时，
// 当前进程不跑测试，而是用 Go 实现协议的最小子集充当工作进程。测试通过 Command.Env 传入
// GOGOSLING_FAKE_MODE 控制它的行为，覆盖握手、事件流、反向工具调用、取消、崩溃、版本不兼容与退出。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

const (
	fakeWorkerEnv = "GOGOSLING_FAKE_WORKER"
	fakeModeEnv   = "GOGOSLING_FAKE_MODE"

	// 假工作进程的行为模式。
	fakeModeNormal         = ""
	fakeModeHelloError     = "hello_error"     // 握手返回 -32001
	fakeModeHelloMismatch  = "hello_mismatch"  // 握手返回 protocol_version "2"
	fakeModeHelloHang      = "hello_hang"      // 不回答握手
	fakeModePingHang       = "ping_hang"       // 不回答 ping
	fakeModeIgnoreShutdown = "ignore_shutdown" // 忽略 worker.shutdown
	fakeModeCrashOnRun     = "crash_on_run"    // run.started 之后立即以退出码 3 崩溃
	fakeModeSlowRun        = "slow_run"        // 运行会一直挂起，直到被取消或进程退出
	fakeModeCancelSilent   = "cancel_silent"   // 像 slow_run，但 run.cancel 只回答 true，不发终态事件
	fakeModeSlowAccept     = "slow_accept"     // 登记运行后延迟 fakeAcceptDelay 才回答 accepted；期间照常处理 run.cancel
	fakeModeEarlyTerminal  = "early_terminal"  // 先发 run.started 与 run.completed，再回答 run.start 的 accepted

	// fakeAcceptDelay 是 slow_accept 模式下 run.start 响应的延迟。
	fakeAcceptDelay = 400 * time.Millisecond
)

func TestMain(m *testing.M) {
	if os.Getenv(fakeWorkerEnv) == "1" {
		os.Exit(runFakeWorker())
	}
	os.Exit(m.Run())
}

type fakeWorker struct {
	mode string
	out  io.Writer
	wmu  sync.Mutex

	nextID  atomic.Int64
	pmu     sync.Mutex
	pending map[int64]chan *wireMessage

	rmu  sync.Mutex
	runs map[string]*fakeRun
}

type fakeRun struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   sync.Once
}

func runFakeWorker() int {
	if os.Getenv(envProtocolVersion) != ProtocolVersion {
		fmt.Fprintln(os.Stderr, "fake worker: 缺少 "+envProtocolVersion)
		return 2
	}
	fmt.Fprintln(os.Stderr, "fake worker started")
	fw := &fakeWorker{
		mode:    os.Getenv(fakeModeEnv),
		out:     os.Stdout,
		pending: make(map[int64]chan *wireMessage),
		runs:    make(map[string]*fakeRun),
	}
	lr := newLineReader(os.Stdin, DefaultMaxLineBytes, false)
	for {
		line, err := lr.ReadLine()
		if err != nil {
			// 守护进程退出（stdin EOF）：工作进程随之退出。
			return 0
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var msg wireMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		if msg.Method == "" {
			fw.deliver(&msg)
			continue
		}
		if code := fw.handle(&msg); code >= 0 {
			return code
		}
	}
}

// handle 处理一条请求或通知；返回非负数表示进程应以该退出码退出。
func (fw *fakeWorker) handle(msg *wireMessage) int {
	switch msg.Method {
	case methodHello:
		switch fw.mode {
		case fakeModeHelloError:
			fw.replyError(msg.ID, codeIncompatibleVersion, "unsupported protocol version", nil)
		case fakeModeHelloMismatch:
			fw.reply(msg.ID, helloResult{ProtocolVersion: "2", WorkerVersion: "fake-9", Python: "fake"})
		case fakeModeHelloHang:
		default:
			fw.reply(msg.ID, helloResult{ProtocolVersion: ProtocolVersion, WorkerVersion: "fake-0.1", Python: "fake-3.12", Capabilities: []string{"run", "cancel"}})
		}
	case methodPing:
		if fw.mode != fakeModePingHang {
			fw.reply(msg.ID, "pong")
		}
	case methodInfo:
		fw.reply(msg.ID, map[string]any{"version": "fake-0.1", "python": "fake-3.12", "pid": os.Getpid()})
	case methodShutdown:
		if fw.mode == fakeModeIgnoreShutdown {
			return -1
		}
		return 0
	case methodRunStart:
		var p runStartParams
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			fw.replyError(msg.ID, codeInvalidParams, "bad params", nil)
			return -1
		}
		fw.rmu.Lock()
		if _, dup := fw.runs[p.RunID]; dup {
			fw.rmu.Unlock()
			fw.replyError(msg.ID, codeDuplicateRun, "duplicate run_id", nil)
			return -1
		}
		ctx, cancel := context.WithCancel(context.Background())
		run := &fakeRun{ctx: ctx, cancel: cancel}
		fw.runs[p.RunID] = run
		fw.rmu.Unlock()
		switch fw.mode {
		case fakeModeSlowAccept:
			// 响应在后台延迟发出，主循环继续读取并处理 run.cancel。
			go func() {
				time.Sleep(fakeAcceptDelay)
				fw.reply(msg.ID, runStartResult{Accepted: true})
				fw.runLoop(run, p)
			}()
		case fakeModeEarlyTerminal:
			emit := func(ev model.RunEvent) {
				ev.RunID = p.RunID
				fw.notify(notifyRunEvent, runEventParams{RunID: p.RunID, Event: ev})
			}
			emit(model.RunEvent{Type: model.EventRunStarted})
			emit(model.RunEvent{Type: model.EventRunCompleted, Message: &model.Message{Role: model.RoleAssistant, Parts: []model.Part{{Type: model.PartText, Text: "早到"}}}})
			fw.rmu.Lock()
			delete(fw.runs, p.RunID)
			fw.rmu.Unlock()
			fw.reply(msg.ID, runStartResult{Accepted: true})
		default:
			fw.reply(msg.ID, runStartResult{Accepted: true})
			go fw.runLoop(run, p)
		}
	case methodRunCancel:
		var p runCancelParams
		_ = json.Unmarshal(msg.Params, &p)
		fw.rmu.Lock()
		run := fw.runs[p.RunID]
		fw.rmu.Unlock()
		if run == nil {
			fw.reply(msg.ID, runCancelResult{Cancelled: false})
			return -1
		}
		if fw.mode != fakeModeCancelSilent {
			run.cancel()
		}
		fw.reply(msg.ID, runCancelResult{Cancelled: true})
	default:
		if len(msg.ID) > 0 {
			fw.replyError(msg.ID, codeMethodNotFound, "method not found", nil)
		}
	}
	return -1
}

func (fw *fakeWorker) runLoop(run *fakeRun, p runStartParams) {
	emit := func(ev model.RunEvent) {
		ev.RunID = p.RunID
		fw.notify(notifyRunEvent, runEventParams{RunID: p.RunID, Event: ev})
	}
	finish := func(ev model.RunEvent) {
		run.done.Do(func() {
			emit(ev)
			fw.rmu.Lock()
			delete(fw.runs, p.RunID)
			fw.rmu.Unlock()
		})
	}

	emit(model.RunEvent{Type: model.EventRunStarted})
	switch fw.mode {
	case fakeModeCrashOnRun:
		os.Exit(3)
	case fakeModeSlowRun, fakeModeCancelSilent, fakeModeSlowAccept:
		select {
		case <-run.ctx.Done():
			finish(model.RunEvent{Type: model.EventRunCancelled})
			return
		case <-time.After(30 * time.Second):
		}
	}

	emit(model.RunEvent{Type: model.EventTextDelta, Delta: "你好"})
	emit(model.RunEvent{Type: model.EventTextDelta, Delta: "，世界"})
	text := "你好，世界"

	if len(p.Tools) > 0 {
		call := model.ToolCall{ID: "call_1", Name: p.Tools[0].Name, Arguments: json.RawMessage(`{"path":"notes.md"}`)}
		emit(model.RunEvent{Type: model.EventToolCallStarted, ToolCall: &call})
		resp, err := fw.request(run.ctx, methodHostToolCall, toolCallParams{RunID: p.RunID, Call: call})
		if err != nil {
			finish(model.RunEvent{Type: model.EventRunCancelled})
			return
		}
		var result model.ToolResult
		switch {
		case resp.Error != nil && resp.Error.Code == codeRunCancelled:
			finish(model.RunEvent{Type: model.EventRunCancelled})
			return
		case resp.Error != nil:
			reason := resp.Error.Message
			if resp.Error.Code == codePermissionDenied {
				var d toolCallErrorData
				if json.Unmarshal(resp.Error.Data, &d) == nil && d.Reason != "" {
					reason = d.Reason
				}
			}
			result = model.ToolResult{CallID: call.ID, Content: reason, IsError: true}
		default:
			var r toolCallResult
			_ = json.Unmarshal(resp.Result, &r)
			result = r.Result
		}
		emit(model.RunEvent{Type: model.EventToolCallCompleted, ToolResult: &result})
		text += " " + result.Content
	}

	if run.ctx.Err() != nil {
		finish(model.RunEvent{Type: model.EventRunCancelled})
		return
	}
	usage := model.Usage{InputTokens: 10, OutputTokens: 5}
	emit(model.RunEvent{Type: model.EventUsage, Usage: &usage})
	finish(model.RunEvent{
		Type:    model.EventRunCompleted,
		Message: &model.Message{SessionID: p.SessionID, Role: model.RoleAssistant, Parts: []model.Part{{Type: model.PartText, Text: text}}},
		Usage:   &usage,
	})
}

func (fw *fakeWorker) request(ctx context.Context, method string, params any) (*wireMessage, error) {
	id := fw.nextID.Add(1)
	ch := make(chan *wireMessage, 1)
	fw.pmu.Lock()
	fw.pending[id] = ch
	fw.pmu.Unlock()
	data, _ := json.Marshal(params)
	fw.write(wireMessage{JSONRPC: jsonrpcVersion, ID: strconv.AppendInt(nil, id, 10), Method: method, Params: data})
	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		fw.pmu.Lock()
		delete(fw.pending, id)
		fw.pmu.Unlock()
		return nil, ctx.Err()
	}
}

func (fw *fakeWorker) deliver(msg *wireMessage) {
	id, err := strconv.ParseInt(string(bytes.TrimSpace(msg.ID)), 10, 64)
	if err != nil {
		return
	}
	fw.pmu.Lock()
	ch := fw.pending[id]
	delete(fw.pending, id)
	fw.pmu.Unlock()
	if ch != nil {
		ch <- msg
	}
}

func (fw *fakeWorker) notify(method string, params any) {
	data, _ := json.Marshal(params)
	fw.write(wireMessage{JSONRPC: jsonrpcVersion, Method: method, Params: data})
}

func (fw *fakeWorker) reply(id json.RawMessage, result any) {
	data, _ := json.Marshal(result)
	fw.write(wireMessage{JSONRPC: jsonrpcVersion, ID: id, Result: data})
}

func (fw *fakeWorker) replyError(id json.RawMessage, code int, message string, data json.RawMessage) {
	fw.write(wireMessage{JSONRPC: jsonrpcVersion, ID: id, Error: &RPCError{Code: code, Message: message, Data: data}})
}

func (fw *fakeWorker) write(msg wireMessage) {
	data, _ := json.Marshal(msg)
	data = append(data, '\n')
	fw.wmu.Lock()
	defer fw.wmu.Unlock()
	_, _ = fw.out.Write(data)
}
