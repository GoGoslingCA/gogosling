package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

const draftCols = `id,owner_id,kind,agent_id,project_id,session_id,artifact_id,base_revision_id,text,filename,material_ids,attachments,version,created_at,updated_at`

func draftScope(d *model.Draft) (string, error) {
	if d.OwnerID == "" || d.AgentID == "" || !utf8.ValidString(d.Text) || len(d.Text) > model.WorkbenchTextLimit {
		return "", model.NewError(model.CodeInvalid, "store: 草稿主体、智能体或正文不合法")
	}
	switch d.Kind {
	case model.DraftComposer:
		if d.ArtifactID != "" || d.BaseRevisionID != "" {
			return "", model.NewError(model.CodeInvalid, "store: 输入草稿不能绑定成果版本")
		}
		if d.SessionID != "" {
			return "session:" + d.SessionID, nil
		}
		return "start:" + d.AgentID + ":" + d.ProjectID, nil
	case model.DraftArtifact:
		if d.ArtifactID == "" || d.BaseRevisionID == "" || d.SessionID == "" {
			return "", model.NewError(model.CodeInvalid, "store: 成果草稿缺少成果、会话或基线")
		}
		return "artifact:" + d.ArtifactID, nil
	default:
		return "", model.NewError(model.CodeInvalid, "store: 未知草稿种类")
	}
}

func validateDraftRefs(ctx context.Context, q dbtx, d, previous *model.Draft) error {
	if d.ProjectID != "" {
		if err := checkProjectAgent(ctx, q, d.ProjectID, d.AgentID); err != nil {
			return err
		}
	}
	if d.SessionID != "" {
		var agent string
		var project sql.NullString
		if err := q.QueryRowContext(ctx, `SELECT agent_id,project_id FROM sessions WHERE id=?`, d.SessionID).Scan(&agent, &project); err != nil {
			return notFoundOr(ctx, "validate draft", "session", d.SessionID, err)
		}
		if agent != d.AgentID || project.String != d.ProjectID {
			return model.NewError(model.CodeConflict, "store: 草稿与会话归属不一致")
		}
	}
	if d.ArtifactID != "" {
		var session string
		if err := q.QueryRowContext(ctx, `SELECT a.session_id FROM artifacts a JOIN artifact_revisions r ON r.artifact_id=a.id WHERE a.id=? AND r.id=?`, d.ArtifactID, d.BaseRevisionID).Scan(&session); err != nil {
			return notFoundOr(ctx, "validate draft", "artifact_revision", d.BaseRevisionID, err)
		}
		if session != d.SessionID {
			return model.NewError(model.CodeConflict, "store: 成果草稿来源会话不一致")
		}
	}
	for _, id := range d.MaterialIDs {
		var project, agent string
		if err := q.QueryRowContext(ctx, `SELECT project_id,agent_id FROM project_materials WHERE id=?`, id).Scan(&project, &agent); err != nil {
			// 已从资料库移除的旧引用保持可见，不能因此阻止用户保存其他正文。
			// 新加入的无效 ID 仍拒绝；发送前由领域层再次校验所有引用。
			if errors.Is(err, sql.ErrNoRows) && previous != nil && containsMaterial(previous.MaterialIDs, id) {
				continue
			}
			return notFoundOr(ctx, "validate draft material", "material", id, err)
		}
		if project != d.ProjectID || agent != d.AgentID {
			return model.NewError(model.CodeConflict, "store: 草稿引用了其他项目的材料")
		}
	}
	for _, a := range d.Attachments {
		if !validAttachment(a) {
			return model.NewError(model.CodeInvalid, "store: 草稿附件路径不合法")
		}
	}
	return nil
}

func containsMaterial(ids []string, target string) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}

func checkDraftScope(ctx context.Context, q dbtx, owner, scope, excludeID string) error {
	var id string
	err := q.QueryRowContext(ctx, `SELECT id FROM drafts WHERE owner_id=? AND scope_key=? AND id<>?`, owner, scope, excludeID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return wrapErr(ctx, "check draft scope", err)
	}
	return &model.Error{Code: model.CodeConflict, Message: "store: 已有可恢复的草稿", Details: map[string]any{"reason": "scope_exists", "existing_draft_id": id}}
}

func draftJSON(d *model.Draft) (any, any, error) {
	ids := d.MaterialIDs
	if ids == nil {
		ids = []string{}
	}
	atts := d.Attachments
	if atts == nil {
		atts = []model.Attachment{}
	}
	m, err := marshalArg("draft.material_ids", ids)
	if err != nil {
		return nil, nil, err
	}
	a, err := marshalArg("draft.attachments", atts)
	return m, a, err
}

// CreateDraft 对同一主体、同一作用域只允许一份草稿。
func (s *Store) CreateDraft(ctx context.Context, d *model.Draft) error {
	if err := requireID("draft", d.ID); err != nil {
		return err
	}
	scope, err := draftScope(d)
	if err != nil {
		return err
	}
	m, a, err := draftJSON(d)
	if err != nil {
		return err
	}
	return s.write(ctx, func(q dbtx) error {
		if err := validateDraftRefs(ctx, q, d, nil); err != nil {
			return err
		}
		if err := checkDraftScope(ctx, q, d.OwnerID, scope, ""); err != nil {
			return err
		}
		_, err := q.ExecContext(ctx, `INSERT INTO drafts(id,owner_id,scope_key,kind,agent_id,project_id,session_id,artifact_id,base_revision_id,text,filename,material_ids,attachments,version,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,1,?,?)`, d.ID, d.OwnerID, scope, d.Kind, d.AgentID, nullableString(d.ProjectID), nullableString(d.SessionID), nullableString(d.ArtifactID), nullableString(d.BaseRevisionID), d.Text, d.Filename, m, a, fmtTime(d.CreatedAt), fmtTime(d.UpdatedAt))
		if err != nil {
			return wrapErr(ctx, "create draft", err)
		}
		d.Version = 1
		return nil
	})
}

// GetDraft 只读取指定主体的草稿，主体不匹配时返回不存在。
func (s *Store) GetDraft(ctx context.Context, id, owner string) (*model.Draft, error) {
	if owner == "" {
		return nil, model.NewError(model.CodeInvalid, "store: 草稿主体不能为空")
	}
	d, err := scanDraft(s.rq().QueryRowContext(ctx, `SELECT `+draftCols+` FROM drafts WHERE id=? AND owner_id=?`, id, owner))
	if err != nil {
		return nil, notFoundOr(ctx, "get draft", "draft", id, err)
	}
	return d, nil
}

// ListDrafts 必须提供主体，不允许列出其他主体的草稿。
func (s *Store) ListDrafts(ctx context.Context, f model.DraftFilter, p model.Page) ([]*model.Draft, error) {
	if f.OwnerID == "" {
		return nil, model.NewError(model.CodeInvalid, "store: 草稿主体不能为空")
	}
	where := []string{"owner_id=?"}
	args := []any{f.OwnerID}
	for _, filter := range []struct{ column, value string }{{"kind", string(f.Kind)}, {"agent_id", f.AgentID}, {"project_id", f.ProjectID}, {"session_id", f.SessionID}, {"artifact_id", f.ArtifactID}} {
		if filter.value != "" {
			where = append(where, filter.column+"=?")
			args = append(args, filter.value)
		}
	}
	limit, offset := pageOf(p)
	args = append(args, limit, offset)
	rows, err := s.rq().QueryContext(ctx, `SELECT `+draftCols+` FROM drafts WHERE `+strings.Join(where, " AND ")+` ORDER BY updated_at DESC,id LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, wrapErr(ctx, "list drafts", err)
	}
	out, err := collect(rows, scanDraft)
	return out, wrapErr(ctx, "list drafts", err)
}

// UpdateDraft 用主体和版本原子更新，允许开始页草稿首次绑定到会话。
func (s *Store) UpdateDraft(ctx context.Context, d *model.Draft, expected int64) error {
	scope, err := draftScope(d)
	if err != nil {
		return err
	}
	m, a, err := draftJSON(d)
	if err != nil {
		return err
	}
	return s.write(ctx, func(q dbtx) error {
		current, err := scanDraft(q.QueryRowContext(ctx, `SELECT `+draftCols+` FROM drafts WHERE id=? AND owner_id=?`, d.ID, d.OwnerID))
		if err != nil {
			return notFoundOr(ctx, "update draft", "draft", d.ID, err)
		}
		if current.Version != expected {
			return staleVersion(current.Version)
		}
		if current.AgentID != d.AgentID || current.Kind != d.Kind || current.ArtifactID != d.ArtifactID || (current.SessionID != "" && current.SessionID != d.SessionID) {
			return model.NewError(model.CodeConflict, "store: 草稿主体或既有关联不可替换")
		}
		if err := validateDraftRefs(ctx, q, d, current); err != nil {
			return err
		}
		if err := checkDraftScope(ctx, q, d.OwnerID, scope, d.ID); err != nil {
			return err
		}
		_, err = q.ExecContext(ctx, `UPDATE drafts SET scope_key=?,project_id=?,session_id=?,base_revision_id=?,text=?,filename=?,material_ids=?,attachments=?,version=version+1,updated_at=? WHERE id=? AND owner_id=? AND version=?`, scope, nullableString(d.ProjectID), nullableString(d.SessionID), nullableString(d.BaseRevisionID), d.Text, d.Filename, m, a, fmtTime(d.UpdatedAt), d.ID, d.OwnerID, expected)
		if err != nil {
			return wrapErr(ctx, "update draft", err)
		}
		d.Version = expected + 1
		return nil
	})
}

// DeleteDraft 仅删除指定主体和指定版本，防止发送完成后误删新编辑。
func (s *Store) DeleteDraft(ctx context.Context, id, owner string, expected int64) error {
	if owner == "" {
		return model.NewError(model.CodeInvalid, "store: 草稿主体不能为空")
	}
	return s.write(ctx, func(q dbtx) error {
		var version int64
		if err := q.QueryRowContext(ctx, `SELECT version FROM drafts WHERE id=? AND owner_id=?`, id, owner).Scan(&version); err != nil {
			return notFoundOr(ctx, "delete draft", "draft", id, err)
		}
		if version != expected {
			return staleVersion(version)
		}
		_, err := q.ExecContext(ctx, `DELETE FROM drafts WHERE id=? AND owner_id=? AND version=?`, id, owner, expected)
		return wrapErr(ctx, "delete draft", err)
	})
}

func scanDraft(sc scanner) (*model.Draft, error) {
	var d model.Draft
	var project, session, artifact, base sql.NullString
	var materialIDs, attachments dbJSON
	var created, updated dbTime
	if err := sc.Scan(&d.ID, &d.OwnerID, &d.Kind, &d.AgentID, &project, &session, &artifact, &base, &d.Text, &d.Filename, &materialIDs, &attachments, &d.Version, &created, &updated); err != nil {
		return nil, err
	}
	d.ProjectID = project.String
	d.SessionID = session.String
	d.ArtifactID = artifact.String
	d.BaseRevisionID = base.String
	d.CreatedAt = created.t
	d.UpdatedAt = updated.t
	if err := unmarshalJSON("draft.material_ids", materialIDs.raw, &d.MaterialIDs); err != nil {
		return nil, err
	}
	if err := unmarshalJSON("draft.attachments", attachments.raw, &d.Attachments); err != nil {
		return nil, err
	}
	return &d, nil
}
