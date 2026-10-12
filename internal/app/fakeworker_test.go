package app

// 本文件实现"测试二进制自我重执行"的 Go 假工作进程，让 internal/app 的集成测试不依赖 Python。
//
// TestMain 发现 os.Args[1] == fakeWorkerArg 时不跑测试，而是在 stdin/stdout 上实现 worker-rpc 的最小子集：
// worker.hello / worker.ping / worker.info / worker.shutdown / run.start / run.cancel，以及反向请求
// host.tool.call。测试把 GOGOSLING_WORKER_CMD 设为 `"<测试二进制>" fake-worker`，引擎按协议启动它
// （环境变量按白名单过滤，所以行为由用户消息里的标记决定，而不是环境变量）：
//
//	默认     run.started → text.delta×2 → usage → run.completed（文本"你好，世界"）
//	#tool    run.started → tool.call.started(run_command "echo hi") → host.tool.call（等待审批与执行）
//	         → tool.call.completed → text.delta → run.completed（tool_call / tool_result / text 三种片段）
//	#slow    run.started 后一直挂起，直到 run.cancel → run.cancelled

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

const fakeWorkerArg = "fake-worker"

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == fakeWorkerArg {
		os.Exit(runFakeWorker(os.Stdin, os.Stdout))
	}
	os.Exit(m.Run())
}

type fwMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *fwError        `json:"error,omitempty"`
}

type fwError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type fwRunStart struct {
	RunID     string              `json:"run_id"`
	SessionID string              `json:"session_id"`
	Agent     struct{ ID string } `json:"agent"`
	Messages  []model.Message     `json:"messages"`
	Tools     []model.ToolSpec    `json:"tools"`
}

type fakeWorkerProc struct {
	out io.Writer
	wmu sync.Mutex

	nextID  atomic.Int64
	pmu     sync.Mutex
	pending map[string]chan *fwMessage

	rmu  sync.Mutex
	runs map[string]context.CancelFunc
}

func runFakeWorker(in io.Reader, out io.Writer) int {
	fw := &fakeWorkerProc{out: out, pending: map[string]chan *fwMessage{}, runs: map[string]context.CancelFunc{}}
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var msg fwMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		if msg.Method == "" {
			fw.deliver(&msg)
			continue
		}
		if exit := fw.handle(&msg); exit {
			return 0
		}
	}
	return 0
}

func (fw *fakeWorkerProc) write(v any) {
	raw, _ := json.Marshal(v)
	fw.wmu.Lock()
	defer fw.wmu.Unlock()
	_, _ = fw.out.Write(append(raw, '\n'))
}

func (fw *fakeWorkerProc) reply(id json.RawMessage, result any) {
	raw, _ := json.Marshal(result)
	fw.write(fwMessage{JSONRPC: "2.0", ID: id, Result: raw})
}

func (fw *fakeWorkerProc) notify(method string, params any) {
	raw, _ := json.Marshal(params)
	fw.write(fwMessage{JSONRPC: "2.0", Method: method, Params: raw})
}

// request 发出反向请求并等待回答；ctx 结束时放弃等待。
func (fw *fakeWorkerProc) request(ctx context.Context, method string, params any) (*fwMessage, error) {
	id := fmt.Sprint(fw.nextID.Add(1))
	ch := make(chan *fwMessage, 1)
	fw.pmu.Lock()
	fw.pending[id] = ch
	fw.pmu.Unlock()
	raw, _ := json.Marshal(params)
	fw.write(fwMessage{JSONRPC: "2.0", ID: json.RawMessage(id), Method: method, Params: raw})
	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (fw *fakeWorkerProc) deliver(msg *fwMessage) {
	key := strings.Trim(string(msg.ID), `"`)
	fw.pmu.Lock()
	ch := fw.pending[key]
	delete(fw.pending, key)
	fw.pmu.Unlock()
	if ch != nil {
		ch <- msg
	}
}

// handle 处理一条请求或通知；返回 true 表示进程应退出。
func (fw *fakeWorkerProc) handle(msg *fwMessage) bool {
	switch msg.Method {
	case "worker.hello":
		fw.reply(msg.ID, map[string]any{"protocol_version": "1", "worker_version": "fake-0.1", "python": "fake-3.12", "capabilities": []string{"run", "cancel"}})
	case "worker.ping":
		fw.reply(msg.ID, "pong")
	case "worker.info":
		fw.reply(msg.ID, map[string]any{"version": "fake-0.1", "python": "fake-3.12", "pid": os.Getpid()})
	case "worker.shutdown":
		return true
	case "run.start":
		var p fwRunStart
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			fw.write(fwMessage{JSONRPC: "2.0", ID: msg.ID, Error: &fwError{Code: -32602, Message: "bad params"}})
			return false
		}
		ctx, cancel := context.WithCancel(context.Background())
		fw.rmu.Lock()
		fw.runs[p.RunID] = cancel
		fw.rmu.Unlock()
		fw.reply(msg.ID, map[string]any{"accepted": true})
		go fw.runScript(ctx, p)
	case "run.cancel":
		var p struct {
			RunID string `json:"run_id"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		fw.rmu.Lock()
		cancel := fw.runs[p.RunID]
		fw.rmu.Unlock()
		if cancel != nil {
			cancel()
		}
		fw.reply(msg.ID, map[string]any{"cancelled": cancel != nil})
	default:
		if len(msg.ID) > 0 {
			fw.write(fwMessage{JSONRPC: "2.0", ID: msg.ID, Error: &fwError{Code: -32601, Message: "method not found"}})
		}
	}
	return false
}

func (fw *fakeWorkerProc) runScript(ctx context.Context, p fwRunStart) {
	emit := func(ev model.RunEvent) {
		ev.RunID, ev.SessionID, ev.AgentID = p.RunID, p.SessionID, p.Agent.ID
		ev.At = time.Now().UTC()
		fw.notify("run.event", map[string]any{"run_id": p.RunID, "event": ev})
	}
	defer func() {
		fw.rmu.Lock()
		delete(fw.runs, p.RunID)
		fw.rmu.Unlock()
	}()
	text := ""
	for i := len(p.Messages) - 1; i >= 0; i-- {
		if p.Messages[i].Role == model.RoleUser {
			text = p.Messages[i].Text()
			break
		}
	}
	emit(model.RunEvent{Type: model.EventRunStarted})
	usage := &model.Usage{InputTokens: 12, OutputTokens: 5}

	switch {
	case strings.Contains(text, "#slow"):
		<-ctx.Done()
		emit(model.RunEvent{Type: model.EventRunCancelled})
		return
	case strings.Contains(text, "#tool"):
		call := model.ToolCall{ID: "call_1", Name: "run_command", Arguments: json.RawMessage(`{"command":"echo hi"}`)}
		emit(model.RunEvent{Type: model.EventToolCallStarted, ToolCall: &call})
		resp, err := fw.request(ctx, "host.tool.call", map[string]any{"run_id": p.RunID, "call": call})
		if err != nil {
			emit(model.RunEvent{Type: model.EventRunCancelled})
			return
		}
		var res model.ToolResult
		switch {
		case resp.Error != nil && resp.Error.Code == -32003:
			emit(model.RunEvent{Type: model.EventRunCancelled})
			return
		case resp.Error != nil:
			res = model.ToolResult{CallID: call.ID, Content: "denied: " + resp.Error.Message, IsError: true}
		default:
			var wrapped struct {
				Result model.ToolResult `json:"result"`
			}
			_ = json.Unmarshal(resp.Result, &wrapped)
			res = wrapped.Result
		}
		emit(model.RunEvent{Type: model.EventToolCallCompleted, ToolResult: &res})
		reply := "命令输出：" + strings.TrimSpace(res.Content)
		emit(model.RunEvent{Type: model.EventTextDelta, Delta: reply})
		emit(model.RunEvent{Type: model.EventUsage, Usage: usage})
		emit(model.RunEvent{Type: model.EventRunCompleted, Usage: usage, Message: &model.Message{
			Role: model.RoleAssistant,
			Parts: []model.Part{
				{Type: model.PartToolCall, ToolCall: &call},
				{Type: model.PartToolResult, ToolResult: &res},
				{Type: model.PartText, Text: reply},
			},
		}})
	default:
		emit(model.RunEvent{Type: model.EventTextDelta, Delta: "你好"})
		emit(model.RunEvent{Type: model.EventTextDelta, Delta: "，世界"})
		emit(model.RunEvent{Type: model.EventUsage, Usage: usage})
		emit(model.RunEvent{Type: model.EventRunCompleted, Usage: usage, Message: &model.Message{
			Role:  model.RoleAssistant,
			Parts: []model.Part{{Type: model.PartText, Text: "你好，世界"}},
		}})
	}
}
