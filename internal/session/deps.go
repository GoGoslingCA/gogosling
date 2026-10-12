package session

import (
	"context"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// 本文件定义 session 包对外部能力的最小依赖。它们都是本包自己的接口，
// 集成阶段由 internal/app 组合根用 store、agent、provider、tools、engine 等包的实现来适配；
// 本包不导入那些包，以便各模块并行开发。

// Store 是本包需要的持久化能力：会话、消息、运行三个仓储加事务。
// model.Store 直接满足它；测试用内存实现替代。
type Store interface {
	model.SessionRepo
	model.MessageRepo
	model.RunRepo
	// Tx 在一个事务内执行 fn；语义与 model.Store.Tx 相同，不可重入。
	Tx(ctx context.Context, fn func(tx model.Store) error) error
}

// RecentSessionLister 是按对话活动时间列出会话的可选仓储能力。
// 排序为最后消息时间（空时取创建时间）、创建时间、ID 降序；不改变 Store.ListSessions 的语义。
type RecentSessionLister interface {
	ListRecentSessions(ctx context.Context, f model.SessionFilter, p model.Page) ([]*model.Session, error)
}

// AgentSource 提供智能体定义（期望由 agent 包实现）。
type AgentSource interface {
	// GetAgent 返回智能体；不存在时返回 CodeNotFound。
	GetAgent(ctx context.Context, id string) (*model.Agent, error)
}

// CredentialSource 把模型引用解析为交给工作进程的一次性凭据（期望由 provider 包实现）。
type CredentialSource interface {
	// Credentials 返回的凭据只能在内存中短暂存在；供应商不存在或未启用时返回带错误码的错误。
	Credentials(ctx context.Context, ref model.ModelRef) (model.ProviderCredentials, error)
}

// ToolSource 返回某个智能体可用的工具声明（期望由 tools 包实现，含内置工具与扩展提供的工具）。
type ToolSource interface {
	ToolsFor(ctx context.Context, agent *model.Agent) ([]model.ToolSpec, error)
}

// RunOptions 是传给运行时的推理参数。
type RunOptions struct {
	// MaxSteps 是一次运行允许的最大模型调用轮数（含工具调用循环）。
	MaxSteps int `json:"max_steps"`
	// Temperature 是采样温度。
	Temperature float64 `json:"temperature"`
	// MaxOutputTokens 是单次模型调用的最大输出 token 数。
	MaxOutputTokens int `json:"max_output_tokens"`
}

// DefaultRunOptions 是未配置时使用的推理参数，与 api/worker-rpc/protocol.md 的示例一致。
var DefaultRunOptions = RunOptions{MaxSteps: 32, Temperature: 0.7, MaxOutputTokens: 4096}

// withDefaults 用默认值填补零值字段。
func (o RunOptions) withDefaults() RunOptions {
	if o.MaxSteps <= 0 {
		o.MaxSteps = DefaultRunOptions.MaxSteps
	}
	if o.Temperature <= 0 {
		o.Temperature = DefaultRunOptions.Temperature
	}
	if o.MaxOutputTokens <= 0 {
		o.MaxOutputTokens = DefaultRunOptions.MaxOutputTokens
	}
	return o
}

// RunRequest 是交给运行时的一次完整运行请求。
//
// Messages 是会话内最近的历史消息（含本次用户消息），顺序为 Seq 升序，不含 system 角色消息；
// 系统提示只通过 Agent.SystemPrompt 传递，由运行时负责注入。
// Credentials 只在本次请求中出现；实现方不得记录或持久化。
type RunRequest struct {
	RunID       string
	SessionID   string
	Agent       *model.Agent
	Credentials model.ProviderCredentials
	Messages    []model.Message
	Tools       []model.ToolSpec
	Options     RunOptions
}

// Runtime 是智能体运行时（期望由 engine 包实现）。
//
// StartRun 在运行被受理后立即返回；此后运行事件通过 sink 异步回调，直到一个终态事件
// （run.completed / run.failed / run.cancelled）为止。sink 可能在 StartRun 返回之前就被调用，
// 也可能从不同 goroutine 调用；本包保证 sink 是并发安全且幂等的（终态之后的事件被忽略）。
// ctx 的生命周期覆盖整个运行：它被取消（例如运行超时或守护进程关闭）时实现方应尽力中止运行。
//
// CancelRun 请求取消一个在途运行；实现方随后通过 sink 发出 run.cancelled。
// 对运行时不认识的 run_id 返回 CodeNotFound 的错误，本包会在本地把运行标记为 cancelled。
type Runtime interface {
	StartRun(ctx context.Context, req RunRequest, sink func(model.RunEvent)) error
	CancelRun(ctx context.Context, runID string) error
}

// PromptBuilder 是可选扩展点：在智能体的 SystemPrompt 基础上生成最终系统提示，
// 例如追加已启用的技能清单、工作区摘要或时区信息（期望由 extension 或 agent 包实现）。
type PromptBuilder interface {
	// BuildSystemPrompt 返回最终系统提示；返回空字符串表示不发送系统消息。
	BuildSystemPrompt(ctx context.Context, agent *model.Agent, sess *model.Session) (string, error)
}
