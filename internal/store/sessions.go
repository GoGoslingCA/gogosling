package store

import (
	"context"
	"database/sql"
	"strings"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

const sessionCols = `id, agent_id, title, origin, key, status, created_at, updated_at, last_message_at, project_id`

// CreateSession 插入会话。同一 Key 已有 active 会话时返回 CodeConflict（部分唯一索引）；
// AgentID 不存在时返回 CodeConflict（外键）。
func (s *Store) CreateSession(ctx context.Context, sess *model.Session) error {
	if err := requireID("session", sess.ID); err != nil {
		return err
	}
	return s.write(ctx, func(q dbtx) error {
		_, err := q.ExecContext(ctx, `INSERT INTO sessions(`+sessionCols+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			sess.ID, sess.AgentID, sess.Title, string(sess.Origin), sess.Key, string(sess.Status),
			fmtTime(sess.CreatedAt), fmtTime(sess.UpdatedAt), fmtTimePtr(sess.LastMessageAt), nullableString(sess.ProjectID))
		return wrapErr(ctx, "create session", err)
	})
}

// GetSession 按 ID 读取会话。
func (s *Store) GetSession(ctx context.Context, id string) (*model.Session, error) {
	row := s.rq().QueryRowContext(ctx, `SELECT `+sessionCols+` FROM sessions WHERE id = ?`, id)
	sess, err := scanSession(row)
	if err != nil {
		return nil, notFoundOr(ctx, "get session", "session", id, err)
	}
	return sess, nil
}

// GetActiveSessionByKey 返回拥有该路由键的 active 会话。
func (s *Store) GetActiveSessionByKey(ctx context.Context, key string) (*model.Session, error) {
	row := s.rq().QueryRowContext(ctx,
		`SELECT `+sessionCols+` FROM sessions WHERE key = ? AND status = ?`, key, string(model.SessionActive))
	sess, err := scanSession(row)
	if err != nil {
		return nil, notFoundOr(ctx, "get active session by key", "session", key, err)
	}
	return sess, nil
}

// ListSessions 按过滤条件分页返回会话，CreatedAt 升序。
func (s *Store) ListSessions(ctx context.Context, f model.SessionFilter, p model.Page) ([]*model.Session, error) {
	return s.listSessions(ctx, f, p, false)
}

// ListRecentSessions 按最后消息时间（空时取创建时间）降序分页；同时间按创建时间、ID 降序。
// 修改标题、归档和恢复会话不会改变对话活动顺序。
func (s *Store) ListRecentSessions(ctx context.Context, f model.SessionFilter, p model.Page) ([]*model.Session, error) {
	return s.listSessions(ctx, f, p, true)
}

func (s *Store) listSessions(ctx context.Context, f model.SessionFilter, p model.Page, recent bool) ([]*model.Session, error) {
	var (
		where []string
		args  []any
	)
	if f.AgentID != "" {
		where = append(where, "agent_id = ?")
		args = append(args, f.AgentID)
	}
	if f.ProjectID != "" {
		where = append(where, "project_id = ?")
		args = append(args, f.ProjectID)
	}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, string(f.Status))
	}
	if f.Origin != "" {
		where = append(where, "origin = ?")
		args = append(args, string(f.Origin))
	}
	query := `SELECT ` + sessionCols + ` FROM sessions`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	limit, offset := pageOf(p)
	if recent {
		query += " ORDER BY COALESCE(last_message_at, created_at) DESC, created_at DESC, id DESC"
	} else {
		query += " ORDER BY created_at, id"
	}
	query += " LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := s.rq().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapErr(ctx, "list sessions", err)
	}
	out, err := collect(rows, scanSession)
	if err != nil {
		return nil, wrapErr(ctx, "list sessions", err)
	}
	return out, nil
}

// UpdateSession 覆盖写入全部可变字段（AgentID 不可变）。
func (s *Store) UpdateSession(ctx context.Context, sess *model.Session) error {
	if err := requireID("session", sess.ID); err != nil {
		return err
	}
	return s.write(ctx, func(q dbtx) error {
		res, err := q.ExecContext(ctx, `UPDATE sessions SET
			title = ?, origin = ?, key = ?, status = ?, updated_at = ?, last_message_at = ?
			WHERE id = ?`,
			sess.Title, string(sess.Origin), sess.Key, string(sess.Status),
			fmtTime(sess.UpdatedAt), fmtTimePtr(sess.LastMessageAt), sess.ID)
		return affected(ctx, "update session", "session", sess.ID, res, err)
	})
}

// DeleteSession 删除会话与模型运行记录；未结束的人工操作会阻止删除，终态操作保留归属墓碑。
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	return s.write(ctx, func(q dbtx) error {
		if err := preventActiveOperationDeletion(ctx, q, "session_id", id); err != nil {
			return err
		}
		res, err := q.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
		return affected(ctx, "delete session", "session", id, res, err)
	})
}

func scanSession(sc scanner) (*model.Session, error) {
	var (
		sess      model.Session
		title     sql.NullString
		origin    string
		status    string
		created   dbTime
		updated   dbTime
		lastMsg   dbNullTime
		projectID sql.NullString
	)
	if err := sc.Scan(&sess.ID, &sess.AgentID, &title, &origin, &sess.Key, &status,
		&created, &updated, &lastMsg, &projectID); err != nil {
		return nil, err
	}
	sess.Title = title.String
	sess.Origin = model.Origin(origin)
	sess.Status = model.SessionStatus(status)
	sess.CreatedAt = created.t
	sess.UpdatedAt = updated.t
	sess.LastMessageAt = lastMsg.t
	sess.ProjectID = projectID.String
	return &sess, nil
}
