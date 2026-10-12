package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

func latestTestSchema(t *testing.T) int {
	t.Helper()
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	return ms[len(ms)-1].version
}

func createProjectForTest(t *testing.T, s *Store, agent string) *model.Project {
	t.Helper()
	p := &model.Project{ID: model.NewID(), AgentID: agent, Name: "需求项目", Status: model.ProjectActive, CreatedAt: ts(), UpdatedAt: ts()}
	if err := s.CreateProject(context.Background(), p); err != nil {
		t.Fatalf("创建项目应成功: %v", err)
	}
	return p
}

func Test工作台项目关联保持身份与CAS(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	a := mustCreateAgent(t, s)
	b := mustCreateAgent(t, s)
	p := createProjectForTest(t, s, a.ID)
	sess := mustCreateSession(t, s, a.ID, "会话路由")
	if err := s.SetSessionProject(ctx, sess.ID, "", p.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSession(ctx, sess.ID)
	if err != nil || got.ProjectID != p.ID || got.Key != sess.Key || got.ID != sess.ID {
		t.Fatalf("关联不应改变会话身份: %+v %v", got, err)
	}
	assertCode(t, s.SetSessionProject(ctx, sess.ID, "", ""), model.CodeConflict)
	other := createProjectForTest(t, s, b.ID)
	assertCode(t, s.SetSessionProject(ctx, sess.ID, p.ID, other.ID), model.CodeConflict)
	got.Title = "改名"
	got.ProjectID = ""
	if err := s.UpdateSession(ctx, got); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetSession(ctx, sess.ID)
	if got.ProjectID != p.ID {
		t.Fatal("旧会话更新不得意外清除项目归属")
	}
	list, err := s.ListSessions(ctx, model.SessionFilter{ProjectID: p.ID}, model.Page{})
	if err != nil || len(list) != 1 {
		t.Fatalf("项目筛选应返回一个会话: %v %d", err, len(list))
	}
	p.Name = "新名称"
	if err := s.UpdateProject(ctx, p, 1); err != nil || p.Version != 2 {
		t.Fatalf("项目更新应递增版本: %v %+v", err, p)
	}
	assertCode(t, s.UpdateProject(ctx, p, 1), model.CodeConflict)
	bad := newSession(b.ID, "跨智能体")
	bad.ProjectID = p.ID
	assertCode(t, s.CreateSession(ctx, bad), model.CodeInvalid)
}

func Test工作台草稿主体作用域与并发版本(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	a := mustCreateAgent(t, s)
	sess := mustCreateSession(t, s, a.ID, "draft")
	d := &model.Draft{ID: model.NewID(), OwnerID: "local", Kind: model.DraftComposer, AgentID: a.ID, SessionID: sess.ID, Text: "尚未发送的需求", CreatedAt: ts(), UpdatedAt: ts()}
	if err := s.CreateDraft(ctx, d); err != nil {
		t.Fatal(err)
	}
	duplicate := *d
	duplicate.ID = model.NewID()
	err := s.CreateDraft(ctx, &duplicate)
	assertCode(t, err, model.CodeConflict)
	var me *model.Error
	if !errors.As(err, &me) || me.Details["existing_draft_id"] != d.ID {
		t.Fatalf("重复作用域应提供可恢复草稿ID: %v", err)
	}
	_, err = s.GetDraft(ctx, d.ID, "other")
	assertCode(t, err, model.CodeNotFound)
	assertCode(t, s.DeleteDraft(ctx, d.ID, "other", 1), model.CodeNotFound)
	foreign := *d
	foreign.OwnerID = "other"
	assertCode(t, s.UpdateDraft(ctx, &foreign, 1), model.CodeNotFound)
	_, err = s.ListDrafts(ctx, model.DraftFilter{}, model.Page{})
	assertCode(t, err, model.CodeInvalid)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, text := range []string{"编辑一", "编辑二"} {
		wg.Add(1)
		go func(body string) {
			defer wg.Done()
			copy := *d
			copy.Text = body
			results <- s.UpdateDraft(ctx, &copy, 1)
		}(text)
	}
	wg.Wait()
	close(results)
	success, conflicts := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if model.IsCode(err, model.CodeConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("同版并发更新应一成功一冲突: %d %d", success, conflicts)
	}
	assertCode(t, s.DeleteDraft(ctx, d.ID, "local", 1), model.CodeConflict)
	saved, err := s.GetDraft(ctx, d.ID, "local")
	if err != nil || saved.Version != 2 {
		t.Fatalf("新编辑应保留: %+v %v", saved, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, s.Path(), WithLogger(testLogger()))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	saved, err = reopened.GetDraft(ctx, d.ID, "local")
	if err != nil || saved.Version != 2 {
		t.Fatalf("重启后应恢复草稿: %+v %v", saved, err)
	}
	if err := reopened.DeleteSession(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	_, err = reopened.GetDraft(ctx, d.ID, "local")
	assertCode(t, err, model.CodeNotFound)
}

func Test工作台材料引用不得跨项目(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	a := mustCreateAgent(t, s)
	p := createProjectForTest(t, s, a.ID)
	other := createProjectForTest(t, s, a.ID)
	m := &model.ProjectMaterial{ID: model.NewID(), ProjectID: p.ID, AgentID: a.ID, Name: "需求.md", MIME: "text/plain", Size: 4, SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("body"))), Attachment: model.Attachment{Name: "需求.md", Path: "inbound/需求.md", MIME: "text/plain", Size: 4}, CreatedAt: ts()}
	if err := s.CreateProjectMaterial(ctx, m); err != nil {
		t.Fatal(err)
	}
	d := &model.Draft{ID: model.NewID(), OwnerID: "local", Kind: model.DraftComposer, AgentID: a.ID, ProjectID: other.ID, MaterialIDs: []string{m.ID}, CreatedAt: ts(), UpdatedAt: ts()}
	assertCode(t, s.CreateDraft(ctx, d), model.CodeConflict)
	d.ProjectID = p.ID
	if err := s.CreateDraft(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProjectMaterial(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	saved, err := s.GetDraft(ctx, d.ID, "local")
	if err != nil || len(saved.MaterialIDs) != 1 {
		t.Fatalf("移除材料不应静默删除草稿引用: %+v %v", saved, err)
	}
	saved.Text = "资料已移除，但我的新编辑仍需保存"
	if err := s.UpdateDraft(ctx, saved, 1); err != nil {
		t.Fatalf("旧失效引用不应阻止正文保存: %v", err)
	}
	saved.MaterialIDs = append(saved.MaterialIDs, model.NewID())
	assertCode(t, s.UpdateDraft(ctx, saved, 2), model.CodeNotFound)
}

func artifactForTest(t *testing.T, s *Store, sess *model.Session) (*model.Artifact, *model.ArtifactRevision) {
	t.Helper()
	ctx := context.Background()
	m := &model.Message{SessionID: sess.ID, Role: model.RoleAssistant, Parts: []model.Part{{Type: model.PartText, Text: "第一稿"}}}
	if err := s.AppendMessage(ctx, m); err != nil {
		t.Fatal(err)
	}
	a := &model.Artifact{ID: model.NewID(), AgentID: sess.AgentID, SessionID: sess.ID, Title: "方案", Kind: model.ArtifactDocument, Filename: "方案.md", MediaType: "text/markdown", Source: model.ArtifactSource{MessageID: m.ID, MessageSeq: m.Seq, SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("第一稿")))}, CreatedAt: ts(), UpdatedAt: ts()}
	r := &model.ArtifactRevision{ID: model.NewID(), ArtifactID: a.ID, Content: "第一稿", Filename: a.Filename, MediaType: a.MediaType, CreatedBy: "local", CreatedAt: ts()}
	if err := s.WorkbenchTx(ctx, func(tx model.WorkbenchStore) error {
		if err := tx.CreateArtifact(ctx, a); err != nil {
			return err
		}
		return tx.AppendArtifactRevision(ctx, r, "")
	}); err != nil {
		t.Fatal(err)
	}
	return a, r
}

func Test工作台成果不可变版本来源与级联(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	a := mustCreateAgent(t, s)
	sess := mustCreateSession(t, s, a.ID, "artifact")
	artifact, first := artifactForTest(t, s, sess)
	got, err := s.GetArtifact(ctx, artifact.ID)
	if err != nil || got.Version != 1 || got.LatestRevisionID != first.ID {
		t.Fatalf("首版本应原子发布: %+v %v", got, err)
	}
	second := &model.ArtifactRevision{ID: model.NewID(), ArtifactID: artifact.ID, Content: "修订后的方案", Filename: "方案二.md", MediaType: "text/markdown", CreatedBy: "local", CreatedAt: ts()}
	if err := s.AppendArtifactRevision(ctx, second, first.ID); err != nil {
		t.Fatal(err)
	}
	stale := *second
	stale.ID = model.NewID()
	assertCode(t, s.AppendArtifactRevision(ctx, &stale, first.ID), model.CodeConflict)
	old, err := s.GetArtifactRevision(ctx, artifact.ID, first.ID)
	if err != nil || old.Content != "第一稿" || second.Number != 2 || second.ParentRevisionID != first.ID {
		t.Fatalf("旧版不可变且新版本链应正确: %+v %+v %v", old, second, err)
	}
	_, err = s.writer.ExecContext(ctx, `UPDATE artifact_revisions SET content='不能覆盖' WHERE id=?`, first.ID)
	if err == nil {
		t.Fatal("数据库必须拒绝原地修改版本")
	}
	meta, _ := json.Marshal(old)
	if strings.Contains(string(meta), "第一稿") {
		t.Fatal("元数据JSON不能带正文")
	}
	list, err := s.ListArtifactRevisions(ctx, artifact.ID, model.Page{})
	if err != nil || len(list) != 2 || list[0].Content != "" {
		t.Fatalf("版本列表只返回元数据: %v %+v", err, list)
	}
	p := createProjectForTest(t, s, a.ID)
	if err := s.SetSessionProject(ctx, sess.ID, "", p.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetArtifact(ctx, artifact.ID)
	if got.ProjectID != p.ID {
		t.Fatal("成果应跟随会话当前项目归属")
	}
	request := &model.WorkbenchRequest{OwnerID: "local", Scope: "artifact:" + sess.ID, Key: "create-1", RequestSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("请求摘要"))), ResourceID: artifact.ID, CreatedAt: ts()}
	if err := s.CreateWorkbenchRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	assertCode(t, s.CreateWorkbenchRequest(ctx, request), model.CodeConflict)
	if err := s.DeleteSession(ctx, sess.ID); err != nil {
		t.Fatalf("会话删除应级联版本且无循环外键问题: %v", err)
	}
	_, err = s.GetArtifactRevision(ctx, artifact.ID, first.ID)
	assertCode(t, err, model.CodeNotFound)
	_, err = s.GetWorkbenchRequest(ctx, request.OwnerID, request.Scope, request.Key)
	assertCode(t, err, model.CodeNotFound)
}

func Test工作台事务失败不留下半份成果(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	a := mustCreateAgent(t, s)
	p := &model.Project{ID: model.NewID(), AgentID: a.ID, Name: "回滚项目", Status: model.ProjectActive, CreatedAt: ts(), UpdatedAt: ts()}
	sentinel := errors.New("模拟失败")
	err := s.WorkbenchTx(ctx, func(tx model.WorkbenchStore) error {
		if err := tx.CreateProject(ctx, p); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	_, err = s.GetProject(ctx, p.ID)
	assertCode(t, err, model.CodeNotFound)
}

func Test删除智能体级联工作台记录且不遗留版本外键(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	agent := mustCreateAgent(t, s)
	project := createProjectForTest(t, s, agent.ID)
	session := mustCreateSession(t, s, agent.ID, "级联验证")
	if err := s.SetSessionProject(ctx, session.ID, "", project.ID); err != nil {
		t.Fatal(err)
	}
	artifact, revision := artifactForTest(t, s, session)
	draft := &model.Draft{ID: model.NewID(), OwnerID: "local", Kind: model.DraftArtifact, AgentID: agent.ID, ProjectID: project.ID, SessionID: session.ID, ArtifactID: artifact.ID, BaseRevisionID: revision.ID, Text: "未完成编辑", CreatedAt: ts(), UpdatedAt: ts()}
	if err := s.CreateDraft(ctx, draft); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAgent(ctx, agent.ID); err != nil {
		t.Fatalf("删除智能体应完整级联: %v", err)
	}
	if _, err := s.GetProject(ctx, project.ID); !model.IsCode(err, model.CodeNotFound) {
		t.Fatalf("项目应删除: %v", err)
	}
	if _, err := s.GetArtifactRevision(ctx, artifact.ID, revision.ID); !model.IsCode(err, model.CodeNotFound) {
		t.Fatalf("版本应删除: %v", err)
	}
	if _, err := s.GetDraft(ctx, draft.ID, "local"); !model.IsCode(err, model.CodeNotFound) {
		t.Fatalf("成果草稿应删除: %v", err)
	}
	rows, err := s.reader.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("级联后不应存在悬空外键")
	}
}

func createVersionOneDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	if err := prepareFile(path); err != nil {
		t.Fatal(err)
	}
	db, err := openPool(buildDSN(path, defaultBusyTimeout, true), 1)
	if err != nil {
		t.Fatal(err)
	}
	body, err := migrationFS.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,name TEXT NOT NULL,applied_at TEXT NOT NULL);` + string(body)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO schema_migrations VALUES(1,'init',?); INSERT INTO settings(key,value) VALUES('locale','"zh"')`, fmtTime(ts())); err != nil {
		t.Fatal(err)
	}
	return db
}

func Test旧库迁移前备份包含未checkpoint的WAL数据(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "gogosling.db")
	old := createVersionOneDatabase(t, dbPath)
	defer old.Close()
	s, err := Open(ctx, dbPath, WithLogger(testLogger()))
	if err != nil {
		t.Fatalf("旧库应先备份再升级: %v", err)
	}
	defer s.Close()
	files, err := filepath.Glob(filepath.Join(filepath.Dir(dbPath), "backups", "*.db"))
	if err != nil || len(files) != 1 {
		t.Fatalf("应保留一份一致备份: %v %v", files, err)
	}
	backup, err := sql.Open(driverName, files[0])
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	var version int
	var locale string
	if err := backup.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 1 {
		t.Fatalf("备份必须保持迁移前版本: %d %v", version, err)
	}
	if err := backup.QueryRow(`SELECT value FROM settings WHERE key='locale'`).Scan(&locale); err != nil || locale != `"zh"` {
		t.Fatalf("备份必须含WAL内已提交数据: %s %v", locale, err)
	}
	content, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	manifestData, err := os.ReadFile(files[0] + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		SHA256 string `json:"sha256"`
	}
	if err := json.Unmarshal(manifestData, &manifest); err != nil || manifest.SHA256 != fmt.Sprintf("%x", sha256.Sum256(content)) {
		t.Fatalf("备份摘要应可验证: %v", err)
	}
	if runtime.GOOS != "windows" {
		for _, file := range []string{files[0], files[0] + ".json"} {
			info, err := os.Stat(file)
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("备份文件权限应为0600: %v", err)
			}
		}
		info, _ := os.Stat(filepath.Dir(files[0]))
		if info.Mode().Perm() != 0o700 {
			t.Fatal("备份目录权限应为0700")
		}
	}
}

func Test备份失败拒绝迁移且保留原版本(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "gogosling.db")
	old := createVersionOneDatabase(t, dbPath)
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(dbPath), "backups"), []byte("阻止建立备份目录"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Open(context.Background(), dbPath, WithLogger(testLogger()))
	assertCode(t, err, model.CodeInternal)
	db, err := sql.Open(driverName, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 1 {
		t.Fatalf("备份失败不得迁移: %d %v", version, err)
	}
}
