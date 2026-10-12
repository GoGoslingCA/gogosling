package servertest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"path"
	"strings"

	"github.com/GoGoslingCA/gogosling/internal/agent"
	"github.com/GoGoslingCA/gogosling/internal/i18n"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/workbench/preview"
	"github.com/GoGoslingCA/gogosling/internal/workspace"
)

// workbenchFiles 只适配现有入站附件能力，不向工作台暴露任意文件写入。
type workbenchFiles struct {
	agents     *agent.Service
	workspaces *workspace.Manager
}

func (a workbenchFiles) open(ctx context.Context, agentID string) (*workspace.Workspace, error) {
	agent, err := a.agents.Get(ctx, agentID)
	if err != nil {
		return nil, err
	}
	return a.workspaces.Open(ctx, agentID, agent.WorkspaceDir)
}

// SaveInbound 复用现有上传净化与原子落盘，不覆盖已有入站文件。
func (a workbenchFiles) SaveInbound(ctx context.Context, agentID, name string, r io.Reader, max int64) (model.Attachment, error) {
	ws, err := a.open(ctx, agentID)
	if err != nil {
		return model.Attachment{}, err
	}
	return ws.SaveInbound(ctx, name, r, max)
}

// Inspect 校验入站路径并按实际文件字节生成大小与摘要。
func (a workbenchFiles) Inspect(ctx context.Context, agentID string, att model.Attachment) (model.Attachment, string, error) {
	rel := strings.ReplaceAll(strings.TrimSpace(att.Path), "\\", "/")
	if !strings.HasPrefix(rel, "inbound/") || path.Clean(rel) != rel {
		return model.Attachment{}, "", model.NewError(model.CodeInvalid, i18n.T("en", "workbench.scope_mismatch", nil))
	}
	ws, err := a.open(ctx, agentID)
	if err != nil {
		return model.Attachment{}, "", err
	}
	entry, err := ws.Stat(ctx, rel)
	if err != nil {
		return model.Attachment{}, "", err
	}
	if entry.IsDir {
		return model.Attachment{}, "", model.NewError(model.CodeInvalid, i18n.T("en", "workbench.invalid_fields", nil))
	}
	body, err := ws.ReadFile(ctx, rel, 64<<20)
	if err != nil {
		return model.Attachment{}, "", err
	}
	sum := sha256.Sum256(body)
	return model.Attachment{Name: entry.Name, Path: entry.Path, MIME: att.MIME, Size: int64(len(body))}, hex.EncodeToString(sum[:]), nil
}

func (a workbenchFiles) Open(ctx context.Context, agentID, rel string) (preview.File, error) {
	ws, e := a.open(ctx, agentID)
	if e != nil {
		return nil, e
	}
	return ws.OpenRead(ctx, rel)
}
