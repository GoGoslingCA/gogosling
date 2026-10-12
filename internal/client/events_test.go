package client

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/event"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

// parseAll 用与生产代码相同的切分与解析逻辑处理一段原始事件流。
func parseAll(t *testing.T, raw string) ([]sseFrame, []time.Duration) {
	t.Helper()
	sc := bufio.NewScanner(strings.NewReader(raw))
	sc.Split(scanSSELines)
	var p sseParser
	var frames []sseFrame
	var retries []time.Duration
	for sc.Scan() {
		f, ok := p.feed(sc.Text())
		if f.retry > 0 {
			retries = append(retries, f.retry)
		}
		if ok {
			frames = append(frames, f)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return frames, retries
}

func TestSSEParser(t *testing.T) {
	raw := ": connected\n\n" +
		"id: 1\nevent: a\ndata: {\"x\":1}\n\n" +
		"id: 2\r\nevent: b\r\ndata: line1\r\ndata: line2\r\n\r\n" +
		"id: 3\revent: c\rdata:no-space\r\r" +
		"retry: 1500\n\n" +
		"id: 4\u0000\nevent: d\ndata: x\nunknown: y\n\n" +
		"event: empty\n\n" +
		"event: gap\nid: 9\n\n" +
		"data: tail-without-blank-line"
	frames, retries := parseAll(t, raw)
	if len(frames) != 5 {
		t.Fatalf("应分派 5 帧，实际 %d: %+v", len(frames), frames)
	}
	want := []sseFrame{
		{event: "a", data: `{"x":1}`, id: 1, hasID: true},
		{event: "b", data: "line1\nline2", id: 2, hasID: true},
		{event: "c", data: "no-space", id: 3, hasID: true},
		{event: "d", data: "x"},
		{event: "gap", id: 9, hasID: true},
	}
	for i, w := range want {
		if frames[i] != w {
			t.Errorf("第 %d 帧 = %+v，期望 %+v", i, frames[i], w)
		}
	}
	if len(retries) != 1 || retries[0] != 1500*time.Millisecond {
		t.Errorf("retry = %v", retries)
	}
}

func TestBackoffIsBounded(t *testing.T) {
	c := New("http://x", "t")
	for attempt := 0; attempt < 20; attempt++ {
		d := c.backoff(attempt, 0)
		if d <= 0 || d > DefaultBackoffMax {
			t.Fatalf("第 %d 次退避 %v 超出 (0, 5s]", attempt, d)
		}
	}
	if d := c.backoff(0, time.Hour); d != DefaultBackoffMax {
		t.Fatalf("服务端建议的 retry 也不能超过上限，实际 %v", d)
	}
	if d := c.backoff(10, 0); d < DefaultBackoffMax/2 {
		t.Fatalf("多次失败后退避应接近上限，实际 %v", d)
	}
}

func TestDecodePayload(t *testing.T) {
	ev := event.Event{Type: "run.completed", Payload: json.RawMessage(`{"type":"run.completed","run_id":"R1"}`)}
	var re model.RunEvent
	if err := DecodePayload(ev, &re); err != nil || re.RunID != "R1" {
		t.Fatalf("解码失败: %+v %v", re, err)
	}
	if err := DecodePayload(event.Event{}, &re); !model.IsCode(err, model.CodeNotFound) {
		t.Fatalf("没有载荷应返回 not_found，实际 %v", err)
	}
}

// sseScript 是一个按连接序号回放脚本的假事件流服务。
type sseScript struct {
	mu    sync.Mutex
	conns []*http.Request
	// steps[i] 处理第 i 个连接；超出时保持连接直到客户端断开。
	steps []func(w http.ResponseWriter, r *http.Request, f http.Flusher)
}

func (s *sseScript) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	idx := len(s.conns)
	s.conns = append(s.conns, r.Clone(context.Background()))
	s.mu.Unlock()
	f := w.(http.Flusher)
	if idx < len(s.steps) {
		s.steps[idx](w, r, f)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = fmt.Fprint(w, ": idle\n\n")
	f.Flush()
	<-r.Context().Done()
}

func (s *sseScript) conn(i int) *http.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i >= len(s.conns) {
		return nil
	}
	return s.conns[i]
}

func writeEvent(w http.ResponseWriter, seq uint64, typ string) {
	data, _ := json.Marshal(event.Event{Seq: seq, Type: typ, SessionID: "S", Payload: map[string]any{"n": seq}})
	_, _ = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", seq, typ, data)
}

func streamHeader(w http.ResponseWriter, f http.Flusher) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, ": connected\n\n")
	f.Flush()
}

func recv(t *testing.T, ch <-chan event.Event) event.Event {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("事件通道意外关闭")
		}
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("等待事件超时")
	}
	return event.Event{}
}

func TestEventsReconnectResumeAndGap(t *testing.T) {
	script := &sseScript{steps: []func(http.ResponseWriter, *http.Request, http.Flusher){
		func(w http.ResponseWriter, _ *http.Request, f http.Flusher) {
			streamHeader(w, f)
			writeEvent(w, 1, "a")
			writeEvent(w, 2, "b")
			f.Flush()
			// 返回即断开连接。
		},
		func(w http.ResponseWriter, _ *http.Request, _ http.Flusher) {
			w.WriteHeader(http.StatusServiceUnavailable) // 一次可恢复的失败
		},
		func(w http.ResponseWriter, _ *http.Request, f http.Flusher) {
			streamHeader(w, f)
			writeEvent(w, 3, "c")
			_, _ = fmt.Fprint(w, "id: 10\nevent: gap\ndata: {\"type\":\"gap\",\"seq\":10}\n\n")
			f.Flush()
		},
		func(w http.ResponseWriter, _ *http.Request, f http.Flusher) {
			streamHeader(w, f)
			writeEvent(w, 11, "d")
			f.Flush()
		},
	}}
	srv := httptest.NewServer(script)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	c := New(srv.URL, "tok", WithReconnectBackoff(5*time.Millisecond, 20*time.Millisecond))
	evs, errs := c.Events(ctx, 0, "S")

	var got []string
	for i := 0; i < 5; i++ {
		ev := recv(t, evs)
		got = append(got, fmt.Sprintf("%s@%d", ev.Type, ev.Seq))
		if ev.Type != EventGap {
			var p map[string]any
			if err := DecodePayload(ev, &p); err != nil || p["n"] != float64(ev.Seq) {
				t.Fatalf("载荷 = %v %v", p, err)
			}
		}
	}
	if strings.Join(got, ",") != "a@1,b@2,c@3,gap@10,d@11" {
		t.Fatalf("事件序列 = %v", got)
	}

	checks := []struct {
		conn            int
		since, lastID   string
		sessionIDNeeded bool
	}{
		{0, "0", "", true},
		{1, "2", "2", true},
		{2, "2", "2", true},
		{3, "10", "10", true},
	}
	for _, ck := range checks {
		r := script.conn(ck.conn)
		if r == nil {
			t.Fatalf("没有第 %d 个连接", ck.conn)
		}
		if r.URL.Query().Get("since") != ck.since || r.Header.Get("Last-Event-ID") != ck.lastID {
			t.Errorf("第 %d 个连接的续传位置 since=%q Last-Event-ID=%q，期望 %q/%q",
				ck.conn, r.URL.Query().Get("since"), r.Header.Get("Last-Event-ID"), ck.since, ck.lastID)
		}
		if r.URL.Query().Get("session_id") != "S" || r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("第 %d 个连接缺少 session_id 或令牌", ck.conn)
		}
	}
	select {
	case err := <-errs:
		if !model.IsCode(err, model.CodeUnavailable) {
			t.Errorf("可恢复错误应以 unavailable 报告，实际 %v", err)
		}
	default:
		t.Error("应报告一次可恢复的连接错误")
	}

	cancel()
	for range evs {
	}
	for range errs {
	}
}

func TestEventsFatalErrorClosesChannels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"no"}}`))
	}))
	defer srv.Close()
	evs, errs := New(srv.URL, "bad").Events(context.Background(), 0, "")
	select {
	case err := <-errs:
		if !model.IsCode(err, model.CodeUnauthorized) {
			t.Fatalf("应投递 unauthorized，实际 %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("没有投递不可恢复的错误")
	}
	select {
	case _, ok := <-evs:
		if ok {
			t.Fatal("不应收到事件")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("事件通道没有关闭")
	}
}

func TestEventsIdleTimeoutReconnects(t *testing.T) {
	script := &sseScript{steps: []func(http.ResponseWriter, *http.Request, http.Flusher){
		func(w http.ResponseWriter, r *http.Request, f http.Flusher) {
			streamHeader(w, f)
			writeEvent(w, 1, "a")
			f.Flush()
			<-r.Context().Done() // 之后一直沉默，直到客户端放弃
		},
		func(w http.ResponseWriter, _ *http.Request, f http.Flusher) {
			streamHeader(w, f)
			writeEvent(w, 2, "b")
			f.Flush()
		},
	}}
	srv := httptest.NewServer(script)
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := New(srv.URL, "t", WithIdleTimeout(100*time.Millisecond), WithReconnectBackoff(5*time.Millisecond, 10*time.Millisecond))
	evs, errs := c.Events(ctx, 0, "")
	if ev := recv(t, evs); ev.Seq != 1 {
		t.Fatalf("第一条事件 = %+v", ev)
	}
	if ev := recv(t, evs); ev.Seq != 2 {
		t.Fatalf("空闲超时后应重连并续传，实际 %+v", ev)
	}
	if err := <-errs; !model.IsCode(err, model.CodeTimeout) {
		t.Fatalf("空闲超时应以 timeout 报告，实际 %v", err)
	}
	if r := script.conn(1); r == nil || r.Header.Get("Last-Event-ID") != "1" {
		t.Fatal("重连应带上 Last-Event-ID: 1")
	}
}

func TestEventsContextCancelClosesChannels(t *testing.T) {
	srv := httptest.NewServer(&sseScript{})
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	evs, errs := New(srv.URL, "t").Events(ctx, 0, "")
	time.Sleep(50 * time.Millisecond)
	cancel()
	deadline := time.After(5 * time.Second)
	for evs != nil || errs != nil {
		select {
		case _, ok := <-evs:
			if !ok {
				evs = nil
			}
		case _, ok := <-errs:
			if !ok {
				errs = nil
			}
		case <-deadline:
			t.Fatal("ctx 取消后通道没有关闭")
		}
	}
}

func TestFatalErrorNeverBlocksWhenErrorsUnread(t *testing.T) {
	errs := make(chan error, 2)
	report(errs, model.NewError(model.CodeUnavailable, "1"))
	report(errs, model.NewError(model.CodeUnavailable, "2"))
	report(errs, model.NewError(model.CodeUnavailable, "3")) // 缓冲已满，被丢弃
	done := make(chan struct{})
	go func() {
		reportFatal(errs, model.NewError(model.CodeUnauthorized, "fatal"))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("缓冲已满时投递不可恢复的错误不应阻塞")
	}
	var codes []model.Code
	for len(errs) > 0 {
		codes = append(codes, model.CodeOf(<-errs))
	}
	if len(codes) != 2 || codes[1] != model.CodeUnauthorized {
		t.Fatalf("不可恢复的错误应排在最后且保留，实际 %v", codes)
	}
}

func writeHello(w http.ResponseWriter, startedAt string, lastSeq uint64) {
	_, _ = fmt.Fprintf(w, "event: hello\ndata: {\"started_at\":%q,\"last_seq\":%d}\n\n", startedAt, lastSeq)
}

// TestEventsDetectsDaemonRestartViaHello 覆盖 CONC-6：新实例的 Seq 已追上旧 since 时，
// 服务端单凭 Seq 无法识别重启；客户端比较 hello.started_at，丢弃旧位置并投递 gap。
func TestEventsDetectsDaemonRestartViaHello(t *testing.T) {
	script := &sseScript{steps: []func(http.ResponseWriter, *http.Request, http.Flusher){
		func(w http.ResponseWriter, _ *http.Request, f http.Flusher) { // 旧实例
			streamHeader(w, f)
			writeHello(w, "2026-10-10T08:00:00Z", 2)
			writeEvent(w, 1, "a")
			writeEvent(w, 2, "b")
			f.Flush()
		},
		func(w http.ResponseWriter, _ *http.Request, f http.Flusher) { // 同一实例重连：正常续传
			streamHeader(w, f)
			writeHello(w, "2026-10-10T08:00:00Z", 3)
			writeEvent(w, 3, "c")
			f.Flush()
		},
		func(w http.ResponseWriter, _ *http.Request, f http.Flusher) { // 守护进程重启，新实例 Seq 已到 400
			streamHeader(w, f)
			writeHello(w, "2026-10-10T09:00:00Z", 400)
			writeEvent(w, 4, "stale") // 按旧 since 回放的新实例事件：不应投递
			f.Flush()
		},
		func(w http.ResponseWriter, _ *http.Request, f http.Flusher) {
			streamHeader(w, f)
			writeHello(w, "2026-10-10T09:00:00Z", 401)
			writeEvent(w, 401, "fresh")
			f.Flush()
		},
	}}
	srv := httptest.NewServer(script)
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := New(srv.URL, "tok", WithReconnectBackoff(5*time.Millisecond, 10*time.Millisecond))
	evs, _ := c.Events(ctx, 0, "")

	var got []string
	for i := 0; i < 5; i++ {
		ev := recv(t, evs)
		got = append(got, fmt.Sprintf("%s@%d", ev.Type, ev.Seq))
	}
	if strings.Join(got, ",") != "a@1,b@2,c@3,gap@400,fresh@401" {
		t.Fatalf("事件序列 = %v（hello 不应投递，重启后应投递 gap 并丢弃按旧位置回放的事件）", got)
	}
	if r := script.conn(3); r == nil || r.URL.Query().Get("since") != "400" || r.Header.Get("Last-Event-ID") != "400" {
		t.Fatalf("发现重启后应从新实例的当前位置 400 重新订阅，实际 %v", r)
	}
	if r := script.conn(1); r == nil || r.URL.Query().Get("since") != "2" {
		t.Fatal("hello 帧不应改变续传位置")
	}
}

func TestObserveHello(t *testing.T) {
	var inst string
	if restarted, _ := observeHello(`{"started_at":"A","last_seq":5}`, &inst); restarted || inst != "A" {
		t.Fatalf("第一次见到实例不算重启，instance = %q", inst)
	}
	if restarted, _ := observeHello(`{"started_at":"A","last_seq":9}`, &inst); restarted {
		t.Fatal("同一实例不算重启")
	}
	if restarted, seq := observeHello(`{"started_at":"B","last_seq":7}`, &inst); !restarted || seq != 7 || inst != "B" {
		t.Fatalf("实例变化应判定为重启并给出新位置，实际 %v %d %q", restarted, seq, inst)
	}
	for _, bad := range []string{`not json`, `{"last_seq":1}`, `{"started_at":""}`} {
		if restarted, _ := observeHello(bad, &inst); restarted || inst != "B" {
			t.Fatalf("无法解析的 hello %q 应被忽略", bad)
		}
	}
}
