package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"

	"github.com/GoGoslingCA/gogosling/internal/event"
	"github.com/GoGoslingCA/gogosling/internal/i18n"
	"github.com/GoGoslingCA/gogosling/internal/identity"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/workspace"
)

// decodeWorkbenchJSON 保持现有 JSON 大小/类型边界，并拒绝 owner_id 等服务端所有字段。
func decodeWorkbenchJSON(r *http.Request, out any, required ...string) error {
	var raw json.RawMessage
	if err := decodeJSON(r, &raw); err != nil {
		return err
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return missingField("body")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return invalid("invalid_fields", i18n.T("en", "workbench.invalid_fields", nil), nil)
	}
	for _, key := range required {
		value, ok := fields[key]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return missingField(key)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return invalid("invalid_fields", i18n.T("en", "workbench.invalid_fields", nil), nil)
	}
	return nil
}
func (s *Server) requireWorkbench(next handlerFunc) handlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		if s.deps.Workbench == nil {
			return model.NewError(model.CodeUnavailable, i18n.T(s.localeOf(r), "workbench.unavailable", nil))
		}
		return next(w, r)
	}
}
func principalOf(r *http.Request) model.Principal {
	p, _ := identity.FromContext(r.Context())
	return p
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) error {
	var in model.ProjectInput
	if err := decodeWorkbenchJSON(r, &in); err != nil {
		return err
	}
	out, err := s.deps.Workbench.CreateProject(r.Context(), principalOf(r), in)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, out)
	return nil
}
func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) error {
	page, err := parsePage(r)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	out, err := s.deps.Workbench.ListProjects(r.Context(), model.ProjectFilter{AgentID: q.Get("agent_id"), Status: model.ProjectStatus(q.Get("status"))}, page)
	if err != nil {
		return err
	}
	writeItems(w, out)
	return nil
}
func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) error {
	out, err := s.deps.Workbench.GetProject(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}
func (s *Server) handleUpdateProject(w http.ResponseWriter, r *http.Request) error {
	var in model.ProjectPatch
	if err := decodeWorkbenchJSON(r, &in); err != nil {
		return err
	}
	out, err := s.deps.Workbench.UpdateProject(r.Context(), principalOf(r), r.PathValue("id"), in)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) handleListProjectMaterials(w http.ResponseWriter, r *http.Request) error {
	page, err := parsePage(r)
	if err != nil {
		return err
	}
	out, err := s.deps.Workbench.ListMaterials(r.Context(), r.PathValue("id"), page)
	if err != nil {
		return err
	}
	writeItems(w, out)
	return nil
}
func (s *Server) handleRemoveProjectMaterial(w http.ResponseWriter, r *http.Request) error {
	if err := s.deps.Workbench.RemoveMaterial(r.Context(), principalOf(r), r.PathValue("id"), r.PathValue("material_id")); err != nil {
		return err
	}
	writeNoContent(w)
	return nil
}
func (s *Server) handleUploadProjectMaterial(w http.ResponseWriter, r *http.Request) error {
	mr, err := r.MultipartReader()
	if err != nil {
		return invalid("multipart_required", i18n.T("en", "workbench.invalid_fields", nil), nil)
	}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return missingField("file")
		}
		if err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				return bodyTooLarge(mbe.Limit, err)
			}
			return invalid("malformed_multipart", i18n.T("en", "workbench.invalid_fields", nil), nil)
		}
		if part.FormName() != "file" {
			_ = part.Close()
			continue
		}
		out, err := s.deps.Workbench.UploadMaterial(r.Context(), principalOf(r), r.PathValue("id"), part.FileName(), part, s.opts.MaxUploadBytes)
		_ = part.Close()
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusCreated, out)
		return nil
	}
}
func (s *Server) handleSetSessionProject(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		ProjectID optional[string] `json:"project_id"`
		Expected  optional[string] `json:"expected_project_id"`
	}
	if err := decodeWorkbenchJSON(r, &req); err != nil {
		return err
	}
	if !req.ProjectID.Set {
		return missingField("project_id")
	}
	if !req.Expected.Set {
		return missingField("expected_project_id")
	}
	if (!req.ProjectID.Null && req.ProjectID.Value == "") || (!req.Expected.Null && req.Expected.Value == "") {
		return invalid("invalid_fields", i18n.T("en", "workbench.invalid_fields", nil), nil)
	}
	out, err := s.deps.Workbench.SetSessionProject(r.Context(), principalOf(r), r.PathValue("id"), req.Expected.Value, req.ProjectID.Value)
	if err != nil {
		return err
	}
	view := s.sessionView(out)
	if s.deps.Bus != nil {
		s.deps.Bus.Publish(event.Event{Type: "session.updated", AgentID: out.AgentID, SessionID: out.ID, Payload: view})
	}
	writeJSON(w, http.StatusOK, view)
	return nil
}

func (s *Server) handleCreateDraft(w http.ResponseWriter, r *http.Request) error {
	var in model.DraftInput
	if err := decodeWorkbenchJSON(r, &in, "kind", "agent_id", "text"); err != nil {
		return err
	}
	out, err := s.deps.Workbench.CreateDraft(r.Context(), principalOf(r), in)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, out)
	return nil
}
func (s *Server) handleListDrafts(w http.ResponseWriter, r *http.Request) error {
	page, err := parsePage(r)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	if q.Has("owner_id") {
		return invalid("invalid_fields", i18n.T("en", "workbench.invalid_fields", nil), nil)
	}
	out, err := s.deps.Workbench.ListDrafts(r.Context(), principalOf(r), model.DraftFilter{Kind: model.DraftKind(q.Get("kind")), AgentID: q.Get("agent_id"), ProjectID: q.Get("project_id"), SessionID: q.Get("session_id"), ArtifactID: q.Get("artifact_id")}, page)
	if err != nil {
		return err
	}
	writeItems(w, out)
	return nil
}
func (s *Server) handleGetDraft(w http.ResponseWriter, r *http.Request) error {
	out, err := s.deps.Workbench.GetDraft(r.Context(), principalOf(r), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}
func (s *Server) handleUpdateDraft(w http.ResponseWriter, r *http.Request) error {
	var in model.DraftInput
	if err := decodeWorkbenchJSON(r, &in, "kind", "agent_id", "text", "expected_version"); err != nil {
		return err
	}
	out, err := s.deps.Workbench.UpdateDraft(r.Context(), principalOf(r), r.PathValue("id"), in)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}
func (s *Server) handleDeleteDraft(w http.ResponseWriter, r *http.Request) error {
	expected, err := strconv.ParseInt(r.URL.Query().Get("expected_version"), 10, 64)
	if err != nil || expected < 1 {
		return missingField("expected_version")
	}
	if err = s.deps.Workbench.DeleteDraft(r.Context(), principalOf(r), r.PathValue("id"), expected); err != nil {
		return err
	}
	writeNoContent(w)
	return nil
}

func (s *Server) handleCreateArtifact(w http.ResponseWriter, r *http.Request) error {
	var in model.ArtifactInput
	if err := decodeWorkbenchJSON(r, &in, "title", "kind", "filename", "media_type", "source_message_id", "content"); err != nil {
		return err
	}
	out, err := s.deps.Workbench.CreateArtifact(r.Context(), principalOf(r), r.PathValue("id"), in, r.Header.Get("Idempotency-Key"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, out)
	return nil
}
func (s *Server) handleListSessionArtifacts(w http.ResponseWriter, r *http.Request) error {
	return s.listArtifacts(w, r, model.ArtifactFilter{SessionID: r.PathValue("id")})
}
func (s *Server) handleListProjectArtifacts(w http.ResponseWriter, r *http.Request) error {
	return s.listArtifacts(w, r, model.ArtifactFilter{ProjectID: r.PathValue("id")})
}
func (s *Server) listArtifacts(w http.ResponseWriter, r *http.Request, f model.ArtifactFilter) error {
	page, err := parsePage(r)
	if err != nil {
		return err
	}
	out, err := s.deps.Workbench.ListArtifacts(r.Context(), f, page)
	if err != nil {
		return err
	}
	writeItems(w, out)
	return nil
}
func (s *Server) handleGetArtifact(w http.ResponseWriter, r *http.Request) error {
	out, err := s.deps.Workbench.GetArtifact(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}
func (s *Server) handleCreateArtifactRevision(w http.ResponseWriter, r *http.Request) error {
	var in model.ArtifactRevisionInput
	if err := decodeWorkbenchJSON(r, &in, "expected_latest_revision_id", "filename", "media_type", "content"); err != nil {
		return err
	}
	out, err := s.deps.Workbench.AppendRevision(r.Context(), principalOf(r), r.PathValue("id"), in)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, out)
	return nil
}
func (s *Server) handleListArtifactRevisions(w http.ResponseWriter, r *http.Request) error {
	page, err := parsePage(r)
	if err != nil {
		return err
	}
	out, err := s.deps.Workbench.ListRevisions(r.Context(), r.PathValue("id"), page)
	if err != nil {
		return err
	}
	writeItems(w, out)
	return nil
}
func (s *Server) handleGetArtifactRevision(w http.ResponseWriter, r *http.Request) error {
	out, err := s.deps.Workbench.GetRevision(r.Context(), r.PathValue("id"), r.PathValue("revision_id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}
func (s *Server) handleArtifactContent(w http.ResponseWriter, r *http.Request) error {
	return s.artifactContent(w, r, false)
}
func (s *Server) handleArtifactDownload(w http.ResponseWriter, r *http.Request) error {
	return s.artifactContent(w, r, true)
}
func (s *Server) artifactContent(w http.ResponseWriter, r *http.Request, download bool) error {
	revision, err := s.deps.Workbench.GetRevision(r.Context(), r.PathValue("id"), r.PathValue("revision_id"))
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if download {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": workspace.SanitizeFilename(revision.Filename)}))
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(revision.Content)))
	w.WriteHeader(http.StatusOK)
	_, err = io.WriteString(w, revision.Content)
	return err
}
