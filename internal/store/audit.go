package store

import (
	"context"
	"database/sql"
	"strings"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

const auditCols = `id, at, actor, agent_id, session_id, run_id, action, resource, outcome, details`

// AppendAudit 追加一条审计记录。审计只追加不修改，也不随智能体删除而级联。
// Details 必须在调用前脱敏；仓储只校验它是合法 JSON。
func (s *Store) AppendAudit(ctx context.Context, e *model.AuditEntry) error {
	if err := requireID("audit", e.ID); err != nil {
		return err
	}
	if e.At.IsZero() {
		return model.NewError(model.CodeInvalid, "store: audit.At 不能为零值")
	}
	details, err := jsonArg("audit.details", e.Details)
	if err != nil {
		return err
	}
	return s.write(ctx, func(q dbtx) error {
		_, err := q.ExecContext(ctx, `INSERT INTO audit_log(`+auditCols+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			e.ID, fmtTime(e.At), e.Actor, e.AgentID, e.SessionID, e.RunID, e.Action, e.Resource,
			e.Outcome, details)
		return wrapErr(ctx, "append audit", err)
	})
}

// ListAudit 按过滤条件分页返回审计记录，At 降序（同一时刻按 ID 降序）。
func (s *Store) ListAudit(ctx context.Context, f model.AuditFilter, p model.Page) ([]*model.AuditEntry, error) {
	var (
		where []string
		args  []any
	)
	if f.AgentID != "" {
		where = append(where, "agent_id = ?")
		args = append(args, f.AgentID)
	}
	if f.SessionID != "" {
		where = append(where, "session_id = ?")
		args = append(args, f.SessionID)
	}
	if f.RunID != "" {
		where = append(where, "run_id = ?")
		args = append(args, f.RunID)
	}
	if f.Actor != "" {
		where = append(where, "actor = ?")
		args = append(args, f.Actor)
	}
	if f.Action != "" {
		where = append(where, "action = ?")
		args = append(args, f.Action)
	}
	if !f.Since.IsZero() {
		where = append(where, "at >= ?")
		args = append(args, fmtTime(f.Since))
	}
	query := `SELECT ` + auditCols + ` FROM audit_log`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	limit, offset := pageOf(p)
	query += " ORDER BY at DESC, id DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := s.rq().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapErr(ctx, "list audit", err)
	}
	out, err := collect(rows, scanAudit)
	if err != nil {
		return nil, wrapErr(ctx, "list audit", err)
	}
	return out, nil
}

func scanAudit(sc scanner) (*model.AuditEntry, error) {
	var (
		e         model.AuditEntry
		at        dbTime
		agentID   sql.NullString
		sessionID sql.NullString
		runID     sql.NullString
		resource  sql.NullString
		details   dbJSON
	)
	if err := sc.Scan(&e.ID, &at, &e.Actor, &agentID, &sessionID, &runID, &e.Action, &resource,
		&e.Outcome, &details); err != nil {
		return nil, err
	}
	e.At = at.t
	e.AgentID = agentID.String
	e.SessionID = sessionID.String
	e.RunID = runID.String
	e.Resource = resource.String
	e.Details = details.raw
	return &e, nil
}
