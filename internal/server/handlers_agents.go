package server

import (
	"net/http"

	"github.com/GoGoslingCA/gogosling/internal/agent"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/provider"
)

// ── 智能体 ────────────────────────────────────────────────────────────────────

// agentCreateRequest 是 POST /api/v1/agents 的请求体（OpenAPI AgentInput，另接受 workspace_dir）。
// tools 缺席或为 null 表示使用模板 / 默认工具集，[] 表示不启用任何内置工具。
type agentCreateRequest struct {
	Name         string              `json:"name"`
	Description  string              `json:"description"`
	SystemPrompt string              `json:"system_prompt"`
	Model        model.ModelRef      `json:"model"`
	Tools        []string            `json:"tools"`
	Extensions   []string            `json:"extensions"`
	Autonomy     model.AutonomyLevel `json:"autonomy"`
	WorkspaceDir string              `json:"workspace_dir"`
	Template     string              `json:"template"`
	Enabled      *bool               `json:"enabled"`
}

// agentPatchRequest 是 PATCH 的请求体：缺席的字段不修改。
// tools / extensions 区分三种状态：缺席不修改；null 重置为默认工具集 / 清空扩展；数组（含 []）按给出值设置。
type agentPatchRequest struct {
	Name         *string              `json:"name"`
	Description  *string              `json:"description"`
	SystemPrompt *string              `json:"system_prompt"`
	Model        *model.ModelRef      `json:"model"`
	Tools        optional[[]string]   `json:"tools"`
	Extensions   optional[[]string]   `json:"extensions"`
	Autonomy     *model.AutonomyLevel `json:"autonomy"`
	Template     *string              `json:"template"`
	Enabled      *bool                `json:"enabled"`
}

func (p agentPatchRequest) toInput() agent.UpdateInput {
	in := agent.UpdateInput{
		Name:         p.Name,
		Description:  p.Description,
		SystemPrompt: p.SystemPrompt,
		Model:        p.Model,
		Autonomy:     p.Autonomy,
		Template:     p.Template,
		Enabled:      p.Enabled,
	}
	in.Tools = sliceField(p.Tools)
	in.Extensions = sliceField(p.Extensions)
	return in
}

// sliceField 把 optional 切片字段映射为 UpdateInput 的指针语义：
// 缺席 → nil（不修改）；null → 指向 nil 切片（重置）；数组 → 指向非 nil 切片。
func sliceField(o optional[[]string]) *[]string {
	if !o.Set {
		return nil
	}
	if o.Null {
		var reset []string
		return &reset
	}
	v := o.Value
	if v == nil {
		v = []string{}
	}
	return &v
}

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) error {
	items, err := s.deps.Agents.List(r.Context())
	if err != nil {
		return err
	}
	writeItems(w, items)
	return nil
}

func (s *Server) handleCreateAgent(w http.ResponseWriter, r *http.Request) error {
	var req agentCreateRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	a, err := s.deps.Agents.Create(r.Context(), agent.CreateInput{
		Name:         req.Name,
		Description:  req.Description,
		SystemPrompt: req.SystemPrompt,
		Model:        req.Model,
		Tools:        req.Tools,
		Extensions:   req.Extensions,
		Autonomy:     req.Autonomy,
		WorkspaceDir: req.WorkspaceDir,
		Template:     req.Template,
		Enabled:      req.Enabled,
	})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, a)
	return nil
}

func (s *Server) handleGetAgent(w http.ResponseWriter, r *http.Request) error {
	a, err := s.deps.Agents.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, a)
	return nil
}

func (s *Server) handleUpdateAgent(w http.ResponseWriter, r *http.Request) error {
	var req agentPatchRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	a, err := s.deps.Agents.Update(r.Context(), r.PathValue("id"), req.toInput())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, a)
	return nil
}

func (s *Server) handleDeleteAgent(w http.ResponseWriter, r *http.Request) error {
	if err := s.deps.Agents.Delete(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	writeNoContent(w)
	return nil
}

func (s *Server) handleAgentTools(w http.ResponseWriter, r *http.Request) error {
	a, err := s.deps.Agents.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	specs, err := s.deps.Tools.ToolsFor(r.Context(), a)
	if err != nil {
		return err
	}
	writeItems(w, specs)
	return nil
}

// ── 供应商 ────────────────────────────────────────────────────────────────────

// providerCreateRequest 是 POST /api/v1/providers 的请求体；api_key 只写入 secret 存储。
type providerCreateRequest struct {
	Name         string             `json:"name"`
	Kind         model.ProviderKind `json:"kind"`
	BaseURL      string             `json:"base_url"`
	APIKey       string             `json:"api_key"`
	DefaultModel string             `json:"default_model"`
	Enabled      *bool              `json:"enabled"`
}

// providerPatchRequest 是 PATCH 的请求体。api_key：缺席不动，"" 清除，非空轮换。
type providerPatchRequest struct {
	Name         *string             `json:"name"`
	Kind         *model.ProviderKind `json:"kind"`
	BaseURL      *string             `json:"base_url"`
	APIKey       *string             `json:"api_key"`
	DefaultModel *string             `json:"default_model"`
	Enabled      *bool               `json:"enabled"`
}

// providerView 把记录转成对外视图（has_secret 代替 secret_ref，永不含密钥）。
func (s *Server) providerView(r *http.Request, p *model.Provider) (provider.View, error) {
	return s.deps.Providers.View(r.Context(), p)
}

func (s *Server) handleListProviders(w http.ResponseWriter, r *http.Request) error {
	list, err := s.deps.Providers.List(r.Context())
	if err != nil {
		return err
	}
	items := make([]provider.View, 0, len(list))
	for _, p := range list {
		v, verr := s.providerView(r, p)
		if verr != nil {
			return verr
		}
		items = append(items, v)
	}
	writeItems(w, items)
	return nil
}

func (s *Server) handleCreateProvider(w http.ResponseWriter, r *http.Request) error {
	var req providerCreateRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	p, err := s.deps.Providers.Create(r.Context(), provider.Input{
		Name:         req.Name,
		Kind:         req.Kind,
		BaseURL:      req.BaseURL,
		APIKey:       req.APIKey,
		DefaultModel: req.DefaultModel,
		Enabled:      req.Enabled,
	})
	if err != nil {
		return err
	}
	v, err := s.providerView(r, p)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, v)
	return nil
}

func (s *Server) handleGetProvider(w http.ResponseWriter, r *http.Request) error {
	p, err := s.deps.Providers.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	v, err := s.providerView(r, p)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, v)
	return nil
}

func (s *Server) handleUpdateProvider(w http.ResponseWriter, r *http.Request) error {
	var req providerPatchRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	p, err := s.deps.Providers.Update(r.Context(), r.PathValue("id"), provider.UpdateInput{
		Name:         req.Name,
		Kind:         req.Kind,
		BaseURL:      req.BaseURL,
		APIKey:       req.APIKey,
		DefaultModel: req.DefaultModel,
		Enabled:      req.Enabled,
	})
	if err != nil {
		return err
	}
	v, err := s.providerView(r, p)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, v)
	return nil
}

// handleDeleteProvider 删除供应商；仍被智能体引用时 provider 包返回的 conflict（含引用者）原样透传。
func (s *Server) handleDeleteProvider(w http.ResponseWriter, r *http.Request) error {
	if err := s.deps.Providers.Delete(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	writeNoContent(w)
	return nil
}

func (s *Server) handleTestProvider(w http.ResponseWriter, r *http.Request) error {
	res, err := s.deps.Providers.Test(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, res)
	return nil
}
