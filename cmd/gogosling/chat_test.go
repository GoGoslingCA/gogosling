package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/client"
	"github.com/GoGoslingCA/gogosling/internal/event"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

// fakeServer 是按 OpenAPI 契约实现的最小假守护进程：会话、发送、取消、审批与 SSE 事件流。
// 运行的行为由消息文本决定：普通文本直接回答；"#tool" 请求审批后执行；"#slow" 挂起直到取消。
type fakeServer struct {
	t   *testing.T
	srv *httptest.Server

	mu        sync.Mutex
	seq       uint64
	events    []event.Event
	notify    chan struct{}
	runs      map[string]*model.Run
	decisions chan model.Decision
	decided   []model.Decision
	cancels   chan string
	// ignoreCancel 为 true 时取消请求不产生 run.cancelled（模拟卡住的运行）。
	ignoreCancel bool
	// hideApproval 为 true 时不发布 approval.requested，只能从 GET /approvals 看到（模拟错过事件）。
	hideApproval bool
	pending      []model.ApprovalRequest
	// origins 记录每次 POST /sessions 请求体里的 origin。
	origins []string
}

const (
	fakeAgentID   = "01JAGENT0000000000000000AA"
	fakeSessionID = "01JSESSION00000000000000AA"
)

func newFakeServer(t *testing.T) *fakeServer {
	fs := &fakeServer{
		t: t, notify: make(chan struct{}), runs: map[string]*model.Run{},
		decisions: make(chan model.Decision, 4), cancels: make(chan string, 4),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSONResp(w, 200, map[string]string{"status": "ok"}) })
	agent := model.Agent{ID: fakeAgentID, Name: "助理", Autonomy: model.AutonomyAskRisky, Enabled: true}
	sess := model.Session{ID: fakeSessionID, AgentID: fakeAgentID, Origin: model.OriginCLI, Status: model.SessionActive}
	mux.HandleFunc("GET /api/v1/agents", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResp(w, 200, map[string]any{"items": []model.Agent{agent}})
	})
	mux.HandleFunc("GET /api/v1/agents/{id}", func(w http.ResponseWriter, _ *http.Request) { writeJSONResp(w, 200, agent) })
	mux.HandleFunc("POST /api/v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Origin string `json:"origin"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		fs.mu.Lock()
		fs.origins = append(fs.origins, body.Origin)
		fs.mu.Unlock()
		writeJSONResp(w, 201, sess)
	})
	mux.HandleFunc("GET /api/v1/sessions/{id}", func(w http.ResponseWriter, _ *http.Request) { writeJSONResp(w, 200, sess) })
	mux.HandleFunc("GET /api/v1/approvals", func(w http.ResponseWriter, _ *http.Request) {
		fs.mu.Lock()
		items := append([]model.ApprovalRequest{}, fs.pending...)
		fs.mu.Unlock()
		writeJSONResp(w, 200, map[string]any{"items": items})
	})
	mux.HandleFunc("POST /api/v1/sessions/{id}/messages", fs.handleSend)
	mux.HandleFunc("GET /api/v1/runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		fs.mu.Lock()
		run := *fs.runs[r.PathValue("id")]
		fs.mu.Unlock()
		writeJSONResp(w, 200, run)
	})
	mux.HandleFunc("POST /api/v1/runs/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		fs.cancels <- id
		fs.mu.Lock()
		run := *fs.runs[id]
		ignore := fs.ignoreCancel
		fs.mu.Unlock()
		if !ignore {
			fs.finish(id, model.RunEvent{Type: model.EventRunCancelled}, model.RunCancelled)
		}
		writeJSONResp(w, 200, run)
	})
	mux.HandleFunc("POST /api/v1/approvals/{id}/decision", func(w http.ResponseWriter, r *http.Request) {
		var d model.Decision
		_ = json.NewDecoder(r.Body).Decode(&d)
		fs.mu.Lock()
		fs.decided = append(fs.decided, d)
		fs.mu.Unlock()
		fs.decisions <- d
		status := model.ApprovalDenied
		if d.Approve {
			status = model.ApprovalApproved
		}
		writeJSONResp(w, 200, model.ApprovalRequest{ID: r.PathValue("id"), Status: status, Scope: d.Scope})
	})
	mux.HandleFunc("GET /api/v1/events", fs.handleEvents)
	fs.srv = httptest.NewServer(mux)
	t.Cleanup(fs.srv.Close)
	return fs
}

func writeJSONResp(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// publish 追加一条运行事件并唤醒所有事件流。
func (fs *fakeServer) publish(runID string, ev model.RunEvent) {
	fs.mu.Lock()
	fs.seq++
	ev.RunID, ev.SessionID, ev.AgentID, ev.At = runID, fakeSessionID, fakeAgentID, time.Now().UTC()
	raw, _ := json.Marshal(ev)
	fs.events = append(fs.events, event.Event{Seq: fs.seq, Type: string(ev.Type), At: ev.At, SessionID: fakeSessionID, RunID: runID, Payload: json.RawMessage(raw)})
	close(fs.notify)
	fs.notify = make(chan struct{})
	fs.mu.Unlock()
}

func (fs *fakeServer) finish(runID string, ev model.RunEvent, status model.RunStatus) {
	fs.mu.Lock()
	if run := fs.runs[runID]; run != nil {
		run.Status = status
		if ev.Usage != nil {
			run.Usage = *ev.Usage
		}
	}
	fs.mu.Unlock()
	fs.publish(runID, ev)
}

func (fs *fakeServer) handleSend(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	fs.mu.Lock()
	id := fmt.Sprintf("01JRUN%020d", len(fs.runs)+1)
	run := &model.Run{ID: id, SessionID: fakeSessionID, AgentID: fakeAgentID, Status: model.RunQueued, Trigger: model.TriggerUser}
	fs.runs[id] = run
	snapshot := *run
	fs.mu.Unlock()
	writeJSONResp(w, http.StatusAccepted, snapshot)
	go fs.script(id, body.Text)
}

func (fs *fakeServer) script(id, text string) {
	usage := &model.Usage{InputTokens: 12, OutputTokens: 5}
	fs.publish(id, model.RunEvent{Type: model.EventRunStarted})
	switch {
	case strings.Contains(text, "#slow"):
		fs.publish(id, model.RunEvent{Type: model.EventTextDelta, Delta: "开始…"})
	case strings.Contains(text, "#tool"):
		call := model.ToolCall{ID: "call_1", Name: "run_command", Arguments: json.RawMessage(`{"command": "echo hi"}`)}
		fs.publish(id, model.RunEvent{Type: model.EventToolCallStarted, ToolCall: &call})
		ap := model.ApprovalRequest{ID: "01JAPPROVAL0000000000000AA", RunID: id, ToolCall: call, Risk: model.RiskExecute, Summary: "执行命令 echo hi", Status: model.ApprovalPending}
		fs.mu.Lock()
		hide := fs.hideApproval
		if hide {
			fs.pending = append(fs.pending, ap)
		}
		fs.mu.Unlock()
		if !hide {
			fs.publish(id, model.RunEvent{Type: model.EventApprovalRequested, Approval: &ap})
		}
		d := <-fs.decisions
		res := model.ToolResult{CallID: "call_1", Content: "hi\n"}
		reply := "完成"
		if !d.Approve {
			res = model.ToolResult{CallID: "call_1", Content: "denied by user", IsError: true}
			reply = "好的，不执行"
		}
		fs.publish(id, model.RunEvent{Type: model.EventToolCallCompleted, ToolResult: &res})
		fs.publish(id, model.RunEvent{Type: model.EventTextDelta, Delta: reply})
		fs.finish(id, model.RunEvent{Type: model.EventRunCompleted, Usage: usage}, model.RunCompleted)
	default:
		fs.publish(id, model.RunEvent{Type: model.EventReasoningDelta, Delta: "想一想"})
		fs.publish(id, model.RunEvent{Type: model.EventTextDelta, Delta: "你好"})
		fs.publish(id, model.RunEvent{Type: model.EventTextDelta, Delta: "，世界"})
		fs.finish(id, model.RunEvent{Type: model.EventRunCompleted, Usage: usage}, model.RunCompleted)
	}
}

func (fs *fakeServer) handleEvents(w http.ResponseWriter, r *http.Request) {
	since, _ := strconv.ParseUint(r.URL.Query().Get("since"), 10, 64)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	fl := w.(http.Flusher)
	fmt.Fprint(w, ": connected\n\n")
	fl.Flush()
	last := since
	for {
		fs.mu.Lock()
		var pending []event.Event
		for _, ev := range fs.events {
			if ev.Seq > last {
				pending = append(pending, ev)
			}
		}
		wait := fs.notify
		fs.mu.Unlock()
		for _, ev := range pending {
			data, _ := json.Marshal(ev)
			fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.Seq, ev.Type, data)
			last = ev.Seq
		}
		fl.Flush()
		select {
		case <-wait:
		case <-r.Context().Done():
			return
		}
	}
}

// syncBuffer 是可并发读写的输出缓冲。
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// chatHarness 用真实的 internal/client 连接假服务器运行 CLI。
type chatHarness struct {
	c      *cli
	out    *syncBuffer
	errOut *syncBuffer
	sigs   chan os.Signal
	fs     *fakeServer
}

func newChatHarness(t *testing.T, stdin string) *chatHarness {
	t.Helper()
	fs := newFakeServer(t)
	h := &chatHarness{out: &syncBuffer{}, errOut: &syncBuffer{}, sigs: make(chan os.Signal, 2), fs: fs}
	h.c = &cli{
		stdin:  strings.NewReader(stdin),
		out:    h.out,
		errOut: h.errOut,
		getenv: func(k string) string {
			if k == "LANG" {
				return "zh_CN.UTF-8"
			}
			return ""
		},
		dial: func(context.Context, string, string) (api, error) {
			return clientAPI{c: client.New(fs.srv.URL, "test-token", client.WithLocale("zh"))}, nil
		},
		daemonCtl:  &fakeDaemon{},
		interrupts: func() (<-chan os.Signal, func()) { return h.sigs, func() {} },
		home:       t.TempDir(),
	}
	return h
}

// runAsync 在后台运行 CLI，返回退出码通道。
func (h *chatHarness) runAsync(args ...string) <-chan int {
	done := make(chan int, 1)
	go func() { done <- h.c.run(context.Background(), args) }()
	return done
}

func waitExit(t *testing.T, done <-chan int, h *chatHarness) int {
	t.Helper()
	select {
	case code := <-done:
		return code
	case <-time.After(15 * time.Second):
		t.Fatalf("CLI 没有退出；输出:\n%s\n错误:\n%s", h.out.String(), h.errOut.String())
		return -1
	}
}

func waitOutput(t *testing.T, h *chatHarness, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(h.out.String(), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待输出 %q 超时；输出:\n%s", want, h.out.String())
}

func TestChatOnceStreamsTextReasoningAndUsage(t *testing.T) {
	h := newChatHarness(t, "")
	code := waitExit(t, h.runAsync("chat", "助理", "--once", "你好"), h)
	out := h.out.String()
	if code != exitOK {
		t.Fatalf("退出码 %d；输出:\n%s\n错误:\n%s", code, out, h.errOut.String())
	}
	for _, want := range []string{"思考│ 想一想", "你好，世界", "完成（输入 12 / 输出 5 tokens）"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出应包含 %q，得到:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b[") {
		t.Error("非终端输出不应包含 ANSI 转义")
	}
}

// 命令行创建的会话以 origin=cli 发起（session new 与 chat 共用同一个 api.CreateSession）。
func TestChatCreatesCLISession(t *testing.T) {
	h := newChatHarness(t, "")
	if code := waitExit(t, h.runAsync("chat", "助理", "--once", "你好"), h); code != exitOK {
		t.Fatalf("退出码 = %d，错误输出 %q", code, h.errOut.String())
	}
	if code := h.c.run(context.Background(), []string{"session", "new", "助理", "--title", "T"}); code != exitOK {
		t.Fatalf("session new 退出码 = %d，错误输出 %q", code, h.errOut.String())
	}
	h.fs.mu.Lock()
	defer h.fs.mu.Unlock()
	if strings.Join(h.fs.origins, ",") != "cli,cli" {
		t.Fatalf("CLI 创建会话应带 origin=cli，实际 %v", h.fs.origins)
	}
}

func TestChatApprovalPromptApprovesForSession(t *testing.T) {
	h := newChatHarness(t, "maybe\ns\n")
	code := waitExit(t, h.runAsync("chat", fakeAgentID, "--once", "#tool 列一下"), h)
	out := h.out.String()
	if code != exitOK {
		t.Fatalf("退出码 %d；输出:\n%s\n错误:\n%s", code, out, h.errOut.String())
	}
	for _, want := range []string{
		`⚙ run_command {"command":"echo hi"}`,
		"⚠ 需要审批：执行命令 echo hi（风险：execute）",
		"是否批准 run_command？[y]一次 / [s]本会话 / [n]拒绝",
		"请输入 y、s 或 n",
		"已批准",
		"✓ run_command：hi",
		"完成（输入 12 / 输出 5 tokens）",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("输出应包含 %q，得到:\n%s", want, out)
		}
	}
	h.fs.mu.Lock()
	defer h.fs.mu.Unlock()
	if len(h.fs.decided) != 1 || !h.fs.decided[0].Approve || h.fs.decided[0].Scope != model.ScopeSession {
		t.Fatalf("应提交一次\"本会话批准\"，得到 %+v", h.fs.decided)
	}
}

func TestChatApprovalDeniedWhenInputEnds(t *testing.T) {
	h := newChatHarness(t, "")
	code := waitExit(t, h.runAsync("chat", "助理", "--once", "#tool"), h)
	if code != exitOK {
		t.Fatalf("退出码 %d；错误:\n%s", code, h.errOut.String())
	}
	out := h.out.String()
	if !strings.Contains(out, "已拒绝") || !strings.Contains(out, "✗ run_command：denied by user") {
		t.Fatalf("输入结束应视为拒绝，得到:\n%s", out)
	}
}

func TestChatCtrlCCancelsRunThenReplExits(t *testing.T) {
	h := newChatHarness(t, "#slow\n")
	done := h.runAsync("chat", "助理")
	waitOutput(t, h, "开始…")
	h.sigs <- os.Interrupt
	code := waitExit(t, done, h)
	out := h.out.String()
	if code != exitOK {
		t.Fatalf("退出码 %d；输出:\n%s", code, out)
	}
	select {
	case id := <-h.fs.cancels:
		if !strings.HasPrefix(id, "01JRUN") {
			t.Fatalf("取消了错误的运行 %q", id)
		}
	default:
		t.Fatal("Ctrl-C 应取消当前运行")
	}
	for _, want := range []string{"与 助理 对话", "正在取消当前运行", "运行已取消", "再见"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出应包含 %q，得到:\n%s", want, out)
		}
	}
}

func TestChatSecondCtrlCQuits(t *testing.T) {
	h := newChatHarness(t, "")
	h.fs.ignoreCancel = true
	done := h.runAsync("chat", "助理", "--once", "#slow")
	waitOutput(t, h, "开始…")
	h.sigs <- os.Interrupt
	waitOutput(t, h, "正在取消当前运行")
	h.sigs <- os.Interrupt
	if code := waitExit(t, done, h); code != exitInterrupts {
		t.Fatalf("运行中两次 Ctrl-C 应以 %d 退出，得到 %d", exitInterrupts, code)
	}
}

func TestChatAsksPendingApprovalWhenEventWasMissed(t *testing.T) {
	h := newChatHarness(t, "y\n")
	h.fs.hideApproval = true
	code := waitExit(t, h.runAsync("chat", "助理", "--once", "#tool"), h)
	out := h.out.String()
	if code != exitOK || !strings.Contains(out, "已批准") || !strings.Contains(out, "✓ run_command：hi") {
		t.Fatalf("错过审批事件时应从待审批列表补问，得到 %d:\n%s", code, out)
	}
}
