package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// ── 测试辅助 ────────────────────────────────────────────────────────────────

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "data", "gogosling.db"), WithLogger(testLogger()))
	if err != nil {
		t.Fatalf("Open 应成功: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// ts 返回去掉单调时钟读数的当前 UTC 时间，便于与数据库读回的值做相等比较。
func ts() time.Time { return time.Now().UTC().Round(0) }

func ptr(t time.Time) *time.Time { return &t }

func newAgent() *model.Agent {
	now := ts()
	return &model.Agent{
		ID: model.NewID(), Name: "助理", Description: "测试用", SystemPrompt: "你是助理",
		Model: model.ModelRef{ProviderID: "prov", Model: "gpt-x"},
		Tools: []string{"fs.read"}, Extensions: []string{"ext1"},
		Autonomy: model.DefaultAutonomy, WorkspaceDir: "ws", Template: "default", Enabled: true,
		CreatedAt: now, UpdatedAt: now,
	}
}

func mustCreateAgent(t *testing.T, s model.Store) *model.Agent {
	t.Helper()
	a := newAgent()
	if err := s.CreateAgent(context.Background(), a); err != nil {
		t.Fatalf("CreateAgent 应成功: %v", err)
	}
	return a
}

func newSession(agentID, key string) *model.Session {
	now := ts()
	return &model.Session{
		ID: model.NewID(), AgentID: agentID, Title: "标题", Origin: model.OriginDesktop, Key: key,
		Status: model.SessionActive, CreatedAt: now, UpdatedAt: now,
	}
}

func mustCreateSession(t *testing.T, s model.Store, agentID, key string) *model.Session {
	t.Helper()
	sess := newSession(agentID, key)
	if err := s.CreateSession(context.Background(), sess); err != nil {
		t.Fatalf("CreateSession 应成功: %v", err)
	}
	return sess
}

func newRun(sess *model.Session) *model.Run {
	return &model.Run{
		ID: model.NewID(), SessionID: sess.ID, AgentID: sess.AgentID, Status: model.RunQueued,
		Trigger: model.TriggerUser, CreatedAt: ts(),
	}
}

func mustCreateRun(t *testing.T, s model.Store, sess *model.Session) *model.Run {
	t.Helper()
	r := newRun(sess)
	if err := s.CreateRun(context.Background(), r); err != nil {
		t.Fatalf("CreateRun 应成功: %v", err)
	}
	return r
}

// assertSameJSON 用 JSON 编码比较两个值；时间按 RFC 3339 比较，避免单调时钟与 Location 指针干扰。
func assertSameJSON(t *testing.T, what string, want, got any) {
	t.Helper()
	w, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("序列化期望值失败: %v", err)
	}
	g, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("序列化实际值失败: %v", err)
	}
	if string(w) != string(g) {
		t.Fatalf("%s 读回的内容应与写入一致\n写入: %s\n读回: %s", what, w, g)
	}
}

func assertCode(t *testing.T, err error, code model.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("期望错误码 %s，实际没有错误", code)
	}
	if got := model.CodeOf(err); got != code {
		t.Fatalf("期望错误码 %s，实际 %s: %v", code, got, err)
	}
}

// rawDB 以独立连接打开同一个库，用于在测试里篡改数据或检查 PRAGMA。
func rawDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open(driverName, path)
	if err != nil {
		t.Fatalf("打开原始连接失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// ── Open 与迁移 ──────────────────────────────────────────────────────────────

func TestOpenCreatesDatabaseWithRestrictedPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "home")
	path := filepath.Join(dir, "gogosling.db")
	s, err := Open(context.Background(), path, WithLogger(testLogger()))
	if err != nil {
		t.Fatalf("Open 应成功: %v", err)
	}
	defer s.Close()

	if runtime.GOOS != "windows" {
		di, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("数据目录应存在: %v", err)
		}
		if got := di.Mode().Perm(); got != 0o700 {
			t.Errorf("数据目录权限应为 0700，实际 %o", got)
		}
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatalf("数据库文件应存在: %v", err)
		}
		if got := fi.Mode().Perm(); got != 0o600 {
			t.Errorf("数据库文件权限应为 0600，实际 %o", got)
		}
	}
	if s.Path() != path {
		t.Errorf("Path 应返回绝对路径 %s，实际 %s", path, s.Path())
	}

	ctx := context.Background()
	v, err := s.Version(ctx)
	if err != nil || v != latestTestSchema(t) {
		t.Fatalf("初始模式版本应为最新版本，实际 %d, err=%v", v, err)
	}
	var mode string
	if err := s.reader.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Errorf("journal_mode 应为 wal，实际 %q err=%v", mode, err)
	}
	var fk int
	if err := s.reader.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
		t.Errorf("foreign_keys 应开启，实际 %d err=%v", fk, err)
	}
	var busy int
	if err := s.writer.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busy); err != nil || busy != 5000 {
		t.Errorf("busy_timeout 应为 5000，实际 %d err=%v", busy, err)
	}
}

func TestOpen拒绝损坏的数据库(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.db")
	// 只保留文件头，页数据不完整。打开或完整性检查都应失败，不能当成空库继续用。
	header := append([]byte("SQLite format 3\x00"), make([]byte, 80)...)
	if err := os.WriteFile(path, header, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), path, WithLogger(testLogger())); err == nil {
		t.Fatal("损坏的数据库应拒绝打开")
	}
}

func TestOpenTwiceIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gogosling.db")
	ctx := context.Background()

	s1, err := Open(ctx, path, WithLogger(testLogger()))
	if err != nil {
		t.Fatalf("第一次 Open 应成功: %v", err)
	}
	a := mustCreateAgent(t, s1)
	if err := s1.Close(); err != nil {
		t.Fatalf("Close 应成功: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("重复 Close 应无副作用: %v", err)
	}

	s2, err := Open(ctx, path, WithLogger(testLogger()))
	if err != nil {
		t.Fatalf("第二次 Open 应成功且不重复执行迁移: %v", err)
	}
	defer s2.Close()
	if v, _ := s2.Version(ctx); v != latestTestSchema(t) {
		t.Errorf("版本应仍为最新版本，实际 %d", v)
	}
	var n int
	if err := s2.reader.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&n); err != nil || n != latestTestSchema(t) {
		t.Errorf("schema_migrations 应每个版本只有一条记录，实际 %d err=%v", n, err)
	}
	if _, err := s2.GetAgent(ctx, a.ID); err != nil {
		t.Errorf("重新打开后数据应保留: %v", err)
	}
}

func TestOpenRefusesNewerSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gogosling.db")
	ctx := context.Background()
	s, err := Open(ctx, path, WithLogger(testLogger()))
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	db := rawDB(t, path)
	if _, err := db.ExecContext(ctx,
		"INSERT INTO schema_migrations(version, name, applied_at) VALUES (999, 'future', 'x')"); err != nil {
		t.Fatal(err)
	}
	_, err = Open(ctx, path, WithLogger(testLogger()))
	assertCode(t, err, model.CodeConflict)
}

func TestOpenRefusesMismatchedMigrationName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gogosling.db")
	ctx := context.Background()
	s, err := Open(ctx, path, WithLogger(testLogger()))
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	db := rawDB(t, path)
	if _, err := db.ExecContext(ctx, "UPDATE schema_migrations SET name = 'other' WHERE version = 1"); err != nil {
		t.Fatal(err)
	}
	_, err = Open(ctx, path, WithLogger(testLogger()))
	assertCode(t, err, model.CodeConflict)
}

func TestOpenRejectsEmptyPath(t *testing.T) {
	// 空路径会被解析成当前目录，是目录而不是文件，Open 必须报错而不是悄悄建库。
	_, err := Open(context.Background(), t.TempDir(), WithLogger(testLogger()))
	if err == nil {
		t.Fatal("把目录当作数据库路径应失败")
	}
}

// ── 事务 ────────────────────────────────────────────────────────────────────

func TestTxCommitsOnSuccess(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a := newAgent()
	err := s.Tx(ctx, func(tx model.Store) error {
		if err := tx.CreateAgent(ctx, a); err != nil {
			return err
		}
		return tx.SetSetting(ctx, "locale", `"zh"`)
	})
	if err != nil {
		t.Fatalf("Tx 应成功: %v", err)
	}
	if _, err := s.GetAgent(ctx, a.ID); err != nil {
		t.Errorf("事务提交后应能读到: %v", err)
	}
	if v, err := s.GetSetting(ctx, "locale"); err != nil || v != `"zh"` {
		t.Errorf("事务提交后设置应存在: %q err=%v", v, err)
	}
}

func TestTxRollsBackOnError(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a := newAgent()
	sentinel := errors.New("boom")
	err := s.Tx(ctx, func(tx model.Store) error {
		if err := tx.CreateAgent(ctx, a); err != nil {
			return err
		}
		if _, err := tx.GetAgent(ctx, a.ID); err != nil {
			t.Errorf("事务内应能读到自己的写入: %v", err)
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Tx 应原样返回回调错误，实际 %v", err)
	}
	_, err = s.GetAgent(ctx, a.ID)
	assertCode(t, err, model.CodeNotFound)
}

func TestTxRollsBackOnPanicAndStoreStaysUsable(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a := newAgent()
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("panic 应向上传播")
			}
		}()
		_ = s.Tx(ctx, func(tx model.Store) error {
			if err := tx.CreateAgent(ctx, a); err != nil {
				return err
			}
			panic("boom")
		})
	}()
	_, err := s.GetAgent(ctx, a.ID)
	assertCode(t, err, model.CodeNotFound)

	// 写连接必须已经归还：后续写入不能卡住。
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.CreateAgent(wctx, newAgent()); err != nil {
		t.Fatalf("panic 回滚后写连接应可用: %v", err)
	}
}

func TestTxCannotBeNested(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	err := s.Tx(ctx, func(tx model.Store) error {
		return tx.Tx(ctx, func(model.Store) error { return nil })
	})
	assertCode(t, err, model.CodeInternal)
}

func TestTxStoreCannotBeClosed(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	err := s.Tx(ctx, func(tx model.Store) error { return tx.Close() })
	assertCode(t, err, model.CodeInternal)
}

func TestTxIsIsolatedFromReaders(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a := newAgent()
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.Tx(ctx, func(tx model.Store) error {
			if err := tx.CreateAgent(ctx, a); err != nil {
				return err
			}
			close(started)
			<-release
			return nil
		})
	}()
	<-started

	// WAL 下读者不被写事务阻塞，且看不到未提交的写入。
	rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err := s.GetAgent(rctx, a.ID)
	assertCode(t, err, model.CodeNotFound)

	// 第二个写者必须等待写连接；等待尊重 ctx，超时后返回 CodeTimeout 而不是死锁或 SQLITE_BUSY。
	wctx, wcancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer wcancel()
	err = s.CreateAgent(wctx, newAgent())
	assertCode(t, err, model.CodeTimeout)

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("事务应提交成功: %v", err)
	}
	if _, err := s.GetAgent(ctx, a.ID); err != nil {
		t.Errorf("提交后读者应能看到: %v", err)
	}
	if err := s.CreateAgent(ctx, newAgent()); err != nil {
		t.Errorf("写连接释放后写入应成功: %v", err)
	}
}

func TestCancelledContextMapsToCancelled(t *testing.T) {
	s := openTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.ListAgents(ctx)
	assertCode(t, err, model.CodeCancelled)
	err = s.CreateAgent(ctx, newAgent())
	assertCode(t, err, model.CodeCancelled)
}

// ── 并发 ────────────────────────────────────────────────────────────────────

func TestConcurrentReadersAndWritersNeverSeeBusy(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	agent := mustCreateAgent(t, s)
	sess := mustCreateSession(t, s, agent.ID, "k1")

	const writers, perWriter, readers = 8, 40, 8
	errs := make(chan error, writers*perWriter+readers*4)
	var wg sync.WaitGroup
	stop := make(chan struct{})

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				m := model.TextMessage(sess.ID, model.RoleUser, "hi")
				var err error
				if (w+i)%4 == 0 {
					// 一部分写者走显式事务：追加消息并改标题。
					err = s.Tx(ctx, func(tx model.Store) error {
						if err := tx.AppendMessage(ctx, &m); err != nil {
							return err
						}
						got, err := tx.GetSession(ctx, sess.ID)
						if err != nil {
							return err
						}
						got.Title = "t"
						got.UpdatedAt = ts()
						return tx.UpdateSession(ctx, got)
					})
				} else {
					err = s.AppendMessage(ctx, &m)
				}
				if err != nil {
					errs <- err
				}
			}
		}(w)
	}

	var rg sync.WaitGroup
	for r := 0; r < readers; r++ {
		rg.Add(1)
		go func() {
			defer rg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := s.ListMessages(ctx, sess.ID, 0, 20); err != nil {
					errs <- err
					return
				}
				if _, err := s.GetSession(ctx, sess.ID); err != nil {
					errs <- err
					return
				}
				if _, err := s.ListActiveRuns(ctx); err != nil {
					errs <- err
					return
				}
			}
		}()
	}

	wg.Wait()
	close(stop)
	rg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("并发读写不应出错: %v", err)
	}

	msgs, err := s.ListMessages(ctx, sess.ID, 0, writers*perWriter+1)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != writers*perWriter {
		t.Fatalf("应写入 %d 条消息，实际 %d", writers*perWriter, len(msgs))
	}
	for i, m := range msgs {
		if m.Seq != int64(i+1) {
			t.Fatalf("Seq 应连续递增：第 %d 条的 Seq 为 %d", i, m.Seq)
		}
	}
}

// ── 级联删除 ────────────────────────────────────────────────────────────────

func TestDeleteSessionCascadesMessagesRunsApprovals(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	agent := mustCreateAgent(t, s)
	sess := mustCreateSession(t, s, agent.ID, "k")
	m := model.TextMessage(sess.ID, model.RoleUser, "hello")
	if err := s.AppendMessage(ctx, &m); err != nil {
		t.Fatal(err)
	}
	run := mustCreateRun(t, s, sess)
	ap := &model.ApprovalRequest{
		ID: model.NewID(), RunID: run.ID, SessionID: sess.ID, AgentID: agent.ID,
		ToolCall: model.ToolCall{ID: "c1", Name: "shell", Arguments: json.RawMessage(`{"cmd":"ls"}`)},
		Risk:     model.RiskExecute, Summary: "运行 ls", Status: model.ApprovalPending,
		CreatedAt: ts(), ExpiresAt: ts().Add(time.Minute),
	}
	if err := s.CreateApproval(ctx, ap); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteSession(ctx, sess.ID); err != nil {
		t.Fatalf("DeleteSession 应成功: %v", err)
	}
	_, err := s.GetSession(ctx, sess.ID)
	assertCode(t, err, model.CodeNotFound)
	_, err = s.GetMessage(ctx, m.ID)
	assertCode(t, err, model.CodeNotFound)
	_, err = s.GetRun(ctx, run.ID)
	assertCode(t, err, model.CodeNotFound)
	_, err = s.GetApproval(ctx, ap.ID)
	assertCode(t, err, model.CodeNotFound)
	if _, err := s.GetAgent(ctx, agent.ID); err != nil {
		t.Errorf("删除会话不应影响智能体: %v", err)
	}
	err = s.DeleteSession(ctx, sess.ID)
	assertCode(t, err, model.CodeNotFound)
}

func TestDeleteAgentCascadesSessionsJobsChannels(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	agent := mustCreateAgent(t, s)
	sess := mustCreateSession(t, s, agent.ID, "k")
	m := model.TextMessage(sess.ID, model.RoleUser, "hello")
	if err := s.AppendMessage(ctx, &m); err != nil {
		t.Fatal(err)
	}
	run := mustCreateRun(t, s, sess)
	job := &model.Job{
		ID: model.NewID(), AgentID: agent.ID, Name: "每日", Schedule: "@daily", Kind: model.JobPushText,
		Input: "早安", MissedPolicy: model.MissedSkip, Enabled: true, CreatedAt: ts(), UpdatedAt: ts(),
	}
	if err := s.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	ch := &model.Channel{
		ID: model.NewID(), AgentID: agent.ID, Kind: model.ChannelTelegram, Name: "tg",
		SecretRef: "telegram/bot", Enabled: true, CreatedAt: ts(), UpdatedAt: ts(),
	}
	if err := s.CreateChannel(ctx, ch); err != nil {
		t.Fatal(err)
	}
	audit := &model.AuditEntry{
		ID: model.NewID(), At: ts(), Actor: "user", AgentID: agent.ID, Action: "agent.create", Outcome: "ok",
	}
	if err := s.AppendAudit(ctx, audit); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteAgent(ctx, agent.ID); err != nil {
		t.Fatalf("DeleteAgent 应成功: %v", err)
	}
	for name, get := range map[string]func() error{
		"session": func() error { _, err := s.GetSession(ctx, sess.ID); return err },
		"message": func() error { _, err := s.GetMessage(ctx, m.ID); return err },
		"run":     func() error { _, err := s.GetRun(ctx, run.ID); return err },
		"job":     func() error { _, err := s.GetJob(ctx, job.ID); return err },
		"channel": func() error { _, err := s.GetChannel(ctx, ch.ID); return err },
	} {
		if err := get(); !model.IsCode(err, model.CodeNotFound) {
			t.Errorf("删除智能体后 %s 应被级联删除，实际 %v", name, err)
		}
	}
	entries, err := s.ListAudit(ctx, model.AuditFilter{AgentID: agent.ID}, model.Page{})
	if err != nil || len(entries) != 1 {
		t.Errorf("审计记录不应随智能体删除: %d 条, err=%v", len(entries), err)
	}
}
