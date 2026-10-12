package workbench

import (
	"context"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"strings"
	"time"
)

// draftFiles 在数据库事务外校验新加入的附件；已保存但后来失效的引用仍可随正文恢复。
func (s *Service) draftFiles(ctx context.Context, in *model.DraftInput, old *model.Draft) error {
	previous := map[string]bool{}
	if old != nil {
		for _, att := range old.Attachments {
			previous[att.Path] = true
		}
	}
	if len(in.Attachments) > 32 {
		return failure(model.CodeInvalid, "invalid_fields", nil)
	}
	for i, att := range in.Attachments {
		rel, err := canonicalPath(att.Path)
		if err != nil {
			return err
		}
		att.Path = rel
		checked, _, err := s.files.Inspect(ctx, in.AgentID, att)
		if err != nil {
			if previous[rel] && model.IsCode(err, model.CodeNotFound) {
				continue
			}
			return err
		}
		in.Attachments[i] = checked
	}
	return nil
}

func (s *Service) draftInput(ctx context.Context, tx model.WorkbenchStore, in model.DraftInput, old *model.Draft) (*model.Draft, error) {
	if err := validText(in.Text); err != nil {
		return nil, err
	}
	if in.Filename != "" && !safeFilename(in.Filename) {
		return nil, failure(model.CodeInvalid, "invalid_fields", nil)
	}
	if len(in.MaterialIDs) > 32 || len(in.Attachments) > 32 {
		return nil, failure(model.CodeInvalid, "invalid_fields", nil)
	}
	if _, err := tx.GetAgent(ctx, in.AgentID); err != nil {
		return nil, err
	}
	out := &model.Draft{Kind: in.Kind, AgentID: in.AgentID, ProjectID: in.ProjectID, SessionID: in.SessionID, ArtifactID: in.ArtifactID, BaseRevisionID: in.BaseRevisionID, Text: in.Text, Filename: in.Filename, MaterialIDs: in.MaterialIDs, Attachments: in.Attachments}
	if in.SessionID != "" {
		sess, err := tx.GetSession(ctx, in.SessionID)
		if err != nil {
			return nil, err
		}
		if sess.AgentID != in.AgentID || (in.ProjectID != "" && in.ProjectID != sess.ProjectID) {
			return nil, scopeMismatch()
		}
		out.ProjectID = sess.ProjectID
	}
	if out.ProjectID != "" {
		project, err := tx.GetProject(ctx, out.ProjectID)
		if err != nil {
			return nil, err
		}
		if project.AgentID != out.AgentID {
			return nil, scopeMismatch()
		}
	}
	switch in.Kind {
	case model.DraftComposer:
		if in.ArtifactID != "" || in.BaseRevisionID != "" {
			return nil, scopeMismatch()
		}
	case model.DraftArtifact:
		art, err := tx.GetArtifact(ctx, in.ArtifactID)
		if err != nil {
			return nil, err
		}
		if in.BaseRevisionID == "" || art.AgentID != in.AgentID || (in.SessionID != "" && in.SessionID != art.SessionID) {
			return nil, scopeMismatch()
		}
		if _, err = tx.GetArtifactRevision(ctx, art.ID, in.BaseRevisionID); err != nil {
			return nil, err
		}
		out.SessionID = art.SessionID
		out.ProjectID = art.ProjectID
	default:
		return nil, failure(model.CodeInvalid, "invalid_fields", nil)
	}
	if old != nil && (old.Kind != out.Kind || old.AgentID != out.AgentID || old.ArtifactID != out.ArtifactID || (old.SessionID != "" && old.SessionID != out.SessionID)) {
		return nil, scopeMismatch()
	}
	prior := map[string]bool{}
	if old != nil {
		for _, id := range old.MaterialIDs {
			prior[id] = true
		}
	}
	for _, id := range in.MaterialIDs {
		m, err := tx.GetProjectMaterial(ctx, id)
		if model.IsCode(err, model.CodeNotFound) && prior[id] {
			continue
		}
		if err != nil {
			return nil, err
		}
		if m.ProjectID != out.ProjectID || m.AgentID != out.AgentID {
			return nil, scopeMismatch()
		}
	}
	for _, att := range in.Attachments {
		if _, err := canonicalPath(att.Path); err != nil {
			return nil, err
		}
		if !validName(att.Name, 160, false) || att.Size < 0 || strings.ContainsAny(att.MIME, "\r\n") {
			return nil, failure(model.CodeInvalid, "invalid_fields", nil)
		}
	}
	return out, nil
}

// CreateDraft 按主体与服务器生成的业务作用域创建草稿。
func (s *Service) CreateDraft(ctx context.Context, p model.Principal, in model.DraftInput) (*model.Draft, error) {
	id, err := owner(p)
	if err != nil {
		return nil, err
	}
	if err = s.draftFiles(ctx, &in, nil); err != nil {
		return nil, err
	}
	var out *model.Draft
	err = s.store.WorkbenchTx(ctx, func(tx model.WorkbenchStore) error {
		var err error
		out, err = s.draftInput(ctx, tx, in, nil)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		out.ID = model.NewID()
		out.OwnerID = id
		out.Version = 1
		out.CreatedAt = now
		out.UpdatedAt = now
		return tx.CreateDraft(ctx, out)
	})
	if err != nil {
		return nil, err
	}
	s.publish("draft.updated", out.AgentID, out.SessionID, map[string]any{"id": out.ID, "version": out.Version})
	return out, nil
}

// GetDraft 只允许读取当前主体的草稿。
func (s *Service) GetDraft(ctx context.Context, p model.Principal, id string) (*model.Draft, error) {
	who, err := owner(p)
	if err != nil {
		return nil, err
	}
	return s.store.GetDraft(ctx, id, who)
}

// ListDrafts 主体由服务器覆盖，不能通过过滤器越权。
func (s *Service) ListDrafts(ctx context.Context, p model.Principal, f model.DraftFilter, page model.Page) ([]*model.Draft, error) {
	who, err := owner(p)
	if err != nil {
		return nil, err
	}
	f.OwnerID = who
	if f.Kind != "" && f.Kind != model.DraftComposer && f.Kind != model.DraftArtifact {
		return nil, failure(model.CodeInvalid, "invalid_fields", nil)
	}
	return s.store.ListDrafts(ctx, f, page)
}

// UpdateDraft 条件更新正文；开始页草稿可以首次绑定到同智能体会话。
// 成果基线可经同一版本条件切换，但必须仍是该成果的真实版本，不改变草稿身份。
func (s *Service) UpdateDraft(ctx context.Context, p model.Principal, id string, in model.DraftInput) (*model.Draft, error) {
	who, err := owner(p)
	if err != nil {
		return nil, err
	}
	if in.ExpectedVersion < 1 {
		return nil, failure(model.CodeInvalid, "expected_version", nil)
	}
	prior, err := s.store.GetDraft(ctx, id, who)
	if err != nil {
		return nil, err
	}
	if err = s.draftFiles(ctx, &in, prior); err != nil {
		return nil, err
	}
	var out *model.Draft
	err = s.store.WorkbenchTx(ctx, func(tx model.WorkbenchStore) error {
		old, err := tx.GetDraft(ctx, id, who)
		if err != nil {
			return err
		}
		out, err = s.draftInput(ctx, tx, in, old)
		if err != nil {
			return err
		}
		out.ID = id
		out.OwnerID = who
		out.CreatedAt = old.CreatedAt
		out.UpdatedAt = time.Now().UTC()
		return tx.UpdateDraft(ctx, out, in.ExpectedVersion)
	})
	if err != nil {
		return nil, err
	}
	s.publish("draft.updated", out.AgentID, out.SessionID, map[string]any{"id": id, "version": out.Version})
	return out, nil
}

// DeleteDraft 仅删除主体持有且仍匹配预期版本的草稿。
func (s *Service) DeleteDraft(ctx context.Context, p model.Principal, id string, expected int64) error {
	who, err := owner(p)
	if err != nil {
		return err
	}
	if expected < 1 {
		return failure(model.CodeInvalid, "expected_version", nil)
	}
	d, err := s.store.GetDraft(ctx, id, who)
	if err != nil {
		return err
	}
	err = s.store.DeleteDraft(ctx, id, who, expected)
	if err == nil {
		s.publish("draft.deleted", d.AgentID, d.SessionID, map[string]any{"id": id, "version": expected})
	}
	return err
}
