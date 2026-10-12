package store

import (
	"context"
	"database/sql"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

const providerCols = `id, name, kind, base_url, secret_ref, default_model, models, enabled,
	created_at, updated_at`

// CreateProvider 插入供应商配置。只保存 SecretRef，不保存任何密钥。
func (s *Store) CreateProvider(ctx context.Context, p *model.Provider) error {
	if err := requireID("provider", p.ID); err != nil {
		return err
	}
	models, err := stringsArg("provider.models", p.Models)
	if err != nil {
		return err
	}
	return s.write(ctx, func(q dbtx) error {
		_, err := q.ExecContext(ctx, `INSERT INTO providers(`+providerCols+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			p.ID, p.Name, string(p.Kind), p.BaseURL, p.SecretRef, p.DefaultModel, models,
			boolInt(p.Enabled), fmtTime(p.CreatedAt), fmtTime(p.UpdatedAt))
		return wrapErr(ctx, "create provider", err)
	})
}

// GetProvider 按 ID 读取供应商。
func (s *Store) GetProvider(ctx context.Context, id string) (*model.Provider, error) {
	row := s.rq().QueryRowContext(ctx, `SELECT `+providerCols+` FROM providers WHERE id = ?`, id)
	p, err := scanProvider(row)
	if err != nil {
		return nil, notFoundOr(ctx, "get provider", "provider", id, err)
	}
	return p, nil
}

// ListProviders 按创建时间升序返回全部供应商。
func (s *Store) ListProviders(ctx context.Context) ([]*model.Provider, error) {
	rows, err := s.rq().QueryContext(ctx, `SELECT `+providerCols+` FROM providers ORDER BY created_at, id`)
	if err != nil {
		return nil, wrapErr(ctx, "list providers", err)
	}
	out, err := collect(rows, scanProvider)
	if err != nil {
		return nil, wrapErr(ctx, "list providers", err)
	}
	return out, nil
}

// UpdateProvider 覆盖写入全部可变字段。
func (s *Store) UpdateProvider(ctx context.Context, p *model.Provider) error {
	if err := requireID("provider", p.ID); err != nil {
		return err
	}
	models, err := stringsArg("provider.models", p.Models)
	if err != nil {
		return err
	}
	return s.write(ctx, func(q dbtx) error {
		res, err := q.ExecContext(ctx, `UPDATE providers SET
			name = ?, kind = ?, base_url = ?, secret_ref = ?, default_model = ?, models = ?,
			enabled = ?, updated_at = ?
			WHERE id = ?`,
			p.Name, string(p.Kind), p.BaseURL, p.SecretRef, p.DefaultModel, models,
			boolInt(p.Enabled), fmtTime(p.UpdatedAt), p.ID)
		return affected(ctx, "update provider", "provider", p.ID, res, err)
	})
}

// DeleteProvider 删除供应商。智能体对供应商的引用是松耦合（无外键），由领域层处理悬挂引用。
func (s *Store) DeleteProvider(ctx context.Context, id string) error {
	return s.write(ctx, func(q dbtx) error {
		res, err := q.ExecContext(ctx, `DELETE FROM providers WHERE id = ?`, id)
		return affected(ctx, "delete provider", "provider", id, res, err)
	})
}

func scanProvider(sc scanner) (*model.Provider, error) {
	var (
		p            model.Provider
		kind         string
		baseURL      sql.NullString
		secretRef    sql.NullString
		defaultModel sql.NullString
		models       dbStrings
		enabled      dbBool
		created      dbTime
		updated      dbTime
	)
	if err := sc.Scan(&p.ID, &p.Name, &kind, &baseURL, &secretRef, &defaultModel, &models,
		&enabled, &created, &updated); err != nil {
		return nil, err
	}
	p.Kind = model.ProviderKind(kind)
	p.BaseURL = baseURL.String
	p.SecretRef = secretRef.String
	p.DefaultModel = defaultModel.String
	p.Models = models.v
	p.Enabled = enabled.v
	p.CreatedAt = created.t
	p.UpdatedAt = updated.t
	return &p, nil
}
