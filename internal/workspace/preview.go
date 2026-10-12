package workspace

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// OpenRead 打开工作区内的普通文件；os.Root 在实际打开时阻止符号链接/路径竞态逃逸。
// 返回的句柄归调用方所有，必须关闭。非阻塞打开避免被替换的 FIFO 卡住请求。
func (w *Workspace) OpenRead(ctx context.Context, rel string) (*os.File, error) {
	if err := checkCtx(ctx); err != nil {
		return nil, err
	}
	if _, err := w.Resolve(rel); err != nil {
		return nil, err
	}
	clean, err := cleanRel(rel)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(w.realRoot)
	if err != nil {
		return nil, model.NewError(model.CodeForbidden, "preview_access")
	}
	defer root.Close()
	f, err := openPreviewFile(root, clean)
	if err != nil {
		return nil, model.NewError(model.CodeNotFound, "preview_file")
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, model.NewError(model.CodeInvalid, "preview_regular_file")
	}
	if err := checkCtx(ctx); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// PreviewEntries 有界读取目录，最多501项（最后一项仅表示列表已截断）。
func (w *Workspace) PreviewEntries(ctx context.Context, rel string) ([]Entry, error) {
	if err := checkCtx(ctx); err != nil {
		return nil, err
	}
	if _, err := w.Resolve(rel); err != nil {
		return nil, err
	}
	clean, err := cleanRel(rel)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(w.realRoot)
	if err != nil {
		return nil, model.NewError(model.CodeForbidden, "preview_access")
	}
	defer root.Close()
	dir, err := openPreviewFile(root, clean)
	if err != nil {
		return nil, model.NewError(model.CodeNotFound, "preview_directory")
	}
	defer dir.Close()
	entries, err := dir.ReadDir(501)
	if err != nil && err != io.EOF {
		return nil, model.NewError(model.CodeInvalid, "preview_directory")
	}
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if err := checkCtx(ctx); err != nil {
			return nil, err
		}
		p := filepath.ToSlash(filepath.Join(clean, e.Name()))
		if reservedPath(p) || e.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, err := e.Info()
		if err != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
			continue
		}
		out = append(out, Entry{Name: e.Name(), Path: p, Size: info.Size(), IsDir: info.IsDir(), ModTime: info.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}
