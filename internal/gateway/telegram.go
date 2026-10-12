package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// Telegram Bot API 相关常量。
const (
	// TelegramDefaultBaseURL 是官方 Bot API 地址；通道配置的 api_base 可以覆盖（例如本地 Bot API 服务器）。
	TelegramDefaultBaseURL = "https://api.telegram.org"
	// TelegramMaxMessage 是 sendMessage 单条文本的硬上限。
	TelegramMaxMessage = 4096
	// telegramResponseMax 是读取 Bot API 响应体的上限，防止异常响应耗尽内存。
	telegramResponseMax = 4 << 20
)

// TelegramOptions 是 Telegram 适配器的可选配置；零值字段使用默认值。
type TelegramOptions struct {
	// BaseURL 是 Bot API 地址，默认 TelegramDefaultBaseURL；通道配置里的 api_base 优先级更高。
	BaseURL string
	// HTTPClient 默认是不带全局超时的 http.Client（每个请求用 ctx 限时）。
	HTTPClient *http.Client
	// PollTimeout 是 getUpdates 长轮询的等待时长，默认 25 秒。
	PollTimeout time.Duration
	// MinBackoff / MaxBackoff 是轮询失败后的退避区间，默认 1 秒到 30 秒。
	MinBackoff time.Duration
	MaxBackoff time.Duration
	// SendRetries 是发送失败（网络错误、5xx、429）时的最大重试次数，默认 3。
	SendRetries int
	// StartTimeout 是 Start 中校验凭据（getMe）的时限，默认 10 秒。
	StartTimeout time.Duration
	Logger       *slog.Logger
}

func (o TelegramOptions) withDefaults() TelegramOptions {
	if o.BaseURL == "" {
		o.BaseURL = TelegramDefaultBaseURL
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{}
	}
	if o.PollTimeout <= 0 {
		o.PollTimeout = 25 * time.Second
	}
	if o.MinBackoff <= 0 {
		o.MinBackoff = time.Second
	}
	if o.MaxBackoff < o.MinBackoff {
		o.MaxBackoff = max(o.MinBackoff, 30*time.Second)
	}
	if o.SendRetries < 0 {
		o.SendRetries = 0
	} else if o.SendRetries == 0 {
		o.SendRetries = 3
	}
	if o.StartTimeout <= 0 {
		o.StartTimeout = 10 * time.Second
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	return o
}

// TelegramAdapter 用 Bot API 长轮询（getUpdates）接收消息、用 sendMessage 发送；不依赖 SDK。
// 支持私聊与群聊的文本消息（含媒体的说明文字）；并发安全。
type TelegramAdapter struct {
	opts TelegramOptions
	log  *slog.Logger

	mu          sync.Mutex
	started     bool
	token       string
	base        string
	channelID   string
	botID       int64
	botUsername string
	inbox       func(Inbound)
	cancel      context.CancelFunc
	done        chan struct{}
	status      AdapterStatus
}

// NewTelegram 创建一个未启动的 Telegram 适配器。
func NewTelegram(opts TelegramOptions) *TelegramAdapter {
	opts = opts.withDefaults()
	return &TelegramAdapter{opts: opts, log: opts.Logger}
}

// Kind 返回 model.ChannelTelegram。
func (t *TelegramAdapter) Kind() model.ChannelKind { return model.ChannelTelegram }

// telegramChannelConfig 是通道配置中 telegram 专有的键。
type telegramChannelConfig struct {
	APIBase string `json:"api_base,omitempty"`
}

// Start 校验 token（getMe）并启动长轮询。token 明确无效时返回错误；
// 网络暂时不可达时记录状态、照常启动轮询并在后台退避重试。
func (t *TelegramAdapter) Start(ctx context.Context, ch *model.Channel, secret string, inbox func(Inbound)) error {
	if ch == nil || inbox == nil {
		return model.NewError(model.CodeInvalid, "telegram: 缺少通道或接收回调")
	}
	token := strings.TrimSpace(secret)
	if token == "" {
		return model.NewError(model.CodeInvalid, "telegram: 缺少 bot token，请在通道的 secret_ref 指向的凭据里配置")
	}
	if strings.ContainsAny(token, "/ \t\r\n?#") {
		return model.NewError(model.CodeInvalid, "telegram: bot token 含有非法字符")
	}
	base, err := t.resolveBase(ch.Config)
	if err != nil {
		return err
	}

	t.mu.Lock()
	if t.started {
		t.mu.Unlock()
		return model.NewError(model.CodeConflict, "telegram: 适配器已经启动")
	}
	t.started = true
	t.token = token
	t.base = base
	t.channelID = ch.ID
	t.inbox = inbox
	pctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	t.cancel = cancel
	t.done = make(chan struct{})
	t.mu.Unlock()

	sctx, scancel := context.WithTimeout(ctx, t.opts.StartTimeout)
	err = t.loadMe(sctx)
	scancel()
	if err != nil {
		if definite := model.CodeOf(err); definite == model.CodeUnauthorized || definite == model.CodeInvalid {
			cancel()
			t.mu.Lock()
			t.started = false
			t.token = ""
			t.inbox = nil
			close(t.done)
			t.mu.Unlock()
			return err
		}
		t.log.Warn("telegram: 启动时无法连接 Bot API，将在后台重试", "channel_id", ch.ID, "error", err)
		t.setStatus(false, err)
	}

	go t.pollLoop(pctx)
	return nil
}

// resolveBase 取通道配置里的 api_base（校验为 http/https 地址），否则用选项里的默认值。
func (t *TelegramAdapter) resolveBase(raw json.RawMessage) (string, error) {
	base := t.opts.BaseURL
	if len(bytes.TrimSpace(raw)) > 0 {
		var cfg telegramChannelConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return "", model.Wrap(model.CodeInvalid, "telegram: 通道配置不是合法 JSON", err)
		}
		if cfg.APIBase != "" {
			u, err := url.Parse(cfg.APIBase)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
				return "", model.NewError(model.CodeInvalid, "telegram: api_base 必须是 http(s) 地址且不含查询参数")
			}
			base = cfg.APIBase
		}
	}
	return strings.TrimRight(base, "/"), nil
}

// Stop 结束长轮询并等待其退出；可重复调用。
func (t *TelegramAdapter) Stop(ctx context.Context) error {
	t.mu.Lock()
	if !t.started {
		t.mu.Unlock()
		return nil
	}
	t.started = false
	t.inbox = nil
	done := t.done
	// 在锁内取消：观察到 started=false 的调用方可以确定轮询 ctx 已经结束。
	t.cancel()
	t.mu.Unlock()

	select {
	case <-done:
	case <-ctx.Done():
		return model.Wrap(model.CodeTimeout, "telegram: 等待轮询结束超时", ctx.Err())
	}
	// 轮询（含停止时的补确认）已经结束，此后不再需要凭据；清掉它让被替换的旧实例无法再发送。
	t.mu.Lock()
	t.token = ""
	t.mu.Unlock()
	return nil
}

// Status 实现 StatusReporter。
func (t *TelegramAdapter) Status() AdapterStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.status
}

func (t *TelegramAdapter) setStatus(connected bool, err error) {
	msg := ""
	if err != nil {
		msg = t.redact(err.Error()) // redact 自己加锁，必须在持锁之前调用
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.status = AdapterStatus{Connected: connected, LastError: msg, UpdatedAt: time.Now().UTC()}
}

// BotUsername 返回 getMe 得到的机器人用户名（未知时为空）。
func (t *TelegramAdapter) BotUsername() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.botUsername
}

// Send 把文本发到 chat；超过硬上限时防御性地分片。失败按 SendRetries 重试网络错误、5xx 与 429。
func (t *TelegramAdapter) Send(ctx context.Context, out Outbound) error {
	t.mu.Lock()
	token := t.token
	t.mu.Unlock()
	if token == "" {
		return model.NewError(model.CodeUnavailable, "telegram: 适配器未启动")
	}
	if strings.TrimSpace(out.Text) == "" {
		return nil
	}
	chatID, err := telegramChatID(out.ChatID)
	if err != nil {
		return err
	}
	for i, chunk := range SplitText(out.Text, TelegramMaxMessage) {
		body := map[string]any{"chat_id": chatID, "text": chunk}
		if i == 0 && out.ReplyTo != "" {
			if id, perr := strconv.ParseInt(out.ReplyTo, 10, 64); perr == nil {
				body["reply_to_message_id"] = id
				body["allow_sending_without_reply"] = true
			}
		}
		if err := t.callWithRetry(ctx, "sendMessage", body); err != nil {
			return err
		}
	}
	return nil
}

// telegramChatID 把字符串 chat_id 转成 Bot API 接受的形式：数字 ID 用整数，否则原样（如 @channel）。
func telegramChatID(s string) (any, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, model.NewError(model.CodeInvalid, "telegram: 缺少 chat_id")
	}
	if id, err := strconv.ParseInt(s, 10, 64); err == nil {
		return id, nil
	}
	return s, nil
}

// callWithRetry 对可重试错误做指数退避；429 按 retry_after 等待。
func (t *TelegramAdapter) callWithRetry(ctx context.Context, method string, body map[string]any) error {
	backoff := t.opts.MinBackoff
	for attempt := 0; ; attempt++ {
		_, err := t.call(ctx, method, body)
		if err == nil {
			return nil
		}
		var te *telegramError
		retriable := model.IsCode(err, model.CodeUnavailable)
		if attempt >= t.opts.SendRetries || !retriable {
			return err
		}
		wait := backoff
		if errors.As(err, &te) && te.retryAfter > 0 {
			wait = time.Duration(te.retryAfter) * time.Second
		}
		t.log.Warn("telegram: 调用失败，稍后重试", "method", method, "attempt", attempt+1, "wait", wait, "error", t.redact(err.Error()))
		if err := sleepCtx(ctx, wait); err != nil {
			return model.Wrap(model.CodeCancelled, "telegram: 重试被取消", err)
		}
		backoff = min(backoff*2, t.opts.MaxBackoff)
	}
}

// pollLoop 用 getUpdates 长轮询接收更新，失败时指数退避；每批更新处理完后用下一次调用的 offset 确认。
func (t *TelegramAdapter) pollLoop(ctx context.Context) {
	defer close(t.done)
	var offset, acked int64
	backoff := t.opts.MinBackoff
	for {
		if ctx.Err() != nil {
			break
		}
		if t.BotUsername() == "" {
			if err := t.loadMe(ctx); err != nil {
				t.setStatus(false, err)
				if !t.sleepBackoff(ctx, &backoff) {
					break
				}
				continue
			}
		}
		updates, err := t.getUpdates(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			t.setStatus(false, err)
			var te *telegramError
			wait := backoff
			if errors.As(err, &te) {
				switch {
				case te.retryAfter > 0:
					wait = time.Duration(te.retryAfter) * time.Second
				case te.code == http.StatusConflict:
					// 同一 token 有另一个 getUpdates 消费者（例如另一个实例）；等久一点再争抢。
					wait = t.opts.MaxBackoff
				}
			}
			t.log.Warn("telegram: 拉取更新失败", "channel_id", t.channelID, "wait", wait, "error", t.redact(err.Error()))
			if sleepCtx(ctx, wait) != nil {
				break
			}
			backoff = min(backoff*2, t.opts.MaxBackoff)
			continue
		}
		backoff = t.opts.MinBackoff
		acked = offset
		t.setStatus(true, nil)
		for _, u := range updates {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			if u.Message != nil {
				t.handleMessage(*u.Message)
			}
			if ctx.Err() != nil {
				break
			}
		}
	}
	if offset > acked {
		// 已处理但尚未确认的更新：尽力确认，避免重启后重复回复。
		actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		_, _ = t.call(actx, "getUpdates", map[string]any{"offset": offset, "timeout": 0, "limit": 1})
		cancel()
	}
	t.setStatus(false, nil)
}

// sleepBackoff 按当前退避等待并加倍；ctx 结束返回 false。
func (t *TelegramAdapter) sleepBackoff(ctx context.Context, backoff *time.Duration) bool {
	if sleepCtx(ctx, *backoff) != nil {
		return false
	}
	*backoff = min(*backoff*2, t.opts.MaxBackoff)
	return true
}

// loadMe 调用 getMe 记录机器人 ID 与用户名。
func (t *TelegramAdapter) loadMe(ctx context.Context) error {
	raw, err := t.call(ctx, "getMe", nil)
	if err != nil {
		return err
	}
	var me struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	}
	if err := json.Unmarshal(raw, &me); err != nil {
		return model.Wrap(model.CodeUnavailable, "telegram: getMe 响应无法解析", err)
	}
	if me.ID == 0 {
		return model.NewError(model.CodeUnavailable, "telegram: getMe 响应缺少机器人 ID")
	}
	t.mu.Lock()
	t.botID = me.ID
	t.botUsername = me.Username
	t.mu.Unlock()
	return nil
}

// telegramUpdate 是 getUpdates 返回的一条更新（只解析需要的字段）。
type telegramUpdate struct {
	UpdateID int64            `json:"update_id"`
	Message  *telegramMessage `json:"message"`
}

type telegramMessage struct {
	MessageID int64             `json:"message_id"`
	Date      int64             `json:"date"`
	Text      string            `json:"text"`
	Caption   string            `json:"caption"`
	From      *telegramUser     `json:"from"`
	Chat      telegramChat      `json:"chat"`
	Entities  []telegramEntity  `json:"entities"`
	ReplyTo   *telegramReplyRef `json:"reply_to_message"`
}

type telegramUser struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

type telegramChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type telegramEntity struct {
	Type string `json:"type"`
}

type telegramReplyRef struct {
	From *telegramUser `json:"from"`
}

// getUpdates 拉取一批更新。
func (t *TelegramAdapter) getUpdates(ctx context.Context, offset int64) ([]telegramUpdate, error) {
	rctx, cancel := context.WithTimeout(ctx, t.opts.PollTimeout+15*time.Second)
	defer cancel()
	raw, err := t.call(rctx, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         int(t.opts.PollTimeout / time.Second),
		"allowed_updates": []string{"message"},
	})
	if err != nil {
		return nil, err
	}
	var updates []telegramUpdate
	if err := json.Unmarshal(raw, &updates); err != nil {
		return nil, model.Wrap(model.CodeUnavailable, "telegram: getUpdates 响应无法解析", err)
	}
	return updates, nil
}

// handleMessage 把一条平台消息归一化为 Inbound 并交给网关。
func (t *TelegramAdapter) handleMessage(m telegramMessage) {
	if m.From == nil || m.From.IsBot {
		return
	}
	var isGroup bool
	switch m.Chat.Type {
	case "private":
	case "group", "supergroup":
		isGroup = true
	default:
		// 频道帖子等没有明确发送者的消息不处理。
		return
	}
	text := m.Text
	if text == "" {
		text = m.Caption
	}
	if text == "" {
		t.log.Debug("telegram: 忽略非文本消息", "chat_id", m.Chat.ID, "message_id", m.MessageID)
		return
	}

	t.mu.Lock()
	inbox, botID, username := t.inbox, t.botID, t.botUsername
	t.mu.Unlock()
	if inbox == nil {
		return
	}

	mentioned := false
	if username != "" {
		tag := "@" + username
		for _, e := range m.Entities {
			if (e.Type == "mention" || e.Type == "bot_command") && containsFold(text, tag) {
				mentioned = true
				break
			}
		}
		if mentioned {
			text = strings.TrimSpace(removeFold(text, tag))
		}
	}
	if m.ReplyTo != nil && m.ReplyTo.From != nil && m.ReplyTo.From.ID == botID && botID != 0 {
		mentioned = true
	}

	name := strings.TrimSpace(m.From.FirstName + " " + m.From.LastName)
	if name == "" {
		name = m.From.Username
	}
	at := time.Now().UTC()
	if m.Date > 0 {
		at = time.Unix(m.Date, 0).UTC()
	}
	inbox(Inbound{
		ChannelID: t.channelID,
		Kind:      model.ChannelTelegram,
		ChatID:    strconv.FormatInt(m.Chat.ID, 10),
		PeerID:    strconv.FormatInt(m.From.ID, 10),
		PeerName:  name,
		IsGroup:   isGroup,
		MessageID: strconv.FormatInt(m.MessageID, 10),
		Text:      text,
		At:        at,
		Mentioned: mentioned,
	})
}

// containsFold 不区分大小写地判断子串。
func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}

// removeFold 不区分大小写地删除所有子串出现。
func removeFold(s, sub string) string {
	if sub == "" {
		return s
	}
	lower, lsub := strings.ToLower(s), strings.ToLower(sub)
	var b strings.Builder
	for {
		i := strings.Index(lower, lsub)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		s, lower = s[i+len(sub):], lower[i+len(lsub):]
	}
}

// telegramError 是 Bot API 返回的错误响应。
type telegramError struct {
	method      string
	code        int
	description string
	retryAfter  int
}

func (e *telegramError) Error() string {
	return fmt.Sprintf("telegram %s: %d %s", e.method, e.code, e.description)
}

// call 发起一次 Bot API 调用并返回 result 原文。错误带 model.Code：401/403/4xx 为确定性错误，网络错误与 5xx、429 为 CodeUnavailable。
// 错误信息里绝不包含 token（net/url 的错误会带上完整 URL，这里只保留底层原因）。
func (t *TelegramAdapter) call(ctx context.Context, method string, body map[string]any) (json.RawMessage, error) {
	t.mu.Lock()
	token, base := t.token, t.base
	t.mu.Unlock()
	if body == nil {
		body = map[string]any{}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, model.Wrap(model.CodeInternal, "telegram: 序列化请求失败", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/bot"+token+"/"+method, bytes.NewReader(payload))
	if err != nil {
		return nil, model.Wrap(model.CodeInternal, "telegram: 构造请求失败", stripURLError(err))
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.opts.HTTPClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, model.Wrap(model.CodeCancelled, "telegram: 请求被取消", ctx.Err())
		}
		return nil, model.Wrap(model.CodeUnavailable, "telegram: "+method+" 请求失败", stripURLError(err))
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, telegramResponseMax))
	if err != nil {
		return nil, model.Wrap(model.CodeUnavailable, "telegram: 读取响应失败", stripURLError(err))
	}

	var env struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
		ErrorCode   int             `json:"error_code"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		// 非 JSON 响应多半来自代理或网关故障，按暂时不可用处理以便重试。
		return nil, &model.Error{Code: model.CodeUnavailable, Message: fmt.Sprintf("telegram %s: HTTP %d 响应无法解析", method, resp.StatusCode)}
	}
	if env.OK {
		return env.Result, nil
	}
	code := env.ErrorCode
	if code == 0 {
		code = resp.StatusCode
	}
	te := &telegramError{method: method, code: code, description: t.redact(env.Description), retryAfter: env.Parameters.RetryAfter}
	var mc model.Code
	switch {
	case code == http.StatusUnauthorized:
		mc = model.CodeUnauthorized
	case code == http.StatusForbidden:
		mc = model.CodeForbidden
	case code == http.StatusTooManyRequests, code >= 500, code == http.StatusConflict:
		mc = model.CodeUnavailable
	default:
		mc = model.CodeInvalid
	}
	return nil, &model.Error{Code: mc, Message: te.Error(), Err: te}
}

// stripURLError 去掉 *url.Error 外壳（它的文本包含带 token 的完整 URL），只保留底层原因。
func stripURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err
	}
	return err
}

// redact 把文本里出现的 token 替换为星号。
func (t *TelegramAdapter) redact(s string) string {
	t.mu.Lock()
	token := t.token
	t.mu.Unlock()
	return redact(s, token)
}

// sleepCtx 等待 d 或 ctx 结束。
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
