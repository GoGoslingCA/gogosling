package store

import (
	"context"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"strings"
)

const operationCols = `id,owner_id,agent_id,session_id,change_set_id,status,created_at,updated_at`

func validOperationStatus(status model.OperationStatus) bool {
	switch status {
	case model.OperationCreated, model.OperationAwaitingApproval, model.OperationExecuting, model.OperationCompleted, model.OperationFailed, model.OperationCancelled, model.OperationExpired, model.OperationRecoveryRequired:
		return true
	}
	return false
}

// CreateOperation 写入真实用户操作，不创建任何模型 Run。
func (s *Store) CreateOperation(ctx context.Context, op *model.Operation) error {
	if err := requireID("operation", op.ID); err != nil {
		return err
	}
	if op.OwnerID == "" || op.AgentID == "" || op.SessionID == "" || op.ChangeSetID == "" || !validOperationStatus(op.Status) {
		return model.NewError(model.CodeInvalid, "store: operation identity or status invalid")
	}
	return s.write(ctx, func(q dbtx) error {
		_, err := q.ExecContext(ctx, `INSERT INTO operations(`+operationCols+`) VALUES(?,?,?,?,?,?,?,?)`, op.ID, op.OwnerID, op.AgentID, op.SessionID, op.ChangeSetID, op.Status, fmtTime(op.CreatedAt), fmtTime(op.UpdatedAt))
		return wrapErr(ctx, "create operation", err)
	})
}

// GetOperation 读取操作主体供组合根解析真实归属。
func (s *Store) GetOperation(ctx context.Context, id string) (*model.Operation, error) {
	op, err := scanOperation(s.rq().QueryRowContext(ctx, `SELECT `+operationCols+` FROM operations WHERE id=?`, id))
	if err != nil {
		return nil, notFoundOr(ctx, "get operation", "operation", id, err)
	}
	return op, nil
}

// UpdateOperation 通过状态条件原子推进；不接受替换主体或变更预览关联。
func (s *Store) UpdateOperation(ctx context.Context, op *model.Operation, expected model.OperationStatus) error {
	if !validOperationStatus(op.Status) || !validOperationStatus(expected) {
		return model.NewError(model.CodeInvalid, "store: operation status invalid")
	}
	return s.write(ctx, func(q dbtx) error {
		current, err := scanOperation(q.QueryRowContext(ctx, `SELECT `+operationCols+` FROM operations WHERE id=?`, op.ID))
		if err != nil {
			return notFoundOr(ctx, "update operation", "operation", op.ID, err)
		}
		if current.OwnerID != op.OwnerID || current.AgentID != op.AgentID || current.SessionID != op.SessionID || current.ChangeSetID != op.ChangeSetID {
			return model.NewError(model.CodeConflict, "store: operation scope is immutable")
		}
		if current.Status != expected || (current.Status.Terminal() && op.Status != current.Status) {
			return &model.Error{Code: model.CodeConflict, Message: "store: operation state changed", Details: map[string]any{"reason": "stale_status", "current_status": current.Status}}
		}
		res, err := q.ExecContext(ctx, `UPDATE operations SET status=?,updated_at=? WHERE id=? AND status=?`, op.Status, fmtTime(op.UpdatedAt), op.ID, expected)
		return affected(ctx, "update operation", "operation", op.ID, res, err)
	})
}

// ListOperations 列出筛选后的操作；正文从不进入操作记录。
func (s *Store) ListOperations(ctx context.Context, f model.OperationFilter, page model.Page) ([]*model.Operation, error) {
	where := []string{"1=1"}
	args := []any{}
	for _, filter := range []struct{ column, value string }{{"owner_id", f.OwnerID}, {"agent_id", f.AgentID}, {"session_id", f.SessionID}, {"change_set_id", f.ChangeSetID}, {"status", string(f.Status)}} {
		if filter.value != "" {
			where = append(where, filter.column+"=?")
			args = append(args, filter.value)
		}
	}
	if f.ActiveOnly {
		where = append(where, "status IN ('created','awaiting_approval','executing','recovery_required')")
	}
	limit, offset := pageOf(page)
	args = append(args, limit, offset)
	rows, err := s.rq().QueryContext(ctx, `SELECT `+operationCols+` FROM operations WHERE `+strings.Join(where, " AND ")+` ORDER BY created_at,id LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, wrapErr(ctx, "list operations", err)
	}
	out, err := collect(rows, scanOperation)
	return out, wrapErr(ctx, "list operations", err)
}

func scanOperation(sc scanner) (*model.Operation, error) {
	var op model.Operation
	var created, updated dbTime
	if err := sc.Scan(&op.ID, &op.OwnerID, &op.AgentID, &op.SessionID, &op.ChangeSetID, &op.Status, &created, &updated); err != nil {
		return nil, err
	}
	op.CreatedAt = created.t
	op.UpdatedAt = updated.t
	return &op, nil
}

// preventActiveOperationDeletion 与数据库触发器共同保护尚需审批、执行或恢复的主体。
func preventActiveOperationDeletion(ctx context.Context, q dbtx, column, id string) error {
	var active bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM operations WHERE `+column+`=? AND status IN ('created','awaiting_approval','executing','recovery_required'))`, id).Scan(&active); err != nil {
		return wrapErr(ctx, "check active operations", err)
	}
	if active {
		return &model.Error{Code: model.CodeConflict, Message: "store: pending file operations must be resolved before deletion", Details: map[string]any{"reason": "operation_in_progress"}}
	}
	return nil
}
