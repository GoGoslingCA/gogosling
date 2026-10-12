package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

type stubHandler struct {
	mu       sync.Mutex
	notifies []string
	onReq    func(ctx context.Context, method string, params json.RawMessage) (any, *RPCError)
}

func (h *stubHandler) onNotify(method string, params json.RawMessage) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.notifies = append(h.notifies, method+":"+string(params))
}

func (h *stubHandler) onRequest(ctx context.Context, method string, params json.RawMessage) (any, *RPCError) {
	if h.onReq == nil {
		return nil, &RPCError{Code: codeMethodNotFound, Message: "no handler"}
	}
	return h.onReq(ctx, method, params)
}

func (h *stubHandler) seen() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.notifies...)
}

// pipeConn 把 conn 接到一对 io.Pipe 上：peerW 写入即 conn 读到；peerR 读到的是 conn 写出的内容。
type pipeConn struct {
	c        *conn
	peerW    *io.PipeWriter
	peerR    *bufio.Reader
	readDone chan error
}

func newPipeConn(t *testing.T, h connHandler, maxLine int) *pipeConn {
	t.Helper()
	toConn, peerW := io.Pipe()
	fromConn, connW := io.Pipe()
	c := newConn(toConn, connW, maxLine, h, slog.New(slog.NewTextHandler(io.Discard, nil)))
	p := &pipeConn{c: c, peerW: peerW, peerR: bufio.NewReader(fromConn), readDone: make(chan error, 1)}
	go func() { p.readDone <- c.readLoop() }()
	t.Cleanup(func() {
		_ = peerW.Close()
		_ = fromConn.Close()
	})
	return p
}

// send / recv 可在任意 goroutine 使用；mustSend / mustRecv 只能在测试 goroutine 使用。

func (p *pipeConn) send(line string) error {
	_, err := io.WriteString(p.peerW, line+"\n")
	return err
}

func (p *pipeConn) recv() (wireMessage, error) {
	line, err := p.peerR.ReadString('\n')
	if err != nil {
		return wireMessage{}, err
	}
	var msg wireMessage
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		return wireMessage{}, fmt.Errorf("连接输出不是 JSON: %q", line)
	}
	return msg, nil
}

func (p *pipeConn) mustSend(t *testing.T, line string) {
	t.Helper()
	if err := p.send(line); err != nil {
		t.Fatalf("写入连接失败: %v", err)
	}
}

func (p *pipeConn) mustRecv(t *testing.T) wireMessage {
	t.Helper()
	msg, err := p.recv()
	if err != nil {
		t.Fatalf("读取连接输出失败: %v", err)
	}
	return msg
}

// peer 在后台扮演对端：读一条请求，交给 fn 决定回什么；fn 返回空串表示不回复。
func (p *pipeConn) peer(t *testing.T, fn func(req wireMessage) string) {
	t.Helper()
	go func() {
		req, err := p.recv()
		if err != nil {
			t.Errorf("对端读取请求失败: %v", err)
			return
		}
		for _, line := range strings.Split(fn(req), "\n") {
			if line == "" {
				continue
			}
			if err := p.send(line); err != nil {
				t.Errorf("对端写入失败: %v", err)
			}
		}
	}()
}

func TestCallRoundTrip(t *testing.T) {
	p := newPipeConn(t, &stubHandler{}, 1<<20)
	p.peer(t, func(req wireMessage) string {
		if req.Method != "worker.ping" || string(req.ID) != "1" {
			t.Errorf("请求 = %s/%s，期望 worker.ping/1", req.Method, req.ID)
		}
		return `{"jsonrpc":"2.0","id":1,"result":"pong"}`
	})
	var pong string
	if err := p.c.Call(context.Background(), "worker.ping", nil, &pong); err != nil {
		t.Fatalf("Call 失败: %v", err)
	}
	if pong != "pong" {
		t.Errorf("结果 = %q，期望 pong", pong)
	}
}

func TestCallRemoteError(t *testing.T) {
	p := newPipeConn(t, &stubHandler{}, 1<<20)
	p.peer(t, func(req wireMessage) string {
		return `{"jsonrpc":"2.0","id":` + string(req.ID) + `,"error":{"code":-32002,"message":"dup","data":{"x":1}}}`
	})
	err := p.c.Call(context.Background(), "run.start", map[string]string{"run_id": "a"}, nil)
	var re *RPCError
	if !errors.As(err, &re) || re.Code != codeDuplicateRun || re.Message != "dup" {
		t.Fatalf("错误 = %v，期望 RPCError -32002", err)
	}
}

func TestCallTimeout(t *testing.T) {
	p := newPipeConn(t, &stubHandler{}, 1<<20)
	p.peer(t, func(wireMessage) string { return "" })
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := p.c.Call(ctx, "worker.ping", nil, nil)
	if !model.IsCode(err, model.CodeTimeout) {
		t.Fatalf("错误码 = %v，期望 timeout", err)
	}
	// 迟到的响应不应造成任何问题。
	p.mustSend(t, `{"jsonrpc":"2.0","id":1,"result":"pong"}`)
}

func TestOversizedLineClosesConn(t *testing.T) {
	p := newPipeConn(t, &stubHandler{}, 1024)
	p.mustSend(t, strings.Repeat("a", 2000))
	select {
	case err := <-p.readDone:
		if !errors.Is(err, errLineTooLong) {
			t.Fatalf("关闭原因 = %v，期望 errLineTooLong", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("超长行没有让读循环退出")
	}
	err := p.c.Call(context.Background(), "worker.ping", nil, nil)
	if !model.IsCode(err, model.CodeUnavailable) {
		t.Fatalf("关闭后 Call 的错误码 = %v，期望 unavailable", err)
	}
}

func TestNonJSONLineIsIgnored(t *testing.T) {
	p := newPipeConn(t, &stubHandler{}, 1<<20)
	p.peer(t, func(wireMessage) string {
		return "this is not json\n" +
			`{"jsonrpc":"2.0","id":99,"result":"stale"}` + "\n" +
			`{"jsonrpc":"2.0","id":1,"result":"pong"}`
	})
	var pong string
	if err := p.c.Call(context.Background(), "worker.ping", nil, &pong); err != nil || pong != "pong" {
		t.Fatalf("Call = %q, %v，期望 pong", pong, err)
	}
}

func TestNotificationDispatch(t *testing.T) {
	h := &stubHandler{}
	p := newPipeConn(t, h, 1<<20)
	p.mustSend(t, `{"jsonrpc":"2.0","method":"run.event","params":{"run_id":"r"}}`)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if seen := h.seen(); len(seen) == 1 && seen[0] == `run.event:{"run_id":"r"}` {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("通知未送达处理器: %v", h.seen())
}

func TestReverseRequestsAreServedConcurrently(t *testing.T) {
	release := make(chan struct{})
	h := &stubHandler{onReq: func(ctx context.Context, method string, params json.RawMessage) (any, *RPCError) {
		switch method {
		case "slow":
			<-release
			return "slow-done", nil
		case "fast":
			return "fast-done", nil
		}
		return nil, &RPCError{Code: codeMethodNotFound, Message: "?"}
	}}
	p := newPipeConn(t, h, 1<<20)
	p.mustSend(t, `{"jsonrpc":"2.0","id":"s1","method":"slow"}`)
	p.mustSend(t, `{"jsonrpc":"2.0","id":2,"method":"fast"}`)

	first := p.mustRecv(t)
	if string(first.ID) != "2" || string(first.Result) != `"fast-done"` {
		t.Fatalf("先到的响应 = %s/%s，期望慢请求不阻塞快请求", first.ID, first.Result)
	}
	close(release)
	second := p.mustRecv(t)
	if string(second.ID) != `"s1"` || string(second.Result) != `"slow-done"` {
		t.Fatalf("第二个响应 = %s/%s，期望原样回显字符串 id", second.ID, second.Result)
	}

	p.mustSend(t, `{"jsonrpc":"2.0","id":3,"method":"unknown"}`)
	third := p.mustRecv(t)
	if third.Error == nil || third.Error.Code != codeMethodNotFound {
		t.Fatalf("未知方法的响应 = %+v，期望 -32601", third)
	}
}

func TestReverseRequestContextCancelledOnClose(t *testing.T) {
	entered := make(chan struct{})
	exited := make(chan error, 1)
	h := &stubHandler{onReq: func(ctx context.Context, method string, params json.RawMessage) (any, *RPCError) {
		close(entered)
		<-ctx.Done()
		exited <- ctx.Err()
		return nil, &RPCError{Code: codeRunCancelled, Message: "cancelled"}
	}}
	p := newPipeConn(t, h, 1<<20)
	p.mustSend(t, `{"jsonrpc":"2.0","id":1,"method":"host.tool.call"}`)
	<-entered
	_ = p.peerW.Close()
	select {
	case err := <-p.readDone:
		if !errors.Is(err, errWorkerEOF) {
			t.Errorf("关闭原因 = %v，期望 EOF", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("EOF 没有让读循环退出")
	}
	select {
	case err := <-exited:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("反向请求的 ctx 错误 = %v，期望 canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("连接关闭后反向请求的 ctx 未被取消")
	}
}

func TestNotifyWritesSingleLine(t *testing.T) {
	p := newPipeConn(t, &stubHandler{}, 1<<20)
	go func() {
		if err := p.c.Notify("log", map[string]string{"message": "多行\n内容"}); err != nil {
			t.Errorf("Notify 失败: %v", err)
		}
	}()
	msg := p.mustRecv(t)
	if msg.Method != "log" || len(msg.ID) != 0 {
		t.Fatalf("通知 = %+v，期望无 id 的 log", msg)
	}
	if !strings.Contains(string(msg.Params), `\n`) {
		t.Errorf("换行应被转义而不是裸露: %s", msg.Params)
	}
}
