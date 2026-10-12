// Package app 是守护进程的组合根：创建并连接各模块，管理它们的启动与关闭顺序。
//
// 职责：
//   - 按依赖顺序构造模块，把依赖显式传入（不使用全局单例，不使用 setter 注入）；
//     各领域包定义的消费者接口在 adapters_*.go 里用薄适配器接到实现上，接口不匹配处显式转换。
//   - 统一的生命周期：顺序启动、逆序关闭；任一模块启动失败时已启动的模块逆序关闭后返回错误。
//   - 单实例锁（daemon.lock）与运行时句柄文件（daemon.json），供桌面壳与 CLI 发现守护进程。
//
// 启动顺序：数据目录 → 单实例锁 → store → 设置（有效语言与时区）→ secret（按 secret.backend 记录）
// → event.Bus → 令牌 → workspace → provider → agent → permission（过期扫描循环）→ sandbox
// → extension（内置资产）→ tools.Executor → engine → session（恢复遗留运行；绑定 agent 的会话清理）
// → 总线桥接（审批状态 → session.NoteApproval；会话结束 → 撤销授权）→ scheduler → gateway + channel
// → agent.deleted → scheduler.Forget / gateway.ForgetAgent → server.Listen → daemon.json → worker.status。
//
// 关闭顺序：worker.status → agent.deleted 订阅 → server → gateway（≤ 总预算的 1/4）→ scheduler
// → engine（至少 ShutdownGrace + 强制结束等待）→ session → 总线桥接 → extension → permission
// → 对仍未结束的写库循环（调度器、引擎监管循环、审批状态桥接、权限扫描）用固定上限再等一次
// → store → daemon.json → 锁。每一步有自己的子预算，前面某一步卡住不会让后面的写库循环
// 落到已关闭的数据库上。
//
// 关闭的触发：Run 的 ctx 取消（信号），或本地 API 的 POST /api/v1/system/shutdown（server.Options.Shutdown）。
//
// 本包只做装配与生命周期；领域规则应放在领域包（现存的例外见 adapters_*.go 的注释与
// docs/reference-notes/app.md）。边界：只有本包与 cmd 下的入口可以同时导入传输层与领域包。
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/agent"
	"github.com/GoGoslingCA/gogosling/internal/channel"
	"github.com/GoGoslingCA/gogosling/internal/config"
	"github.com/GoGoslingCA/gogosling/internal/engine"
	"github.com/GoGoslingCA/gogosling/internal/event"
	"github.com/GoGoslingCA/gogosling/internal/extension"
	"github.com/GoGoslingCA/gogosling/internal/gateway"
	"github.com/GoGoslingCA/gogosling/internal/identity"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/permission"
	"github.com/GoGoslingCA/gogosling/internal/protocol/mcp"
	"github.com/GoGoslingCA/gogosling/internal/provider"
	"github.com/GoGoslingCA/gogosling/internal/sandbox"
	"github.com/GoGoslingCA/gogosling/internal/scheduler"
	"github.com/GoGoslingCA/gogosling/internal/secret"
	"github.com/GoGoslingCA/gogosling/internal/server"
	"github.com/GoGoslingCA/gogosling/internal/session"
	"github.com/GoGoslingCA/gogosling/internal/store"
	"github.com/GoGoslingCA/gogosling/internal/tools"
	"github.com/GoGoslingCA/gogosling/internal/version"
	"github.com/GoGoslingCA/gogosling/internal/workbench"
	"github.com/GoGoslingCA/gogosling/internal/workbench/preview"
	"github.com/GoGoslingCA/gogosling/internal/workspace"
)

// dataDirPerm 让数据根目录只对当前用户可见。
const dataDirPerm = 0o700

// DatabaseFileName 是数据目录下 SQLite 控制面的文件名。
const DatabaseFileName = "gogosling.db"

// 生命周期默认值。
const (
	// DefaultShutdownTimeout 是关闭过程的总预算；各步骤在其中分配子预算（见 runtimeState.shutdown），
	// 引擎的最低保障与写库循环的固定再等待可能让实际耗时略超出它。
	DefaultShutdownTimeout = 20 * time.Second
	// DefaultRunTimeout 是单次运行的安全上限（含等待审批），防止挂死的运行让会话永久处于忙碌。
	DefaultRunTimeout = time.Hour
	// busCapacity 是事件总线的回放缓冲区大小。
	busCapacity = 4096
)

// Option 调整 App 的可选行为；主要供测试与嵌入方使用。
type Option func(*options)

type options struct {
	secrets              secret.Store
	getenv               func(string) string
	workerStatusInterval time.Duration
	shutdownTimeout      time.Duration
	runTimeout           time.Duration
	resources            *resourceLocator
}

// WithSecretStore 注入凭据存储（例如测试用的文件存储），不再按环境自动选择后端。
func WithSecretStore(s secret.Store) Option { return func(o *options) { o.secrets = s } }

// WithWorkerStatusInterval 设置 worker.status 的轮询间隔（默认 2s）。
func WithWorkerStatusInterval(d time.Duration) Option {
	return func(o *options) { o.workerStatusInterval = d }
}

// WithShutdownTimeout 设置关闭过程的时间预算（默认 DefaultShutdownTimeout）。
func WithShutdownTimeout(d time.Duration) Option { return func(o *options) { o.shutdownTimeout = d } }

// WithRunTimeout 设置单次运行的上限（默认 DefaultRunTimeout；负数表示不限制）。
func WithRunTimeout(d time.Duration) Option { return func(o *options) { o.runTimeout = d } }

// WithGetenv 替换环境变量读取（测试用）；影响语言与时区推断、凭据后端选择、资源目录定位与额外 Origin。
func WithGetenv(fn func(string) string) Option { return func(o *options) { o.getenv = fn } }

// App 持有守护进程内所有长生命周期的组件。Run 只能调用一次。
type App struct {
	cfg    config.Config
	logger *slog.Logger
	opts   options

	mu      sync.Mutex
	ran     bool
	addr    string
	readyCh chan struct{}

	// shutdownCh 在收到关闭请求（POST /api/v1/system/shutdown）时关闭；shutdownOnce 保证只关闭一次。
	shutdownCh   chan struct{}
	shutdownOnce sync.Once
}

// New 校验配置并创建 App；此时不产生任何副作用。
//
// ListenAddr 必须是回环地址（config.IsLoopbackAddr：127.0.0.0/8、::1 或 localhost，与 server 同一规则）：
// 回环地址不是安全边界，但绑定到其他网卡会把本地 API 暴露给局域网，这需要一个显式的、本版本不提供的开关。
func New(cfg config.Config, logger *slog.Logger, opts ...Option) (*App, error) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if strings.TrimSpace(cfg.DataDir) == "" || !filepath.IsAbs(cfg.DataDir) {
		return nil, model.Errorf(model.CodeInvalid, "数据目录必须是绝对路径：%q", cfg.DataDir)
	}
	if err := checkLoopback(cfg.ListenAddr); err != nil {
		return nil, err
	}
	o := options{getenv: os.Getenv, shutdownTimeout: DefaultShutdownTimeout, runTimeout: DefaultRunTimeout}
	for _, opt := range opts {
		opt(&o)
	}
	if o.getenv == nil {
		o.getenv = os.Getenv
	}
	if o.shutdownTimeout <= 0 {
		o.shutdownTimeout = DefaultShutdownTimeout
	}
	return &App{cfg: cfg, logger: logger, opts: o, readyCh: make(chan struct{}), shutdownCh: make(chan struct{})}, nil
}

// requestShutdown 请求优雅关闭（server.Options.Shutdown）：非阻塞、可重复调用；Run 之前调用时
// Run 启动后立即开始关闭。
func (a *App) requestShutdown() {
	a.shutdownOnce.Do(func() { close(a.shutdownCh) })
}

// Addr 返回本地 API 的实际监听地址；Ready 关闭之前为空。
func (a *App) Addr() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.addr
}

// Ready 在本地 API 开始服务、daemon.json 已写入之后关闭。
func (a *App) Ready() <-chan struct{} { return a.readyCh }

// Run 启动守护进程并阻塞，直到 ctx 取消、收到经 API 的关闭请求或本地 API 意外退出；
// 返回前按关闭顺序停止所有已启动的模块。正常关闭（含经 API 关闭）返回关闭过程中的错误或 nil。
//
// 启动失败时返回带 model.Code 的错误：数据目录已被另一个守护进程占用为 CodeConflict
// （Details 含 pid 与 data_dir）；记录的凭据后端不可用为 CodeUnavailable。
func (a *App) Run(ctx context.Context) error {
	a.mu.Lock()
	if a.ran {
		a.mu.Unlock()
		return model.NewError(model.CodeConflict, "App.Run 只能调用一次")
	}
	a.ran = true
	a.mu.Unlock()

	// runCtx 在 ctx 取消或收到关闭请求时结束；Serve 随之优雅退出。
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	go func() {
		select {
		case <-a.shutdownCh:
			a.logger.Info("收到关闭请求")
			cancelRun()
		case <-runCtx.Done():
		}
	}()

	rt := &runtimeState{log: a.logger}
	if err := a.start(ctx, rt); err != nil {
		rt.shutdown(a.opts.shutdownTimeout)
		return err
	}

	a.mu.Lock()
	a.addr = rt.addr
	a.mu.Unlock()
	close(a.readyCh)
	a.logger.Info("gogoslingd 已就绪", "endpoint", "http://"+rt.addr, "data_dir", a.cfg.DataDir, "version", version.Version)

	serveErr := rt.srv.Serve(runCtx)
	rt.served = true
	unexpected := serveErr != nil && runCtx.Err() == nil
	if unexpected {
		a.logger.Error("本地 API 意外退出", "error", serveErr)
	}
	a.logger.Info("gogoslingd 正在关闭")
	shutdownErr := rt.shutdown(a.opts.shutdownTimeout)
	if unexpected {
		return serveErr
	}
	return shutdownErr
}

// runtimeState 记录已经启动的模块，关闭时据此按关闭顺序处理；字段为零值表示该模块未启动。
//
// 模块以最小接口保存（而不是具体类型），关闭流程因此可以用替身单独测试。
type runtimeState struct {
	log *slog.Logger

	lock    *instanceLock
	handle  *identity.HandleFile
	store   closer
	ext     closer
	engine  stopper
	session interface{ Close() }
	changes interface{ Close() }
	gateway stopper
	srv     *server.Server
	addr    string
	served  bool

	// 后台循环，按关闭顺序逐个停止：
	//   status    worker.status 轮询（只发布事件）；
	//   lifecycle agent.deleted → scheduler.Forget / gateway.ForgetAgent（只改内存与适配器）；
	//   sched     调度循环（写库：任务状态写回、run_agent 运行收尾）；
	//   notes     approval.* → session.NoteApproval（写库：运行状态）；
	//   revoke    会话结束 → 撤销会话授权（只改内存）；
	//   perm      审批过期扫描（写库）。
	status, lifecycle, sched, notes, revoke, perm *bgLoop
	// stopEngine / stopGateway 取消引擎与网关的生命周期 ctx（它们自己的 Stop 之后调用）。
	stopEngine, stopGateway context.CancelFunc
}

// closer 是关闭时只需要 Close 的模块（store、extension）。
type closer interface{ Close() error }

// stopper 是关闭时需要带时限停止的模块（engine、gateway）。
type stopper interface {
	Stop(ctx context.Context) error
}

// bgLoop 是一个长生命周期的后台 goroutine：stop 取消它的 ctx，done 在它退出后关闭。
type bgLoop struct {
	stop context.CancelFunc
	done chan struct{}
}

// goLoop 在独立 goroutine 中运行 fn，返回可停止、可等待的句柄。
func goLoop(parent context.Context, fn func(ctx context.Context)) *bgLoop {
	ctx, cancel := context.WithCancel(parent)
	l := &bgLoop{stop: cancel, done: make(chan struct{})}
	go func() {
		defer close(l.done)
		fn(ctx)
	}()
	return l
}

// finished 报告循环是否已经退出（nil 视为已退出）。
func (l *bgLoop) finished() bool {
	if l == nil {
		return true
	}
	select {
	case <-l.done:
		return true
	default:
		return false
	}
}

// halt 取消循环并在 ctx 内等待它退出；返回是否已退出。nil 视为已退出。
func (l *bgLoop) halt(ctx context.Context) bool {
	if l == nil {
		return true
	}
	l.stop()
	select {
	case <-l.done:
		return true
	case <-ctx.Done():
		return false
	}
}

// start 按依赖顺序构造并启动全部模块；失败时返回错误，已启动的模块由调用方 shutdown。
func (a *App) start(ctx context.Context, rt *runtimeState) error {
	log := a.logger
	dataDir := a.cfg.DataDir
	startedAt := time.Now().UTC()
	// 后台模块的生命周期独立于 ctx：信号到来时先停 server，其余模块按关闭顺序逐个停止。
	life := context.WithoutCancel(ctx)

	// 1. 数据目录与单实例锁。
	if err := ensureDataDir(dataDir, log); err != nil {
		return err
	}
	lock, err := acquireLock(dataDir)
	if err != nil {
		return err
	}
	rt.lock = lock
	hf := identity.NewHandleFile(dataDir)
	rt.handle = &hf

	// 2. 持久化与设置：有效语言与时区在这里解析一次，交给引擎、网关、调度器、提示构造器与本地 API。
	st, err := store.Open(ctx, filepath.Join(dataDir, DatabaseFileName), store.WithLogger(log.With("module", "store")))
	if err != nil {
		return fmt.Errorf("打开数据库: %w", err)
	}
	rt.store = st
	cfg, err := loadSettings(ctx, st, a.opts.getenv, log)
	if err != nil {
		return fmt.Errorf("读取设置: %w", err)
	}
	log.Info("设置", "locale", cfg.Locale, "timezone", cfg.EffectiveTimezone)

	// 3. 凭据、事件、令牌。
	secrets := a.opts.secrets
	if secrets == nil {
		secrets, err = openSecrets(ctx, dataDir, a.opts.getenv, log.With("module", "secret"))
		if err != nil {
			return fmt.Errorf("打开凭据存储: %w", err)
		}
	}
	sec := secretAdapter{s: secrets}
	bus := event.NewBus(busCapacity)
	token, err := identity.NewToken()
	if err != nil {
		return fmt.Errorf("生成访问令牌: %w", err)
	}
	auth, err := identity.NewAuthenticator(token)
	if err != nil {
		return fmt.Errorf("创建令牌校验器: %w", err)
	}

	// 4. 工作区、供应商、智能体。智能体服务先于会话服务构造（会话服务需要读取智能体），
	// 删除智能体时的会话清理经 cleaner 在第 9 步绑定。
	workspaces := workspace.New(dataDir)
	providers := provider.New(st, sec, bus, nil, log.With("module", "provider"), provider.WithAgentLister(st))
	cleaner := &sessionCleaner{}
	agents, err := agent.New(st, providers, agent.WorkspaceOpenerFunc(func(ctx context.Context, id, dir string) (agent.Workspace, error) {
		ws, err := workspaces.Open(ctx, id, dir)
		if err != nil {
			return nil, err
		}
		return ws, nil
	}), bus, log.With("module", "agent"), agent.WithSessionCleaner(cleaner))
	if err != nil {
		return fmt.Errorf("创建智能体服务: %w", err)
	}

	// 5. 权限（含审批过期扫描循环）与沙箱。
	perms, err := permission.New(permission.Deps{Approvals: st, Audit: st, Bus: bus, Logger: log.With("module", "permission"), DataDir: dataDir})
	if err != nil {
		return fmt.Errorf("创建权限服务: %w", err)
	}
	rt.perm = goLoop(life, func(ctx context.Context) {
		if err := perms.Run(ctx); err != nil && ctx.Err() == nil {
			log.Error("审批过期扫描退出", "error", err)
		}
	})
	runner, caps := sandbox.New(log.With("module", "sandbox"))
	log.Info("沙箱", "platform", caps.Platform, "mechanism", string(sandbox.MechanismOf(runner)),
		"candidate_mechanism", string(caps.Mechanism), "available", caps.Available, "implemented", caps.Implemented)

	// 6. 扩展与内置资产。
	ext := extension.New(st, dataDir, log.With("module", "extension"),
		extension.NewMCPDialer(sec.Resolve, mcp.Options{Logger: log.With("module", "mcp"), Version: version.Version}),
		extension.WithBus(bus))
	rt.ext = ext
	a.seedBundled(ctx, ext)

	// 7. 工具执行器：它同时是会话的 ToolSource 与本地 API 的工具清单来源（同一个 ToolsFor，
	// "没有技能时隐藏 load_skill" 由 tools 经 SkillLoader.HasSkills 判定）。
	// 运行上下文解析需要 session，而 session 需要执行器提供工具声明：先建解析器，稍后绑定。
	registry, err := tools.NewBuiltinRegistry(tools.BuiltinOptions{Runner: runner, Skills: skillLoaderAdapter{s: ext}, Logger: log.With("module", "tools"), Locale: cfg.Locale})
	if err != nil {
		return fmt.Errorf("登记内置工具: %w", err)
	}
	changeFiles := changeFiles{workbenchFiles: workbenchFiles{agents: agents, workspaces: workspaces}, store: st}
	changes, err := workbench.NewChanges(st, changeFiles, bus)
	if err != nil {
		return err
	}
	rt.changes = changes
	resolver := &runContextResolver{agents: agents, workspaces: workspaces, operations: st}
	executor, err := tools.NewExecutor(tools.Deps{
		Registry:          registry,
		Resolver:          resolver,
		Permissions:       permissionAdapter{p: perms},
		External:          ext,
		Logger:            log.With("module", "tools"),
		OperationResolver: operationResolver{changes: changes, files: changeFiles.workbenchFiles},
		UserOperationTool: fileChangeTool{changes: changes},
	})
	if err != nil {
		return fmt.Errorf("创建工具执行器: %w", err)
	}
	changes.BindExecutor(executor)

	// 8. 引擎：找不到工作进程不致命，Status 报告 unavailable，运行请求返回 unavailable。
	eng := engine.New(engine.Config{Tools: executor, Locale: cfg.Locale, DaemonVersion: version.Version, Logger: log.With("module", "engine")})
	engCtx, stopEngine := context.WithCancel(life)
	rt.stopEngine = stopEngine
	if err := eng.Start(engCtx); err != nil {
		return fmt.Errorf("启动引擎: %w", err)
	}
	rt.engine = eng
	if _, err := engine.Locate(); err != nil {
		log.Warn("未找到工作进程；守护进程照常启动，对话不可用", "error", err, "hint", engine.EnvWorkerCmd)
	}

	// 9. 会话：启动时把上次遗留的非终态运行标记为失败；绑定智能体删除时的会话清理与工具的运行上下文。
	runTimeout := a.opts.runTimeout
	if runTimeout < 0 {
		runTimeout = 0
	}
	sessions := session.New(st, agentSource{agents: agents}, providers, executor,
		runtimeAdapter{rt: eng}, bus, log.With("module", "session"),
		session.Options{PromptBuilder: workbenchPrompt{skillPrompt{skills: ext, locale: cfg.Locale, log: log}}, RunTimeout: runTimeout, MaxParallelRuns: cfg.MaxParallel, MaxQueuedRuns: session.DefaultQueuedRuns, WorkspaceGroup: func(ctx context.Context, agent *model.Agent) (string, error) {
			ws, err := workspaces.Open(ctx, agent.ID, agent.WorkspaceDir)
			if err != nil {
				return "", err
			}
			return filepath.EvalSymlinks(ws.Root())
		}})
	rt.session = sessions
	resolver.bind(sessions)
	cleaner.bind(sessions)
	if n, err := sessions.RecoverActiveRuns(ctx); err != nil {
		log.Error("恢复遗留运行时部分失败", "recovered", n, "error", err)
	} else if n > 0 {
		log.Info("已把上次遗留的运行标记为失败", "count", n)
	}
	if err := changes.RecoverActive(ctx); err != nil {
		return fmt.Errorf("恢复文件操作: %w", err)
	}
	// 总线桥接：审批等待写进运行状态（approval.* 由 permission 直接发布，不经过运行时回调）；
	// 会话删除或归档时撤销它的"本会话内允许"授权。订阅的起点在这里同步取得，不漏掉之后发布的事件。
	notesFrom, revokeFrom := bus.LastSeq(), bus.LastSeq()
	rt.notes = goLoop(life, func(ctx context.Context) {
		noteApprovals(ctx, bus, notesFrom, sessions, log)
	})
	rt.revoke = goLoop(life, func(ctx context.Context) {
		revokeGrantsOnSessionEnd(ctx, bus, revokeFrom, perms, log)
	})

	// 10. 定时任务（阻塞循环，独立 goroutine）。
	jobs, err := scheduler.New(st, jobRunner{sessions: sessions, bus: bus, log: log.With("module", "scheduler")}, bus,
		log.With("module", "scheduler"), scheduler.Options{DefaultTimezone: cfg.Timezone})
	if err != nil {
		return fmt.Errorf("创建调度器: %w", err)
	}
	rt.sched = goLoop(life, func(ctx context.Context) {
		if err := jobs.Start(ctx); err != nil {
			log.Error("调度器退出", "error", err)
		}
	})

	// 11. IM 网关与通道服务：网关启动失败不致命，通道 CRUD 照常可用。
	factory := gateway.DefaultFactory(log.With("module", "gateway"))
	gw := gateway.New(st, senderAdapter{s: sessions}, sec, bus, factory, log.With("module", "gateway"),
		gateway.Options{Texts: gatewayTexts(cfg.Locale)})
	gwCtx, stopGateway := context.WithCancel(life)
	rt.stopGateway = stopGateway
	if err := gw.Start(gwCtx); err != nil {
		log.Error("IM 网关启动失败，通道暂不可用", "error", err)
	}
	rt.gateway = gw
	channels, err := channel.New(st, sec, channel.Options{
		Gateway:        channelGateway{g: gw},
		Kinds:          factory,
		ValidateConfig: validateChannelConfig,
		Logger:         log.With("module", "channel"),
	})
	if err != nil {
		return fmt.Errorf("创建通道服务: %w", err)
	}
	// 删除智能体后清理内存状态：数据库中的任务与通道已由外键级联删除。
	lifecycleFrom := bus.LastSeq()
	rt.lifecycle = goLoop(life, func(ctx context.Context) {
		forgetDeletedAgents(ctx, bus, lifecycleFrom, []agentForgetter{schedulerForgetter{s: jobs}, gw}, log)
	})

	// 12. 本地 API：先监听拿到实际地址，再写 daemon.json，最后 Serve。
	bench, err := workbench.New(st, workbenchFiles{agents: agents, workspaces: workspaces}, bus)
	if err != nil {
		return fmt.Errorf("创建工作台服务: %w", err)
	}
	srv, err := server.New(server.Deps{
		Workbench:        bench,
		PreviewConverter: newPreviewConverter(a.opts.getenv("GOGOSLING_OFFICE_BIN")),
		Preview:          preview.New(previewFiles{workbenchFiles{agents: agents, workspaces: workspaces}, dataDir}, log.With("module", "preview")),
		Changes:          changes,
		Auth:             auth,
		Bus:              bus,
		Store:            st,
		Secrets:          sec,
		Agents:           agents,
		Providers:        providers,
		Sessions:         sessions,
		Permissions:      perms,
		Jobs:             jobs,
		Extensions:       ext,
		Tools:            executor,
		Workspaces:       workspaces,
		Engine:           eng,
		Gateway:          gw,
		Channels:         channelServer{svc: channels},
		Logger:           log.With("module", "server"),
	}, server.Options{
		ListenAddr:      a.cfg.ListenAddr,
		AllowedOrigins:  splitList(a.opts.getenv(config.EnvAllowedOrigins)),
		StartedAt:       startedAt.Format(time.RFC3339),
		DefaultLocale:   cfg.Locale,
		DefaultTimezone: cfg.EffectiveTimezone,
		Shutdown:        a.requestShutdown,
	})
	if err != nil {
		return fmt.Errorf("创建本地 API: %w", err)
	}
	ln, err := srv.Listen(ctx)
	if err != nil {
		return fmt.Errorf("监听 %s: %w", a.cfg.ListenAddr, err)
	}
	addr := ln.String()
	rt.srv, rt.addr = srv, addr
	if err := hf.Write(ctx, identity.Handle{
		PID:       os.Getpid(),
		Endpoint:  "http://" + addr,
		Token:     token,
		Version:   version.Version,
		StartedAt: startedAt,
	}); err != nil {
		return fmt.Errorf("写入运行时句柄: %w", err)
	}

	// 13. 工作进程状态事件。
	rt.status = goLoop(life, func(ctx context.Context) {
		watchWorkerStatus(ctx, eng, bus, a.opts.workerStatusInterval)
	})
	return nil
}

// seedBundled 安装或升级随发行包分发的内置扩展；找不到资源目录时跳过并记录日志。
func (a *App) seedBundled(ctx context.Context, ext *extension.Manager) {
	loc := systemResourceLocator()
	if a.opts.resources != nil {
		loc = *a.opts.resources
	}
	loc.getenv = a.opts.getenv
	res, source := loc.locate()
	dir := bundledExtensionsDir(res)
	if dir == "" {
		a.logger.Info("未找到内置资源目录，跳过内置扩展", "hint", config.EnvResources)
		return
	}
	report, err := ext.SeedBundled(ctx, os.DirFS(dir))
	if err != nil {
		a.logger.Error("安装内置扩展失败", "dir", dir, "error", err)
		return
	}
	a.logger.Info("内置扩展", "dir", dir, "source", source,
		"installed", len(report.Installed), "upgraded", len(report.Upgraded), "failed", len(report.Failed))
}

// 关闭预算的划分（见 shutdown）。
const (
	// gatewayBudgetDivisor：网关最多占总预算的 1/N。IM 平台不可达或限流时放弃剩余回复，
	// 不让它耗尽后续写库步骤的时间。
	gatewayBudgetDivisor = 4
	// engineKillWait 对应 engine 在宽限期后强制结束工作进程、再等待操作系统回收的上限（engine 内部的 killWait）。
	engineKillWait = 5 * time.Second
	// engineStopFloor 是 engine.Stop 至少能拿到的时间：总预算剩余更少时也给足，
	// 否则监管循环会在 store 关闭之后才对在途运行 failRuns，终态写不进数据库。
	engineStopFloor = engine.DefaultShutdownGrace + engineKillWait
	// drainCap 是 store.Close 之前对仍未结束的写库循环再等一次的固定上限，不受总预算剩余量影响。
	drainCap = 5 * time.Second
)

// shutdown 按关闭顺序停止已启动的模块；可在任何启动阶段之后调用。返回全部关闭错误（errors.Join）。
//
// 预算：每一步有自己的子预算，而不是所有步骤共享一个到期即全部放弃的 ctx——
//   - 网关最多占 budget/4，超时后放弃剩余回复（网关的 Stop 不按时返回时也不再等它）；
//   - 调度循环、总线桥接、权限扫描等用总预算的剩余部分；
//   - 引擎至少 engineStopFloor（ShutdownGrace + 强制结束等待），即使总预算已经用完；
//   - store.Close 之前，对仍未结束的写库循环（调度器、引擎监管循环、审批状态桥接、权限扫描）
//     再用固定上限 drainCap 等一次；仍未结束才记录错误并关闭数据库。
//
// 因此最坏情况下关闭耗时约为 budget + engineStopFloor + drainCap。
func (rt *runtimeState) shutdown(budget time.Duration) error {
	deadline := time.Now().Add(budget)
	// stepCtx 为一个步骤分配预算：不超过 max（0 表示不限），不超过总预算剩余，但至少 floor。
	stepCtx := func(max, floor time.Duration) (context.Context, context.CancelFunc) {
		d := time.Until(deadline)
		if max > 0 && d > max {
			d = max
		}
		if d < floor {
			d = floor
		}
		return context.WithTimeout(context.Background(), d)
	}
	var errs []error
	note := func(step string, err error) {
		if err != nil {
			rt.log.Error("关闭模块失败", "step", step, "error", err)
			errs = append(errs, fmt.Errorf("%s: %w", step, err))
		}
	}
	halt := func(l *bgLoop) {
		ctx, cancel := stepCtx(0, 0)
		defer cancel()
		l.halt(ctx)
	}

	halt(rt.status)
	halt(rt.lifecycle)
	if rt.srv != nil && !rt.served {
		// 已监听但还没开始服务（启动在后续步骤失败）：用已取消的 ctx 调 Serve 释放监听。
		cctx, c := context.WithCancel(context.Background())
		c()
		note("server", rt.srv.Serve(cctx))
	}
	if rt.gateway != nil {
		ctx, cancel := stepCtx(budget/gatewayBudgetDivisor, 0)
		note("gateway", callWithin(ctx, rt.gateway.Stop))
		cancel()
	}
	if rt.stopGateway != nil {
		rt.stopGateway()
	}
	halt(rt.sched)
	engineDone := rt.engine == nil
	if rt.engine != nil {
		ctx, cancel := stepCtx(0, engineStopFloor)
		err := rt.engine.Stop(ctx)
		cancel()
		engineDone = err == nil
		note("engine", err)
	}
	if rt.stopEngine != nil {
		rt.stopEngine()
	}
	if rt.session != nil {
		rt.session.Close()
	}
	if rt.changes != nil {
		rt.changes.Close()
	}
	halt(rt.notes)
	halt(rt.revoke)
	if rt.ext != nil {
		note("extension", rt.ext.Close())
	}
	halt(rt.perm)

	// store.Close 之前：仍在写库的循环用固定上限再等一次，宁可多等几秒也不让它们写到已关闭的数据库。
	if rt.store != nil {
		pending := !engineDone || !rt.sched.finished() || !rt.notes.finished() || !rt.perm.finished()
		if pending {
			ctx, cancel := context.WithTimeout(context.Background(), drainCap)
			rt.log.Warn("关闭预算内仍有写库的后台循环未结束，再等一次", "cap", drainCap)
			if !engineDone {
				engineDone = rt.engine.Stop(ctx) == nil
			}
			var stuck []string
			for _, l := range []struct {
				name string
				loop *bgLoop
			}{{"scheduler", rt.sched}, {"approval_notes", rt.notes}, {"permission", rt.perm}} {
				if !l.loop.halt(ctx) {
					stuck = append(stuck, l.name)
				}
			}
			if !engineDone {
				stuck = append(stuck, "engine")
			}
			cancel()
			if len(stuck) > 0 {
				note("drain", fmt.Errorf("后台循环在关闭期限内没有结束，仍关闭数据库: %v", stuck))
			}
		}
		note("store", rt.store.Close())
	}
	if rt.handle != nil {
		ctx, cancel := context.WithTimeout(context.Background(), drainCap)
		note("daemon.json", rt.handle.Remove(ctx))
		cancel()
	}
	if rt.lock != nil {
		note("lock", rt.lock.Release())
	}
	return errors.Join(errs...)
}

// callWithin 在独立 goroutine 中执行 fn(ctx)，最多等到 ctx 结束：fn 不尊重 ctx 而迟迟不返回时，
// 调用方不再被它拖住（fn 留在后台继续，结果被丢弃）。
func callWithin(ctx context.Context, fn func(context.Context) error) error {
	done := make(chan error, 1)
	go func() { done <- fn(ctx) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return model.Wrap(model.CodeTimeout, "等待模块停止超时", ctx.Err())
	}
}

// ensureDataDir 创建数据目录（0700）；已存在且对其他用户可见时收紧权限（Windows 跳过）。
func ensureDataDir(dir string, log *slog.Logger) error {
	if err := os.MkdirAll(dir, dataDirPerm); err != nil {
		return model.Wrap(model.CodeUnavailable, "创建数据目录失败", err)
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return model.Wrap(model.CodeUnavailable, "读取数据目录失败", err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		log.Warn("数据目录对其他用户可见，已收紧为 0700", "data_dir", dir, "mode", fi.Mode().Perm().String())
		if err := os.Chmod(dir, dataDirPerm); err != nil {
			return model.Wrap(model.CodeUnavailable, "收紧数据目录权限失败", err)
		}
	}
	return nil
}

// checkLoopback 确认监听地址只绑定回环接口；规则与 server 相同（config.IsLoopbackAddr），
// 非法地址在打开任何模块之前就被拒绝（gogoslingd 以参数错误退出）。
func checkLoopback(addr string) error {
	if config.IsLoopbackAddr(addr) {
		return nil
	}
	return &model.Error{
		Code:    model.CodeInvalid,
		Message: "本地 API 只能监听回环地址（host:port，主机为 127.0.0.0/8、::1 或 localhost）",
		Details: map[string]any{"listen": addr},
	}
}
