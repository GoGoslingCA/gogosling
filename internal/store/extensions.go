package store

import (
	"context"
	"database/sql"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

const extensionCols = `id, kind, name, version, source, install_dir, manifest, mcp, enabled, digest,
	installed_at, updated_at`

// CreateExtension 插入已安装扩展。MCP 配置中的 Env 值只允许 "secret:<ref>" 形式的引用，
// 这一点由 extension 包在写入前保证，仓储不做内容检查。
func (s *Store) CreateExtension(ctx context.Context, e *model.Extension) error {
	if err := requireID("extension", e.ID); err != nil {
		return err
	}
	manifest, err := jsonArg("extension.manifest", e.Manifest)
	if err != nil {
		return err
	}
	mcp, err := mcpArg(e.MCP)
	if err != nil {
		return err
	}
	return s.write(ctx, func(q dbtx) error {
		_, err := q.ExecContext(ctx, `INSERT INTO extensions(`+extensionCols+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			e.ID, string(e.Kind), e.Name, e.Version, e.Source, e.InstallDir, manifest, mcp,
			boolInt(e.Enabled), e.Digest, fmtTime(e.InstalledAt), fmtTime(e.UpdatedAt))
		return wrapErr(ctx, "create extension", err)
	})
}

// GetExtension 按 ID 读取扩展。
func (s *Store) GetExtension(ctx context.Context, id string) (*model.Extension, error) {
	row := s.rq().QueryRowContext(ctx, `SELECT `+extensionCols+` FROM extensions WHERE id = ?`, id)
	e, err := scanExtension(row)
	if err != nil {
		return nil, notFoundOr(ctx, "get extension", "extension", id, err)
	}
	return e, nil
}

// ListExtensions 按类型返回扩展（kind 为空表示全部），InstalledAt 升序。
func (s *Store) ListExtensions(ctx context.Context, kind model.ExtensionKind) ([]*model.Extension, error) {
	query := `SELECT ` + extensionCols + ` FROM extensions`
	args := []any{}
	if kind != "" {
		query += " WHERE kind = ?"
		args = append(args, string(kind))
	}
	query += " ORDER BY installed_at, id"
	rows, err := s.rq().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapErr(ctx, "list extensions", err)
	}
	out, err := collect(rows, scanExtension)
	if err != nil {
		return nil, wrapErr(ctx, "list extensions", err)
	}
	return out, nil
}

// UpdateExtension 覆盖写入全部可变字段。
func (s *Store) UpdateExtension(ctx context.Context, e *model.Extension) error {
	if err := requireID("extension", e.ID); err != nil {
		return err
	}
	manifest, err := jsonArg("extension.manifest", e.Manifest)
	if err != nil {
		return err
	}
	mcp, err := mcpArg(e.MCP)
	if err != nil {
		return err
	}
	return s.write(ctx, func(q dbtx) error {
		res, err := q.ExecContext(ctx, `UPDATE extensions SET
			kind = ?, name = ?, version = ?, source = ?, install_dir = ?, manifest = ?, mcp = ?,
			enabled = ?, digest = ?, updated_at = ?
			WHERE id = ?`,
			string(e.Kind), e.Name, e.Version, e.Source, e.InstallDir, manifest, mcp,
			boolInt(e.Enabled), e.Digest, fmtTime(e.UpdatedAt), e.ID)
		return affected(ctx, "update extension", "extension", e.ID, res, err)
	})
}

// DeleteExtension 删除扩展记录（不触碰 InstallDir 下的文件，由 extension 包负责）。
func (s *Store) DeleteExtension(ctx context.Context, id string) error {
	return s.write(ctx, func(q dbtx) error {
		res, err := q.ExecContext(ctx, `DELETE FROM extensions WHERE id = ?`, id)
		return affected(ctx, "delete extension", "extension", id, res, err)
	})
}

func mcpArg(cfg *model.MCPServerConfig) (any, error) {
	if cfg == nil {
		return nil, nil
	}
	return marshalArg("extension.mcp", cfg)
}

func scanExtension(sc scanner) (*model.Extension, error) {
	var (
		e          model.Extension
		kind       string
		version    sql.NullString
		source     sql.NullString
		installDir sql.NullString
		manifest   dbJSON
		mcp        dbJSON
		enabled    dbBool
		digest     sql.NullString
		installed  dbTime
		updated    dbTime
	)
	if err := sc.Scan(&e.ID, &kind, &e.Name, &version, &source, &installDir, &manifest, &mcp,
		&enabled, &digest, &installed, &updated); err != nil {
		return nil, err
	}
	e.Kind = model.ExtensionKind(kind)
	e.Version = version.String
	e.Source = source.String
	e.InstallDir = installDir.String
	e.Manifest = manifest.raw
	if len(mcp.raw) > 0 {
		var cfg model.MCPServerConfig
		if err := unmarshalJSON("extension.mcp", mcp.raw, &cfg); err != nil {
			return nil, err
		}
		e.MCP = &cfg
	}
	e.Enabled = enabled.v
	e.Digest = digest.String
	e.InstalledAt = installed.t
	e.UpdatedAt = updated.t
	return &e, nil
}
