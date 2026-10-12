package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// wrapErr 把驱动与 database/sql 的错误映射为带 model.Code 的错误。
//
// 映射规则：
//   - 已经是 *model.Error 的原样返回（例如调用点先判出的 NotFound）。
//   - sql.ErrNoRows 不在这里处理：它需要资源类型与 ID，由调用点转成 model.NotFound。
//   - ctx 已取消 / 超时 → CodeCancelled / CodeTimeout（驱动在这种情况下通常报 SQLITE_INTERRUPT）。
//   - SQLITE_CONSTRAINT_UNIQUE / PRIMARYKEY → CodeConflict；FOREIGNKEY → CodeConflict；
//     NOTNULL / CHECK → CodeInvalid；其他约束 → CodeInvalid。
//   - SQLITE_BUSY / SQLITE_LOCKED → CodeUnavailable（进程内写者已串行化，这只会来自跨进程竞争）。
//   - 其余 → CodeInternal。
//
// Details 只放约束类别，不放 SQLite 消息（消息含表名列名，但不含行数据；仍只留在 Err 里进日志）。
func wrapErr(ctx context.Context, op string, err error) error {
	if err == nil {
		return nil
	}
	var me *model.Error
	if errors.As(err, &me) {
		return err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			return model.Wrap(model.CodeTimeout, "store: "+op+" 超时", err)
		}
		return model.Wrap(model.CodeCancelled, "store: "+op+" 已取消", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return model.Wrap(model.CodeTimeout, "store: "+op+" 超时", err)
	}
	if errors.Is(err, context.Canceled) {
		return model.Wrap(model.CodeCancelled, "store: "+op+" 已取消", err)
	}
	if errors.Is(err, sql.ErrTxDone) || errors.Is(err, sql.ErrConnDone) {
		return model.Wrap(model.CodeInternal, "store: "+op+" 在已结束的事务或连接上执行", err)
	}

	var se *sqlite.Error
	if errors.As(err, &se) {
		code := se.Code()
		switch code & 0xff {
		case sqlite3.SQLITE_CONSTRAINT:
			return constraintErr(op, code, se.Error(), err)
		case sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED:
			return model.Wrap(model.CodeUnavailable, "store: "+op+" 时数据库忙", err)
		case sqlite3.SQLITE_INTERRUPT:
			return model.Wrap(model.CodeCancelled, "store: "+op+" 被中断", err)
		}
	}
	return model.Wrap(model.CodeInternal, "store: "+op+" 失败", err)
}

// constraintErr 细分约束失败。扩展错误码由驱动默认开启；万一拿到的是主码 19，
// 退回到按消息文本判断 UNIQUE。
func constraintErr(op string, code int, msg string, err error) error {
	kind := "constraint"
	switch code {
	case sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY:
		kind = "unique"
	case sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY:
		kind = "foreign_key"
	case sqlite3.SQLITE_CONSTRAINT_NOTNULL:
		kind = "not_null"
	case sqlite3.SQLITE_CONSTRAINT_CHECK:
		kind = "check"
	default:
		switch {
		case strings.Contains(msg, "UNIQUE constraint failed"):
			kind = "unique"
		case strings.Contains(msg, "FOREIGN KEY constraint failed"):
			kind = "foreign_key"
		case strings.Contains(msg, "NOT NULL constraint failed"):
			kind = "not_null"
		case strings.Contains(msg, "CHECK constraint failed"):
			kind = "check"
		}
	}
	switch kind {
	case "unique":
		return &model.Error{
			Code:    model.CodeConflict,
			Message: "store: " + op + " 与已有记录冲突（唯一键重复）",
			Details: map[string]any{"constraint": kind},
			Err:     err,
		}
	case "foreign_key":
		return &model.Error{
			Code:    model.CodeConflict,
			Message: "store: " + op + " 引用的记录不存在",
			Details: map[string]any{"constraint": kind},
			Err:     err,
		}
	default:
		return &model.Error{
			Code:    model.CodeInvalid,
			Message: "store: " + op + " 违反数据约束",
			Details: map[string]any{"constraint": kind},
			Err:     err,
		}
	}
}

// notFoundOr 把 sql.ErrNoRows 转成 NotFound，其余交给 wrapErr。
func notFoundOr(ctx context.Context, op, kind, id string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return model.NotFound(kind, id)
	}
	return wrapErr(ctx, op, err)
}
