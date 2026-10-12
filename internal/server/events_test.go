package server_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/event"
	"github.com/GoGoslingCA/gogosling/internal/i18n"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/server"
	"github.com/GoGoslingCA/gogosling/internal/server/servertest"
)

// sseFrame 是解析出的一帧；只有注释的帧 comment 非空。
type sseFrame struct {
	id, event, data, comment string
}

type frameReader struct{ r *bufio.Reader }

func (fr *frameReader) next() (sseFrame, error) {
	var f sseFrame
	var data []string
	seen := false
	for {
		line, err := fr.r.ReadString('\n')
		if err != nil {
			return f, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if !seen {
				continue
			}
			f.data = strings.Join(data, "\n")
			return f, nil
		}
		seen = true
		switch {
		case strings.HasPrefix(line, ":"):
			f.comment = strings.TrimSpace(line[1:])
		case strings.HasPrefix(line, "id: "):
			f.id = line[4:]
		case strings.HasPrefix(line, "event: "):
			f.event = line[7:]
		case strings.HasPrefix(line, "data: "):
			data = append(data, line[6:])
		}
	}
}

// nextEvent 跳过注释帧与连接开头的 hello 帧，返回下一条事件帧。
func (fr *frameReader) nextEvent(t *testing.T) sseFrame {
	t.Helper()
	for {
		f := fr.nextFrame(t)
		if f.event != server.EventHello {
			return f
		}
	}
}

// nextFrame 跳过注释帧，返回下一条带 event 的帧（含 hello）。
func (fr *frameReader) nextFrame(t *testing.T) sseFrame {
	t.Helper()
	for {
		f, err := fr.next()
		if err != nil {
			t.Fatalf("读取事件流失败: %v", err)
		}
		if f.event != "" {
			return f
		}
	}
}

// newStreamServer 启动真实 HTTP 服务；用 t.Cleanup 关闭，保证它晚于各事件流的断开执行。
func newStreamServer(t *testing.T, h *harness) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(h.h)
	t.Cleanup(ts.Close)
	return ts
}

// openStream 经真实 HTTP 打开事件流（5 秒后自动断开，防止测试挂起）。
func openStream(t *testing.T, ts *httptest.Server, token, query string, hdr map[string]string) (*http.Response, *frameReader) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/v1/events"+query, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("打开事件流失败: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("事件流状态码 = %d: %s", resp.StatusCode, body)
	}
	return resp, &frameReader{r: bufio.NewReader(resp.Body)}
}

func publish(bus *event.Bus, typ, sessionID string) event.Event {
	return bus.Publish(event.Event{Type: typ, SessionID: sessionID, Payload: map[string]string{"k": "v"}})
}

func TestEventsStreamFormatReplayAndResume(t *testing.T) {
	h := newHarness(t)
	ts := newStreamServer(t, h)
	bus := h.env.Bus
	for i := 0; i < 3; i++ {
		publish(bus, "test.event", "")
	}

	resp, fr := openStream(t, ts, h.env.Token, "", nil)
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q", ct)
	}
	if resp.Header.Get("Cache-Control") != "no-cache" || resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Errorf("事件流响应头不完整: %v", resp.Header)
	}
	for k := range resp.Header {
		if strings.HasPrefix(strings.ToLower(k), "access-control-") {
			t.Errorf("事件流带了 CORS 头 %s", k)
		}
	}
	for want := uint64(1); want <= 3; want++ {
		f := fr.nextEvent(t)
		if f.id != strconv.FormatUint(want, 10) || f.event != "test.event" {
			t.Fatalf("第 %d 条事件帧 = %+v", want, f)
		}
		var ev event.Event
		if err := json.Unmarshal([]byte(f.data), &ev); err != nil || ev.Seq != want || ev.Type != "test.event" {
			t.Fatalf("data 不是对应的 Event JSON: %q (%v)", f.data, err)
		}
	}
	// 实时事件。
	publish(bus, "test.live", "")
	if f := fr.nextEvent(t); f.event != "test.live" || f.id != "4" {
		t.Fatalf("实时事件 = %+v", f)
	}

	// Last-Event-ID 续传；与 since 同时给出时取较大者。
	_, fr2 := openStream(t, ts, h.env.Token, "?since=1", map[string]string{"Last-Event-ID": "3"})
	if f := fr2.nextEvent(t); f.id != "4" {
		t.Fatalf("续传应从 Seq 4 开始，实际 %+v", f)
	}
	_, fr3 := openStream(t, ts, h.env.Token, "?since=2", map[string]string{"Last-Event-ID": "garbage"})
	if f := fr3.nextEvent(t); f.id != "3" {
		t.Fatalf("非法 Last-Event-ID 应被忽略，实际 %+v", f)
	}
	mustError(t, h.do(http.MethodGet, "/api/v1/events?since=-1", nil), http.StatusBadRequest, model.CodeInvalid)
}

func TestEventsSessionFilterKeepsGlobalEvents(t *testing.T) {
	h := newHarness(t)
	ts := newStreamServer(t, h)
	publish(h.env.Bus, "a.event", "S-A")
	publish(h.env.Bus, "b.event", "S-B")
	publish(h.env.Bus, "worker.status", "")
	publish(h.env.Bus, "a.event", "S-A")

	_, fr := openStream(t, ts, h.env.Token, "?session_id=S-A", nil)
	var got []string
	for i := 0; i < 3; i++ {
		got = append(got, fr.nextEvent(t).event)
	}
	if strings.Join(got, ",") != "a.event,worker.status,a.event" {
		t.Fatalf("会话过滤结果 = %v", got)
	}
}

func TestEventsGapWhenReplayBufferOverwritten(t *testing.T) {
	h := newHarness(t, func(c *harnessConfig) { c.env.BusCapacity = 4 })
	ts := newStreamServer(t, h)
	for i := 0; i < 10; i++ {
		publish(h.env.Bus, "test.event", "")
	}

	_, fr := openStream(t, ts, h.env.Token, "?since=2", nil)
	f := fr.nextEvent(t)
	if f.event != server.EventGap || f.id != "10" {
		t.Fatalf("应收到 id 为最新 Seq 的 gap，实际 %+v", f)
	}
	var ev event.Event
	if err := json.Unmarshal([]byte(f.data), &ev); err != nil || ev.Type != server.EventGap || ev.Seq != 10 {
		t.Fatalf("gap 的 data = %q (%v)", f.data, err)
	}
	if _, err := fr.next(); err != io.EOF {
		t.Fatalf("gap 之后应结束响应，实际 %v", err)
	}

	// 从 gap 给出的位置续传不会再次 gap。
	_, fr2 := openStream(t, ts, h.env.Token, "?since=10", nil)
	publish(h.env.Bus, "after.gap", "")
	if f := fr2.nextEvent(t); f.event != "after.gap" || f.id != "11" {
		t.Fatalf("续传后的事件 = %+v", f)
	}

	// 客户端的位置超前于本实例（守护进程重启）→ gap。
	_, fr3 := openStream(t, ts, h.env.Token, "?since=5000", nil)
	if f := fr3.nextEvent(t); f.event != server.EventGap || f.id != "11" {
		t.Fatalf("超前的 since 应得到 gap，实际 %+v", f)
	}
}

func TestEventsKeepalive(t *testing.T) {
	h := newHarness(t)
	server.SetKeepaliveForTest(h.srv, 20*time.Millisecond)
	ts := newStreamServer(t, h)
	_, fr := openStream(t, ts, h.env.Token, "", nil)
	for {
		f, err := fr.next()
		if err != nil {
			t.Fatalf("读取失败: %v", err)
		}
		if f.comment == "keepalive" {
			return
		}
	}
}

func TestEventsUnavailableWithoutBus(t *testing.T) {
	h := newHarness(t)
	deps := h.env.Deps
	deps.Bus = nil
	srv, err := server.New(deps, server.Options{})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil)
	req.Host = testHost
	req.Header.Set("Authorization", "Bearer "+h.env.Token)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	mustError(t, rec, http.StatusServiceUnavailable, model.CodeUnavailable)
}

func TestEventsRunLifecycleAndLocalizedFailure(t *testing.T) {
	h := newHarness(t)
	ts := newStreamServer(t, h)
	a := h.seedAgent("事件")
	sess := h.seedSession(a.ID)
	_, fr := openStream(t, ts, h.env.Token, "?session_id="+sess.ID, map[string]string{"Accept-Language": "en"})

	run := h.send(sess.ID, servertest.ScriptFail)
	for {
		f := fr.nextEvent(t)
		if f.event != string(model.EventRunFailed) {
			continue
		}
		var ev struct {
			RunID   string         `json:"run_id"`
			Payload model.RunEvent `json:"payload"`
		}
		if err := json.Unmarshal([]byte(f.data), &ev); err != nil {
			t.Fatal(err)
		}
		if ev.RunID != run.ID || ev.Payload.Error != i18n.T("en", "run.error.run_timeout", nil) {
			t.Fatalf("run.failed 的错误应本地化: %+v", ev)
		}
		return
	}
}

// blockingWriter 模拟卡住的客户端：paused 时 Write 阻塞，用于制造慢消费。
type blockingWriter struct {
	header http.Header
	mu     sync.Mutex
	cond   *sync.Cond
	paused bool
	// waiting 是正阻塞在 Write 里的调用数。
	waiting int
	buf     bytes.Buffer
}

func newBlockingWriter() *blockingWriter {
	b := &blockingWriter{header: http.Header{}}
	b.cond = sync.NewCond(&b.mu)
	return b
}

func (b *blockingWriter) Header() http.Header { return b.header }
func (b *blockingWriter) WriteHeader(int)     {}
func (b *blockingWriter) Flush()              {}

func (b *blockingWriter) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for b.paused {
		b.waiting++
		b.cond.Broadcast()
		b.cond.Wait()
		b.waiting--
	}
	return b.buf.Write(p)
}

func (b *blockingWriter) setPaused(p bool) {
	b.mu.Lock()
	b.paused = p
	b.cond.Broadcast()
	b.mu.Unlock()
}

func (b *blockingWriter) waitBlocked(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	b.mu.Lock()
	defer b.mu.Unlock()
	for b.waiting == 0 {
		if time.Now().After(deadline) {
			t.Fatal("处理器没有阻塞在写出上")
		}
		b.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
		b.mu.Lock()
	}
}

func (b *blockingWriter) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestEventsSlowConsumerIsResubscribedWithoutGap(t *testing.T) {
	h := newHarness(t)
	server.SetSSEBufferForTest(h.srv, 1)
	bw := newBlockingWriter()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil).WithContext(ctx)
	req.Host = testHost
	req.Header.Set("Authorization", "Bearer "+h.env.Token)
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.h.ServeHTTP(bw, req)
	}()
	eventually(t, "事件流握手", func() bool { return strings.Contains(bw.String(), ": connected") })
	time.Sleep(20 * time.Millisecond) // 让处理器完成订阅

	bw.setPaused(true)
	publish(h.env.Bus, "slow.event", "")
	bw.waitBlocked(t)
	// 处理器卡在写第 1 条：缓冲为 1，第 3 条起溢出，订阅被总线踢掉。
	for i := 0; i < 9; i++ {
		publish(h.env.Bus, "slow.event", "")
	}
	bw.setPaused(false)

	eventually(t, "收到全部 10 条事件", func() bool { return strings.Contains(bw.String(), "id: 10\n") })
	out := bw.String()
	if strings.Contains(out, "event: gap") {
		t.Fatalf("事件仍在回放缓冲内时不应发送 gap:\n%s", out)
	}
	last := 0
	for _, line := range strings.Split(out, "\n") {
		if id, ok := strings.CutPrefix(line, "id: "); ok {
			n, _ := strconv.Atoi(id)
			if n != last+1 {
				t.Fatalf("事件 Seq 不连续：%d 之后是 %d\n%s", last, n, out)
			}
			last = n
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("请求取消后事件流没有结束")
	}
}
