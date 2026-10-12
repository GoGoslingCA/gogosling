package app

import (
	"context"
	"strings"
	"sync"

	"github.com/GoGoslingCA/gogosling/internal/extension"
	"github.com/GoGoslingCA/gogosling/internal/i18n"
	"github.com/GoGoslingCA/gogosling/internal/identity"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/permission"
	"github.com/GoGoslingCA/gogosling/internal/tools"
	"github.com/GoGoslingCA/gogosling/internal/workspace"
)

// 本文件把 tools 包的消费者接口接到 permission、workspace、session、extension 的实现上。

// permissionChecker 是 *permission.Service 的判定能力。
type permissionChecker interface {
	Check(ctx context.Context, req permission.Request) (permission.Verdict, error)
}

// permissionAdapter 实现 tools.PermissionChecker。
type permissionAdapter struct {
	p permissionChecker
}

// Check 逐字段转换请求与判定结果。permission 在 ctx 结束时同时返回已填充的 Verdict 与
// CodeCancelled / CodeTimeout 错误，这里原样转交，tools 据此把取消向上抛给 engine（-32003）。
func (a permissionAdapter) Check(ctx context.Context, req tools.CheckRequest) (tools.Verdict, error) {
	v, err := a.p.Check(ctx, toPermissionRequest(req))
	return tools.Verdict{Allowed: v.Allowed, Reason: v.Reason, Source: string(v.Source)}, err
}

// toPermissionRequest 是 tools.CheckRequest → permission.Request 的字段映射（两者一一对应）。
func toPermissionRequest(req tools.CheckRequest) permission.Request {
	return permission.Request{
		Principal:      req.Principal,
		AgentID:        req.AgentID,
		Autonomy:       req.Autonomy,
		SessionID:      req.SessionID,
		RunID:          req.RunID,
		OperationID:    req.OperationID,
		Tool:           req.Tool,
		Call:           req.Call,
		Workspace:      req.Workspace,
		Summary:        req.Summary,
		Details:        req.Details,
		UntrustedInput: req.UntrustedInput,
		Isolated:       req.Isolated,
	}
}

// workspaceAdapter 让 *workspace.Workspace 满足 tools.Workspace（只差 Entry 类型）。
type workspaceAdapter struct {
	ws *workspace.Workspace
}

var _ tools.WorkspaceRemover = workspaceAdapter{}

func (w workspaceAdapter) Root() string { return w.ws.Root() }

func (w workspaceAdapter) Resolve(rel string) (string, error) { return w.ws.Resolve(rel) }

func (w workspaceAdapter) ReadFile(ctx context.Context, rel string, maxBytes int64) ([]byte, error) {
	return w.ws.ReadFile(ctx, rel, maxBytes)
}

func (w workspaceAdapter) WriteFile(ctx context.Context, rel string, data []byte) error {
	return w.ws.WriteFile(ctx, rel, data)
}

func (w workspaceAdapter) Remove(ctx context.Context, rel string) error {
	return w.ws.Remove(ctx, rel)
}

func (w workspaceAdapter) List(ctx context.Context, rel string) ([]tools.Entry, error) {
	entries, err := w.ws.List(ctx, rel)
	if err != nil {
		return nil, err
	}
	out := make([]tools.Entry, len(entries))
	for i, e := range entries {
		out[i] = toToolsEntry(e)
	}
	return out, nil
}

func (w workspaceAdapter) Stat(ctx context.Context, rel string) (tools.Entry, error) {
	e, err := w.ws.Stat(ctx, rel)
	if err != nil {
		return tools.Entry{}, err
	}
	return toToolsEntry(e), nil
}

// toToolsEntry 是 workspace.Entry → tools.Entry 的字段映射（同形结构）。
func toToolsEntry(e workspace.Entry) tools.Entry {
	return tools.Entry{Name: e.Name, Path: e.Path, Size: e.Size, IsDir: e.IsDir, ModTime: e.ModTime}
}

// runLookup 是 *session.Service 按运行找会话的能力。
type runLookup interface {
	GetRun(ctx context.Context, runID string) (*model.Run, error)
	Get(ctx context.Context, id string) (*model.Session, error)
}

// workspaceOpener 是 *workspace.Manager 的打开能力。
type workspaceOpener interface {
	Open(ctx context.Context, agentID, workspaceDir string) (*workspace.Workspace, error)
}

// runContextResolver 实现 tools.RunContextResolver：Run → Session → Agent → Workspace。
//
// tools.Executor 必须在 session.Service 之前构造（session 依赖它提供工具声明），而解析运行上下文
// 又需要 session；因此 sessions 在 session.Service 建好后通过 bind 延迟注入。工具调用只会在运行
// 存在之后到达，绑定之前的调用返回 CodeUnavailable。
type runContextResolver struct {
	agents     agentGetter
	workspaces workspaceOpener
	operations model.OperationRepo

	mu       sync.RWMutex
	sessions runLookup
}

// bind 注入会话服务。
func (r *runContextResolver) bind(s runLookup) {
	r.mu.Lock()
	r.sessions = s
	r.mu.Unlock()
}

// ResolveRunContext 解析运行上下文。
//
// Principal 是本机用户（运行由本机用户或内核自身发起；IM 对端的身份记录在用户消息 Meta 中）；
// 会话起源为 channel 时整段会话视为含不可信输入，权限层据此收紧。
func (r *runContextResolver) ResolveRunContext(ctx context.Context, runID string) (tools.RunContext, error) {
	r.mu.RLock()
	sessions := r.sessions
	r.mu.RUnlock()
	if sessions == nil {
		return tools.RunContext{}, model.NewError(model.CodeUnavailable, "会话服务尚未就绪")
	}
	run, err := sessions.GetRun(ctx, runID)
	if err != nil {
		return tools.RunContext{}, err
	}
	sess, err := sessions.Get(ctx, run.SessionID)
	if err != nil {
		return tools.RunContext{}, err
	}
	agent, err := r.agents.Get(ctx, sess.AgentID)
	if err != nil {
		return tools.RunContext{}, err
	}
	if r.operations != nil {
		ops, e := r.operations.ListOperations(ctx, model.OperationFilter{AgentID: agent.ID, ActiveOnly: true}, model.Page{Limit: 200})
		if e != nil {
			return tools.RunContext{}, e
		}
		for _, op := range ops {
			if op.Status == model.OperationExecuting || op.Status == model.OperationRecoveryRequired {
				return tools.RunContext{}, model.NewError(model.CodeConflict, i18n.T("en", "workbench.change_busy", nil))
			}
		}
	}
	ws, err := r.workspaces.Open(ctx, agent.ID, agent.WorkspaceDir)
	if err != nil {
		return tools.RunContext{}, err
	}
	return tools.RunContext{
		RunID:     run.ID,
		SessionID: sess.ID,
		Agent:     agent,
		Workspace: workspaceAdapter{ws: ws},
		Principal: identity.LocalPrincipal(),
		Untrusted: sess.Origin == model.OriginChannel,
	}, nil
}

// skillStore 是 *extension.Manager 的技能能力。
type skillStore interface {
	SkillsFor(ctx context.Context, agent *model.Agent) ([]extension.Skill, error)
	LoadSkill(ctx context.Context, id string) (string, error)
}

// skillResourceStore 是技能引用资源的可选读取能力，保持现有技能仓储替身兼容。
type skillResourceStore interface {
	ReadSkillResource(ctx context.Context, id, path string) (string, error)
}

// skillLoaderAdapter 实现 tools.SkillLoader：只在该智能体启用的技能里按 ID 或名称（不区分大小写）查找；
// HasSkills 供 tools 判定是否向该智能体提供 load_skill。
type skillLoaderAdapter struct {
	s skillStore
}

// HasSkills 报告该智能体是否启用了至少一个技能。
func (a skillLoaderAdapter) HasSkills(ctx context.Context, agent *model.Agent) (bool, error) {
	if agent == nil {
		return false, nil
	}
	skills, err := a.s.SkillsFor(ctx, agent)
	if err != nil {
		return false, err
	}
	return len(skills) > 0, nil
}

// LoadSkill 返回技能正文；不在该智能体的技能清单里时返回 CodeNotFound。
func (a skillLoaderAdapter) LoadSkill(ctx context.Context, agent *model.Agent, name string) (string, error) {
	if agent == nil {
		return "", model.NewError(model.CodeInvalid, "缺少智能体")
	}
	skills, err := a.s.SkillsFor(ctx, agent)
	if err != nil {
		return "", err
	}
	name = strings.TrimSpace(name)
	for _, sk := range skills {
		if sk.ID == name || strings.EqualFold(sk.Name, name) {
			return a.s.LoadSkill(ctx, sk.ID)
		}
	}
	return "", &model.Error{
		Code:    model.CodeNotFound,
		Message: "技能不存在或未对该智能体启用：" + name,
		Details: map[string]any{"kind": "skill", "id": name},
	}
}

// HasSkillResources 只在底层支持资源读取且智能体选用了技能时声明资源工具。
func (a skillLoaderAdapter) HasSkillResources(ctx context.Context, agent *model.Agent) (bool, error) {
	if _, ok := a.s.(skillResourceStore); !ok {
		return false, nil
	}
	return a.HasSkills(ctx, agent)
}

// LoadSkillResource 只允许从该智能体已启用的技能清单中按 ID 或名称读取资源。
func (a skillLoaderAdapter) LoadSkillResource(ctx context.Context, agent *model.Agent, name, path string) (string, error) {
	resources, ok := a.s.(skillResourceStore)
	if !ok {
		return "", model.NewError(model.CodeUnavailable, i18n.T(i18n.LocaleZH, "file_tools.resource_unavailable", nil))
	}
	if agent == nil {
		return "", model.NewError(model.CodeInvalid, i18n.T(i18n.LocaleZH, "file_tools.resource_agent_required", nil))
	}
	skills, err := a.s.SkillsFor(ctx, agent)
	if err != nil {
		return "", err
	}
	name = strings.TrimSpace(name)
	for _, sk := range skills {
		if sk.ID == name || strings.EqualFold(sk.Name, name) {
			return resources.ReadSkillResource(ctx, sk.ID, path)
		}
	}
	return "", model.NotFound("skill", name)
}
