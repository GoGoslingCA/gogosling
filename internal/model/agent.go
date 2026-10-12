package model

import "time"

// AutonomyLevel 决定智能体在没有人工介入时可以走多远。
type AutonomyLevel string

const (
	// AutonomyAskAll 每次工具调用（含只读）都需要审批。
	AutonomyAskAll AutonomyLevel = "ask_all"
	// AutonomyAskRisky 只对高风险操作（执行命令、网络、发送消息、删除）审批，工作区内读写自动放行。
	AutonomyAskRisky AutonomyLevel = "ask_risky"
	// AutonomyAutoInSandbox 在有 OS 级隔离时自动执行命令与网络访问，只有越出沙箱边界的操作才审批；
	// 当前平台没有可用隔离（Linux / Windows 尚未实现）时，execute / network 退回 ask_risky 的行为。
	AutonomyAutoInSandbox AutonomyLevel = "auto_in_sandbox"
)

// DefaultAutonomy 是新建智能体的默认档位。
const DefaultAutonomy = AutonomyAskRisky

// ModelRef 指向某个供应商下的具体模型。
type ModelRef struct {
	ProviderID string `json:"provider_id"`
	Model      string `json:"model"`
}

// Agent 是一份可持久化的智能体配置；推理循环由工作进程按这份配置执行。
type Agent struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// SystemPrompt 是人格与行为约束；为空时使用内置默认提示。
	SystemPrompt string   `json:"system_prompt,omitempty"`
	Model        ModelRef `json:"model"`
	// Tools 是启用的内置工具名。输入侧：缺席 = 不修改 / 创建时用默认集，null = 重置为默认集，[] = 不启用任何内置工具；
	// 输出侧始终是数组（[] 与默认集不同），因此不带 omitempty。
	Tools []string `json:"tools"`
	// Extensions 是启用的扩展 ID（技能、MCP 服务、插件）；输出侧始终是数组。
	Extensions []string      `json:"extensions"`
	Autonomy   AutonomyLevel `json:"autonomy"`
	// WorkspaceDir 是工作区目录；相对路径相对于数据根目录下的 agents/，绝对路径原样使用。
	WorkspaceDir string `json:"workspace_dir"`
	// Template 记录创建时所用的模板标识，便于后续升级模板；可为空。
	Template  string    `json:"template,omitempty"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
