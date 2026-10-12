package store

import (
	"context"
	"database/sql"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

const approvalCols = `id, run_id, operation_id, session_id, agent_id, tool_call, risk, summary, details, status,
	scope, decided_by, created_at, expires_at, resolved_at`

// CreateApproval 插入审批请求；必须引用且只引用真实 Run 或 Operation。
func (s *Store) CreateApproval(ctx context.Context, a *model.ApprovalRequest) error {
	if err := requireID("approval", a.ID); err != nil {
		return err
	}
	if (a.RunID == "") == (a.OperationID == "") {
		return model.NewError(model.CodeInvalid, "store: approval requires exactly one execution subject")
	}
	if a.OperationID != "" && a.Scope != "" && a.Scope != model.ScopeOnce {
		return model.NewError(model.CodeInvalid, "store: user operations only accept once approval")
	}
	toolCall, err := marshalArg("approval.tool_call", a.ToolCall)
	if err != nil {
		return err
	}
	details, err := jsonArg("approval.details", a.Details)
	if err != nil {
		return err
	}
	return s.write(ctx, func(q dbtx) error {
		_, err := q.ExecContext(ctx, `INSERT INTO approvals(`+approvalCols+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			a.ID, nullableString(a.RunID), nullableString(a.OperationID), a.SessionID, a.AgentID, toolCall, string(a.Risk), a.Summary, details,
			string(a.Status), string(a.Scope), a.DecidedBy,
			fmtTime(a.CreatedAt), fmtTime(a.ExpiresAt), fmtTimePtr(a.ResolvedAt))
		return wrapErr(ctx, "create approval", err)
	})
}

// GetApproval 按 ID 读取审批请求。
func (s *Store) GetApproval(ctx context.Context, id string) (*model.ApprovalRequest, error) {
	row := s.rq().QueryRowContext(ctx, `SELECT `+approvalCols+` FROM approvals WHERE id = ?`, id)
	a, err := scanApproval(row)
	if err != nil {
		return nil, notFoundOr(ctx, "get approval", "approval", id, err)
	}
	return a, nil
}

// UpdateApproval 覆盖写入状态、范围、决定人与解决时间（请求内容本身不可变）。
func (s *Store) UpdateApproval(ctx context.Context, a *model.ApprovalRequest) error {
	if err := requireID("approval", a.ID); err != nil {
		return err
	}
	return s.write(ctx, func(q dbtx) error {
		res, err := q.ExecContext(ctx, `UPDATE approvals SET
			status = ?, scope = ?, decided_by = ?, expires_at = ?, resolved_at = ?
			WHERE id = ?`,
			string(a.Status), string(a.Scope), a.DecidedBy, fmtTime(a.ExpiresAt), fmtTimePtr(a.ResolvedAt), a.ID)
		return affected(ctx, "update approval", "approval", a.ID, res, err)
	})
}

// ListApprovals 按状态分页返回审批请求（status 为空表示全部），CreatedAt 升序。
func (s *Store) ListApprovals(ctx context.Context, status model.ApprovalStatus, p model.Page) ([]*model.ApprovalRequest, error) {
	limit, offset := pageOf(p)
	query := `SELECT ` + approvalCols + ` FROM approvals`
	args := []any{}
	if status != "" {
		query += " WHERE status = ?"
		args = append(args, string(status))
	}
	query += " ORDER BY created_at, id LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := s.rq().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapErr(ctx, "list approvals", err)
	}
	out, err := collect(rows, scanApproval)
	if err != nil {
		return nil, wrapErr(ctx, "list approvals", err)
	}
	return out, nil
}

func scanApproval(sc scanner) (*model.ApprovalRequest, error) {
	var (
		a                  model.ApprovalRequest
		toolCall           dbJSON
		risk               string
		summary            sql.NullString
		details            dbJSON
		status             string
		scope              sql.NullString
		decidedBy          sql.NullString
		runID, operationID sql.NullString
		created            dbTime
		expires            dbTime
		resolved           dbNullTime
	)
	if err := sc.Scan(&a.ID, &runID, &operationID, &a.SessionID, &a.AgentID, &toolCall, &risk, &summary, &details,
		&status, &scope, &decidedBy, &created, &expires, &resolved); err != nil {
		return nil, err
	}
	a.RunID = runID.String
	a.OperationID = operationID.String
	if err := unmarshalJSON("approval.tool_call", toolCall.raw, &a.ToolCall); err != nil {
		return nil, err
	}
	a.Risk = model.ToolRisk(risk)
	a.Summary = summary.String
	a.Details = details.raw
	a.Status = model.ApprovalStatus(status)
	a.Scope = model.ApprovalScope(scope.String)
	a.DecidedBy = decidedBy.String
	a.CreatedAt = created.t
	a.ExpiresAt = expires.t
	a.ResolvedAt = resolved.t
	return &a, nil
}
