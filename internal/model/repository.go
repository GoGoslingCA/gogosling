package model

import (
	"context"
	"time"
)

// 本文件定义持久化仓储接口。store 包提供实现；领域包只依赖这些接口，测试时可用内存实现替代。
//
// 通用约定：
//   - 找不到记录时返回 CodeNotFound 的 *Error；唯一键冲突返回 CodeConflict；
//     引用不存在的父记录（外键）返回 CodeConflict；违反 NOT NULL / CHECK 返回 CodeInvalid。
//   - Create 由调用方填好 ID 与时间戳；仓储不生成 ID。
//     例外：Message 的 ID、Seq、CreatedAt 由 AppendMessage 填充（空值时），使 TextMessage 可直接使用。
//   - 列表方法按 CreatedAt 升序返回；分页参数见 Page。
//   - 带过滤参数的列表方法（ListJobs、ListChannels、ListExtensions、ListApprovals）
//     在参数为零值时返回全部。

// Page 是通用分页参数。Limit 为 0 时使用实现的默认值（建议 100）。
type Page struct {
	Limit  int
	Offset int
}

// AgentRepo 持久化智能体。
type AgentRepo interface {
	CreateAgent(ctx context.Context, a *Agent) error
	GetAgent(ctx context.Context, id string) (*Agent, error)
	ListAgents(ctx context.Context) ([]*Agent, error)
	UpdateAgent(ctx context.Context, a *Agent) error
	DeleteAgent(ctx context.Context, id string) error
}

// ProviderRepo 持久化模型供应商。
type ProviderRepo interface {
	CreateProvider(ctx context.Context, p *Provider) error
	GetProvider(ctx context.Context, id string) (*Provider, error)
	ListProviders(ctx context.Context) ([]*Provider, error)
	UpdateProvider(ctx context.Context, p *Provider) error
	DeleteProvider(ctx context.Context, id string) error
}

// SessionFilter 是会话列表的过滤条件；零值表示不过滤。
type SessionFilter struct {
	AgentID   string
	ProjectID string
	Status    SessionStatus
	Origin    Origin
}

// ProjectRepo 持久化项目与材料引用；更新版本由仓储原子递增。
type ProjectRepo interface {
	CreateProject(context.Context, *Project) error
	GetProject(context.Context, string) (*Project, error)
	ListProjects(context.Context, ProjectFilter, Page) ([]*Project, error)
	UpdateProject(context.Context, *Project, int64) error
	SetSessionProject(context.Context, string, string, string) error
	CreateProjectMaterial(context.Context, *ProjectMaterial) error
	GetProjectMaterial(context.Context, string) (*ProjectMaterial, error)
	ListProjectMaterials(context.Context, string, Page) ([]*ProjectMaterial, error)
	DeleteProjectMaterial(context.Context, string) error
}

// DraftRepo 的读取与删除显式传 ownerID；更新还必须匹配预期版本。
type DraftRepo interface {
	CreateDraft(context.Context, *Draft) error
	GetDraft(ctx context.Context, id, ownerID string) (*Draft, error)
	ListDrafts(context.Context, DraftFilter, Page) ([]*Draft, error)
	UpdateDraft(context.Context, *Draft, int64) error
	DeleteDraft(ctx context.Context, id, ownerID string, expectedVersion int64) error
}

// ArtifactRepo 只追加不可变版本；追加与最新指针更新在同一事务中完成。
type ArtifactRepo interface {
	CreateArtifact(context.Context, *Artifact) error
	GetArtifact(context.Context, string) (*Artifact, error)
	ListArtifacts(context.Context, ArtifactFilter, Page) ([]*Artifact, error)
	AppendArtifactRevision(context.Context, *ArtifactRevision, string) error
	GetArtifactRevision(ctx context.Context, artifactID, revisionID string) (*ArtifactRevision, error)
	ListArtifactRevisions(context.Context, string, Page) ([]*ArtifactRevision, error)
}

// WorkbenchStore 是工作台独立仓储集合，不扩大既有 Store 消费者的依赖。
type WorkbenchStore interface {
	ProjectRepo
	DraftRepo
	ArtifactRepo
	AgentRepo
	SessionRepo
	MessageRepo
	AuditRepo
	WorkbenchRequestRepo
	WorkbenchTx(context.Context, func(WorkbenchStore) error) error
}

// WorkbenchRequestRepo 在资源创建的同一事务中保证主体、端点和请求键唯一。
type WorkbenchRequestRepo interface {
	GetWorkbenchRequest(ctx context.Context, ownerID, scope, key string) (*WorkbenchRequest, error)
	CreateWorkbenchRequest(context.Context, *WorkbenchRequest) error
}

// SessionRepo 持久化会话。
type SessionRepo interface {
	CreateSession(ctx context.Context, s *Session) error
	GetSession(ctx context.Context, id string) (*Session, error)
	// GetActiveSessionByKey 返回拥有该路由键的 active 会话；没有时返回 CodeNotFound。
	GetActiveSessionByKey(ctx context.Context, key string) (*Session, error)
	ListSessions(ctx context.Context, f SessionFilter, p Page) ([]*Session, error)
	UpdateSession(ctx context.Context, s *Session) error
	// DeleteSession 级联删除会话下的消息与运行记录。
	DeleteSession(ctx context.Context, id string) error
}

// MessageRepo 持久化消息。
type MessageRepo interface {
	// AppendMessage 分配会话内递增的 Seq 并写入；同时更新会话的 LastMessageAt。
	AppendMessage(ctx context.Context, m *Message) error
	GetMessage(ctx context.Context, id string) (*Message, error)
	// ListMessages 返回 Seq 大于 afterSeq 的消息，按 Seq 升序。
	ListMessages(ctx context.Context, sessionID string, afterSeq int64, limit int) ([]*Message, error)
}

// RunRepo 持久化运行记录。
type RunRepo interface {
	CreateRun(ctx context.Context, r *Run) error
	GetRun(ctx context.Context, id string) (*Run, error)
	UpdateRun(ctx context.Context, r *Run) error
	ListRuns(ctx context.Context, sessionID string, p Page) ([]*Run, error)
	// ListActiveRuns 返回所有非终态的运行，用于启动时恢复或标记为失败。
	ListActiveRuns(ctx context.Context) ([]*Run, error)
}

// OperationRepo 独立持久化用户文件操作，不扩展既有模型运行仓储集合。
type OperationRepo interface {
	CreateOperation(context.Context, *Operation) error
	GetOperation(context.Context, string) (*Operation, error)
	// UpdateOperation 仅更新状态与时间，且必须匹配 expectedStatus；归属不可修改。
	UpdateOperation(context.Context, *Operation, OperationStatus) error
	ListOperations(context.Context, OperationFilter, Page) ([]*Operation, error)
}

// ApprovalRepo 持久化审批请求。
type ApprovalRepo interface {
	CreateApproval(ctx context.Context, a *ApprovalRequest) error
	GetApproval(ctx context.Context, id string) (*ApprovalRequest, error)
	UpdateApproval(ctx context.Context, a *ApprovalRequest) error
	ListApprovals(ctx context.Context, status ApprovalStatus, p Page) ([]*ApprovalRequest, error)
}

// JobRepo 持久化定时任务。
type JobRepo interface {
	CreateJob(ctx context.Context, j *Job) error
	GetJob(ctx context.Context, id string) (*Job, error)
	ListJobs(ctx context.Context, agentID string) ([]*Job, error)
	UpdateJob(ctx context.Context, j *Job) error
	DeleteJob(ctx context.Context, id string) error
}

// ExtensionRepo 持久化已安装的扩展。
type ExtensionRepo interface {
	CreateExtension(ctx context.Context, e *Extension) error
	GetExtension(ctx context.Context, id string) (*Extension, error)
	ListExtensions(ctx context.Context, kind ExtensionKind) ([]*Extension, error)
	UpdateExtension(ctx context.Context, e *Extension) error
	DeleteExtension(ctx context.Context, id string) error
}

// AuditFilter 是审计查询条件；零值表示不过滤。
type AuditFilter struct {
	AgentID   string
	SessionID string
	RunID     string
	Actor     string
	Action    string
	Since     time.Time
}

// AuditRepo 追加与查询审计记录。
type AuditRepo interface {
	AppendAudit(ctx context.Context, e *AuditEntry) error
	// ListAudit 按 At 降序返回。
	ListAudit(ctx context.Context, f AuditFilter, p Page) ([]*AuditEntry, error)
}

// SettingsRepo 是简单的键值配置（时区、语言等），值为 JSON 文本。
type SettingsRepo interface {
	GetSetting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value string) error
}

// Store 聚合全部仓储，并提供事务与关闭。
type Store interface {
	AgentRepo
	ProviderRepo
	SessionRepo
	MessageRepo
	RunRepo
	ApprovalRepo
	JobRepo
	ExtensionRepo
	ChannelRepo
	AuditRepo
	SettingsRepo

	// Tx 在一个事务内执行 fn；fn 返回错误则回滚。fn 收到的 Store 只在事务内有效。
	Tx(ctx context.Context, fn func(tx Store) error) error
	Close() error
}
