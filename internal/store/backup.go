package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// backupBeforeMigration 在业务连接开放前用 SQLite 一致快照备份旧模式，验证后才允许迁移。
func (s *Store) backupBeforeMigration(ctx context.Context, current, next int) error {
	dir := filepath.Join(filepath.Dir(s.path), "backups")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return model.Wrap(model.CodeInternal, "store: 创建迁移备份目录失败", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(dir, 0o700); err != nil {
			return model.Wrap(model.CodeInternal, "store: 设置备份目录权限失败", err)
		}
	}
	f, err := os.CreateTemp(dir, fmt.Sprintf("before-v%d-to-v%d-*.db", current, next))
	if err != nil {
		return model.Wrap(model.CodeInternal, "store: 创建迁移备份失败", err)
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		return model.Wrap(model.CodeInternal, "store: 关闭备份占位失败", err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.Remove(name)
			_ = os.Remove(name + ".json")
		}
	}()
	if _, err := s.writer.ExecContext(ctx, `VACUUM main INTO ?`, name); err != nil {
		return wrapErr(ctx, "backup before migration", err)
	}
	backup, err := sql.Open(driverName, name)
	if err != nil {
		return model.Wrap(model.CodeInternal, "store: 打开备份验证失败", err)
	}
	var integrity string
	err = backup.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity)
	var version int
	if err == nil {
		err = backup.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&version)
	}
	closeErr := backup.Close()
	if err != nil {
		return wrapErr(ctx, "verify migration backup", err)
	}
	if closeErr != nil {
		return model.Wrap(model.CodeInternal, "store: 关闭备份验证失败", closeErr)
	}
	if integrity != "ok" || version != current {
		return model.NewError(model.CodeInternal, "store: 迁移备份校验未通过")
	}
	file, err := os.OpenFile(name, os.O_RDWR, 0)
	if err != nil {
		return model.Wrap(model.CodeInternal, "store: 读取备份摘要失败", err)
	}
	hash := sha256.New()
	_, err = io.Copy(hash, file)
	if err == nil {
		err = file.Sync()
	}
	closeErr = file.Close()
	if err != nil {
		return model.Wrap(model.CodeInternal, "store: 同步迁移备份失败", err)
	}
	if closeErr != nil {
		return model.Wrap(model.CodeInternal, "store: 关闭迁移备份失败", closeErr)
	}
	manifest, err := json.Marshal(struct {
		SchemaVersion int    `json:"schema_version"`
		TargetVersion int    `json:"target_version"`
		File          string `json:"file"`
		SHA256        string `json:"sha256"`
		CreatedAt     string `json:"created_at"`
	}{current, next, filepath.Base(name), fmt.Sprintf("%x", hash.Sum(nil)), time.Now().UTC().Format(time.RFC3339Nano)})
	if err != nil {
		return model.Wrap(model.CodeInternal, "store: 编码备份清单失败", err)
	}
	if err := os.WriteFile(name+".json", manifest, 0o600); err != nil {
		return model.Wrap(model.CodeInternal, "store: 写入备份清单失败", err)
	}
	complete = true
	s.log.InfoContext(ctx, "store: 已验证迁移前备份", "schema_version", current, "target_version", next, "file", filepath.Base(name))
	return nil
}
