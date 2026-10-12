package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// errWorkerEOF 表示工作进程关闭了输出流。
var errWorkerEOF = errors.New("engine: 工作进程输出流已结束")

// connHandler 是连接向上层回调的接口：通知与反向请求。
//
// onRequest 在独立 goroutine 中调用，可以长时间阻塞；ctx 在连接关闭时取消。
type connHandler interface {
	onNotify(method string, params json.RawMessage)
	onRequest(ctx context.Context, method string, params json.RawMessage) (any, *RPCError)
}

// conn 是一条全双工的行分隔 JSON-RPC 连接：本端发请求并收响应，同时服务对端的通知与反向请求。
//
// 并发安全。写入由 wmu 串行化；读循环只有一个 goroutine；反向请求各自开 goroutine，
// 因此慢工具调用不会阻塞后续消息的读取。
type conn struct {
	log     *slog.Logger
	w       io.Writer
	lr      *lineReader
	handler connHandler

	wmu sync.Mutex

	nextID  atomic.Int64
	pmu     sync.Mutex
	pending map[int64]chan *wireMessage

	ctx       context.Context
	cancel    context.CancelFunc
	closed    chan struct{}
	closeOnce sync.Once
	closeErr  error

	// reqMu 串行化"登记反向请求"与"关闭连接"：关闭之后不会再有 inflight.Add，
	// 因此关闭后调用 waitInflight 是安全的（WaitGroup 要求计数为零时的 Add 先于 Wait）。
	reqMu    sync.Mutex
	inflight sync.WaitGroup
}

func newConn(r io.Reader, w io.Writer, maxLine int, h connHandler, log *slog.Logger) *conn {
	ctx, cancel := context.WithCancel(context.Background())
	return &conn{
		log:     log,
		w:       w,
		lr:      newLineReader(r, maxLine, false),
		handler: h,
		pending: make(map[int64]chan *wireMessage),
		ctx:     ctx,
		cancel:  cancel,
		closed:  make(chan struct{}),
	}
}

// readLoop 阻塞读取直到流结束、读错误或协议错误；返回前连接一定已关闭，返回值即关闭原因。
func (c *conn) readLoop() error {
	for {
		line, err := c.lr.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = errWorkerEOF
			}
			c.closeWith(err)
			return err
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var msg wireMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			// 工作进程把非协议内容写到了 stdout（例如第三方库的 print）。
			// 只记录长度，不记录内容，避免把意外输出的敏感信息带进日志。
			c.log.Warn("工作进程输出了非 JSON 行，已忽略", "bytes", len(line))
			continue
		}
		c.dispatch(&msg)
	}
}

func (c *conn) dispatch(msg *wireMessage) {
	switch {
	case msg.Method == "" && len(msg.ID) > 0:
		c.deliverResponse(msg)
	case msg.Method != "" && len(msg.ID) > 0:
		c.reqMu.Lock()
		if c.isClosed() {
			c.reqMu.Unlock()
			// 连接已关闭：响应也写不回去，不再启动处理。
			c.log.Debug("连接已关闭，忽略反向请求", "method", msg.Method)
			return
		}
		c.inflight.Add(1)
		c.reqMu.Unlock()
		go c.serveRequest(msg)
	case msg.Method != "":
		c.safeNotify(msg)
	default:
		c.log.Warn("收到既无 method 也无 id 的 JSON-RPC 消息，已忽略")
	}
}

func (c *conn) deliverResponse(msg *wireMessage) {
	id, err := strconv.ParseInt(string(bytes.TrimSpace(msg.ID)), 10, 64)
	if err != nil {
		c.log.Warn("响应的 id 不是本端发出的整数编号，已忽略", "id", string(msg.ID))
		return
	}
	c.pmu.Lock()
	ch, ok := c.pending[id]
	if ok {
		delete(c.pending, id)
	}
	c.pmu.Unlock()
	if !ok {
		c.log.Warn("收到未知请求的响应，已忽略", "id", id)
		return
	}
	ch <- msg
}

func (c *conn) safeNotify(msg *wireMessage) {
	defer func() {
		if r := recover(); r != nil {
			c.log.Error("处理通知时发生 panic", "method", msg.Method, "panic", fmt.Sprint(r))
		}
	}()
	c.handler.onNotify(msg.Method, msg.Params)
}

// serveRequest 处理一条反向请求并写回响应；在独立 goroutine 中运行。
func (c *conn) serveRequest(msg *wireMessage) {
	defer c.inflight.Done()
	result, rerr := c.callHandler(msg)
	resp := wireMessage{JSONRPC: jsonrpcVersion, ID: msg.ID}
	if rerr != nil {
		resp.Error = rerr
	} else {
		data, err := json.Marshal(result)
		if err != nil {
			resp.Error = &RPCError{Code: codeInternalError, Message: "序列化结果失败"}
		} else {
			resp.Result = data
		}
	}
	if err := c.write(resp); err != nil {
		c.log.Debug("写回反向请求的响应失败", "method", msg.Method, "err", err)
	}
}

func (c *conn) callHandler(msg *wireMessage) (result any, rerr *RPCError) {
	defer func() {
		if r := recover(); r != nil {
			c.log.Error("处理反向请求时发生 panic", "method", msg.Method, "panic", fmt.Sprint(r))
			result, rerr = nil, &RPCError{Code: codeInternalError, Message: "内部错误"}
		}
	}()
	return c.handler.onRequest(c.ctx, msg.Method, msg.Params)
}

// Call 发送请求并等待响应。ctx 结束返回 CodeTimeout / CodeCancelled；连接关闭返回 CodeUnavailable；
// 对端返回错误对象时返回 *RPCError。result 为 nil 时忽略结果。
func (c *conn) Call(ctx context.Context, method string, params any, result any) error {
	id := c.nextID.Add(1)
	ch := make(chan *wireMessage, 1)

	c.pmu.Lock()
	if c.isClosed() {
		c.pmu.Unlock()
		return c.closedError()
	}
	c.pending[id] = ch
	c.pmu.Unlock()

	req := wireMessage{JSONRPC: jsonrpcVersion, ID: strconv.AppendInt(nil, id, 10), Method: method}
	if err := setParams(&req, params); err != nil {
		c.removePending(id)
		return err
	}
	if err := c.write(req); err != nil {
		c.removePending(id)
		return err
	}

	select {
	case resp := <-ch:
		if resp.Error != nil {
			return resp.Error
		}
		if result != nil && len(resp.Result) > 0 {
			if err := json.Unmarshal(resp.Result, result); err != nil {
				return model.Wrap(model.CodeInternal, "解析工作进程响应失败", err)
			}
		}
		return nil
	case <-ctx.Done():
		c.removePending(id)
		return ctxError(ctx.Err(), "等待工作进程响应")
	case <-c.closed:
		c.removePending(id)
		return c.closedError()
	}
}

// Notify 发送通知（无 id，不等待响应）。
func (c *conn) Notify(method string, params any) error {
	msg := wireMessage{JSONRPC: jsonrpcVersion, Method: method}
	if err := setParams(&msg, params); err != nil {
		return err
	}
	return c.write(msg)
}

func setParams(msg *wireMessage, params any) error {
	if params == nil {
		return nil
	}
	data, err := json.Marshal(params)
	if err != nil {
		return model.Wrap(model.CodeInternal, "序列化请求参数失败", err)
	}
	msg.Params = data
	return nil
}

func (c *conn) write(msg wireMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return model.Wrap(model.CodeInternal, "序列化 JSON-RPC 消息失败", err)
	}
	// encoding/json 会转义字符串中的换行，因此 data 内不会出现裸换行，整条消息恰好一行。
	data = append(data, '\n')

	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.isClosed() {
		return c.closedError()
	}
	if _, err := c.w.Write(data); err != nil {
		c.closeWith(fmt.Errorf("写入工作进程失败: %w", err))
		return model.Wrap(model.CodeUnavailable, "写入工作进程失败", err)
	}
	return nil
}

func (c *conn) removePending(id int64) {
	c.pmu.Lock()
	delete(c.pending, id)
	c.pmu.Unlock()
}

// closeWith 以给定原因关闭连接：取消所有反向请求的 ctx，并唤醒所有等待响应的 Call。可重复调用。
func (c *conn) closeWith(err error) {
	c.closeOnce.Do(func() {
		c.reqMu.Lock()
		defer c.reqMu.Unlock()
		c.closeErr = err
		c.cancel()
		close(c.closed)
	})
}

// waitInflight 等待所有反向请求的处理 goroutine 返回，最多 d；返回是否全部结束。
// 只能在 closeWith 之后调用（之后不会再登记新的反向请求）。超时后留下的等待 goroutine
// 会在最后一个处理返回时自行结束。
func (c *conn) waitInflight(d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		c.inflight.Wait()
		close(done)
	}()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

// Done 在连接关闭后关闭。
func (c *conn) Done() <-chan struct{} { return c.closed }

func (c *conn) isClosed() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

func (c *conn) closedError() error {
	return model.Wrap(model.CodeUnavailable, "工作进程连接已关闭", c.closeErr)
}

// ctxError 把 context 错误映射为带错误码的领域错误。
func ctxError(err error, what string) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return model.Wrap(model.CodeTimeout, what+"超时", err)
	}
	return model.Wrap(model.CodeCancelled, what+"被取消", err)
}
