package model

import (
	"encoding/json"
	"time"
)

// ApprovalStatus 是审批请求的状态。
type ApprovalStatus string

const (
	ApprovalPending  ApprovalStatus = "pending"
	ApprovalApproved ApprovalStatus = "approved"
	ApprovalDenied   ApprovalStatus = "denied"
	ApprovalExpired  ApprovalStatus = "expired"
)

// ApprovalScope 决定一次批准的有效范围。
type ApprovalScope string

const (
	// ScopeOnce 只对这一次调用有效。
	ScopeOnce ApprovalScope = "once"
	// ScopeSession 对本会话内同一工具的后续调用有效。
	ScopeSession ApprovalScope = "session"
	// ScopeAlways 永久记住，写入权限规则。
	ScopeAlways ApprovalScope = "always"
)

// ApprovalRequest 是等待用户决定的一次操作。
type ApprovalRequest struct {
	ID string `json:"id"`
	// RunID 和 OperationID 必须且只能填一个，分别引用真实模型运行或本机操作。
	RunID       string `json:"run_id,omitempty"`
	OperationID string `json:"operation_id,omitempty"`
	SessionID   string `json:"session_id"`
	AgentID     string `json:"agent_id"`
	// ToolCall 是待审批的调用；Risk 是其风险等级。
	ToolCall ToolCall `json:"tool_call"`
	Risk     ToolRisk `json:"risk"`
	// Summary 是面向用户的一句话描述，例如"在工作区外写入文件 /tmp/x"。
	Summary string `json:"summary"`
	// Details 是展示用的结构化细节（命令、路径、目标域名等）。
	Details    json.RawMessage `json:"details,omitempty"`
	Status     ApprovalStatus  `json:"status"`
	Scope      ApprovalScope   `json:"scope,omitempty"`
	DecidedBy  string          `json:"decided_by,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
	ExpiresAt  time.Time       `json:"expires_at"`
	ResolvedAt *time.Time      `json:"resolved_at,omitempty"`
}

// Decision 是用户对审批请求的回答。
type Decision struct {
	Approve bool          `json:"approve"`
	Scope   ApprovalScope `json:"scope"`
	// Reason 是可选的拒绝理由，会回填给模型。
	Reason string `json:"reason,omitempty"`
}
