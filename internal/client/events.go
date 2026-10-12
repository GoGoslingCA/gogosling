package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/event"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

// EventGap 是"服务端缓冲已丢弃续传位置之后的事件"的事件类型：收到后调用方应全量刷新状态，
// 之后的事件从该事件的 Seq 继续。
const EventGap = "gap"

// eventHello 是服务端在每条连接开头发送的握手帧（data 为 {"started_at","last_seq"}）；本包自己消费，不投递给调用方。
const eventHello = "hello"

const (
	// maxEventLine 是事件流单行上限（与工作进程协议的分帧上限一致）。
	maxEventLine = 16 << 20
	// eventsBuffer 是事件通道的缓冲长度。
	eventsBuffer = 64
)

// Events 订阅事件流（GET /api/v1/events）。
//
// 事件按 Seq 升序投递；Payload 是 json.RawMessage，可用 DecodePayload 按 Type 解码。
// 连接断开、空闲超时或服务端出错时按最后收到的 Seq 自动重连（since 参数 + Last-Event-ID 头），
// 退避从 250ms 指数增长、上限 5 秒并带抖动；建立过连接后退避归零。
// 服务端发送 gap 时投递一条 Type == EventGap 的事件（Seq 为新的续传位置），随后立即重连。
//
// 守护进程重启检测：每条连接开头的 hello 帧带有实例启动时间 started_at。重连后发现它与上一次连接不同，
// 说明守护进程重启过、旧实例的 Seq 在新实例上没有意义（新实例的 Seq 可能已经追上旧位置，单凭 Seq
// 无法识别）：此时丢弃本地续传位置，投递一条 Seq 为新实例当前最新 Seq 的 gap，并从那里重新订阅。
//
// 错误通道报告重连原因（非阻塞投递，来不及读取的会被丢弃）；遇到不可恢复的错误
// （unauthorized、forbidden、invalid、not_found）时投递该错误并关闭两个通道。
// ctx 取消时两个通道都会关闭。sessionID 非空时只接收该会话的事件与全局事件。
func (c *Client) Events(ctx context.Context, since uint64, sessionID string) (<-chan event.Event, <-chan error) {
	evs := make(chan event.Event, eventsBuffer)
	errs := make(chan error, 8)
	go c.runEvents(ctx, since, sessionID, evs, errs)
	return evs, errs
}

// DecodePayload 把 Events 投递的事件载荷解码到 v（例如 *model.RunEvent、*model.Session）。
func DecodePayload(ev event.Event, v any) error {
	var raw []byte
	switch p := ev.Payload.(type) {
	case nil:
		return model.NewError(model.CodeNotFound, "事件没有载荷")
	case json.RawMessage:
		raw = p
	case []byte:
		raw = p
	default:
		b, err := json.Marshal(p)
		if err != nil {
			return model.Wrap(model.CodeInvalid, "无法编码事件载荷", err)
		}
		raw = b
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return model.Wrap(model.CodeInvalid, "无法解析事件载荷", err)
	}
	return nil
}

// streamResult 说明一次连接是怎么结束的。
type streamResult struct {
	// connected 表示收到了 200 的事件流响应。
	connected bool
	// gap 表示服务端发送了 gap 并结束响应（应立即重连）。
	gap bool
	// retry 是服务端用 retry 字段建议的重连间隔。
	retry time.Duration
}

func (c *Client) runEvents(ctx context.Context, since uint64, sessionID string, evs chan<- event.Event, errs chan error) {
	defer close(errs)
	defer close(evs)
	last := since
	attempt := 0
	var serverRetry time.Duration
	// instance 是上一次连接 hello 帧中的 started_at；为空表示还没见过（或服务端不发送 hello）。
	var instance string
	for {
		res, err := c.streamOnce(ctx, sessionID, &last, &instance, evs, errs)
		if ctx.Err() != nil {
			return
		}
		if res.retry > 0 {
			serverRetry = res.retry
		}
		if err != nil {
			if fatalStreamError(err) {
				reportFatal(errs, err)
				return
			}
			report(errs, err)
		}
		if res.connected {
			attempt = 0
		}
		var delay time.Duration
		if !res.gap {
			delay = c.backoff(attempt, serverRetry)
			attempt++
		}
		if delay > 0 {
			t := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
		}
	}
}

// backoff 返回第 attempt 次重连前的等待：min·2^attempt，上限 max，取 [d/2, d] 的随机值；
// 服务端建议的 retry 作为下限（同样不超过 max）。
func (c *Client) backoff(attempt int, serverRetry time.Duration) time.Duration {
	d := c.backoffMin
	for i := 0; i < attempt && d < c.backoffMax; i++ {
		d *= 2
	}
	d = min(d, c.backoffMax)
	d = d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
	if serverRetry > d {
		d = min(serverRetry, c.backoffMax)
	}
	return d
}

// fatalStreamError 判断事件流错误是否不可恢复（重试也不会成功）。
func fatalStreamError(err error) bool {
	switch model.CodeOf(err) {
	case model.CodeUnauthorized, model.CodeForbidden, model.CodeInvalid, model.CodeNotFound:
		return true
	}
	return false
}

// report 非阻塞地报告可恢复的错误；调用方没来得及读取时丢弃。
func report(errs chan<- error, err error) {
	select {
	case errs <- err:
	default:
	}
}

// reportFatal 保证不可恢复的错误一定进入通道且不阻塞：缓冲已满时丢弃最旧的一条可恢复错误腾出位置。
// 本函数是唯一的发送方，因此腾出的位置不会被别人占用。
func reportFatal(errs chan error, err error) {
	for {
		select {
		case errs <- err:
			return
		default:
			select {
			case <-errs:
			default:
			}
		}
	}
}

// streamOnce 建立一次连接并读取到连接结束；last 随投递的事件推进，instance 记录服务端实例（hello.started_at）。
func (c *Client) streamOnce(ctx context.Context, sessionID string, last *uint64, instance *string, evs chan<- event.Event, errs chan<- error) (streamResult, error) {
	var res streamResult
	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	wd := newWatchdog(c.idleTimeout, cancel)
	defer wd.stop()

	q := url.Values{}
	q.Set("since", strconv.FormatUint(*last, 10))
	setIf(q, "session_id", sessionID)
	req, err := http.NewRequestWithContext(connCtx, http.MethodGet, c.endpoint+"/api/v1/events?"+q.Encode(), nil)
	if err != nil {
		return res, model.Wrap(model.CodeInvalid, "无法构造事件流请求", err)
	}
	c.decorate(req, false)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	if *last > 0 {
		req.Header.Set("Last-Event-ID", strconv.FormatUint(*last, 10))
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		if wd.fired() {
			return res, model.Wrap(model.CodeTimeout, "连接事件流超时", err)
		}
		return res, transportError(ctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		apiErr, _ := decodeErrorResponse(resp)
		return res, apiErr
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		return res, model.Errorf(model.CodeUnavailable, "事件流的 Content-Type 不正确: %q", ct)
	}
	res.connected = true
	wd.reset()

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), maxEventLine)
	sc.Split(scanSSELines)
	var p sseParser
	for sc.Scan() {
		wd.reset()
		f, ok := p.feed(sc.Text())
		if f.retry > 0 {
			res.retry = f.retry
		}
		if !ok {
			continue
		}
		if f.event == eventHello {
			restarted, seq := observeHello(f.data, instance)
			if !restarted {
				continue
			}
			// 守护进程重启：本地续传位置作废，通知调用方全量刷新，并从新实例的当前位置重新订阅。
			wd.pause()
			select {
			case evs <- event.Event{Seq: seq, Type: EventGap, At: time.Now().UTC()}:
			case <-ctx.Done():
				return res, nil
			}
			*last = seq
			res.gap = true
			return res, nil
		}
		ev, isGap, perr := toEvent(f, *last)
		if perr != nil {
			report(errs, perr)
			if f.hasID {
				*last = f.id
			}
			continue
		}
		if isGap {
			res.gap = true
		}
		// 投递可能因调用方读得慢而阻塞：期间暂停空闲看门狗，避免误判为连接空闲。
		wd.pause()
		select {
		case evs <- ev:
		case <-ctx.Done():
			return res, nil
		}
		wd.reset()
		*last = ev.Seq
	}
	if ctx.Err() != nil {
		return res, nil
	}
	if err := sc.Err(); err != nil {
		if wd.fired() {
			return res, model.Errorf(model.CodeTimeout, "事件流超过 %s 没有任何数据", c.idleTimeout)
		}
		return res, &model.Error{Code: model.CodeUnavailable, Message: "事件流中断", Details: map[string]any{"reason": "stream_broken"}, Err: err}
	}
	return res, nil
}

// observeHello 解析 hello 帧并更新 instance；返回服务端实例是否与上一次连接不同（守护进程重启），
// 以及新实例当前的最新 Seq。data 无法解析或缺少 started_at 时忽略该帧。
func observeHello(data string, instance *string) (restarted bool, lastSeq uint64) {
	var h struct {
		StartedAt string `json:"started_at"`
		LastSeq   uint64 `json:"last_seq"`
	}
	if json.Unmarshal([]byte(data), &h) != nil || h.StartedAt == "" {
		return false, 0
	}
	prev := *instance
	*instance = h.StartedAt
	return prev != "" && prev != h.StartedAt, h.LastSeq
}

// wireEvent 是 data 行中 Event JSON 的解码目标；Payload 保留原始 JSON。
type wireEvent struct {
	Seq       uint64          `json:"seq"`
	Type      string          `json:"type"`
	At        time.Time       `json:"at"`
	AgentID   string          `json:"agent_id"`
	SessionID string          `json:"session_id"`
	RunID     string          `json:"run_id"`
	Payload   json.RawMessage `json:"payload"`
}

// toEvent 把一帧转成事件。gap 帧的 Seq 取帧 id（缺失时沿用 last）；
// 普通帧的 Seq 以帧 id 为准，缺失时取 data 中的 seq。
func toEvent(f sseFrame, last uint64) (event.Event, bool, error) {
	if f.event == EventGap {
		seq := last
		if f.hasID {
			seq = f.id
		}
		return event.Event{Seq: seq, Type: EventGap, At: time.Now().UTC()}, true, nil
	}
	var w wireEvent
	if err := json.Unmarshal([]byte(f.data), &w); err != nil {
		return event.Event{}, false, model.Wrap(model.CodeInvalid, "事件 data 不是合法的 JSON", err)
	}
	ev := event.Event{
		Seq: w.Seq, Type: w.Type, At: w.At,
		AgentID: w.AgentID, SessionID: w.SessionID, RunID: w.RunID,
	}
	if ev.Type == "" {
		ev.Type = f.event
	}
	if f.hasID {
		ev.Seq = f.id
	}
	if len(w.Payload) > 0 && !bytes.Equal(w.Payload, []byte("null")) {
		ev.Payload = w.Payload
	}
	return ev, false, nil
}

// sseFrame 是一条已分派的 SSE 消息。
type sseFrame struct {
	event string
	data  string
	id    uint64
	hasID bool
	retry time.Duration
}

// sseParser 按 WHATWG 规范逐行解析：event / data（多行以 \n 连接）/ id（含 NUL 或非数字时忽略）/ retry，
// 注释行与未知字段忽略，空行分派。没有 data 的帧不分派（gap 例外）。
type sseParser struct {
	event string
	data  []string
	hasD  bool
	id    uint64
	hasID bool
}

// feed 处理一行；返回的 frame 在 ok 为 true 时是一条完整消息。retry 字段即使不分派也会通过 frame.retry 返回。
func (p *sseParser) feed(line string) (sseFrame, bool) {
	if line == "" {
		defer p.reset()
		if !p.hasD && p.event != EventGap {
			return sseFrame{}, false
		}
		return sseFrame{event: p.event, data: strings.Join(p.data, "\n"), id: p.id, hasID: p.hasID}, true
	}
	if strings.HasPrefix(line, ":") {
		return sseFrame{}, false
	}
	field, value, found := strings.Cut(line, ":")
	if found {
		value = strings.TrimPrefix(value, " ")
	}
	switch field {
	case "event":
		p.event = value
	case "data":
		p.data = append(p.data, value)
		p.hasD = true
	case "id":
		if !strings.ContainsRune(value, 0) {
			if n, err := strconv.ParseUint(value, 10, 64); err == nil {
				p.id, p.hasID = n, true
			}
		}
	case "retry":
		if n, err := strconv.ParseUint(value, 10, 32); err == nil && n > 0 {
			return sseFrame{retry: time.Duration(n) * time.Millisecond}, false
		}
	}
	return sseFrame{}, false
}

func (p *sseParser) reset() {
	p.event, p.data, p.hasD, p.id, p.hasID = "", p.data[:0], false, 0, false
}

// scanSSELines 是 bufio.SplitFunc：按 \r\n、\n 或单独的 \r 切分行（SSE 允许三种行尾）。
func scanSSELines(data []byte, atEOF bool) (int, []byte, error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		if data[i] == '\n' {
			return i + 1, data[:i], nil
		}
		if i+1 < len(data) {
			if data[i+1] == '\n' {
				return i + 2, data[:i], nil
			}
			return i + 1, data[:i], nil
		}
		if atEOF {
			return i + 1, data[:i], nil
		}
		// 行尾是 \r：等下一块数据确认后面是否跟着 \n。
		return 0, nil, nil
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// watchdog 在超过 idle 没有 reset 时调用 cancel；idle 为 0 时不启用。并发安全。
type watchdog struct {
	idle   time.Duration
	mu     sync.Mutex
	timer  *time.Timer
	didRun bool
}

func newWatchdog(idle time.Duration, cancel context.CancelFunc) *watchdog {
	w := &watchdog{idle: idle}
	if idle > 0 {
		w.timer = time.AfterFunc(idle, func() {
			w.mu.Lock()
			w.didRun = true
			w.mu.Unlock()
			cancel()
		})
	}
	return w
}

func (w *watchdog) reset() {
	if w.timer != nil {
		w.timer.Reset(w.idle)
	}
}

func (w *watchdog) pause() {
	if w.timer != nil {
		w.timer.Stop()
	}
}

func (w *watchdog) stop() { w.pause() }

func (w *watchdog) fired() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.didRun
}
