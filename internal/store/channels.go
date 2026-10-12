package store

import (
	"context"
	"database/sql"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

const channelCols = `id, agent_id, kind, name, secret_ref, config, enabled, last_error, created_at, updated_at`

// CreateChannel 插入 IM 通道；只保存 SecretRef 与非敏感 Config。AgentID 不存在时返回 CodeConflict。
func (s *Store) CreateChannel(ctx context.Context, c *model.Channel) error {
	if err := requireID("channel", c.ID); err != nil {
		return err
	}
	config, err := jsonArg("channel.config", c.Config)
	if err != nil {
		return err
	}
	return s.write(ctx, func(q dbtx) error {
		_, err := q.ExecContext(ctx, `INSERT INTO channels(`+channelCols+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.ID, c.AgentID, string(c.Kind), c.Name, c.SecretRef, config, boolInt(c.Enabled),
			c.LastError, fmtTime(c.CreatedAt), fmtTime(c.UpdatedAt))
		return wrapErr(ctx, "create channel", err)
	})
}

// GetChannel 按 ID 读取通道。
func (s *Store) GetChannel(ctx context.Context, id string) (*model.Channel, error) {
	row := s.rq().QueryRowContext(ctx, `SELECT `+channelCols+` FROM channels WHERE id = ?`, id)
	c, err := scanChannel(row)
	if err != nil {
		return nil, notFoundOr(ctx, "get channel", "channel", id, err)
	}
	return c, nil
}

// ListChannels 返回某智能体的通道（agentID 为空表示全部），CreatedAt 升序。
func (s *Store) ListChannels(ctx context.Context, agentID string) ([]*model.Channel, error) {
	query := `SELECT ` + channelCols + ` FROM channels`
	args := []any{}
	if agentID != "" {
		query += " WHERE agent_id = ?"
		args = append(args, agentID)
	}
	query += " ORDER BY created_at, id"
	rows, err := s.rq().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapErr(ctx, "list channels", err)
	}
	out, err := collect(rows, scanChannel)
	if err != nil {
		return nil, wrapErr(ctx, "list channels", err)
	}
	return out, nil
}

// UpdateChannel 覆盖写入全部可变字段（AgentID 不可变）。
func (s *Store) UpdateChannel(ctx context.Context, c *model.Channel) error {
	if err := requireID("channel", c.ID); err != nil {
		return err
	}
	config, err := jsonArg("channel.config", c.Config)
	if err != nil {
		return err
	}
	return s.write(ctx, func(q dbtx) error {
		res, err := q.ExecContext(ctx, `UPDATE channels SET
			kind = ?, name = ?, secret_ref = ?, config = ?, enabled = ?, last_error = ?, updated_at = ?
			WHERE id = ?`,
			string(c.Kind), c.Name, c.SecretRef, config, boolInt(c.Enabled), c.LastError,
			fmtTime(c.UpdatedAt), c.ID)
		return affected(ctx, "update channel", "channel", c.ID, res, err)
	})
}

// DeleteChannel 删除通道记录（凭据由 secret 存储单独清理）。
func (s *Store) DeleteChannel(ctx context.Context, id string) error {
	return s.write(ctx, func(q dbtx) error {
		res, err := q.ExecContext(ctx, `DELETE FROM channels WHERE id = ?`, id)
		return affected(ctx, "delete channel", "channel", id, res, err)
	})
}

func scanChannel(sc scanner) (*model.Channel, error) {
	var (
		c         model.Channel
		kind      string
		secretRef sql.NullString
		config    dbJSON
		enabled   dbBool
		lastError sql.NullString
		created   dbTime
		updated   dbTime
	)
	if err := sc.Scan(&c.ID, &c.AgentID, &kind, &c.Name, &secretRef, &config, &enabled, &lastError,
		&created, &updated); err != nil {
		return nil, err
	}
	c.Kind = model.ChannelKind(kind)
	c.SecretRef = secretRef.String
	c.Config = config.raw
	c.Enabled = enabled.v
	c.LastError = lastError.String
	c.CreatedAt = created.t
	c.UpdatedAt = updated.t
	return &c, nil
}
