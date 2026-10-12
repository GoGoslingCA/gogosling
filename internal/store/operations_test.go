package store

import (
	"context"
	"encoding/json"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"path/filepath"
	"testing"
	"time"
)

func makeOperation(t *testing.T, s *Store, session *model.Session) *model.Operation {
	t.Helper()
	op := &model.Operation{ID: model.NewID(), OwnerID: "local", AgentID: session.AgentID, SessionID: session.ID, ChangeSetID: model.NewID(), Status: model.OperationCreated, CreatedAt: ts(), UpdatedAt: ts()}
	if err := s.CreateOperation(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	return op
}

func Test用户操作主体归属及状态条件更新(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	a := mustCreateAgent(t, s)
	sess := mustCreateSession(t, s, a.ID, "operation-session")
	op := makeOperation(t, s, sess)
	op.Status = model.OperationAwaitingApproval
	if err := s.UpdateOperation(ctx, op, model.OperationCreated); err != nil {
		t.Fatal(err)
	}
	op.Status = model.OperationExecuting
	assertCode(t, s.UpdateOperation(ctx, op, model.OperationCreated), model.CodeConflict)
	if err := s.UpdateOperation(ctx, op, model.OperationAwaitingApproval); err != nil {
		t.Fatal(err)
	}
	changed := *op
	changed.OwnerID = "other"
	assertCode(t, s.UpdateOperation(ctx, &changed, model.OperationExecuting), model.CodeConflict)
	op.Status = model.OperationRecoveryRequired
	if err := s.UpdateOperation(ctx, op, model.OperationExecuting); err != nil {
		t.Fatal(err)
	}
	active, err := s.ListOperations(ctx, model.OperationFilter{SessionID: sess.ID, ActiveOnly: true}, model.Page{})
	if err != nil || len(active) != 1 || active[0].Status != model.OperationRecoveryRequired {
		t.Fatalf("未确认的文件操作应继续进入恢复扫描: %+v %v", active, err)
	}
	op.Status = model.OperationCompleted
	if err = s.UpdateOperation(ctx, op, model.OperationRecoveryRequired); err != nil {
		t.Fatal(err)
	}
	op.Status = model.OperationExecuting
	assertCode(t, s.UpdateOperation(ctx, op, model.OperationCompleted), model.CodeConflict)
	other := mustCreateAgent(t, s)
	invalid := *op
	invalid.ID = model.NewID()
	invalid.AgentID = other.ID
	invalid.Status = model.OperationCreated
	if err = s.CreateOperation(ctx, &invalid); err == nil {
		t.Fatal("不能创建跨智能体会话操作主体")
	}
}

func Test审批必须且只能关联一个真实执行主体(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	a := mustCreateAgent(t, s)
	sess := mustCreateSession(t, s, a.ID, "approval-operation-session")
	op := makeOperation(t, s, sess)
	run := mustCreateRun(t, s, sess)
	approval := &model.ApprovalRequest{ID: model.NewID(), OperationID: op.ID, AgentID: a.ID, SessionID: sess.ID, ToolCall: model.ToolCall{ID: "call", Name: model.OperationFileChangeTool, Arguments: json.RawMessage(`{"change_id":"change","preview_digest":"digest"}`)}, Risk: model.RiskWrite, Summary: "检查文件变更", Status: model.ApprovalPending, CreatedAt: ts(), ExpiresAt: ts().Add(time.Minute)}
	if err := s.CreateApproval(ctx, approval); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetApproval(ctx, approval.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RunID != "" || got.OperationID != op.ID {
		t.Fatal("用户审批不得绑定伪造Run")
	}
	for _, tc := range []struct {
		name               string
		runID, operationID string
		scope              model.ApprovalScope
	}{{"缺少主体", "", "", ""}, {"两个主体", run.ID, op.ID, ""}, {"未知主体", "missing-run", "", ""}, {"未知操作", "", "missing-operation", ""}, {"永久范围", "", op.ID, model.ScopeAlways}} {
		t.Run(tc.name, func(t *testing.T) {
			bad := *approval
			bad.ID = model.NewID()
			bad.RunID = tc.runID
			bad.OperationID = tc.operationID
			bad.Scope = tc.scope
			if err := s.CreateApproval(ctx, &bad); err == nil {
				t.Fatal("不真实或不明确的执行主体必须拒绝")
			}
		})
	}
	other := mustCreateSession(t, s, a.ID, "other-approval-session")
	wrong := *approval
	wrong.ID = model.NewID()
	wrong.SessionID = other.ID
	if err = s.CreateApproval(ctx, &wrong); err == nil {
		t.Fatal("审批会话必须匹配操作会话")
	}
	approval.Status = model.ApprovalApproved
	approval.Scope = model.ScopeSession
	if err = s.UpdateApproval(ctx, approval); err == nil {
		t.Fatal("数据库也必须拒绝人工长期授权")
	}
	assertCode(t, s.DeleteSession(ctx, sess.ID), model.CodeConflict)
	assertCode(t, s.DeleteAgent(ctx, a.ID), model.CodeConflict)
	op.Status = model.OperationRecoveryRequired
	if err = s.UpdateOperation(ctx, op, model.OperationCreated); err != nil {
		t.Fatal(err)
	}
	assertCode(t, s.DeleteSession(ctx, sess.ID), model.CodeConflict)
	op.Status = model.OperationCompleted
	if err = s.UpdateOperation(ctx, op, model.OperationRecoveryRequired); err != nil {
		t.Fatal(err)
	}
	approval.Scope = model.ScopeOnce
	if err = s.UpdateApproval(ctx, approval); err != nil {
		t.Fatal(err)
	}
	change := &model.ChangeSet{ID: op.ChangeSetID, OwnerID: op.OwnerID, AgentID: a.ID, SessionID: sess.ID, ArtifactID: "retained-artifact", RevisionID: "retained-revision", TargetPath: "notes.md", Kind: model.ChangeReplace, Status: model.ChangeApplied, Version: 1, OperationID: op.ID, CreatedAt: ts()}
	if err = s.CreateChangeSet(ctx, change); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteSession(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	if retained, err := s.GetOperation(ctx, op.ID); err != nil || retained.SessionID != sess.ID {
		t.Fatalf("会话删除应保留终态操作归属: %v", err)
	}
	if _, err = s.GetChangeSet(ctx, change.ID); err != nil {
		t.Fatalf("终态操作前像不能随会话删除: %v", err)
	}
	if err = s.DeleteAgent(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetApproval(ctx, approval.ID); err != nil {
		t.Fatalf("终态人工审批应保留可审计记录: %v", err)
	}
}

func Test操作迁移保留旧Run审批及原有外键(t *testing.T) {
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "gogosling.db")
	old := createVersionOneDatabase(t, file)
	second, err := migrationFS.ReadFile("migrations/002_workbench.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = old.Exec(string(second)); err != nil {
		t.Fatal(err)
	}
	if _, err = old.Exec(`INSERT INTO schema_migrations VALUES(2,'workbench',?)`, fmtTime(ts())); err != nil {
		t.Fatal(err)
	}
	legacy := &Store{path: file, writer: old, reader: old, log: testLogger()}
	agent := mustCreateAgent(t, legacy)
	sess := mustCreateSession(t, legacy, agent.ID, "legacy-session")
	run := mustCreateRun(t, legacy, sess)
	id := model.NewID()
	now := ts()
	call := `{"id":"old-call","name":"read_file","arguments":{"path":"notes.md"}}`
	if _, err = old.Exec(`INSERT INTO approvals(id,run_id,session_id,agent_id,tool_call,risk,summary,status,scope,decided_by,created_at,expires_at,resolved_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, run.ID, sess.ID, agent.ID, call, "read", "旧审批", "approved", "session", "local", fmtTime(now), fmtTime(now.Add(time.Minute)), fmtTime(now)); err != nil {
		t.Fatal(err)
	}
	if err = old.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, file, WithLogger(testLogger()))
	if err != nil {
		t.Fatalf("应原样保留Run审批并完成迁移: %v", err)
	}
	defer s.Close()
	got, err := s.GetApproval(ctx, id)
	if err != nil || got.RunID != run.ID || got.OperationID != "" || got.Status != model.ApprovalApproved || got.Scope != model.ScopeSession || got.ToolCall.ID != "old-call" {
		t.Fatalf("旧审批主体、状态和长期范围应保持: %+v %v", got, err)
	}
	rows, err := s.reader.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("升级不能产生悬空外键")
	}
}
