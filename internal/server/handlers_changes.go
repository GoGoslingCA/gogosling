package server

import (
	"github.com/GoGoslingCA/gogosling/internal/i18n"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"net/http"
)

func (s *Server) requireChanges(next handlerFunc) handlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		if s.deps.Changes == nil || !s.deps.Changes.Supported() {
			return model.NewError(model.CodeUnavailable, i18n.T(s.localeOf(r), "workbench.change_unsupported", nil))
		}
		w.Header().Set("Cache-Control", "no-store")
		return next(w, r)
	}
}
func (s *Server) handlePrepareChange(w http.ResponseWriter, r *http.Request) error {
	var in model.ChangeInput
	if err := decodeWorkbenchJSON(r, &in, "artifact_id", "revision_id", "target_path", "operation"); err != nil {
		return err
	}
	c, err := s.deps.Changes.Prepare(r.Context(), principalOf(r), r.PathValue("id"), in)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, c)
	return nil
}
func (s *Server) handleListChanges(w http.ResponseWriter, r *http.Request) error {
	p, err := parsePage(r)
	if err != nil {
		return err
	}
	items, err := s.deps.Changes.List(r.Context(), principalOf(r), r.PathValue("id"), p)
	if err != nil {
		return err
	}
	writeItems(w, items)
	return nil
}
func (s *Server) handleGetChange(w http.ResponseWriter, r *http.Request) error {
	c, err := s.deps.Changes.Get(r.Context(), principalOf(r), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, c)
	return nil
}
func (s *Server) handleChangeDiff(w http.ResponseWriter, r *http.Request) error {
	d, err := s.deps.Changes.Diff(r.Context(), principalOf(r), r.PathValue("id"))
	if err != nil {
		return err
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	writeJSON(w, http.StatusOK, d)
	return nil
}
func (s *Server) handleApplyChange(w http.ResponseWriter, r *http.Request) error {
	var in model.ChangeApplyInput
	if err := decodeWorkbenchJSON(r, &in, "expected_version", "preview_digest"); err != nil {
		return err
	}
	c, err := s.deps.Changes.Apply(r.Context(), principalOf(r), r.PathValue("id"), in, r.Header.Get("Idempotency-Key"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusAccepted, c)
	return nil
}
func (s *Server) handleUndoChange(w http.ResponseWriter, r *http.Request) error {
	var in model.ChangeVersionInput
	if err := decodeWorkbenchJSON(r, &in, "expected_version"); err != nil {
		return err
	}
	c, err := s.deps.Changes.UndoPreview(r.Context(), principalOf(r), r.PathValue("id"), in.ExpectedVersion)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, c)
	return nil
}
func (s *Server) handleCancelChange(w http.ResponseWriter, r *http.Request) error {
	var in model.ChangeVersionInput
	if err := decodeWorkbenchJSON(r, &in, "expected_version"); err != nil {
		return err
	}
	c, err := s.deps.Changes.Cancel(r.Context(), principalOf(r), r.PathValue("id"), in.ExpectedVersion)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusAccepted, c)
	return nil
}
func (s *Server) handleReconcileChange(w http.ResponseWriter, r *http.Request) error {
	var in model.ChangeVersionInput
	if err := decodeWorkbenchJSON(r, &in, "expected_version"); err != nil {
		return err
	}
	c, err := s.deps.Changes.Reconcile(r.Context(), principalOf(r), r.PathValue("id"), in.ExpectedVersion)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, c)
	return nil
}
