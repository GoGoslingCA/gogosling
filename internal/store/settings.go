package store

import (
	"context"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// GetSetting 读取设置项的 JSON 文本；不存在返回 CodeNotFound。
func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	if key == "" {
		return "", model.NewError(model.CodeInvalid, "store: setting key 不能为空")
	}
	var value string
	err := s.rq().QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if err != nil {
		return "", notFoundOr(ctx, "get setting", "setting", key, err)
	}
	return value, nil
}

// SetSetting 写入或覆盖设置项（upsert）。value 应是 JSON 文本，仓储不校验其结构。
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	if key == "" {
		return model.NewError(model.CodeInvalid, "store: setting key 不能为空")
	}
	return s.write(ctx, func(q dbtx) error {
		_, err := q.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
		return wrapErr(ctx, "set setting", err)
	})
}
