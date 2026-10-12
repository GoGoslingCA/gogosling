package server

import (
	"context"
	"log/slog"

	"github.com/GoGoslingCA/gogosling/internal/agent"
	"github.com/GoGoslingCA/gogosling/internal/engine"
	"github.com/GoGoslingCA/gogosling/internal/event"
	"github.com/GoGoslingCA/gogosling/internal/extension"
	"github.com/GoGoslingCA/gogosling/internal/gateway"
	"github.com/GoGoslingCA/gogosling/internal/identity"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/permission"
	"github.com/GoGoslingCA/gogosling/internal/provider"
	"github.com/GoGoslingCA/gogosling/internal/scheduler"
	"github.com/GoGoslingCA/gogosling/internal/secret"
	"github.com/GoGoslingCA/gogosling/internal/session"
	"github.com/GoGoslingCA/gogosling/internal/tools"
	"github.com/GoGoslingCA/gogosling/internal/workbench"
	"github.com/GoGoslingCA/gogosling/internal/workbench/preview"
	"github.com/GoGoslingCA/gogosling/internal/workspace"
)

// 本文件是 server 与 app 两个包之间的编译期契约：
//   - app 组合根构造 Deps / Options 并调用 New；
//   - server 只通过这里列出的依赖访问内核，不自己创建任何领域对象。
//
// 约定的生命周期（server 实现，app 调用）：
//
//	srv, err := server.New(deps, opts)
//	addr, err := srv.Listen(ctx)   // 绑定监听地址（opts.ListenAddr 的端口可为 0），返回实际地址
//	err = srv.Serve(ctx)           // 阻塞直到 ctx 取消，随后优雅关闭
//	h := srv.Handler()             // 供 httptest 直接驱动
//
// app 在 Listen 之后、Serve 之前写入 daemon.json（端点 = "http://" + addr）。

// ChannelService 是 IM 通道的管理接口（期望由 internal/channel 包实现）。
//
// 凭据只经 Set/Create/Update 的 secret 参数写入 secret 存储，记录里只保存引用名；
// 返回的 Channel 不含凭据。Create/Update/Delete 完成后实现方负责让网关重载该通道。
type ChannelService interface {
	Create(ctx context.Context, in ChannelInput) (*model.Channel, error)
	Get(ctx context.Context, id string) (*model.Channel, error)
	List(ctx context.Context, agentID string) ([]*model.Channel, error)
	Update(ctx context.Context, id string, in ChannelPatch) (*model.Channel, error)
	Delete(ctx context.Context, id string) error
	// Reload 重启该通道的适配器并返回最新记录。
	Reload(ctx context.Context, id string) (*model.Channel, error)
	// HasSecret 报告通道是否已配置凭据。
	HasSecret(ctx context.Context, ch *model.Channel) bool
	// Status 返回适配器运行状态（stopped / starting / running / failed）。
	Status(id string) (state string, lastError string)
}

// ChannelInput 是创建通道的输入；Secret 只写入 secret 存储。
type ChannelInput struct {
	AgentID string
	Kind    model.ChannelKind
	Name    string
	Secret  string
	Config  []byte
	Enabled *bool
}

// ChannelPatch 是部分更新；nil 表示不修改，Secret 为非 nil 空串表示清除凭据。
type ChannelPatch struct {
	Name    *string
	Secret  *string
	Config  []byte
	Enabled *bool
}

// Deps 是本地 API 需要的全部内核依赖。除 Logger 与 Bus 外均不可为 nil（Gateway、Channels 可为 nil，表示通道功能未启用）。
type Deps struct {
	Preview          *preview.Service         // 可选的只读文件预览。
	PreviewConverter PreviewConverter         // 缺席时保留内容阅读及原文件下载。
	Workbench        *workbench.Service       // 可选；缺席时不声明工作台能力。
	Changes          *workbench.ChangeService // 独立人工文件操作；旧内核可省略。
	Auth             *identity.Authenticator
	Bus              *event.Bus
	Store            model.Store // 设置（SettingsRepo）与审计（AuditRepo）
	Secrets          secret.Store
	Agents           *agent.Service
	Providers        *provider.Service
	Sessions         *session.Service
	Permissions      *permission.Service
	Jobs             *scheduler.Service
	Extensions       *extension.Manager
	Tools            *tools.Executor // GET /agents/{id}/tools 使用 ToolsFor
	Workspaces       *workspace.Manager
	Engine           *engine.Supervisor // GET /version 的 worker 状态
	Gateway          *gateway.Gateway
	Channels         ChannelService
	Logger           *slog.Logger
}

// Options 是传输层自身的配置。
type Options struct {
	// ListenAddr 形如 "127.0.0.1:46750"；端口为 0 时由系统分配，实际地址由 Listen 返回。
	ListenAddr string
	// AllowedOrigins 是除"无 Origin"与 "app://gogosling" 之外额外允许的 Origin（开发服务器等）。
	AllowedOrigins []string
	// MaxBodyBytes 是普通 JSON 请求体上限（默认 1 MiB）；MaxUploadBytes 是附件上限（默认 64 MiB）。
	MaxBodyBytes   int64
	MaxUploadBytes int64
	// StartedAt 用于 /version 的 started_at。
	StartedAt string
	// DefaultLocale / DefaultTimezone 是内核实际生效的语言与时区（由 app 解析：设置 > 系统环境 > 默认），
	// 用于 GET /settings 的 effective_* 字段与未带 Accept-Language 时的错误文案。
	DefaultLocale   string
	DefaultTimezone string
	// Shutdown 由 app 提供：POST /api/v1/system/shutdown 鉴权通过后调用它触发优雅关闭（必须非阻塞）。
	// 为 nil 时该端点返回 unavailable。
	Shutdown func()
}
