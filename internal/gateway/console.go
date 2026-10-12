package gateway

import (
	"context"
	"sync"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// ConsoleAdapter 是内存实现的适配器：调用方用 Inject 模拟入站消息，用 Sent 读取发出的回复。
// 供测试与 CLI 的本地对话使用；并发安全。
type ConsoleAdapter struct {
	mu        sync.Mutex
	channelID string
	inbox     func(Inbound)
	started   bool
	sent      []Outbound
	onSend    func(Outbound)
	sendErr   error
	updatedAt time.Time
}

// NewConsole 创建一个未启动的控制台适配器。
func NewConsole() *ConsoleAdapter { return &ConsoleAdapter{} }

// Kind 返回 model.ChannelConsole。
func (c *ConsoleAdapter) Kind() model.ChannelKind { return model.ChannelConsole }

// Start 登记通道与接收回调；secret 被忽略。重复启动返回 CodeConflict。
func (c *ConsoleAdapter) Start(_ context.Context, ch *model.Channel, _ string, inbox func(Inbound)) error {
	if ch == nil || inbox == nil {
		return model.NewError(model.CodeInvalid, "console: 缺少通道或接收回调")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return model.NewError(model.CodeConflict, "console: 适配器已经启动")
	}
	c.channelID = ch.ID
	c.inbox = inbox
	c.started = true
	c.updatedAt = time.Now().UTC()
	return nil
}

// Send 记录一条出站消息并调用 OnSend 钩子。
func (c *ConsoleAdapter) Send(ctx context.Context, out Outbound) error {
	if err := ctx.Err(); err != nil {
		return model.Wrap(model.CodeCancelled, "console: 发送被取消", err)
	}
	c.mu.Lock()
	if c.sendErr != nil {
		err := c.sendErr
		c.mu.Unlock()
		return err
	}
	c.sent = append(c.sent, out)
	hook := c.onSend
	c.mu.Unlock()
	if hook != nil {
		hook(out)
	}
	return nil
}

// Stop 停止接收；之后 Inject 返回错误。可重复调用。
func (c *ConsoleAdapter) Stop(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.started = false
	c.inbox = nil
	c.updatedAt = time.Now().UTC()
	return nil
}

// Inject 模拟一条入站消息并同步交给网关处理；ChannelID、Kind、At 为空时自动填充。
// 适配器未启动时返回 CodeUnavailable。
func (c *ConsoleAdapter) Inject(in Inbound) error {
	c.mu.Lock()
	inbox := c.inbox
	channelID := c.channelID
	c.mu.Unlock()
	if inbox == nil {
		return model.NewError(model.CodeUnavailable, "console: 适配器未启动")
	}
	if in.ChannelID == "" {
		in.ChannelID = channelID
	}
	if in.Kind == "" {
		in.Kind = model.ChannelConsole
	}
	if in.At.IsZero() {
		in.At = time.Now().UTC()
	}
	inbox(in)
	return nil
}

// Sent 返回迄今发出的全部消息的副本。
func (c *ConsoleAdapter) Sent() []Outbound {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Outbound(nil), c.sent...)
}

// Reset 清空已记录的出站消息。
func (c *ConsoleAdapter) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = nil
}

// OnSend 设置每次 Send 成功后的回调（例如 CLI 打印到终端）；在 Start 前后都可设置。
func (c *ConsoleAdapter) OnSend(fn func(Outbound)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onSend = fn
}

// FailSends 让后续 Send 返回 err（err 为 nil 恢复正常）；用于测试发送失败路径。
func (c *ConsoleAdapter) FailSends(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sendErr = err
}

// Status 实现 StatusReporter：已启动即视为已连接。
func (c *ConsoleAdapter) Status() AdapterStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return AdapterStatus{Connected: c.started, UpdatedAt: c.updatedAt}
}
