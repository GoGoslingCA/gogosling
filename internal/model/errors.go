package model

import (
	"errors"
	"fmt"
)

// Code 是跨模块、跨进程稳定的错误码。
//
// 传输层把它映射为 HTTP 状态码与本地化文案（i18n 键为 errors.<code>）；
// 领域包只负责选对错误码，不关心如何展示。
type Code string

const (
	// CodeInvalid 表示输入不合法（HTTP 400）。
	CodeInvalid Code = "invalid"
	// CodeUnauthorized 表示未提供或提供了无效的访问令牌（HTTP 401）。
	CodeUnauthorized Code = "unauthorized"
	// CodeForbidden 表示身份合法但无权执行（HTTP 403）。
	CodeForbidden Code = "forbidden"
	// CodeNotFound 表示资源不存在（HTTP 404）。
	CodeNotFound Code = "not_found"
	// CodeConflict 表示与当前状态冲突，例如重复名称或非法的状态迁移（HTTP 409）。
	CodeConflict Code = "conflict"
	// CodeDenied 表示操作被权限规则或用户审批拒绝（HTTP 403）。
	CodeDenied Code = "denied"
	// CodeUnavailable 表示依赖的组件暂时不可用，例如工作进程未就绪（HTTP 503）。
	CodeUnavailable Code = "unavailable"
	// CodeTimeout 表示操作超时（HTTP 504）。
	CodeTimeout Code = "timeout"
	// CodeCancelled 表示操作被取消（HTTP 499 语义，映射为 409）。
	CodeCancelled Code = "cancelled"
	// CodeInternal 表示未预期的内部错误（HTTP 500）。
	CodeInternal Code = "internal"
)

// Error 是带错误码的错误。所有从领域包返回给传输层的错误都应当是它或包装了它。
type Error struct {
	Code    Code
	Message string
	// Details 是可选的结构化补充信息，会原样返回给客户端，因此不得包含敏感数据。
	Details map[string]any
	// Err 是被包装的底层错误，只进日志，不返回给客户端。
	Err error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap 让 errors.Is / errors.As 能穿透到底层错误。
func (e *Error) Unwrap() error { return e.Err }

// Is 让 errors.Is(err, &Error{Code: X}) 可以按错误码匹配。
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code
}

// NewError 创建一个带错误码的错误。
func NewError(code Code, message string) *Error {
	return &Error{Code: code, Message: message}
}

// Errorf 创建一个带错误码的格式化错误。
func Errorf(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Wrap 用错误码包装底层错误；err 为 nil 时返回 nil。
func Wrap(code Code, message string, err error) error {
	if err == nil {
		return nil
	}
	return &Error{Code: code, Message: message, Err: err}
}

// CodeOf 返回错误链中第一个 *Error 的错误码；没有则视为内部错误。
func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return CodeInternal
}

// IsCode 判断错误链中是否包含指定错误码。
func IsCode(err error, code Code) bool {
	return err != nil && CodeOf(err) == code
}

// NotFound 是 CodeNotFound 的便捷构造：资源类型与标识会写入 Details。
func NotFound(kind, id string) *Error {
	return &Error{
		Code:    CodeNotFound,
		Message: kind + " 不存在",
		Details: map[string]any{"kind": kind, "id": id},
	}
}
