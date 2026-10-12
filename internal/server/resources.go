package server

import (
	"net/http"
	"strconv"

	"github.com/GoGoslingCA/gogosling/internal/i18n"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

// SettingMaxParallel 是本地资源配置的持久键，组合根与 API 共用。
const SettingMaxParallel = "max_parallel_runs"

func (s *Server) handleResources(w http.ResponseWriter, r *http.Request) error {
	if s.deps.Sessions.Resources().MaxParallel < 1 {
		return model.NewError(model.CodeUnavailable, i18n.T(s.localeOf(r), "resources.unavailable", nil))
	}
	writeJSON(w, http.StatusOK, s.deps.Sessions.Resources())
	return nil
}

func (s *Server) handleUpdateResources(w http.ResponseWriter, r *http.Request) error {
	if s.deps.Sessions.Resources().MaxParallel < 1 {
		return model.NewError(model.CodeUnavailable, i18n.T(s.localeOf(r), "resources.unavailable", nil))
	}
	var input struct {
		MaxParallel int `json:"max_parallel"`
	}
	if err := decodeWorkbenchJSON(r, &input, "max_parallel"); err != nil {
		return err
	}
	if input.MaxParallel < 1 || input.MaxParallel > 8 {
		return model.NewError(model.CodeInvalid, i18n.T(s.localeOf(r), "resources.invalid_limit", nil))
	}
	s.settings.writeMu.Lock()
	defer s.settings.writeMu.Unlock()
	if err := s.deps.Store.SetSetting(r.Context(), SettingMaxParallel, strconv.Itoa(input.MaxParallel)); err != nil {
		return err
	}
	if err := s.deps.Sessions.SetParallelLimit(input.MaxParallel); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.deps.Sessions.Resources())
	return nil
}
