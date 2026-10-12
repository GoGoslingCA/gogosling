package store

import (
	"context"
	"encoding/json"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

// changeRecord 仅用于受保护数据库；公开 ChangeSet JSON 永不携带正文与身份。
type changeRecord struct {
	Change   *model.ChangeSet     `json:"change"`
	Base     model.ChangeSnapshot `json:"base"`
	Proposed string               `json:"proposed"`
	Applied  model.ChangeSnapshot `json:"applied"`
	Result   model.ChangeStatus   `json:"result,omitempty"`
}

func encodeChange(c *model.ChangeSet) ([]byte, error) {
	return json.Marshal(changeRecord{Change: c, Base: c.Base, Proposed: c.Proposed, Applied: c.Applied, Result: c.PendingResult})
}

// CreateChangeSet 保存不可变预览的前像和提议，未来更新只改变执行状态。
func (s *Store) CreateChangeSet(ctx context.Context, c *model.ChangeSet) error {
	if err := requireID("change", c.ID); err != nil {
		return err
	}
	b, err := encodeChange(c)
	if err != nil {
		return err
	}
	return s.write(ctx, func(q dbtx) error {
		_, err := q.ExecContext(ctx, `INSERT INTO changesets(id,owner_id,agent_id,session_id,artifact_id,revision_id,target_path,operation,status,version,operation_id,undo_of,payload,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, c.ID, c.OwnerID, c.AgentID, c.SessionID, c.ArtifactID, c.RevisionID, c.TargetPath, c.Kind, c.Status, c.Version, nullableString(c.OperationID), nullableString(c.UndoOf), string(b), fmtTime(c.CreatedAt))
		return wrapErr(ctx, "create change", err)
	})
}

// GetChangeSet 读取操作及内核前像。
func (s *Store) GetChangeSet(ctx context.Context, id string) (*model.ChangeSet, error) {
	var b string
	if err := s.rq().QueryRowContext(ctx, `SELECT payload FROM changesets WHERE id=?`, id).Scan(&b); err != nil {
		return nil, notFoundOr(ctx, "get change", "change", id, err)
	}
	return decodeChange(b)
}
func decodeChange(b string) (*model.ChangeSet, error) {
	var r changeRecord
	if err := json.Unmarshal([]byte(b), &r); err != nil {
		return nil, model.Wrap(model.CodeInternal, "store: invalid change record", err)
	}
	if r.Change == nil {
		return nil, model.NewError(model.CodeInternal, "store: missing change")
	}
	r.Change.Base = r.Base
	r.Change.Proposed = r.Proposed
	r.Change.Applied = r.Applied
	r.Change.PendingResult = r.Result
	return r.Change, nil
}

// UpdateChangeSet 在同一事务中核对版本且拒绝改变已预览内容。
func (s *Store) UpdateChangeSet(ctx context.Context, c *model.ChangeSet, expected int64) error {
	return s.write(ctx, func(q dbtx) error {
		var raw string
		if err := q.QueryRowContext(ctx, `SELECT payload FROM changesets WHERE id=?`, c.ID).Scan(&raw); err != nil {
			return notFoundOr(ctx, "update change", "change", c.ID, err)
		}
		old, err := decodeChange(raw)
		if err != nil {
			return err
		}
		if old.Version != expected {
			return model.NewError(model.CodeConflict, "store: stale change version")
		}
		if old.OwnerID != c.OwnerID || old.AgentID != c.AgentID || old.SessionID != c.SessionID || old.ArtifactID != c.ArtifactID || old.RevisionID != c.RevisionID || old.TargetPath != c.TargetPath || old.PreviewDigest != c.PreviewDigest || old.Kind != c.Kind || old.Base != c.Base || old.Proposed != c.Proposed || old.UndoOf != c.UndoOf {
			return model.NewError(model.CodeInvalid, "store: immutable change preview")
		}
		c.Version = expected + 1
		b, err := encodeChange(c)
		if err != nil {
			return err
		}
		_, err = q.ExecContext(ctx, `UPDATE changesets SET status=?,version=?,operation_id=?,payload=? WHERE id=? AND version=?`, c.Status, c.Version, nullableString(c.OperationID), string(b), c.ID, expected)
		return wrapErr(ctx, "update change", err)
	})
}

// ListChangeSets 按会话及操作者列出变更，正文不会序列化给客户端。
func (s *Store) ListChangeSets(ctx context.Context, owner, session string, p model.Page) ([]*model.ChangeSet, error) {
	limit, offset := pageOf(p)
	rows, err := s.rq().QueryContext(ctx, `SELECT payload FROM changesets WHERE owner_id=? AND session_id=? ORDER BY created_at DESC,id DESC LIMIT ? OFFSET ?`, owner, session, limit, offset)
	if err != nil {
		return nil, wrapErr(ctx, "list changes", err)
	}
	defer rows.Close()
	out := []*model.ChangeSet{}
	for rows.Next() {
		var b string
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		c, err := decodeChange(b)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ChangeTx 复用现有写连接及事务，等待审批和文件操作必须在回调外执行。
func (s *Store) ChangeTx(ctx context.Context, fn func(model.ChangeStore) error) error {
	return s.Tx(ctx, func(tx model.Store) error { return fn(tx.(*Store)) })
}
