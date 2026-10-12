package model

import (
	"encoding/json"
	"time"
)

// Origin 标记会话从哪个入口发起。它只是会话的一个属性，不改变会话的处理方式。
type Origin string

const (
	OriginDesktop Origin = "desktop"
	OriginCLI     Origin = "cli"
	OriginChannel Origin = "channel"
	OriginJob     Origin = "job"
	OriginACP     Origin = "acp"
)

// SessionStatus 是会话的生命周期状态。
type SessionStatus string

const (
	SessionActive   SessionStatus = "active"
	SessionArchived SessionStatus = "archived"
)

// Session 是一条持续的对话线程。
type Session struct {
	ID      string `json:"id"`
	AgentID string `json:"agent_id"`
	// ProjectID 是同一智能体下的可选组织归属，不改变 Key 或文件访问范围。
	ProjectID string `json:"project_id,omitempty"`
	Title     string `json:"title,omitempty"`
	Origin    Origin `json:"origin"`
	// Key 是路由键，格式为 "<agent_id>:<origin>:<peer>"，用于把 IM 消息或定时任务路由到同一会话。
	// 同一时刻最多只有一个 active 会话拥有某个 Key。
	Key           string        `json:"key"`
	Status        SessionStatus `json:"status"`
	CreatedAt     time.Time     `json:"created_at"`
	UpdatedAt     time.Time     `json:"updated_at"`
	LastMessageAt *time.Time    `json:"last_message_at,omitempty"`
}

// Role 是消息的发出方。
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// PartType 是消息片段的类型。
type PartType string

const (
	PartText       PartType = "text"
	PartToolCall   PartType = "tool_call"
	PartToolResult PartType = "tool_result"
	PartAttachment PartType = "attachment"
)

// Part 是消息的一个片段；一条消息由若干片段按顺序组成。
type Part struct {
	Type       PartType    `json:"type"`
	Text       string      `json:"text,omitempty"`
	ToolCall   *ToolCall   `json:"tool_call,omitempty"`
	ToolResult *ToolResult `json:"tool_result,omitempty"`
	Attachment *Attachment `json:"attachment,omitempty"`
}

// Attachment 指向工作区 inbound/ 目录下的一个文件。
type Attachment struct {
	Name string `json:"name"`
	// Path 是工作区相对路径。
	Path string `json:"path"`
	MIME string `json:"mime,omitempty"`
	Size int64  `json:"size"`
}

// Message 是会话中的一条消息。
type Message struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	// Seq 是会话内单调递增的序号，由仓储在插入时分配。
	Seq  int64 `json:"seq"`
	Role Role  `json:"role"`
	// RunID 关联产生这条消息的运行；用户消息为触发的运行，助手与工具消息为生成它的运行。
	RunID string `json:"run_id,omitempty"`
	Parts []Part `json:"parts"`
	// Meta 保存不参与推理的附加信息（如 IM 平台消息 ID），结构不固定。
	Meta      json.RawMessage `json:"meta,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

// Text 把所有文本片段拼接起来，便于展示与日志。
func (m Message) Text() string {
	var out string
	for _, p := range m.Parts {
		if p.Type == PartText {
			out += p.Text
		}
	}
	return out
}

// TextMessage 构造一条只含文本的消息（ID、Seq、时间由仓储填充）。
func TextMessage(sessionID string, role Role, text string) Message {
	return Message{SessionID: sessionID, Role: role, Parts: []Part{{Type: PartText, Text: text}}}
}
