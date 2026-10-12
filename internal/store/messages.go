package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

const messageCols = `id, session_id, seq, role, run_id, parts, meta, created_at`

// AppendMessage 在写事务内分配会话内递增的 Seq、插入消息并刷新会话的 LastMessageAt。
//
// 为方便直接使用 model.TextMessage，ID 为空时生成，CreatedAt 为零值时取当前时间（UTC）；
// 成功后回填到 m。Seq 由 MAX(seq)+1 得到：写事务是 BEGIN IMMEDIATE 且进程内只有
// 一条写连接，因此不存在两个写者同时读到相同 MAX 的窗口；UNIQUE(session_id, seq)
// 作为最后一道防线（跨进程写入时触发 CodeConflict）。
func (s *Store) AppendMessage(ctx context.Context, m *model.Message) error {
	if m.SessionID == "" {
		return model.NewError(model.CodeInvalid, "store: message.SessionID 不能为空")
	}
	if m.ID == "" {
		m.ID = model.NewID()
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	parts := m.Parts
	if parts == nil {
		parts = []model.Part{}
	}
	partsArg, err := marshalArg("message.parts", parts)
	if err != nil {
		return err
	}
	metaArg, err := jsonArg("message.meta", m.Meta)
	if err != nil {
		return err
	}
	return s.write(ctx, func(q dbtx) error {
		// 先刷新会话：0 行即会话不存在，直接给出精确的 NotFound，而不是等外键报 Conflict。
		res, err := q.ExecContext(ctx,
			`UPDATE sessions SET last_message_at = ? WHERE id = ?`, fmtTime(m.CreatedAt), m.SessionID)
		if err := affected(ctx, "touch session", "session", m.SessionID, res, err); err != nil {
			return err
		}
		var seq int64
		err = q.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(seq), 0) + 1 FROM messages WHERE session_id = ?`, m.SessionID).Scan(&seq)
		if err != nil {
			return wrapErr(ctx, "allocate message seq", err)
		}
		if _, err := q.ExecContext(ctx, `INSERT INTO messages(`+messageCols+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			m.ID, m.SessionID, seq, string(m.Role), m.RunID, partsArg, metaArg, fmtTime(m.CreatedAt)); err != nil {
			return wrapErr(ctx, "append message", err)
		}
		m.Seq = seq
		return nil
	})
}

// GetMessage 按 ID 读取消息。
func (s *Store) GetMessage(ctx context.Context, id string) (*model.Message, error) {
	row := s.rq().QueryRowContext(ctx, `SELECT `+messageCols+` FROM messages WHERE id = ?`, id)
	m, err := scanMessage(row)
	if err != nil {
		return nil, notFoundOr(ctx, "get message", "message", id, err)
	}
	return m, nil
}

// ListMessages 返回 Seq 大于 afterSeq 的消息，按 Seq 升序；limit <= 0 时用默认页大小。
func (s *Store) ListMessages(ctx context.Context, sessionID string, afterSeq int64, limit int) ([]*model.Message, error) {
	if limit <= 0 {
		limit = defaultPageLimit
	}
	rows, err := s.rq().QueryContext(ctx,
		`SELECT `+messageCols+` FROM messages WHERE session_id = ? AND seq > ? ORDER BY seq LIMIT ?`,
		sessionID, afterSeq, limit)
	if err != nil {
		return nil, wrapErr(ctx, "list messages", err)
	}
	out, err := collect(rows, scanMessage)
	if err != nil {
		return nil, wrapErr(ctx, "list messages", err)
	}
	return out, nil
}

func scanMessage(sc scanner) (*model.Message, error) {
	var (
		m       model.Message
		role    string
		runID   sql.NullString
		parts   dbJSON
		meta    dbJSON
		created dbTime
	)
	if err := sc.Scan(&m.ID, &m.SessionID, &m.Seq, &role, &runID, &parts, &meta, &created); err != nil {
		return nil, err
	}
	m.Role = model.Role(role)
	m.RunID = runID.String
	if err := unmarshalJSON("message.parts", parts.raw, &m.Parts); err != nil {
		return nil, err
	}
	if len(m.Parts) == 0 {
		m.Parts = nil
	}
	m.Meta = meta.raw
	m.CreatedAt = created.t
	return &m, nil
}
