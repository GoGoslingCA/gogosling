package model

import (
	"context"
	"encoding/json"
	"time"
)

// ChannelKind 是 IM 平台类型；适配器按它注册。
type ChannelKind string

const (
	ChannelConsole  ChannelKind = "console"
	ChannelTelegram ChannelKind = "telegram"
	ChannelFeishu   ChannelKind = "feishu"
	ChannelDingTalk ChannelKind = "dingtalk"
	ChannelWeCom    ChannelKind = "wecom"
	ChannelDiscord  ChannelKind = "discord"
)

// Channel 是一条绑定到某个智能体的 IM 通道配置。平台凭据只保存 secret 引用。
type Channel struct {
	ID      string      `json:"id"`
	AgentID string      `json:"agent_id"`
	Kind    ChannelKind `json:"kind"`
	Name    string      `json:"name"`
	// SecretRef 指向平台凭据（Bot Token、App Secret 等）在 secret 存储中的名称。
	SecretRef string `json:"secret_ref,omitempty"`
	// Config 是平台相关的非敏感配置（允许的发送者、群聊触发规则等），结构随 Kind 而异。
	Config  json.RawMessage `json:"config,omitempty"`
	Enabled bool            `json:"enabled"`
	// LastError 记录最近一次连接失败的原因，便于界面展示。
	LastError string    `json:"last_error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ChannelRepo 持久化 IM 通道。
type ChannelRepo interface {
	CreateChannel(ctx context.Context, c *Channel) error
	GetChannel(ctx context.Context, id string) (*Channel, error)
	ListChannels(ctx context.Context, agentID string) ([]*Channel, error)
	UpdateChannel(ctx context.Context, c *Channel) error
	DeleteChannel(ctx context.Context, id string) error
}
