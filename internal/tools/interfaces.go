package tools

import (
	"context"
	"encoding/json"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// 本文件定义本包对外部能力的最小依赖。它们由其他领域包（workspace、permission、session、extension）
// 的类型在组合根 internal/app 中适配实现；本包不导入那些包。

// Entry 描述工作区内的一个文件或目录；Path 是以 "/" 分隔的工作区相对路径。
// 字段与 workspace.Entry 一致，便于适配时直接转换。
type Entry struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	Size    int64     `json:"size"`
	IsDir   bool      `json:"is_dir"`
	ModTime time.Time `json:"mod_time"`
}

// Workspace 是工具对智能体工作区的最小依赖（与 workspace 包的方法签名对齐）。
//
// 所有路径参数都是工作区相对路径；实现必须拒绝绝对路径、".." 与经符号链接越出根目录的路径
// （返回 CodeForbidden），找不到时返回 CodeNotFound，超过 maxBytes 时返回 CodeInvalid。
type Workspace interface {
	// Root 返回工作区根目录的绝对路径。
	Root() string
	// Resolve 把相对路径转换为根目录内的绝对路径，越界返回 CodeForbidden。
	Resolve(rel string) (string, error)
	// ReadFile 读取整个文件；maxBytes 大于 0 时超过该大小返回 CodeInvalid 而不是截断。
	ReadFile(ctx context.Context, rel string, maxBytes int64) ([]byte, error)
	// WriteFile 原子地写入文件，父目录不存在时自动创建。
	WriteFile(ctx context.Context, rel string, data []byte) error
	// List 返回目录下的直接子项：目录在前，再按名称排序。
	List(ctx context.Context, rel string) ([]Entry, error)
	// Stat 返回单个路径的信息；根目录的 Path 为 "."。
	Stat(ctx context.Context, rel string) (Entry, error)
}

// DocumentWindow 限制返回的正文字符范围；Version 在翻页时防止混合不同文件版本。
type DocumentWindow struct {
	StartChar int
	MaxChars  int
	Version   string
}

// DocumentSegment 是可追溯到源文档位置的纯文本片段；位置只使用段、页、工作表和行编号。
type DocumentSegment struct {
	Location  string `json:"location"`
	StartChar int    `json:"start_char"`
	Text      string `json:"text"`
}

// DocumentText 是一次有界阅读结果；NextStartChar 非零表示可继续读取。
type DocumentText struct {
	Format        string            `json:"format"`
	Version       string            `json:"version"`
	TotalChars    int               `json:"total_chars"`
	Segments      []DocumentSegment `json:"segments"`
	NextStartChar int               `json:"next_start_char,omitempty"`
	Truncated     bool              `json:"truncated"`
	Warnings      []string          `json:"warnings,omitempty"`
}

// DocumentReader 由组合根适配只读解析服务，必须使用本次已获准的 Workspace，而非重新解析智能体目录。
// 实现不执行宏、公式、脚本、OCR 或网络请求，并保留解析并发、文件大小和取消限制。
type DocumentReader interface {
	ReadDocument(ctx context.Context, workspace Workspace, path string, window DocumentWindow) (DocumentText, error)
}

// CheckRequest 是一次权限判定的输入（与 permission.Request 字段对齐）。
type CheckRequest struct {
	Principal   model.Principal
	AgentID     string
	Autonomy    model.AutonomyLevel
	SessionID   string
	RunID       string
	OperationID string
	Tool        model.ToolSpec
	Call        model.ToolCall
	// Workspace 是智能体工作区根目录的绝对路径，供权限层做路径越界判断。
	Workspace string
	// Summary 是面向用户的一句话描述，例如"执行命令: ls -la"。
	Summary string
	// Details 是结构化细节（path、paths、command、cwd、url 等），供规则匹配与审批界面展示。
	Details json.RawMessage
	// UntrustedInput 表示本会话包含不可信输入（IM 消息、网页、文件内容）。
	UntrustedInput bool
	// Isolated 表示这次调用的副作用发生在操作系统级隔离中（对应 permission.Request.Isolated）。
	// 由 Executor 按工具的 Isolation 实现填写：run_command 为 sandbox.MechanismOf(runner) != MechanismNone
	// （sandbox.Direct 视为未隔离）；其他内置工具与扩展工具不经沙箱执行，为 false。
	Isolated bool
}

// Verdict 是权限判定结果。
type Verdict struct {
	Allowed bool
	// Reason 是面向模型的说明：拒绝原因；放行时可为空。
	Reason string
	// Source 说明判定依据（guard / rule / session / autonomy / approval …），仅用于日志。
	Source string
}

// PermissionChecker 决定一次工具调用是否允许；实现可能阻塞很久等待用户审批，必须尊重 ctx。
// 返回 (Verdict{Allowed:false}, nil) 与返回 CodeDenied 的 error 都表示拒绝。
type PermissionChecker interface {
	Check(ctx context.Context, req CheckRequest) (Verdict, error)
}

// RunContext 是执行一次工具调用所需的运行上下文，由集成方按 runID 提供。
type RunContext struct {
	RunID string
	// 下列字段只供真实用户操作解析器填充；模型运行中必须为空。
	OperationID      string
	ChangeSetID      string
	PreviewDigest    string
	OperationRisk    model.ToolRisk
	OperationSummary string
	OperationDetails json.RawMessage
	SessionID        string
	Agent            *model.Agent
	Workspace        Workspace
	Principal        model.Principal
	// Untrusted 表示本会话已包含不可信输入。
	Untrusted bool
}

// RunContextResolver 按运行 ID 解析运行上下文；运行不存在时返回 CodeNotFound。
type RunContextResolver interface {
	ResolveRunContext(ctx context.Context, runID string) (RunContext, error)
}

// OperationContextResolver 从持久化操作与变更记录解析本机主体，不接受伪造 Run。
// 必须检查主体所有者、会话智能体归属、操作状态、变更双向绑定、摘要和过期时间。
type OperationContextResolver interface {
	ResolveOperationContext(ctx context.Context, operationID string) (RunContext, error)
}

// ExternalTools 提供扩展（MCP 服务、插件）贡献的工具；由 extension 包实现。
type ExternalTools interface {
	// ToolsFor 返回该智能体启用的扩展工具声明。
	ToolsFor(ctx context.Context, agent *model.Agent) ([]model.ToolSpec, error)
	// CallTool 执行一次扩展工具调用；权限判定已由调用方完成。
	CallTool(ctx context.Context, agent *model.Agent, call model.ToolCall) (model.ToolResult, error)
}

// Tool 是一个内置工具。实现必须是无状态或并发安全的。
type Tool interface {
	// Spec 返回暴露给模型的声明；InputSchema 必须是合法的 JSON Schema 对象。
	Spec() model.ToolSpec
	// Execute 执行调用。返回的 ToolResult 不需要填 CallID（由 Executor 填充）；
	// 参数非法返回 CodeInvalid，其他失败返回带错误码的错误；非零退出码等"业务失败"
	// 用 IsError 的结果表达而不是 error。
	Execute(ctx context.Context, rc RunContext, args json.RawMessage) (model.ToolResult, error)
}

// Isolation 是 Tool 的可选扩展：报告该工具的副作用是否在操作系统级隔离中发生。
// 未实现的工具视为未隔离。结果写入 CheckRequest.Isolated，供权限层的 auto_in_sandbox 档位使用。
type Isolation interface {
	Isolated() bool
}

// isIsolated 判断工具是否实现了 Isolation 并报告已隔离；tool 为 nil（扩展工具）时为 false。
func isIsolated(t Tool) bool {
	if t == nil {
		return false
	}
	i, ok := t.(Isolation)
	return ok && i.Isolated()
}

// Description 是工具调用的人类可读摘要与结构化细节。
type Description struct {
	// Summary 是一句话描述，例如"写入文件 notes/todo.md（128 字节）"。
	Summary string
	// Details 的键遵循权限层的约定：path / paths / command / cwd / url …。
	Details map[string]any
}

// Describer 是 Tool 的可选扩展：在执行前从参数生成 Description，供权限判定与审批界面使用。
// 参数非法时返回 CodeInvalid。
type Describer interface {
	Describe(args json.RawMessage) (Description, error)
}
