package servertest

import (
	"context"
	"errors"

	"github.com/GoGoslingCA/gogosling/internal/config"
	"github.com/GoGoslingCA/gogosling/internal/i18n"
	"github.com/GoGoslingCA/gogosling/internal/identity"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/permission"
	"github.com/GoGoslingCA/gogosling/internal/secret"
	"github.com/GoGoslingCA/gogosling/internal/tools"
	"github.com/GoGoslingCA/gogosling/internal/workspace"
)

// 本文件是组合根（internal/app）里几个适配器的测试替身。按依赖规则传输层不能导入组合根，
// 所以这里另写一份；它们只用于测试装配，不是生产实现。为了让测试结论能代表生产装配，
// 行为与 app 中的对应实现保持一致（app/adapters_secret.go、app/adapters_tools.go）：
//   - secretAdapter：错误映射与 app.secretAdapter 相同（含 ErrTooLarge、ctx、删除不存在视为成功）；
//   - runResolver：与 app.runContextResolver 相同，channel 起源的会话标记为不可信输入；
//   - permChecker / toolWorkspace：纯字段转换。
// 修改 app 中的适配器时请同步这里（servertest 的 TestAdaptersMirrorComposition 覆盖了关键差异点）。

// secretAdapter 把 secret 包的哨兵错误映射为 model.Code，满足 provider.SecretStore 与 channel.Secrets。
type secretAdapter struct{ s secret.Store }

// mapSecretErr 与 app.mapSecretErr 的映射一致；消息只含凭据名，不含凭据值。
func mapSecretErr(name string, err error) error {
	if err == nil {
		return nil
	}
	details := map[string]any{"secret": name}
	var me *model.Error
	switch {
	case errors.As(err, &me):
		return err
	case errors.Is(err, context.Canceled):
		return &model.Error{Code: model.CodeCancelled, Message: "凭据操作被取消", Details: details, Err: err}
	case errors.Is(err, context.DeadlineExceeded):
		return &model.Error{Code: model.CodeTimeout, Message: "凭据操作超时", Details: details, Err: err}
	case errors.Is(err, secret.ErrNotFound):
		return &model.Error{Code: model.CodeNotFound, Message: "凭据不存在", Details: details, Err: err}
	case errors.Is(err, secret.ErrInvalidName):
		return &model.Error{Code: model.CodeInvalid, Message: "凭据名称不合法", Details: details, Err: err}
	case errors.Is(err, secret.ErrTooLarge):
		return &model.Error{Code: model.CodeInvalid, Message: "凭据内容过大", Details: details, Err: err}
	case errors.Is(err, secret.ErrBackendUnavailable):
		details["env"] = config.EnvSecretBackend
		return &model.Error{Code: model.CodeUnavailable, Message: "数据目录记录的凭据后端当前不可用", Details: details, Err: err}
	case errors.Is(err, secret.ErrCorrupt), errors.Is(err, secret.ErrKeyMissing):
		return &model.Error{Code: model.CodeUnavailable, Message: "凭据存储已损坏或缺少密钥", Details: details, Err: err}
	default:
		return &model.Error{Code: model.CodeUnavailable, Message: "凭据存储不可用", Details: details, Err: err}
	}
}

func (a secretAdapter) Get(ctx context.Context, ref string) (string, error) {
	v, err := a.s.Get(ctx, ref)
	if err != nil {
		return "", mapSecretErr(ref, err)
	}
	return v, nil
}

func (a secretAdapter) Set(ctx context.Context, ref, value string) error {
	return mapSecretErr(ref, a.s.Set(ctx, ref, value))
}

// Delete 删除凭据；不存在视为成功（与生产一致）。
func (a secretAdapter) Delete(ctx context.Context, ref string) error {
	err := a.s.Delete(ctx, ref)
	if errors.Is(err, secret.ErrNotFound) {
		return nil
	}
	return mapSecretErr(ref, err)
}

func (a secretAdapter) Has(ctx context.Context, ref string) (bool, error) {
	ok, err := a.s.Has(ctx, ref)
	if err != nil {
		return false, mapSecretErr(ref, err)
	}
	return ok, nil
}

// permChecker 把 *permission.Service 适配为 tools.PermissionChecker。
type permChecker struct{ p *permission.Service }

func (c permChecker) Check(ctx context.Context, req tools.CheckRequest) (tools.Verdict, error) {
	v, err := c.p.Check(ctx, permission.Request{
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
	})
	return tools.Verdict{Allowed: v.Allowed, Reason: v.Reason, Source: string(v.Source)}, err
}

// toolWorkspace 把 *workspace.Workspace 适配为 tools.Workspace（Entry 类型转换）。
type toolWorkspace struct{ *workspace.Workspace }

func (w toolWorkspace) List(ctx context.Context, rel string) ([]tools.Entry, error) {
	list, err := w.Workspace.List(ctx, rel)
	if err != nil {
		return nil, err
	}
	out := make([]tools.Entry, 0, len(list))
	for _, e := range list {
		out = append(out, tools.Entry(e))
	}
	return out, nil
}

func (w toolWorkspace) Stat(ctx context.Context, rel string) (tools.Entry, error) {
	e, err := w.Workspace.Stat(ctx, rel)
	return tools.Entry(e), err
}

// runResolver 按运行 ID 解析工具执行所需的上下文：运行 → 会话 → 智能体 → 工作区。
// 与生产一致：channel 起源的会话整段视为含不可信输入（Untrusted），权限层据此收紧。
type runResolver struct {
	store      model.Store
	workspaces *workspace.Manager
}

func (r runResolver) ResolveRunContext(ctx context.Context, runID string) (tools.RunContext, error) {
	run, err := r.store.GetRun(ctx, runID)
	if err != nil {
		return tools.RunContext{}, err
	}
	sess, err := r.store.GetSession(ctx, run.SessionID)
	if err != nil {
		return tools.RunContext{}, err
	}
	a, err := r.store.GetAgent(ctx, sess.AgentID)
	if err != nil {
		return tools.RunContext{}, err
	}
	// 与生产组合根相同：正在执行或等待恢复的人工操作阻止同智能体模型工具。
	if operations, ok := r.store.(model.OperationRepo); ok {
		items, e := operations.ListOperations(ctx, model.OperationFilter{AgentID: a.ID, ActiveOnly: true}, model.Page{Limit: 200})
		if e != nil {
			return tools.RunContext{}, e
		}
		for _, op := range items {
			if op.Status == model.OperationExecuting || op.Status == model.OperationRecoveryRequired {
				return tools.RunContext{}, model.NewError(model.CodeConflict, i18n.T("en", "workbench.change_busy", nil))
			}
		}
	}
	ws, err := r.workspaces.Open(ctx, a.ID, a.WorkspaceDir)
	if err != nil {
		return tools.RunContext{}, err
	}
	return tools.RunContext{
		RunID:     run.ID,
		SessionID: sess.ID,
		Agent:     a,
		Workspace: toolWorkspace{ws},
		Principal: identity.LocalPrincipal(),
		Untrusted: sess.Origin == model.OriginChannel,
	}, nil
}

// noopRunner 是 scheduler.Runner 的空实现：测试只关心触发是否被受理。
type noopRunner struct{}

func (noopRunner) RunJob(context.Context, *model.Job) error { return nil }
