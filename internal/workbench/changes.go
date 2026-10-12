package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/event"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

// ChangeReceipt 是工作区条件写入结果；未确认的副作用必须保留恢复记录。
type ChangeReceipt struct {
	State, Reason string
	Before, After model.ChangeSnapshot
}

// ChangeFiles 由组合根适配，文件副作用只在受审批工具 ExecuteApproved 内调用。
type ChangeFiles interface {
	Snapshot(context.Context, string, string, int64) (model.ChangeSnapshot, error)
	Apply(context.Context, string, string, string, model.ChangeSnapshot, *string) (ChangeReceipt, error)
	Recover(context.Context, string, string, string, model.ChangeSnapshot, *string) (ChangeReceipt, error)
	Finalize(context.Context, string, string, string) error
	Supported() bool
}

// UserOperationExecutor 复用现有工具执行器；只允许持久化的人工文件主体。
type UserOperationExecutor interface {
	ExecuteUserOperation(context.Context, string, model.ToolCall) (model.ToolResult, error)
}

// ChangeService 管理预览、执行意图和恢复，线程安全；审批期间不持有文件锁或事务。
type ChangeService struct {
	store    model.ChangeStore
	files    ChangeFiles
	bus      *event.Bus
	executor UserOperationExecutor
	ctx      context.Context
	stop     context.CancelFunc
	mu       sync.Mutex
	pending  map[string]context.CancelFunc
	wg       sync.WaitGroup
	closed   bool
}

// NewChanges 创建独立文件操作服务；调用 BindExecutor 后才能受理应用。
func NewChanges(store model.ChangeStore, files ChangeFiles, bus *event.Bus) (*ChangeService, error) {
	if store == nil || files == nil {
		return nil, failure(model.CodeUnavailable, "unavailable", nil)
	}
	ctx, stop := context.WithCancel(context.Background())
	return &ChangeService{store: store, files: files, bus: bus, ctx: ctx, stop: stop, pending: map[string]context.CancelFunc{}}, nil
}

// BindExecutor 只在启动期调用，HTTP 开放前完成装配。
func (s *ChangeService) BindExecutor(e UserOperationExecutor) { s.executor = e }

// Supported 只在完整装配及工作区平台支持时声明能力。
func (s *ChangeService) Supported() bool { return s.executor != nil && s.files.Supported() }

// Close 取消审批并等待已开始的副作用完成必要的恢复落账。
func (s *ChangeService) Close() { s.mu.Lock(); s.closed = true; s.stop(); s.mu.Unlock(); s.wg.Wait() }

func (s *ChangeService) publish(c *model.ChangeSet) {
	if s.bus != nil {
		s.bus.Publish(event.Event{Type: "changeset.updated", AgentID: c.AgentID, SessionID: c.SessionID, Payload: map[string]any{"id": c.ID, "version": c.Version, "status": c.Status, "operation_id": c.OperationID}})
	}
}
func changePath(p string) bool {
	return p != "" && p == path.Clean(p) && p != "." && !strings.HasPrefix(p, "/") && !strings.ContainsAny(p, "\\:\x00\r\n") && p != ".." && !strings.HasPrefix(p, "../")
}
func (s *ChangeService) get(ctx context.Context, p model.Principal, id string) (*model.ChangeSet, error) {
	uid, err := changeOwner(p)
	if err != nil {
		return nil, err
	}
	c, err := s.store.GetChangeSet(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.OwnerID != uid {
		return nil, model.NotFound("change", id)
	}
	return c, nil
}
func changeOwner(p model.Principal) (string, error) {
	if p.Kind != model.PrincipalLocalUser {
		return "", failure(model.CodeForbidden, "owner_required", nil)
	}
	return owner(p)
}

// Get 只返回所属主体的变更；JSON 不含前像或正文。
func (s *ChangeService) Get(ctx context.Context, p model.Principal, id string) (*model.ChangeSet, error) {
	return s.get(ctx, p, id)
}

// List 保留历史文件操作，不依赖模型工作进程运行。
func (s *ChangeService) List(ctx context.Context, p model.Principal, sessionID string, page model.Page) ([]*model.ChangeSet, error) {
	uid, err := changeOwner(p)
	if err != nil {
		return nil, err
	}
	if _, err = s.store.GetSession(ctx, sessionID); err != nil {
		return nil, err
	}
	return s.store.ListChangeSets(ctx, uid, sessionID, page)
}

// Diff 单独提供已经冻结的前像与提议，不使用客户端替换正文。
func (s *ChangeService) Diff(ctx context.Context, p model.Principal, id string) (*model.ChangeDiff, error) {
	c, err := s.get(ctx, p, id)
	if err != nil {
		return nil, err
	}
	return &model.ChangeDiff{ChangeSet: c, Before: c.Base.Content, After: c.Proposed}, nil
}

func bindDigest(c *model.ChangeSet) string {
	b, _ := json.Marshal([]any{c.ID, c.OwnerID, c.AgentID, c.SessionID, c.ArtifactID, c.RevisionID, c.TargetPath, c.Kind, c.Base.Exists, c.Base.SHA256, c.Base.Identity, c.Base.RootIdentity, c.ProposedSHA256, c.UndoOf, c.ExpiresAt})
	return digest(string(b))
}
func (s *ChangeService) savePreview(ctx context.Context, p model.Principal, c *model.ChangeSet) (*model.ChangeSet, error) {
	c.PreviewDigest = bindDigest(c)
	err := s.store.ChangeTx(ctx, func(tx model.ChangeStore) error {
		if err := tx.CreateChangeSet(ctx, c); err != nil {
			return err
		}
		return audit(ctx, tx, p, "changeset.prepared", c.ID, c.AgentID, c.SessionID)
	})
	if err == nil {
		s.publish(c)
	}
	return c, err
}

// Prepare 冻结真实文件前像与不可变成果版本，仅预览不修改目标。
func (s *ChangeService) Prepare(ctx context.Context, p model.Principal, sessionID string, in model.ChangeInput) (*model.ChangeSet, error) {
	uid, err := changeOwner(p)
	if err != nil {
		return nil, err
	}
	if !s.Supported() {
		return nil, failure(model.CodeUnavailable, "change_unsupported", nil)
	}
	if !changePath(in.TargetPath) || (in.Operation != model.ChangeCreate && in.Operation != model.ChangeReplace) {
		return nil, failure(model.CodeInvalid, "change_path", nil)
	}
	sess, err := s.store.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	a, err := s.store.GetArtifact(ctx, in.ArtifactID)
	if err != nil {
		return nil, err
	}
	if a.SessionID != sessionID || a.AgentID != sess.AgentID {
		return nil, scopeMismatch()
	}
	r, err := s.store.GetArtifactRevision(ctx, a.ID, in.RevisionID)
	if err != nil {
		return nil, err
	}
	if validText(r.Content) != nil || digest(r.Content) != r.SHA256 || int64(len(r.Content)) != r.ByteSize {
		return nil, failure(model.CodeConflict, "content_integrity", nil)
	}
	base, err := s.files.Snapshot(ctx, sess.AgentID, in.TargetPath, model.WorkbenchTextLimit)
	if err != nil {
		return nil, err
	}
	if base.Exists != (in.Operation == model.ChangeReplace) {
		return nil, failure(model.CodeConflict, "change_target", nil)
	}
	now := time.Now().UTC()
	c := &model.ChangeSet{ID: model.NewID(), OwnerID: uid, AgentID: sess.AgentID, SessionID: sessionID, ArtifactID: a.ID, RevisionID: r.ID, TargetPath: in.TargetPath, Kind: in.Operation, Status: model.ChangePrepared, Version: 1, BaseExists: base.Exists, BaseSHA256: base.SHA256, ProposedSHA256: r.SHA256, ProposedSize: r.ByteSize, CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute), Base: base, Proposed: r.Content}
	return s.savePreview(ctx, p, c)
}

// UndoPreview 以应用后的文件为基线生成新预览；任何外部改动都拒绝撤销。
func (s *ChangeService) UndoPreview(ctx context.Context, p model.Principal, id string, version int64) (*model.ChangeSet, error) {
	c, err := s.get(ctx, p, id)
	if err != nil {
		return nil, err
	}
	if c.Version != version || c.Status != model.ChangeApplied {
		return nil, failure(model.CodeConflict, "change_state", nil)
	}
	current, err := s.files.Snapshot(ctx, c.AgentID, c.TargetPath, model.WorkbenchTextLimit)
	if err != nil {
		return nil, err
	}
	if !sameSnapshot(current, c.Applied) {
		return nil, failure(model.CodeConflict, "change_external", nil)
	}
	kind := model.ChangeReplace
	content := c.Base.Content
	if !c.Base.Exists {
		kind = model.ChangeDelete
		content = ""
	} else if !current.Exists {
		kind = model.ChangeCreate
	}
	now := time.Now().UTC()
	next := &model.ChangeSet{ID: model.NewID(), OwnerID: c.OwnerID, AgentID: c.AgentID, SessionID: c.SessionID, ArtifactID: c.ArtifactID, RevisionID: c.RevisionID, TargetPath: c.TargetPath, Kind: kind, Status: model.ChangePrepared, Version: 1, BaseExists: current.Exists, BaseSHA256: current.SHA256, ProposedSHA256: digest(content), ProposedSize: int64(len(content)), UndoOf: c.ID, CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute), Base: current, Proposed: content}
	return s.savePreview(ctx, p, next)
}
func sameSnapshot(a, b model.ChangeSnapshot) bool {
	return a.Exists == b.Exists && a.SHA256 == b.SHA256 && a.Size == b.Size && a.Identity == b.Identity && a.RootIdentity == b.RootIdentity
}
func proposed(c *model.ChangeSet) *string {
	if c.Kind == model.ChangeDelete {
		return nil
	}
	return &c.Proposed
}
func conflictReason(err error) string {
	var e *model.Error
	if errors.As(err, &e) && e.Details["reason"] == "change_busy" {
		return "change_busy"
	}
	return "change_external"
}

// Apply 原子受理已预览请求；返回等待审批状态，最终状态必须重读。
func (s *ChangeService) Apply(ctx context.Context, p model.Principal, id string, in model.ChangeApplyInput, key string) (*model.ChangeSet, error) {
	if !s.Supported() {
		return nil, failure(model.CodeUnavailable, "change_unsupported", nil)
	}
	uid, err := changeOwner(p)
	if err != nil {
		return nil, err
	}
	if key == "" || len(key) > 160 {
		return nil, failure(model.CodeInvalid, "change_key", nil)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, failure(model.CodeUnavailable, "unavailable", nil)
	}
	var c *model.ChangeSet
	var op *model.Operation
	b, _ := json.Marshal(in)
	hash := digest(string(b))
	scope := "change.apply:" + id
	err = s.store.ChangeTx(ctx, func(tx model.ChangeStore) error {
		if prev, e := tx.GetWorkbenchRequest(ctx, uid, scope, key); e == nil {
			if prev.RequestSHA256 != hash {
				return failure(model.CodeConflict, "change_key", nil)
			}
			c, e = tx.GetChangeSet(ctx, id)
			return e
		} else if !model.IsCode(e, model.CodeNotFound) {
			return e
		}
		var e error
		c, e = tx.GetChangeSet(ctx, id)
		if e != nil {
			return e
		}
		if c.OwnerID != uid {
			return model.NotFound("change", id)
		}
		if c.Status != model.ChangePrepared || c.Version != in.ExpectedVersion || c.PreviewDigest != in.PreviewDigest || c.PreviewDigest != bindDigest(c) {
			return failure(model.CodeConflict, "change_state", nil)
		}
		if time.Now().After(c.ExpiresAt) {
			return failure(model.CodeConflict, "change_expired", nil)
		}
		if _, e = tx.GetSession(ctx, c.SessionID); e != nil {
			return e
		}
		ops, e := tx.ListOperations(ctx, model.OperationFilter{AgentID: c.AgentID, ActiveOnly: true}, model.Page{Limit: 1})
		if e != nil {
			return e
		}
		if len(ops) > 0 {
			return failure(model.CodeConflict, "change_busy", nil)
		}
		now := time.Now().UTC()
		op = &model.Operation{ID: model.NewID(), OwnerID: uid, AgentID: c.AgentID, SessionID: c.SessionID, ChangeSetID: c.ID, Status: model.OperationAwaitingApproval, CreatedAt: now, UpdatedAt: now}
		if e = tx.CreateOperation(ctx, op); e != nil {
			return e
		}
		c.OperationID = op.ID
		c.Status = model.ChangeAwaitingApproval
		if e = tx.UpdateChangeSet(ctx, c, c.Version); e != nil {
			return e
		}
		if e = tx.CreateWorkbenchRequest(ctx, &model.WorkbenchRequest{OwnerID: uid, Scope: scope, Key: key, RequestSHA256: hash, ResourceID: op.ID, CreatedAt: now}); e != nil {
			return e
		}
		return audit(ctx, tx, p, "changeset.requested", c.ID, c.AgentID, c.SessionID)
	})
	if err != nil {
		return nil, err
	}
	if op != nil {
		runctx, cancel := context.WithDeadline(s.ctx, c.ExpiresAt)
		s.pending[c.ID] = cancel
		s.wg.Add(1)
		go s.execute(runctx, cancel, op.ID, c.ID, c.PreviewDigest)
	}
	s.publish(c)
	return c, nil
}

func (s *ChangeService) execute(ctx context.Context, cancel context.CancelFunc, operationID, id, hash string) {
	defer s.wg.Done()
	defer cancel()
	defer func() { s.mu.Lock(); delete(s.pending, id); s.mu.Unlock() }()
	args, _ := json.Marshal(map[string]string{"change_id": id, "preview_digest": hash})
	res, err := s.executor.ExecuteUserOperation(ctx, operationID, model.ToolCall{ID: operationID, Name: "apply_file_change", Arguments: args})
	if err == nil && !res.IsError {
		return
	}
	state := model.ChangeFailed
	reason := "change_failed"
	if model.IsCode(err, model.CodeDenied) {
		state = model.ChangeDenied
		reason = "change_denied"
	} else if model.IsCode(err, model.CodeConflict) {
		state = model.ChangeConflict
		reason = conflictReason(err)
	} else if model.IsCode(err, model.CodeCancelled) || ctx.Err() == context.Canceled {
		state = model.ChangeCancelled
		reason = "change_cancelled"
	} else if model.IsCode(err, model.CodeTimeout) || ctx.Err() == context.DeadlineExceeded {
		state = model.ChangeExpired
		reason = "change_expired"
	}
	finishctx, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer stop()
	if current, e := s.store.GetChangeSet(finishctx, id); e == nil && current.Status == model.ChangeApplying {
		state = model.ChangeRecoveryRequired
		reason = "change_recovery"
	}
	_ = s.finish(finishctx, id, state, reason, model.ChangeSnapshot{})
}

// OperationContext 解析真实主体与预览；不允许执行已终结或未绑定的操作。
func (s *ChangeService) OperationContext(ctx context.Context, id string) (*model.Operation, *model.ChangeSet, error) {
	op, err := s.store.GetOperation(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	c, err := s.store.GetChangeSet(ctx, op.ChangeSetID)
	if err != nil {
		return nil, nil, err
	}
	if op.Status != model.OperationAwaitingApproval || c.Status != model.ChangeAwaitingApproval || c.OperationID != id || op.OwnerID != c.OwnerID || op.SessionID != c.SessionID || op.AgentID != c.AgentID || c.PreviewDigest != bindDigest(c) || digest(c.Proposed) != c.ProposedSHA256 || int64(len(c.Proposed)) != c.ProposedSize {
		return nil, nil, failure(model.CodeConflict, "change_state", nil)
	}
	return op, c, nil
}

// ExecuteApproved 仅供固定人工工具在完成 permission 判定后调用。
func (s *ChangeService) ExecuteApproved(ctx context.Context, operationID, id, hash string) error {
	_, c, err := s.OperationContext(ctx, operationID)
	if err != nil {
		return err
	}
	if c.ID != id || c.PreviewDigest != hash {
		return failure(model.CodeConflict, "change_state", nil)
	}
	if time.Now().After(c.ExpiresAt) {
		return failure(model.CodeTimeout, "change_expired", nil)
	}
	err = s.store.ChangeTx(ctx, func(tx model.ChangeStore) error {
		op, e := tx.GetOperation(ctx, operationID)
		if e != nil {
			return e
		}
		old := op.Status
		op.Status = model.OperationExecuting
		op.UpdatedAt = time.Now().UTC()
		if e = tx.UpdateOperation(ctx, op, old); e != nil {
			return e
		}
		c.Status = model.ChangeApplying
		return tx.UpdateChangeSet(ctx, c, c.Version)
	})
	if err != nil {
		return err
	}
	s.publish(c)
	receipt, writeErr := s.files.Apply(ctx, c.AgentID, operationID, c.TargetPath, c.Base, proposed(c))
	finishctx, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer stop()
	if receipt.State == "applied" && writeErr == nil {
		return s.recordAndFinalize(finishctx, c, model.ChangeApplied, "", receipt.After)
	}
	state := model.ChangeFailed
	reason := "change_failed"
	if receipt.State == "recovery_required" {
		state = model.ChangeRecoveryRequired
		reason = "change_recovery"
	} else if model.IsCode(writeErr, model.CodeConflict) {
		state = model.ChangeConflict
		reason = conflictReason(writeErr)
	} else if model.IsCode(writeErr, model.CodeCancelled) {
		state = model.ChangeCancelled
		reason = "change_cancelled"
	}
	if receipt.State == "restored" || receipt.State == "unchanged" {
		err = s.recordAndFinalize(finishctx, c, state, reason, receipt.After)
	} else {
		err = s.finish(finishctx, id, state, reason, receipt.After)
	}
	if err != nil {
		return err
	}
	if writeErr != nil {
		return writeErr
	}
	return failure(model.CodeInternal, reason, nil)
}

// recordAndFinalize 先持久保存真实文件结果，再清理原件，最后终结状态。
// 清理失败同样阻止释放操作门闩；未知原件永不静默删除。
func (s *ChangeService) recordAndFinalize(ctx context.Context, expected *model.ChangeSet, status model.ChangeStatus, reason string, after model.ChangeSnapshot) error {
	var recorded *model.ChangeSet
	err := s.store.ChangeTx(ctx, func(tx model.ChangeStore) error {
		var e error
		recorded, e = tx.GetChangeSet(ctx, expected.ID)
		if e != nil {
			return e
		}
		if recorded.Version != expected.Version || recorded.Status != expected.Status {
			return failure(model.CodeConflict, "change_state", nil)
		}
		recorded.PendingResult = status
		recorded.Applied = after
		recorded.Reason = reason
		if status == model.ChangeApplied {
			now := time.Now().UTC()
			recorded.AppliedAt = &now
		}
		return tx.UpdateChangeSet(ctx, recorded, recorded.Version)
	})
	if err != nil {
		return err
	}
	if err = s.files.Finalize(ctx, recorded.AgentID, recorded.OperationID, recorded.TargetPath); err != nil && !model.IsCode(err, model.CodeNotFound) {
		if saveErr := s.finishExpected(ctx, recorded.ID, model.ChangeRecoveryRequired, "change_recovery", after, recorded); saveErr != nil {
			return saveErr
		}
		return failure(model.CodeConflict, "change_recovery", nil)
	}
	return s.finishExpected(ctx, recorded.ID, status, reason, after, recorded)
}

func (s *ChangeService) finish(ctx context.Context, id string, status model.ChangeStatus, reason string, after model.ChangeSnapshot) error {
	return s.finishExpected(ctx, id, status, reason, after, nil)
}
func (s *ChangeService) finishExpected(ctx context.Context, id string, status model.ChangeStatus, reason string, after model.ChangeSnapshot, expected *model.ChangeSet) error {
	var c *model.ChangeSet
	err := s.store.ChangeTx(ctx, func(tx model.ChangeStore) error {
		var e error
		c, e = tx.GetChangeSet(ctx, id)
		if e != nil {
			return e
		}
		if expected != nil && (c.Version != expected.Version || c.Status != expected.Status) {
			return failure(model.CodeConflict, "change_state", nil)
		}
		if expected == nil && (c.Status == model.ChangeApplied || c.Status == model.ChangeRecoveryRequired || c.Status == model.ChangeConflict || c.Status == model.ChangeFailed || c.Status == model.ChangeCancelled || c.Status == model.ChangeDenied || c.Status == model.ChangeExpired) {
			return nil
		}
		c.Status = status
		c.Reason = reason
		if after.RootIdentity != "" || after.Identity != "" {
			c.Applied = after
		}
		if status == model.ChangeApplied {
			now := time.Now().UTC()
			c.AppliedAt = &now
		}
		if e = tx.UpdateChangeSet(ctx, c, c.Version); e != nil {
			return e
		}
		op, e := tx.GetOperation(ctx, c.OperationID)
		if e != nil {
			return e
		}
		old := op.Status
		op.Status = model.OperationFailed
		if status == model.ChangeApplied {
			op.Status = model.OperationCompleted
		} else if status == model.ChangeCancelled {
			op.Status = model.OperationCancelled
		} else if status == model.ChangeExpired {
			op.Status = model.OperationExpired
		} else if status == model.ChangeRecoveryRequired {
			op.Status = model.OperationRecoveryRequired
		}
		op.UpdatedAt = time.Now().UTC()
		if e = tx.UpdateOperation(ctx, op, old); e != nil {
			return e
		}
		outcome := "failed"
		if status == model.ChangeApplied {
			outcome = "ok"
		}
		return tx.AppendAudit(ctx, &model.AuditEntry{ID: model.NewID(), At: time.Now().UTC(), Actor: c.OwnerID, Action: "changeset." + string(status), Resource: c.ID, AgentID: c.AgentID, SessionID: c.SessionID, Outcome: outcome})
	})
	if err == nil && c != nil {
		s.publish(c)
	}
	return err
}

// Cancel 只取消尚未执行的审批；已开始应用必须先完成文件状态对账。
func (s *ChangeService) Cancel(ctx context.Context, p model.Principal, id string, version int64) (*model.ChangeSet, error) {
	c, err := s.get(ctx, p, id)
	if err != nil {
		return nil, err
	}
	if c.Version != version || c.Status != model.ChangeAwaitingApproval {
		return nil, failure(model.CodeConflict, "change_state", nil)
	}
	s.mu.Lock()
	cancel := s.pending[id]
	s.mu.Unlock()
	if cancel == nil {
		return nil, failure(model.CodeConflict, "change_state", nil)
	}
	cancel()
	return c, nil
}

// RecoverActive 在 HTTP 开放前对账崩溃遗留操作，绝不自动重新执行审批或覆盖未知内容。
func (s *ChangeService) RecoverActive(ctx context.Context) error {
	for offset := 0; ; offset += 200 {
		ops, err := s.store.ListOperations(ctx, model.OperationFilter{}, model.Page{Limit: 200, Offset: offset})
		if err != nil {
			return err
		}
		for _, op := range ops {
			c, e := s.store.GetChangeSet(ctx, op.ChangeSetID)
			if e != nil {
				if model.IsCode(e, model.CodeNotFound) {
					continue
				}
				return e
			}
			if op.Status.Terminal() {
				continue
			}
			if c.Status == model.ChangeAwaitingApproval {
				if e = s.finish(ctx, c.ID, model.ChangeExpired, "change_interrupted", model.ChangeSnapshot{}); e != nil {
					return e
				}
				continue
			}
			if c.Status != model.ChangeApplying && c.Status != model.ChangeRecoveryRequired {
				continue
			}
			if e = s.recoverOne(ctx, c); e != nil {
				current, readErr := s.store.GetChangeSet(ctx, c.ID)
				if readErr == nil && current.Status == model.ChangeRecoveryRequired && model.IsCode(e, model.CodeConflict) {
					continue
				}
				return e
			}
		}
		if len(ops) < 200 {
			return nil
		}
	}
}

func (s *ChangeService) recoverOne(ctx context.Context, c *model.ChangeSet) error {
	r, err := s.files.Recover(ctx, c.AgentID, c.OperationID, c.TargetPath, c.Base, proposed(c))
	if model.IsCode(err, model.CodeNotFound) {
		current, e := s.files.Snapshot(ctx, c.AgentID, c.TargetPath, model.WorkbenchTextLimit)
		if e == nil && c.PendingResult != "" && c.Applied.RootIdentity != "" && sameSnapshot(current, c.Applied) {
			r.After = current
			r.State = "restored"
			if c.PendingResult == model.ChangeApplied {
				r.State = "applied"
			}
			err = nil
		} else if e == nil && sameSnapshot(current, c.Base) {
			r.State = "restored"
			r.After = current
			err = nil
		}
	}
	status := model.ChangeRecoveryRequired
	reason := "change_recovery"
	if err == nil && r.State == "applied" {
		status = model.ChangeApplied
		reason = ""
	} else if err == nil && (r.State == "restored" || r.State == "unchanged") {
		status = model.ChangeFailed
		reason = "change_interrupted"
		if c.PendingResult != "" && c.PendingResult != model.ChangeApplied {
			status = c.PendingResult
			reason = c.Reason
		}
	}
	if status == model.ChangeRecoveryRequired {
		return s.finishExpected(ctx, c.ID, status, reason, r.After, c)
	}
	return s.recordAndFinalize(ctx, c, status, reason, r.After)
}

// Reconcile 显式核对文件状态，必要时只将已保留原件恢复至空缺目标，不覆盖外部编辑。
func (s *ChangeService) Reconcile(ctx context.Context, p model.Principal, id string, version int64) (*model.ChangeSet, error) {
	c, err := s.get(ctx, p, id)
	if err != nil {
		return nil, err
	}
	if c.Version != version || c.Status != model.ChangeRecoveryRequired {
		return nil, failure(model.CodeConflict, "change_state", nil)
	}
	if err = s.recoverOne(ctx, c); err != nil {
		return nil, err
	}
	return s.get(ctx, p, id)
}
