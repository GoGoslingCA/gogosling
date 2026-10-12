package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

const artifactCols = `a.id,a.agent_id,a.session_id,s.project_id,a.title,a.kind,a.filename,a.media_type,a.source,a.latest_revision_id,a.version,a.created_at,a.updated_at`
const artifactFrom = ` FROM artifacts a JOIN sessions s ON s.id=a.session_id`

// CreateArtifact 建立来源索引；调用方须在同一 WorkbenchTx 内追加第一版。
func (s *Store) CreateArtifact(ctx context.Context, a *model.Artifact) error {
	if err := requireID("artifact", a.ID); err != nil {
		return err
	}
	source, err := marshalArg("artifact.source", a.Source)
	if err != nil {
		return err
	}
	return s.write(ctx, func(q dbtx) error {
		var agent, role, session string
		if err := q.QueryRowContext(ctx, `SELECT s.agent_id,m.role,m.session_id FROM messages m JOIN sessions s ON s.id=m.session_id WHERE m.id=?`, a.Source.MessageID).Scan(&agent, &role, &session); err != nil {
			return notFoundOr(ctx, "artifact source", "message", a.Source.MessageID, err)
		}
		if agent != a.AgentID || session != a.SessionID || role != string(model.RoleAssistant) {
			return model.NewError(model.CodeConflict, "store: 成果必须来自所属会话的助手消息")
		}
		_, err := q.ExecContext(ctx, `INSERT INTO artifacts(id,agent_id,session_id,title,kind,filename,media_type,source_message_id,source,version,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,0,?,?)`, a.ID, a.AgentID, a.SessionID, a.Title, a.Kind, a.Filename, a.MediaType, a.Source.MessageID, source, fmtTime(a.CreatedAt), fmtTime(a.UpdatedAt))
		if err != nil {
			return wrapErr(ctx, "create artifact", err)
		}
		a.Version = 0
		a.LatestRevisionID = ""
		return nil
	})
}

// GetArtifact 返回元数据；项目归属从会话实时取得。
func (s *Store) GetArtifact(ctx context.Context, id string) (*model.Artifact, error) {
	a, err := scanArtifact(s.rq().QueryRowContext(ctx, `SELECT `+artifactCols+artifactFrom+` WHERE a.id=?`, id))
	if err != nil {
		return nil, notFoundOr(ctx, "get artifact", "artifact", id, err)
	}
	return a, nil
}

// ListArtifacts 按会话或当前项目归属列出成果。
func (s *Store) ListArtifacts(ctx context.Context, f model.ArtifactFilter, p model.Page) ([]*model.Artifact, error) {
	where := []string{"1=1"}
	args := []any{}
	for _, filter := range []struct{ column, value string }{{"a.agent_id", f.AgentID}, {"a.session_id", f.SessionID}, {"s.project_id", f.ProjectID}} {
		if filter.value != "" {
			where = append(where, filter.column+"=?")
			args = append(args, filter.value)
		}
	}
	limit, offset := pageOf(p)
	args = append(args, limit, offset)
	rows, err := s.rq().QueryContext(ctx, `SELECT `+artifactCols+artifactFrom+` WHERE `+strings.Join(where, " AND ")+` ORDER BY a.created_at,a.id LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, wrapErr(ctx, "list artifacts", err)
	}
	out, err := collect(rows, scanArtifact)
	return out, wrapErr(ctx, "list artifacts", err)
}

func scanArtifact(sc scanner) (*model.Artifact, error) {
	var a model.Artifact
	var project, latest sql.NullString
	var source dbJSON
	var created, updated dbTime
	if err := sc.Scan(&a.ID, &a.AgentID, &a.SessionID, &project, &a.Title, &a.Kind, &a.Filename, &a.MediaType, &source, &latest, &a.Version, &created, &updated); err != nil {
		return nil, err
	}
	a.ProjectID = project.String
	a.LatestRevisionID = latest.String
	a.CreatedAt = created.t
	a.UpdatedAt = updated.t
	if err := unmarshalJSON("artifact.source", source.raw, &a.Source); err != nil {
		return nil, err
	}
	return &a, nil
}

const revisionCols = `id,artifact_id,number,parent_revision_id,content,sha256,byte_size,filename,media_type,created_by,created_at`

// AppendArtifactRevision 原子校验最新版本并追加不可变正文，成功后回填序号和摘要。
func (s *Store) AppendArtifactRevision(ctx context.Context, r *model.ArtifactRevision, expected string) error {
	if err := requireID("artifact_revision", r.ID); err != nil {
		return err
	}
	if !utf8.ValidString(r.Content) || len(r.Content) > model.WorkbenchTextLimit || r.CreatedBy == "" {
		return model.NewError(model.CodeInvalid, "store: 成果正文或作者不合法")
	}
	return s.write(ctx, func(q dbtx) error {
		var latest sql.NullString
		var version int64
		if err := q.QueryRowContext(ctx, `SELECT latest_revision_id,version FROM artifacts WHERE id=?`, r.ArtifactID).Scan(&latest, &version); err != nil {
			return notFoundOr(ctx, "append revision", "artifact", r.ArtifactID, err)
		}
		if latest.String != expected {
			return &model.Error{Code: model.CodeConflict, Message: "store: 成果已有新版本", Details: map[string]any{"reason": "stale_version", "current_latest_revision_id": latest.String, "current_version": version}}
		}
		r.Number = version + 1
		r.ParentRevisionID = latest.String
		r.ByteSize = int64(len(r.Content))
		r.SHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(r.Content)))
		_, err := q.ExecContext(ctx, `INSERT INTO artifact_revisions(`+revisionCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, r.ID, r.ArtifactID, r.Number, nullableString(r.ParentRevisionID), r.Content, r.SHA256, r.ByteSize, r.Filename, r.MediaType, r.CreatedBy, fmtTime(r.CreatedAt))
		if err != nil {
			return wrapErr(ctx, "append revision", err)
		}
		_, err = q.ExecContext(ctx, `UPDATE artifacts SET latest_revision_id=?,version=version+1,filename=?,media_type=?,updated_at=? WHERE id=? AND version=?`, r.ID, r.Filename, r.MediaType, fmtTime(r.CreatedAt), r.ArtifactID, version)
		return wrapErr(ctx, "advance artifact", err)
	})
}

// GetArtifactRevision 读取指定成果的正文，不能用其他成果 ID 读取版本。
func (s *Store) GetArtifactRevision(ctx context.Context, artifactID, revisionID string) (*model.ArtifactRevision, error) {
	r, err := scanRevision(s.rq().QueryRowContext(ctx, `SELECT `+revisionCols+` FROM artifact_revisions WHERE id=? AND artifact_id=?`, revisionID, artifactID))
	if err != nil {
		return nil, notFoundOr(ctx, "get revision", "artifact_revision", revisionID, err)
	}
	return r, nil
}

// ListArtifactRevisions 只读取版本元数据，避免列表批量加载正文。
func (s *Store) ListArtifactRevisions(ctx context.Context, id string, p model.Page) ([]*model.ArtifactRevision, error) {
	limit, offset := pageOf(p)
	cols := strings.Replace(revisionCols, ",content,", ",'' AS content,", 1)
	rows, err := s.rq().QueryContext(ctx, `SELECT `+cols+` FROM artifact_revisions WHERE artifact_id=? ORDER BY number DESC LIMIT ? OFFSET ?`, id, limit, offset)
	if err != nil {
		return nil, wrapErr(ctx, "list revisions", err)
	}
	out, err := collect(rows, scanRevision)
	return out, wrapErr(ctx, "list revisions", err)
}

func scanRevision(sc scanner) (*model.ArtifactRevision, error) {
	var r model.ArtifactRevision
	var parent sql.NullString
	var created dbTime
	if err := sc.Scan(&r.ID, &r.ArtifactID, &r.Number, &parent, &r.Content, &r.SHA256, &r.ByteSize, &r.Filename, &r.MediaType, &r.CreatedBy, &created); err != nil {
		return nil, err
	}
	r.ParentRevisionID = parent.String
	r.CreatedAt = created.t
	return &r, nil
}

// GetWorkbenchRequest 按主体、端点作用域和键读取幂等记录。
func (s *Store) GetWorkbenchRequest(ctx context.Context, owner, scope, key string) (*model.WorkbenchRequest, error) {
	var r model.WorkbenchRequest
	var created dbTime
	table := "workbench_requests"
	if strings.HasPrefix(scope, "change.apply:") {
		table = "change_requests"
	}
	err := s.rq().QueryRowContext(ctx, `SELECT owner_id,scope,request_key,request_sha256,resource_id,created_at FROM `+table+` WHERE owner_id=? AND scope=? AND request_key=?`, owner, scope, key).Scan(&r.OwnerID, &r.Scope, &r.Key, &r.RequestSHA256, &r.ResourceID, &created)
	if err != nil {
		return nil, notFoundOr(ctx, "get workbench request", "workbench_request", key, err)
	}
	r.CreatedAt = created.t
	return &r, nil
}

// CreateWorkbenchRequest 必须与成果创建处于同一事务，不保存正文。
func (s *Store) CreateWorkbenchRequest(ctx context.Context, r *model.WorkbenchRequest) error {
	if r.OwnerID == "" || r.Scope == "" || r.Key == "" || !validDigest(r.RequestSHA256) {
		return model.NewError(model.CodeInvalid, "store: 请求幂等字段不合法")
	}
	return s.write(ctx, func(q dbtx) error {
		table := "workbench_requests"
		if strings.HasPrefix(r.Scope, "change.apply:") {
			table = "change_requests"
		}
		_, err := q.ExecContext(ctx, `INSERT INTO `+table+`(owner_id,scope,request_key,request_sha256,resource_id,created_at) VALUES(?,?,?,?,?,?)`, r.OwnerID, r.Scope, r.Key, r.RequestSHA256, r.ResourceID, fmtTime(r.CreatedAt))
		return wrapErr(ctx, "create workbench request", err)
	})
}
