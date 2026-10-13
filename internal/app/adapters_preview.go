package app

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/workbench/preview"
)

// previewFiles 复用既有 Agent 工作区，拒绝以过宽工作区覆盖内核数据根。
type previewFiles struct {
	workbenchFiles
	dataDir string
}

func (p previewFiles) Open(ctx context.Context, agentID, rel string) (preview.File, error) {
	ws, err := p.open(ctx, agentID)
	if err != nil {
		return nil, err
	}
	_, err = ws.Resolve(rel)
	if err != nil {
		return nil, err
	}
	if !previewRootAllowed(ws.Root(), p.dataDir) {
		return nil, model.NewError(model.CodeForbidden, "preview_access")
	}
	return ws.OpenRead(ctx, rel)
}

// previewRootAllowed 保留界面与工具读取的一致边界，禁止以过宽工作区覆盖内核目录。
func previewRootAllowed(root, dataDir string) bool {
	realRoot, rootErr := filepath.EvalSymlinks(root)
	realData, dataErr := filepath.EvalSymlinks(dataDir)
	return rootErr == nil && dataErr == nil && !pathUnder(realRoot, realData)
}
func pathUnder(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
