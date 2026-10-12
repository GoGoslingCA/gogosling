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
	realRoot, rootErr := filepath.EvalSymlinks(ws.Root())
	realData, dataErr := filepath.EvalSymlinks(p.dataDir)
	if rootErr != nil || dataErr != nil || pathUnder(realRoot, realData) {
		return nil, model.NewError(model.CodeForbidden, "preview_access")
	}
	return ws.OpenRead(ctx, rel)
}
func pathUnder(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
