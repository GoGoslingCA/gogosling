package model

import (
	"context"
	"time"
)

// ChangeKind 仅允许单文件创建、替换，以及新建文件的受控撤销删除。
type ChangeKind string

const (
	ChangeCreate  ChangeKind = "create"
	ChangeReplace ChangeKind = "replace"
	ChangeDelete  ChangeKind = "delete"
)

// ChangeStatus 是可恢复的文件操作状态，不属于模型 Run。
type ChangeStatus string

const (
	ChangePrepared         ChangeStatus = "prepared"
	ChangeAwaitingApproval ChangeStatus = "awaiting_approval"
	ChangeApplying         ChangeStatus = "applying"
	ChangeApplied          ChangeStatus = "applied"
	ChangeDenied           ChangeStatus = "denied"
	ChangeExpired          ChangeStatus = "expired"
	ChangeConflict         ChangeStatus = "conflict"
	ChangeFailed           ChangeStatus = "failed"
	ChangeCancelled        ChangeStatus = "cancelled"
	ChangeRecoveryRequired ChangeStatus = "recovery_required"
)

// ChangeSnapshot 在内核保存文件前像；正文及文件系统身份不进入元数据响应。
type ChangeSnapshot struct {
	Exists       bool   `json:"exists"`
	SHA256       string `json:"sha256"`
	Identity     string `json:"identity"`
	RootIdentity string `json:"root_identity"`
	Content      string `json:"content"`
	Size         int64  `json:"size"`
}

// ChangeSet 绑定操作者、不可变成果版本、目标与预览；应用不能更换正文。
type ChangeSet struct {
	ID             string         `json:"id"`
	OwnerID        string         `json:"owner_id"`
	AgentID        string         `json:"agent_id"`
	SessionID      string         `json:"session_id"`
	ArtifactID     string         `json:"artifact_id"`
	RevisionID     string         `json:"revision_id"`
	TargetPath     string         `json:"target_path"`
	Kind           ChangeKind     `json:"operation"`
	Status         ChangeStatus   `json:"status"`
	Version        int64          `json:"version"`
	PreviewDigest  string         `json:"preview_digest"`
	BaseExists     bool           `json:"base_exists"`
	BaseSHA256     string         `json:"base_sha256"`
	ProposedSHA256 string         `json:"proposed_sha256"`
	ProposedSize   int64          `json:"proposed_size"`
	OperationID    string         `json:"operation_id,omitempty"`
	UndoOf         string         `json:"undo_of,omitempty"`
	Reason         string         `json:"reason,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	ExpiresAt      time.Time      `json:"expires_at"`
	AppliedAt      *time.Time     `json:"applied_at,omitempty"`
	Base           ChangeSnapshot `json:"-"`
	Proposed       string         `json:"-"`
	Applied        ChangeSnapshot `json:"-"`
	// PendingResult 是已持久核验的文件结果，清理及最终落账完成前仍保持 applying。
	PendingResult ChangeStatus `json:"-"`
}

// ChangeInput 从指定成果版本生成预览，不接受任意正文或绝对路径。
type ChangeInput struct {
	ArtifactID string     `json:"artifact_id"`
	RevisionID string     `json:"revision_id"`
	TargetPath string     `json:"target_path"`
	Operation  ChangeKind `json:"operation"`
}

// ChangeApplyInput 只携带已预览版本与摘要。
type ChangeApplyInput struct {
	ExpectedVersion int64  `json:"expected_version"`
	PreviewDigest   string `json:"preview_digest"`
}

// ChangeVersionInput 用于取消、撤销预览或恢复核对。
type ChangeVersionInput struct {
	ExpectedVersion int64 `json:"expected_version"`
}

// ChangeDiff 的正文仅经鉴权专门端点传递，不进入事件或审计。
type ChangeDiff struct {
	ChangeSet *ChangeSet `json:"change"`
	Before    string     `json:"before"`
	After     string     `json:"after"`
}

// ChangeSetRepo 持久化前像与执行意图，更新始终带预期版本。
type ChangeSetRepo interface {
	CreateChangeSet(context.Context, *ChangeSet) error
	GetChangeSet(context.Context, string) (*ChangeSet, error)
	UpdateChangeSet(context.Context, *ChangeSet, int64) error
	ListChangeSets(context.Context, string, string, Page) ([]*ChangeSet, error)
}

// ChangeStore 是文件操作的独立仓储事务，不扩大原 Store 或 WorkbenchStore。
type ChangeStore interface {
	WorkbenchStore
	OperationRepo
	ChangeSetRepo
	ChangeTx(context.Context, func(ChangeStore) error) error
}
