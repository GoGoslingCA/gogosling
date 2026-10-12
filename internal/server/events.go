package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/event"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

// EventGap 是事件流中"有事件丢失、请全量刷新"的事件类型。
const EventGap = "gap"

// EventHello 是每条事件流连接的第一帧（在任何回放之前）：data 为 {"started_at","last_seq"}，不带 id
// （不推进客户端的续传位置）。started_at 标识守护进程实例：客户端重连时发现它与上次不同，
// 说明守护进程重启过、旧 Seq 已无意义，应丢弃本地续传位置并全量刷新。
const EventHello = "hello"

// helloData 是 hello 帧的 data。
type helloData struct {
	StartedAt string `json:"started_at"`
	LastSeq   uint64 `json:"last_seq"`
}

// pumpResult 说明一次订阅为什么结束。
type pumpResult int

const (
	// pumpDone：客户端断开、请求取消或服务器关闭，结束响应。
	pumpDone pumpResult = iota
	// pumpLost：订阅因慢消费被总线踢掉；从最后写出的 Seq 重新订阅。
	pumpLost
)

// handleEvents 实现 GET /api/v1/events（Server-Sent Events）。
//
// 每条连接先写一帧 event: hello（实例启动时间与当前最新 Seq，见 EventHello），再开始回放。
// 续传位置取 since 查询参数与 Last-Event-ID 头中较大者。订阅建立时若回放缓冲已不含该位置之后的事件，
// 或客户端的 Seq 超前于本实例（since 大于当前最新 Seq：守护进程重启后 Seq 从头计数），先发 event: gap
// （id 为当前最新 Seq，客户端全量刷新后从它续传）再结束响应。新实例的 Seq 已经追上旧 since 的情形
// 服务端无法单凭 Seq 识别，由客户端比较 hello.started_at 发现。订阅中途因慢消费被踢时，服务端从最后
// 写出的 Seq 重新订阅：事件仍在回放缓冲内时对客户端无感，否则同样发送 gap。
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) error {
	bus := s.deps.Bus
	if bus == nil {
		return model.NewError(model.CodeUnavailable, "事件总线未启用")
	}
	since, err := eventsSince(r)
	if err != nil {
		return err
	}
	var filter event.Filter
	if sid := strings.TrimSpace(r.URL.Query().Get("session_id")); sid != "" {
		// 会话过滤：本会话的事件，以及不属于任何会话的全局事件（worker.status 等）。
		filter = func(ev event.Event) bool { return ev.SessionID == "" || ev.SessionID == sid }
	}

	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)

	st := &sseWriter{w: w, rc: rc, locale: s.localeOf(r), log: s.log}
	if st.comment("connected") != nil || st.hello(s.opts.StartedAt, bus.LastSeq()) != nil || st.flush() != nil {
		return nil
	}

	ctx := r.Context()
	keep := time.NewTicker(s.keepalive)
	defer keep.Stop()

	last := since
	for {
		if last > bus.LastSeq() {
			// 客户端的位置超前于本实例：守护进程重启过，之前的事件已无从回放。
			_ = st.gap(bus.LastSeq())
			return nil
		}
		// 每个订阅用独立的子上下文，重新订阅时旧订阅的看守 goroutine 随之退出。
		subCtx, cancel := context.WithCancel(ctx)
		sub := bus.Subscribe(subCtx, last, s.sseBuffer, filter)
		if sub.Lost() && len(sub.C) == 0 {
			// 订阅一建立就丢失且没有任何回放：回放缓冲已覆盖续传位置。
			sub.Cancel()
			cancel()
			_ = st.gap(bus.LastSeq())
			return nil
		}
		res := s.pump(ctx, st, sub, &last, keep.C)
		sub.Cancel()
		cancel()
		if res != pumpLost {
			return nil
		}
		s.log.Debug("事件流订阅因慢消费被踢，按最后写出的 Seq 重新订阅", "last_seq", last)
	}
}

// pump 把订阅中的事件写给客户端，直到订阅关闭、客户端断开或服务器关闭。
// 每次至多合并 sseBatch 条事件再 Flush，减少系统调用。
func (s *Server) pump(ctx context.Context, st *sseWriter, sub *event.Subscription, last *uint64, keep <-chan time.Time) pumpResult {
	for {
		select {
		case ev, ok := <-sub.C:
			if !ok {
				return lostOrDone(sub)
			}
			if st.event(ev) != nil {
				return pumpDone
			}
			*last = ev.Seq
			closed := false
		batch:
			for i := 1; i < sseBatch; i++ {
				select {
				case next, more := <-sub.C:
					if !more {
						closed = true
						break batch
					}
					if st.event(next) != nil {
						return pumpDone
					}
					*last = next.Seq
				default:
					break batch
				}
			}
			if st.flush() != nil {
				return pumpDone
			}
			if closed {
				return lostOrDone(sub)
			}
		case <-keep:
			if st.comment("keepalive") != nil || st.flush() != nil {
				return pumpDone
			}
		case <-ctx.Done():
			return pumpDone
		case <-s.closing:
			return pumpDone
		}
	}
}

func lostOrDone(sub *event.Subscription) pumpResult {
	if sub.Lost() {
		return pumpLost
	}
	return pumpDone
}

// eventsSince 取 since 查询参数与 Last-Event-ID 头中较大者；查询参数非法返回 invalid，头非法则忽略。
func eventsSince(r *http.Request) (uint64, error) {
	var since uint64
	if v := r.URL.Query().Get("since"); v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return 0, badValue("since", v)
		}
		since = n
	}
	if v := strings.TrimSpace(r.Header.Get("Last-Event-ID")); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil && n > since {
			since = n
		}
	}
	return since, nil
}

// sseWriter 负责事件流的帧格式与每次写出的期限。
type sseWriter struct {
	w      http.ResponseWriter
	rc     *http.ResponseController
	locale string
	log    *slog.Logger
}

func (st *sseWriter) arm() {
	_ = st.rc.SetWriteDeadline(time.Now().Add(sseWriteTimeout))
}

func (st *sseWriter) flush() error {
	st.arm()
	return st.rc.Flush()
}

// comment 写一条注释行（保活或握手），客户端解析器会忽略它。
func (st *sseWriter) comment(text string) error {
	st.arm()
	_, err := io.WriteString(st.w, ": "+text+"\n\n")
	return err
}

// event 写出一条事件：id 为 Seq，event 为类型，data 为 Event 的单行 JSON。
func (st *sseWriter) event(ev event.Event) error {
	ev = localizeEvent(st.locale, ev)
	data, err := json.Marshal(ev)
	if err != nil {
		// 载荷无法序列化时仍发出信封（不带 payload），保证 Seq 连续、客户端能据此刷新。
		st.log.Warn("事件载荷无法序列化，去掉载荷后发送", "type", ev.Type, "seq", ev.Seq, "error", err)
		ev.Payload = nil
		if data, err = json.Marshal(ev); err != nil {
			return nil
		}
	}
	st.arm()
	var b strings.Builder
	b.Grow(len(data) + 64)
	b.WriteString("id: ")
	b.WriteString(strconv.FormatUint(ev.Seq, 10))
	b.WriteString("\nevent: ")
	b.WriteString(sanitizeField(ev.Type))
	b.WriteString("\ndata: ")
	b.Write(data)
	b.WriteString("\n\n")
	_, err = io.WriteString(st.w, b.String())
	return err
}

// hello 写出连接的第一帧（不带 id，不推进客户端的 Last-Event-ID）。
func (st *sseWriter) hello(startedAt string, lastSeq uint64) error {
	data, _ := json.Marshal(helloData{StartedAt: startedAt, LastSeq: lastSeq})
	st.arm()
	_, err := io.WriteString(st.w, "event: "+EventHello+"\ndata: "+string(data)+"\n\n")
	return err
}

// gap 发送 gap 事件：id 为续传位置（当前最新 Seq），data 为 {"type":"gap","seq":N,"at":...}。
func (st *sseWriter) gap(seq uint64) error {
	data, _ := json.Marshal(event.Event{Seq: seq, Type: EventGap, At: time.Now().UTC()})
	st.arm()
	_, err := io.WriteString(st.w, "id: "+strconv.FormatUint(seq, 10)+"\nevent: "+EventGap+"\ndata: "+string(data)+"\n\n")
	if err != nil {
		return err
	}
	return st.flush()
}

// sanitizeField 去掉事件类型中的换行，防止破坏帧结构。
func sanitizeField(s string) string {
	if !strings.ContainsAny(s, "\r\n") {
		return s
	}
	return strings.NewReplacer("\r", "", "\n", "").Replace(s)
}

// localizeEvent 把 run.failed 载荷中的机器常量错误换成请求语言的文案（与 GET /runs/{id} 一致）。
func localizeEvent(locale string, ev event.Event) event.Event {
	if ev.Type != string(model.EventRunFailed) {
		return ev
	}
	switch p := ev.Payload.(type) {
	case model.RunEvent:
		p.Error = localizeRunError(locale, p.Error)
		ev.Payload = p
	case *model.RunEvent:
		if p != nil {
			cp := *p
			cp.Error = localizeRunError(locale, cp.Error)
			ev.Payload = cp
		}
	}
	return ev
}
