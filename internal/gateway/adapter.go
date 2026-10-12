package gateway

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// Inbound 是适配器归一化后的一条入站消息。所有字段都来自平台，必须视为不可信数据。
type Inbound struct {
	// ChannelID 是产生这条消息的通道；适配器留空时由 Gateway 填充。
	ChannelID string
	Kind      model.ChannelKind
	// ChatID 是平台会话标识：私聊通常等于对端 ID，群聊为群 ID。它决定会话键。
	ChatID string
	// PeerID 是发送者在平台上的稳定标识；PeerName 是展示名，可为空。
	PeerID   string
	PeerName string
	IsGroup  bool
	// MessageID 是平台消息 ID，用于回复时引用；可为空。
	MessageID   string
	Text        string
	Attachments []model.Attachment
	// At 是平台记录的消息时间；零值时由 Gateway 填为当前时间。
	At time.Time
	// Mentioned 表示机器人在群聊中被显式 @ 或被回复；由能识别提及的适配器填写，
	// 供 group_mode=mention 的触发规则使用。私聊可忽略。
	Mentioned bool
}

// Outbound 是一条要送回平台的消息。Text 已按通道上限分片，适配器不需要再拆分（但可以防御性地拆分）。
type Outbound struct {
	ChannelID string
	ChatID    string
	Text      string
	// ReplyTo 是被回复的平台消息 ID；平台不支持或为空时忽略。
	ReplyTo string
}

// Adapter 是平台适配器。一个实例只服务一个通道；并发安全。
//
// 约定：
//   - Start 做必要的凭据校验后立即返回，长轮询或长连接在后台 goroutine 里运行，直到 Stop。
//     凭据校验失败（如 token 无效）应返回错误；网络暂时不可达应在后台退避重试而不是返回错误。
//   - inbox 由适配器在收到消息时同步调用；调用返回后适配器才确认（ack）这条消息，
//     因此 inbox 的实现要快速返回，但可以阻塞到消息被受理。
//   - secret 是平台凭据明文，只能保存在内存里，不得进入日志、错误信息或持久化。
//   - Stop 之后不再调用 inbox；Send 在 Stop 之后的行为由实现定义。
type Adapter interface {
	Kind() model.ChannelKind
	Start(ctx context.Context, ch *model.Channel, secret string, inbox func(Inbound)) error
	Send(ctx context.Context, out Outbound) error
	Stop(ctx context.Context) error
}

// AdapterStatus 是适配器的连接状态快照。
type AdapterStatus struct {
	Connected bool
	// LastError 是最近一次失败的原因（已脱敏），连接正常时为空。
	LastError string
	UpdatedAt time.Time
}

// StatusReporter 是适配器可选实现的接口：Gateway.Status 会把它的结果合并到通道状态里。
type StatusReporter interface {
	Status() AdapterStatus
}

// Factory 是按 ChannelKind 创建适配器的注册表；并发安全。
type Factory struct {
	mu    sync.RWMutex
	ctors map[model.ChannelKind]func() Adapter
}

// NewFactory 创建一个空注册表。
func NewFactory() *Factory {
	return &Factory{ctors: make(map[model.ChannelKind]func() Adapter)}
}

// DefaultFactory 返回注册了 console 与 telegram 适配器的注册表。logger 为 nil 时丢弃适配器日志。
func DefaultFactory(logger *slog.Logger) *Factory {
	f := NewFactory()
	f.Register(model.ChannelConsole, func() Adapter { return NewConsole() })
	f.Register(model.ChannelTelegram, func() Adapter { return NewTelegram(TelegramOptions{Logger: logger}) })
	return f
}

// Register 登记某种通道的构造函数；重复登记覆盖之前的。ctor 每次调用必须返回新的实例。
func (f *Factory) Register(kind model.ChannelKind, ctor func() Adapter) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ctors[kind] = ctor
}

// New 为通道类型创建一个新的适配器实例；未登记的类型返回 CodeInvalid。
func (f *Factory) New(kind model.ChannelKind) (Adapter, error) {
	f.mu.RLock()
	ctor := f.ctors[kind]
	f.mu.RUnlock()
	if ctor == nil {
		return nil, &model.Error{
			Code:    model.CodeInvalid,
			Message: "不支持的通道类型",
			Details: map[string]any{"kind": string(kind)},
		}
	}
	a := ctor()
	if a == nil {
		return nil, model.Errorf(model.CodeInternal, "通道类型 %s 的构造函数返回了 nil", kind)
	}
	return a, nil
}

// Kinds 返回已登记的通道类型，按字典序。
func (f *Factory) Kinds() []model.ChannelKind {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]model.ChannelKind, 0, len(f.ctors))
	for k := range f.ctors {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
