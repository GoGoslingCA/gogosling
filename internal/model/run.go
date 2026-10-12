package model

import (
	"encoding/json"
	"time"
)

// RunStatus 是一次运行的状态机。
//
// 合法迁移：queued → running → (awaiting_approval ⇄ running) → completed | failed | cancelled。
// 终态之间不能再迁移。
type RunStatus string

const (
	RunQueued           RunStatus = "queued"
	RunRunning          RunStatus = "running"
	RunAwaitingApproval RunStatus = "awaiting_approval"
	RunCompleted        RunStatus = "completed"
	RunFailed           RunStatus = "failed"
	RunCancelled        RunStatus = "cancelled"
)

// Terminal 判断状态是否为终态。
func (s RunStatus) Terminal() bool {
	return s == RunCompleted || s == RunFailed || s == RunCancelled
}

// RunTrigger 记录是谁发起了这次运行。
type RunTrigger string

const (
	TriggerUser    RunTrigger = "user"
	TriggerJob     RunTrigger = "job"
	TriggerChannel RunTrigger = "channel"
	TriggerAgent   RunTrigger = "agent"
)

// Usage 是一次运行消耗的 token 数。
type Usage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	// CacheReadTokens 是命中提示缓存的输入 token 数，若供应商不报告则为 0。
	CacheReadTokens int64 `json:"cache_read_tokens,omitempty"`
	// CacheWriteTokens 是写入提示缓存的输入 token 数（如 Anthropic 的 cache_creation_input_tokens）。
	CacheWriteTokens int64 `json:"cache_write_tokens,omitempty"`
}

// Add 累加另一份用量。
func (u *Usage) Add(o Usage) {
	u.InputTokens += o.InputTokens
	u.OutputTokens += o.OutputTokens
	u.CacheReadTokens += o.CacheReadTokens
	u.CacheWriteTokens += o.CacheWriteTokens
}

// Run 是智能体对一条输入的一次完整处理：从收到输入到产出最终回复。
type Run struct {
	ID        string     `json:"id"`
	SessionID string     `json:"session_id"`
	AgentID   string     `json:"agent_id"`
	Status    RunStatus  `json:"status"`
	Trigger   RunTrigger `json:"trigger"`
	// InputMessageID 是触发运行的用户消息；OutputMessageID 是最终的助手消息（完成后才有）。
	InputMessageID  string `json:"input_message_id,omitempty"`
	OutputMessageID string `json:"output_message_id,omitempty"`
	// Error 在 failed 状态下保存面向用户的失败原因。
	Error     string     `json:"error,omitempty"`
	Usage     Usage      `json:"usage"`
	CreatedAt time.Time  `json:"created_at"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
}

// ToolSource 标记工具来自哪里，格式为 "builtin"、"mcp:<extension_id>" 或 "extension:<extension_id>"。
type ToolSource string

// ToolRisk 是工具的风险等级，权限规则按它决定是否需要审批。
type ToolRisk string

const (
	// RiskRead 只读取工作区内信息。
	RiskRead ToolRisk = "read"
	// RiskWrite 修改工作区内文件。
	RiskWrite ToolRisk = "write"
	// RiskExecute 执行命令或代码。
	RiskExecute ToolRisk = "execute"
	// RiskNetwork 访问网络或外部服务。
	RiskNetwork ToolRisk = "network"
	// RiskDestructive 删除数据或执行不可逆操作。
	RiskDestructive ToolRisk = "destructive"
)

// ToolSpec 是暴露给模型的工具声明。
type ToolSpec struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// InputSchema 是参数的 JSON Schema。
	InputSchema json.RawMessage `json:"input_schema"`
	Source      ToolSource      `json:"source"`
	Risk        ToolRisk        `json:"risk"`
}

// ToolCall 是模型发起的一次工具调用。
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// ToolResult 是工具调用的结果，会作为 tool 角色消息回填给模型。
type ToolResult struct {
	CallID  string `json:"call_id"`
	Content string `json:"content"`
	IsError bool   `json:"is_error,omitempty"`
	// Truncated 表示 Content 因超过大小上限被截断，界面可据此提示。
	Truncated bool `json:"truncated,omitempty"`
}

// RunEventType 是运行过程中产生的事件类型；事件按 Seq 排序，供界面流式展示与断线回放。
type RunEventType string

const (
	EventRunStarted RunEventType = "run.started"
	EventTextDelta  RunEventType = "text.delta"
	// EventReasoningDelta 是模型思考内容的增量（供应商支持时才有），Delta 字段携带文本；
	// 它不进入最终助手消息，只用于界面展示。
	EventReasoningDelta    RunEventType = "reasoning.delta"
	EventToolCallStarted   RunEventType = "tool.call.started"
	EventToolCallCompleted RunEventType = "tool.call.completed"
	EventApprovalRequested RunEventType = "approval.requested"
	EventApprovalResolved  RunEventType = "approval.resolved"
	EventMessageCreated    RunEventType = "message.created"
	EventUsage             RunEventType = "usage"
	EventRunCompleted      RunEventType = "run.completed"
	EventRunFailed         RunEventType = "run.failed"
	EventRunCancelled      RunEventType = "run.cancelled"
)

// RunEvent 是一次运行中的一个事件。只有与 Type 对应的字段会被填充。
type RunEvent struct {
	Type      RunEventType `json:"type"`
	RunID     string       `json:"run_id"`
	SessionID string       `json:"session_id"`
	AgentID   string       `json:"agent_id"`
	At        time.Time    `json:"at"`
	// Delta 用于 text.delta 与 reasoning.delta。
	Delta string `json:"delta,omitempty"`
	// ToolCall 用于 tool.call.started；ToolResult 用于 tool.call.completed。
	ToolCall   *ToolCall   `json:"tool_call,omitempty"`
	ToolResult *ToolResult `json:"tool_result,omitempty"`
	// Approval 用于 approval.requested / approval.resolved。
	Approval *ApprovalRequest `json:"approval,omitempty"`
	// Message 用于 message.created。
	Message *Message `json:"message,omitempty"`
	// Usage 用于 usage 与 run.completed。
	Usage *Usage `json:"usage,omitempty"`
	// Error 用于 run.failed。
	Error string `json:"error,omitempty"`
}
