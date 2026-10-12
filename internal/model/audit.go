package model

import (
	"encoding/json"
	"time"
)

// AuditEntry 是一条审计记录：谁、在什么上下文里、对什么做了什么、结果如何。
//
// 审计日志只追加不修改。
type AuditEntry struct {
	ID string    `json:"id"`
	At time.Time `json:"at"`
	// Actor 是发起方的标识，例如本机用户的 Principal.ID、"job:<id>" 或 "agent:<id>"。
	Actor     string `json:"actor"`
	AgentID   string `json:"agent_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	RunID     string `json:"run_id,omitempty"`
	// Action 是动词，例如 "tool.call"、"approval.decide"、"agent.create"、"secret.set"。
	Action string `json:"action"`
	// Resource 是对象，例如工具名、文件路径、资源 ID。
	Resource string `json:"resource,omitempty"`
	// Outcome 是结果："allowed"、"denied"、"ok"、"failed"。
	Outcome string `json:"outcome"`
	// Details 是补充信息，写入前必须脱敏。
	Details json.RawMessage `json:"details,omitempty"`
}
