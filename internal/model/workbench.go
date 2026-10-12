package model

import "time"

// WorkbenchTextLimit 是草稿和成果单份 UTF-8 正文的最大字节数。
const WorkbenchTextLimit = 512 << 10

// ProjectStatus 是项目的组织状态；归档不改变会话运行状态。
type ProjectStatus string

const (
	ProjectActive   ProjectStatus = "active"
	ProjectArchived ProjectStatus = "archived"
)

// Project 组织同一智能体下的会话和材料，不创建新的文件访问边界。
type Project struct {
	ID          string        `json:"id"`
	AgentID     string        `json:"agent_id"`
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	Status      ProjectStatus `json:"status"`
	Version     int64         `json:"version"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
}

// ProjectFilter 是项目列表过滤条件。
type ProjectFilter struct {
	AgentID string
	Status  ProjectStatus
}

// ProjectInput 是创建项目的输入；版本和时间由领域层填充。
type ProjectInput struct {
	AgentID     string `json:"agent_id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// ProjectPatch 通过预期版本修改项目，可省略不变字段。
type ProjectPatch struct {
	ExpectedVersion int64          `json:"expected_version"`
	Name            *string        `json:"name,omitempty"`
	Description     *string        `json:"description,omitempty"`
	Status          *ProjectStatus `json:"status,omitempty"`
}

// ProjectMaterial 是工作区入站文件的项目索引与采集时的内容摘要。
type ProjectMaterial struct {
	ID         string     `json:"id"`
	ProjectID  string     `json:"project_id"`
	AgentID    string     `json:"agent_id"`
	Name       string     `json:"name"`
	MIME       string     `json:"mime,omitempty"`
	Size       int64      `json:"size"`
	SHA256     string     `json:"sha256"`
	Attachment Attachment `json:"attachment"`
	CreatedAt  time.Time  `json:"created_at"`
}

// DraftKind 区分输入草稿和成果编辑草稿。
type DraftKind string

const (
	DraftComposer DraftKind = "composer"
	DraftArtifact DraftKind = "artifact"
)

// Draft 是归属于鉴权主体的可恢复草稿；所有更新必须校验 Version。
type Draft struct {
	ID             string       `json:"id"`
	OwnerID        string       `json:"owner_id"`
	Kind           DraftKind    `json:"kind"`
	AgentID        string       `json:"agent_id"`
	ProjectID      string       `json:"project_id,omitempty"`
	SessionID      string       `json:"session_id,omitempty"`
	ArtifactID     string       `json:"artifact_id,omitempty"`
	BaseRevisionID string       `json:"base_revision_id,omitempty"`
	Text           string       `json:"text"`
	Filename       string       `json:"filename,omitempty"`
	MaterialIDs    []string     `json:"material_ids,omitempty"`
	Attachments    []Attachment `json:"attachments,omitempty"`
	Version        int64        `json:"version"`
	CreatedAt      time.Time    `json:"created_at"`
	UpdatedAt      time.Time    `json:"updated_at"`
}

// DraftFilter 的 OwnerID 必填，不能通过省略主体读取全部草稿。
type DraftFilter struct {
	OwnerID    string
	Kind       DraftKind
	AgentID    string
	ProjectID  string
	SessionID  string
	ArtifactID string
}

// DraftInput 是创建或完整更新草稿的内容；OwnerID 只能从鉴权主体取得。
type DraftInput struct {
	Kind            DraftKind    `json:"kind"`
	AgentID         string       `json:"agent_id"`
	ProjectID       string       `json:"project_id,omitempty"`
	SessionID       string       `json:"session_id,omitempty"`
	ArtifactID      string       `json:"artifact_id,omitempty"`
	BaseRevisionID  string       `json:"base_revision_id,omitempty"`
	Text            string       `json:"text"`
	Filename        string       `json:"filename,omitempty"`
	MaterialIDs     []string     `json:"material_ids,omitempty"`
	Attachments     []Attachment `json:"attachments,omitempty"`
	ExpectedVersion int64        `json:"expected_version,omitempty"`
}

// ArtifactKind 是文本成果的展示方式，不代表可执行能力。
type ArtifactKind string

const (
	ArtifactDocument ArtifactKind = "document"
	ArtifactCode     ArtifactKind = "code"
	ArtifactText     ArtifactKind = "text"
)

// ArtifactSource 由服务端从真实助手消息取得，Fragment 仅用于界面定位。
type ArtifactSource struct {
	MessageID  string `json:"message_id"`
	RunID      string `json:"run_id,omitempty"`
	MessageSeq int64  `json:"message_seq"`
	SHA256     string `json:"source_sha256"`
	Partial    bool   `json:"partial"`
	Fragment   string `json:"fragment,omitempty"`
}

// Artifact 是持久化成果索引；ProjectID 由会话当前归属派生。
type Artifact struct {
	ID               string         `json:"id"`
	AgentID          string         `json:"agent_id"`
	SessionID        string         `json:"session_id"`
	ProjectID        string         `json:"project_id,omitempty"`
	Title            string         `json:"title"`
	Kind             ArtifactKind   `json:"kind"`
	Filename         string         `json:"filename"`
	MediaType        string         `json:"media_type"`
	Source           ArtifactSource `json:"source"`
	LatestRevisionID string         `json:"latest_revision_id,omitempty"`
	Version          int64          `json:"version"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
}

// ArtifactFilter 按会话或项目过滤成果。
type ArtifactFilter struct {
	AgentID   string
	SessionID string
	ProjectID string
}

// ArtifactRevision 是不可变正文版本；Content 只供专门读取接口使用，不进入元数据 JSON。
type ArtifactRevision struct {
	ID               string    `json:"id"`
	ArtifactID       string    `json:"artifact_id"`
	Number           int64     `json:"number"`
	ParentRevisionID string    `json:"parent_revision_id,omitempty"`
	Content          string    `json:"-"`
	SHA256           string    `json:"sha256"`
	ByteSize         int64     `json:"byte_size"`
	Filename         string    `json:"filename"`
	MediaType        string    `json:"media_type"`
	CreatedBy        string    `json:"created_by"`
	CreatedAt        time.Time `json:"created_at"`
}

// ArtifactInput 是显式保存第一份成果版本的请求。
type ArtifactInput struct {
	Title           string       `json:"title"`
	Kind            ArtifactKind `json:"kind"`
	Filename        string       `json:"filename"`
	MediaType       string       `json:"media_type"`
	SourceMessageID string       `json:"source_message_id"`
	Fragment        string       `json:"fragment,omitempty"`
	Content         string       `json:"content"`
}

// ArtifactRevisionInput 要求客户端明确提供正在编辑的最新版本。
type ArtifactRevisionInput struct {
	ExpectedLatestRevisionID string `json:"expected_latest_revision_id"`
	Content                  string `json:"content"`
	Filename                 string `json:"filename"`
	MediaType                string `json:"media_type"`
}

// WorkbenchRequest 记录创建请求的幂等摘要，不存储正文或凭据。
type WorkbenchRequest struct {
	OwnerID       string
	Scope         string
	Key           string
	RequestSHA256 string
	ResourceID    string
	CreatedAt     time.Time
}
