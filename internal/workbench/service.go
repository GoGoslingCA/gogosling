// Package workbench 管理项目、资料索引、主体草稿和不可变成果版本。
// 文件操作只通过组合根注入的入站附件能力；不覆盖工作区文件，不执行模型内容。
package workbench

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/GoGoslingCA/gogosling/internal/event"
	"github.com/GoGoslingCA/gogosling/internal/i18n"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

// Files 是现有工作区入站附件的消费者接口；实现方须核实路径、符号链接与大小。
type Files interface {
	SaveInbound(context.Context, string, string, io.Reader, int64) (model.Attachment, error)
	Inspect(context.Context, string, model.Attachment) (model.Attachment, string, error)
}

// Service 并发安全；业务并发控制依赖仓储事务及条件更新。
type Service struct {
	store model.WorkbenchStore
	files Files
	bus   *event.Bus
}

// New 构造工作台服务；仓储和文件能力必须已完成初始化。
func New(store model.WorkbenchStore, files Files, bus *event.Bus) (*Service, error) {
	if store == nil || files == nil {
		return nil, failure(model.CodeUnavailable, "unavailable", nil)
	}
	return &Service{store: store, files: files, bus: bus}, nil
}

func failure(code model.Code, reason string, details map[string]any) *model.Error {
	if details == nil {
		details = map[string]any{}
	}
	details["reason"] = reason
	return &model.Error{Code: code, Message: i18n.T("en", "workbench."+reason, nil), Details: details}
}
func scopeMismatch() error { return failure(model.CodeInvalid, "scope_mismatch", nil) }
func validText(text string) error {
	if !utf8.ValidString(text) || len(text) > model.WorkbenchTextLimit {
		return failure(model.CodeInvalid, "text_limit", map[string]any{"max_bytes": model.WorkbenchTextLimit})
	}
	return nil
}
func validName(name string, max int, required bool) bool {
	return utf8.ValidString(name) && utf8.RuneCountInString(name) <= max && (!required || strings.TrimSpace(name) != "") && !strings.ContainsAny(name, "\x00\r\n")
}
func safeFilename(name string) bool {
	return validName(name, 160, true) && name != "." && name != ".." && !strings.ContainsAny(name, "/\\:")
}
func digest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
func owner(p model.Principal) (string, error) {
	if p.ID == "" {
		return "", failure(model.CodeUnauthorized, "owner_required", nil)
	}
	return p.ID, nil
}
func (s *Service) publish(kind, agentID, sessionID string, data map[string]any) {
	if s.bus != nil {
		s.bus.Publish(event.Event{Type: kind, AgentID: agentID, SessionID: sessionID, Payload: data})
	}
}
func audit(ctx context.Context, tx model.WorkbenchStore, p model.Principal, action, id, agentID, sessionID string) error {
	return tx.AppendAudit(ctx, &model.AuditEntry{ID: model.NewID(), At: time.Now().UTC(), Actor: p.ID, Action: action, Resource: id, AgentID: agentID, SessionID: sessionID, Outcome: "ok"})
}

// CreateProject 创建同一智能体下的组织容器。
func (s *Service) CreateProject(ctx context.Context, p model.Principal, in model.ProjectInput) (*model.Project, error) {
	in.Name = strings.TrimSpace(in.Name)
	if !validName(in.Name, 120, true) || !utf8.ValidString(in.Description) || utf8.RuneCountInString(in.Description) > 2000 {
		return nil, failure(model.CodeInvalid, "invalid_fields", nil)
	}
	now := time.Now().UTC()
	out := &model.Project{ID: model.NewID(), AgentID: in.AgentID, Name: in.Name, Description: in.Description, Status: model.ProjectActive, Version: 1, CreatedAt: now, UpdatedAt: now}
	err := s.store.WorkbenchTx(ctx, func(tx model.WorkbenchStore) error {
		if _, err := tx.GetAgent(ctx, in.AgentID); err != nil {
			return err
		}
		if err := tx.CreateProject(ctx, out); err != nil {
			return err
		}
		return audit(ctx, tx, p, "project.created", out.ID, out.AgentID, "")
	})
	if err != nil {
		return nil, err
	}
	s.publish("project.created", out.AgentID, "", map[string]any{"id": out.ID, "version": out.Version})
	return out, nil
}

// GetProject 读取项目元数据。
func (s *Service) GetProject(ctx context.Context, id string) (*model.Project, error) {
	return s.store.GetProject(ctx, id)
}

// ListProjects 按智能体和归档状态列出项目。
func (s *Service) ListProjects(ctx context.Context, f model.ProjectFilter, page model.Page) ([]*model.Project, error) {
	if f.Status != "" && f.Status != model.ProjectActive && f.Status != model.ProjectArchived {
		return nil, failure(model.CodeInvalid, "invalid_fields", nil)
	}
	return s.store.ListProjects(ctx, f, page)
}

// UpdateProject 通过版本条件修改名称、描述或归档状态。
func (s *Service) UpdateProject(ctx context.Context, p model.Principal, id string, in model.ProjectPatch) (*model.Project, error) {
	if in.ExpectedVersion < 1 {
		return nil, failure(model.CodeInvalid, "expected_version", nil)
	}
	var out *model.Project
	err := s.store.WorkbenchTx(ctx, func(tx model.WorkbenchStore) error {
		var err error
		out, err = tx.GetProject(ctx, id)
		if err != nil {
			return err
		}
		if in.Name != nil {
			out.Name = strings.TrimSpace(*in.Name)
		}
		if in.Description != nil {
			out.Description = *in.Description
		}
		if in.Status != nil {
			out.Status = *in.Status
		}
		if !validName(out.Name, 120, true) || utf8.RuneCountInString(out.Description) > 2000 || (out.Status != model.ProjectActive && out.Status != model.ProjectArchived) {
			return failure(model.CodeInvalid, "invalid_fields", nil)
		}
		out.UpdatedAt = time.Now().UTC()
		if err = tx.UpdateProject(ctx, out, in.ExpectedVersion); err != nil {
			return err
		}
		return audit(ctx, tx, p, "project.updated", out.ID, out.AgentID, "")
	})
	if err != nil {
		return nil, err
	}
	s.publish("project.updated", out.AgentID, "", map[string]any{"id": id, "version": out.Version})
	return out, nil
}

// SetSessionProject 校验智能体归属，以预期原项目为条件更新关联；调用方发布既有会话视图事件。
func (s *Service) SetSessionProject(ctx context.Context, p model.Principal, id, expected, next string) (*model.Session, error) {
	var out *model.Session
	err := s.store.WorkbenchTx(ctx, func(tx model.WorkbenchStore) error {
		current, err := tx.GetSession(ctx, id)
		if err != nil {
			return err
		}
		if next != "" {
			project, err := tx.GetProject(ctx, next)
			if err != nil {
				return err
			}
			if project.AgentID != current.AgentID {
				return scopeMismatch()
			}
		}
		if err = tx.SetSessionProject(ctx, id, expected, next); err != nil {
			return err
		}
		out, err = tx.GetSession(ctx, id)
		if err != nil {
			return err
		}
		return audit(ctx, tx, p, "session.project.updated", id, current.AgentID, id)
	})
	return out, err
}

// UploadMaterial 保存新入站文件后创建项目索引，不覆盖或清理其他附件。
func (s *Service) UploadMaterial(ctx context.Context, p model.Principal, projectID, name string, r io.Reader, max int64) (*model.ProjectMaterial, error) {
	project, err := s.store.GetProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	att, err := s.files.SaveInbound(ctx, project.AgentID, name, r, max)
	if err != nil {
		return nil, err
	}
	att, hash, err := s.files.Inspect(ctx, project.AgentID, att)
	if err != nil {
		return nil, err
	}
	out := &model.ProjectMaterial{ID: model.NewID(), ProjectID: projectID, AgentID: project.AgentID, Name: att.Name, MIME: att.MIME, Size: att.Size, SHA256: hash, Attachment: att, CreatedAt: time.Now().UTC()}
	err = s.store.WorkbenchTx(ctx, func(tx model.WorkbenchStore) error {
		if err := tx.CreateProjectMaterial(ctx, out); err != nil {
			return err
		}
		return audit(ctx, tx, p, "project.material.created", out.ID, out.AgentID, "")
	})
	if err != nil {
		return nil, err
	}
	s.publish("project.material.created", out.AgentID, "", map[string]any{"id": out.ID, "project_id": projectID})
	return out, nil
}

// ListMaterials 读取项目资料索引，不扫描历史入站目录。
func (s *Service) ListMaterials(ctx context.Context, projectID string, page model.Page) ([]*model.ProjectMaterial, error) {
	if _, err := s.store.GetProject(ctx, projectID); err != nil {
		return nil, err
	}
	return s.store.ListProjectMaterials(ctx, projectID, page)
}

// RemoveMaterial 仅移除项目索引，保留文件与历史消息。
func (s *Service) RemoveMaterial(ctx context.Context, p model.Principal, projectID, id string) error {
	var agentID string
	err := s.store.WorkbenchTx(ctx, func(tx model.WorkbenchStore) error {
		m, err := tx.GetProjectMaterial(ctx, id)
		if err != nil {
			return err
		}
		if m.ProjectID != projectID {
			return model.NotFound("material", id)
		}
		agentID = m.AgentID
		if err = tx.DeleteProjectMaterial(ctx, id); err != nil {
			return err
		}
		return audit(ctx, tx, p, "project.material.removed", id, agentID, "")
	})
	if err == nil {
		s.publish("project.material.removed", agentID, "", map[string]any{"id": id, "project_id": projectID})
	}
	return err
}

// ResolveMaterials 校验本次明确选中的项目资料，文件变化或越界时拒绝整次发送。
func (s *Service) ResolveMaterials(ctx context.Context, sessionID string, ids []string) ([]model.Attachment, json.RawMessage, error) {
	if len(ids) > 32 {
		return nil, nil, failure(model.CodeInvalid, "invalid_fields", nil)
	}
	sess, err := s.store.GetSession(ctx, sessionID)
	if err != nil {
		return nil, nil, err
	}
	out := []model.Attachment{}
	sources := []map[string]any{}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		m, err := s.store.GetProjectMaterial(ctx, id)
		if err != nil {
			return nil, nil, err
		}
		if sess.ProjectID == "" || m.ProjectID != sess.ProjectID || m.AgentID != sess.AgentID {
			return nil, nil, scopeMismatch()
		}
		att, hash, err := s.files.Inspect(ctx, sess.AgentID, m.Attachment)
		if err != nil {
			return nil, nil, err
		}
		if att.Size != m.Size || hash != m.SHA256 {
			return nil, nil, failure(model.CodeConflict, "material_changed", map[string]any{"material_id": id})
		}
		out = append(out, att)
		sources = append(sources, map[string]any{"id": id, "sha256": hash, "project_id": m.ProjectID, "agent_id": m.AgentID})
	}
	meta, _ := json.Marshal(sources)
	return out, meta, nil
}

func canonicalPath(value string) (string, error) {
	p := strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	if !strings.HasPrefix(p, "inbound/") || path.Clean(p) != p || strings.ContainsRune(p, 0) {
		return "", scopeMismatch()
	}
	return p, nil
}

// Capabilities 只描述本服务已完整实现的 P3A 能力与正文限制。
func (s *Service) Capabilities() map[string]any {
	return map[string]any{"workbench.projects": 1, "workbench.materials": 1, "workbench.drafts": 1, "workbench.artifacts": 1, "workbench.limits": map[string]int{"text_bytes": model.WorkbenchTextLimit}}
}
