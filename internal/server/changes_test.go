package server_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/identity"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/server/servertest"
	"github.com/GoGoslingCA/gogosling/internal/workspace"
)

func changeFixture(t *testing.T) (*harness, *model.Agent, sessionView, model.Artifact, *workspace.Workspace) {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("条件修改仅实现Linux/macOS，其他平台明确降级")
	}
	h := newHarness(t)
	a := h.seedAgent("修改助手")
	sess := h.seedSession(a.ID)
	m := model.TextMessage(sess.ID, model.RoleAssistant, "已核对的文稿")
	if err := h.env.Store.AppendMessage(context.Background(), &m); err != nil {
		t.Fatal(err)
	}
	r := h.do("POST", "/api/v1/sessions/"+sess.ID+"/artifacts", model.ArtifactInput{Title: "文稿", Kind: model.ArtifactDocument, Filename: "plan.md", MediaType: "text/markdown", SourceMessageID: m.ID, Content: "# 新版\n第二行\n"})
	mustStatus(t, r, 201)
	artifact := decodeBody[model.Artifact](t, r)
	ws, err := h.env.Deps.Workspaces.Open(context.Background(), a.ID, a.WorkspaceDir)
	if err != nil {
		t.Fatal(err)
	}
	return h, a, sess, artifact, ws
}
func prepareFile(t *testing.T, h *harness, sess sessionView, a model.Artifact, rel string, kind model.ChangeKind) model.ChangeSet {
	t.Helper()
	r := h.do("POST", "/api/v1/sessions/"+sess.ID+"/changesets", model.ChangeInput{ArtifactID: a.ID, RevisionID: a.LatestRevisionID, TargetPath: rel, Operation: kind})
	mustStatus(t, r, 201)
	if strings.Contains(r.Body.String(), "# 新版") {
		t.Fatal("变更元数据不得带正文")
	}
	return decodeBody[model.ChangeSet](t, r)
}
func applyFile(t *testing.T, h *harness, c model.ChangeSet, key string) model.ChangeSet {
	t.Helper()
	r := h.do("POST", "/api/v1/changesets/"+c.ID+"/apply", model.ChangeApplyInput{ExpectedVersion: c.Version, PreviewDigest: c.PreviewDigest}, withHeader("Idempotency-Key", key))
	mustStatus(t, r, 202)
	return decodeBody[model.ChangeSet](t, r)
}
func waitFileApproval(t *testing.T, h *harness, operation string) *model.ApprovalRequest {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		items, e := h.env.Deps.Permissions.ListPending(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		for _, a := range items {
			if a.OperationID == operation {
				return a
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("人工操作应进入真实审批")
	return nil
}
func waitFileState(t *testing.T, h *harness, id string, want model.ChangeStatus) *model.ChangeSet {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last *model.ChangeSet
	for time.Now().Before(deadline) {
		c, e := h.env.Deps.Changes.Get(context.Background(), identity.LocalPrincipal(), id)
		if e != nil {
			t.Fatal(e)
		}
		last = c
		if c.Status == want {
			return c
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("变更应为%s，实际%+v", want, last)
	return nil
}
func approveFile(t *testing.T, h *harness, c model.ChangeSet, approve bool) {
	t.Helper()
	a := waitFileApproval(t, h, c.OperationID)
	if a.RunID != "" || a.Risk == model.RiskRead {
		t.Fatal("人工审批不能伪造模型运行或低报写入风险")
	}
	mustError(t, h.do("POST", "/api/v1/approvals/"+a.ID+"/decision", model.Decision{Approve: true, Scope: model.ScopeSession}), 400, model.CodeInvalid)
	mustStatus(t, h.do("POST", "/api/v1/approvals/"+a.ID+"/decision", model.Decision{Approve: approve, Scope: model.ScopeOnce}), 200)
}

func Test文件修改先预览经审批应用并独立撤销(t *testing.T) {
	h, _, sess, a, ws := changeFixture(t)
	c := prepareFile(t, h, sess, a, "plan.md", model.ChangeCreate)
	if _, err := ws.Stat(context.Background(), "plan.md"); !model.IsCode(err, model.CodeNotFound) {
		t.Fatal("预览不能创建文件")
	}
	d := h.do("GET", "/api/v1/changesets/"+c.ID+"/diff", nil)
	mustStatus(t, d, 200)
	if !strings.Contains(d.Body.String(), "新版") {
		t.Fatal("专门差异接口应含提议")
	}
	accepted := applyFile(t, h, c, "create-1")
	if accepted.Status != model.ChangeAwaitingApproval {
		t.Fatal("202只能表示受理，不能表示已应用")
	}
	again := applyFile(t, h, c, "create-1")
	if again.OperationID != accepted.OperationID {
		t.Fatal("同请求重试不得增加主体或重新申请审批")
	}
	approveFile(t, h, accepted, true)
	applied := waitFileState(t, h, c.ID, model.ChangeApplied)
	body, err := ws.ReadFile(context.Background(), "plan.md", 1024)
	if err != nil || string(body) != "# 新版\n第二行\n" {
		t.Fatalf("应用字节应准确: %v", err)
	}
	u := h.do("POST", "/api/v1/changesets/"+c.ID+"/undo-preview", model.ChangeVersionInput{ExpectedVersion: applied.Version})
	mustStatus(t, u, 201)
	undo := decodeBody[model.ChangeSet](t, u)
	if undo.ID == c.ID || undo.Kind != model.ChangeDelete || undo.UndoOf != c.ID {
		t.Fatal("撤销新建应生成独立删除预览")
	}
	if _, err = ws.Stat(context.Background(), "plan.md"); err != nil {
		t.Fatal("撤销预览不能删除文件")
	}
	undoAccepted := applyFile(t, h, undo, "undo-1")
	req := waitFileApproval(t, h, undoAccepted.OperationID)
	if req.Risk != model.RiskDestructive {
		t.Fatal("删除必须按破坏性风险审批")
	}
	approveFile(t, h, undoAccepted, true)
	waitFileState(t, h, undo.ID, model.ChangeApplied)
	if _, err = ws.Stat(context.Background(), "plan.md"); !model.IsCode(err, model.CodeNotFound) {
		t.Fatal("已确认撤销应删除新建文件")
	}
	if strings.Contains(h.logs.String(), "# 新版") {
		t.Fatal("日志不得带正文")
	}
	runs, err := h.env.Store.ListRuns(context.Background(), sess.ID, model.Page{})
	if err != nil || len(runs) != 0 {
		t.Fatal("人工修改不能制造模型运行或费用")
	}
}

func Test外部编辑在审批期间和撤销前均不被覆盖(t *testing.T) {
	h, _, sess, a, ws := changeFixture(t)
	ctx := context.Background()
	if err := ws.WriteFile(ctx, "plan.md", []byte("旧稿")); err != nil {
		t.Fatal(err)
	}
	c := prepareFile(t, h, sess, a, "plan.md", model.ChangeReplace)
	accepted := applyFile(t, h, c, "replace-1")
	waitFileApproval(t, h, accepted.OperationID)
	if err := os.WriteFile(filepath.Join(ws.Root(), "plan.md"), []byte("外部编辑"), 0o600); err != nil {
		t.Fatal(err)
	}
	approveFile(t, h, accepted, true)
	waitFileState(t, h, c.ID, model.ChangeConflict)
	b, _ := ws.ReadFile(ctx, "plan.md", 1024)
	if string(b) != "外部编辑" {
		t.Fatal("外部修改应原样保留")
	}
	fresh := prepareFile(t, h, sess, a, "plan.md", model.ChangeReplace)
	accepted = applyFile(t, h, fresh, "replace-2")
	approveFile(t, h, accepted, true)
	done := waitFileState(t, h, fresh.ID, model.ChangeApplied)
	if err := os.WriteFile(filepath.Join(ws.Root(), "plan.md"), []byte("另一次编辑"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustError(t, h.do("POST", "/api/v1/changesets/"+fresh.ID+"/undo-preview", model.ChangeVersionInput{ExpectedVersion: done.Version}), 409, model.CodeConflict)
	b, _ = ws.ReadFile(ctx, "plan.md", 1024)
	if string(b) != "另一次编辑" {
		t.Fatal("撤销冲突不能改变文件")
	}
}

func Test拒绝取消及预览绑定失败不修改文件(t *testing.T) {
	h, _, sess, a, ws := changeFixture(t)
	c := prepareFile(t, h, sess, a, "plan.md", model.ChangeCreate)
	mustError(t, h.do("POST", "/api/v1/changesets/"+c.ID+"/apply", map[string]any{"expected_version": c.Version, "preview_digest": c.PreviewDigest, "content": "替换预览正文"}, withHeader("Idempotency-Key", "bad")), 400, model.CodeInvalid)
	mustError(t, h.do("POST", "/api/v1/changesets/"+c.ID+"/apply", model.ChangeApplyInput{ExpectedVersion: c.Version, PreviewDigest: "wrong"}, withHeader("Idempotency-Key", "bad")), 409, model.CodeConflict)
	accepted := applyFile(t, h, c, "deny-1")
	approveFile(t, h, accepted, false)
	waitFileState(t, h, c.ID, model.ChangeDenied)
	c = prepareFile(t, h, sess, a, "plan.md", model.ChangeCreate)
	accepted = applyFile(t, h, c, "cancel-1")
	waitFileApproval(t, h, accepted.OperationID)
	mustStatus(t, h.do("POST", "/api/v1/changesets/"+c.ID+"/cancel", model.ChangeVersionInput{ExpectedVersion: accepted.Version}), 202)
	waitFileState(t, h, c.ID, model.ChangeCancelled)
	if _, err := ws.Stat(context.Background(), "plan.md"); !model.IsCode(err, model.CodeNotFound) {
		t.Fatal("拒绝或取消不能创建文件")
	}
}

func Test应用后落账前重开服务核验文件并保留撤销(t *testing.T) {
	h, ag, sess, a, ws := changeFixture(t)
	ctx := context.Background()
	if err := ws.WriteFile(ctx, "plan.md", []byte("崩溃前旧稿")); err != nil {
		t.Fatal(err)
	}
	c := prepareFile(t, h, sess, a, "plan.md", model.ChangeReplace)
	stored, err := h.env.Store.GetChangeSet(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	op := &model.Operation{ID: model.NewID(), OwnerID: "local", AgentID: ag.ID, SessionID: sess.ID, ChangeSetID: c.ID, Status: model.OperationExecuting, CreatedAt: now, UpdatedAt: now}
	if err = h.env.Store.ChangeTx(ctx, func(tx model.ChangeStore) error {
		if e := tx.CreateOperation(ctx, op); e != nil {
			return e
		}
		stored.OperationID = op.ID
		stored.Status = model.ChangeApplying
		return tx.UpdateChangeSet(ctx, stored, stored.Version)
	}); err != nil {
		t.Fatal(err)
	}
	base := workspace.Snapshot{Exists: stored.Base.Exists, SHA256: stored.Base.SHA256, Identity: stored.Base.Identity, RootIdentity: stored.Base.RootIdentity, Content: stored.Base.Content, Size: stored.Base.Size}
	receipt, err := ws.Apply(ctx, op.ID, stored.TargetPath, base, &stored.Proposed)
	if err != nil || receipt.State != "applied" {
		t.Fatalf("模拟写入成功但未落账: %v", err)
	}
	dir := h.env.Dir
	if err = h.env.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := servertest.NewEnv(ctx, dir, servertest.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	result, err := reopened.Deps.Changes.Get(ctx, identity.LocalPrincipal(), c.ID)
	if err != nil || result.Status != model.ChangeApplied {
		t.Fatalf("重开应通过inode和hash补齐成功账: %+v/%v", result, err)
	}
	undo, err := reopened.Deps.Changes.UndoPreview(ctx, identity.LocalPrincipal(), c.ID, result.Version)
	if err != nil || undo.Proposed != "崩溃前旧稿" {
		t.Fatalf("恢复后原前像仍可用于撤销: %v", err)
	}
	var meta map[string]any
	raw, _ := json.Marshal(result)
	_ = json.Unmarshal(raw, &meta)
	if _, ok := meta["base"]; ok {
		t.Fatal("元数据不能返回恢复前像")
	}
}

func Test旧工作台不启用新写接口并保持原会话(t *testing.T) {
	h := newHarness(t, func(c *harnessConfig) { c.env.WithoutChanges = true })
	a := h.seedAgent("旧实例")
	sess := h.seedSession(a.ID)
	v := h.do("GET", "/api/v1/version", nil)
	mustStatus(t, v, 200)
	if strings.Contains(v.Body.String(), "workbench.changesets") {
		t.Fatal("旧实例不得声明新写能力")
	}
	mustError(t, h.do("POST", "/api/v1/sessions/"+sess.ID+"/changesets", map[string]string{}), 503, model.CodeUnavailable)
	mustStatus(t, h.do("GET", "/api/v1/sessions/"+sess.ID, nil), 200)
}
