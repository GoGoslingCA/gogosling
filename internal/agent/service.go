package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/GoGoslingCA/gogosling/internal/event"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

// 事件类型；Payload 为 *model.Agent（deleted 事件也携带删除前的记录）。
const (
	EventCreated = "agent.created"
	EventUpdated = "agent.updated"
	EventDeleted = "agent.deleted"
)

const (
	// SoulFile 是工作区根目录下保存人格提示的文件名；它与 Agent.SystemPrompt 保持同步。
	SoulFile = "SOUL.md"
	// MaxNameLength 是智能体名称的最大字符数。
	MaxNameLength = 64
	// MaxToolNameLength 是单个工具名的最大字节数。
	MaxToolNameLength = 64
	// maxSoulBytes 是从已有工作区读取 SOUL.md 时的大小上限。
	maxSoulBytes = 256 << 10
)

// DefaultTools 返回新建智能体的默认内置工具集（返回副本）。
// 名称与 tools 包的内置工具一一对应；本包只负责记录选择，不负责执行。
func DefaultTools() []string {
	return []string{"read_file", "read_document", "write_file", "edit_file", "delete_file", "list_dir", "search_files", "run_command", "fetch_url"}
}

// ProviderChecker 用于校验模型引用指向的供应商存在；provider.Service 直接满足该接口。
type ProviderChecker interface {
	ProviderExists(ctx context.Context, id string) (bool, error)
}

// Workspace 是本包对工作区的最小依赖；workspace.Workspace 的同名方法签名一致。
type Workspace interface {
	Root() string
	Exists(ctx context.Context, rel string) (bool, error)
	ReadFile(ctx context.Context, rel string, maxBytes int64) ([]byte, error)
	WriteFile(ctx context.Context, rel string, data []byte) error
}

// WorkspaceOpener 打开（必要时创建）智能体工作区。
// workspace.Manager.Open 返回具体类型，组合根用 WorkspaceOpenerFunc 做一行适配。
type WorkspaceOpener interface {
	Open(ctx context.Context, agentID, workspaceDir string) (Workspace, error)
}

// WorkspaceOpenerFunc 让普通函数满足 WorkspaceOpener。
type WorkspaceOpenerFunc func(ctx context.Context, agentID, workspaceDir string) (Workspace, error)

// Open 实现 WorkspaceOpener。
func (f WorkspaceOpenerFunc) Open(ctx context.Context, agentID, workspaceDir string) (Workspace, error) {
	return f(ctx, agentID, workspaceDir)
}

// CreateInput 是创建智能体的输入。Name 与 Model 必填，其余可留空取默认值或模板值。
type CreateInput struct {
	Name         string
	Description  string
	SystemPrompt string
	Model        model.ModelRef
	// Tools 为 nil 时使用模板或 DefaultTools；空切片表示不启用任何内置工具。
	Tools      []string
	Extensions []string
	// Autonomy 为空时使用模板值，再回退到 model.DefaultAutonomy。
	Autonomy model.AutonomyLevel
	// WorkspaceDir 为空时使用智能体 id（相对 <dataDir>/agents/）。
	WorkspaceDir string
	// Template 是模板标识；SystemPrompt、Tools、Autonomy 未显式给出时取模板值。
	Template string
	// Enabled 为 nil 时默认启用。
	Enabled *bool
}

// UpdateInput 是部分更新的输入：nil 字段保持不变。
// Tools / Extensions 指向 nil 切片时重置为默认值 / 清空。
// Template 只更新记录上的模板标识（必须存在或为空），不会重新应用模板内容。
type UpdateInput struct {
	Name         *string
	Description  *string
	SystemPrompt *string
	Model        *model.ModelRef
	Tools        *[]string
	Extensions   *[]string
	Autonomy     *model.AutonomyLevel
	Template     *string
	Enabled      *bool
}

// SessionCleaner 在删除智能体之前删除它名下的全部会话（期望由 session.Service 实现）。
//
// 外键级联也会删掉这些会话，但不会发布 session.deleted，也不检查在途运行；因此 Delete 先调用它：
// 有在途运行时实现方返回 CodeConflict（Details 含 run_id），Delete 原样返回、不删除智能体。
type SessionCleaner interface {
	DeleteByAgent(ctx context.Context, agentID string) error
}

// Service 管理智能体定义的生命周期。并发安全：没有共享可变状态。
type Service struct {
	repo       model.AgentRepo
	providers  ProviderChecker
	workspaces WorkspaceOpener
	bus        *event.Bus
	log        *slog.Logger
	templates  *Library
	sessions   SessionCleaner
	now        func() time.Time
}

// Option 配置 Service 的可选依赖。
type Option func(*Service)

// WithTemplates 替换模板库（默认使用内置模板）。
func WithTemplates(lib *Library) Option {
	return func(s *Service) { s.templates = lib }
}

// WithSessionCleaner 注入会话清理能力；未注入时 Delete 只删记录（会话由仓储级联删除，不发布 session.deleted）。
func WithSessionCleaner(c SessionCleaner) Option {
	return func(s *Service) { s.sessions = c }
}

// WithClock 替换时间源（测试用）。
func WithClock(now func() time.Time) Option {
	return func(s *Service) { s.now = now }
}

// New 创建智能体服务。未通过 WithTemplates 指定模板库时加载内置模板；加载失败返回错误。
// repo、providers、workspaces 必须提供；bus 可为 nil（不发事件）；logger 为 nil 时丢弃日志。
func New(repo model.AgentRepo, providers ProviderChecker, workspaces WorkspaceOpener, bus *event.Bus, logger *slog.Logger, opts ...Option) (*Service, error) {
	if repo == nil || providers == nil || workspaces == nil {
		return nil, model.NewError(model.CodeInternal, "agent: 缺少仓储、供应商检查或工作区依赖")
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	s := &Service{repo: repo, providers: providers, workspaces: workspaces, bus: bus, log: logger, now: time.Now}
	for _, opt := range opts {
		opt(s)
	}
	if s.templates == nil {
		lib, err := BuiltinTemplates()
		if err != nil {
			return nil, err
		}
		s.templates = lib
	}
	return s, nil
}

// Templates 返回当前使用的模板库。
func (s *Service) Templates() *Library { return s.templates }

// Create 校验输入、创建工作区并写入 SOUL.md、持久化记录，最后发布 agent.created。
//
// SOUL.md 的处理：显式给出 SystemPrompt 时写入该内容；否则若工作区里已有 SOUL.md
// （用户指向了已有目录），采用文件内容作为 SystemPrompt；两者都没有时写入模板或内置默认人格。
// 记录创建失败时工作区目录会保留（无害，且避免误删用户指定的目录）。
func (s *Service) Create(ctx context.Context, in CreateInput) (*model.Agent, error) {
	if err := checkCtx(ctx); err != nil {
		return nil, err
	}
	name, err := validateName(in.Name)
	if err != nil {
		return nil, err
	}
	var tpl *Template
	if id := strings.TrimSpace(in.Template); id != "" {
		found, ok := s.templates.Get(id)
		if !ok {
			return nil, model.Errorf(model.CodeInvalid, "模板 %q 不存在", id)
		}
		tpl = &found
	}

	explicitPrompt := strings.TrimSpace(in.SystemPrompt)
	systemPrompt := explicitPrompt
	tools := in.Tools
	autonomy := in.Autonomy
	if tpl != nil {
		if systemPrompt == "" {
			systemPrompt = tpl.SystemPrompt
		}
		if tools == nil && tpl.Tools != nil {
			tools = append([]string(nil), tpl.Tools...)
		}
		if autonomy == "" {
			autonomy = tpl.Autonomy
		}
	}
	if autonomy == "" {
		autonomy = model.DefaultAutonomy
	}
	if err := validateAutonomy(autonomy); err != nil {
		return nil, err
	}
	if tools == nil {
		tools = DefaultTools()
	}
	if tools, err = normalizeTools(tools); err != nil {
		return nil, err
	}
	extensions, err := normalizeExtensions(in.Extensions)
	if err != nil {
		return nil, err
	}
	if err := s.checkModel(ctx, in.Model); err != nil {
		return nil, err
	}

	now := s.now().UTC()
	a := &model.Agent{
		ID:           model.NewID(),
		Name:         name,
		Description:  strings.TrimSpace(in.Description),
		SystemPrompt: systemPrompt,
		Model:        in.Model,
		Tools:        tools,
		Extensions:   extensions,
		Autonomy:     autonomy,
		WorkspaceDir: strings.TrimSpace(in.WorkspaceDir),
		Enabled:      true,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if tpl != nil {
		a.Template = tpl.ID
	}
	if a.WorkspaceDir == "" {
		a.WorkspaceDir = a.ID
	}
	if in.Enabled != nil {
		a.Enabled = *in.Enabled
	}

	ws, err := s.workspaces.Open(ctx, a.ID, a.WorkspaceDir)
	if err != nil {
		return nil, fmt.Errorf("agent: 打开工作区: %w", err)
	}
	if err := s.seedSoul(ctx, ws, a, explicitPrompt != ""); err != nil {
		return nil, fmt.Errorf("agent: 初始化 %s: %w", SoulFile, err)
	}
	if err := s.repo.CreateAgent(ctx, a); err != nil {
		s.log.Warn("智能体记录创建失败，工作区目录保留", "agent_id", a.ID, "workspace", ws.Root(), "error", err)
		return nil, fmt.Errorf("agent: 创建智能体: %w", err)
	}
	s.publish(EventCreated, a)
	return a, nil
}

// seedSoul 按 Create 注释中的规则初始化 SOUL.md，并在采用已有文件时回填 a.SystemPrompt。
func (s *Service) seedSoul(ctx context.Context, ws Workspace, a *model.Agent, explicit bool) error {
	exists, err := ws.Exists(ctx, SoulFile)
	if err != nil {
		return err
	}
	if exists && !explicit {
		data, err := ws.ReadFile(ctx, SoulFile, maxSoulBytes)
		if err != nil {
			return err
		}
		if text := strings.TrimSpace(string(data)); text != "" {
			a.SystemPrompt = text
		}
		return nil
	}
	return writeSoul(ctx, ws, a)
}

// writeSoul 把当前 SystemPrompt（为空时用内置默认人格）写入 SOUL.md。
func writeSoul(ctx context.Context, ws Workspace, a *model.Agent) error {
	content := a.SystemPrompt
	if content == "" {
		content = DefaultSoul(a.Name)
	}
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return ws.WriteFile(ctx, SoulFile, []byte(content))
}

// Update 做部分更新并发布 agent.updated。SystemPrompt 改变时同步重写 SOUL.md；
// 数据库是事实来源，文件同步失败只记录警告。WorkspaceDir 创建后不可修改。
func (s *Service) Update(ctx context.Context, id string, in UpdateInput) (*model.Agent, error) {
	if err := checkCtx(ctx); err != nil {
		return nil, err
	}
	current, err := s.repo.GetAgent(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("agent: 读取智能体: %w", err)
	}
	next := *current
	next.Tools = append([]string(nil), current.Tools...)
	next.Extensions = append([]string(nil), current.Extensions...)

	if in.Name != nil {
		if next.Name, err = validateName(*in.Name); err != nil {
			return nil, err
		}
	}
	if in.Description != nil {
		next.Description = strings.TrimSpace(*in.Description)
	}
	if in.SystemPrompt != nil {
		next.SystemPrompt = strings.TrimSpace(*in.SystemPrompt)
	}
	if in.Model != nil {
		if err := s.checkModel(ctx, *in.Model); err != nil {
			return nil, err
		}
		next.Model = *in.Model
	}
	if in.Tools != nil {
		tools := *in.Tools
		if tools == nil {
			tools = DefaultTools()
		}
		if next.Tools, err = normalizeTools(tools); err != nil {
			return nil, err
		}
	}
	if in.Extensions != nil {
		if next.Extensions, err = normalizeExtensions(*in.Extensions); err != nil {
			return nil, err
		}
	}
	if in.Autonomy != nil {
		if err := validateAutonomy(*in.Autonomy); err != nil {
			return nil, err
		}
		next.Autonomy = *in.Autonomy
	}
	if in.Template != nil {
		tplID := strings.TrimSpace(*in.Template)
		if tplID != "" {
			if _, ok := s.templates.Get(tplID); !ok {
				return nil, model.Errorf(model.CodeInvalid, "模板 %q 不存在", tplID)
			}
		}
		next.Template = tplID
	}
	if in.Enabled != nil {
		next.Enabled = *in.Enabled
	}
	next.UpdatedAt = s.now().UTC()

	if err := s.repo.UpdateAgent(ctx, &next); err != nil {
		return nil, fmt.Errorf("agent: 更新智能体: %w", err)
	}
	if in.SystemPrompt != nil && next.SystemPrompt != current.SystemPrompt {
		s.syncSoul(ctx, &next)
	}
	s.publish(EventUpdated, &next)
	return &next, nil
}

// syncSoul 尽力把 SystemPrompt 写回工作区的 SOUL.md。
func (s *Service) syncSoul(ctx context.Context, a *model.Agent) {
	ws, err := s.workspaces.Open(ctx, a.ID, a.WorkspaceDir)
	if err == nil {
		err = writeSoul(ctx, ws, a)
	}
	if err != nil {
		s.log.Warn("同步 SOUL.md 失败", "agent_id", a.ID, "error", err)
	}
}

// Get 返回智能体记录。
func (s *Service) Get(ctx context.Context, id string) (*model.Agent, error) {
	a, err := s.repo.GetAgent(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("agent: 读取智能体: %w", err)
	}
	return a, nil
}

// List 返回全部智能体，按创建时间升序。
func (s *Service) List(ctx context.Context) ([]*model.Agent, error) {
	items, err := s.repo.ListAgents(ctx)
	if err != nil {
		return nil, fmt.Errorf("agent: 列出智能体: %w", err)
	}
	return items, nil
}

// Delete 删除智能体记录并发布 agent.deleted。工作区文件保留（契约 DELETE /agents/{id}）。
//
// 注入了 SessionCleaner 时先由它删除该智能体的全部会话并逐个发布 session.deleted；
// 它返回 CodeConflict（有在途运行）时原样返回，智能体不被删除。任务、通道等其余从属记录
// 由仓储的外键级联删除，调度器与网关的内存状态由组合根在收到 agent.deleted 后清理。
func (s *Service) Delete(ctx context.Context, id string) error {
	if err := checkCtx(ctx); err != nil {
		return err
	}
	a, err := s.repo.GetAgent(ctx, id)
	if err != nil {
		return fmt.Errorf("agent: 读取智能体: %w", err)
	}
	if s.sessions != nil {
		if err := s.sessions.DeleteByAgent(ctx, id); err != nil {
			if model.IsCode(err, model.CodeConflict) {
				return err
			}
			return fmt.Errorf("agent: 删除智能体的会话: %w", err)
		}
	}
	if err := s.repo.DeleteAgent(ctx, id); err != nil {
		return fmt.Errorf("agent: 删除智能体: %w", err)
	}
	s.publish(EventDeleted, a)
	return nil
}

// checkModel 校验模型引用完整且供应商存在。
func (s *Service) checkModel(ctx context.Context, ref model.ModelRef) error {
	if strings.TrimSpace(ref.ProviderID) == "" || strings.TrimSpace(ref.Model) == "" {
		return model.NewError(model.CodeInvalid, "必须指定模型供应商与模型名")
	}
	ok, err := s.providers.ProviderExists(ctx, ref.ProviderID)
	if err != nil {
		return fmt.Errorf("agent: 检查供应商: %w", err)
	}
	if !ok {
		return &model.Error{
			Code:    model.CodeInvalid,
			Message: "模型供应商不存在",
			Details: map[string]any{"provider_id": ref.ProviderID},
		}
	}
	return nil
}

func (s *Service) publish(typ string, a *model.Agent) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(event.Event{Type: typ, AgentID: a.ID, Payload: a})
}

// DefaultSoul 返回没有模板也没有系统提示时写入 SOUL.md 的内置人格。
func DefaultSoul(name string) string {
	return "# " + name + "\n\n" +
		"你是 " + name + "，一个在用户本机运行的 AI 助手。\n\n" +
		"- 回答直接、具体：先给结论，再给依据。\n" +
		"- 不确定时说明不确定，并提出如何验证。\n" +
		"- 修改文件或执行命令前先说明意图；涉及工作区之外的操作先征得同意。\n" +
		"- 把需要长期记住的偏好写回本文件。\n"
}

func validateName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", model.NewError(model.CodeInvalid, "智能体名称不能为空")
	}
	if utf8.RuneCountInString(name) > MaxNameLength {
		return "", model.Errorf(model.CodeInvalid, "智能体名称不能超过 %d 个字符", MaxNameLength)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", model.NewError(model.CodeInvalid, "智能体名称包含控制字符")
		}
	}
	return name, nil
}

func validateAutonomy(level model.AutonomyLevel) error {
	switch level {
	case model.AutonomyAskAll, model.AutonomyAskRisky, model.AutonomyAutoInSandbox:
		return nil
	}
	return model.Errorf(model.CodeInvalid, "不支持的自主等级 %q", string(level))
}

// normalizeTools 去空白、去重并校验工具名格式；返回新切片（空输入返回空切片而不是 nil）。
func normalizeTools(tools []string) ([]string, error) {
	out := make([]string, 0, len(tools))
	seen := make(map[string]struct{}, len(tools))
	for _, raw := range tools {
		name := strings.TrimSpace(raw)
		if name == "" {
			return nil, model.NewError(model.CodeInvalid, "工具名不能为空")
		}
		if len(name) > MaxToolNameLength || strings.ContainsFunc(name, unicode.IsSpace) {
			return nil, model.Errorf(model.CodeInvalid, "工具名 %q 不合法", name)
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out, nil
}

// normalizeExtensions 去空白、去重；nil 输入返回 nil。
func normalizeExtensions(ids []string) ([]string, error) {
	if ids == nil {
		return nil, nil
	}
	out := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" {
			return nil, model.NewError(model.CodeInvalid, "扩展标识不能为空")
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, nil
}

// checkCtx 把已经结束的 ctx 转成带错误码的错误。
func checkCtx(ctx context.Context) error {
	err := ctx.Err()
	if err == nil {
		return nil
	}
	code := model.CodeCancelled
	if errors.Is(err, context.DeadlineExceeded) {
		code = model.CodeTimeout
	}
	return model.Wrap(code, "操作未完成", err)
}
