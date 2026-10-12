package store

import (
	"context"
	"database/sql"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

const agentCols = `id, name, description, system_prompt, provider_id, model, tools, extensions,
	autonomy, workspace_dir, template, enabled, created_at, updated_at`

// CreateAgent 插入智能体；ID 重复返回 CodeConflict。
func (s *Store) CreateAgent(ctx context.Context, a *model.Agent) error {
	if err := requireID("agent", a.ID); err != nil {
		return err
	}
	tools, err := stringsArg("agent.tools", a.Tools)
	if err != nil {
		return err
	}
	exts, err := stringsArg("agent.extensions", a.Extensions)
	if err != nil {
		return err
	}
	return s.write(ctx, func(q dbtx) error {
		_, err := q.ExecContext(ctx, `INSERT INTO agents(`+agentCols+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			a.ID, a.Name, a.Description, a.SystemPrompt, a.Model.ProviderID, a.Model.Model,
			tools, exts, string(a.Autonomy), a.WorkspaceDir, a.Template, boolInt(a.Enabled),
			fmtTime(a.CreatedAt), fmtTime(a.UpdatedAt))
		return wrapErr(ctx, "create agent", err)
	})
}

// GetAgent 按 ID 读取智能体。
func (s *Store) GetAgent(ctx context.Context, id string) (*model.Agent, error) {
	row := s.rq().QueryRowContext(ctx, `SELECT `+agentCols+` FROM agents WHERE id = ?`, id)
	a, err := scanAgent(row)
	if err != nil {
		return nil, notFoundOr(ctx, "get agent", "agent", id, err)
	}
	return a, nil
}

// ListAgents 按创建时间升序返回全部智能体。
func (s *Store) ListAgents(ctx context.Context) ([]*model.Agent, error) {
	rows, err := s.rq().QueryContext(ctx, `SELECT `+agentCols+` FROM agents ORDER BY created_at, id`)
	if err != nil {
		return nil, wrapErr(ctx, "list agents", err)
	}
	out, err := collect(rows, scanAgent)
	if err != nil {
		return nil, wrapErr(ctx, "list agents", err)
	}
	return out, nil
}

// UpdateAgent 覆盖写入全部可变字段；不存在返回 CodeNotFound。
func (s *Store) UpdateAgent(ctx context.Context, a *model.Agent) error {
	if err := requireID("agent", a.ID); err != nil {
		return err
	}
	tools, err := stringsArg("agent.tools", a.Tools)
	if err != nil {
		return err
	}
	exts, err := stringsArg("agent.extensions", a.Extensions)
	if err != nil {
		return err
	}
	return s.write(ctx, func(q dbtx) error {
		res, err := q.ExecContext(ctx, `UPDATE agents SET
			name = ?, description = ?, system_prompt = ?, provider_id = ?, model = ?,
			tools = ?, extensions = ?, autonomy = ?, workspace_dir = ?, template = ?,
			enabled = ?, updated_at = ?
			WHERE id = ?`,
			a.Name, a.Description, a.SystemPrompt, a.Model.ProviderID, a.Model.Model,
			tools, exts, string(a.Autonomy), a.WorkspaceDir, a.Template,
			boolInt(a.Enabled), fmtTime(a.UpdatedAt), a.ID)
		return affected(ctx, "update agent", "agent", a.ID, res, err)
	})
}

// DeleteAgent 删除智能体及会话；未完成人工操作阻止删除，终态文件操作与审批保留。
func (s *Store) DeleteAgent(ctx context.Context, id string) error {
	return s.write(ctx, func(q dbtx) error {
		if err := preventActiveOperationDeletion(ctx, q, "agent_id", id); err != nil {
			return err
		}
		res, err := q.ExecContext(ctx, `DELETE FROM agents WHERE id = ?`, id)
		return affected(ctx, "delete agent", "agent", id, res, err)
	})
}

func scanAgent(sc scanner) (*model.Agent, error) {
	var (
		a          model.Agent
		tools      dbStrings
		exts       dbStrings
		autonomy   string
		enabled    dbBool
		created    dbTime
		updated    dbTime
		desc       sql.NullString
		sysPrompt  sql.NullString
		providerID sql.NullString
		modelName  sql.NullString
		template   sql.NullString
	)
	if err := sc.Scan(&a.ID, &a.Name, &desc, &sysPrompt, &providerID, &modelName, &tools, &exts,
		&autonomy, &a.WorkspaceDir, &template, &enabled, &created, &updated); err != nil {
		return nil, err
	}
	a.Description = desc.String
	a.SystemPrompt = sysPrompt.String
	a.Model = model.ModelRef{ProviderID: providerID.String, Model: modelName.String}
	a.Tools = tools.v
	a.Extensions = exts.v
	a.Autonomy = model.AutonomyLevel(autonomy)
	a.Template = template.String
	a.Enabled = enabled.v
	a.CreatedAt = created.t
	a.UpdatedAt = updated.t
	return &a, nil
}
