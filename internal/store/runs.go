package store

import (
	"context"
	"database/sql"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

const runCols = `id, session_id, agent_id, status, trigger, input_message_id, output_message_id,
	error, input_tokens, output_tokens, cache_read_tokens, created_at, started_at, ended_at`

// CreateRun 插入运行记录；SessionID 不存在时返回 CodeConflict（外键）。
func (s *Store) CreateRun(ctx context.Context, r *model.Run) error {
	if err := requireID("run", r.ID); err != nil {
		return err
	}
	return s.write(ctx, func(q dbtx) error {
		_, err := q.ExecContext(ctx, `INSERT INTO runs(`+runCols+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.SessionID, r.AgentID, string(r.Status), string(r.Trigger),
			r.InputMessageID, r.OutputMessageID, r.Error,
			r.Usage.InputTokens, r.Usage.OutputTokens, r.Usage.CacheReadTokens,
			fmtTime(r.CreatedAt), fmtTimePtr(r.StartedAt), fmtTimePtr(r.EndedAt))
		return wrapErr(ctx, "create run", err)
	})
}

// GetRun 按 ID 读取运行。
func (s *Store) GetRun(ctx context.Context, id string) (*model.Run, error) {
	row := s.rq().QueryRowContext(ctx, `SELECT `+runCols+` FROM runs WHERE id = ?`, id)
	r, err := scanRun(row)
	if err != nil {
		return nil, notFoundOr(ctx, "get run", "run", id, err)
	}
	return r, nil
}

// UpdateRun 覆盖写入状态、关联消息、错误、用量与时间戳（SessionID / AgentID 不可变）。
func (s *Store) UpdateRun(ctx context.Context, r *model.Run) error {
	if err := requireID("run", r.ID); err != nil {
		return err
	}
	return s.write(ctx, func(q dbtx) error {
		res, err := q.ExecContext(ctx, `UPDATE runs SET
			status = ?, trigger = ?, input_message_id = ?, output_message_id = ?, error = ?,
			input_tokens = ?, output_tokens = ?, cache_read_tokens = ?, started_at = ?, ended_at = ?
			WHERE id = ?`,
			string(r.Status), string(r.Trigger), r.InputMessageID, r.OutputMessageID, r.Error,
			r.Usage.InputTokens, r.Usage.OutputTokens, r.Usage.CacheReadTokens,
			fmtTimePtr(r.StartedAt), fmtTimePtr(r.EndedAt), r.ID)
		return affected(ctx, "update run", "run", r.ID, res, err)
	})
}

// ListRuns 分页返回某会话的运行，CreatedAt 升序。
func (s *Store) ListRuns(ctx context.Context, sessionID string, p model.Page) ([]*model.Run, error) {
	limit, offset := pageOf(p)
	rows, err := s.rq().QueryContext(ctx,
		`SELECT `+runCols+` FROM runs WHERE session_id = ? ORDER BY created_at, id LIMIT ? OFFSET ?`,
		sessionID, limit, offset)
	if err != nil {
		return nil, wrapErr(ctx, "list runs", err)
	}
	out, err := collect(rows, scanRun)
	if err != nil {
		return nil, wrapErr(ctx, "list runs", err)
	}
	return out, nil
}

// ListActiveRuns 返回全部非终态运行（queued / running / awaiting_approval），CreatedAt 升序。
func (s *Store) ListActiveRuns(ctx context.Context) ([]*model.Run, error) {
	rows, err := s.rq().QueryContext(ctx,
		`SELECT `+runCols+` FROM runs WHERE status IN (?, ?, ?) ORDER BY created_at, id`,
		string(model.RunQueued), string(model.RunRunning), string(model.RunAwaitingApproval))
	if err != nil {
		return nil, wrapErr(ctx, "list active runs", err)
	}
	out, err := collect(rows, scanRun)
	if err != nil {
		return nil, wrapErr(ctx, "list active runs", err)
	}
	return out, nil
}

func scanRun(sc scanner) (*model.Run, error) {
	var (
		r         model.Run
		status    string
		trigger   string
		inputID   sql.NullString
		outputID  sql.NullString
		errText   sql.NullString
		created   dbTime
		startedAt dbNullTime
		endedAt   dbNullTime
	)
	if err := sc.Scan(&r.ID, &r.SessionID, &r.AgentID, &status, &trigger, &inputID, &outputID,
		&errText, &r.Usage.InputTokens, &r.Usage.OutputTokens, &r.Usage.CacheReadTokens,
		&created, &startedAt, &endedAt); err != nil {
		return nil, err
	}
	r.Status = model.RunStatus(status)
	r.Trigger = model.RunTrigger(trigger)
	r.InputMessageID = inputID.String
	r.OutputMessageID = outputID.String
	r.Error = errText.String
	r.CreatedAt = created.t
	r.StartedAt = startedAt.t
	r.EndedAt = endedAt.t
	return &r, nil
}
