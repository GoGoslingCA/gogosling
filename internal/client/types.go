package client

import (
	"encoding/json"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/provider"
	"github.com/GoGoslingCA/gogosling/internal/session"
)

// ── 响应视图：model 之外由服务端附加字段的类型 ─────────────────────────────────

// VersionInfo 对应 GET /api/v1/version。
type VersionInfo struct {
	Version   string       `json:"version"`
	Commit    string       `json:"commit"`
	Date      string       `json:"date"`
	StartedAt string       `json:"started_at"`
	Worker    WorkerStatus `json:"worker"`
}

// WorkerStatus 是工作进程状态：Status ∈ ready / starting / unavailable。
type WorkerStatus struct {
	Status  string `json:"status"`
	Version string `json:"version,omitempty"`
	Python  string `json:"python,omitempty"`
}

// SessionView 是会话加上当前在途运行的 ID（没有时为空）。它就是 session 包导出的视图类型：
// REST 响应与 session.* 事件的 payload 同形，可用 DecodePayload 直接解码事件载荷。
type SessionView = session.View

// ProviderView 是供应商的对外视图：HasSecret 代替密钥引用，永不包含密钥。
type ProviderView = provider.View

// ProviderTestResult 是连通性测试结果；探测失败表现为 OK=false 加 Error 文本，而不是 Go 错误。
type ProviderTestResult = provider.TestResult

// ChannelView 是 IM 通道的对外视图：不含凭据与凭据引用，附加 has_secret 与适配器运行状态。
type ChannelView struct {
	ID        string            `json:"id"`
	AgentID   string            `json:"agent_id"`
	Kind      model.ChannelKind `json:"kind"`
	Name      string            `json:"name"`
	HasSecret bool              `json:"has_secret"`
	Config    json.RawMessage   `json:"config,omitempty"`
	Enabled   bool              `json:"enabled"`
	// Status ∈ stopped / starting / running / failed。
	Status    string    `json:"status,omitempty"`
	LastError string    `json:"last_error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ExtensionStatus 是扩展的运行时状态（MCP 连接、工具声明是否变化）。
type ExtensionStatus struct {
	ID           string `json:"id"`
	Connected    bool   `json:"connected"`
	ToolCount    int    `json:"tool_count"`
	ToolsChanged bool   `json:"tools_changed"`
	LastError    string `json:"last_error,omitempty"`
}

// Settings 是全局设置。Timezone / Locale 只反映用户显式保存的值，未设置时为空；
// EffectiveLocale / EffectiveTimezone 是守护进程实际生效的值（只读，设置 > 系统环境 > 默认）。
type Settings struct {
	Timezone          string   `json:"timezone,omitempty"`
	Locale            string   `json:"locale,omitempty"`
	EffectiveLocale   string   `json:"effective_locale,omitempty"`
	EffectiveTimezone string   `json:"effective_timezone,omitempty"`
	AllowedOrigins    []string `json:"allowed_origins"`
}

// ── 请求输入 ──────────────────────────────────────────────────────────────────

// AgentCreate 是创建智能体的输入；Name 与 Model 必填。
// Tools 为 nil 时使用模板 / 默认工具集，空切片表示不启用任何内置工具。
type AgentCreate struct {
	Name         string              `json:"name"`
	Description  string              `json:"description,omitempty"`
	SystemPrompt string              `json:"system_prompt,omitempty"`
	Model        model.ModelRef      `json:"model"`
	Tools        []string            `json:"tools"`
	Extensions   []string            `json:"extensions,omitempty"`
	Autonomy     model.AutonomyLevel `json:"autonomy,omitempty"`
	WorkspaceDir string              `json:"workspace_dir,omitempty"`
	Template     string              `json:"template,omitempty"`
	Enabled      *bool               `json:"enabled,omitempty"`
}

// AgentPatch 是部分更新：nil 字段不发送（不修改）。
// Tools 指向 nil 切片时发送 null（重置为默认工具集），指向空切片时发送 []（不启用内置工具）。
type AgentPatch struct {
	Name         *string              `json:"name,omitempty"`
	Description  *string              `json:"description,omitempty"`
	SystemPrompt *string              `json:"system_prompt,omitempty"`
	Model        *model.ModelRef      `json:"model,omitempty"`
	Tools        *[]string            `json:"tools,omitempty"`
	Extensions   *[]string            `json:"extensions,omitempty"`
	Autonomy     *model.AutonomyLevel `json:"autonomy,omitempty"`
	Template     *string              `json:"template,omitempty"`
	Enabled      *bool                `json:"enabled,omitempty"`
}

// ProviderCreate 是创建供应商的输入；APIKey 只写入守护进程的 secret 存储。
type ProviderCreate struct {
	Name         string             `json:"name"`
	Kind         model.ProviderKind `json:"kind"`
	BaseURL      string             `json:"base_url,omitempty"`
	APIKey       string             `json:"api_key,omitempty"`
	DefaultModel string             `json:"default_model,omitempty"`
	Enabled      *bool              `json:"enabled,omitempty"`
}

// ProviderPatch 是部分更新。APIKey：nil 不动，指向 "" 清除，非空轮换。
type ProviderPatch struct {
	Name         *string             `json:"name,omitempty"`
	Kind         *model.ProviderKind `json:"kind,omitempty"`
	BaseURL      *string             `json:"base_url,omitempty"`
	APIKey       *string             `json:"api_key,omitempty"`
	DefaultModel *string             `json:"default_model,omitempty"`
	Enabled      *bool               `json:"enabled,omitempty"`
}

// SessionQuery 是会话列表的过滤与分页；零值表示不过滤、默认分页。
type SessionQuery struct {
	AgentID string
	Status  model.SessionStatus
	Limit   int
	Offset  int
}

// SessionCreate 是创建会话的输入。Origin 是发起入口（model.OriginDesktop 或 model.OriginCLI），
// 为空时服务端按 desktop 处理；它决定会话的路由键（"<agent_id>:<origin>:local"）与界面上的来源标记。
type SessionCreate struct {
	AgentID string       `json:"agent_id"`
	Title   string       `json:"title,omitempty"`
	Origin  model.Origin `json:"origin,omitempty"`
}

// SessionPatch 重命名或归档会话。
type SessionPatch struct {
	Title  *string              `json:"title,omitempty"`
	Status *model.SessionStatus `json:"status,omitempty"`
}

// JobCreate 是创建定时任务的输入；Enabled 为 nil 时服务端缺省启用，MissedPolicy 为空缺省 skip。
type JobCreate struct {
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

// JobPatch 是定时任务的部分更新（agent_id 不可修改）。
type JobPatch struct {
	Name         *string             `json:"name,omitempty"`
	Schedule     *string             `json:"schedule,omitempty"`
	Timezone     *string             `json:"timezone,omitempty"`
	Kind         *model.JobKind      `json:"kind,omitempty"`
	Input        *string             `json:"input,omitempty"`
	SessionKey   *string             `json:"session_key,omitempty"`
	MissedPolicy *model.MissedPolicy `json:"missed_policy,omitempty"`
	Enabled      *bool               `json:"enabled,omitempty"`
}

// ChannelCreate 是创建通道的输入；Secret 只写入守护进程的 secret 存储，Config 必须是 JSON 对象。
type ChannelCreate struct {
	AgentID string            `json:"agent_id"`
	Kind    model.ChannelKind `json:"kind"`
	Name    string            `json:"name"`
	Secret  string            `json:"secret,omitempty"`
	Config  json.RawMessage   `json:"config,omitempty"`
	Enabled *bool             `json:"enabled,omitempty"`
}

// ChannelPatch 是通道的部分更新。Secret：nil 不动，指向 "" 清除，非空轮换；Config 为 nil 不修改。
type ChannelPatch struct {
	Name    *string         `json:"name,omitempty"`
	Secret  *string         `json:"secret,omitempty"`
	Config  json.RawMessage `json:"config,omitempty"`
	Enabled *bool           `json:"enabled,omitempty"`
}

// SettingsPatch 是设置的部分更新；AllowedOrigins 指向空切片表示清空。
type SettingsPatch struct {
	Timezone       *string   `json:"timezone,omitempty"`
	Locale         *string   `json:"locale,omitempty"`
	AllowedOrigins *[]string `json:"allowed_origins,omitempty"`
}

// AuditQuery 是审计查询条件；零值表示不过滤。
type AuditQuery struct {
	AgentID   string
	SessionID string
	RunID     string
	Actor     string
	Action    string
	Since     time.Time
	Limit     int
	Offset    int
}

// Ptr 返回 v 的指针，便于填写 Patch 类型的字段。
func Ptr[T any](v T) *T { return &v }
