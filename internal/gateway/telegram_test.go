package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

const testToken = "123456:ABC-DEF-secret"

// fakeBotAPI 用 httptest 模拟 Telegram Bot API 的 getMe / getUpdates / sendMessage。
type fakeBotAPI struct {
	t   *testing.T
	srv *httptest.Server

	mu            sync.Mutex
	cond          *sync.Cond
	updates       []map[string]any
	sent          []map[string]any
	offsets       []int64 // 每次 getUpdates 的 offset
	ackOffsets    []int64 // timeout=0 的确认调用
	getMeFailures int     // getMe 返回 503 的次数
	getMeStatus   int     // 非 0 时 getMe 固定返回该错误码
	pollFailures  int     // getUpdates 返回 500 的次数
	sendFailures  int     // sendMessage 返回 500 的次数
	sendTooMany   int     // sendMessage 返回 429 的次数
	sendBadReq    bool    // sendMessage 返回 400
	username      string
}

func newFakeBotAPI(t *testing.T) *fakeBotAPI {
	t.Helper()
	f := &fakeBotAPI{t: t, username: "GoslingBot"}
	f.cond = sync.NewCond(&f.mu)
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeBotAPI) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeBotAPI) fail(w http.ResponseWriter, code int, desc string, extra map[string]any) {
	body := map[string]any{"ok": false, "error_code": code, "description": desc}
	for k, v := range extra {
		body[k] = v
	}
	f.writeJSON(w, code, body)
}

func (f *fakeBotAPI) handle(w http.ResponseWriter, r *http.Request) {
	prefix := "/bot" + testToken + "/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		f.fail(w, http.StatusUnauthorized, "Unauthorized", nil)
		return
	}
	method := strings.TrimPrefix(r.URL.Path, prefix)
	var body map[string]any
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	switch method {
	case "getMe":
		f.mu.Lock()
		if f.getMeStatus != 0 {
			code := f.getMeStatus
			f.mu.Unlock()
			f.fail(w, code, "Unauthorized", nil)
			return
		}
		if f.getMeFailures > 0 {
			f.getMeFailures--
			f.mu.Unlock()
			f.fail(w, http.StatusServiceUnavailable, "try later", nil)
			return
		}
		name := f.username
		f.mu.Unlock()
		f.writeJSON(w, 200, map[string]any{"ok": true, "result": map[string]any{"id": 42, "is_bot": true, "username": name}})
	case "getUpdates":
		offset := int64(0)
		if v, ok := body["offset"].(float64); ok {
			offset = int64(v)
		}
		timeout := 0.0
		if v, ok := body["timeout"].(float64); ok {
			timeout = v
		}
		f.mu.Lock()
		if timeout == 0 {
			f.ackOffsets = append(f.ackOffsets, offset)
		} else {
			f.offsets = append(f.offsets, offset)
		}
		if f.pollFailures > 0 {
			f.pollFailures--
			f.mu.Unlock()
			f.fail(w, http.StatusInternalServerError, "boom", nil)
			return
		}
		// 丢掉已确认的更新，等待新更新最多 100ms（模拟长轮询）。
		f.updates = dropAcked(f.updates, offset)
		if timeout > 0 && len(f.updates) == 0 {
			timer := time.AfterFunc(100*time.Millisecond, func() {
				f.mu.Lock()
				f.cond.Broadcast()
				f.mu.Unlock()
			})
			f.cond.Wait()
			timer.Stop()
			f.updates = dropAcked(f.updates, offset)
		}
		result := append([]map[string]any(nil), f.updates...)
		f.mu.Unlock()
		f.writeJSON(w, 200, map[string]any{"ok": true, "result": result})
	case "sendMessage":
		f.mu.Lock()
		switch {
		case f.sendBadReq:
			f.mu.Unlock()
			f.fail(w, http.StatusBadRequest, "Bad Request: chat not found", nil)
			return
		case f.sendFailures > 0:
			f.sendFailures--
			f.mu.Unlock()
			f.fail(w, http.StatusInternalServerError, "Internal", nil)
			return
		case f.sendTooMany > 0:
			f.sendTooMany--
			f.mu.Unlock()
			f.fail(w, http.StatusTooManyRequests, "Too Many Requests", map[string]any{"parameters": map[string]any{"retry_after": 0}})
			return
		}
		f.sent = append(f.sent, body)
		f.mu.Unlock()
		f.writeJSON(w, 200, map[string]any{"ok": true, "result": map[string]any{"message_id": 100}})
	default:
		f.fail(w, http.StatusNotFound, "Not Found", nil)
	}
}

func dropAcked(updates []map[string]any, offset int64) []map[string]any {
	var keep []map[string]any
	for _, u := range updates {
		if int64(u["update_id"].(float64)) >= offset {
			keep = append(keep, u)
		}
	}
	return keep
}

// push 放入一条更新并唤醒等待中的长轮询。
func (f *fakeBotAPI) push(updateID int64, msg map[string]any) {
	f.mu.Lock()
	f.updates = append(f.updates, map[string]any{"update_id": float64(updateID), "message": msg})
	f.cond.Broadcast()
	f.mu.Unlock()
}

func (f *fakeBotAPI) sentMessages() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.sent...)
}

func (f *fakeBotAPI) lastOffset() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.offsets) == 0 {
		return -1
	}
	return f.offsets[len(f.offsets)-1]
}

func (f *fakeBotAPI) acks() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.ackOffsets...)
}

func tgMessage(id int64, chatID int64, chatType string, from map[string]any, text string, extra map[string]any) map[string]any {
	m := map[string]any{
		"message_id": float64(id), "date": float64(1_700_000_000), "text": text,
		"chat": map[string]any{"id": float64(chatID), "type": chatType},
		"from": from,
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

var user7 = map[string]any{"id": float64(7), "is_bot": false, "first_name": "Li", "last_name": "Lei", "username": "lilei"}

func newTelegramForTest(t *testing.T, f *fakeBotAPI) *TelegramAdapter {
	t.Helper()
	return NewTelegram(TelegramOptions{
		BaseURL:     f.srv.URL,
		HTTPClient:  f.srv.Client(),
		PollTimeout: time.Second,
		MinBackoff:  5 * time.Millisecond,
		MaxBackoff:  20 * time.Millisecond,
	})
}

// startTelegram 启动适配器并收集入站消息。
func startTelegram(t *testing.T, a *TelegramAdapter, config string) func() []Inbound {
	t.Helper()
	var mu sync.Mutex
	var got []Inbound
	ch := &model.Channel{ID: "tg1", Kind: model.ChannelTelegram}
	if config != "" {
		ch.Config = json.RawMessage(config)
	}
	if err := a.Start(context.Background(), ch, testToken, func(in Inbound) {
		mu.Lock()
		got = append(got, in)
		mu.Unlock()
	}); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = a.Stop(ctx)
	})
	return func() []Inbound {
		mu.Lock()
		defer mu.Unlock()
		return append([]Inbound(nil), got...)
	}
}

func TestTelegramStartValidatesToken(t *testing.T) {
	f := newFakeBotAPI(t)
	ch := &model.Channel{ID: "tg1", Kind: model.ChannelTelegram}
	noop := func(Inbound) {}

	a := newTelegramForTest(t, f)
	if err := a.Start(context.Background(), ch, "   ", noop); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("空 token 应返回 invalid，得到 %v", err)
	}
	if err := a.Start(context.Background(), ch, "has/slash", noop); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("含非法字符的 token 应返回 invalid，得到 %v", err)
	}
	err := a.Start(context.Background(), ch, "wrong-token-value", noop)
	if !model.IsCode(err, model.CodeUnauthorized) {
		t.Fatalf("被拒绝的 token 应返回 unauthorized，得到 %v", err)
	}
	if strings.Contains(err.Error(), "wrong-token-value") {
		t.Fatalf("错误信息不应包含 token: %v", err)
	}
	if a.Status().Connected {
		t.Fatal("启动失败后不应报告已连接")
	}
	// 失败后可以再次启动。
	if err := a.Start(context.Background(), ch, testToken, noop); err != nil {
		t.Fatalf("修正 token 后启动应成功: %v", err)
	}
	if err := a.Start(context.Background(), ch, testToken, noop); !model.IsCode(err, model.CodeConflict) {
		t.Fatalf("重复启动应返回 conflict，得到 %v", err)
	}
	if err := a.Stop(context.Background()); err != nil {
		t.Fatalf("停止失败: %v", err)
	}
	if err := a.Stop(context.Background()); err != nil {
		t.Fatalf("重复停止应成功: %v", err)
	}
}

func TestTelegramPollDeliversMessagesAndAdvancesOffset(t *testing.T) {
	f := newFakeBotAPI(t)
	a := newTelegramForTest(t, f)
	got := startTelegram(t, a, "")
	if a.BotUsername() != "GoslingBot" {
		t.Fatalf("启动后应知道机器人用户名，得到 %q", a.BotUsername())
	}

	f.push(10, tgMessage(1, 7, "private", user7, "你好", nil))
	f.push(11, tgMessage(2, -100, "supergroup", user7, "@goslingbot 帮我", map[string]any{
		"entities": []map[string]any{{"type": "mention", "offset": 0, "length": 11}},
	}))
	f.push(12, tgMessage(3, -100, "supergroup", user7, "闲聊", nil))
	f.push(13, tgMessage(4, -100, "supergroup", user7, "/start@GoslingBot", map[string]any{
		"entities": []map[string]any{{"type": "bot_command", "offset": 0, "length": 17}},
	}))
	f.push(14, tgMessage(5, -100, "supergroup", user7, "回复机器人", map[string]any{
		"reply_to_message": map[string]any{"from": map[string]any{"id": float64(42), "is_bot": true}},
	}))
	f.push(15, tgMessage(6, 7, "private", map[string]any{"id": float64(99), "is_bot": true, "first_name": "Other"}, "机器人发的", nil))
	f.push(16, tgMessage(7, 7, "private", user7, "", map[string]any{"caption": "图片说明"}))
	f.push(17, tgMessage(8, 7, "private", user7, "", nil))
	f.push(18, tgMessage(9, 500, "channel", nil, "频道帖子", nil))

	waitFor(t, "消息送达", func() bool { return len(got()) == 6 })
	in := got()
	if in[0].ChatID != "7" || in[0].PeerID != "7" || in[0].PeerName != "Li Lei" || in[0].IsGroup || in[0].MessageID != "1" ||
		in[0].Text != "你好" || in[0].Kind != model.ChannelTelegram || in[0].ChannelID != "tg1" || in[0].At.Unix() != 1_700_000_000 {
		t.Fatalf("私聊消息字段错误: %+v", in[0])
	}
	if !in[1].IsGroup || in[1].ChatID != "-100" || !in[1].Mentioned || in[1].Text != "帮我" {
		t.Fatalf("群聊提及应被识别并去掉 @: %+v", in[1])
	}
	if in[2].Mentioned || in[2].Text != "闲聊" {
		t.Fatalf("未提及的群消息不应标记 Mentioned: %+v", in[2])
	}
	if !in[3].Mentioned || in[3].Text != "/start" {
		t.Fatalf("带机器人名的命令应视为提及并去掉后缀: %+v", in[3])
	}
	if !in[4].Mentioned {
		t.Fatalf("回复机器人的消息应视为提及: %+v", in[4])
	}
	if in[5].Text != "图片说明" || in[5].MessageID != "7" {
		t.Fatalf("说明文字应作为文本: %+v", in[5])
	}
	waitFor(t, "offset 推进到最后一条之后", func() bool { return f.lastOffset() == 19 })
	if !a.Status().Connected || a.Status().LastError != "" {
		t.Fatalf("轮询正常时应报告已连接: %+v", a.Status())
	}
}

func TestTelegramPollBackoffRecoversAndStartSurvivesOutage(t *testing.T) {
	f := newFakeBotAPI(t)
	f.mu.Lock()
	f.getMeFailures = 2
	f.pollFailures = 2
	f.mu.Unlock()
	a := newTelegramForTest(t, f)
	got := startTelegram(t, a, "")
	if a.BotUsername() != "" {
		t.Fatal("getMe 失败时启动不应得到用户名")
	}
	st := a.Status()
	if st.Connected || st.LastError == "" || strings.Contains(st.LastError, testToken) {
		t.Fatalf("启动时连接失败应记录脱敏后的状态: %+v", st)
	}
	f.push(1, tgMessage(1, 7, "private", user7, "恢复后", nil))
	waitFor(t, "退避重试后收到消息", func() bool { return len(got()) == 1 })
	if a.BotUsername() != "GoslingBot" || !a.Status().Connected {
		t.Fatalf("恢复后应补齐用户名并报告已连接: %+v", a.Status())
	}
}

func TestTelegramSendChunksRepliesAndRetries(t *testing.T) {
	f := newFakeBotAPI(t)
	a := newTelegramForTest(t, f)
	startTelegram(t, a, "")

	long := strings.Repeat("字", TelegramMaxMessage+10)
	if err := a.Send(context.Background(), Outbound{ChatID: "7", Text: long, ReplyTo: "5"}); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	sent := f.sentMessages()
	if len(sent) != 2 {
		t.Fatalf("超过硬上限应分成两片，得到 %d", len(sent))
	}
	if sent[0]["chat_id"] != float64(7) || sent[0]["reply_to_message_id"] != float64(5) || sent[0]["allow_sending_without_reply"] != true {
		t.Fatalf("第一片应带数字 chat_id 与引用: %v", sent[0])
	}
	if _, has := sent[1]["reply_to_message_id"]; has {
		t.Fatal("后续分片不应引用")
	}
	if len([]rune(sent[0]["text"].(string)))+len([]rune(sent[1]["text"].(string))) != TelegramMaxMessage+10 {
		t.Fatal("分片应保留全部内容")
	}
	if err := a.Send(context.Background(), Outbound{ChatID: "@somechannel", Text: "x"}); err != nil || f.sentMessages()[2]["chat_id"] != "@somechannel" {
		t.Fatalf("非数字 chat_id 应原样发送: %v", err)
	}
	if err := a.Send(context.Background(), Outbound{ChatID: "7", Text: "   "}); err != nil || len(f.sentMessages()) != 3 {
		t.Fatalf("空文本应直接忽略: %v", err)
	}
	if err := a.Send(context.Background(), Outbound{ChatID: "", Text: "x"}); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("缺少 chat_id 应返回 invalid，得到 %v", err)
	}

	f.mu.Lock()
	f.sendFailures = 1
	f.sendTooMany = 1
	f.mu.Unlock()
	if err := a.Send(context.Background(), Outbound{ChatID: "7", Text: "重试"}); err != nil {
		t.Fatalf("5xx 与 429 应重试后成功，得到 %v", err)
	}
	if n := len(f.sentMessages()); n != 4 {
		t.Fatalf("重试成功后应记录一条，得到 %d", n)
	}

	f.mu.Lock()
	f.sendBadReq = true
	f.mu.Unlock()
	err := a.Send(context.Background(), Outbound{ChatID: "7", Text: "坏请求"})
	if !model.IsCode(err, model.CodeInvalid) || !strings.Contains(err.Error(), "chat not found") {
		t.Fatalf("4xx 不应重试且应带平台描述，得到 %v", err)
	}
	f.mu.Lock()
	f.sendBadReq = false
	f.sendFailures = 10
	f.mu.Unlock()
	if err := a.Send(context.Background(), Outbound{ChatID: "7", Text: "一直失败"}); !model.IsCode(err, model.CodeUnavailable) {
		t.Fatalf("重试耗尽应返回 unavailable，得到 %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.Send(cancelled, Outbound{ChatID: "7", Text: "取消"}); !model.IsCode(err, model.CodeCancelled) {
		t.Fatalf("ctx 取消应返回 cancelled，得到 %v", err)
	}
}

func TestTelegramErrorsNeverContainToken(t *testing.T) {
	f := newFakeBotAPI(t)
	a := newTelegramForTest(t, f)
	startTelegram(t, a, "")
	f.srv.Close()

	err := a.Send(context.Background(), Outbound{ChatID: "7", Text: "x"})
	if err == nil {
		t.Fatal("服务关闭后发送应失败")
	}
	if strings.Contains(err.Error(), testToken) || strings.Contains(fmt.Sprintf("%+v", err), "secret") {
		t.Fatalf("错误信息泄露了 token: %v", err)
	}
	var me *model.Error
	if !errors.As(err, &me) || me.Code != model.CodeUnavailable {
		t.Fatalf("网络错误应是 unavailable，得到 %v", err)
	}
	waitFor(t, "轮询记录错误状态", func() bool { return a.Status().LastError != "" })
	if strings.Contains(a.Status().LastError, testToken) {
		t.Fatalf("状态里泄露了 token: %q", a.Status().LastError)
	}
}

func TestTelegramStopEndsPollingPromptlyAndAcksProcessedUpdates(t *testing.T) {
	f := newFakeBotAPI(t)
	a := NewTelegram(TelegramOptions{BaseURL: f.srv.URL, HTTPClient: f.srv.Client(), PollTimeout: 30 * time.Second, MinBackoff: time.Millisecond})
	got := startTelegram(t, a, "")
	f.push(5, tgMessage(1, 7, "private", user7, "一条", nil))
	waitFor(t, "消息送达", func() bool { return len(got()) == 1 })
	// 此刻轮询循环已用 offset=6 发起下一次长轮询（30 秒），Stop 必须立刻打断它。
	waitFor(t, "下一次长轮询开始", func() bool { return f.lastOffset() == 6 })

	begin := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Stop(ctx); err != nil {
		t.Fatalf("停止失败: %v", err)
	}
	if time.Since(begin) > 2*time.Second {
		t.Fatalf("Stop 应立刻打断长轮询，用了 %v", time.Since(begin))
	}
	if a.Status().Connected {
		t.Fatal("停止后不应报告已连接")
	}
	if err := a.Send(context.Background(), Outbound{ChatID: "7", Text: "x"}); !model.IsCode(err, model.CodeUnavailable) {
		t.Fatalf("停止后 Send 应返回 unavailable，得到 %v", err)
	}
	// 处理完 update 5 后，下一次 getUpdates(offset=6) 通常已经确认了它；若 Stop 恰好打断了那次请求，
	// 适配器会用同一个 offset 做一次冗余但幂等的补确认。两种情况都不能出现别的 offset。
	for _, off := range f.acks() {
		if off != 6 {
			t.Fatalf("补确认只能使用已处理更新之后的 offset 6，得到 %v", f.acks())
		}
	}
}

func TestTelegramStopAcksUpdatesProcessedMidBatch(t *testing.T) {
	f := newFakeBotAPI(t)
	a := NewTelegram(TelegramOptions{BaseURL: f.srv.URL, HTTPClient: f.srv.Client(), PollTimeout: 30 * time.Second, MinBackoff: time.Millisecond})
	ch := &model.Channel{ID: "tg1", Kind: model.ChannelTelegram}
	blocked := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	if err := a.Start(context.Background(), ch, testToken, func(in Inbound) {
		// 第一条消息处理期间发生 Stop：第二条必须被丢弃，第一条必须被补确认。
		handled := false
		once.Do(func() {
			handled = true
			close(blocked)
			<-release
		})
		if !handled {
			t.Errorf("Stop 之后不应再处理消息: %+v", in)
		}
	}); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	f.push(5, tgMessage(1, 7, "private", user7, "一", nil))
	f.push(6, tgMessage(2, 7, "private", user7, "二", nil))
	<-blocked

	stopped := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		stopped <- a.Stop(ctx)
	}()
	// Stop 在锁内取消轮询 ctx，因此看到 started=false 即可确定取消已经生效。
	waitFor(t, "Stop 已取消轮询", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return !a.started
	})
	close(release)
	if err := <-stopped; err != nil {
		t.Fatalf("停止失败: %v", err)
	}
	waitFor(t, "补确认已处理的更新", func() bool {
		acks := f.acks()
		return len(acks) == 1 && acks[0] == 6
	})
}

func TestTelegramAPIBaseFromConfig(t *testing.T) {
	f := newFakeBotAPI(t)
	ch := &model.Channel{ID: "tg1", Kind: model.ChannelTelegram, Config: json.RawMessage(`{"api_base":"ftp://x"}`)}
	a := NewTelegram(TelegramOptions{BaseURL: "http://127.0.0.1:1", HTTPClient: f.srv.Client(), MinBackoff: time.Millisecond, StartTimeout: time.Second})
	if err := a.Start(context.Background(), ch, testToken, func(Inbound) {}); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("非 http(s) 的 api_base 应返回 invalid，得到 %v", err)
	}
	ch.Config = json.RawMessage(`{"api_base":"` + f.srv.URL + `/"}`)
	if err := a.Start(context.Background(), ch, testToken, func(Inbound) {}); err != nil {
		t.Fatalf("配置里的 api_base 应覆盖默认地址: %v", err)
	}
	defer func() { _ = a.Stop(context.Background()) }()
	if a.BotUsername() != "GoslingBot" {
		t.Fatalf("应通过配置的地址完成 getMe，得到 %q", a.BotUsername())
	}
}

func TestRemoveFold(t *testing.T) {
	if got := removeFold("Hi @GoslingBot, @goslingbot!", "@GoslingBot"); got != "Hi , !" {
		t.Fatalf("应不区分大小写删除全部出现，得到 %q", got)
	}
	if got := removeFold("无", ""); got != "无" {
		t.Fatalf("空子串应原样返回，得到 %q", got)
	}
}
