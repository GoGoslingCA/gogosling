package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/i18n"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/provider"
	"github.com/GoGoslingCA/gogosling/internal/server/servertest"
)

func TestAgentsCRUD(t *testing.T) {
	h := newHarness(t)
	p, err := h.env.SeedProvider(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}

	// 缺少名称 → invalid，details 带领域层说明。
	env := mustError(t, h.do(http.MethodPost, "/api/v1/agents", map[string]any{
		"model": map[string]string{"provider_id": p.ID, "model": "m"},
	}), http.StatusBadRequest, model.CodeInvalid)
	if env.Error.Details["detail"] == nil {
		t.Errorf("领域层校验错误应在 details.detail 里给出说明: %v", env.Error.Details)
	}

	rec := h.do(http.MethodPost, "/api/v1/agents", map[string]any{
		"name":     "助手",
		"model":    map[string]string{"provider_id": p.ID, "model": "m"},
		"autonomy": "ask_all",
	})
	mustStatus(t, rec, http.StatusCreated)
	a := decodeBody[model.Agent](t, rec)
	if a.ID == "" || a.Name != "助手" || a.Autonomy != model.AutonomyAskAll || !a.Enabled {
		t.Fatalf("创建结果不符: %+v", a)
	}
	defaultTools := a.Tools

	list := decodeBody[items[model.Agent]](t, h.do(http.MethodGet, "/api/v1/agents", nil))
	if len(list.Items) != 1 || list.Items[0].ID != a.ID {
		t.Fatalf("列表 = %+v", list.Items)
	}
	got := decodeBody[model.Agent](t, h.do(http.MethodGet, "/api/v1/agents/"+a.ID, nil))
	if got.ID != a.ID {
		t.Fatalf("获取结果 = %+v", got)
	}

	// PATCH：tools 缺席不修改；[] 清空；null 重置为默认。
	rec = h.do(http.MethodPatch, "/api/v1/agents/"+a.ID, map[string]any{"description": "说明"})
	mustStatus(t, rec, http.StatusOK)
	if u := decodeBody[model.Agent](t, rec); u.Description != "说明" || len(u.Tools) != len(defaultTools) {
		t.Fatalf("缺席的 tools 不应被修改: %+v", u)
	}
	rec = h.do(http.MethodPatch, "/api/v1/agents/"+a.ID, `{"tools":[]}`)
	mustStatus(t, rec, http.StatusOK)
	stored, err := h.env.Store.GetAgent(context.Background(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Tools == nil || len(stored.Tools) != 0 {
		t.Fatalf("tools=[] 应清空内置工具（非 nil 空切片），实际 %#v", stored.Tools)
	}
	toolsResp := decodeBody[items[model.ToolSpec]](t, h.do(http.MethodGet, "/api/v1/agents/"+a.ID+"/tools", nil))
	if len(toolsResp.Items) != 0 {
		t.Fatalf("清空后不应有内置工具: %+v", toolsResp.Items)
	}
	rec = h.do(http.MethodPatch, "/api/v1/agents/"+a.ID, `{"tools":null}`)
	mustStatus(t, rec, http.StatusOK)
	toolsResp = decodeBody[items[model.ToolSpec]](t, h.do(http.MethodGet, "/api/v1/agents/"+a.ID+"/tools", nil))
	if len(toolsResp.Items) == 0 {
		t.Fatal("tools=null 应重置为默认工具集")
	}
	for _, spec := range toolsResp.Items {
		if spec.Name == "" || spec.Source == "" || spec.Risk == "" {
			t.Errorf("工具声明不完整: %+v", spec)
		}
	}

	mustError(t, h.do(http.MethodPatch, "/api/v1/agents/"+a.ID, map[string]any{"autonomy": "yolo"}),
		http.StatusBadRequest, model.CodeInvalid)

	rec = h.do(http.MethodDelete, "/api/v1/agents/"+a.ID, nil)
	mustStatus(t, rec, http.StatusNoContent)
	mustError(t, h.do(http.MethodGet, "/api/v1/agents/"+a.ID, nil), http.StatusNotFound, model.CodeNotFound)
	mustError(t, h.do(http.MethodGet, "/api/v1/agents/"+a.ID+"/tools", nil), http.StatusNotFound, model.CodeNotFound)
}

func TestProvidersNeverExposeSecrets(t *testing.T) {
	h := newHarness(t)
	const key = "sk-test-SUPERSECRET-1234567890"
	const rotated = "sk-test-ROTATED-0987654321"

	// 连通性测试的模型列表端点（本机假服务）。
	models := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+rotated {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"m-1"},{"id":"m-2"}]}`))
	}))
	defer models.Close()

	var bodies []string
	record := func(rec *httptest.ResponseRecorder) *httptest.ResponseRecorder {
		bodies = append(bodies, rec.Body.String())
		return rec
	}

	rec := record(h.do(http.MethodPost, "/api/v1/providers", map[string]any{
		"name": "OpenAI 兼容", "kind": "openai_compatible", "base_url": models.URL + "/v1",
		"api_key": key, "default_model": "m-1",
	}))
	mustStatus(t, rec, http.StatusCreated)
	v := decodeBody[provider.View](t, rec)
	if !v.HasSecret {
		t.Fatal("创建时给了 api_key，has_secret 应为 true")
	}
	if strings.Contains(rec.Body.String(), "secret_ref") || strings.Contains(rec.Body.String(), "api_key") {
		t.Fatalf("响应不应包含 secret_ref / api_key 字段: %s", rec.Body.String())
	}

	record(h.do(http.MethodGet, "/api/v1/providers", nil))
	record(h.do(http.MethodGet, "/api/v1/providers/"+v.ID, nil))

	// 缺席不动。
	rec = record(h.do(http.MethodPatch, "/api/v1/providers/"+v.ID, map[string]any{"name": "改名"}))
	mustStatus(t, rec, http.StatusOK)
	if u := decodeBody[provider.View](t, rec); !u.HasSecret || u.Name != "改名" {
		t.Fatalf("缺席的 api_key 不应改变密钥: %+v", u)
	}
	// 旧密钥连接测试失败（401），不是 Go 错误。
	rec = record(h.do(http.MethodPost, "/api/v1/providers/"+v.ID+"/test", nil))
	mustStatus(t, rec, http.StatusOK)
	if res := decodeBody[provider.TestResult](t, rec); res.OK {
		t.Fatalf("旧密钥不应通过测试: %+v", res)
	}
	// 非空轮换。
	rec = record(h.do(http.MethodPatch, "/api/v1/providers/"+v.ID, map[string]any{"api_key": rotated}))
	mustStatus(t, rec, http.StatusOK)
	rec = record(h.do(http.MethodPost, "/api/v1/providers/"+v.ID+"/test", nil))
	mustStatus(t, rec, http.StatusOK)
	res := decodeBody[provider.TestResult](t, rec)
	if !res.OK || len(res.Models) != 2 {
		t.Fatalf("轮换后的密钥应通过测试并返回模型列表: %+v", res)
	}
	// "" 清除。
	rec = record(h.do(http.MethodPatch, "/api/v1/providers/"+v.ID, map[string]any{"api_key": ""}))
	mustStatus(t, rec, http.StatusOK)
	if u := decodeBody[provider.View](t, rec); u.HasSecret {
		t.Fatal("api_key=\"\" 应清除密钥")
	}

	// 被智能体引用时删除返回 conflict，details 列出引用者。
	a := h.seedAgent("引用者")
	stored, err := h.env.Store.GetAgent(context.Background(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	env := mustError(t, record(h.do(http.MethodDelete, "/api/v1/providers/"+stored.Model.ProviderID, nil)),
		http.StatusConflict, model.CodeConflict)
	if env.Error.Details["agents"] == nil {
		t.Errorf("conflict 的 details 应列出引用的智能体: %v", env.Error.Details)
	}
	mustStatus(t, record(h.do(http.MethodDelete, "/api/v1/providers/"+v.ID, nil)), http.StatusNoContent)
	mustError(t, record(h.do(http.MethodGet, "/api/v1/providers/"+v.ID, nil)), http.StatusNotFound, model.CodeNotFound)

	for _, b := range bodies {
		if strings.Contains(b, key) || strings.Contains(b, rotated) {
			t.Fatalf("响应体泄露了密钥: %s", b)
		}
	}
	if logs := h.logs.String(); strings.Contains(logs, key) || strings.Contains(logs, rotated) {
		t.Fatal("日志泄露了密钥")
	}
}

func TestSessionsMessagesAndRuns(t *testing.T) {
	h := newHarness(t)
	a := h.seedAgent("聊天")

	mustError(t, h.do(http.MethodPost, "/api/v1/sessions", map[string]any{"title": "x"}), http.StatusBadRequest, model.CodeInvalid)
	sess := h.seedSession(a.ID)
	if sess.Origin != model.OriginDesktop || sess.AgentID != a.ID || sess.Status != model.SessionActive {
		t.Fatalf("会话 = %+v", sess)
	}

	run := h.send(sess.ID, "你好")
	if run.SessionID != sess.ID || run.Trigger != model.TriggerUser {
		t.Fatalf("运行 = %+v", run)
	}
	done := h.waitRun(run.ID, terminal)
	if done.Status != model.RunCompleted || done.Usage.OutputTokens != 5 {
		t.Fatalf("运行应完成并记录用量: %+v", done)
	}

	msgs := decodeBody[items[model.Message]](t, h.do(http.MethodGet, "/api/v1/sessions/"+sess.ID+"/messages", nil))
	if len(msgs.Items) != 2 || msgs.Items[0].Role != model.RoleUser || msgs.Items[1].Text() != servertest.EchoPrefix+"你好" {
		t.Fatalf("消息 = %+v", msgs.Items)
	}
	after := decodeBody[items[model.Message]](t, h.do(http.MethodGet,
		"/api/v1/sessions/"+sess.ID+"/messages?after_seq=1&limit=10", nil))
	if len(after.Items) != 1 || after.Items[0].Seq != 2 {
		t.Fatalf("after_seq 过滤错误: %+v", after.Items)
	}
	mustError(t, h.do(http.MethodGet, "/api/v1/sessions/"+sess.ID+"/messages?after_seq=-1", nil), http.StatusBadRequest, model.CodeInvalid)
	mustError(t, h.do(http.MethodPost, "/api/v1/sessions/"+sess.ID+"/messages", map[string]any{"text": "  "}),
		http.StatusBadRequest, model.CodeInvalid)

	// 在途运行：会话视图带 active_run_id，第二次发送 conflict，删除/归档 conflict。
	held := h.send(sess.ID, servertest.ScriptHold)
	h.waitRun(held.ID, func(r model.Run) bool { return r.Status == model.RunRunning })
	got := decodeBody[sessionView](t, h.do(http.MethodGet, "/api/v1/sessions/"+sess.ID, nil))
	if got.ActiveRunID != held.ID {
		t.Fatalf("active_run_id = %q，期望 %q", got.ActiveRunID, held.ID)
	}
	list := decodeBody[items[sessionView]](t, h.do(http.MethodGet, "/api/v1/sessions?agent_id="+a.ID, nil))
	if len(list.Items) != 1 || list.Items[0].ActiveRunID != held.ID {
		t.Fatalf("列表中的 active_run_id 不正确: %+v", list.Items)
	}
	mustError(t, h.do(http.MethodPost, "/api/v1/sessions/"+sess.ID+"/messages", map[string]any{"text": "again"}),
		http.StatusConflict, model.CodeConflict)
	mustError(t, h.do(http.MethodDelete, "/api/v1/sessions/"+sess.ID, nil), http.StatusConflict, model.CodeConflict)

	rec := h.do(http.MethodPost, "/api/v1/runs/"+held.ID+"/cancel", nil)
	mustStatus(t, rec, http.StatusOK)
	cancelled := h.waitRun(held.ID, terminal)
	if cancelled.Status != model.RunCancelled {
		t.Fatalf("取消后状态 = %s", cancelled.Status)
	}
	// 幂等：对终态运行再次取消返回当前记录。
	rec = h.do(http.MethodPost, "/api/v1/runs/"+held.ID+"/cancel", nil)
	mustStatus(t, rec, http.StatusOK)
	if r := decodeBody[model.Run](t, rec); r.Status != model.RunCancelled {
		t.Fatalf("重复取消返回 %s", r.Status)
	}
	got = decodeBody[sessionView](t, h.do(http.MethodGet, "/api/v1/sessions/"+sess.ID, nil))
	if got.ActiveRunID != "" {
		t.Fatalf("运行结束后不应再有 active_run_id: %+v", got)
	}

	// 失败原因的机器常量按请求语言本地化。
	failed := h.send(sess.ID, servertest.ScriptFail)
	h.waitRun(failed.ID, terminal)
	en := h.waitRun(failed.ID, terminal, withHeader("Accept-Language", "en"))
	if en.Status != model.RunFailed || en.Error != i18n.T("en", "run.error.run_timeout", nil) {
		t.Fatalf("英文失败原因 = %q", en.Error)
	}
	zh := h.waitRun(failed.ID, terminal, withHeader("Accept-Language", "zh"))
	if zh.Error != i18n.T("zh", "run.error.run_timeout", nil) {
		t.Fatalf("中文失败原因 = %q", zh.Error)
	}

	// 分页与过滤参数校验。
	mustError(t, h.do(http.MethodGet, "/api/v1/sessions?status=bogus", nil), http.StatusBadRequest, model.CodeInvalid)
	mustError(t, h.do(http.MethodGet, "/api/v1/sessions?limit=abc", nil), http.StatusBadRequest, model.CodeInvalid)
	mustStatus(t, h.do(http.MethodGet, "/api/v1/sessions?limit=9999&offset=0", nil), http.StatusOK)

	// 重命名、归档与删除。
	rec = h.do(http.MethodPatch, "/api/v1/sessions/"+sess.ID, map[string]any{"title": "新标题"})
	mustStatus(t, rec, http.StatusOK)
	if s := decodeBody[sessionView](t, rec); s.Title != "新标题" {
		t.Fatalf("重命名失败: %+v", s)
	}
	mustError(t, h.do(http.MethodPatch, "/api/v1/sessions/"+sess.ID, map[string]any{"status": "gone"}), http.StatusBadRequest, model.CodeInvalid)
	rec = h.do(http.MethodPatch, "/api/v1/sessions/"+sess.ID, map[string]any{"status": "archived"})
	mustStatus(t, rec, http.StatusOK)
	archived := decodeBody[items[sessionView]](t, h.do(http.MethodGet, "/api/v1/sessions?status=archived", nil))
	if len(archived.Items) != 1 {
		t.Fatalf("归档列表 = %+v", archived.Items)
	}
	mustError(t, h.do(http.MethodPost, "/api/v1/sessions/"+sess.ID+"/messages", map[string]any{"text": "x"}),
		http.StatusConflict, model.CodeConflict)
	mustStatus(t, h.do(http.MethodDelete, "/api/v1/sessions/"+sess.ID, nil), http.StatusNoContent)
	mustError(t, h.do(http.MethodGet, "/api/v1/sessions/"+sess.ID, nil), http.StatusNotFound, model.CodeNotFound)
	mustError(t, h.do(http.MethodGet, "/api/v1/runs/"+run.ID, nil), http.StatusNotFound, model.CodeNotFound)
}

func TestSendFailureAfterRunPersistedCarriesRun(t *testing.T) {
	h := newHarness(t)
	a := h.seedAgent("停用供应商")
	sess := h.seedSession(a.ID)
	// 停用供应商：凭据解析在写入任何记录之前失败，不应留下运行。
	stored, err := h.env.Store.GetAgent(context.Background(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	mustStatus(t, h.do(http.MethodPatch, "/api/v1/providers/"+stored.Model.ProviderID, map[string]any{"enabled": false}), http.StatusOK)
	env := mustError(t, h.do(http.MethodPost, "/api/v1/sessions/"+sess.ID+"/messages", map[string]any{"text": "hi"}),
		http.StatusServiceUnavailable, model.CodeUnavailable)
	if env.Run != nil {
		t.Fatalf("运行落盘之前的错误不应携带 run: %+v", env.Run)
	}
	msgs := decodeBody[items[model.Message]](t, h.do(http.MethodGet, "/api/v1/sessions/"+sess.ID+"/messages", nil))
	if len(msgs.Items) != 0 {
		t.Fatalf("凭据错误不应留下用户消息: %+v", msgs.Items)
	}
}

// uploadFile 以 multipart 上传附件。
func (h *harness) uploadFile(sessionID, name string, content []byte) *httptest.ResponseRecorder {
	h.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("note", "ignored")
	fw, err := mw.CreateFormFile("file", name)
	if err != nil {
		h.t.Fatal(err)
	}
	_, _ = fw.Write(content)
	_ = mw.Close()
	return h.do(http.MethodPost, "/api/v1/sessions/"+sessionID+"/attachments", buf.Bytes(),
		withHeader("Content-Type", mw.FormDataContentType()))
}

func TestAttachmentsUploadAndValidation(t *testing.T) {
	h := newHarness(t, func(c *harnessConfig) { c.srv.MaxUploadBytes = 1024 })
	a := h.seedAgent("附件")
	sess := h.seedSession(a.ID)

	rec := h.uploadFile(sess.ID, "../../报告.txt", []byte("hello attachment"))
	mustStatus(t, rec, http.StatusCreated)
	att := decodeBody[model.Attachment](t, rec)
	if !strings.HasPrefix(att.Path, "inbound/") || strings.Contains(att.Path, "..") || att.Size != int64(len("hello attachment")) {
		t.Fatalf("附件 = %+v", att)
	}
	stored, err := h.env.Store.GetAgent(context.Background(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := h.env.Deps.Workspaces.Open(context.Background(), stored.ID, stored.WorkspaceDir)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(ws.Root(), filepath.FromSlash(att.Path)))
	if err != nil || string(data) != "hello attachment" {
		t.Fatalf("附件内容 = %q, %v", data, err)
	}

	// 发送时附件以服务端看到的路径与大小为准。
	rec = h.do(http.MethodPost, "/api/v1/sessions/"+sess.ID+"/messages", map[string]any{
		"text":        "看附件",
		"attachments": []map[string]any{{"name": "x", "path": att.Path, "size": 1}},
	})
	mustStatus(t, rec, http.StatusAccepted)
	run := decodeBody[model.Run](t, rec)
	h.waitRun(run.ID, terminal)
	msgs := decodeBody[items[model.Message]](t, h.do(http.MethodGet, "/api/v1/sessions/"+sess.ID+"/messages", nil))
	var found bool
	for _, p := range msgs.Items[0].Parts {
		if p.Type == model.PartAttachment && p.Attachment != nil && p.Attachment.Size == att.Size {
			found = true
		}
	}
	if !found {
		t.Fatalf("用户消息应带上校正后的附件: %+v", msgs.Items[0])
	}

	// 声明 inbound/ 之外或不存在的路径被拒绝。
	for _, bad := range []string{"SOUL.md", "inbound/../SOUL.md", "/etc/passwd", "inbound/missing.txt"} {
		rec = h.do(http.MethodPost, "/api/v1/sessions/"+sess.ID+"/messages", map[string]any{
			"text": "x", "attachments": []map[string]any{{"name": "x", "path": bad, "size": 1}},
		})
		if rec.Code < 400 || rec.Code >= 500 {
			t.Errorf("附件路径 %q 应被拒绝，实际 %d %s", bad, rec.Code, rec.Body.String())
		}
	}

	// 超过附件上限 → invalid；不是 multipart → invalid；缺少 file 字段 → invalid。
	mustError(t, h.uploadFile(sess.ID, "big.bin", bytes.Repeat([]byte("x"), 2048)), http.StatusBadRequest, model.CodeInvalid)
	mustError(t, h.do(http.MethodPost, "/api/v1/sessions/"+sess.ID+"/attachments", map[string]any{}), http.StatusBadRequest, model.CodeInvalid)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("other", "x")
	_ = mw.Close()
	env := mustError(t, h.do(http.MethodPost, "/api/v1/sessions/"+sess.ID+"/attachments", buf.Bytes(),
		withHeader("Content-Type", mw.FormDataContentType())), http.StatusBadRequest, model.CodeInvalid)
	if env.Error.Details["reason"] != "file_missing" {
		t.Errorf("缺少 file 字段的 reason = %v", env.Error.Details["reason"])
	}
	mustError(t, h.uploadFile("01ARZ3NDEKTSV4RRFFQ69G5FAV", "a.txt", []byte("x")), http.StatusNotFound, model.CodeNotFound)
}

func TestApprovalDecisionFlow(t *testing.T) {
	h := newHarness(t)
	a := h.seedAgent("审批")
	sess := h.seedSession(a.ID)
	run := h.send(sess.ID, servertest.ScriptApprove)

	var pending []model.ApprovalRequest
	eventually(t, "出现待审批请求", func() bool {
		pending = decodeBody[items[model.ApprovalRequest]](t, h.do(http.MethodGet, "/api/v1/approvals", nil)).Items
		return len(pending) == 1
	})
	ap := pending[0]
	if ap.RunID != run.ID || ap.Status != model.ApprovalPending || ap.ToolCall.Name != "run_command" {
		t.Fatalf("待审批请求 = %+v", ap)
	}

	mustError(t, h.do(http.MethodPost, "/api/v1/approvals/"+ap.ID+"/decision", map[string]any{"scope": "once"}),
		http.StatusBadRequest, model.CodeInvalid)
	mustError(t, h.do(http.MethodPost, "/api/v1/approvals/"+ap.ID+"/decision", map[string]any{"approve": true, "scope": "forever"}),
		http.StatusBadRequest, model.CodeInvalid)

	rec := h.do(http.MethodPost, "/api/v1/approvals/"+ap.ID+"/decision", map[string]any{"approve": true})
	mustStatus(t, rec, http.StatusOK)
	decided := decodeBody[model.ApprovalRequest](t, rec)
	if decided.Status != model.ApprovalApproved || decided.DecidedBy != "local" || decided.Scope != model.ScopeOnce {
		t.Fatalf("审批结果 = %+v", decided)
	}
	done := h.waitRun(run.ID, terminal)
	if done.Status != model.RunCompleted {
		t.Fatalf("批准后运行应完成，实际 %s", done.Status)
	}
	// 不同的决定 → conflict；相同决定幂等。
	mustError(t, h.do(http.MethodPost, "/api/v1/approvals/"+ap.ID+"/decision", map[string]any{"approve": false}),
		http.StatusConflict, model.CodeConflict)
	mustStatus(t, h.do(http.MethodPost, "/api/v1/approvals/"+ap.ID+"/decision", map[string]any{"approve": true}), http.StatusOK)

	approved := decodeBody[items[model.ApprovalRequest]](t, h.do(http.MethodGet, "/api/v1/approvals?status=approved", nil))
	if len(approved.Items) != 1 {
		t.Fatalf("已批准列表 = %+v", approved.Items)
	}
	if len(decodeBody[items[model.ApprovalRequest]](t, h.do(http.MethodGet, "/api/v1/approvals", nil)).Items) != 0 {
		t.Fatal("决定之后不应再有待审批请求")
	}
	mustError(t, h.do(http.MethodGet, "/api/v1/approvals?status=maybe", nil), http.StatusBadRequest, model.CodeInvalid)
	mustError(t, h.do(http.MethodPost, "/api/v1/approvals/01ARZ3NDEKTSV4RRFFQ69G5FAV/decision", map[string]any{"approve": true}),
		http.StatusNotFound, model.CodeNotFound)

	// 审计：决定被记录，可按动作与时间过滤。
	audit := decodeBody[items[model.AuditEntry]](t, h.do(http.MethodGet, "/api/v1/audit?action=approval.decide&run_id="+run.ID, nil))
	if len(audit.Items) == 0 || audit.Items[0].Actor != "local" {
		t.Fatalf("审计记录 = %+v", audit.Items)
	}
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if n := len(decodeBody[items[model.AuditEntry]](t, h.do(http.MethodGet, "/api/v1/audit?since="+future, nil)).Items); n != 0 {
		t.Fatalf("since 在未来时不应有记录，实际 %d 条", n)
	}
	mustError(t, h.do(http.MethodGet, "/api/v1/audit?since=yesterday", nil), http.StatusBadRequest, model.CodeInvalid)
}

func TestApprovalCheckFromGoroutineThenDecideOverHTTP(t *testing.T) {
	h := newHarness(t)
	a := h.seedAgent("直接审批")
	sess := h.seedSession(a.ID)
	held := h.send(sess.ID, servertest.ScriptHold)
	h.waitRun(held.ID, func(r model.Run) bool { return r.Status == model.RunRunning })

	type result struct {
		allowed bool
		err     error
	}
	out := make(chan result, 1)
	go func() {
		v, err := h.env.Deps.Permissions.Check(context.Background(), approvalCheckRequest(a.ID, sess.ID, held.ID))
		out <- result{v.Allowed, err}
	}()
	var ap model.ApprovalRequest
	eventually(t, "Check 产生待审批请求", func() bool {
		list := decodeBody[items[model.ApprovalRequest]](t, h.do(http.MethodGet, "/api/v1/approvals?limit=1", nil)).Items
		if len(list) == 1 {
			ap = list[0]
		}
		return len(list) == 1
	})
	rec := h.do(http.MethodPost, "/api/v1/approvals/"+ap.ID+"/decision", map[string]any{"approve": false, "reason": "不行"})
	mustStatus(t, rec, http.StatusOK)
	select {
	case r := <-out:
		if r.allowed || r.err != nil {
			t.Fatalf("拒绝后 Check 应返回不允许且无错误，实际 %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Check 没有被 HTTP 决定唤醒")
	}
	mustStatus(t, h.do(http.MethodPost, "/api/v1/runs/"+held.ID+"/cancel", nil), http.StatusOK)
}

func TestJobsCRUDAndTrigger(t *testing.T) {
	h := newHarness(t)
	a := h.seedAgent("任务")

	mustError(t, h.do(http.MethodPost, "/api/v1/jobs", map[string]any{
		"agent_id": a.ID, "name": "坏表达式", "schedule": "61 * * * *", "kind": "push_text", "input": "x",
	}), http.StatusBadRequest, model.CodeInvalid)

	rec := h.do(http.MethodPost, "/api/v1/jobs", map[string]any{
		"agent_id": a.ID, "name": "早报", "schedule": "@daily", "kind": "push_text", "input": "早上好",
	})
	mustStatus(t, rec, http.StatusCreated)
	job := decodeBody[model.Job](t, rec)
	if !job.Enabled || job.MissedPolicy != model.MissedSkip || job.NextRunAt == nil {
		t.Fatalf("enabled 应缺省 true、missed_policy 缺省 skip: %+v", job)
	}
	rec = h.do(http.MethodPost, "/api/v1/jobs", map[string]any{
		"agent_id": a.ID, "name": "停用", "schedule": "@hourly", "kind": "run_agent", "input": "x",
		"enabled": false, "missed_policy": "run_once",
	})
	mustStatus(t, rec, http.StatusCreated)
	if j := decodeBody[model.Job](t, rec); j.Enabled || j.MissedPolicy != model.MissedRunOnce {
		t.Fatalf("显式给出的 enabled / missed_policy 应生效: %+v", j)
	}

	list := decodeBody[items[model.Job]](t, h.do(http.MethodGet, "/api/v1/jobs?agent_id="+a.ID, nil))
	if len(list.Items) != 2 {
		t.Fatalf("任务列表 = %+v", list.Items)
	}
	mustStatus(t, h.do(http.MethodGet, "/api/v1/jobs/"+job.ID, nil), http.StatusOK)

	rec = h.do(http.MethodPatch, "/api/v1/jobs/"+job.ID, map[string]any{"name": "晚报", "agent_id": a.ID})
	mustStatus(t, rec, http.StatusOK)
	if j := decodeBody[model.Job](t, rec); j.Name != "晚报" {
		t.Fatalf("改名失败: %+v", j)
	}
	other := h.seedAgent("另一个")
	env := mustError(t, h.do(http.MethodPatch, "/api/v1/jobs/"+job.ID, map[string]any{"agent_id": other.ID}),
		http.StatusBadRequest, model.CodeInvalid)
	if env.Error.Details["reason"] != "agent_id_immutable" {
		t.Errorf("修改 agent_id 的 reason = %v", env.Error.Details["reason"])
	}

	rec = h.do(http.MethodPost, "/api/v1/jobs/"+job.ID+"/run", nil)
	mustStatus(t, rec, http.StatusAccepted)
	mustError(t, h.do(http.MethodPost, "/api/v1/jobs/01ARZ3NDEKTSV4RRFFQ69G5FAV/run", nil), http.StatusNotFound, model.CodeNotFound)

	mustStatus(t, h.do(http.MethodDelete, "/api/v1/jobs/"+job.ID, nil), http.StatusNoContent)
	mustError(t, h.do(http.MethodGet, "/api/v1/jobs/"+job.ID, nil), http.StatusNotFound, model.CodeNotFound)
}

func TestExtensionsInstallStatusAndUninstall(t *testing.T) {
	h := newHarness(t)
	skillDir := filepath.Join(t.TempDir(), "hello-skill")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	skill := "---\nname: hello-skill\ndescription: 打招呼\n---\n\n# Hello\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skill), 0o600); err != nil {
		t.Fatal(err)
	}
	emptyDir := t.TempDir()

	mustError(t, h.do(http.MethodPost, "/api/v1/extensions", map[string]any{}), http.StatusBadRequest, model.CodeInvalid)
	mustError(t, h.do(http.MethodPost, "/api/v1/extensions", map[string]any{"path": "relative/dir"}), http.StatusBadRequest, model.CodeInvalid)
	env := mustError(t, h.do(http.MethodPost, "/api/v1/extensions", map[string]any{"path": emptyDir}), http.StatusBadRequest, model.CodeInvalid)
	if env.Error.Details["reason"] != "unknown_extension_layout" {
		t.Errorf("空目录的 reason = %v", env.Error.Details["reason"])
	}
	mustError(t, h.do(http.MethodPost, "/api/v1/extensions", map[string]any{"path": filepath.Join(emptyDir, "nope")}),
		http.StatusNotFound, model.CodeNotFound)
	mustError(t, h.do(http.MethodPost, "/api/v1/extensions", map[string]any{
		"path": skillDir, "mcp": map[string]any{"transport": "stdio", "command": "x"},
	}), http.StatusBadRequest, model.CodeInvalid)

	rec := h.do(http.MethodPost, "/api/v1/extensions", map[string]any{"path": skillDir})
	mustStatus(t, rec, http.StatusCreated)
	ext := decodeBody[model.Extension](t, rec)
	if ext.Kind != model.ExtensionSkill || !ext.Enabled {
		t.Fatalf("技能安装结果 = %+v", ext)
	}

	rec = h.do(http.MethodPost, "/api/v1/extensions", map[string]any{
		"name": "本地 MCP", "mcp": map[string]any{"transport": "stdio", "command": "mcp-server-demo", "env": map[string]string{"TOKEN": "secret:mcp-token"}},
	})
	mustStatus(t, rec, http.StatusCreated)
	mcpExt := decodeBody[model.Extension](t, rec)
	if mcpExt.Kind != model.ExtensionMCPServer || mcpExt.Enabled {
		t.Fatalf("MCP 登记结果（应默认停用）= %+v", mcpExt)
	}

	skills := decodeBody[items[model.Extension]](t, h.do(http.MethodGet, "/api/v1/extensions?kind=skill", nil))
	if len(skills.Items) != 1 {
		t.Fatalf("按类型过滤 = %+v", skills.Items)
	}
	mustError(t, h.do(http.MethodGet, "/api/v1/extensions?kind=theme", nil), http.StatusBadRequest, model.CodeInvalid)

	rec = h.do(http.MethodPatch, "/api/v1/extensions/"+ext.ID, map[string]any{"enabled": false})
	mustStatus(t, rec, http.StatusOK)
	if e := decodeBody[model.Extension](t, rec); e.Enabled {
		t.Fatal("停用失败")
	}
	rec = h.do(http.MethodPatch, "/api/v1/extensions/"+ext.ID, map[string]any{"enabled": true})
	mustStatus(t, rec, http.StatusOK)
	if e := decodeBody[model.Extension](t, rec); !e.Enabled {
		t.Fatal("启用失败")
	}
	mustStatus(t, h.do(http.MethodGet, "/api/v1/extensions/"+ext.ID, nil), http.StatusOK)

	rec = h.do(http.MethodGet, "/api/v1/extensions/"+mcpExt.ID+"/status", nil)
	mustStatus(t, rec, http.StatusOK)
	var st struct {
		ID           string `json:"id"`
		Connected    bool   `json:"connected"`
		ToolsChanged bool   `json:"tools_changed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.ID != mcpExt.ID || st.Connected || st.ToolsChanged {
		t.Fatalf("状态 = %+v", st)
	}
	mustError(t, h.do(http.MethodGet, "/api/v1/extensions/01ARZ3NDEKTSV4RRFFQ69G5FAV/status", nil), http.StatusNotFound, model.CodeNotFound)
	rec = h.do(http.MethodPost, "/api/v1/extensions/"+ext.ID+"/accept-tools", nil)
	mustStatus(t, rec, http.StatusOK)

	mustStatus(t, h.do(http.MethodDelete, "/api/v1/extensions/"+ext.ID, nil), http.StatusNoContent)
	mustError(t, h.do(http.MethodGet, "/api/v1/extensions/"+ext.ID, nil), http.StatusNotFound, model.CodeNotFound)
}

func TestChannelsUnavailableWithoutService(t *testing.T) {
	h := newHarness(t, func(c *harnessConfig) { c.env.WithoutChannels = true })
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/channels"},
		{http.MethodPost, "/api/v1/channels"},
		{http.MethodGet, "/api/v1/channels/x"},
		{http.MethodPatch, "/api/v1/channels/x"},
		{http.MethodDelete, "/api/v1/channels/x"},
		{http.MethodPost, "/api/v1/channels/x/reload"},
	} {
		mustError(t, h.do(c.method, c.path, map[string]any{}), http.StatusServiceUnavailable, model.CodeUnavailable)
	}
}

func TestChannelsCRUDNeverExposeSecret(t *testing.T) {
	h := newHarness(t)
	a := h.seedAgent("通道")
	const botToken = "123456:BOT-TOKEN-SHOULD-NOT-LEAK"

	mustError(t, h.do(http.MethodPost, "/api/v1/channels", map[string]any{
		"agent_id": a.ID, "kind": "telegram", "name": "tg", "config": []string{"x"},
	}), http.StatusBadRequest, model.CodeInvalid)

	rec := h.do(http.MethodPost, "/api/v1/channels", map[string]any{
		"agent_id": a.ID, "kind": "telegram", "name": "tg", "secret": botToken,
		"config": map[string]any{"allowed_peers": []string{"42"}},
	})
	mustStatus(t, rec, http.StatusCreated)
	var ch struct {
		ID        string          `json:"id"`
		HasSecret bool            `json:"has_secret"`
		Status    string          `json:"status"`
		Config    json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ch); err != nil {
		t.Fatal(err)
	}
	if !ch.HasSecret || ch.Status != "running" || !strings.Contains(string(ch.Config), "allowed_peers") {
		t.Fatalf("通道 = %s", rec.Body.String())
	}
	bodies := []string{rec.Body.String()}
	if strings.Contains(rec.Body.String(), "secret_ref") {
		t.Fatalf("响应不应包含 secret_ref: %s", rec.Body.String())
	}

	rec = h.do(http.MethodGet, "/api/v1/channels?agent_id="+a.ID, nil)
	mustStatus(t, rec, http.StatusOK)
	bodies = append(bodies, rec.Body.String())
	rec = h.do(http.MethodGet, "/api/v1/channels/"+ch.ID, nil)
	mustStatus(t, rec, http.StatusOK)
	bodies = append(bodies, rec.Body.String())

	rec = h.do(http.MethodPatch, "/api/v1/channels/"+ch.ID, map[string]any{"enabled": false, "secret": ""})
	mustStatus(t, rec, http.StatusOK)
	bodies = append(bodies, rec.Body.String())
	if strings.Contains(rec.Body.String(), `"has_secret":true`) || !strings.Contains(rec.Body.String(), `"status":"stopped"`) {
		t.Fatalf("清除凭据并停用后 = %s", rec.Body.String())
	}

	before := h.env.Gateway.Reloads(ch.ID)
	rec = h.do(http.MethodPost, "/api/v1/channels/"+ch.ID+"/reload", nil)
	mustStatus(t, rec, http.StatusOK)
	if got := h.env.Gateway.Reloads(ch.ID); got != before+1 {
		t.Fatalf("reload 应让网关重载一次，重载次数 %d → %d", before, got)
	}
	mustStatus(t, h.do(http.MethodDelete, "/api/v1/channels/"+ch.ID, nil), http.StatusNoContent)
	mustError(t, h.do(http.MethodGet, "/api/v1/channels/"+ch.ID, nil), http.StatusNotFound, model.CodeNotFound)

	for _, b := range bodies {
		if strings.Contains(b, botToken) {
			t.Fatalf("响应泄露了通道凭据: %s", b)
		}
	}
	if strings.Contains(h.logs.String(), botToken) {
		t.Fatal("日志泄露了通道凭据")
	}
}

func TestSettingsGetAndValidation(t *testing.T) {
	h := newHarness(t)
	rec := h.do(http.MethodGet, "/api/v1/settings", nil)
	mustStatus(t, rec, http.StatusOK)
	// 从未保存过：timezone / locale 省略（C3），effective_locale 取 Options.DefaultLocale（未提供时 en）。
	if got := rec.Body.String(); strings.Contains(got, `"timezone":`) || strings.Contains(got, `"locale":`) ||
		!strings.Contains(got, `"effective_locale":"en"`) || !strings.Contains(got, `"allowed_origins":[]`) {
		t.Fatalf("缺省设置 = %s", got)
	}

	mustError(t, h.do(http.MethodPatch, "/api/v1/settings", map[string]any{"timezone": "Mars/Olympus"}), http.StatusBadRequest, model.CodeInvalid)
	mustError(t, h.do(http.MethodPatch, "/api/v1/settings", map[string]any{"timezone": "Local"}), http.StatusBadRequest, model.CodeInvalid)
	mustError(t, h.do(http.MethodPatch, "/api/v1/settings", map[string]any{"locale": "ja"}), http.StatusBadRequest, model.CodeInvalid)
	env := mustError(t, h.do(http.MethodPatch, "/api/v1/settings", map[string]any{"allowed_origins": []string{"http://ok:1", "javascript:alert(1)"}}),
		http.StatusBadRequest, model.CodeInvalid)
	if env.Error.Details["index"] != float64(1) {
		t.Errorf("非法 Origin 的 index = %v", env.Error.Details["index"])
	}
	// 校验失败的 PATCH 不改动任何字段。
	rec = h.do(http.MethodPatch, "/api/v1/settings", map[string]any{"timezone": "Asia/Shanghai", "locale": "xx"})
	mustError(t, rec, http.StatusBadRequest, model.CodeInvalid)
	if got := h.do(http.MethodGet, "/api/v1/settings", nil).Body.String(); strings.Contains(got, `"timezone":`) {
		t.Fatalf("部分校验失败不应写入任何字段: %s", got)
	}

	rec = h.do(http.MethodPatch, "/api/v1/settings", map[string]any{"timezone": "Asia/Shanghai", "locale": "en-US"})
	mustStatus(t, rec, http.StatusOK)
	if got := rec.Body.String(); !strings.Contains(got, `"timezone":"Asia/Shanghai"`) || !strings.Contains(got, `"locale":"en"`) ||
		!strings.Contains(got, `"effective_timezone":"Asia/Shanghai"`) || !strings.Contains(got, `"effective_locale":"en"`) {
		t.Fatalf("更新后的设置 = %s", got)
	}
	rec = h.do(http.MethodPatch, "/api/v1/settings", map[string]any{"locale": "fr-FR"})
	mustStatus(t, rec, http.StatusOK)
	if got := rec.Body.String(); !strings.Contains(got, `"timezone":"Asia/Shanghai"`) || !strings.Contains(got, `"locale":"fr"`) ||
		!strings.Contains(got, `"effective_locale":"fr"`) {
		t.Fatalf("法语设置 = %s", got)
	}
	raw, err := h.env.Store.GetSetting(context.Background(), "timezone")
	if err != nil || raw != `"Asia/Shanghai"` {
		t.Fatalf("设置应以 JSON 文本落库，实际 %q, %v", raw, err)
	}
}
