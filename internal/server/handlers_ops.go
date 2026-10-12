package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/identity"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/scheduler"
)

// ── 审批 ──────────────────────────────────────────────────────────────────────

func validApprovalStatus(st model.ApprovalStatus) bool {
	switch st {
	case model.ApprovalPending, model.ApprovalApproved, model.ApprovalDenied, model.ApprovalExpired:
		return true
	}
	return false
}

func (s *Server) handleListApprovals(w http.ResponseWriter, r *http.Request) error {
	page, err := parsePage(r)
	if err != nil {
		return err
	}
	status := model.ApprovalPending
	if v := r.URL.Query().Get("status"); v != "" {
		status = model.ApprovalStatus(v)
		if !validApprovalStatus(status) {
			return badValue("status", v)
		}
	}
	items, err := s.deps.Permissions.List(r.Context(), status, page)
	if err != nil {
		return err
	}
	writeItems(w, items)
	return nil
}

type decisionRequest struct {
	Approve *bool               `json:"approve"`
	Scope   model.ApprovalScope `json:"scope"`
	Reason  string              `json:"reason"`
}

// handleDecideApproval 提交审批决定，决定人是令牌对应的 Principal。
func (s *Server) handleDecideApproval(w http.ResponseWriter, r *http.Request) error {
	var req decisionRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if req.Approve == nil {
		return missingField("approve")
	}
	scope := req.Scope
	if scope == "" {
		scope = model.ScopeOnce
	}
	switch scope {
	case model.ScopeOnce, model.ScopeSession, model.ScopeAlways:
	default:
		return badValue("scope", string(scope))
	}
	p, err := identity.RequirePrincipal(r.Context())
	if err != nil {
		return err
	}
	a, err := s.deps.Permissions.Decide(r.Context(), r.PathValue("id"),
		model.Decision{Approve: *req.Approve, Scope: scope, Reason: req.Reason}, p.ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, a)
	return nil
}

// ── 定时任务 ──────────────────────────────────────────────────────────────────

// jobCreateRequest 是 POST /api/v1/jobs 的请求体；enabled 缺省 true（传输层的缺席语义），
// missed_policy 的缺省值与全部字段校验由 scheduler 负责。
type jobCreateRequest struct {
	AgentID      string             `json:"agent_id"`
	Name         string             `json:"name"`
	Schedule     string             `json:"schedule"`
	Timezone     string             `json:"timezone"`
	Kind         model.JobKind      `json:"kind"`
	Input        string             `json:"input"`
	SessionKey   string             `json:"session_key"`
	MissedPolicy model.MissedPolicy `json:"missed_policy"`
	Enabled      *bool              `json:"enabled"`
}

// jobPatchRequest 是 PATCH 的请求体；agent_id 不可变，给出且与现值不同返回 invalid。
type jobPatchRequest struct {
	AgentID      *string             `json:"agent_id"`
	Name         *string             `json:"name"`
	Schedule     *string             `json:"schedule"`
	Timezone     *string             `json:"timezone"`
	Kind         *model.JobKind      `json:"kind"`
	Input        *string             `json:"input"`
	SessionKey   *string             `json:"session_key"`
	MissedPolicy *model.MissedPolicy `json:"missed_policy"`
	Enabled      *bool               `json:"enabled"`
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) error {
	items, err := s.deps.Jobs.List(r.Context(), strings.TrimSpace(r.URL.Query().Get("agent_id")))
	if err != nil {
		return err
	}
	writeItems(w, items)
	return nil
}

func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) error {
	var req jobCreateRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	job, err := s.deps.Jobs.Create(r.Context(), scheduler.JobSpec{
		AgentID:      req.AgentID,
		Name:         req.Name,
		Schedule:     req.Schedule,
		Timezone:     req.Timezone,
		Kind:         req.Kind,
		Input:        req.Input,
		SessionKey:   req.SessionKey,
		MissedPolicy: req.MissedPolicy,
		Enabled:      enabled,
	})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, job)
	return nil
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) error {
	job, err := s.deps.Jobs.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, job)
	return nil
}

func (s *Server) handleUpdateJob(w http.ResponseWriter, r *http.Request) error {
	var req jobPatchRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	id := r.PathValue("id")
	if req.AgentID != nil && strings.TrimSpace(*req.AgentID) != "" {
		cur, err := s.deps.Jobs.Get(r.Context(), id)
		if err != nil {
			return err
		}
		if cur.AgentID != strings.TrimSpace(*req.AgentID) {
			return invalid("agent_id_immutable", "定时任务的 agent_id 不可修改", map[string]any{"field": "agent_id"})
		}
	}
	job, err := s.deps.Jobs.Update(r.Context(), id, scheduler.JobPatch{
		Name:         req.Name,
		Schedule:     req.Schedule,
		Timezone:     req.Timezone,
		Kind:         req.Kind,
		Input:        req.Input,
		SessionKey:   req.SessionKey,
		MissedPolicy: req.MissedPolicy,
		Enabled:      req.Enabled,
	})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, job)
	return nil
}

func (s *Server) handleDeleteJob(w http.ResponseWriter, r *http.Request) error {
	if err := s.deps.Jobs.Delete(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	writeNoContent(w)
	return nil
}

// handleRunJob 立即触发一次（异步执行），返回 202。
func (s *Server) handleRunJob(w http.ResponseWriter, r *http.Request) error {
	if err := s.deps.Jobs.TriggerNow(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusAccepted)
	return nil
}

// ── 扩展 ──────────────────────────────────────────────────────────────────────

// extensionInstallRequest：path 安装本地目录中的技能或插件；mcp + name 登记一个 MCP 服务。
type extensionInstallRequest struct {
	Path string                 `json:"path"`
	MCP  *model.MCPServerConfig `json:"mcp"`
	Name string                 `json:"name"`
}

// extensionStatusView 对应 OpenAPI 的 ExtensionStatus。
type extensionStatusView struct {
	ID           string `json:"id"`
	Connected    bool   `json:"connected"`
	ToolCount    int    `json:"tool_count"`
	ToolsChanged bool   `json:"tools_changed"`
	LastError    string `json:"last_error,omitempty"`
}

func validExtensionKind(k model.ExtensionKind) bool {
	switch k {
	case model.ExtensionSkill, model.ExtensionMCPServer, model.ExtensionPlugin:
		return true
	}
	return false
}

func (s *Server) handleListExtensions(w http.ResponseWriter, r *http.Request) error {
	kind := model.ExtensionKind(r.URL.Query().Get("kind"))
	if kind != "" && !validExtensionKind(kind) {
		return badValue("kind", string(kind))
	}
	items, err := s.deps.Extensions.List(r.Context(), kind)
	if err != nil {
		return err
	}
	writeItems(w, items)
	return nil
}

// handleInstallExtension 按请求形状分派：目录里有 SKILL.md → 技能；有 extension.json → 插件；
// 给出 mcp → 登记 MCP 服务。path 与 mcp 同时给出或都缺席都是 invalid。
func (s *Server) handleInstallExtension(w http.ResponseWriter, r *http.Request) error {
	var req extensionInstallRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	ctx := r.Context()
	path := strings.TrimSpace(req.Path)
	var (
		ext *model.Extension
		err error
	)
	switch {
	case path != "" && req.MCP != nil:
		return invalid("ambiguous_install", "path 与 mcp 只能给出一个", nil)
	case path != "":
		ext, err = s.deps.Extensions.Install(ctx, path)
	case req.MCP != nil:
		ext, err = s.deps.Extensions.RegisterMCP(ctx, req.Name, *req.MCP)
	default:
		return invalid("missing_source", "需要 path（技能或插件目录）或 mcp（MCP 服务配置）", nil)
	}
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, ext)
	return nil
}

func (s *Server) handleGetExtension(w http.ResponseWriter, r *http.Request) error {
	ext, err := s.deps.Extensions.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, ext)
	return nil
}

type extensionPatchRequest struct {
	Enabled *bool `json:"enabled"`
}

func (s *Server) handleUpdateExtension(w http.ResponseWriter, r *http.Request) error {
	var req extensionPatchRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	ctx, id := r.Context(), r.PathValue("id")
	var (
		ext *model.Extension
		err error
	)
	switch {
	case req.Enabled == nil:
		ext, err = s.deps.Extensions.Get(ctx, id)
	case *req.Enabled:
		ext, err = s.deps.Extensions.Enable(ctx, id)
	default:
		ext, err = s.deps.Extensions.Disable(ctx, id)
	}
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, ext)
	return nil
}

func (s *Server) handleDeleteExtension(w http.ResponseWriter, r *http.Request) error {
	if err := s.deps.Extensions.Uninstall(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	writeNoContent(w)
	return nil
}

func (s *Server) extensionStatus(id string) extensionStatusView {
	st := s.deps.Extensions.Status(id)
	return extensionStatusView{
		ID:           id,
		Connected:    st.Connected,
		ToolCount:    st.ToolCount,
		ToolsChanged: st.ToolsChanged,
		LastError:    st.LastError,
	}
}

func (s *Server) handleExtensionStatus(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	if _, err := s.deps.Extensions.Get(r.Context(), id); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.extensionStatus(id))
	return nil
}

func (s *Server) handleAcceptExtensionTools(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	if _, err := s.deps.Extensions.AcceptTools(r.Context(), id); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.extensionStatus(id))
	return nil
}

// ── IM 通道 ───────────────────────────────────────────────────────────────────

// channelView 对应 OpenAPI 的 Channel：不含 secret_ref，附加 has_secret 与适配器运行状态。
type channelView struct {
	ID        string            `json:"id"`
	AgentID   string            `json:"agent_id"`
	Kind      model.ChannelKind `json:"kind"`
	Name      string            `json:"name"`
	HasSecret bool              `json:"has_secret"`
	Config    json.RawMessage   `json:"config,omitempty"`
	Enabled   bool              `json:"enabled"`
	Status    string            `json:"status,omitempty"`
	LastError string            `json:"last_error,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// channelCreateRequest：secret 只经 ChannelService 写入 secret 存储，不进入任何响应。
type channelCreateRequest struct {
	AgentID string            `json:"agent_id"`
	Kind    model.ChannelKind `json:"kind"`
	Name    string            `json:"name"`
	Secret  string            `json:"secret"`
	Config  json.RawMessage   `json:"config"`
	Enabled *bool             `json:"enabled"`
}

// channelPatchRequest：secret 缺席不动、"" 清除、非空轮换；config 缺席或 null 不修改。
type channelPatchRequest struct {
	Name    *string         `json:"name"`
	Secret  *string         `json:"secret"`
	Config  json.RawMessage `json:"config"`
	Enabled *bool           `json:"enabled"`
}

// channels 返回通道服务；未启用通道功能时返回 CodeUnavailable。
func (s *Server) channels() (ChannelService, error) {
	if s.deps.Channels == nil {
		return nil, model.NewError(model.CodeUnavailable, "IM 通道功能未启用")
	}
	return s.deps.Channels, nil
}

func (s *Server) channelView(r *http.Request, svc ChannelService, ch *model.Channel) channelView {
	v := channelView{
		ID:        ch.ID,
		AgentID:   ch.AgentID,
		Kind:      ch.Kind,
		Name:      ch.Name,
		HasSecret: svc.HasSecret(r.Context(), ch),
		Config:    ch.Config,
		Enabled:   ch.Enabled,
		LastError: ch.LastError,
		CreatedAt: ch.CreatedAt,
		UpdatedAt: ch.UpdatedAt,
	}
	state, lastErr := svc.Status(ch.ID)
	v.Status = state
	if lastErr != "" {
		v.LastError = lastErr
	}
	return v
}

// channelConfig 只处理传输层的缺席语义：缺席或 null 返回 nil（创建时表示没有配置，PATCH 时表示不修改）；
// 其余原样交给 ChannelService，"必须是 JSON 对象"等校验由 channel 包负责（不在两层各写一份）。
func channelConfig(raw json.RawMessage) []byte {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	return append([]byte(nil), trimmed...)
}

func (s *Server) handleListChannels(w http.ResponseWriter, r *http.Request) error {
	svc, err := s.channels()
	if err != nil {
		return err
	}
	list, err := svc.List(r.Context(), strings.TrimSpace(r.URL.Query().Get("agent_id")))
	if err != nil {
		return err
	}
	items := make([]channelView, 0, len(list))
	for _, ch := range list {
		items = append(items, s.channelView(r, svc, ch))
	}
	writeItems(w, items)
	return nil
}

func (s *Server) handleCreateChannel(w http.ResponseWriter, r *http.Request) error {
	svc, err := s.channels()
	if err != nil {
		return err
	}
	var req channelCreateRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	ch, err := svc.Create(r.Context(), ChannelInput{
		AgentID: strings.TrimSpace(req.AgentID),
		Kind:    req.Kind,
		Name:    req.Name,
		Secret:  req.Secret,
		Config:  channelConfig(req.Config),
		Enabled: req.Enabled,
	})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, s.channelView(r, svc, ch))
	return nil
}

func (s *Server) handleGetChannel(w http.ResponseWriter, r *http.Request) error {
	svc, err := s.channels()
	if err != nil {
		return err
	}
	ch, err := svc.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.channelView(r, svc, ch))
	return nil
}

func (s *Server) handleUpdateChannel(w http.ResponseWriter, r *http.Request) error {
	svc, err := s.channels()
	if err != nil {
		return err
	}
	var req channelPatchRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	ch, err := svc.Update(r.Context(), r.PathValue("id"), ChannelPatch{
		Name:    req.Name,
		Secret:  req.Secret,
		Config:  channelConfig(req.Config),
		Enabled: req.Enabled,
	})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.channelView(r, svc, ch))
	return nil
}

func (s *Server) handleDeleteChannel(w http.ResponseWriter, r *http.Request) error {
	svc, err := s.channels()
	if err != nil {
		return err
	}
	if err := svc.Delete(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	writeNoContent(w)
	return nil
}

func (s *Server) handleReloadChannel(w http.ResponseWriter, r *http.Request) error {
	svc, err := s.channels()
	if err != nil {
		return err
	}
	ch, err := svc.Reload(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.channelView(r, svc, ch))
	return nil
}
