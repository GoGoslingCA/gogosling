package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

func benchProject(t *testing.T, h *harness, agentID string) model.Project {
	t.Helper()
	rec := h.do("POST", "/api/v1/projects", map[string]any{"agent_id": agentID, "name": "需求项目"})
	mustStatus(t, rec, 201)
	return decodeBody[model.Project](t, rec)
}
func benchSession(t *testing.T, h *harness, agentID, projectID string) sessionView {
	t.Helper()
	rec := h.do("POST", "/api/v1/sessions", map[string]any{"agent_id": agentID, "project_id": projectID})
	mustStatus(t, rec, 201)
	return decodeBody[sessionView](t, rec)
}

func TestWorkbenchProjectsAndSessionCAS(t *testing.T) {
	h := newHarness(t)
	a := h.seedAgent("项目助手")
	other := h.seedAgent("另一个助手")
	p := benchProject(t, h, a.ID)
	created := benchSession(t, h, a.ID, p.ID)
	if created.ProjectID != p.ID {
		t.Fatal("新会话应原子关联项目")
	}
	mustError(t, h.do("POST", "/api/v1/sessions", map[string]any{"agent_id": other.ID, "project_id": p.ID}), 400, model.CodeInvalid)
	rec := h.do("PATCH", "/api/v1/projects/"+p.ID, map[string]any{"expected_version": 1, "name": "已修改", "status": "archived"})
	mustStatus(t, rec, 200)
	updated := decodeBody[model.Project](t, rec)
	if updated.Version != 2 || updated.Name != "已修改" {
		t.Fatalf("项目修改应递增版本: %+v", updated)
	}
	conflict := mustError(t, h.do("PATCH", "/api/v1/projects/"+p.ID, map[string]any{"expected_version": 1, "name": "旧版本覆盖"}), 409, model.CodeConflict)
	if conflict.Error.Details["reason"] != "stale_version" {
		t.Fatalf("应报告版本冲突: %+v", conflict.Error.Details)
	}
	read := decodeBody[sessionView](t, h.do("GET", "/api/v1/sessions/"+created.ID, nil))
	if read.Status != model.SessionActive {
		t.Fatal("项目归档不应归档会话")
	}
	mustError(t, h.do("PUT", "/api/v1/sessions/"+created.ID+"/project", map[string]any{"project_id": nil, "expected_project_id": nil}), 409, model.CodeConflict)
	rec = h.do("PUT", "/api/v1/sessions/"+created.ID+"/project", map[string]any{"project_id": nil, "expected_project_id": p.ID})
	mustStatus(t, rec, 200)
	detached := decodeBody[sessionView](t, rec)
	if detached.ProjectID != "" || detached.Key != created.Key || detached.ID != created.ID {
		t.Fatal("关联变化只能修改项目归属")
	}
	mustStatus(t, h.do("DELETE", "/api/v1/projects/"+p.ID, nil), http.StatusMethodNotAllowed)
}

func TestWorkbenchDraftScopeOwnershipAndCAS(t *testing.T) {
	h := newHarness(t)
	a := h.seedAgent("草稿助手")
	other := h.seedAgent("其他助手")
	sess := h.seedSession(a.ID)
	input := model.DraftInput{Kind: model.DraftComposer, AgentID: a.ID, SessionID: sess.ID, Text: "待恢复的初稿"}
	rec := h.do("POST", "/api/v1/drafts", input)
	mustStatus(t, rec, 201)
	draft := decodeBody[model.Draft](t, rec)
	if draft.OwnerID != "local" || draft.Version != 1 {
		t.Fatal("所有者与首版必须由服务端生成")
	}
	duplicate := mustError(t, h.do("POST", "/api/v1/drafts", input), 409, model.CodeConflict)
	if duplicate.Error.Details["existing_draft_id"] != draft.ID {
		t.Fatalf("应指出同作用域已有草稿: %+v", duplicate)
	}
	mustError(t, h.do("POST", "/api/v1/drafts", map[string]any{"kind": "composer", "agent_id": a.ID, "owner_id": "other", "text": "invalid owner"}), 400, model.CodeInvalid)
	invalid := input
	invalid.AgentID = other.ID
	mustError(t, h.do("POST", "/api/v1/drafts", invalid), 400, model.CodeInvalid)
	input.ExpectedVersion = 1
	input.Text = "确认保存的第二版"
	rec = h.do("PUT", "/api/v1/drafts/"+draft.ID, input)
	mustStatus(t, rec, 200)
	if decodeBody[model.Draft](t, rec).Version != 2 {
		t.Fatal("草稿更新应原子递增版本")
	}
	mustError(t, h.do("PUT", "/api/v1/drafts/"+draft.ID, input), 409, model.CodeConflict)
	mustError(t, h.do("DELETE", "/api/v1/drafts/"+draft.ID+"?expected_version=1", nil), 409, model.CodeConflict)
	if _, err := h.env.Deps.Workbench.GetDraft(context.Background(), model.Principal{ID: "different-owner"}, draft.ID); !model.IsCode(err, model.CodeNotFound) {
		t.Fatalf("不同主体不应读取草稿: %v", err)
	}
	input.Text = strings.Repeat("a", model.WorkbenchTextLimit+1)
	input.ExpectedVersion = 2
	mustError(t, h.do("PUT", "/api/v1/drafts/"+draft.ID, input), 400, model.CodeInvalid)
	mustStatus(t, h.do("DELETE", "/api/v1/drafts/"+draft.ID+"?expected_version=2", nil), 204)
}

func TestWorkbenchArtifactSourceRevisionsAndSafeDownloads(t *testing.T) {
	h := newHarness(t)
	a := h.seedAgent("成果助手")
	sess := h.seedSession(a.ID)
	other := h.seedSession(a.ID)
	source := model.TextMessage(sess.ID, model.RoleAssistant, "# 助手原稿\n来源内容")
	source.Meta = json.RawMessage(`{"partial":true}`)
	if err := h.env.Store.AppendMessage(context.Background(), &source); err != nil {
		t.Fatal(err)
	}
	user := model.TextMessage(sess.ID, model.RoleUser, "不能冒充助手")
	if err := h.env.Store.AppendMessage(context.Background(), &user); err != nil {
		t.Fatal(err)
	}
	input := model.ArtifactInput{Title: "方案初稿", Kind: model.ArtifactDocument, Filename: "plan.md", MediaType: "text/markdown", SourceMessageID: source.ID, Content: "<script>alert('text only')</script>"}
	mustError(t, h.do("POST", "/api/v1/sessions/"+other.ID+"/artifacts", input), 400, model.CodeInvalid)
	bad := input
	bad.SourceMessageID = user.ID
	mustError(t, h.do("POST", "/api/v1/sessions/"+sess.ID+"/artifacts", bad), 400, model.CodeInvalid)
	url := "/api/v1/sessions/" + sess.ID + "/artifacts"
	rec := h.do("POST", url, input, withHeader("Idempotency-Key", "request-1"))
	mustStatus(t, rec, 201)
	artifact := decodeBody[model.Artifact](t, rec)
	if artifact.Version != 1 || artifact.LatestRevisionID == "" || !artifact.Source.Partial || artifact.Source.MessageSeq != source.Seq || artifact.Source.SHA256 == "" {
		t.Fatalf("首版和来源应完整: %+v", artifact)
	}
	again := decodeBody[model.Artifact](t, h.do("POST", url, input, withHeader("Idempotency-Key", "request-1")))
	if again.ID != artifact.ID {
		t.Fatal("同请求幂等重试不能创建第二成果")
	}
	changed := input
	changed.Content = "不同请求内容"
	mustError(t, h.do("POST", url, changed, withHeader("Idempotency-Key", "request-1")), 409, model.CodeConflict)
	base := "/api/v1/artifacts/" + artifact.ID + "/revisions"
	revision := model.ArtifactRevisionInput{ExpectedLatestRevisionID: artifact.LatestRevisionID, Content: "修改正文", Filename: "plan-v2.md", MediaType: "text/markdown"}
	rec = h.do("POST", base, revision)
	mustStatus(t, rec, 201)
	second := decodeBody[model.ArtifactRevision](t, rec)
	if second.Number != 2 || second.ParentRevisionID != artifact.LatestRevisionID {
		t.Fatal("新版本应保留父版本")
	}
	if strings.Contains(rec.Body.String(), "修改正文") {
		t.Fatal("版本元数据不能返回正文")
	}
	mustError(t, h.do("POST", base, revision), 409, model.CodeConflict)
	content := h.do("GET", base+"/"+artifact.LatestRevisionID+"/content", nil)
	mustStatus(t, content, 200)
	if content.Body.String() != input.Content || content.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatal("原版正文应不可变且以纯文本返回")
	}
	download := h.do("GET", base+"/"+second.ID+"/download", nil)
	mustStatus(t, download, 200)
	if download.Body.String() != revision.Content || !strings.HasPrefix(download.Header().Get("Content-Disposition"), "attachment;") || download.Header().Get("X-Content-Type-Options") != "nosniff" || download.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("下载必须为受保护的附件响应")
	}
	mustError(t, h.do("GET", base+"/"+second.ID+"/content", nil, withoutAuth()), 401, model.CodeUnauthorized)
}

func TestWorkbenchMaterialsScopeDigestAndDraftRecovery(t *testing.T) {
	h := newHarness(t)
	a := h.seedAgent("资料助手")
	p := benchProject(t, h, a.ID)
	q := benchProject(t, h, a.ID)
	sess := benchSession(t, h, a.ID, p.ID)
	other := benchSession(t, h, a.ID, q.ID)
	var buf bytes.Buffer
	form := multipart.NewWriter(&buf)
	file, err := form.CreateFormFile("file", "requirements.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.Write([]byte("原始需求"))
	_ = form.Close()
	rec := h.do("POST", "/api/v1/projects/"+p.ID+"/materials", buf.Bytes(), withHeader("Content-Type", form.FormDataContentType()))
	mustStatus(t, rec, 201)
	material := decodeBody[model.ProjectMaterial](t, rec)
	if material.SHA256 == "" || material.Attachment.Path == "" {
		t.Fatal("项目上传应建立可校验索引")
	}
	mustError(t, h.do("POST", "/api/v1/sessions/"+other.ID+"/messages", map[string]any{"text": "整理", "material_ids": []string{material.ID}}), 400, model.CodeInvalid)
	input := model.DraftInput{Kind: model.DraftComposer, AgentID: a.ID, SessionID: sess.ID, ProjectID: p.ID, Text: "保留正文", MaterialIDs: []string{material.ID}}
	rec = h.do("POST", "/api/v1/drafts", input)
	mustStatus(t, rec, 201)
	draft := decodeBody[model.Draft](t, rec)
	rec = h.do("POST", "/api/v1/sessions/"+sess.ID+"/messages", map[string]any{"text": "整理所选资料", "material_ids": []string{material.ID, material.ID}, "attachments": []model.Attachment{material.Attachment}})
	mustStatus(t, rec, 202)
	history, err := h.env.Store.ListMessages(context.Background(), sess.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range history {
		if message.Role != model.RoleUser {
			continue
		}
		found = true
		attachments := 0
		for _, part := range message.Parts {
			if part.Type == model.PartAttachment {
				attachments++
			}
		}
		if attachments != 1 || !strings.Contains(string(message.Meta), material.ID) || !strings.Contains(string(message.Meta), material.SHA256) {
			t.Fatalf("本次资料应去重且保留来源摘要: %+v", message)
		}
	}
	if !found {
		t.Fatal("发送应持久化用户消息")
	}
	ws, err := h.env.Deps.Workspaces.Open(context.Background(), a.ID, a.WorkspaceDir)
	if err != nil {
		t.Fatal(err)
	}
	if err = ws.WriteFile(context.Background(), material.Attachment.Path, []byte("变化需求")); err != nil {
		t.Fatal(err)
	}
	conflict := mustError(t, h.do("POST", "/api/v1/sessions/"+sess.ID+"/messages", map[string]any{"text": "整理", "material_ids": []string{material.ID}}), 409, model.CodeConflict)
	if conflict.Error.Details["reason"] != "material_changed" {
		t.Fatal("同大小内容变化也必须拒绝")
	}
	mustStatus(t, h.do("DELETE", "/api/v1/projects/"+p.ID+"/materials/"+material.ID, nil), 204)
	if _, err = ws.Stat(context.Background(), material.Attachment.Path); err != nil {
		t.Fatal("移除索引不能删除原入站文件")
	}
	input.ExpectedVersion = 1
	input.Text = "资料失效后继续保存正文"
	rec = h.do("PUT", "/api/v1/drafts/"+draft.ID, input)
	mustStatus(t, rec, 200)
	if decodeBody[model.Draft](t, rec).MaterialIDs[0] != material.ID {
		t.Fatal("恢复草稿应保留失效引用供用户处理")
	}
}

func TestWorkbenchCapabilitiesAndDisabledCompatibility(t *testing.T) {
	h := newHarness(t, func(c *harnessConfig) { c.env.WithoutChanges = true })
	version := decodeBody[map[string]json.RawMessage](t, h.do("GET", "/api/v1/version", nil))
	if len(version["capabilities"]) == 0 || strings.Contains(string(version["capabilities"]), "changesets") {
		t.Fatal("只应声明已完成P3A能力")
	}
	old := newHarness(t, func(c *harnessConfig) { c.env.WithoutWorkbench = true })
	v := decodeBody[map[string]any](t, old.do("GET", "/api/v1/version", nil))
	if caps, ok := v["capabilities"].(map[string]any); ok {
		for name := range caps {
			if strings.HasPrefix(name, "workbench.") {
				t.Fatal("未配服务不能声明工作台能力")
			}
		}
	}
	mustError(t, old.do("GET", "/api/v1/projects", nil), 503, model.CodeUnavailable)
	agent := old.seedAgent("兼容助手")
	sess := old.seedSession(agent.ID)
	mustStatus(t, old.do("GET", "/api/v1/sessions/"+sess.ID, nil), 200)
	// 草稿事件与审计中只能出现标识和版本，不得复制正文。
	agent2 := h.seedAgent("事件助手")
	sub := h.env.Bus.Subscribe(context.Background(), h.env.Bus.LastSeq(), 8, nil)
	defer sub.Cancel()
	text := "不可进入事件的草稿正文"
	rec := h.do("POST", "/api/v1/drafts", model.DraftInput{Kind: model.DraftComposer, AgentID: agent2.ID, Text: text})
	mustStatus(t, rec, 201)
	audit := h.do("GET", "/api/v1/audit", nil)
	if strings.Contains(audit.Body.String(), text) {
		t.Fatal("审计不应含草稿正文")
	}
	select {
	case ev := <-sub.C:
		payload, _ := json.Marshal(ev.Payload)
		if ev.Type != "draft.updated" || strings.Contains(string(payload), text) {
			t.Fatalf("草稿事件只能有标识和版本: %s", payload)
		}
	default:
		t.Fatal("成功写入应发布草稿元数据事件")
	}
}

func TestWorkbenchRequiresExplicitTextFields(t *testing.T) {
	h := newHarness(t)
	a := h.seedAgent("正文校验")
	sess := h.seedSession(a.ID)
	draftInput := map[string]any{"kind": "composer", "agent_id": a.ID, "session_id": sess.ID, "text": "不应被缺字段清空"}
	rec := h.do("POST", "/api/v1/drafts", draftInput)
	mustStatus(t, rec, 201)
	draft := decodeBody[model.Draft](t, rec)
	draftInput["expected_version"] = 1
	for _, value := range []any{nil, "omitted"} {
		if value == nil {
			draftInput["text"] = nil
		} else {
			delete(draftInput, "text")
		}
		mustError(t, h.do("PUT", "/api/v1/drafts/"+draft.ID, draftInput), 400, model.CodeInvalid)
	}
	got := decodeBody[model.Draft](t, h.do("GET", "/api/v1/drafts/"+draft.ID, nil))
	if got.Text != "不应被缺字段清空" || got.Version != 1 {
		t.Fatal("缺失正文不能改变已有草稿")
	}
	draftInput["text"] = ""
	mustStatus(t, h.do("PUT", "/api/v1/drafts/"+draft.ID, draftInput), 200)
	source := model.TextMessage(sess.ID, model.RoleAssistant, "真实来源")
	if err := h.env.Store.AppendMessage(context.Background(), &source); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"title": "文档", "kind": "document", "filename": "text.md", "media_type": "text/markdown", "source_message_id": source.ID}
	url := "/api/v1/sessions/" + sess.ID + "/artifacts"
	mustError(t, h.do("POST", url, input), 400, model.CodeInvalid)
	input["content"] = nil
	mustError(t, h.do("POST", url, input), 400, model.CodeInvalid)
	input["content"] = "首版内容"
	rec = h.do("POST", url, input)
	mustStatus(t, rec, 201)
	art := decodeBody[model.Artifact](t, rec)
	rev := map[string]any{"expected_latest_revision_id": art.LatestRevisionID, "filename": "text.md", "media_type": "text/markdown"}
	url = "/api/v1/artifacts/" + art.ID + "/revisions"
	mustError(t, h.do("POST", url, rev), 400, model.CodeInvalid)
	rev["content"] = nil
	mustError(t, h.do("POST", url, rev), 400, model.CodeInvalid)
	rev["content"] = ""
	mustStatus(t, h.do("POST", url, rev), 201)
}

func TestWorkbenchDraftRebaseKeepsScopeAndVersionChecks(t *testing.T) {
	h := newHarness(t)
	a := h.seedAgent("基线助手")
	sess := h.seedSession(a.ID)
	source := model.TextMessage(sess.ID, model.RoleAssistant, "真实成果来源")
	if err := h.env.Store.AppendMessage(context.Background(), &source); err != nil {
		t.Fatal(err)
	}
	input := model.ArtifactInput{Title: "计划", Kind: model.ArtifactDocument, Filename: "plan.md", MediaType: "text/markdown", SourceMessageID: source.ID, Content: "原稿"}
	createURL := "/api/v1/sessions/" + sess.ID + "/artifacts"
	rec := h.do("POST", createURL, input)
	mustStatus(t, rec, 201)
	art := decodeBody[model.Artifact](t, rec)
	draftIn := model.DraftInput{Kind: model.DraftArtifact, AgentID: a.ID, SessionID: sess.ID, ArtifactID: art.ID, BaseRevisionID: art.LatestRevisionID, Text: "基于第一版编辑"}
	rec = h.do("POST", "/api/v1/drafts", draftIn)
	mustStatus(t, rec, 201)
	draft := decodeBody[model.Draft](t, rec)
	revision := model.ArtifactRevisionInput{ExpectedLatestRevisionID: art.LatestRevisionID, Content: "已保存第二版", Filename: "plan.md", MediaType: "text/markdown"}
	revisionURL := "/api/v1/artifacts/" + art.ID + "/revisions"
	rec = h.do("POST", revisionURL, revision)
	mustStatus(t, rec, 201)
	second := decodeBody[model.ArtifactRevision](t, rec)
	draftIn.ExpectedVersion = 1
	draftIn.BaseRevisionID = second.ID
	draftIn.Text = "在第二版上继续编辑"
	draftURL := "/api/v1/drafts/" + draft.ID
	rec = h.do("PUT", draftURL, draftIn)
	mustStatus(t, rec, 200)
	updated := decodeBody[model.Draft](t, rec)
	if updated.ID != draft.ID || updated.Version != 2 || updated.BaseRevisionID != second.ID {
		t.Fatal("同成果新基线应由CAS更新同一草稿")
	}
	mustError(t, h.do("PUT", draftURL, draftIn), 409, model.CodeConflict)
	rec = h.do("POST", createURL, input)
	mustStatus(t, rec, 201)
	alien := decodeBody[model.Artifact](t, rec)
	draftIn.ExpectedVersion = 2
	draftIn.BaseRevisionID = alien.LatestRevisionID
	mustError(t, h.do("PUT", draftURL, draftIn), 404, model.CodeNotFound)
	mustError(t, h.do("POST", revisionURL, revision), 409, model.CodeConflict)
	got := decodeBody[model.Draft](t, h.do("GET", draftURL, nil))
	if got.BaseRevisionID != second.ID || got.Version != 2 || got.Text != "在第二版上继续编辑" {
		t.Fatal("错误基线或旧版本不能覆盖草稿")
	}
}
