package store

import (
	"context"
	"database/sql"
	"embed"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// migrationFS 嵌入 migrations/ 下的全部 SQL 文件。文件名格式为 NNN_name.sql，
// NNN 从 001 起连续递增；已发布的迁移文件不得修改，只能追加新版本。
//
//go:embed migrations/*.sql
var migrationFS embed.FS

var migrationNameRe = regexp.MustCompile(`^(\d{3})_([a-z0-9_]+)\.sql$`)

// migration 是一份已解析的迁移脚本。
type migration struct {
	version int
	name    string
	sql     string
}

// loadMigrations 读取并校验嵌入的迁移：版本号唯一、从 1 起连续。
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, model.Wrap(model.CodeInternal, "store: 读取迁移目录失败", err)
	}
	var out []migration
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		v, name, ok := parseMigrationName(e.Name())
		if !ok {
			return nil, model.Errorf(model.CodeInternal, "store: 迁移文件名不合法: %s", e.Name())
		}
		body, err := fs.ReadFile(migrationFS, "migrations/"+e.Name())
		if err != nil {
			return nil, model.Wrap(model.CodeInternal, "store: 读取迁移文件失败", err)
		}
		out = append(out, migration{version: v, name: name, sql: string(body)})
	}
	return validateMigrations(out)
}

// parseMigrationName 解析 "NNN_name.sql"。
func parseMigrationName(filename string) (version int, name string, ok bool) {
	m := migrationNameRe.FindStringSubmatch(filename)
	if m == nil {
		return 0, "", false
	}
	v, err := strconv.Atoi(m[1])
	if err != nil || v < 1 {
		return 0, "", false
	}
	return v, m[2], true
}

// validateMigrations 排序并检查版本号连续、不重复；重复版本直接拒绝启动。
func validateMigrations(ms []migration) ([]migration, error) {
	sort.Slice(ms, func(i, j int) bool { return ms[i].version < ms[j].version })
	for i, m := range ms {
		want := i + 1
		if m.version != want {
			if i > 0 && m.version == ms[i-1].version {
				return nil, model.Errorf(model.CodeInternal,
					"store: 迁移版本重复: %03d (%s 与 %s)", m.version, ms[i-1].name, m.name)
			}
			return nil, model.Errorf(model.CodeInternal,
				"store: 迁移版本不连续: 期望 %03d，得到 %03d_%s", want, m.version, m.name)
		}
	}
	return ms, nil
}

// migrate 创建 schema_migrations 并按序应用未执行的迁移；每个版本一个事务。
// 已应用版本的名称若与嵌入文件不一致，说明数据库来自另一条代码线，拒绝继续。
func (s *Store) migrate(ctx context.Context) error {
	ms, err := loadMigrations()
	if err != nil {
		return err
	}
	if len(ms) == 0 {
		return model.NewError(model.CodeInternal, "store: 没有可用的迁移文件")
	}
	latest := ms[len(ms)-1].version

	if _, err := s.writer.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
		  version    INTEGER PRIMARY KEY,
		  name       TEXT NOT NULL,
		  applied_at TEXT NOT NULL
		)`); err != nil {
		return wrapErr(ctx, "create schema_migrations", err)
	}

	applied, err := s.appliedMigrations(ctx)
	if err != nil {
		return err
	}
	current := 0
	for v, name := range applied {
		if v < 1 {
			return &model.Error{
				Code:    model.CodeConflict,
				Message: "store: 数据库中的迁移记录不合法",
				Details: map[string]any{"version": v},
			}
		}
		if v > current {
			current = v
		}
		if v <= latest && ms[v-1].name != name {
			return &model.Error{
				Code:    model.CodeConflict,
				Message: "store: 数据库中的迁移记录与本程序不一致",
				Details: map[string]any{"version": v, "db_name": name, "expected_name": ms[v-1].name},
			}
		}
	}
	if current > latest {
		return &model.Error{
			Code:    model.CodeConflict,
			Message: "store: 数据库模式版本高于本程序支持的版本，禁止降级",
			Details: map[string]any{"db_version": current, "supported_version": latest},
		}
	}
	if current > 0 && current < latest {
		if err := s.backupBeforeMigration(ctx, current, latest); err != nil {
			return err
		}
	}

	for _, m := range ms[current:] {
		err := s.write(ctx, func(q dbtx) error {
			if _, err := q.ExecContext(ctx, m.sql); err != nil {
				return wrapErr(ctx, "apply migration "+m.name, err)
			}
			_, err := q.ExecContext(ctx,
				`INSERT INTO schema_migrations(version, name, applied_at) VALUES (?, ?, ?)`,
				m.version, m.name, fmtTime(time.Now()))
			return wrapErr(ctx, "record migration "+m.name, err)
		})
		if err != nil {
			return err
		}
		s.log.InfoContext(ctx, "store: 已应用迁移", "version", m.version, "name", m.name)
	}
	return nil
}

// appliedMigrations 返回 version → name。
func (s *Store) appliedMigrations(ctx context.Context) (map[int]string, error) {
	rows, err := s.writer.QueryContext(ctx, `SELECT version, name FROM schema_migrations`)
	if err != nil {
		return nil, wrapErr(ctx, "read schema_migrations", err)
	}
	defer rows.Close()
	out := map[int]string{}
	for rows.Next() {
		var v int
		var name sql.NullString
		if err := rows.Scan(&v, &name); err != nil {
			return nil, wrapErr(ctx, "scan schema_migrations", err)
		}
		out[v] = name.String
	}
	if err := rows.Err(); err != nil {
		return nil, wrapErr(ctx, "iterate schema_migrations", err)
	}
	return out, nil
}
