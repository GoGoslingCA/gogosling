package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/GoGoslingCA/gogosling/internal/i18n"
	"github.com/GoGoslingCA/gogosling/internal/identity"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/session"
	"github.com/GoGoslingCA/gogosling/internal/workspace"
)

// maxAttachments 是一条消息允许携带的附件数上限。
const maxAttachments = 32

// sessionView 返回会话的响应视图（OpenAPI Session，含 active_run_id）。视图类型由 session 包导出，
// 与 session.* 事件的 payload 是同一个类型，REST 响应与事件流的形状不会分叉。
func (s *Server) sessionView(sess *model.Session) session.View {
	return s.deps.Sessions.ViewOf(sess)
}

func validSessionStatus(st model.SessionStatus) bool {
	return st == model.SessionActive || st == model.SessionArchived
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) error {
	page, err := parsePage(r)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	f := model.SessionFilter{AgentID: strings.TrimSpace(q.Get("agent_id")), ProjectID: strings.TrimSpace(q.Get("project_id"))}
	if f.ProjectID != "" && s.deps.Workbench == nil {
		return model.NewError(model.CodeUnavailable, i18n.T(s.localeOf(r), "workbench.unavailable", nil))
	}
	if v := q.Get("status"); v != "" {
		f.Status = model.SessionStatus(v)
		if !validSessionStatus(f.Status) {
			return badValue("status", v)
		}
	}
	list, err := s.deps.Sessions.List(r.Context(), f, page)
	if err != nil {
		return err
	}
	items := make([]session.View, 0, len(list))
	for _, sess := range list {
		items = append(items, s.sessionView(sess))
	}
	writeItems(w, items)
	return nil
}

type sessionCreateRequest struct {
	ProjectID string       `json:"project_id,omitempty"`
	AgentID   string       `json:"agent_id"`
	Title     string       `json:"title"`
	Origin    model.Origin `json:"origin"`
}

// handleCreateSession 创建会话。origin 是发起入口（OpenAPI：desktop | cli，缺省 desktop）；
// channel / job / acp 会话只由网关、调度器等内核入口按路由键创建，不经本端点。
func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) error {
	var req sessionCreateRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if strings.TrimSpace(req.AgentID) == "" {
		return missingField("agent_id")
	}
	origin := model.Origin(strings.TrimSpace(string(req.Origin)))
	switch origin {
	case "":
		origin = model.OriginDesktop
	case model.OriginDesktop, model.OriginCLI:
	default:
		return badValue("origin", string(req.Origin))
	}
	if req.ProjectID != "" && s.deps.Workbench == nil {
		return model.NewError(model.CodeUnavailable, i18n.T(s.localeOf(r), "workbench.unavailable", nil))
	}
	sess, err := s.deps.Sessions.CreateInProject(r.Context(), strings.TrimSpace(req.AgentID), origin, req.Title, req.ProjectID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, s.sessionView(sess))
	return nil
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) error {
	sess, err := s.deps.Sessions.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.sessionView(sess))
	return nil
}

type sessionPatchRequest struct {
	Title  *string              `json:"title"`
	Status *model.SessionStatus `json:"status"`
}

// handleUpdateSession 重命名或归档会话。OpenAPI 中请求体是可选的：空请求体（或 {}）视为不修改，
// 返回当前会话（200），不存在时 404。
func (s *Server) handleUpdateSession(w http.ResponseWriter, r *http.Request) error {
	var req sessionPatchRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		return err
	}
	if req.Status != nil && !validSessionStatus(*req.Status) {
		return badValue("status", string(*req.Status))
	}
	if req.Title == nil && req.Status == nil {
		sess, err := s.deps.Sessions.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, s.sessionView(sess))
		return nil
	}
	sess, err := s.deps.Sessions.Update(r.Context(), r.PathValue("id"), session.SessionUpdate{
		Title:  req.Title,
		Status: req.Status,
	})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.sessionView(sess))
	return nil
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) error {
	if err := s.deps.Sessions.Delete(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	writeNoContent(w)
	return nil
}

func (s *Server) handleListMessages(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	var after int64
	if v := q.Get("after_seq"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			return badValue("after_seq", v)
		}
		after = n
	}
	// limit 只按通用分页规则解析（缺省 100、上限 500，与 OpenAPI 一致）；session 包的上限与之相同，
	// 传输层不再另设一层截断。messages 用 after_seq 翻页，offset 不适用。
	page, err := parsePage(r)
	if err != nil {
		return err
	}
	items, err := s.deps.Sessions.Messages(r.Context(), r.PathValue("id"), after, page.Limit)
	if err != nil {
		return err
	}
	writeItems(w, items)
	return nil
}

type sendRequest struct {
	MaterialIDs []string           `json:"material_ids,omitempty"`
	Text        string             `json:"text"`
	Attachments []model.Attachment `json:"attachments"`
}

// handleSendMessage 追加用户消息并发起运行，返回 202 + Run。
// session 包在运行已落盘之后出错时同时返回失败的 Run 与错误：此时按错误码回状态，信封里带上 run。
func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request) error {
	var req sendRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	id := r.PathValue("id")
	var sources json.RawMessage
	if len(req.MaterialIDs) > 0 {
		if s.deps.Workbench == nil {
			return model.NewError(model.CodeUnavailable, i18n.T(s.localeOf(r), "workbench.unavailable", nil))
		}
		materialAtts, meta, err := s.deps.Workbench.ResolveMaterials(r.Context(), id, req.MaterialIDs)
		if err != nil {
			return err
		}
		sources = meta
		req.Attachments = append(req.Attachments, materialAtts...)
		seen := map[string]bool{}
		unique := []model.Attachment{}
		for _, att := range req.Attachments {
			key := strings.ReplaceAll(strings.TrimSpace(att.Path), `\`, "/")
			if !seen[key] {
				seen[key] = true
				unique = append(unique, att)
			}
		}
		req.Attachments = unique
	}
	atts, err := s.checkAttachments(r.Context(), id, req.Attachments)
	if err != nil {
		return err
	}
	p, _ := identity.FromContext(r.Context())
	run, err := s.deps.Sessions.Send(r.Context(), session.SendRequest{
		SessionID:       id,
		Text:            req.Text,
		Attachments:     atts,
		MaterialSources: sources,
		Trigger:         model.TriggerUser,
		Principal:       p,
		Untrusted:       false,
	})
	if err != nil {
		if run != nil {
			s.writeErrorFull(w, r, 0, err, run)
			return nil
		}
		return err
	}
	writeJSON(w, http.StatusAccepted, s.runView(r, run))
	return nil
}

// checkAttachments 确认每个附件都是该会话智能体工作区 inbound/ 下真实存在的文件，
// 并以服务端看到的路径与大小为准（客户端声明的路径不可信）。
func (s *Server) checkAttachments(ctx context.Context, sessionID string, in []model.Attachment) ([]model.Attachment, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) > maxAttachments {
		return nil, invalid("too_many_attachments", "附件太多", map[string]any{"max": maxAttachments})
	}
	ws, err := s.sessionWorkspace(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]model.Attachment, 0, len(in))
	for i, a := range in {
		p := strings.ReplaceAll(strings.TrimSpace(a.Path), `\`, "/")
		if !strings.HasPrefix(p, workspace.InboundDir+"/") {
			return nil, invalid("attachment_outside_inbound", "附件必须位于工作区 inbound/ 目录",
				map[string]any{"index": i, "path": a.Path})
		}
		e, serr := ws.Stat(ctx, p)
		if serr != nil {
			return nil, serr
		}
		if e.IsDir {
			return nil, invalid("attachment_is_directory", "附件不能是目录", map[string]any{"index": i, "path": a.Path})
		}
		name := strings.TrimSpace(a.Name)
		if name == "" {
			name = e.Name
		}
		out = append(out, model.Attachment{Name: name, Path: e.Path, MIME: a.MIME, Size: e.Size})
	}
	return out, nil
}

// sessionWorkspace 打开会话所属智能体的工作区。
func (s *Server) sessionWorkspace(ctx context.Context, sessionID string) (*workspace.Workspace, error) {
	sess, err := s.deps.Sessions.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	a, err := s.deps.Agents.Get(ctx, sess.AgentID)
	if err != nil {
		return nil, err
	}
	return s.deps.Workspaces.Open(ctx, a.ID, a.WorkspaceDir)
}

// handleUploadAttachment 把 multipart 字段 file 流式写入智能体工作区 inbound/（不整体读入内存），返回 201。
func (s *Server) handleUploadAttachment(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	ws, err := s.sessionWorkspace(ctx, r.PathValue("id"))
	if err != nil {
		return err
	}
	mr, err := r.MultipartReader()
	if err != nil {
		e := invalid("multipart_required", "请求体必须是 multipart/form-data", nil)
		e.Err = err
		return e
	}
	for {
		part, perr := mr.NextPart()
		if errors.Is(perr, io.EOF) {
			return invalid("file_missing", "缺少 file 字段", nil)
		}
		if perr != nil {
			var mbe *http.MaxBytesError
			if errors.As(perr, &mbe) {
				return bodyTooLarge(mbe.Limit, perr)
			}
			e := invalid("malformed_multipart", "multipart 请求体格式不正确", nil)
			e.Err = perr
			return e
		}
		if part.FormName() != "file" {
			_ = part.Close()
			continue
		}
		att, serr := ws.SaveInbound(ctx, part.FileName(), part, s.opts.MaxUploadBytes)
		_ = part.Close()
		if serr != nil {
			var mbe *http.MaxBytesError
			if errors.As(serr, &mbe) {
				return bodyTooLarge(mbe.Limit, serr)
			}
			return serr
		}
		writeJSON(w, http.StatusCreated, att)
		return nil
	}
}

func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) error {
	run, err := s.deps.Sessions.GetRun(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.runView(r, run))
	return nil
}

// handleCancelRun 幂等：已是终态时返回当前记录。
func (s *Server) handleCancelRun(w http.ResponseWriter, r *http.Request) error {
	run, err := s.deps.Sessions.Cancel(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.runView(r, run))
	return nil
}
