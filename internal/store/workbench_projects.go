package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"path"
	"strings"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

var _ model.WorkbenchStore = (*Store)(nil)

// WorkbenchTx 复用现有写事务，不扩大 model.Store 的接口集合。
func (s *Store) WorkbenchTx(ctx context.Context, fn func(model.WorkbenchStore) error) error {
	if s.tx != nil {
		return model.NewError(model.CodeInternal, "store: 事务不可嵌套")
	}
	return s.write(ctx, func(q dbtx) error { return fn(&Store{path: s.path, log: s.log, tx: q.(*sql.Tx)}) })
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func validDigest(value string) bool {
	b, err := hex.DecodeString(value)
	return err == nil && len(b) == 32 && value == strings.ToLower(value)
}

func staleVersion(version int64) error {
	return &model.Error{Code: model.CodeConflict, Message: "store: 数据已由其他操作更新", Details: map[string]any{"reason": "stale_version", "current_version": version}}
}

const projectCols = `id,agent_id,name,description,status,version,created_at,updated_at`

// CreateProject 创建初始版本为 1 的项目。
func (s *Store) CreateProject(ctx context.Context, p *model.Project) error {
	if err := requireID("project", p.ID); err != nil {
		return err
	}
	p.Version = 1
	return s.write(ctx, func(q dbtx) error {
		_, err := q.ExecContext(ctx, `INSERT INTO projects(`+projectCols+`) VALUES(?,?,?,?,?,?,?,?)`, p.ID, p.AgentID, p.Name, p.Description, p.Status, p.Version, fmtTime(p.CreatedAt), fmtTime(p.UpdatedAt))
		return wrapErr(ctx, "create project", err)
	})
}

// GetProject 读取项目。
func (s *Store) GetProject(ctx context.Context, id string) (*model.Project, error) {
	p, err := scanProject(s.rq().QueryRowContext(ctx, `SELECT `+projectCols+` FROM projects WHERE id=?`, id))
	if err != nil {
		return nil, notFoundOr(ctx, "get project", "project", id, err)
	}
	return p, nil
}

// ListProjects 按创建时间分页读取项目。
func (s *Store) ListProjects(ctx context.Context, f model.ProjectFilter, p model.Page) ([]*model.Project, error) {
	where := []string{"1=1"}
	args := []any{}
	if f.AgentID != "" {
		where = append(where, "agent_id=?")
		args = append(args, f.AgentID)
	}
	if f.Status != "" {
		where = append(where, "status=?")
		args = append(args, f.Status)
	}
	limit, offset := pageOf(p)
	args = append(args, limit, offset)
	rows, err := s.rq().QueryContext(ctx, `SELECT `+projectCols+` FROM projects WHERE `+strings.Join(where, " AND ")+` ORDER BY created_at,id LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, wrapErr(ctx, "list projects", err)
	}
	out, err := collect(rows, scanProject)
	return out, wrapErr(ctx, "list projects", err)
}

// UpdateProject 原子校验版本与不可变 AgentID，成功后递增并回填 Version。
func (s *Store) UpdateProject(ctx context.Context, p *model.Project, expected int64) error {
	return s.write(ctx, func(q dbtx) error {
		current, err := scanProject(q.QueryRowContext(ctx, `SELECT `+projectCols+` FROM projects WHERE id=?`, p.ID))
		if err != nil {
			return notFoundOr(ctx, "update project", "project", p.ID, err)
		}
		if current.AgentID != p.AgentID {
			return model.NewError(model.CodeConflict, "store: 项目智能体不可修改")
		}
		if expected != current.Version {
			return staleVersion(current.Version)
		}
		_, err = q.ExecContext(ctx, `UPDATE projects SET name=?,description=?,status=?,version=version+1,updated_at=? WHERE id=? AND version=?`, p.Name, p.Description, p.Status, fmtTime(p.UpdatedAt), p.ID, expected)
		if err != nil {
			return wrapErr(ctx, "update project", err)
		}
		p.Version = expected + 1
		return nil
	})
}

// SetSessionProject 校验当前归属与同一智能体后修改项目，不触碰会话路由键。
func (s *Store) SetSessionProject(ctx context.Context, id, expected, next string) error {
	return s.write(ctx, func(q dbtx) error {
		var agent string
		var current sql.NullString
		if err := q.QueryRowContext(ctx, `SELECT agent_id,project_id FROM sessions WHERE id=?`, id).Scan(&agent, &current); err != nil {
			return notFoundOr(ctx, "link session", "session", id, err)
		}
		if current.String != expected {
			return &model.Error{Code: model.CodeConflict, Message: "store: 会话归属已变化", Details: map[string]any{"reason": "scope_mismatch", "current_project_id": current.String}}
		}
		if next != "" {
			if err := checkProjectAgent(ctx, q, next, agent); err != nil {
				return err
			}
		}
		_, err := q.ExecContext(ctx, `UPDATE sessions SET project_id=?,updated_at=? WHERE id=?`, nullableString(next), fmtTime(time.Now()), id)
		return wrapErr(ctx, "link session", err)
	})
}

func checkProjectAgent(ctx context.Context, q dbtx, projectID, agentID string) error {
	var actual string
	if err := q.QueryRowContext(ctx, `SELECT agent_id FROM projects WHERE id=?`, projectID).Scan(&actual); err != nil {
		return notFoundOr(ctx, "check project", "project", projectID, err)
	}
	if actual != agentID {
		return &model.Error{Code: model.CodeConflict, Message: "store: 项目与资源必须属于同一智能体", Details: map[string]any{"reason": "scope_mismatch"}}
	}
	return nil
}

func scanProject(sc scanner) (*model.Project, error) {
	var p model.Project
	var created, updated dbTime
	if err := sc.Scan(&p.ID, &p.AgentID, &p.Name, &p.Description, &p.Status, &p.Version, &created, &updated); err != nil {
		return nil, err
	}
	p.CreatedAt = created.t
	p.UpdatedAt = updated.t
	return &p, nil
}

const materialCols = `id,project_id,agent_id,name,mime,size,sha256,attachment,created_at`

func validAttachment(a model.Attachment) bool {
	return a.Size >= 0 && strings.HasPrefix(a.Path, "inbound/") && path.Clean(a.Path) == a.Path && !strings.ContainsAny(a.Path, "\\\x00") && a.Path != "inbound/"
}

// CreateProjectMaterial 记录材料摘要与引用；不复制或覆盖工作区文件。
func (s *Store) CreateProjectMaterial(ctx context.Context, m *model.ProjectMaterial) error {
	if err := requireID("material", m.ID); err != nil {
		return err
	}
	if !validDigest(m.SHA256) || !validAttachment(m.Attachment) || m.Size != m.Attachment.Size || m.Name != m.Attachment.Name || m.MIME != m.Attachment.MIME {
		return model.NewError(model.CodeInvalid, "store: 材料描述或摘要不合法")
	}
	a, err := marshalArg("material.attachment", m.Attachment)
	if err != nil {
		return err
	}
	return s.write(ctx, func(q dbtx) error {
		if err := checkProjectAgent(ctx, q, m.ProjectID, m.AgentID); err != nil {
			return err
		}
		_, err := q.ExecContext(ctx, `INSERT INTO project_materials(`+materialCols+`) VALUES(?,?,?,?,?,?,?,?,?)`, m.ID, m.ProjectID, m.AgentID, m.Name, m.MIME, m.Size, m.SHA256, a, fmtTime(m.CreatedAt))
		return wrapErr(ctx, "create material", err)
	})
}

// GetProjectMaterial 读取材料索引。
func (s *Store) GetProjectMaterial(ctx context.Context, id string) (*model.ProjectMaterial, error) {
	m, err := scanMaterial(s.rq().QueryRowContext(ctx, `SELECT `+materialCols+` FROM project_materials WHERE id=?`, id))
	if err != nil {
		return nil, notFoundOr(ctx, "get material", "material", id, err)
	}
	return m, nil
}

// ListProjectMaterials 按项目分页读取材料索引。
func (s *Store) ListProjectMaterials(ctx context.Context, id string, p model.Page) ([]*model.ProjectMaterial, error) {
	limit, offset := pageOf(p)
	rows, err := s.rq().QueryContext(ctx, `SELECT `+materialCols+` FROM project_materials WHERE project_id=? ORDER BY created_at,id LIMIT ? OFFSET ?`, id, limit, offset)
	if err != nil {
		return nil, wrapErr(ctx, "list materials", err)
	}
	out, err := collect(rows, scanMaterial)
	return out, wrapErr(ctx, "list materials", err)
}

// DeleteProjectMaterial 只移除索引，不删除文件或历史消息。
func (s *Store) DeleteProjectMaterial(ctx context.Context, id string) error {
	return s.write(ctx, func(q dbtx) error {
		res, err := q.ExecContext(ctx, `DELETE FROM project_materials WHERE id=?`, id)
		return affected(ctx, "delete material", "material", id, res, err)
	})
}

func scanMaterial(sc scanner) (*model.ProjectMaterial, error) {
	var m model.ProjectMaterial
	var attachment dbJSON
	var created dbTime
	if err := sc.Scan(&m.ID, &m.ProjectID, &m.AgentID, &m.Name, &m.MIME, &m.Size, &m.SHA256, &attachment, &created); err != nil {
		return nil, err
	}
	if err := unmarshalJSON("material.attachment", attachment.raw, &m.Attachment); err != nil {
		return nil, err
	}
	m.CreatedAt = created.t
	return &m, nil
}
