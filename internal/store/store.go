package store

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	_ "modernc.org/sqlite" // 注册 "sqlite" 驱动

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// driverName 是 modernc.org/sqlite 注册的 database/sql 驱动名。
const driverName = "sqlite"

const (
	defaultBusyTimeout = 5 * time.Second
	defaultReaders     = 4
	defaultPageLimit   = 100
)

// Option 调整 Open 的行为。
type Option func(*options)

type options struct {
	log         *slog.Logger
	readers     int
	busyTimeout time.Duration
}

// WithLogger 注入日志器；默认丢弃日志。
func WithLogger(l *slog.Logger) Option {
	return func(o *options) {
		if l != nil {
			o.log = l
		}
	}
}

// WithReaders 设置读连接池的大小（至少 1，默认 4）。
func WithReaders(n int) Option {
	return func(o *options) {
		if n > 0 {
			o.readers = n
		}
	}
}

// WithBusyTimeout 设置 SQLite busy_timeout（默认 5s）。它只影响跨进程竞争；
// 进程内的写操作由写连接池串行化，不依赖这个超时。
func WithBusyTimeout(d time.Duration) Option {
	return func(o *options) {
		if d > 0 {
			o.busyTimeout = d
		}
	}
}

// dbtx 是 *sql.DB 与 *sql.Tx 的公共子集；仓储方法只通过它访问数据库。
type dbtx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Store 实现 model.Store。零值不可用，请用 Open 创建。
//
// 并发安全：根 Store 可被任意多个 goroutine 同时使用；Tx 回调收到的 Store 只在
// 回调内、由回调所在的 goroutine 使用。
type Store struct {
	path string
	log  *slog.Logger

	reader *sql.DB // 读连接池（根 Store）
	writer *sql.DB // 写连接池，MaxOpenConns=1，BEGIN IMMEDIATE（根 Store）
	tx     *sql.Tx // 非 nil 表示这是事务内的 Store，读写都走它

	closeOnce sync.Once
	closeErr  error
}

var _ model.Store = (*Store)(nil)

// Open 打开（必要时创建）path 指向的 SQLite 数据库并应用全部待执行迁移。
//
// 父目录以 0700 创建，数据库文件以 0600 创建；WAL 与 SHM 文件由 SQLite 沿用
// 主文件权限。若数据库的模式版本高于本程序支持的版本，返回 CodeConflict 以
// 阻止降级（见架构文档 §6.6）。
func Open(ctx context.Context, path string, opts ...Option) (*Store, error) {
	o := options{
		log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		readers:     defaultReaders,
		busyTimeout: defaultBusyTimeout,
	}
	for _, opt := range opts {
		opt(&o)
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, model.Wrap(model.CodeInvalid, "store: 数据库路径无效", err)
	}
	if err := prepareFile(abs); err != nil {
		return nil, err
	}

	writer, err := openPool(buildDSN(abs, o.busyTimeout, true), 1)
	if err != nil {
		return nil, err
	}
	reader, err := openPool(buildDSN(abs, o.busyTimeout, false), o.readers)
	if err != nil {
		_ = writer.Close()
		return nil, err
	}

	s := &Store{path: abs, log: o.log, reader: reader, writer: writer}
	if err := s.migrate(ctx); err != nil {
		_ = s.Close()
		return nil, err
	}
	if err := s.checkIntegrity(ctx); err != nil {
		_ = s.Close()
		return nil, err
	}
	// 让读池至少建立一条连接，尽早暴露 DSN 或文件权限问题。
	if err := reader.PingContext(ctx); err != nil {
		_ = s.Close()
		return nil, wrapErr(ctx, "open reader", err)
	}
	return s, nil
}

// checkIntegrity 在开始对外服务前确认库文件可读且结构完整。
// 损坏时拒绝打开，避免后续读写表现为随机失败。
func (s *Store) checkIntegrity(ctx context.Context) error {
	var result string
	if err := s.writer.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&result); err != nil {
		return wrapErr(ctx, "integrity", err)
	}
	if result != "ok" {
		s.log.Error("数据库完整性检查未通过", "result_bytes", len(result))
		return model.Errorf(model.CodeInternal, "数据库完整性检查未通过")
	}
	return nil
}

// prepareFile 创建父目录（0700）与空数据库文件（0600）。文件已存在时只收紧权限。
func prepareFile(abs string) error {
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return model.Wrap(model.CodeInternal, "store: 创建数据目录失败", err)
	}
	f, err := os.OpenFile(abs, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return model.Wrap(model.CodeInternal, "store: 创建数据库文件失败", err)
	}
	if err := f.Close(); err != nil {
		return model.Wrap(model.CodeInternal, "store: 关闭数据库文件失败", err)
	}
	if runtime.GOOS != "windows" {
		// O_CREATE 的权限受 umask 影响，且已存在的文件不会被改权限，这里再收紧一次。
		if err := os.Chmod(abs, 0o600); err != nil {
			return model.Wrap(model.CodeInternal, "store: 设置数据库文件权限失败", err)
		}
	}
	return nil
}

// buildDSN 组装 modernc 驱动的 DSN。路径直接作为文件名（不用 file: URI，避免 Windows
// 路径转义问题），查询串里的 _pragma 在每条新连接建立时执行。
func buildDSN(abs string, busy time.Duration, immediate bool) string {
	dsn := abs +
		"?_pragma=busy_timeout(" + strconv.FormatInt(busy.Milliseconds(), 10) + ")" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(ON)" +
		"&_pragma=synchronous(NORMAL)"
	if immediate {
		dsn += "&_txlock=immediate"
	}
	return dsn
}

func openPool(dsn string, size int) (*sql.DB, error) {
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, model.Wrap(model.CodeInternal, "store: 打开数据库失败", err)
	}
	db.SetMaxOpenConns(size)
	db.SetMaxIdleConns(size)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)
	return db, nil
}

// Path 返回数据库文件的绝对路径（备份与诊断用）。
func (s *Store) Path() string { return s.path }

// Close 关闭两个连接池；可重复调用。事务内的 Store 不能关闭。
func (s *Store) Close() error {
	if s.tx != nil {
		return model.NewError(model.CodeInternal, "store: 事务内的 Store 不能关闭")
	}
	s.closeOnce.Do(func() {
		var errs []error
		if s.reader != nil {
			if err := s.reader.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		if s.writer != nil {
			if err := s.writer.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		if len(errs) > 0 {
			s.closeErr = model.Wrap(model.CodeInternal, "store: 关闭数据库失败", errs[0])
		}
	})
	return s.closeErr
}

// Version 返回当前数据库的模式版本（已应用的最大迁移号；空库为 0）。
func (s *Store) Version(ctx context.Context) (int, error) {
	var v int
	err := s.rq().QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v)
	if err != nil {
		return 0, wrapErr(ctx, "read schema version", err)
	}
	return v, nil
}

// Tx 在一个 BEGIN IMMEDIATE 事务内执行 fn；fn 返回错误或 panic 时回滚。
// fn 收到的 Store 只在事务内有效，读操作也在该事务中执行（能看到未提交的写入）。
// 不支持嵌套：在事务内的 Store 上再调用 Tx 返回 CodeInternal。
func (s *Store) Tx(ctx context.Context, fn func(tx model.Store) error) error {
	if s.tx != nil {
		return model.NewError(model.CodeInternal, "store: 事务不可嵌套")
	}
	return s.write(ctx, func(q dbtx) error {
		return fn(&Store{path: s.path, log: s.log, tx: q.(*sql.Tx)})
	})
}

// rq 返回读操作使用的执行器：事务内用事务，否则用读池。
func (s *Store) rq() dbtx {
	if s.tx != nil {
		return s.tx
	}
	return s.reader
}

// write 在写事务内执行 fn。事务内的 Store 直接复用当前事务；根 Store 从写池取
// 唯一的写连接并 BEGIN IMMEDIATE，fn 返回 nil 则提交，否则回滚。
func (s *Store) write(ctx context.Context, fn func(q dbtx) error) (err error) {
	if s.tx != nil {
		return fn(s.tx)
	}
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return wrapErr(ctx, "begin", err)
	}
	done := false
	defer func() {
		if !done {
			_ = tx.Rollback()
		}
	}()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return wrapErr(ctx, "commit", err)
	}
	done = true
	return nil
}

// affected 把 UPDATE / DELETE 的结果转成"找到了吗"：0 行视为 NotFound。
func affected(ctx context.Context, op, kind, id string, res sql.Result, err error) error {
	if err != nil {
		return wrapErr(ctx, op, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return wrapErr(ctx, op, err)
	}
	if n == 0 {
		return model.NotFound(kind, id)
	}
	return nil
}

// requireID 校验调用方已填好主键。
func requireID(kind, id string) error {
	if id == "" {
		return model.Errorf(model.CodeInvalid, "store: %s.ID 不能为空", kind)
	}
	return nil
}

// pageOf 把 Page 规范化为 LIMIT / OFFSET。
func pageOf(p model.Page) (limit, offset int) {
	limit = p.Limit
	if limit <= 0 {
		limit = defaultPageLimit
	}
	offset = p.Offset
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// String 便于日志中识别实例，不暴露任何数据。
func (s *Store) String() string {
	if s.tx != nil {
		return fmt.Sprintf("store(tx %s)", filepath.Base(s.path))
	}
	return fmt.Sprintf("store(%s)", filepath.Base(s.path))
}
