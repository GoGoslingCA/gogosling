// Package servertest 为 internal/server 提供接近真实的测试装配（类似 net/http/httptest）：
// 真实的 SQLite 存储、加密文件凭据后端、真实的 agent / provider / workspace / permission /
// scheduler / extension / tools / session / channel 服务；只有三处是测试替身：
//   - 推理运行时换成脚本化的 FakeRuntime；
//   - IM 网关换成 FakeGateway（通道记录、凭据与校验仍走生产的 channel.Service）；
//   - 工作进程监管者只构造不启动（状态为 unavailable）。
//
// 组合根（internal/app）里的几个适配器按依赖规则不能被传输层导入，adapters.go 中是它们的
// 测试替身（行为与生产保持一致，见该文件注释）。测试通过只说明"server 在这套装配下正确"，
// 组合根自己的装配由 internal/app 的集成测试覆盖。
//
// 本包只用于测试与 testdaemon（客户端端到端测试启动的守护进程），生产代码不要导入。
package servertest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/GoGoslingCA/gogosling/internal/agent"
	"github.com/GoGoslingCA/gogosling/internal/channel"
	"github.com/GoGoslingCA/gogosling/internal/engine"
	"github.com/GoGoslingCA/gogosling/internal/event"
	"github.com/GoGoslingCA/gogosling/internal/extension"
	"github.com/GoGoslingCA/gogosling/internal/identity"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/permission"
	"github.com/GoGoslingCA/gogosling/internal/provider"
	"github.com/GoGoslingCA/gogosling/internal/scheduler"
	"github.com/GoGoslingCA/gogosling/internal/secret"
	"github.com/GoGoslingCA/gogosling/internal/server"
	"github.com/GoGoslingCA/gogosling/internal/session"
	"github.com/GoGoslingCA/gogosling/internal/store"
	"github.com/GoGoslingCA/gogosling/internal/tools"
	"github.com/GoGoslingCA/gogosling/internal/workbench"
	"github.com/GoGoslingCA/gogosling/internal/workbench/preview"
	"github.com/GoGoslingCA/gogosling/internal/workspace"
)

// DefaultBusCapacity 是测试装配中事件总线回放缓冲的默认容量。
const DefaultBusCapacity = 1024

// FakeModel 是 SeedAgent 创建的供应商与智能体使用的模型名。
const FakeModel = "fake-model"

// Options 控制装配细节；零值可用。
type Options struct {
	// BusCapacity 是事件总线回放缓冲容量；0 表示 DefaultBusCapacity。
	BusCapacity int
	// Logger 为 nil 时丢弃日志。
	Logger *slog.Logger
	// WithoutChannels 为 true 时 Deps.Channels 为 nil（通道功能未启用）。
	WithoutChannels bool
	// WithoutWorkbench 模拟旧版或未配置工作台能力的守护进程。
	WithoutWorkbench bool
	// WithoutChanges 模拟只有 P3A 的守护进程。
	WithoutChanges bool
}

// Env 持有一套装配好的内核依赖。
type Env struct {
	Dir     string
	Token   string
	Store   *store.Store
	Bus     *event.Bus
	Secrets secret.Store
	Runtime *FakeRuntime
	// Channels 是生产的通道服务；Gateway 是它背后的假网关（可查询重载次数）。WithoutChannels 时都为 nil。
	Channels *channel.Service
	Gateway  *FakeGateway
	Deps     server.Deps

	logger     *slog.Logger
	sessions   *session.Service
	extensions *extension.Manager
}

// NewEnv 在 dir（应为空的临时目录）中装配一套内核依赖。调用方负责 Close。
func NewEnv(ctx context.Context, dir string, opts Options) (*Env, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	capacity := opts.BusCapacity
	if capacity <= 0 {
		capacity = DefaultBusCapacity
	}

	st, err := store.Open(ctx, filepath.Join(dir, "gogosling.db"), store.WithLogger(logger))
	if err != nil {
		return nil, fmt.Errorf("打开数据库: %w", err)
	}
	env := &Env{Dir: dir, Store: st, Bus: event.NewBus(capacity), logger: logger}
	ok := false
	defer func() {
		if !ok {
			_ = env.Close()
		}
	}()

	env.Secrets, err = secret.OpenWith(ctx, dir, logger, secret.Options{DisableKeyring: true})
	if err != nil {
		return nil, fmt.Errorf("打开凭据存储: %w", err)
	}
	env.Token, err = identity.NewToken()
	if err != nil {
		return nil, err
	}
	auth, err := identity.NewAuthenticator(env.Token)
	if err != nil {
		return nil, err
	}

	workspaces := workspace.New(dir)
	providers := provider.New(st, secretAdapter{env.Secrets}, env.Bus, nil, logger, provider.WithAgentLister(st))
	agents, err := agent.New(st, providers,
		agent.WorkspaceOpenerFunc(func(ctx context.Context, id, wsDir string) (agent.Workspace, error) {
			return workspaces.Open(ctx, id, wsDir)
		}),
		env.Bus, logger)
	if err != nil {
		return nil, fmt.Errorf("创建智能体服务: %w", err)
	}
	perms, err := permission.New(permission.Deps{
		Approvals: st, Audit: st, Bus: env.Bus, Logger: logger, DataDir: dir,
	})
	if err != nil {
		return nil, fmt.Errorf("创建权限服务: %w", err)
	}
	env.extensions = extension.New(st, dir, logger, nil, extension.WithBus(env.Bus))
	registry, err := tools.NewBuiltinRegistry(tools.BuiltinOptions{Logger: logger})
	if err != nil {
		return nil, fmt.Errorf("创建内置工具: %w", err)
	}
	var changes *workbench.ChangeService
	files := workbenchFiles{agents: agents, workspaces: workspaces}
	if !opts.WithoutWorkbench && !opts.WithoutChanges {
		changes, err = workbench.NewChanges(st, changeFiles{workbenchFiles: files, store: st}, env.Bus)
		if err != nil {
			return nil, err
		}
	}
	executor, err := tools.NewExecutor(tools.Deps{
		Registry:          registry,
		Resolver:          runResolver{store: st, workspaces: workspaces},
		Permissions:       permChecker{perms},
		External:          env.extensions,
		Logger:            logger,
		OperationResolver: operationResolver{changes: changes, files: files},
		UserOperationTool: fileChangeTool{changes: changes},
	})
	if err != nil {
		return nil, fmt.Errorf("创建工具执行器: %w", err)
	}
	if changes != nil {
		changes.BindExecutor(executor)
		if err = changes.RecoverActive(ctx); err != nil {
			return nil, err
		}
	}

	env.Runtime = NewFakeRuntime()
	env.Runtime.Permissions = perms
	env.sessions = session.New(st, st, providers, executor, env.Runtime, env.Bus, logger, session.Options{})
	jobs, err := scheduler.New(st, noopRunner{}, env.Bus, logger, scheduler.Options{})
	if err != nil {
		return nil, fmt.Errorf("创建调度器: %w", err)
	}
	supervisor := engine.New(engine.Config{Logger: logger})

	env.Deps = server.Deps{
		Auth:        auth,
		Bus:         env.Bus,
		Store:       st,
		Secrets:     env.Secrets,
		Agents:      agents,
		Providers:   providers,
		Sessions:    env.sessions,
		Permissions: perms,
		Jobs:        jobs,
		Extensions:  env.extensions,
		Tools:       executor,
		Workspaces:  workspaces,
		Engine:      supervisor,
		Logger:      logger,
		Changes:     changes,
	}
	if !opts.WithoutWorkbench {
		env.Deps.Preview = preview.New(workbenchFiles{agents: agents, workspaces: workspaces}, logger)
		env.Deps.Workbench, err = workbench.New(st, workbenchFiles{agents: agents, workspaces: workspaces}, env.Bus)
		if err != nil {
			return nil, err
		}
	}
	if !opts.WithoutChannels {
		env.Gateway = NewFakeGateway(st)
		env.Channels, err = newChannelService(st, secretAdapter{env.Secrets}, env.Gateway, env)
		if err != nil {
			return nil, fmt.Errorf("创建通道服务: %w", err)
		}
		env.Deps.Channels = channelServer{svc: env.Channels}
	}
	ok = true
	return env, nil
}

// Close 停止会话服务与扩展连接并关闭数据库；可重复调用。
func (e *Env) Close() error {
	if e.Deps.Changes != nil {
		e.Deps.Changes.Close()
	}
	if e.sessions != nil {
		e.sessions.Close()
	}
	var errs []error
	if e.extensions != nil {
		errs = append(errs, e.extensions.Close())
	}
	if e.Store != nil {
		errs = append(errs, e.Store.Close())
	}
	return errors.Join(errs...)
}

// SeedProvider 创建一个不需要密钥、指向本机不可达地址的 openai_compatible 供应商。
func (e *Env) SeedProvider(ctx context.Context, name string) (*model.Provider, error) {
	return e.Deps.Providers.Create(ctx, provider.Input{
		Name:         name,
		Kind:         model.ProviderOpenAICompatible,
		BaseURL:      "http://127.0.0.1:9/v1",
		DefaultModel: FakeModel,
	})
}

// SeedAgent 创建一个供应商与一个使用它的智能体。
func (e *Env) SeedAgent(ctx context.Context, name string) (*model.Agent, error) {
	p, err := e.SeedProvider(ctx, name+"-provider")
	if err != nil {
		return nil, err
	}
	return e.Deps.Agents.Create(ctx, agent.CreateInput{
		Name:  name,
		Model: model.ModelRef{ProviderID: p.ID, Model: FakeModel},
	})
}
