package gateway

import (
	"context"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// 本文件定义 gateway 包对其他领域能力的最小依赖。它们是本包自己的接口，
// 集成阶段由 internal/app 组合根用 session、secret 等包的实现适配；本包不导入那些包。

// SendRequest 是交给会话服务的一次用户输入；字段与 session.SendRequest 对齐。
type SendRequest struct {
	SessionID   string
	Text        string
	Attachments []model.Attachment
	// Trigger 固定为 model.TriggerChannel。
	Trigger model.RunTrigger
	// Principal 是发送者身份：Kind 为 PrincipalChannelPeer，ID 为 "<kind>:<peer_id>"。
	Principal model.Principal
	// Untrusted 固定为 true：IM 消息是不可信输入。
	Untrusted bool
}

// Sender 是会话服务的入口（期望由 session.Service 实现）。
//
// Sender 可以同时实现 SessionGetter：那样网关在内存路由缺失时（守护进程重启后、对端再次发言之前，
// 或定时任务写入的通道会话）能按会话的路由键推导出回复目标；不实现时只回复本进程内由网关受理过的会话。
type Sender interface {
	// EnsureByKey 返回持有 key 的 active 会话，没有时创建；key 的 agent 段必须等于 agentID，origin 段必须等于 origin。
	EnsureByKey(ctx context.Context, agentID, key string, origin model.Origin) (*model.Session, error)
	// Send 追加用户消息并启动一次运行。同一会话已有在途运行时返回 CodeConflict。
	Send(ctx context.Context, req SendRequest) (*model.Run, error)
}

// SessionGetter 是 Sender 的可选扩展：按 ID 读取会话（*session.Service 的 Get 满足）。不存在时返回 CodeNotFound。
type SessionGetter interface {
	Get(ctx context.Context, id string) (*model.Session, error)
}

// SecretReader 按名称读取通道凭据（期望由 secret 包实现）。不存在时返回 CodeNotFound。
type SecretReader interface {
	Get(ctx context.Context, name string) (string, error)
}
