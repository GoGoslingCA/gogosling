package server_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/identity"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/permission"
	"github.com/GoGoslingCA/gogosling/internal/server/servertest"
	"github.com/GoGoslingCA/gogosling/internal/workbench"
)

// failingFinishStore 只在文件已安装后的首次成功落账处失败，其余均使用真实SQLite事务。
type failingFinishStore struct {
	model.ChangeStore
	mu        sync.Mutex
	remaining int
}
type failingFinishTx struct {
	model.ChangeStore
	parent *failingFinishStore
}

func (s *failingFinishStore) ChangeTx(ctx context.Context, fn func(model.ChangeStore) error) error {
	return s.ChangeStore.ChangeTx(ctx, func(tx model.ChangeStore) error { return fn(&failingFinishTx{ChangeStore: tx, parent: s}) })
}
func (tx *failingFinishTx) UpdateChangeSet(ctx context.Context, c *model.ChangeSet, expected int64) error {
	if c.Status == model.ChangeApplied {
		tx.parent.mu.Lock()
		fail := tx.parent.remaining > 0
		if fail {
			tx.parent.remaining--
		}
		tx.parent.mu.Unlock()
		if fail {
			return model.NewError(model.CodeInternal, "模拟首次成功落账失败")
		}
	}
	return tx.ChangeStore.UpdateChangeSet(ctx, c, expected)
}

// recoveryFiles 只替换文件系统步骤，使数据库落账和并发恢复可确定地在指定时刻竞争。
type recoveryFiles struct {
	mu               sync.Mutex
	current          model.ChangeSnapshot
	writes           int
	journal          bool
	finalizations    int
	finalizeFailures int
	finalizeError    error
	recoverEntered   chan struct{}
	recoverRelease   chan struct{}
}

func (f *recoveryFiles) Supported() bool { return true }
func (f *recoveryFiles) Snapshot(context.Context, string, string, int64) (model.ChangeSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.current, nil
}
func (f *recoveryFiles) Apply(_ context.Context, _ string, operationID, _ string, expected model.ChangeSnapshot, body *string) (workbench.ChangeReceipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	next := model.ChangeSnapshot{RootIdentity: expected.RootIdentity}
	if body != nil {
		next.Exists = true
		next.Content = *body
		next.Size = int64(len(*body))
		next.SHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(*body)))
		next.Identity = "installed:" + operationID
	}
	f.current = next
	f.journal = true
	return workbench.ChangeReceipt{State: "applied", Before: expected, After: next}, nil
}
func (f *recoveryFiles) Recover(ctx context.Context, _ string, _ string, _ string, expected model.ChangeSnapshot, _ *string) (workbench.ChangeReceipt, error) {
	f.mu.Lock()
	entered, release := f.recoverEntered, f.recoverRelease
	current := f.current
	journal := f.journal
	f.mu.Unlock()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		case <-ctx.Done():
			return workbench.ChangeReceipt{}, ctx.Err()
		}
		select {
		case <-release:
		case <-ctx.Done():
			return workbench.ChangeReceipt{}, ctx.Err()
		}
	}
	if !journal {
		return workbench.ChangeReceipt{}, model.NotFound("reservation", "operation")
	}
	return workbench.ChangeReceipt{State: "applied", Before: expected, After: current}, nil
}
func (f *recoveryFiles) Finalize(context.Context, string, string, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finalizations++
	if f.finalizeError != nil {
		return f.finalizeError
	}
	if f.finalizeFailures > 0 {
		f.finalizeFailures--
		return model.NewError(model.CodeInternal, "模拟一次保留原件清理失败")
	}
	f.journal = false
	return nil
}

// approvedChangeExecutor 模拟已经完成一次审批的工具回调；不制造模型Run。
type approvedChangeExecutor struct{ service *workbench.ChangeService }

func (e approvedChangeExecutor) ExecuteUserOperation(ctx context.Context, id string, call model.ToolCall) (model.ToolResult, error) {
	var args struct {
		ChangeID string `json:"change_id"`
		Digest   string `json:"preview_digest"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return model.ToolResult{}, err
	}
	return model.ToolResult{}, e.service.ExecuteApproved(ctx, id, args.ChangeID, args.Digest)
}

func failedFinishFixture(t *testing.T) (*harness, *workbench.ChangeService, *recoveryFiles, *model.ChangeSet) {
	return recoveryFailureFixture(t, 1, 0, nil)
}

func recoveryFailureFixture(t *testing.T, finalCommitFailures, finalizeFailures int, finalizeError error) (*harness, *workbench.ChangeService, *recoveryFiles, *model.ChangeSet) {
	t.Helper()
	h := newHarness(t, func(c *harnessConfig) { c.env.WithoutChanges = true })
	a := h.seedAgent("落账故障助手")
	sess := h.seedSession(a.ID)
	source := model.TextMessage(sess.ID, model.RoleAssistant, "已生成的方案")
	if err := h.env.Store.AppendMessage(context.Background(), &source); err != nil {
		t.Fatal(err)
	}
	rec := h.do("POST", "/api/v1/sessions/"+sess.ID+"/artifacts", model.ArtifactInput{Title: "方案", Kind: model.ArtifactDocument, Filename: "plan.md", MediaType: "text/markdown", SourceMessageID: source.ID, Content: "经过审批的新文件内容"})
	mustStatus(t, rec, 201)
	artifact := decodeBody[model.Artifact](t, rec)
	fault := &failingFinishStore{ChangeStore: h.env.Store, remaining: finalCommitFailures}
	files := &recoveryFiles{current: model.ChangeSnapshot{RootIdentity: "workspace-identity"}, finalizeFailures: finalizeFailures, finalizeError: finalizeError}
	service, err := workbench.NewChanges(fault, files, h.env.Bus)
	if err != nil {
		t.Fatal(err)
	}
	service.BindExecutor(approvedChangeExecutor{service})
	t.Cleanup(service.Close)
	preview, err := service.Prepare(context.Background(), identity.LocalPrincipal(), sess.ID, model.ChangeInput{ArtifactID: artifact.ID, RevisionID: artifact.LatestRevisionID, TargetPath: "plan.md", Operation: model.ChangeCreate})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Apply(context.Background(), identity.LocalPrincipal(), preview.ID, model.ChangeApplyInput{ExpectedVersion: preview.Version, PreviewDigest: preview.PreviewDigest}, "finish-failure-request"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	var last *model.ChangeSet
	for time.Now().Before(deadline) {
		last, err = service.Get(context.Background(), identity.LocalPrincipal(), preview.ID)
		if err != nil {
			t.Fatal(err)
		}
		if last.Status == model.ChangeRecoveryRequired {
			return h, service, files, last
		}
		if last.Status == model.ChangeFailed {
			t.Fatal("文件实际已变化，完成步骤失败后不能记成failed")
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("落账未确认应进入recovery_required，实际 %+v", last)
	return nil, nil, nil, nil
}

func Test文件已应用但首次落账失败必须保留恢复状态(t *testing.T) {
	h, service, files, change := failedFinishFixture(t)
	files.mu.Lock()
	writes, body := files.writes, files.current.Content
	files.mu.Unlock()
	if writes != 1 || body != "经过审批的新文件内容" {
		t.Fatal("故障点必须发生在真实文件副作用之后")
	}
	if change.PendingResult != model.ChangeApplied || change.Applied.RootIdentity == "" || change.Applied.Identity == "" {
		t.Fatal("最终落账失败前必须已持久化预期终态与完整文件身份")
	}
	files.mu.Lock()
	journal := files.journal
	files.mu.Unlock()
	if journal {
		t.Fatal("测试应模拟Finalize成功但最终数据库落账失败、保留记录已清理的切点")
	}
	op, err := h.env.Store.GetOperation(context.Background(), change.OperationID)
	if err != nil || op.Status != model.OperationRecoveryRequired {
		t.Fatalf("操作主体也必须保留恢复锁: %+v %v", op, err)
	}
	if err = h.env.Store.DeleteSession(context.Background(), change.SessionID); !model.IsCode(err, model.CodeConflict) {
		t.Fatalf("待恢复记录不能随会话删除: %v", err)
	}
	result, err := service.Reconcile(context.Background(), identity.LocalPrincipal(), change.ID, change.Version)
	if err != nil || result.Status != model.ChangeApplied {
		t.Fatalf("再次核验后可以落账为applied: %+v %v", result, err)
	}
	files.mu.Lock()
	defer files.mu.Unlock()
	if files.writes != 1 {
		t.Fatal("恢复对账不得再次写文件")
	}
}

func Test清理原件一次失败须保留恢复门闩并允许重新核对(t *testing.T) {
	h, service, files, change := recoveryFailureFixture(t, 0, 1, nil)
	if change.PendingResult != model.ChangeApplied || change.Status != model.ChangeRecoveryRequired {
		t.Fatal("清理未成功时只能记录待确认结果，不能对外宣布applied")
	}
	op, err := h.env.Store.GetOperation(context.Background(), change.OperationID)
	if err != nil || op.Status != model.OperationRecoveryRequired {
		t.Fatalf("清理失败不得释放人工操作门闩: %+v %v", op, err)
	}
	if err = h.env.Store.DeleteAgent(context.Background(), change.AgentID); !model.IsCode(err, model.CodeConflict) {
		t.Fatalf("清理仍待恢复时不能删除执行归属: %v", err)
	}
	result, err := service.Reconcile(context.Background(), identity.LocalPrincipal(), change.ID, change.Version)
	if err != nil || result.Status != model.ChangeApplied {
		t.Fatalf("清理重试通过后才能最终applied: %+v %v", result, err)
	}
	op, err = h.env.Store.GetOperation(context.Background(), change.OperationID)
	if err != nil || op.Status != model.OperationCompleted {
		t.Fatalf("最终确认后才能解除操作门闩: %+v %v", op, err)
	}
	files.mu.Lock()
	defer files.mu.Unlock()
	if files.writes != 1 || files.finalizations != 2 || files.journal {
		t.Fatalf("重新核对只能清理，不能重复写正文: writes=%d finalizations=%d journal=%v", files.writes, files.finalizations, files.journal)
	}
}

func Test保留原件被外部改动时反复清理失败不能宣称已完成(t *testing.T) {
	h, service, files, change := recoveryFailureFixture(t, 0, 0, model.NewError(model.CodeConflict, "保留的原件已被外部改动"))
	for range 2 {
		_, _ = service.Reconcile(context.Background(), identity.LocalPrincipal(), change.ID, change.Version)
		current, err := service.Get(context.Background(), identity.LocalPrincipal(), change.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status != model.ChangeRecoveryRequired || current.PendingResult != model.ChangeApplied {
			t.Fatalf("目标虽已安装，无法安全处理旧原件时仍须保留恢复状态: %+v", current)
		}
		change = current
		op, err := h.env.Store.GetOperation(context.Background(), change.OperationID)
		if err != nil || op.Status != model.OperationRecoveryRequired {
			t.Fatalf("外部旧原件变化必须持续保留门闩: %+v %v", op, err)
		}
	}
	files.mu.Lock()
	defer files.mu.Unlock()
	if files.writes != 1 || files.finalizations != 3 || !files.journal {
		t.Fatalf("不得重写目标或丢弃未知原件: writes=%d finalizations=%d journal=%v", files.writes, files.finalizations, files.journal)
	}
}

func Test两个并发恢复不能倒退已核验状态且拒绝旧版本(t *testing.T) {
	h, service, files, change := failedFinishFixture(t)
	files.mu.Lock()
	files.recoverEntered = make(chan struct{}, 2)
	files.recoverRelease = make(chan struct{})
	entered, release := files.recoverEntered, files.recoverRelease
	files.mu.Unlock()
	results := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := service.Reconcile(context.Background(), identity.LocalPrincipal(), change.ID, change.Version)
			results <- err
		}()
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			close(release)
			t.Fatal("两个恢复请求应同时读取原恢复版本")
		}
	}
	close(release)
	success, conflicts := 0, 0
	for range 2 {
		select {
		case err := <-results:
			if err == nil {
				success++
			} else if model.IsCode(err, model.CodeConflict) {
				conflicts++
			} else {
				t.Fatalf("恢复返回非预期错误: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("恢复请求未完成")
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("同一恢复版本只应有一个成功: %d/%d", success, conflicts)
	}
	current, err := service.Get(context.Background(), identity.LocalPrincipal(), change.ID)
	if err != nil || current.Status != model.ChangeApplied {
		t.Fatalf("后到请求不得把applied降回applying: %+v %v", current, err)
	}
	op, err := h.env.Store.GetOperation(context.Background(), change.OperationID)
	if err != nil || op.Status != model.OperationCompleted {
		t.Fatalf("操作和变更状态必须一致: %+v %v", op, err)
	}
	if _, err = service.Reconcile(context.Background(), identity.LocalPrincipal(), change.ID, change.Version); !model.IsCode(err, model.CodeConflict) {
		t.Fatalf("服务必须拒绝旧恢复版本: %v", err)
	}
}

func Test启动恢复保留清理冲突但仍允许打开界面核对(t *testing.T) {
	_, service, files, change := recoveryFailureFixture(t, 0, 0, model.NewError(model.CodeConflict, "保留原件已变化"))
	if err := service.RecoverActive(context.Background()); err != nil {
		t.Fatalf("已持久保留的冲突不能阻止用户打开恢复界面: %v", err)
	}
	current, err := service.Get(context.Background(), identity.LocalPrincipal(), change.ID)
	if err != nil || current.Status != model.ChangeRecoveryRequired {
		t.Fatalf("启动不得清除待恢复状态: %+v %v", current, err)
	}
	files.mu.Lock()
	defer files.mu.Unlock()
	if files.writes != 1 || !files.journal {
		t.Fatal("启动核对不能再次写入或丢失保留记录")
	}
}

func Test显式规则拒绝先于人工审批且不产生副作用(t *testing.T) {
	h, _, sess, artifact, ws := changeFixture(t)
	if err := h.env.Deps.Permissions.ReplaceRules(context.Background(), []permission.Rule{{ID: "deny-manual-file", Tool: model.OperationFileChangeTool, Effect: permission.EffectDeny}, {ID: "allow-other", Tool: "*", Effect: permission.EffectAllow}}, "local"); err != nil {
		t.Fatal(err)
	}
	preview := prepareFile(t, h, sess, artifact, "denied.md", model.ChangeCreate)
	applyFile(t, h, preview, "rule-denied")
	waitFileState(t, h, preview.ID, model.ChangeDenied)
	approvals, err := h.env.Store.ListApprovals(context.Background(), "", model.Page{})
	if err != nil || len(approvals) != 0 {
		t.Fatalf("deny必须在可批准请求之前终止: %+v %v", approvals, err)
	}
	if _, err = ws.Stat(context.Background(), "denied.md"); !model.IsCode(err, model.CodeNotFound) {
		t.Fatalf("规则拒绝不能创建文件: %v", err)
	}
}

func Test活跃模型运行阻止同智能体人工修改(t *testing.T) {
	h, _, sess, artifact, ws := changeFixture(t)
	mustStatus(t, h.do("POST", "/api/v1/sessions/"+sess.ID+"/messages", map[string]string{"text": servertest.ScriptHold}), 202)
	preview := prepareFile(t, h, sess, artifact, "busy.md", model.ChangeCreate)
	accepted := applyFile(t, h, preview, "active-model")
	approval := waitFileApproval(t, h, accepted.OperationID)
	mustStatus(t, h.do("POST", "/api/v1/approvals/"+approval.ID+"/decision", model.Decision{Approve: true, Scope: model.ScopeOnce}), 200)
	result := waitFileState(t, h, preview.ID, model.ChangeConflict)
	if result.Reason != "change_busy" {
		t.Fatal("运行互斥不能误报外部文件已修改")
	}
	if _, err := ws.Stat(context.Background(), "busy.md"); !model.IsCode(err, model.CodeNotFound) {
		t.Fatalf("活跃模型运行期间不能发生人工文件副作用: %v", err)
	}
}

func Test执行或待恢复人工主体阻止同智能体模型工具(t *testing.T) {
	h, agent, sess, artifact, ws := changeFixture(t)
	ctx := context.Background()
	if err := ws.WriteFile(ctx, "existing.md", []byte("稳定的原文件")); err != nil {
		t.Fatal(err)
	}
	preview := prepareFile(t, h, sess, artifact, "plan.md", model.ChangeCreate)
	stored, err := h.env.Store.GetChangeSet(ctx, preview.ID)
	if err != nil {
		t.Fatal(err)
	}
	op := &model.Operation{ID: model.NewID(), OwnerID: "local", AgentID: agent.ID, SessionID: sess.ID, ChangeSetID: stored.ID, Status: model.OperationExecuting, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err = h.env.Store.ChangeTx(ctx, func(tx model.ChangeStore) error {
		if err := tx.CreateOperation(ctx, op); err != nil {
			return err
		}
		stored.OperationID = op.ID
		stored.Status = model.ChangeApplying
		return tx.UpdateChangeSet(ctx, stored, stored.Version)
	}); err != nil {
		t.Fatal(err)
	}
	rec := h.do("POST", "/api/v1/sessions/"+sess.ID+"/messages", map[string]string{"text": servertest.ScriptHold})
	mustStatus(t, rec, 202)
	run := decodeBody[model.Run](t, rec)
	call := model.ToolCall{ID: "read-call", Name: "read_file", Arguments: json.RawMessage(`{"path":"existing.md"}`)}
	if _, err = h.env.Deps.Tools.Execute(ctx, run.ID, call); !model.IsCode(err, model.CodeConflict) {
		t.Fatalf("执行中的人工主体必须阻止模型工具: %v", err)
	}
	if err = h.env.Store.ChangeTx(ctx, func(tx model.ChangeStore) error {
		op.Status = model.OperationRecoveryRequired
		if err := tx.UpdateOperation(ctx, op, model.OperationExecuting); err != nil {
			return err
		}
		stored.Status = model.ChangeRecoveryRequired
		return tx.UpdateChangeSet(ctx, stored, stored.Version)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = h.env.Deps.Tools.Execute(ctx, run.ID, call); !model.IsCode(err, model.CodeConflict) {
		t.Fatalf("待恢复主体必须继续阻止模型工具: %v", err)
	}
	if err = h.env.Store.ChangeTx(ctx, func(tx model.ChangeStore) error {
		op.Status = model.OperationFailed
		if err := tx.UpdateOperation(ctx, op, model.OperationRecoveryRequired); err != nil {
			return err
		}
		stored.Status = model.ChangeFailed
		return tx.UpdateChangeSet(ctx, stored, stored.Version)
	}); err != nil {
		t.Fatal(err)
	}
	result, err := h.env.Deps.Tools.Execute(ctx, run.ID, call)
	if err != nil || result.IsError {
		t.Fatalf("核验后的终态不应永久锁住原模型工具: %+v %v", result, err)
	}
}
