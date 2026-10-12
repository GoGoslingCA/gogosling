package servertest

import (
	"context"
	"encoding/json"
	"runtime"

	"github.com/GoGoslingCA/gogosling/internal/i18n"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/tools"
	"github.com/GoGoslingCA/gogosling/internal/workbench"
	"github.com/GoGoslingCA/gogosling/internal/workspace"
)

// changeFiles 将文件条件写能力接到受保护工作台，不增加直接 HTTP 写入路径。
type changeFiles struct {
	workbenchFiles
	store model.Store
}

func (a changeFiles) Supported() bool { return runtime.GOOS == "darwin" || runtime.GOOS == "linux" }
func changeSnapshot(v workspace.Snapshot) model.ChangeSnapshot {
	return model.ChangeSnapshot{Exists: v.Exists, SHA256: v.SHA256, Identity: v.Identity, RootIdentity: v.RootIdentity, Content: v.Content, Size: v.Size}
}
func workspaceSnapshot(v model.ChangeSnapshot) workspace.Snapshot {
	return workspace.Snapshot{Exists: v.Exists, SHA256: v.SHA256, Identity: v.Identity, RootIdentity: v.RootIdentity, Content: v.Content, Size: v.Size}
}
func changeReceipt(v workspace.Receipt) workbench.ChangeReceipt {
	return workbench.ChangeReceipt{State: v.State, Reason: v.Reason, Before: changeSnapshot(v.Before), After: changeSnapshot(v.After)}
}
func (a changeFiles) Snapshot(ctx context.Context, agentID, rel string, max int64) (model.ChangeSnapshot, error) {
	ws, err := a.open(ctx, agentID)
	if err != nil {
		return model.ChangeSnapshot{}, err
	}
	v, err := ws.Snapshot(ctx, rel, max)
	return changeSnapshot(v), err
}
func (a changeFiles) Apply(ctx context.Context, agentID, op, rel string, expected model.ChangeSnapshot, body *string) (workbench.ChangeReceipt, error) {
	// 不与正在运行的模型命令竞争；执行中的人工主体反过来阻止新的模型工具。
	runs, err := a.store.ListActiveRuns(ctx)
	if err != nil {
		return workbench.ChangeReceipt{}, err
	}
	for _, r := range runs {
		sess, e := a.store.GetSession(ctx, r.SessionID)
		if e != nil {
			return workbench.ChangeReceipt{}, e
		}
		if sess.AgentID == agentID {
			return workbench.ChangeReceipt{}, &model.Error{Code: model.CodeConflict, Message: i18n.T("en", "workbench.change_busy", nil), Details: map[string]any{"reason": "change_busy"}}
		}
	}
	ws, err := a.open(ctx, agentID)
	if err != nil {
		return workbench.ChangeReceipt{}, err
	}
	v, err := ws.Apply(ctx, op, rel, workspaceSnapshot(expected), body)
	return changeReceipt(v), err
}
func (a changeFiles) Recover(ctx context.Context, agentID, op, rel string, expected model.ChangeSnapshot, body *string) (workbench.ChangeReceipt, error) {
	ws, err := a.open(ctx, agentID)
	if err != nil {
		return workbench.ChangeReceipt{}, err
	}
	v, err := ws.Recover(ctx, op, rel, workspaceSnapshot(expected), body)
	return changeReceipt(v), err
}
func (a changeFiles) Finalize(ctx context.Context, agentID, op, rel string) error {
	ws, err := a.open(ctx, agentID)
	if err != nil {
		return err
	}
	return ws.Finalize(ctx, op, rel)
}

// operationResolver 只解析真实的持久 Operation，并从冻结预览取得审批详情。
type operationResolver struct {
	changes *workbench.ChangeService
	files   workbenchFiles
}

func (a operationResolver) ResolveOperationContext(ctx context.Context, id string) (tools.RunContext, error) {
	op, c, err := a.changes.OperationContext(ctx, id)
	if err != nil {
		return tools.RunContext{}, err
	}
	agent, err := a.files.agents.Get(ctx, c.AgentID)
	if err != nil {
		return tools.RunContext{}, err
	}
	ws, err := a.files.open(ctx, c.AgentID)
	if err != nil {
		return tools.RunContext{}, err
	}
	risk := model.RiskWrite
	if c.Kind == model.ChangeDelete {
		risk = model.RiskDestructive
	}
	details, _ := json.Marshal(map[string]any{"path": c.TargetPath, "change_id": c.ID, "preview_digest": c.PreviewDigest, "operation_kind": c.Kind, "base_sha256": c.BaseSHA256, "proposed_sha256": c.ProposedSHA256, "revision_id": c.RevisionID, "expires_at": c.ExpiresAt.Format("2006-01-02T15:04:05Z"), "before_size": c.Base.Size, "after_size": c.ProposedSize, "restore_of": c.UndoOf})
	return tools.RunContext{OperationID: op.ID, ChangeSetID: c.ID, PreviewDigest: c.PreviewDigest, SessionID: c.SessionID, Agent: agent, Workspace: toolWorkspace{ws}, Principal: model.Principal{ID: op.OwnerID, Kind: model.PrincipalLocalUser, Roles: []string{model.RoleAdmin}}, OperationRisk: risk, OperationSummary: i18n.T("en", "workbench.change_"+string(c.Kind), map[string]any{"path": c.TargetPath}), OperationDetails: details}, nil
}

// fileChangeTool 只注入人工执行器，不登记模型工具或扩展。
type fileChangeTool struct{ changes *workbench.ChangeService }

func (t fileChangeTool) Spec() model.ToolSpec {
	return model.ToolSpec{Name: tools.UserFileChangeTool, Description: i18n.T("en", "workbench.change_tool", nil), Risk: model.RiskWrite, Source: model.ToolSource("builtin"), InputSchema: json.RawMessage(`{"type":"object","required":["change_id","preview_digest"],"properties":{"change_id":{"type":"string"},"preview_digest":{"type":"string"}},"additionalProperties":false}`)}
}
func (t fileChangeTool) Execute(ctx context.Context, rc tools.RunContext, args json.RawMessage) (model.ToolResult, error) {
	if err := t.changes.ExecuteApproved(ctx, rc.OperationID, rc.ChangeSetID, rc.PreviewDigest); err != nil {
		return model.ToolResult{}, err
	}
	return model.ToolResult{}, nil
}
