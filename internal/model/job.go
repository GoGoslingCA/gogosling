package model

import "time"

// JobKind 决定定时任务触发时做什么。
type JobKind string

const (
	// JobPushText 直接把固定文本推送到目标会话，不经过模型。
	JobPushText JobKind = "push_text"
	// JobRunAgent 以 Input 作为用户输入发起一次运行，再把回复推送到目标会话。
	JobRunAgent JobKind = "run_agent"
)

// MissedPolicy 决定设备休眠或离线导致错过触发时如何处理。
type MissedPolicy string

const (
	// MissedSkip 跳过错过的触发，等待下一次。
	MissedSkip MissedPolicy = "skip"
	// MissedRunOnce 恢复后补跑一次（无论错过多少次）。
	MissedRunOnce MissedPolicy = "run_once"
)

// JobRunStatus 是单次触发的执行结果。
type JobRunStatus string

const (
	JobRunOK      JobRunStatus = "ok"
	JobRunFailed  JobRunStatus = "failed"
	JobRunSkipped JobRunStatus = "skipped"
)

// Job 是一条定时任务。
type Job struct {
	ID      string `json:"id"`
	AgentID string `json:"agent_id"`
	Name    string `json:"name"`
	// Schedule 是标准五字段 cron 表达式（分 时 日 月 周），另支持 @hourly / @daily 等描述符。
	Schedule string `json:"schedule"`
	// Timezone 是 IANA 时区名；为空时使用全局默认时区。
	Timezone string  `json:"timezone,omitempty"`
	Kind     JobKind `json:"kind"`
	// Input 在 push_text 时是推送文本，在 run_agent 时是给智能体的输入。
	Input string `json:"input"`
	// SessionKey 是目标会话的路由键；为空时使用该智能体的默认桌面会话。
	SessionKey   string       `json:"session_key,omitempty"`
	MissedPolicy MissedPolicy `json:"missed_policy"`
	Enabled      bool         `json:"enabled"`
	NextRunAt    *time.Time   `json:"next_run_at,omitempty"`
	LastRunAt    *time.Time   `json:"last_run_at,omitempty"`
	LastStatus   JobRunStatus `json:"last_status,omitempty"`
	LastError    string       `json:"last_error,omitempty"`
	CreatedAt    time.Time    `json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
}
