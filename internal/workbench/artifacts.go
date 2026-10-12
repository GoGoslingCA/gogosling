package workbench

import (
	"context"
	"encoding/json"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"mime"
	"strings"
	"time"
)

func artifactFields(title, filename, mediaType, content string) error {
	if err := validText(content); err != nil {
		return err
	}
	if !validName(title, 200, true) || !safeFilename(filename) {
		return failure(model.CodeInvalid, "invalid_fields", nil)
	}
	mt, _, err := mime.ParseMediaType(mediaType)
	if err != nil || (!strings.HasPrefix(mt, "text/") && mt != "application/json" && mt != "application/xml" && mt != "application/yaml") {
		return failure(model.CodeInvalid, "invalid_fields", nil)
	}
	return nil
}

// CreateArtifact 在同一事务中核实真实助手来源并建立成果与第一个不可变版本。
func (s *Service) CreateArtifact(ctx context.Context, p model.Principal, sessionID string, in model.ArtifactInput, idempotencyKey string) (*model.Artifact, error) {
	who, err := owner(p)
	if err != nil {
		return nil, err
	}
	if err = artifactFields(in.Title, in.Filename, in.MediaType, in.Content); err != nil {
		return nil, err
	}
	if in.Kind != model.ArtifactDocument && in.Kind != model.ArtifactCode && in.Kind != model.ArtifactText {
		return nil, failure(model.CodeInvalid, "invalid_fields", nil)
	}
	if !validName(in.Fragment, 80, false) || !validName(idempotencyKey, 128, false) {
		return nil, failure(model.CodeInvalid, "invalid_fields", nil)
	}
	payload, _ := json.Marshal(in)
	requestHash := digest(string(payload))
	scope := "sessions/" + sessionID + "/artifacts"
	var out *model.Artifact
	created := false
	err = s.store.WorkbenchTx(ctx, func(tx model.WorkbenchStore) error {
		if idempotencyKey != "" {
			prior, err := tx.GetWorkbenchRequest(ctx, who, scope, idempotencyKey)
			if err == nil {
				if prior.RequestSHA256 != requestHash {
					return failure(model.CodeConflict, "idempotency_conflict", nil)
				}
				out, err = tx.GetArtifact(ctx, prior.ResourceID)
				return err
			}
			if !model.IsCode(err, model.CodeNotFound) {
				return err
			}
		}
		sess, err := tx.GetSession(ctx, sessionID)
		if err != nil {
			return err
		}
		source, err := tx.GetMessage(ctx, in.SourceMessageID)
		if err != nil {
			return err
		}
		if source.SessionID != sessionID || source.Role != model.RoleAssistant || source.Text() == "" {
			return failure(model.CodeInvalid, "invalid_source", nil)
		}
		var meta struct {
			Partial bool `json:"partial"`
		}
		_ = json.Unmarshal(source.Meta, &meta)
		now := time.Now().UTC()
		out = &model.Artifact{ID: model.NewID(), AgentID: sess.AgentID, SessionID: sessionID, ProjectID: sess.ProjectID, Title: strings.TrimSpace(in.Title), Kind: in.Kind, Filename: in.Filename, MediaType: in.MediaType, Version: 1, CreatedAt: now, UpdatedAt: now, Source: model.ArtifactSource{MessageID: source.ID, RunID: source.RunID, MessageSeq: source.Seq, SHA256: digest(source.Text()), Partial: meta.Partial, Fragment: in.Fragment}}
		if err = tx.CreateArtifact(ctx, out); err != nil {
			return err
		}
		revision := &model.ArtifactRevision{ID: model.NewID(), ArtifactID: out.ID, Content: in.Content, Filename: in.Filename, MediaType: in.MediaType, CreatedBy: who, CreatedAt: now}
		if err = tx.AppendArtifactRevision(ctx, revision, ""); err != nil {
			return err
		}
		if idempotencyKey != "" {
			if err = tx.CreateWorkbenchRequest(ctx, &model.WorkbenchRequest{OwnerID: who, Scope: scope, Key: idempotencyKey, RequestSHA256: requestHash, ResourceID: out.ID, CreatedAt: now}); err != nil {
				return err
			}
		}
		if err = audit(ctx, tx, p, "artifact.created", out.ID, out.AgentID, sessionID); err != nil {
			return err
		}
		out, err = tx.GetArtifact(ctx, out.ID)
		created = err == nil
		return err
	})
	if err != nil {
		return nil, err
	}
	if created {
		s.publish("artifact.created", out.AgentID, out.SessionID, map[string]any{"id": out.ID, "version": out.Version, "revision_id": out.LatestRevisionID, "source_message_id": out.Source.MessageID})
	}
	return out, nil
}

// GetArtifact 读取成果索引，项目归属由仓储随会话关联派生。
func (s *Service) GetArtifact(ctx context.Context, id string) (*model.Artifact, error) {
	return s.store.GetArtifact(ctx, id)
}

// ListArtifacts 只返回元数据；上下文资源不存在时明确返回 not_found。
func (s *Service) ListArtifacts(ctx context.Context, f model.ArtifactFilter, page model.Page) ([]*model.Artifact, error) {
	if f.SessionID != "" {
		if _, err := s.store.GetSession(ctx, f.SessionID); err != nil {
			return nil, err
		}
	}
	if f.ProjectID != "" {
		if _, err := s.store.GetProject(ctx, f.ProjectID); err != nil {
			return nil, err
		}
	}
	return s.store.ListArtifacts(ctx, f, page)
}

// AppendRevision 只在预期最新版本仍匹配时追加，不替换旧版本。
func (s *Service) AppendRevision(ctx context.Context, p model.Principal, id string, in model.ArtifactRevisionInput) (*model.ArtifactRevision, error) {
	who, err := owner(p)
	if err != nil {
		return nil, err
	}
	if in.ExpectedLatestRevisionID == "" {
		return nil, failure(model.CodeInvalid, "expected_version", nil)
	}
	if err = artifactFields("revision", in.Filename, in.MediaType, in.Content); err != nil {
		return nil, err
	}
	out := &model.ArtifactRevision{ID: model.NewID(), ArtifactID: id, Content: in.Content, Filename: in.Filename, MediaType: in.MediaType, CreatedBy: who, CreatedAt: time.Now().UTC()}
	var art *model.Artifact
	err = s.store.WorkbenchTx(ctx, func(tx model.WorkbenchStore) error {
		var err error
		art, err = tx.GetArtifact(ctx, id)
		if err != nil {
			return err
		}
		if err = tx.AppendArtifactRevision(ctx, out, in.ExpectedLatestRevisionID); err != nil {
			return err
		}
		return audit(ctx, tx, p, "artifact.revision.created", out.ID, art.AgentID, art.SessionID)
	})
	if err != nil {
		return nil, err
	}
	s.publish("artifact.revision.created", art.AgentID, art.SessionID, map[string]any{"id": id, "revision_id": out.ID, "version": out.Number})
	return out, nil
}

// GetRevision 同时检查成果 ID 与版本 ID，不能跨成果取正文。
func (s *Service) GetRevision(ctx context.Context, id, revisionID string) (*model.ArtifactRevision, error) {
	r, err := s.store.GetArtifactRevision(ctx, id, revisionID)
	if err != nil {
		return nil, err
	}
	if int64(len(r.Content)) != r.ByteSize || digest(r.Content) != r.SHA256 {
		return nil, failure(model.CodeInternal, "content_integrity", nil)
	}
	return r, nil
}

// ListRevisions 返回不可变版本元数据，不包含正文。
func (s *Service) ListRevisions(ctx context.Context, id string, page model.Page) ([]*model.ArtifactRevision, error) {
	if _, err := s.store.GetArtifact(ctx, id); err != nil {
		return nil, err
	}
	return s.store.ListArtifactRevisions(ctx, id, page)
}
