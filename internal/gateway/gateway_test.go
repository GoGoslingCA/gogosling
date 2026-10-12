package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/event"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

const agentA = "01AGENTAAAAAAAAAAAAAAAAAAA"

func privateMsg(peer, text string) Inbound {
	return Inbound{ChatID: peer, PeerID: peer, PeerName: "用户" + peer, MessageID: "m-" + text, Text: text}
}

func TestStartLoadsEnabledChannelsAndIsolatesFailures(t *testing.T) {
	h := newHarness(t, Options{})
	h.secrets["feishu-secret"] = "sekrit-token"
	h.factory.Register(model.ChannelFeishu, func() Adapter {
		return &failingAdapter{kind: model.ChannelFeishu, err: errors.New("bad token sekrit-token")}
	})
	ok := h.addChannel("ok", agentA, model.ChannelConsole, "", true)
	ok.LastError = "旧错误"
	_ = h.channels.UpdateChannel(context.Background(), ok)
	h.addChannel("bad", agentA, model.ChannelFeishu, "", true)
	bad := h.channels.get("bad")
	bad.SecretRef = "feishu-secret"
	_ = h.channels.UpdateChannel(context.Background(), bad)
	h.addChannel("off", agentA, model.ChannelConsole, "", false)
	h.addChannel("nosecret", agentA, model.ChannelConsole, "", true)
	ns := h.channels.get("nosecret")
	ns.SecretRef = "missing"
	_ = h.channels.UpdateChannel(context.Background(), ns)
	h.addChannel("badcfg", agentA, model.ChannelConsole, `{"group_mode":"weird"}`, true)
	h.addChannel("unknownkind", agentA, model.ChannelKind("pigeon"), "", true)

	h.start()

	if st, ok := h.gw.Status("ok"); !ok || !st.Running || !st.Connected || st.Kind != model.ChannelConsole {
		t.Fatalf("正常通道应处于运行状态，得到 %+v/%v", st, ok)
	}
	if got := h.channels.get("ok").LastError; got != "" {
		t.Fatalf("启动成功应清空 LastError，得到 %q", got)
	}
	for _, id := range []string{"bad", "off", "nosecret", "badcfg", "unknownkind"} {
		if _, ok := h.gw.Status(id); ok {
			t.Fatalf("通道 %s 不应处于运行状态", id)
		}
	}
	if got := h.channels.get("bad").LastError; !strings.Contains(got, "bad token ***") || strings.Contains(got, "sekrit") {
		t.Fatalf("启动失败应写入脱敏后的 LastError，得到 %q", got)
	}
	if got := h.channels.get("nosecret").LastError; got == "" {
		t.Fatal("凭据缺失应写入 LastError")
	}
	if got := h.channels.get("badcfg").LastError; !strings.Contains(got, "group_mode") {
		t.Fatalf("配置错误应写入 LastError，得到 %q", got)
	}
	if got := h.channels.get("unknownkind").LastError; !strings.Contains(got, "不支持的通道类型") {
		t.Fatalf("未知类型应写入 LastError，得到 %q", got)
	}
	if got := h.channels.get("off").LastError; got != "" {
		t.Fatalf("停用的通道不应被碰，得到 %q", got)
	}
	if len(h.gw.Statuses()) != 1 {
		t.Fatalf("应只有一个运行中的通道，得到 %d", len(h.gw.Statuses()))
	}
	if err := h.gw.Start(context.Background()); !model.IsCode(err, model.CodeConflict) {
		t.Fatalf("重复启动应返回 conflict，得到 %v", err)
	}
}

func TestStartFailsWhenChannelListUnavailableAndCanRetry(t *testing.T) {
	h := newHarness(t, Options{})
	h.addChannel("c1", agentA, model.ChannelConsole, "", true)
	h.channels.mu.Lock()
	h.channels.listErr = errors.New("db down")
	h.channels.mu.Unlock()
	err := h.gw.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "db down") {
		t.Fatalf("读取通道列表失败应返回错误，得到 %v", err)
	}
	if err := h.gw.Stop(context.Background()); err != nil {
		t.Fatalf("失败后的 Stop 应是空操作，得到 %v", err)
	}
	h.channels.mu.Lock()
	h.channels.listErr = nil
	h.channels.mu.Unlock()
	h.start()
	if _, ok := h.gw.Status("c1"); !ok {
		t.Fatal("修复后重试 Start 应正常启动通道")
	}
}

func TestInboundRoutesToSessionKeyAndSender(t *testing.T) {
	h := newHarness(t, Options{})
	h.addChannel("c1", agentA, model.ChannelConsole, "", true)
	h.start()

	h.inject("c1", Inbound{ChatID: "chat:9", PeerID: "u1", PeerName: "张三", MessageID: "m1", Text: "  你好\x00世界\r\n  "})

	reqs := h.sender.sent()
	if len(reqs) != 1 {
		t.Fatalf("应调用一次 Send，得到 %d", len(reqs))
	}
	req := reqs[0]
	key := SessionKey(agentA, model.ChannelConsole, "chat:9")
	if key != agentA+":channel:console:chat:9" {
		t.Fatalf("会话键格式错误: %q", key)
	}
	sess := h.sender.session(key)
	if sess == nil || req.SessionID != sess.ID {
		t.Fatalf("应按会话键 %q 解析会话并发送，得到 %+v", key, req)
	}
	if req.Text != "你好世界" {
		t.Fatalf("文本应被净化，得到 %q", req.Text)
	}
	if req.Trigger != model.TriggerChannel || !req.Untrusted {
		t.Fatalf("应标记 channel 触发与不可信，得到 %+v", req)
	}
	if req.Principal.Kind != model.PrincipalChannelPeer || req.Principal.ID != "console:u1" || req.Principal.Name != "张三" {
		t.Fatalf("发送者身份错误: %+v", req.Principal)
	}

	// 同一会话的第二条消息复用会话。
	h.inject("c1", Inbound{ChatID: "chat:9", PeerID: "u2", Text: "再来"})
	reqs = h.sender.sent()
	if len(reqs) != 2 || reqs[1].SessionID != sess.ID {
		t.Fatalf("同一 chat 应复用会话，得到 %+v", reqs)
	}
}

func TestInboundDropsEmptyOrMalformed(t *testing.T) {
	h := newHarness(t, Options{})
	h.addChannel("c1", agentA, model.ChannelConsole, "", true)
	h.start()

	h.inject("c1", Inbound{ChatID: "a", PeerID: "a", Text: "\x01\x02 \r\n"})
	h.inject("c1", Inbound{ChatID: "", PeerID: "a", Text: "x"})
	h.inject("c1", Inbound{ChatID: "a", PeerID: "", Text: "x"})
	if n := len(h.sender.sent()); n != 0 {
		t.Fatalf("空内容或缺少标识的消息不应发送，得到 %d", n)
	}
	// 只有附件没有文本也算有内容。
	h.inject("c1", Inbound{ChatID: "a", PeerID: "a", Attachments: []model.Attachment{{Name: "f.txt", Path: "inbound/f.txt"}}})
	if n := len(h.sender.sent()); n != 1 {
		t.Fatalf("只有附件的消息应发送，得到 %d", n)
	}
}

func TestInboundTruncatesLongText(t *testing.T) {
	h := newHarness(t, Options{})
	h.addChannel("c1", agentA, model.ChannelConsole, "", true)
	h.start()

	long := strings.Repeat("好", MaxInboundBytes) // 3 字节一个字符，远超上限
	h.inject("c1", privateMsg("p", long))
	reqs := h.sender.sent()
	if len(reqs) != 1 {
		t.Fatalf("应发送一次，得到 %d", len(reqs))
	}
	if n := len(reqs[0].Text); n > MaxInboundBytes || n < MaxInboundBytes-3 {
		t.Fatalf("文本应在字符边界截断到 8 KiB 以内，得到 %d 字节", n)
	}
	if !strings.HasSuffix(reqs[0].Text, "好") {
		t.Fatal("截断不应留下半个字符")
	}
}

func TestAllowedPeersWhitelist(t *testing.T) {
	h := newHarness(t, Options{})
	h.addChannel("c1", agentA, model.ChannelConsole, `{"allowed_peers":[" u1 ","u3"]}`, true)
	h.start()

	h.inject("c1", privateMsg("u2", "不该进"))
	if n := len(h.sender.sent()); n != 0 {
		t.Fatalf("白名单外的发送者应被忽略，得到 %d", n)
	}
	settle()
	if n := len(h.console("c1").Sent()); n != 0 {
		t.Fatalf("白名单外的发送者不应收到任何回复，得到 %d", n)
	}
	h.inject("c1", privateMsg("u1", "可以进"))
	if n := len(h.sender.sent()); n != 1 {
		t.Fatalf("白名单内的发送者应被受理，得到 %d", n)
	}
}

func TestGroupTriggerRules(t *testing.T) {
	h := newHarness(t, Options{})
	h.addChannel("mention", agentA, model.ChannelConsole, `{"trigger_prefix":"!"}`, true)
	h.addChannel("all", agentA, model.ChannelConsole, `{"group_mode":"all"}`, true)
	h.start()

	group := func(text string, mentioned bool) Inbound {
		return Inbound{ChatID: "g1", PeerID: "u1", IsGroup: true, Text: text, Mentioned: mentioned}
	}
	h.inject("mention", group("没人叫我", false))
	if n := len(h.sender.sent()); n != 0 {
		t.Fatalf("mention 模式下未提及的群消息应被忽略，得到 %d", n)
	}
	h.inject("mention", group("叫我了", true))
	h.inject("mention", group("!带前缀 的消息", false))
	h.inject("mention", Inbound{ChatID: "u1", PeerID: "u1", Text: "!私聊也去前缀"})
	h.inject("mention", Inbound{ChatID: "u1", PeerID: "u1", Text: "私聊不需要提及"})
	h.inject("mention", group("!", false))
	reqs := h.sender.sent()
	texts := make([]string, 0, len(reqs))
	for _, r := range reqs {
		texts = append(texts, r.Text)
	}
	want := []string{"叫我了", "带前缀 的消息", "私聊也去前缀", "私聊不需要提及"}
	if strings.Join(texts, "|") != strings.Join(want, "|") {
		t.Fatalf("群聊触发规则结果错误: 得到 %v，期望 %v", texts, want)
	}

	h.inject("all", group("所有消息都响应", false))
	reqs = h.sender.sent()
	if len(reqs) != 5 || reqs[4].Text != "所有消息都响应" {
		t.Fatalf("all 模式应响应未提及的群消息，得到 %v", reqs)
	}
}

func TestRateLimitPerChannelPeer(t *testing.T) {
	h := newHarness(t, Options{})
	h.addChannel("c1", agentA, model.ChannelConsole, `{"rate_per_minute":60,"rate_burst":2}`, true)
	h.addChannel("c2", agentA, model.ChannelConsole, `{"rate_per_minute":60,"rate_burst":2}`, true)
	h.start()

	for i := 0; i < 3; i++ {
		h.inject("c1", privateMsg("u1", "x"))
	}
	if n := len(h.sender.sent()); n != 2 {
		t.Fatalf("突发容量为 2，第三条应被限流，得到 %d", n)
	}
	h.inject("c1", privateMsg("u2", "另一个人"))
	h.inject("c2", privateMsg("u1", "另一个通道"))
	if n := len(h.sender.sent()); n != 4 {
		t.Fatalf("限流应按通道与对端分桶，得到 %d", n)
	}
	h.clock.advance(time.Second) // 60/分钟 = 每秒补 1 个
	h.inject("c1", privateMsg("u1", "补充后"))
	h.inject("c1", privateMsg("u1", "再超"))
	if n := len(h.sender.sent()); n != 5 {
		t.Fatalf("补充一个令牌后应只放行一条，得到 %d", n)
	}
}

func TestReplyIsChunkedAndQuotesInboundMessage(t *testing.T) {
	h := newHarness(t, Options{})
	h.addChannel("c1", agentA, model.ChannelConsole, `{"max_chunk":8}`, true)
	h.start()

	h.inject("c1", Inbound{ChatID: "chat1", PeerID: "u1", MessageID: "m1", Text: "问题"})
	run := h.sender.lastRun()
	msg := model.TextMessage(run.SessionID, model.RoleAssistant, "第一段落\n\n第二段落\n\n第三段落")
	h.publishRun(model.RunEvent{Type: model.EventRunCompleted, RunID: run.ID, SessionID: run.SessionID, AgentID: agentA, Message: &msg})

	con := h.console("c1")
	waitFor(t, "回复分片送达", func() bool { return len(con.Sent()) == 3 })
	sent := con.Sent()
	for i, out := range sent {
		if out.ChatID != "chat1" || out.ChannelID != "c1" {
			t.Fatalf("分片 %d 的目标错误: %+v", i, out)
		}
		if n := len([]rune(out.Text)); n > 8 {
			t.Fatalf("分片 %d 超过上限: %d 字符", i, n)
		}
	}
	if sent[0].ReplyTo != "m1" || sent[1].ReplyTo != "" || sent[2].ReplyTo != "" {
		t.Fatalf("只有第一片应引用入站消息，得到 %+v", sent)
	}
	if got := sent[0].Text + sent[1].Text + sent[2].Text; got != "第一段落第二段落第三段落" {
		t.Fatalf("分片内容错误: %q", got)
	}

	// 同一运行的终态只回复一次；之后的引用表已清理。
	h.publishRun(model.RunEvent{Type: model.EventRunCompleted, RunID: run.ID, SessionID: run.SessionID, Message: &msg})
	waitFor(t, "第二次事件送达", func() bool { return len(con.Sent()) == 6 })
	if con.Sent()[3].ReplyTo != "" {
		t.Fatal("终态处理后不应再引用已消费的入站消息")
	}
}

func TestEventsNotifyFailureCancelApprovalAndEmptyReply(t *testing.T) {
	h := newHarness(t, Options{Texts: Texts{RunFailed: "失败了: {error}", ApprovalRequired: "审批 {summary} [{risk}]"}})
	h.addChannel("c1", agentA, model.ChannelConsole, "", true)
	h.start()

	h.inject("c1", Inbound{ChatID: "chat1", PeerID: "u1", MessageID: "m1", Text: "问题"})
	run := h.sender.lastRun()
	con := h.console("c1")

	h.publishRun(model.RunEvent{Type: model.EventApprovalRequested, RunID: run.ID, SessionID: run.SessionID,
		Approval: &model.ApprovalRequest{Summary: "写入 /etc/hosts", Risk: model.RiskWrite}})
	waitFor(t, "审批通知", func() bool { return len(con.Sent()) == 1 })
	if got := con.Sent()[0]; got.Text != "审批 写入 /etc/hosts [write]" || got.ReplyTo != "m1" {
		t.Fatalf("审批通知错误: %+v", got)
	}

	// 不在 RunErrorMap 中的原因（运行时给出的任意文本）不外发，只发通用失败文案。
	h.publishRun(model.RunEvent{Type: model.EventRunFailed, RunID: run.ID, SessionID: run.SessionID, Error: "模型不可用"})
	waitFor(t, "失败通知", func() bool { return len(con.Sent()) == 2 })
	if got := con.Sent()[1]; got.Text != "失败了" || got.ReplyTo != "m1" {
		t.Fatalf("失败通知错误: %+v", got)
	}

	h.inject("c1", Inbound{ChatID: "chat1", PeerID: "u1", MessageID: "m2", Text: "再问"})
	run2 := h.sender.lastRun()
	h.publishRun(model.RunEvent{Type: model.EventRunCancelled, RunID: run2.ID, SessionID: run2.SessionID})
	waitFor(t, "取消通知", func() bool { return len(con.Sent()) == 3 })
	if got := con.Sent()[2]; got.Text != DefaultTexts.RunCancelled {
		t.Fatalf("取消通知应使用默认文案，得到 %+v", got)
	}

	h.inject("c1", Inbound{ChatID: "chat1", PeerID: "u1", Text: "第三问"})
	run3 := h.sender.lastRun()
	empty := model.Message{Parts: []model.Part{{Type: model.PartToolCall, ToolCall: &model.ToolCall{Name: "x"}}}}
	h.publishRun(model.RunEvent{Type: model.EventRunCompleted, RunID: run3.ID, SessionID: run3.SessionID, Message: &empty})
	waitFor(t, "空回复提示", func() bool { return len(con.Sent()) == 4 })
	if got := con.Sent()[3]; got.Text != DefaultTexts.EmptyReply || got.ReplyTo != "" {
		t.Fatalf("没有文本时应发送空回复提示，得到 %+v", got)
	}

	// 指针形式的 payload 也能识别；无关会话与无关类型被忽略。
	ptrMsg := msgText("指针")
	ev := model.RunEvent{Type: model.EventRunCompleted, RunID: "R-x", SessionID: run3.SessionID, Message: &ptrMsg}
	h.bus.Publish(event.Event{Type: string(ev.Type), SessionID: ev.SessionID, RunID: ev.RunID, Payload: &ev})
	waitFor(t, "指针 payload", func() bool { return len(con.Sent()) == 5 })
	h.publishRun(model.RunEvent{Type: model.EventRunCompleted, RunID: "R-y", SessionID: "nobody", Message: &msg})
	h.publishRun(model.RunEvent{Type: model.EventTextDelta, RunID: run3.ID, SessionID: run3.SessionID, Delta: "x"})
	settle()
	if n := len(con.Sent()); n != 5 {
		t.Fatalf("无关事件不应产生回复，得到 %d", n)
	}
}

var msg = model.TextMessage("", model.RoleAssistant, "回复")

func msgText(s string) model.Message { return model.TextMessage("", model.RoleAssistant, s) }

func TestBusyAndUnavailableNotices(t *testing.T) {
	h := newHarness(t, Options{})
	h.addChannel("c1", agentA, model.ChannelConsole, "", true)
	h.start()
	con := h.console("c1")

	h.sender.setSendErr(&model.Error{Code: model.CodeConflict, Message: "会话已有在途运行", Details: map[string]any{"run_id": "R0"}})
	h.inject("c1", Inbound{ChatID: "c", PeerID: "u", MessageID: "m1", Text: "快"})
	waitFor(t, "忙碌提示", func() bool { return len(con.Sent()) == 1 })
	if got := con.Sent()[0]; got.Text != DefaultTexts.Busy || got.ReplyTo != "m1" || got.ChatID != "c" {
		t.Fatalf("忙碌提示错误: %+v", got)
	}

	h.sender.setSendErr(model.Wrap(model.CodeInternal, "炸了", errors.New("secret detail")))
	h.inject("c1", Inbound{ChatID: "c", PeerID: "u", Text: "再来"})
	waitFor(t, "不可用提示", func() bool { return len(con.Sent()) == 2 })
	if got := con.Sent()[1].Text; got != DefaultTexts.Unavailable || strings.Contains(got, "secret") {
		t.Fatalf("不可用提示不应泄露内部错误，得到 %q", got)
	}

	h.sender.setSendErr(nil)
	h.sender.ensureErr = model.NotFound("agent", agentA)
	h.inject("c1", Inbound{ChatID: "c", PeerID: "u", Text: "三"})
	waitFor(t, "会话解析失败提示", func() bool { return len(con.Sent()) == 3 })
	if got := con.Sent()[2].Text; got != DefaultTexts.Unavailable {
		t.Fatalf("会话解析失败应提示不可用，得到 %q", got)
	}
}

func TestSendFailureAbortsRemainingChunks(t *testing.T) {
	h := newHarness(t, Options{})
	h.addChannel("c1", agentA, model.ChannelConsole, `{"max_chunk":5}`, true)
	h.start()
	con := h.console("c1")
	con.FailSends(errors.New("平台拒绝"))

	h.inject("c1", Inbound{ChatID: "c", PeerID: "u", Text: "问"})
	run := h.sender.lastRun()
	m := msgText("一二三四五六七八九十")
	h.publishRun(model.RunEvent{Type: model.EventRunCompleted, RunID: run.ID, SessionID: run.SessionID, Message: &m})
	settle()
	if n := len(con.Sent()); n != 0 {
		t.Fatalf("发送失败时不应记录成功的分片，得到 %d", n)
	}
	con.FailSends(nil)
	h.publishRun(model.RunEvent{Type: model.EventRunCompleted, RunID: "R-again", SessionID: run.SessionID, Message: &m})
	waitFor(t, "恢复后发送", func() bool { return len(con.Sent()) == 2 })
}

func TestReloadAppliesNewConfigAndHonoursDisable(t *testing.T) {
	h := newHarness(t, Options{})
	h.addChannel("c1", agentA, model.ChannelConsole, "", true)
	h.start()
	old := h.console("c1")

	h.inject("c1", privateMsg("u2", "改配置前"))
	if n := len(h.sender.sent()); n != 1 {
		t.Fatalf("改配置前应受理，得到 %d", n)
	}

	ch := h.channels.get("c1")
	ch.Config = json.RawMessage(`{"allowed_peers":["u1"]}`)
	_ = h.channels.UpdateChannel(context.Background(), ch)
	if err := h.gw.Reload(context.Background(), "c1"); err != nil {
		t.Fatalf("热更新失败: %v", err)
	}
	if old.Status().Connected {
		t.Fatal("热更新应停止旧适配器")
	}
	if err := old.Inject(privateMsg("u1", "旧适配器")); !model.IsCode(err, model.CodeUnavailable) {
		t.Fatalf("旧适配器停止后注入应失败，得到 %v", err)
	}
	fresh := h.console("c1")
	if fresh == old {
		t.Fatal("热更新应创建新的适配器实例")
	}
	h.inject("c1", privateMsg("u2", "改配置后"))
	if n := len(h.sender.sent()); n != 1 {
		t.Fatalf("新白名单应生效，得到 %d", n)
	}
	h.inject("c1", privateMsg("u1", "白名单内"))
	if n := len(h.sender.sent()); n != 2 {
		t.Fatalf("白名单内应受理，得到 %d", n)
	}

	// 回复仍能送到热更新后的适配器。
	run := h.sender.lastRun()
	m := msgText("更新后回复")
	h.publishRun(model.RunEvent{Type: model.EventRunCompleted, RunID: run.ID, SessionID: run.SessionID, Message: &m})
	waitFor(t, "热更新后回复", func() bool { return len(fresh.Sent()) == 1 })

	// 停用后 Reload 停止通道；此后的事件被丢弃而不是崩溃。
	ch = h.channels.get("c1")
	ch.Enabled = false
	_ = h.channels.UpdateChannel(context.Background(), ch)
	if err := h.gw.Reload(context.Background(), "c1"); err != nil {
		t.Fatalf("停用通道的 Reload 应成功，得到 %v", err)
	}
	if _, ok := h.gw.Status("c1"); ok {
		t.Fatal("停用后通道不应运行")
	}
	h.publishRun(model.RunEvent{Type: model.EventRunCompleted, RunID: "R-late", SessionID: run.SessionID, Message: &m})
	settle()
	if n := len(fresh.Sent()); n != 1 {
		t.Fatalf("停用后不应再发送，得到 %d", n)
	}

	// 启动失败的 Reload 返回错误并写入 LastError；不存在的通道返回 not_found。
	ch = h.channels.get("c1")
	ch.Enabled = true
	ch.Config = json.RawMessage(`{"group_mode":"nope"}`)
	_ = h.channels.UpdateChannel(context.Background(), ch)
	if err := h.gw.Reload(context.Background(), "c1"); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("配置错误的 Reload 应返回 invalid，得到 %v", err)
	}
	if h.channels.get("c1").LastError == "" {
		t.Fatal("Reload 失败应写入 LastError")
	}
	if err := h.gw.Reload(context.Background(), "ghost"); !model.IsCode(err, model.CodeNotFound) {
		t.Fatalf("不存在的通道应返回 not_found，得到 %v", err)
	}
}

func TestReloadBeforeStartIsRejected(t *testing.T) {
	h := newHarness(t, Options{})
	if err := h.gw.Reload(context.Background(), "c1"); !model.IsCode(err, model.CodeUnavailable) {
		t.Fatalf("未启动时 Reload 应返回 unavailable，得到 %v", err)
	}
}

func TestStopDrainsInFlightReplyThenStopsAdapter(t *testing.T) {
	h := newHarness(t, Options{})
	gated := &gatedAdapter{ConsoleAdapter: NewConsole(), entered: make(chan struct{}, 1), gate: make(chan struct{})}
	h.factory.Register(model.ChannelDiscord, func() Adapter { return gated })
	h.addChannel("d1", agentA, model.ChannelDiscord, "", true)
	h.start()

	if err := gated.Inject(privateMsg("u1", "问")); err != nil {
		t.Fatalf("注入失败: %v", err)
	}
	run := h.sender.lastRun()
	m := msgText("停止前的回复")
	h.publishRun(model.RunEvent{Type: model.EventRunCompleted, RunID: run.ID, SessionID: run.SessionID, Message: &m})
	<-gated.entered // 发送进行中

	stopped := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		stopped <- h.gw.Stop(ctx)
	}()
	select {
	case err := <-stopped:
		t.Fatalf("Stop 不应在回复发完之前返回: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if !gated.Status().Connected {
		t.Fatal("排队回复发完之前不应停止适配器")
	}
	close(gated.gate)
	if err := <-stopped; err != nil {
		t.Fatalf("停止失败: %v", err)
	}
	if len(gated.Sent()) != 1 || gated.Sent()[0].Text != "停止前的回复" {
		t.Fatalf("Stop 应先把进行中的回复发完，得到 %+v", gated.Sent())
	}
	if gated.Status().Connected {
		t.Fatal("Stop 应停止适配器")
	}
}

func TestStopGivesUpPendingRepliesWhenContextExpires(t *testing.T) {
	h := newHarness(t, Options{})
	gated := &gatedAdapter{ConsoleAdapter: NewConsole(), entered: make(chan struct{}, 1), gate: make(chan struct{})}
	h.factory.Register(model.ChannelDiscord, func() Adapter { return gated })
	h.addChannel("d1", agentA, model.ChannelDiscord, "", true)
	h.start()

	if err := gated.Inject(privateMsg("u1", "问")); err != nil {
		t.Fatalf("注入失败: %v", err)
	}
	run := h.sender.lastRun()
	m := msgText("永远发不出去")
	h.publishRun(model.RunEvent{Type: model.EventRunCompleted, RunID: run.ID, SessionID: run.SessionID, Message: &m})
	<-gated.entered

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	begin := time.Now()
	if err := h.gw.Stop(ctx); err != nil {
		t.Fatalf("超时放弃后 Stop 仍应成功返回，得到 %v", err)
	}
	if time.Since(begin) > 2*time.Second {
		t.Fatalf("Stop 应在 ctx 到期后放弃等待，用了 %v", time.Since(begin))
	}
	if len(gated.Sent()) != 0 || gated.Status().Connected {
		t.Fatalf("放弃的回复不应算作已发送，适配器应已停止: %+v", gated.Sent())
	}
}

func TestStopAllowsRestart(t *testing.T) {
	h := newHarness(t, Options{})
	h.addChannel("c1", agentA, model.ChannelConsole, "", true)
	if err := h.gw.Start(context.Background()); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	con := h.console("c1")
	h.inject("c1", privateMsg("u1", "问"))
	run := h.sender.lastRun()
	m := msgText("停止前的回复")
	h.publishRun(model.RunEvent{Type: model.EventRunCompleted, RunID: run.ID, SessionID: run.SessionID, Message: &m})
	waitFor(t, "回复送达", func() bool { return len(con.Sent()) == 1 })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.gw.Stop(ctx); err != nil {
		t.Fatalf("停止失败: %v", err)
	}
	if con.Status().Connected {
		t.Fatal("Stop 应停止适配器")
	}
	if _, ok := h.gw.Status("c1"); ok {
		t.Fatal("Stop 后不应有运行中的通道")
	}
	if err := h.gw.Stop(ctx); err != nil {
		t.Fatalf("重复 Stop 应是空操作，得到 %v", err)
	}

	if err := h.gw.Start(context.Background()); err != nil {
		t.Fatalf("重新启动失败: %v", err)
	}
	t.Cleanup(func() { _ = h.gw.Stop(context.Background()) })
	if h.console("c1") == con {
		t.Fatal("重新启动应创建新的适配器")
	}
	h.inject("c1", privateMsg("u1", "重启后"))
	if n := len(h.sender.sent()); n != 2 {
		t.Fatalf("重启后应继续受理，得到 %d", n)
	}
}

func TestLateInboundAfterStopIsDropped(t *testing.T) {
	h := newHarness(t, Options{})
	h.addChannel("c1", agentA, model.ChannelConsole, "", true)
	h.start()
	a, _ := h.gw.Adapter("c1")
	con := a.(*ConsoleAdapter)
	con.mu.Lock()
	inbox := con.inbox // 模拟适配器在停止后仍残留的回调
	con.mu.Unlock()
	_ = h.gw.Stop(context.Background())
	inbox(privateMsg("u1", "迟到"))
	if n := len(h.sender.sent()); n != 0 {
		t.Fatalf("通道停止后迟到的消息应被丢弃，得到 %d", n)
	}
	h.start()
}

func TestFactoryRegistry(t *testing.T) {
	f := DefaultFactory(nil)
	kinds := f.Kinds()
	if len(kinds) != 2 || kinds[0] != model.ChannelConsole || kinds[1] != model.ChannelTelegram {
		t.Fatalf("默认注册表应含 console 与 telegram，得到 %v", kinds)
	}
	a, err := f.New(model.ChannelTelegram)
	if err != nil || a.Kind() != model.ChannelTelegram {
		t.Fatalf("创建 telegram 适配器失败: %v", err)
	}
	b, _ := f.New(model.ChannelTelegram)
	if a == b {
		t.Fatal("每次 New 应返回新的实例")
	}
	if _, err := f.New(model.ChannelDiscord); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("未登记的类型应返回 invalid，得到 %v", err)
	}
	f.Register(model.ChannelDiscord, func() Adapter { return nil })
	if _, err := f.New(model.ChannelDiscord); !model.IsCode(err, model.CodeInternal) {
		t.Fatalf("构造函数返回 nil 应报 internal，得到 %v", err)
	}
}

const agentB = "01AGENTBBBBBBBBBBBBBBBBBBB"

func TestRepliesRouteBySessionKeyWithoutInMemoryRoute(t *testing.T) {
	h := newHarness(t, Options{})
	gs := h.withSessionGetter()
	h.addChannel("c1", agentA, model.ChannelConsole, "", true)
	h.start()
	con := h.console("c1")

	// 守护进程重启后：会话存在，但内存路由表为空（对端尚未再次发言）。
	sess := h.seedSession("S-old", agentA, SessionKey(agentA, model.ChannelConsole, "chat:7"), model.OriginChannel)
	m := msgText("定时任务的回复")
	h.publishRun(model.RunEvent{Type: model.EventRunCompleted, RunID: "R-job", SessionID: sess.ID, AgentID: agentA, Message: &m})
	waitFor(t, "按会话键推导出的回复", func() bool { return len(con.Sent()) == 1 })
	if got := con.Sent()[0]; got.ChatID != "chat:7" || got.Text != "定时任务的回复" || got.ReplyTo != "" {
		t.Fatalf("应按会话键中的 chat_id 回复且不引用入站消息，得到 %+v", got)
	}
	// 推导出的路由被缓存：同一会话的下一个事件不再读会话。
	before := gs.getCount()
	h.publishRun(model.RunEvent{Type: model.EventRunCancelled, RunID: "R-job2", SessionID: sess.ID, AgentID: agentA})
	waitFor(t, "第二条回复", func() bool { return len(con.Sent()) == 2 })
	if gs.getCount() != before {
		t.Fatalf("路由推导后应缓存，Get 次数从 %d 变为 %d", before, gs.getCount())
	}

	// 桌面会话、没有对应通道的智能体都不回复；后者连会话都不读。
	desk := h.seedSession("S-desk", agentA, agentA+":desktop:local", model.OriginDesktop)
	h.publishRun(model.RunEvent{Type: model.EventRunCompleted, RunID: "R-d", SessionID: desk.ID, AgentID: agentA, Message: &m})
	other := h.seedSession("S-b", agentB, SessionKey(agentB, model.ChannelConsole, "x"), model.OriginChannel)
	h.publishRun(model.RunEvent{Type: model.EventRunCompleted, RunID: "R-b", SessionID: other.ID, AgentID: agentB, Message: &m})
	// 键属于 agentA 的 telegram 类型，但 agentA 没有 telegram 通道。
	tg := h.seedSession("S-tg", agentA, SessionKey(agentA, model.ChannelTelegram, "9"), model.OriginChannel)
	h.publishRun(model.RunEvent{Type: model.EventRunCompleted, RunID: "R-tg", SessionID: tg.ID, AgentID: agentA, Message: &m})
	waitFor(t, "S-tg 被读取", func() bool { return gs.getCount() == before+2 })
	settle()
	if n := len(con.Sent()); n != 2 {
		t.Fatalf("非通道会话或没有匹配通道时不应回复，得到 %d 条", n)
	}
	gs.gmu.Lock()
	gets := append([]string(nil), gs.gets...)
	gs.gmu.Unlock()
	for _, id := range gets {
		if id == other.ID {
			t.Fatalf("没有已启动通道的智能体不应读会话，读取记录 %v", gets)
		}
	}
	if len(gets) != before+2 {
		t.Fatalf("应只读取 S-desk 与 S-tg，读取记录 %v", gets)
	}
}

func TestWithoutSessionGetterUnroutedSessionsAreIgnored(t *testing.T) {
	h := newHarness(t, Options{})
	h.addChannel("c1", agentA, model.ChannelConsole, "", true)
	h.start()
	con := h.console("c1")
	sess := h.seedSession("S-old", agentA, SessionKey(agentA, model.ChannelConsole, "chat:7"), model.OriginChannel)
	m := msgText("回复")
	h.publishRun(model.RunEvent{Type: model.EventRunCompleted, RunID: "R1", SessionID: sess.ID, AgentID: agentA, Message: &m})
	settle()
	if n := len(con.Sent()); n != 0 {
		t.Fatalf("Sender 不提供 SessionGetter 时无法推导路由，不应回复，得到 %d", n)
	}
}

func TestPushedMessagesAreDeliveredToChannel(t *testing.T) {
	h := newHarness(t, Options{})
	h.withSessionGetter()
	h.addChannel("c1", agentA, model.ChannelConsole, `{"max_chunk":4}`, true)
	h.start()
	con := h.console("c1")

	// push_text 定时任务写入的通道会话：没有内存路由，按会话键推导。
	sess := h.seedSession("S-push", agentA, SessionKey(agentA, model.ChannelConsole, "chat:1"), model.OriginChannel)
	h.publishPushed(sess.ID, agentA, "早上好呀朋友")
	waitFor(t, "推送送达", func() bool { return len(con.Sent()) == 2 })
	if got := con.Sent(); got[0].ChatID != "chat:1" || got[0].Text+got[1].Text != "早上好呀朋友" || got[0].ReplyTo != "" {
		t.Fatalf("推送应分片送到会话键中的 chat，得到 %+v", got)
	}

	// 已有内存路由（对端发过言）的会话同样送达。
	h.inject("c1", Inbound{ChatID: "chat:2", PeerID: "u2", Text: "在吗"})
	live := h.sender.session(SessionKey(agentA, model.ChannelConsole, "chat:2"))
	h.publishPushed(live.ID, agentA, "提醒")
	waitFor(t, "已路由会话的推送", func() bool { return len(con.Sent()) == 3 })
	if got := con.Sent()[2]; got.ChatID != "chat:2" || got.Text != "提醒" {
		t.Fatalf("推送应送到已路由的 chat，得到 %+v", got)
	}

	// 用户消息、运行产出的助手消息、没有 pushed 标记的助手消息都不经 message.created 外发。
	user := model.TextMessage(live.ID, model.RoleUser, "用户消息")
	user.Meta = json.RawMessage(`{"pushed":true}`)
	fromRun := model.TextMessage(live.ID, model.RoleAssistant, "运行回复")
	fromRun.RunID = "R-x"
	fromRun.Meta = json.RawMessage(`{"pushed":true}`)
	plain := model.TextMessage(live.ID, model.RoleAssistant, "普通")
	for _, m := range []*model.Message{&user, &fromRun, &plain} {
		h.publishRun(model.RunEvent{Type: model.EventMessageCreated, RunID: m.RunID, SessionID: live.ID, AgentID: agentA, Message: m})
	}
	// 桌面会话的推送不外发。
	desk := h.seedSession("S-desk", agentA, agentA+":desktop:local", model.OriginDesktop)
	h.publishPushed(desk.ID, agentA, "桌面推送")
	settle()
	if n := len(con.Sent()); n != 3 {
		t.Fatalf("只有通道会话的推送消息应外发，得到 %d 条: %+v", n, con.Sent())
	}
}

func TestRunFailureNoticeUsesRunErrorMap(t *testing.T) {
	h := newHarness(t, Options{Texts: Texts{RunErrorMap: map[string]string{"run timeout": "运行超时"}}})
	h.addChannel("c1", agentA, model.ChannelConsole, "", true)
	h.start()
	con := h.console("c1")

	h.inject("c1", Inbound{ChatID: "c", PeerID: "u", MessageID: "m1", Text: "慢问题"})
	run := h.sender.lastRun()
	h.publishRun(model.RunEvent{Type: model.EventRunFailed, RunID: run.ID, SessionID: run.SessionID, Error: "run timeout"})
	waitFor(t, "超时通知", func() bool { return len(con.Sent()) == 1 })
	if got := con.Sent()[0].Text; got != "处理失败：运行超时" {
		t.Fatalf("机器常量应换成本地化文案，得到 %q", got)
	}
	h.inject("c1", Inbound{ChatID: "c", PeerID: "u", Text: "再问"})
	run2 := h.sender.lastRun()
	h.publishRun(model.RunEvent{Type: model.EventRunFailed, RunID: run2.ID, SessionID: run2.SessionID, Error: "daemon restarted"})
	waitFor(t, "未映射的失败通知", func() bool { return len(con.Sent()) == 2 })
	if got := con.Sent()[1].Text; got != "处理失败" || strings.Contains(got, "daemon") {
		t.Fatalf("未映射的机器常量不应原样外发，得到 %q", got)
	}
}

func TestForgetAgentStopsItsChannelsAndRoutes(t *testing.T) {
	h := newHarness(t, Options{})
	if err := h.gw.ForgetAgent(context.Background(), agentA); err != nil {
		t.Fatalf("网关未启动时 ForgetAgent 应是空操作，得到 %v", err)
	}
	h.addChannel("a1", agentA, model.ChannelConsole, "", true)
	h.addChannel("a2", agentA, model.ChannelConsole, "", true)
	h.addChannel("b1", agentB, model.ChannelConsole, "", true)
	h.start()
	conA, conB := h.console("a1"), h.console("b1")

	h.inject("a1", Inbound{ChatID: "chat", PeerID: "u", Text: "你好"})
	runA := h.sender.lastRun()
	h.inject("b1", Inbound{ChatID: "chat", PeerID: "u", Text: "你好"})
	runB := h.sender.lastRun()

	if err := h.gw.ForgetAgent(context.Background(), agentA); err != nil {
		t.Fatalf("ForgetAgent 失败: %v", err)
	}
	for _, id := range []string{"a1", "a2"} {
		if _, ok := h.gw.Status(id); ok {
			t.Fatalf("被删除智能体的通道 %s 应已停止", id)
		}
	}
	if _, ok := h.gw.Status("b1"); !ok {
		t.Fatal("其他智能体的通道不应受影响")
	}
	if err := conA.Inject(Inbound{ChatID: "chat", PeerID: "u", Text: "还在吗"}); err == nil {
		t.Fatal("已停止的适配器不应再接收入站消息")
	}
	h.gw.mu.Lock()
	_, hasA := h.gw.routes[runA.SessionID]
	_, hasB := h.gw.routes[runB.SessionID]
	h.gw.mu.Unlock()
	if hasA || !hasB {
		t.Fatalf("应只清除该智能体的回复路由：A=%v B=%v", hasA, hasB)
	}

	m := msgText("迟到的回复")
	h.publishRun(model.RunEvent{Type: model.EventRunCompleted, RunID: runA.ID, SessionID: runA.SessionID, AgentID: agentA, Message: &m})
	h.publishRun(model.RunEvent{Type: model.EventRunCompleted, RunID: runB.ID, SessionID: runB.SessionID, AgentID: agentB, Message: &m})
	waitFor(t, "其他智能体的回复", func() bool { return len(conB.Sent()) == 1 })
	settle()
	if n := len(conA.Sent()); n != 0 {
		t.Fatalf("被删除智能体的回复不应再发出，得到 %d", n)
	}
	if err := h.gw.ForgetAgent(context.Background(), ""); !model.IsCode(err, model.CodeInvalid) {
		t.Fatalf("空智能体 ID 应返回 invalid，得到 %v", err)
	}
}
