package client

import (
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// items 是列表接口的 {"items": [...]}。
type items[T any] struct {
	Items []T `json:"items"`
}

func listOf[T any](ctx context.Context, c *Client, path string, q url.Values) ([]T, error) {
	var out items[T]
	if err := c.call(ctx, request{method: http.MethodGet, path: path, query: q}, &out); err != nil {
		return nil, err
	}
	if out.Items == nil {
		out.Items = []T{}
	}
	return out.Items, nil
}

func one[T any](ctx context.Context, c *Client, method, path string, body any) (*T, error) {
	var out T
	if err := c.call(ctx, request{method: method, path: path, body: body}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) noContent(ctx context.Context, method, path string) error {
	return c.call(ctx, request{method: method, path: path}, nil)
}

// esc 转义路径中的标识符。
func esc(id string) string { return url.PathEscape(id) }

func pageQuery(q url.Values, limit, offset int) url.Values {
	if q == nil {
		q = url.Values{}
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if offset > 0 {
		q.Set("offset", strconv.Itoa(offset))
	}
	return q
}

func setIf(q url.Values, key, value string) {
	if value != "" {
		q.Set(key, value)
	}
}

// ── system ───────────────────────────────────────────────────────────────────

// Health 调用免鉴权的 GET /healthz。
func (c *Client) Health(ctx context.Context) error {
	var out struct {
		Status string `json:"status"`
	}
	if err := c.call(ctx, request{method: http.MethodGet, path: "/healthz", noAuth: true}, &out); err != nil {
		return err
	}
	if out.Status != "ok" {
		return model.Errorf(model.CodeUnavailable, "健康检查返回 %q", out.Status)
	}
	return nil
}

// Version 返回版本与工作进程状态。
func (c *Client) Version(ctx context.Context) (*VersionInfo, error) {
	return one[VersionInfo](ctx, c, http.MethodGet, "/api/v1/version", nil)
}

// Shutdown 请求守护进程优雅关闭（POST /api/v1/system/shutdown，经鉴权）。服务端返回 202 即表示已开始关闭，
// 本方法不等待进程退出；调用方应自行确认（例如轮询进程是否存在），超时后再考虑强制结束。
// 守护进程不支持经 API 关闭时返回 CodeUnavailable；更早版本的守护进程没有该端点，返回 CodeNotFound。
func (c *Client) Shutdown(ctx context.Context) error {
	return c.call(ctx, request{method: http.MethodPost, path: "/api/v1/system/shutdown"}, nil)
}

// ── agents ───────────────────────────────────────────────────────────────────

// ListAgents 列出智能体。
func (c *Client) ListAgents(ctx context.Context) ([]*model.Agent, error) {
	return listOf[*model.Agent](ctx, c, "/api/v1/agents", nil)
}

// CreateAgent 创建智能体。
func (c *Client) CreateAgent(ctx context.Context, in AgentCreate) (*model.Agent, error) {
	return one[model.Agent](ctx, c, http.MethodPost, "/api/v1/agents", in)
}

// GetAgent 获取智能体。
func (c *Client) GetAgent(ctx context.Context, id string) (*model.Agent, error) {
	return one[model.Agent](ctx, c, http.MethodGet, "/api/v1/agents/"+esc(id), nil)
}

// UpdateAgent 部分更新智能体。
func (c *Client) UpdateAgent(ctx context.Context, id string, in AgentPatch) (*model.Agent, error) {
	return one[model.Agent](ctx, c, http.MethodPatch, "/api/v1/agents/"+esc(id), in)
}

// DeleteAgent 删除智能体（级联删除其会话；工作区文件保留）。
func (c *Client) DeleteAgent(ctx context.Context, id string) error {
	return c.noContent(ctx, http.MethodDelete, "/api/v1/agents/"+esc(id))
}

// AgentTools 返回智能体当前可用的工具声明（内置 + 已启用扩展）。
func (c *Client) AgentTools(ctx context.Context, id string) ([]model.ToolSpec, error) {
	return listOf[model.ToolSpec](ctx, c, "/api/v1/agents/"+esc(id)+"/tools", nil)
}

// ── providers ────────────────────────────────────────────────────────────────

// ListProviders 列出供应商（不含密钥）。
func (c *Client) ListProviders(ctx context.Context) ([]ProviderView, error) {
	return listOf[ProviderView](ctx, c, "/api/v1/providers", nil)
}

// CreateProvider 创建供应商。
func (c *Client) CreateProvider(ctx context.Context, in ProviderCreate) (*ProviderView, error) {
	return one[ProviderView](ctx, c, http.MethodPost, "/api/v1/providers", in)
}

// GetProvider 获取供应商。
func (c *Client) GetProvider(ctx context.Context, id string) (*ProviderView, error) {
	return one[ProviderView](ctx, c, http.MethodGet, "/api/v1/providers/"+esc(id), nil)
}

// UpdateProvider 部分更新供应商（APIKey 非 nil 时轮换或清除密钥）。
func (c *Client) UpdateProvider(ctx context.Context, id string, in ProviderPatch) (*ProviderView, error) {
	return one[ProviderView](ctx, c, http.MethodPatch, "/api/v1/providers/"+esc(id), in)
}

// DeleteProvider 删除供应商；仍被智能体引用时返回 CodeConflict（Details 列出引用者）。
func (c *Client) DeleteProvider(ctx context.Context, id string) error {
	return c.noContent(ctx, http.MethodDelete, "/api/v1/providers/"+esc(id))
}

// TestProvider 做连通性测试；成功时服务端刷新模型列表。
func (c *Client) TestProvider(ctx context.Context, id string) (*ProviderTestResult, error) {
	return one[ProviderTestResult](ctx, c, http.MethodPost, "/api/v1/providers/"+esc(id)+"/test", nil)
}

// ── sessions ─────────────────────────────────────────────────────────────────

// ListSessions 列出会话。
func (c *Client) ListSessions(ctx context.Context, q SessionQuery) ([]SessionView, error) {
	v := url.Values{}
	setIf(v, "agent_id", q.AgentID)
	setIf(v, "status", string(q.Status))
	return listOf[SessionView](ctx, c, "/api/v1/sessions", pageQuery(v, q.Limit, q.Offset))
}

// CreateSession 为智能体创建桌面会话（origin 缺省为 desktop）。
func (c *Client) CreateSession(ctx context.Context, agentID, title string) (*SessionView, error) {
	return c.CreateSessionWith(ctx, SessionCreate{AgentID: agentID, Title: title})
}

// CreateSessionWith 按 in 创建会话；命令行等非桌面入口用它传 Origin（例如 model.OriginCLI）。
func (c *Client) CreateSessionWith(ctx context.Context, in SessionCreate) (*SessionView, error) {
	return one[SessionView](ctx, c, http.MethodPost, "/api/v1/sessions", in)
}

// GetSession 获取会话。
func (c *Client) GetSession(ctx context.Context, id string) (*SessionView, error) {
	return one[SessionView](ctx, c, http.MethodGet, "/api/v1/sessions/"+esc(id), nil)
}

// UpdateSession 重命名或归档会话。
func (c *Client) UpdateSession(ctx context.Context, id string, in SessionPatch) (*SessionView, error) {
	return one[SessionView](ctx, c, http.MethodPatch, "/api/v1/sessions/"+esc(id), in)
}

// DeleteSession 删除会话及其消息与运行记录。
func (c *Client) DeleteSession(ctx context.Context, id string) error {
	return c.noContent(ctx, http.MethodDelete, "/api/v1/sessions/"+esc(id))
}

// ListMessages 读取 Seq 大于 afterSeq 的消息；limit 为 0 时用服务端缺省值。
func (c *Client) ListMessages(ctx context.Context, sessionID string, afterSeq int64, limit int) ([]*model.Message, error) {
	v := url.Values{}
	if afterSeq > 0 {
		v.Set("after_seq", strconv.FormatInt(afterSeq, 10))
	}
	return listOf[*model.Message](ctx, c, "/api/v1/sessions/"+esc(sessionID)+"/messages", pageQuery(v, limit, 0))
}

// SendMessage 发送用户消息并发起一次运行，返回受理的运行（queued / running）。
//
// 运行记录已落盘之后才出错时（例如运行时不可用），同时返回失败的运行与错误，调用方可据此展示 run_id。
func (c *Client) SendMessage(ctx context.Context, sessionID, text string, attachments []model.Attachment) (*model.Run, error) {
	body := map[string]any{"text": text}
	if len(attachments) > 0 {
		body["attachments"] = attachments
	}
	resp, cancel, err := c.send(ctx, request{
		method: http.MethodPost, path: "/api/v1/sessions/" + esc(sessionID) + "/messages", body: body,
	})
	if err != nil {
		return nil, err
	}
	defer cancel()
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		apiErr, run := decodeErrorResponse(resp)
		return run, apiErr
	}
	var run model.Run
	if err := decodeJSONBody(ctx, resp.Body, &run); err != nil {
		return nil, err
	}
	return &run, nil
}

// UploadAttachment 把 r 的内容作为附件上传到会话所属智能体的工作区 inbound/，流式发送不整体读入内存。
func (c *Client) UploadAttachment(ctx context.Context, sessionID, filename string, r io.Reader) (*model.Attachment, error) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	done := make(chan struct{})
	go func() {
		defer close(done)
		fw, err := mw.CreateFormFile("file", filename)
		if err == nil {
			_, err = io.Copy(fw, r)
		}
		if err == nil {
			err = mw.Close()
		}
		_ = pw.CloseWithError(err)
	}()
	var att model.Attachment
	err := c.call(ctx, request{
		method:      http.MethodPost,
		path:        "/api/v1/sessions/" + esc(sessionID) + "/attachments",
		raw:         pr,
		contentType: mw.FormDataContentType(),
		timeout:     uploadTimeout,
	}, &att)
	// 请求提前失败时关闭读端让写入方退出，并等它结束，返回之后不再读取调用方的 r。
	_ = pr.Close()
	<-done
	if err != nil {
		return nil, err
	}
	return &att, nil
}

// ── runs ─────────────────────────────────────────────────────────────────────

// GetRun 获取运行；失败原因中的机器常量已按 Accept-Language 本地化。
func (c *Client) GetRun(ctx context.Context, id string) (*model.Run, error) {
	return one[model.Run](ctx, c, http.MethodGet, "/api/v1/runs/"+esc(id), nil)
}

// CancelRun 取消运行（幂等；已是终态时返回当前记录）。
func (c *Client) CancelRun(ctx context.Context, id string) (*model.Run, error) {
	return one[model.Run](ctx, c, http.MethodPost, "/api/v1/runs/"+esc(id)+"/cancel", nil)
}

// ── approvals ────────────────────────────────────────────────────────────────

// ListApprovals 列出审批请求；status 为空时服务端缺省 pending。
func (c *Client) ListApprovals(ctx context.Context, status model.ApprovalStatus, limit, offset int) ([]*model.ApprovalRequest, error) {
	v := url.Values{}
	setIf(v, "status", string(status))
	return listOf[*model.ApprovalRequest](ctx, c, "/api/v1/approvals", pageQuery(v, limit, offset))
}

// DecideApproval 提交审批决定（Scope 为空时按 once）。
func (c *Client) DecideApproval(ctx context.Context, id string, d model.Decision) (*model.ApprovalRequest, error) {
	body := map[string]any{"approve": d.Approve}
	if d.Scope != "" {
		body["scope"] = d.Scope
	}
	if d.Reason != "" {
		body["reason"] = d.Reason
	}
	return one[model.ApprovalRequest](ctx, c, http.MethodPost, "/api/v1/approvals/"+esc(id)+"/decision", body)
}

// ── jobs ─────────────────────────────────────────────────────────────────────

// ListJobs 列出定时任务；agentID 为空时列出全部。
func (c *Client) ListJobs(ctx context.Context, agentID string) ([]*model.Job, error) {
	v := url.Values{}
	setIf(v, "agent_id", agentID)
	return listOf[*model.Job](ctx, c, "/api/v1/jobs", v)
}

// CreateJob 创建定时任务。
func (c *Client) CreateJob(ctx context.Context, in JobCreate) (*model.Job, error) {
	return one[model.Job](ctx, c, http.MethodPost, "/api/v1/jobs", in)
}

// GetJob 获取定时任务。
func (c *Client) GetJob(ctx context.Context, id string) (*model.Job, error) {
	return one[model.Job](ctx, c, http.MethodGet, "/api/v1/jobs/"+esc(id), nil)
}

// UpdateJob 部分更新定时任务。
func (c *Client) UpdateJob(ctx context.Context, id string, in JobPatch) (*model.Job, error) {
	return one[model.Job](ctx, c, http.MethodPatch, "/api/v1/jobs/"+esc(id), in)
}

// DeleteJob 删除定时任务。
func (c *Client) DeleteJob(ctx context.Context, id string) error {
	return c.noContent(ctx, http.MethodDelete, "/api/v1/jobs/"+esc(id))
}

// RunJob 立即触发一次（异步执行；服务端返回 202）。
func (c *Client) RunJob(ctx context.Context, id string) error {
	return c.noContent(ctx, http.MethodPost, "/api/v1/jobs/"+esc(id)+"/run")
}

// ── extensions ───────────────────────────────────────────────────────────────

// ListExtensions 列出扩展；kind 为空时列出全部。
func (c *Client) ListExtensions(ctx context.Context, kind model.ExtensionKind) ([]*model.Extension, error) {
	v := url.Values{}
	setIf(v, "kind", string(kind))
	return listOf[*model.Extension](ctx, c, "/api/v1/extensions", v)
}

// InstallExtension 从守护进程所在机器上的本地目录（绝对路径）安装技能（SKILL.md）或插件（extension.json）。
func (c *Client) InstallExtension(ctx context.Context, dir string) (*model.Extension, error) {
	return one[model.Extension](ctx, c, http.MethodPost, "/api/v1/extensions", map[string]string{"path": dir})
}

// RegisterMCP 登记一个 MCP 服务（默认停用；Env / Headers 的值可以是 "secret:<ref>" 引用）。
func (c *Client) RegisterMCP(ctx context.Context, name string, cfg model.MCPServerConfig) (*model.Extension, error) {
	return one[model.Extension](ctx, c, http.MethodPost, "/api/v1/extensions",
		map[string]any{"name": name, "mcp": cfg})
}

// GetExtension 获取扩展。
func (c *Client) GetExtension(ctx context.Context, id string) (*model.Extension, error) {
	return one[model.Extension](ctx, c, http.MethodGet, "/api/v1/extensions/"+esc(id), nil)
}

// SetExtensionEnabled 启用或停用扩展。
func (c *Client) SetExtensionEnabled(ctx context.Context, id string, enabled bool) (*model.Extension, error) {
	return one[model.Extension](ctx, c, http.MethodPatch, "/api/v1/extensions/"+esc(id),
		map[string]bool{"enabled": enabled})
}

// UninstallExtension 卸载扩展。
func (c *Client) UninstallExtension(ctx context.Context, id string) error {
	return c.noContent(ctx, http.MethodDelete, "/api/v1/extensions/"+esc(id))
}

// ExtensionStatus 返回扩展的运行时状态。
func (c *Client) ExtensionStatus(ctx context.Context, id string) (*ExtensionStatus, error) {
	return one[ExtensionStatus](ctx, c, http.MethodGet, "/api/v1/extensions/"+esc(id)+"/status", nil)
}

// AcceptExtensionTools 确认并重新固定扩展的工具声明，返回最新状态。
func (c *Client) AcceptExtensionTools(ctx context.Context, id string) (*ExtensionStatus, error) {
	return one[ExtensionStatus](ctx, c, http.MethodPost, "/api/v1/extensions/"+esc(id)+"/accept-tools", nil)
}

// ── channels ─────────────────────────────────────────────────────────────────

// ListChannels 列出 IM 通道；agentID 为空时列出全部。
func (c *Client) ListChannels(ctx context.Context, agentID string) ([]ChannelView, error) {
	v := url.Values{}
	setIf(v, "agent_id", agentID)
	return listOf[ChannelView](ctx, c, "/api/v1/channels", v)
}

// CreateChannel 创建通道。
func (c *Client) CreateChannel(ctx context.Context, in ChannelCreate) (*ChannelView, error) {
	return one[ChannelView](ctx, c, http.MethodPost, "/api/v1/channels", in)
}

// GetChannel 获取通道（含运行状态）。
func (c *Client) GetChannel(ctx context.Context, id string) (*ChannelView, error) {
	return one[ChannelView](ctx, c, http.MethodGet, "/api/v1/channels/"+esc(id), nil)
}

// UpdateChannel 部分更新通道（修改后服务端自动重载适配器）。
func (c *Client) UpdateChannel(ctx context.Context, id string, in ChannelPatch) (*ChannelView, error) {
	return one[ChannelView](ctx, c, http.MethodPatch, "/api/v1/channels/"+esc(id), in)
}

// DeleteChannel 删除通道并停止适配器。
func (c *Client) DeleteChannel(ctx context.Context, id string) error {
	return c.noContent(ctx, http.MethodDelete, "/api/v1/channels/"+esc(id))
}

// ReloadChannel 重启通道的适配器。
func (c *Client) ReloadChannel(ctx context.Context, id string) (*ChannelView, error) {
	return one[ChannelView](ctx, c, http.MethodPost, "/api/v1/channels/"+esc(id)+"/reload", nil)
}

// ── settings / audit ─────────────────────────────────────────────────────────

// GetSettings 返回全局设置。
func (c *Client) GetSettings(ctx context.Context) (*Settings, error) {
	return one[Settings](ctx, c, http.MethodGet, "/api/v1/settings", nil)
}

// UpdateSettings 部分更新全局设置，返回更新后的值。
func (c *Client) UpdateSettings(ctx context.Context, in SettingsPatch) (*Settings, error) {
	return one[Settings](ctx, c, http.MethodPatch, "/api/v1/settings", in)
}

// ListAudit 查询审计记录（按时间降序）。
func (c *Client) ListAudit(ctx context.Context, q AuditQuery) ([]*model.AuditEntry, error) {
	v := url.Values{}
	setIf(v, "agent_id", q.AgentID)
	setIf(v, "session_id", q.SessionID)
	setIf(v, "run_id", q.RunID)
	setIf(v, "actor", q.Actor)
	setIf(v, "action", q.Action)
	if !q.Since.IsZero() {
		v.Set("since", q.Since.UTC().Format(time.RFC3339Nano))
	}
	return listOf[*model.AuditEntry](ctx, c, "/api/v1/audit", pageQuery(v, q.Limit, q.Offset))
}
