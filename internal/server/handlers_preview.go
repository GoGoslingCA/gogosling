package server

import (
	"context"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/workbench/preview"
)

func (s *Server) handleFilePreview(w http.ResponseWriter, r *http.Request) error {
	if s.deps.Preview == nil {
		return preview.Error("unsupported")
	}
	doc, err := s.deps.Preview.Preview(r.Context(), r.PathValue("id"), r.URL.Query().Get("path"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, doc)
	return nil
}
func (s *Server) handleFileContent(w http.ResponseWriter, r *http.Request) error {
	if s.deps.Preview == nil {
		return preview.Error("unsupported")
	}
	f, info, err := s.deps.Preview.Open(r.Context(), r.PathValue("id"), r.URL.Query().Get("path"), r.URL.Query().Get("version"))
	if err != nil {
		return err
	}
	defer f.Close()
	contentType := s.deps.Preview.Detect(info.Name()).MIME
	if contentType == "application/octet-stream" {
		if t := mime.TypeByExtension(strings.ToLower(filepath.Ext(info.Name()))); t != "" {
			contentType = t
		}
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": info.Name()}))
	http.ServeContent(w, r, info.Name(), info.ModTime(), previewReadSeeker{r.Context(), f})
	return nil
}

type previewReadSeeker struct {
	ctx context.Context
	io.ReadSeeker
}

func (r previewReadSeeker) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.ReadSeeker.Read(p)
}
func (s *Server) handlePreviewFiles(w http.ResponseWriter, r *http.Request) error {
	if s.deps.Preview == nil {
		return preview.Error("unsupported")
	}
	a, err := s.deps.Agents.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	ws, err := s.deps.Workspaces.Open(r.Context(), a.ID, a.WorkspaceDir)
	if err != nil {
		return err
	}
	items, err := ws.PreviewEntries(r.Context(), r.URL.Query().Get("path"))
	if err != nil {
		return err
	}
	// 元数据列举不读取文件正文，权限仍由 Open 在实际预览时核验。
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "truncated": len(items) > 500})
	return nil
}

// PreviewConverter 可选地把受控输入转换为 PDF；实现必须隔离进程并及时清理。
type PreviewConverter interface {
	Convert(ctx context.Context, name string, input io.Reader) ([]byte, error)
}

func (s *Server) handlePreviewConversion(w http.ResponseWriter, r *http.Request) error {
	if s.deps.Preview == nil || s.deps.PreviewConverter == nil {
		return preview.Error("converter_unavailable")
	}
	rel := r.URL.Query().Get("path")
	f, info, err := s.deps.Preview.Open(r.Context(), r.PathValue("id"), rel, r.URL.Query().Get("version"))
	if err != nil {
		return err
	}
	defer f.Close()
	kind := s.deps.Preview.Detect(info.Name()).ID
	if kind != "legacy" && kind != "word" && kind != "slides" && kind != "sheets" && kind != "opendocument" {
		return preview.Error("unsupported")
	}
	if info.Size() > preview.MaxStructured {
		return preview.Error("too_large")
	}
	result, err := s.deps.PreviewConverter.Convert(r.Context(), info.Name(), previewReadSeeker{r.Context(), f})
	if err != nil {
		return err
	}
	after, err := f.Stat()
	if err != nil || preview.Version(info) != preview.Version(after) {
		return preview.Error("changed")
	}
	if len(result) > preview.MaxFile {
		return preview.Error("too_large")
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", "attachment")
	_, err = w.Write(result)
	return err
}

func (s *Server) previewHandler(next handlerFunc) handlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		id := model.NewID()
		w.Header().Set("X-Preview-Id", id)
		return preview.SafeError(next(w, r.WithContext(preview.WithRequestID(r.Context(), id))))
	}
}
