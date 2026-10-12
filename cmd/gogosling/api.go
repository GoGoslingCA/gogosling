package main

import (
	"context"
	"encoding/json"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// api 是 CLI 用到的本地 API 能力。生产实现是 internal/client 的薄包装（client_adapter.go）；
// 测试可以用 httptest 假服务器驱动真实客户端，也可以直接替换这个接口。
//
// 所有方法的错误都应能用 errorCode / errorMessage 取出 model.Code 与服务端本地化的消息。
type api interface {
	Version(ctx context.Context) (versionInfo, error)

	ListAgents(ctx context.Context) ([]model.Agent, error)
	GetAgent(ctx context.Context, id string) (model.Agent, error)
	CreateAgent(ctx context.Context, in agentInput) (model.Agent, error)
	DeleteAgent(ctx context.Context, id string) error

	ListProviders(ctx context.Context) ([]providerView, error)
	CreateProvider(ctx context.Context, in providerInput) (providerView, error)
	TestProvider(ctx context.Context, id string) (providerTest, error)
	DeleteProvider(ctx context.Context, id string) error

	ListSessions(ctx context.Context, f sessionFilter) ([]sessionView, error)
	GetSession(ctx context.Context, id string) (sessionView, error)
	// CreateSession 创建 CLI 会话（origin=cli，与桌面会话的路由键互不干扰）。
	CreateSession(ctx context.Context, agentID, title string) (sessionView, error)
	ArchiveSession(ctx context.Context, id string) (sessionView, error)
	DeleteSession(ctx context.Context, id string) error
	SendMessage(ctx context.Context, sessionID, text string) (model.Run, error)
	CancelRun(ctx context.Context, runID string) (model.Run, error)
	GetRun(ctx context.Context, runID string) (model.Run, error)
	// Events 订阅事件流（会话过滤为空表示全部）；通道在 ctx 结束或流无法恢复时关闭。
	// 实现负责断线续传；回放缓冲已丢失时投递一条 Type 为 "gap" 的事件。
	Events(ctx context.Context, sessionID string) (<-chan streamEvent, error)

	ListApprovals(ctx context.Context, status string) ([]model.ApprovalRequest, error)
	DecideApproval(ctx context.Context, id string, d model.Decision) (model.ApprovalRequest, error)

	ListJobs(ctx context.Context, agentID string) ([]model.Job, error)
	CreateJob(ctx context.Context, in jobInput) (model.Job, error)
	DeleteJob(ctx context.Context, id string) error
	RunJob(ctx context.Context, id string) error

	ListExtensions(ctx context.Context, kind string) ([]model.Extension, error)
	InstallExtension(ctx context.Context, path string) (model.Extension, error)
	RegisterMCP(ctx context.Context, name string, cfg model.MCPServerConfig) (model.Extension, error)
	SetExtensionEnabled(ctx context.Context, id string, enabled bool) (model.Extension, error)
	DeleteExtension(ctx context.Context, id string) error
	ExtensionStatus(ctx context.Context, id string) (extensionStatus, error)
	AcceptExtensionTools(ctx context.Context, id string) (extensionStatus, error)

	ListChannels(ctx context.Context, agentID string) ([]channelView, error)
	CreateChannel(ctx context.Context, in channelInput) (channelView, error)
	DeleteChannel(ctx context.Context, id string) error
	ReloadChannel(ctx context.Context, id string) (channelView, error)

	GetSettings(ctx context.Context) (map[string]any, error)
	UpdateSettings(ctx context.Context, patch map[string]any) (map[string]any, error)
}

// versionInfo 对应 OpenAPI VersionInfo。
type versionInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Date      string `json:"date"`
	StartedAt string `json:"started_at"`
	Worker    struct {
		Status  string `json:"status"`
		Version string `json:"version,omitempty"`
		Python  string `json:"python,omitempty"`
	} `json:"worker"`
}

// agentInput 对应 OpenAPI AgentInput（创建时用到的字段）。
type agentInput struct {
	Name         string              `json:"name"`
	Description  string              `json:"description,omitempty"`
	SystemPrompt string              `json:"system_prompt,omitempty"`
	Model        model.ModelRef      `json:"model"`
	Autonomy     model.AutonomyLevel `json:"autonomy,omitempty"`
	Template     string              `json:"template,omitempty"`
}

// providerView 对应 OpenAPI Provider（没有密钥，只有 has_secret）。
type providerView struct {
	ID           string             `json:"id"`
	Name         string             `json:"name"`
	Kind         model.ProviderKind `json:"kind"`
	BaseURL      string             `json:"base_url,omitempty"`
	HasSecret    bool               `json:"has_secret"`
	DefaultModel string             `json:"default_model,omitempty"`
	Models       []string           `json:"models,omitempty"`
	Enabled      bool               `json:"enabled"`
	CreatedAt    time.Time          `json:"created_at"`
	UpdatedAt    time.Time          `json:"updated_at"`
}

// providerInput 对应 OpenAPI ProviderInput。APIKey 只写，不进日志与输出。
type providerInput struct {
	Name         string             `json:"name"`
	Kind         model.ProviderKind `json:"kind"`
	BaseURL      string             `json:"base_url,omitempty"`
	APIKey       string             `json:"api_key,omitempty"`
	DefaultModel string             `json:"default_model,omitempty"`
}

// providerTest 对应 /providers/{id}/test 的响应。
type providerTest struct {
	OK        bool     `json:"ok"`
	Models    []string `json:"models,omitempty"`
	LatencyMS int64    `json:"latency_ms"`
	Error     string   `json:"error,omitempty"`
}

// sessionFilter 是会话列表的查询参数。
type sessionFilter struct {
	AgentID string
	Status  string
	Limit   int
}

// sessionView 对应 OpenAPI Session（比 model.Session 多 active_run_id）。
type sessionView struct {
	model.Session
	ActiveRunID string `json:"active_run_id,omitempty"`
}

// jobInput 对应 OpenAPI JobInput。
type jobInput struct {
	AgentID      string             `json:"agent_id"`
	Name         string             `json:"name"`
	Schedule     string             `json:"schedule"`
	Timezone     string             `json:"timezone,omitempty"`
	Kind         model.JobKind      `json:"kind"`
	Input        string             `json:"input"`
	SessionKey   string             `json:"session_key,omitempty"`
	MissedPolicy model.MissedPolicy `json:"missed_policy,omitempty"`
	Enabled      *bool              `json:"enabled,omitempty"`
}

// extensionStatus 对应 OpenAPI ExtensionStatus（MCP 连接与工具声明是否变化）。
type extensionStatus struct {
	ID           string `json:"id"`
	Connected    bool   `json:"connected"`
	ToolCount    int    `json:"tool_count"`
	ToolsChanged bool   `json:"tools_changed"`
	LastError    string `json:"last_error,omitempty"`
}

// channelView 对应 OpenAPI Channel（含运行状态，不含凭据）。
type channelView struct {
	model.Channel
	HasSecret bool   `json:"has_secret"`
	Status    string `json:"status,omitempty"`
}

// channelInput 对应 OpenAPI ChannelInput。Secret 只写。
type channelInput struct {
	AgentID string            `json:"agent_id"`
	Kind    model.ChannelKind `json:"kind"`
	Name    string            `json:"name"`
	Secret  string            `json:"secret,omitempty"`
	Config  json.RawMessage   `json:"config,omitempty"`
	Enabled *bool             `json:"enabled,omitempty"`
}

// streamEvent 是事件流中的一条事件；Payload 保留原始 JSON，由使用方按 Type 解码。
type streamEvent struct {
	Seq       uint64          `json:"seq"`
	Type      string          `json:"type"`
	At        time.Time       `json:"at"`
	AgentID   string          `json:"agent_id,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	RunID     string          `json:"run_id,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// runEvent 解码运行类事件的载荷；载荷不是 RunEvent 时返回 false。
func (e streamEvent) runEvent() (model.RunEvent, bool) {
	var ev model.RunEvent
	if len(e.Payload) == 0 || json.Unmarshal(e.Payload, &ev) != nil {
		return ev, false
	}
	return ev, true
}
