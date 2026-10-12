package engine

import (
	"context"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// State 是工作进程的可用状态，取值与 OpenAPI VersionInfo.worker.status 一致。
type State string

const (
	// StateStarting 表示工作进程正在启动或正在重启，稍后可能变为 ready。
	StateStarting State = "starting"
	// StateReady 表示握手完成，可以接受运行请求。
	StateReady State = "ready"
	// StateUnavailable 表示找不到、无法启动、协议不兼容或已停止。
	StateUnavailable State = "unavailable"
)

// Status 是运行时的状态快照，可直接序列化为 OpenAPI 的 VersionInfo.worker。
type Status struct {
	State State `json:"status"`
	// Version 与 Python 来自最近一次成功握手，重启期间保留上一次的值。
	Version string `json:"version,omitempty"`
	Python  string `json:"python,omitempty"`
	// Capabilities 是工作进程在握手时声明的能力列表。
	Capabilities []string `json:"capabilities,omitempty"`
	// PID 是当前工作进程的进程号；没有存活的进程时为 0。
	PID int `json:"pid,omitempty"`
	// Restarts 是首次启动之后的重新拉起次数（含失败的尝试）。
	Restarts int `json:"restarts"`
	// ActiveRuns 是尚未收到终态事件的运行数。
	ActiveRuns int `json:"active_runs"`
	// LastError 是最近一次失败的原因，面向运维展示，不含敏感信息。
	LastError string `json:"last_error,omitempty"`
}

// AgentSpec 是交给工作进程的智能体描述，只包含推理循环需要的字段。
type AgentSpec struct {
	ID           string              `json:"id"`
	Name         string              `json:"name"`
	SystemPrompt string              `json:"system_prompt,omitempty"`
	Autonomy     model.AutonomyLevel `json:"autonomy"`
}

// RunOptions 是单次运行的推理参数；零值字段不发送，由工作进程使用默认值。
type RunOptions struct {
	// MaxSteps 是模型与工具之间最多往返的轮数。
	MaxSteps int `json:"max_steps,omitempty"`
	// Temperature 为 0 时表示使用工作进程默认值。
	Temperature     float64 `json:"temperature,omitempty"`
	MaxOutputTokens int     `json:"max_output_tokens,omitempty"`
}

// RunRequest 描述一次运行所需的全部输入。
//
// Credentials 含明文密钥：本结构不得写入日志、数据库或事件。
type RunRequest struct {
	RunID       string
	SessionID   string
	Agent       AgentSpec
	Credentials model.ProviderCredentials
	Messages    []model.Message
	Tools       []model.ToolSpec
	Options     RunOptions
}

// 运行失败原因的机器可读取值：engine 自己补发的 run.failed 事件的 Error，以及 StartRun 返回的
// *model.Error 的 Message（session 把它写进 Run.Error）都只用这些常量，与 session.Error* 风格一致；
// 传输层按 i18n 的 run.error.* 键本地化。细节（退出码、协议错误、底层原因）只写日志或 Details。
// 工作进程自己发出的 run.failed（例如模型服务报错）原样透传，不在此列。
const (
	// ErrorWorkerUnavailable 表示工作进程未就绪或连接已断开，运行没有被提交。
	ErrorWorkerUnavailable = "worker unavailable"
	// ErrorWorkerCrashed 表示工作进程在运行进行中意外退出、断开或违反协议。
	ErrorWorkerCrashed = "worker crashed"
	// ErrorWorkerStopped 表示运行进行中 engine 被停止（守护进程关闭）。
	ErrorWorkerStopped = "worker stopped"
	// ErrorRunRejected 表示工作进程拒绝了 run.start（参数不合法、供应商无法构造等）。
	ErrorRunRejected = "run rejected"
	// ErrorRunCancelled 表示运行在被工作进程接受之前就已取消（StartRun 返回 CodeCancelled）。
	ErrorRunCancelled = "run cancelled"
	// ErrorInvalidRequest 表示运行请求本身不合法（例如 openai_compatible 缺少 base_url），未提交给工作进程。
	ErrorInvalidRequest = "invalid run request"
)

// EventSink 接收一次运行的事件流。同一运行的事件按顺序同步投递；实现必须快速返回。
type EventSink func(model.RunEvent)

// ToolExecutor 执行工作进程申请的工具调用。集成阶段由 tools 包实现：
// 权限判定（可能长时间等待审批）、沙箱执行与审计都在其中完成。
//
// 错误码约定：model.CodeDenied 会以 JSON-RPC -32004 回给工作进程（Message 作为面向模型的说明），
// model.CodeCancelled 或 ctx 被取消会以 -32003 回给工作进程，其余错误为 -32603。
type ToolExecutor interface {
	Execute(ctx context.Context, runID string, call model.ToolCall) (model.ToolResult, error)
}

// Runtime 是内核与智能体运行时之间的唯一接口。实现必须并发安全。
type Runtime interface {
	// StartRun 把运行交给运行时并立即返回；进展通过 sink 流式回传。
	// 运行时未就绪返回 CodeUnavailable；RunID 重复返回 CodeConflict；请求不合法（含 openai_compatible
	// 缺少 base_url）返回 CodeInvalid；在被接受前已被 CancelRun 取消返回 CodeCancelled。
	// 返回错误时 Message 是 Error* 常量之一。
	StartRun(ctx context.Context, req RunRequest, sink EventSink) error
	// CancelRun 请求取消一个在途运行；未知的 runID 返回 CodeNotFound。
	// 取消是异步的：成功返回后 sink 仍会收到 run.cancelled（或其他终态）事件。
	CancelRun(ctx context.Context, runID string) error
	// Status 返回当前状态快照。
	Status() Status
}
