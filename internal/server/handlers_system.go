package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/engine"
	"github.com/GoGoslingCA/gogosling/internal/identity"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/version"
)

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) error {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	return nil
}

// workerInfo 对应 OpenAPI 的 VersionInfo.worker。
type workerInfo struct {
	Status  string `json:"status"`
	Version string `json:"version,omitempty"`
	Python  string `json:"python,omitempty"`
}

// versionInfo 对应 OpenAPI 的 VersionInfo。
type versionInfo struct {
	Capabilities map[string]any `json:"capabilities,omitempty"`
	Version      string         `json:"version"`
	Commit       string         `json:"commit"`
	Date         string         `json:"date"`
	StartedAt    string         `json:"started_at"`
	Worker       workerInfo     `json:"worker"`
}

func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) error {
	st := s.deps.Engine.Status()
	var capabilities map[string]any
	if s.deps.Workbench != nil {
		capabilities = s.deps.Workbench.Capabilities()
	}
	if s.deps.Changes != nil && s.deps.Changes.Supported() {
		if capabilities == nil {
			capabilities = map[string]any{}
		}
		capabilities["workbench.changesets"] = 1
	}
	if s.deps.Sessions.Resources().MaxParallel > 0 {
		if capabilities == nil {
			capabilities = map[string]any{}
		}
		capabilities["runtime.resources"] = 1
	}
	if capabilities == nil {
		capabilities = map[string]any{}
	}
	capabilities["prompt.enhance"] = 1
	if s.deps.Preview != nil {
		capabilities["workbench.preview"] = 1
	}
	if s.deps.PreviewConverter != nil {
		capabilities["workbench.preview_layout"] = 1
	}
	writeJSON(w, http.StatusOK, versionInfo{
		Version:      version.Version,
		Capabilities: capabilities,
		Commit:       version.Commit,
		Date:         version.Date,
		StartedAt:    s.opts.StartedAt,
		Worker:       workerInfo{Status: workerStatus(st.State), Version: st.Version, Python: st.Python},
	})
	return nil
}

// handleShutdown 实现 POST /api/v1/system/shutdown：鉴权已由中间件完成，先写出 202，再触发组合根提供的
// Options.Shutdown（契约要求它非阻塞；它让 Serve 的 ctx 取消，随后本服务按正常流程优雅关闭）。
// 重复请求只触发一次，后续请求同样返回 202。没有提供 Shutdown 时返回 unavailable。
func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) error {
	if s.opts.Shutdown == nil {
		return model.NewError(model.CodeUnavailable, "本实例不支持经 API 关闭")
	}
	principal := ""
	if p, ok := identity.FromContext(r.Context()); ok {
		principal = p.ID
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusAccepted)
	_ = http.NewResponseController(w).Flush()
	s.shutdownOnce.Do(func() {
		s.log.Info("收到经 API 的关闭请求", "principal_id", principal)
		s.opts.Shutdown()
	})
	return nil
}

// workerStatus 把 engine.State 映射为契约的三个取值；未知状态按 unavailable。
func workerStatus(s engine.State) string {
	switch s {
	case engine.StateReady:
		return "ready"
	case engine.StateStarting:
		return "starting"
	default:
		return "unavailable"
	}
}

func (s *Server) handleListAudit(w http.ResponseWriter, r *http.Request) error {
	page, err := parsePage(r)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	f := model.AuditFilter{
		AgentID:   strings.TrimSpace(q.Get("agent_id")),
		SessionID: strings.TrimSpace(q.Get("session_id")),
		RunID:     strings.TrimSpace(q.Get("run_id")),
		Actor:     strings.TrimSpace(q.Get("actor")),
		Action:    strings.TrimSpace(q.Get("action")),
	}
	if v := q.Get("since"); v != "" {
		t, perr := time.Parse(time.RFC3339Nano, v)
		if perr != nil {
			return badValue("since", v)
		}
		f.Since = t.UTC()
	}
	items, err := s.deps.Store.ListAudit(r.Context(), f, page)
	if err != nil {
		return err
	}
	writeItems(w, items)
	return nil
}
