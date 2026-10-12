package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/config"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

const (
	// defaultMaxBodyBytes 是普通 JSON 请求体的默认上限。
	defaultMaxBodyBytes int64 = 1 << 20
	// defaultMaxUploadBytes 是单个附件的默认上限。
	defaultMaxUploadBytes int64 = 64 << 20
	// multipartOverhead 是附件请求体在文件本身之外允许的额外字节（multipart 边界与头部）。
	multipartOverhead int64 = 1 << 20

	// readHeaderTimeout 限制读取请求头的时间，防止慢速连接占用资源。
	readHeaderTimeout = 10 * time.Second
	// idleTimeout 是保持连接的空闲上限。
	idleTimeout = 2 * time.Minute
	// shutdownTimeout 是优雅关闭时等待在途请求结束的上限。
	shutdownTimeout = 10 * time.Second
	// requestTimeout 是普通请求的读写期限；附件上传用 uploadTimeout；事件流不受期限约束。
	requestTimeout = 2 * time.Minute
	uploadTimeout  = 10 * time.Minute

	// defaultKeepalive 是事件流保活注释行的间隔。
	defaultKeepalive = 15 * time.Second
	// defaultSSEBuffer 是每个事件流订阅的缓冲长度。
	defaultSSEBuffer = 256
	// sseWriteTimeout 是事件流单次写出的期限：卡住的客户端在此之后被断开。
	sseWriteTimeout = 30 * time.Second
	// sseBatch 是一次 Flush 之前最多合并写出的事件数。
	sseBatch = 64

	// settingsLoadTimeout 是 New 读取设置项的期限。
	settingsLoadTimeout = 5 * time.Second
)

// Server 是只面向本机的 API 服务。并发安全；Listen 与 Serve 各只能调用一次。
type Server struct {
	deps     Deps
	opts     Options
	log      *slog.Logger
	settings *settingsState
	// staticOrigins 是 Options.AllowedOrigins 规范化后的集合（启动后不变）。
	staticOrigins map[string]struct{}
	handler       http.Handler

	// keepalive 与 sseBuffer 只在测试中被调整（export_test.go）。
	keepalive time.Duration
	sseBuffer int

	// shutdownOnce 保证 Options.Shutdown 只被调用一次。
	shutdownOnce sync.Once

	mu        sync.Mutex
	ln        net.Listener
	serving   bool
	closing   chan struct{}
	closeOnce sync.Once
}

// New 校验依赖与配置并创建一个尚未监听的 Server。
//
// 监听地址必须只绑定回环接口（config.IsLoopbackAddr：127.0.0.0/8、::1 或 localhost），否则返回 CodeInvalid；
// ListenAddr 为空时使用 config.DefaultListenAddr。New 会从 Store 读取一次设置项
// （时区、语言、额外 Origin），之后由 PATCH /api/v1/settings 热更新内存副本。
func New(deps Deps, opts Options) (*Server, error) {
	if err := validateDeps(deps); err != nil {
		return nil, err
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	opts = withDefaults(opts)
	if err := checkLoopbackAddr(opts.ListenAddr); err != nil {
		return nil, err
	}
	static := make(map[string]struct{}, len(opts.AllowedOrigins))
	for i, raw := range opts.AllowedOrigins {
		o, err := normalizeOrigin(raw)
		if err != nil {
			return nil, &model.Error{
				Code:    model.CodeInvalid,
				Message: "Options.AllowedOrigins 含有非法的 Origin",
				Details: map[string]any{"index": i, "origin": raw},
				Err:     err,
			}
		}
		static[o] = struct{}{}
	}

	ctx, cancel := context.WithTimeout(context.Background(), settingsLoadTimeout)
	defer cancel()
	st, err := loadSettings(ctx, deps.Store, logger)
	if err != nil {
		return nil, err
	}

	s := &Server{
		deps:          deps,
		opts:          opts,
		log:           logger,
		settings:      st,
		staticOrigins: static,
		keepalive:     defaultKeepalive,
		sseBuffer:     defaultSSEBuffer,
		closing:       make(chan struct{}),
	}
	s.handler = s.chain(s.routes())
	return s, nil
}

// Handler 返回挂好全部中间件与路由的处理器，测试可以不经网络直接驱动它。
func (s *Server) Handler() http.Handler { return s.handler }

// Addr 返回 Listen 之后的实际监听地址；尚未监听时返回 nil。
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return nil
	}
	return s.ln.Addr()
}

// Listen 绑定监听地址并返回实际地址（ListenAddr 的端口为 0 时由系统分配）。
// 重复调用返回 CodeConflict；绑定到的地址不是回环地址时关闭监听并返回 CodeInvalid。
func (s *Server) Listen(ctx context.Context) (net.Addr, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln != nil {
		return nil, model.NewError(model.CodeConflict, "本地 API 已经在监听")
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", s.opts.ListenAddr)
	if err != nil {
		return nil, model.Wrap(model.CodeUnavailable, "监听本地地址失败", err)
	}
	if tcp, ok := ln.Addr().(*net.TCPAddr); !ok || !tcp.IP.IsLoopback() {
		_ = ln.Close()
		return nil, &model.Error{
			Code:    model.CodeInvalid,
			Message: "监听地址不是回环地址",
			Details: map[string]any{"addr": ln.Addr().String()},
		}
	}
	s.ln = ln
	return ln.Addr(), nil
}

// Serve 在 Listen 得到的地址上提供服务并阻塞（尚未 Listen 时先 Listen）。
// ctx 取消后停止接受新连接、通知事件流结束，并等待在途请求最多 10s；超时则强制关闭连接。
// 正常关闭返回 nil；重复调用返回 CodeConflict。
func (s *Server) Serve(ctx context.Context) error {
	s.mu.Lock()
	if s.serving {
		s.mu.Unlock()
		return model.NewError(model.CodeConflict, "本地 API 已经在运行")
	}
	s.serving = true
	ln := s.ln
	s.mu.Unlock()
	if ln == nil {
		if _, err := s.Listen(ctx); err != nil {
			s.mu.Lock()
			s.serving = false
			s.mu.Unlock()
			return err
		}
		s.mu.Lock()
		ln = s.ln
		s.mu.Unlock()
	}

	srv := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
	}
	srv.RegisterOnShutdown(s.beginClose)

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	s.log.Info("本地 API 已启动", "addr", ln.Addr().String())

	select {
	case err := <-errCh:
		s.beginClose()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return model.Wrap(model.CodeUnavailable, "本地 API 意外停止", err)
	case <-ctx.Done():
	}

	s.beginClose()
	sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		s.log.Warn("等待在途请求超时，强制关闭连接", "error", err)
		_ = srv.Close()
	}
	<-errCh
	s.log.Info("本地 API 已停止")
	return nil
}

// beginClose 通知所有事件流结束；可重复调用。
func (s *Server) beginClose() {
	s.closeOnce.Do(func() { close(s.closing) })
}

// routes 注册 OpenAPI 中的全部路径。
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	h := func(pattern string, fn handlerFunc) { mux.Handle(pattern, s.adapt(fn)) }

	// system
	h("GET /healthz", s.handleHealthz)
	h("GET /api/v1/version", s.handleVersion)
	h("POST /api/v1/system/shutdown", s.handleShutdown)

	// agents
	h("GET /api/v1/agents", s.handleListAgents)
	h("POST /api/v1/agents", s.handleCreateAgent)
	h("GET /api/v1/agents/{id}", s.handleGetAgent)
	h("PATCH /api/v1/agents/{id}", s.handleUpdateAgent)
	h("DELETE /api/v1/agents/{id}", s.handleDeleteAgent)
	h("GET /api/v1/agents/{id}/tools", s.handleAgentTools)

	// providers
	h("GET /api/v1/providers", s.handleListProviders)
	h("POST /api/v1/providers", s.handleCreateProvider)
	h("GET /api/v1/providers/{id}", s.handleGetProvider)
	h("PATCH /api/v1/providers/{id}", s.handleUpdateProvider)
	h("DELETE /api/v1/providers/{id}", s.handleDeleteProvider)
	h("POST /api/v1/providers/{id}/test", s.handleTestProvider)

	// sessions
	h("GET /api/v1/sessions", s.handleListSessions)
	h("POST /api/v1/sessions", s.handleCreateSession)
	h("GET /api/v1/sessions/{id}", s.handleGetSession)
	h("PATCH /api/v1/sessions/{id}", s.handleUpdateSession)
	h("DELETE /api/v1/sessions/{id}", s.handleDeleteSession)
	h("GET /api/v1/sessions/{id}/messages", s.handleListMessages)
	h("POST /api/v1/sessions/{id}/messages", s.handleSendMessage)
	h("POST /api/v1/sessions/{id}/attachments", s.handleUploadAttachment)

	// 工作台 P3A；没有文件覆盖或执行路由。
	h("GET /api/v1/projects", s.requireWorkbench(s.handleListProjects))
	h("POST /api/v1/projects", s.requireWorkbench(s.handleCreateProject))
	h("GET /api/v1/projects/{id}", s.requireWorkbench(s.handleGetProject))
	h("PATCH /api/v1/projects/{id}", s.requireWorkbench(s.handleUpdateProject))
	h("GET /api/v1/projects/{id}/materials", s.requireWorkbench(s.handleListProjectMaterials))
	h("POST /api/v1/projects/{id}/materials", s.requireWorkbench(s.handleUploadProjectMaterial))
	h("DELETE /api/v1/projects/{id}/materials/{material_id}", s.requireWorkbench(s.handleRemoveProjectMaterial))
	h("PUT /api/v1/sessions/{id}/project", s.requireWorkbench(s.handleSetSessionProject))
	h("GET /api/v1/drafts", s.requireWorkbench(s.handleListDrafts))
	h("POST /api/v1/drafts", s.requireWorkbench(s.handleCreateDraft))
	h("GET /api/v1/drafts/{id}", s.requireWorkbench(s.handleGetDraft))
	h("PUT /api/v1/drafts/{id}", s.requireWorkbench(s.handleUpdateDraft))
	h("POST /api/v1/sessions/{id}/changesets", s.requireChanges(s.handlePrepareChange))
	h("GET /api/v1/sessions/{id}/changesets", s.requireChanges(s.handleListChanges))
	h("GET /api/v1/changesets/{id}", s.requireChanges(s.handleGetChange))
	h("GET /api/v1/changesets/{id}/diff", s.requireChanges(s.handleChangeDiff))
	h("POST /api/v1/changesets/{id}/apply", s.requireChanges(s.handleApplyChange))
	h("POST /api/v1/changesets/{id}/undo-preview", s.requireChanges(s.handleUndoChange))
	h("POST /api/v1/changesets/{id}/cancel", s.requireChanges(s.handleCancelChange))
	h("POST /api/v1/changesets/{id}/reconcile", s.requireChanges(s.handleReconcileChange))
	h("DELETE /api/v1/drafts/{id}", s.requireWorkbench(s.handleDeleteDraft))
	h("GET /api/v1/sessions/{id}/artifacts", s.requireWorkbench(s.handleListSessionArtifacts))
	h("POST /api/v1/sessions/{id}/artifacts", s.requireWorkbench(s.handleCreateArtifact))
	h("GET /api/v1/projects/{id}/artifacts", s.requireWorkbench(s.handleListProjectArtifacts))
	h("GET /api/v1/artifacts/{id}", s.requireWorkbench(s.handleGetArtifact))
	h("GET /api/v1/artifacts/{id}/revisions", s.requireWorkbench(s.handleListArtifactRevisions))
	h("POST /api/v1/artifacts/{id}/revisions", s.requireWorkbench(s.handleCreateArtifactRevision))
	h("GET /api/v1/artifacts/{id}/revisions/{revision_id}", s.requireWorkbench(s.handleGetArtifactRevision))
	h("GET /api/v1/artifacts/{id}/revisions/{revision_id}/content", s.requireWorkbench(s.handleArtifactContent))
	h("GET /api/v1/artifacts/{id}/revisions/{revision_id}/download", s.requireWorkbench(s.handleArtifactDownload))

	// runs
	h("GET /api/v1/runs/{id}", s.handleGetRun)
	h("POST /api/v1/runs/{id}/cancel", s.handleCancelRun)

	// approvals
	h("GET /api/v1/approvals", s.handleListApprovals)
	h("POST /api/v1/approvals/{id}/decision", s.handleDecideApproval)

	// jobs
	h("GET /api/v1/jobs", s.handleListJobs)
	h("POST /api/v1/jobs", s.handleCreateJob)
	h("GET /api/v1/jobs/{id}", s.handleGetJob)
	h("PATCH /api/v1/jobs/{id}", s.handleUpdateJob)
	h("DELETE /api/v1/jobs/{id}", s.handleDeleteJob)
	h("POST /api/v1/jobs/{id}/run", s.handleRunJob)

	// extensions
	h("GET /api/v1/extensions", s.handleListExtensions)
	h("POST /api/v1/extensions", s.handleInstallExtension)
	h("GET /api/v1/extensions/{id}", s.handleGetExtension)
	h("PATCH /api/v1/extensions/{id}", s.handleUpdateExtension)
	h("DELETE /api/v1/extensions/{id}", s.handleDeleteExtension)
	h("GET /api/v1/extensions/{id}/status", s.handleExtensionStatus)
	h("POST /api/v1/extensions/{id}/accept-tools", s.handleAcceptExtensionTools)

	// channels
	h("GET /api/v1/channels", s.handleListChannels)
	h("POST /api/v1/channels", s.handleCreateChannel)
	h("GET /api/v1/channels/{id}", s.handleGetChannel)
	h("PATCH /api/v1/channels/{id}", s.handleUpdateChannel)
	h("DELETE /api/v1/channels/{id}", s.handleDeleteChannel)
	h("POST /api/v1/channels/{id}/reload", s.handleReloadChannel)

	// events / settings / audit
	h("GET /api/v1/events", s.handleEvents)
	h("GET /api/v1/settings", s.handleGetSettings)
	h("PATCH /api/v1/settings", s.handleUpdateSettings)
	h("POST /api/v1/agents/{id}/prompt-enhancement", s.handleEnhancePrompt)
	h("GET /api/v1/agents/{id}/files", s.previewHandler(s.handlePreviewFiles))
	h("GET /api/v1/agents/{id}/file-preview", s.previewHandler(s.handleFilePreview))
	h("GET /api/v1/agents/{id}/file-content", s.previewHandler(s.handleFileContent))
	h("GET /api/v1/agents/{id}/file-layout", s.previewHandler(s.handlePreviewConversion))
	h("GET /api/v1/resources", s.handleResources)
	h("PUT /api/v1/resources", s.handleUpdateResources)
	h("GET /api/v1/audit", s.handleListAudit)

	return s.dispatch(mux)
}

// handlerFunc 是本包处理器的形状：返回的错误由 writeError 统一映射为状态码与信封。
type handlerFunc func(w http.ResponseWriter, r *http.Request) error

func (s *Server) adapt(fn handlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := fn(w, r); err != nil {
			s.writeError(w, r, err)
		}
	})
}

// dispatch 把未匹配的请求写成统一的错误信封：路径存在但方法不对 → 405（附 Allow），否则 → 404。
func (s *Server) dispatch(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pattern := mux.Handler(r); pattern != "" {
			mux.ServeHTTP(w, r)
			return
		}
		var allowed []string
		for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete} {
			if m == r.Method {
				continue
			}
			probe := r.WithContext(r.Context())
			probe.Method = m
			if _, p := mux.Handler(probe); p != "" {
				allowed = append(allowed, m)
			}
		}
		if len(allowed) > 0 {
			w.Header().Set("Allow", strings.Join(allowed, ", "))
			s.writeErrorStatus(w, r, http.StatusMethodNotAllowed, &model.Error{
				Code:    model.CodeInvalid,
				Message: "不支持的请求方法",
				Details: map[string]any{"reason": "method_not_allowed", "method": r.Method, "allowed": allowed},
			})
			return
		}
		s.writeError(w, r, &model.Error{
			Code:    model.CodeNotFound,
			Message: "路由不存在",
			Details: map[string]any{"kind": "route", "path": r.URL.Path},
		})
	})
}

// validateDeps 检查契约要求非 nil 的依赖（Logger、Bus、Gateway、Channels 可为 nil）。
func validateDeps(d Deps) error {
	missing := make([]string, 0, 4)
	check := func(name string, isNil bool) {
		if isNil {
			missing = append(missing, name)
		}
	}
	check("Auth", d.Auth == nil)
	check("Store", d.Store == nil)
	check("Secrets", d.Secrets == nil)
	check("Agents", d.Agents == nil)
	check("Providers", d.Providers == nil)
	check("Sessions", d.Sessions == nil)
	check("Permissions", d.Permissions == nil)
	check("Jobs", d.Jobs == nil)
	check("Extensions", d.Extensions == nil)
	check("Tools", d.Tools == nil)
	check("Workspaces", d.Workspaces == nil)
	check("Engine", d.Engine == nil)
	if len(missing) > 0 {
		return &model.Error{
			Code:    model.CodeInvalid,
			Message: "server.Deps 缺少必需的依赖",
			Details: map[string]any{"missing": missing},
		}
	}
	return nil
}

// withDefaults 填充 Options 的零值字段。
func withDefaults(o Options) Options {
	if strings.TrimSpace(o.ListenAddr) == "" {
		o.ListenAddr = config.DefaultListenAddr
	}
	if o.MaxBodyBytes <= 0 {
		o.MaxBodyBytes = defaultMaxBodyBytes
	}
	if o.MaxUploadBytes <= 0 {
		o.MaxUploadBytes = defaultMaxUploadBytes
	}
	if o.StartedAt == "" {
		o.StartedAt = time.Now().UTC().Format(time.RFC3339)
	}
	o.AllowedOrigins = append([]string(nil), o.AllowedOrigins...)
	return o
}

// checkLoopbackAddr 要求监听地址格式正确、端口合法，且主机只绑定回环接口。
// 回环判定用 config.IsLoopbackAddr（全仓唯一的规则：127.0.0.0/8、::1、localhost），与组合根一致。
func checkLoopbackAddr(addr string) error {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return &model.Error{
			Code:    model.CodeInvalid,
			Message: "监听地址格式不正确",
			Details: map[string]any{"addr": addr},
			Err:     err,
		}
	}
	if n, perr := strconv.Atoi(port); perr != nil || n < 0 || n > 65535 {
		return &model.Error{
			Code:    model.CodeInvalid,
			Message: "监听端口不正确",
			Details: map[string]any{"addr": addr},
		}
	}
	if !config.IsLoopbackAddr(addr) {
		return &model.Error{
			Code:    model.CodeInvalid,
			Message: "只允许监听回环地址（127.0.0.0/8、::1 或 localhost）",
			Details: map[string]any{"addr": addr},
		}
	}
	return nil
}
