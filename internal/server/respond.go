package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/GoGoslingCA/gogosling/internal/i18n"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

const (
	// defaultPageLimit 与 maxPageLimit 是列表分页的缺省值与上限。
	defaultPageLimit = 100
	maxPageLimit     = 500
)

// statusFor 把错误码映射为 HTTP 状态码（契约见 docs/architecture.md §8）。
func statusFor(code model.Code) int {
	switch code {
	case model.CodeInvalid:
		return http.StatusBadRequest
	case model.CodeUnauthorized:
		return http.StatusUnauthorized
	case model.CodeForbidden, model.CodeDenied:
		return http.StatusForbidden
	case model.CodeNotFound:
		return http.StatusNotFound
	case model.CodeConflict, model.CodeCancelled:
		return http.StatusConflict
	case model.CodeUnavailable:
		return http.StatusServiceUnavailable
	case model.CodeTimeout:
		return http.StatusGatewayTimeout
	default:
		return http.StatusInternalServerError
	}
}

// errorBody 是错误信封 {"error": {...}} 的内容。
type errorBody struct {
	Code    model.Code     `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

type errorEnvelope struct {
	Error errorBody `json:"error"`
	// Run 只在"运行已落盘后出错"的发送消息响应里出现，便于客户端拿到 run_id。
	Run *model.Run `json:"run,omitempty"`
}

// writeError 按错误码写出本地化的错误信封。
func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	s.writeErrorFull(w, r, 0, err, nil)
}

// writeErrorStatus 与 writeError 相同，但使用指定的 HTTP 状态码（405 等不在映射表中的情形）。
func (s *Server) writeErrorStatus(w http.ResponseWriter, r *http.Request, status int, err error) {
	s.writeErrorFull(w, r, status, err, nil)
}

// writeErrorFull 写出错误信封。internal 错误只记日志、不返回任何细节；
// 其他错误返回领域层给出的 Details（契约要求其中不含敏感数据），并以 details.detail
// 附上领域层的原始说明（未本地化，供排障；底层 Err 链从不返回）。details.reason 保留给机器可读的原因码。
func (s *Server) writeErrorFull(w http.ResponseWriter, r *http.Request, status int, err error, run *model.Run) {
	code := model.CodeOf(err)
	if status == 0 {
		status = statusFor(code)
	}
	attrs := []any{"method", r.Method, "path", r.URL.Path, "code", string(code), "error", err}
	switch {
	case code == model.CodeInternal:
		s.log.Error("请求处理失败", attrs...)
	case status >= 500:
		s.log.Warn("请求处理失败", attrs...)
	default:
		s.log.Debug("请求被拒绝", attrs...)
	}

	body := errorBody{Code: code, Message: i18n.ErrorMessage(s.localeOf(r), string(code), nil)}
	if code != model.CodeInternal {
		var me *model.Error
		if errors.As(err, &me) {
			if reason, ok := me.Details["reason"].(string); ok && strings.HasPrefix(reason, "prompt_enhance_") {
				body.Message = i18n.T(s.localeOf(r), "enhance."+strings.TrimPrefix(reason, "prompt_enhance_"), nil)
			}
			if reason, ok := me.Details["reason"].(string); ok && strings.HasPrefix(reason, "preview_") {
				body.Message = i18n.T(s.localeOf(r), "preview."+strings.TrimPrefix(reason, "preview_"), nil)
			}
			if me.Details["reason"] == "resource_queue_full" {
				body.Message = i18n.T(s.localeOf(r), "resources.queue_full", nil)
			}
			if reason, ok := me.Details["reason"].(string); ok && strings.HasPrefix(reason, "change_") {
				if localized, exists := i18n.Lookup(s.localeOf(r), "workbench."+reason); exists {
					body.Message = localized
				}
			}
			if len(me.Details) > 0 {
				body.Details = make(map[string]any, len(me.Details)+1)
				for k, v := range me.Details {
					body.Details[k] = v
				}
			}
			if me.Message != "" {
				if _, ok := body.Details["detail"]; !ok {
					if body.Details == nil {
						body.Details = map[string]any{}
					}
					body.Details["detail"] = me.Message
				}
			}
		}
	}
	env := errorEnvelope{Error: body}
	if run != nil {
		env.Run = s.runView(r, run)
	}
	data, mErr := json.Marshal(env)
	if mErr != nil {
		// Details 中出现不可序列化的值时退回到不带细节的信封。
		s.log.Error("序列化错误信封失败", "error", mErr)
		env.Error.Details = nil
		data, _ = json.Marshal(env)
	}
	writeRaw(w, status, data)
}

// writeJSON 以 JSON 写出响应体。
func writeJSON(w http.ResponseWriter, status int, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		writeRaw(w, http.StatusInternalServerError,
			[]byte(`{"error":{"code":"internal","message":"internal"}}`))
		return
	}
	writeRaw(w, status, data)
}

func writeRaw(w http.ResponseWriter, status int, data []byte) {
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(data)
	_, _ = w.Write([]byte{'\n'})
}

// writeNoContent 写出 204。
func writeNoContent(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

// itemsResponse 是列表接口统一的 {"items": [...]}；空列表编码为 []。
type itemsResponse[T any] struct {
	Items []T `json:"items"`
}

func writeItems[T any](w http.ResponseWriter, items []T) {
	if items == nil {
		items = []T{}
	}
	writeJSON(w, http.StatusOK, itemsResponse[T]{Items: items})
}

// decodeJSON 解码请求体为单个 JSON 值。Content-Type 存在时必须是 JSON；
// 空体、语法错误、类型不符、超限与尾随数据都返回 CodeInvalid（details.reason 说明原因）。
func decodeJSON(r *http.Request, v any) error {
	if ct := r.Header.Get("Content-Type"); ct != "" {
		mt, _, err := mime.ParseMediaType(ct)
		if err != nil || (mt != "application/json" && !strings.HasSuffix(mt, "+json")) {
			return invalid("content_type", "请求体必须是 application/json", map[string]any{"content_type": ct})
		}
	}
	if r.Body == nil {
		return invalid("empty_body", "请求体为空", nil)
	}
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		return decodeError(err)
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return decodeError(err)
		}
		return invalid("trailing_data", "请求体只能包含一个 JSON 值", nil)
	}
	return nil
}

// decodeOptionalJSON 与 decodeJSON 相同，但请求体可以为空（OpenAPI 中 requestBody 非 required 的端点）：
// 没有请求体或请求体为空时 v 保持零值并返回 nil。
func decodeOptionalJSON(r *http.Request, v any) error {
	if r.Body == nil || r.Body == http.NoBody || r.ContentLength == 0 {
		return nil
	}
	err := decodeJSON(r, v)
	var me *model.Error
	if errors.As(err, &me) && me.Details["reason"] == "empty_body" {
		return nil
	}
	return err
}

func decodeError(err error) error {
	var (
		mbe *http.MaxBytesError
		se  *json.SyntaxError
		ute *json.UnmarshalTypeError
	)
	switch {
	case errors.As(err, &mbe):
		return bodyTooLarge(mbe.Limit, err)
	case errors.Is(err, io.EOF):
		return invalid("empty_body", "请求体为空", nil)
	case errors.As(err, &se):
		e := invalid("malformed_json", "请求体不是合法的 JSON", map[string]any{"offset": se.Offset})
		e.Err = err
		return e
	case errors.As(err, &ute):
		e := invalid("wrong_type", "字段类型不正确", map[string]any{"field": ute.Field, "expected": ute.Type.String()})
		e.Err = err
		return e
	default:
		e := invalid("malformed_json", "请求体不是合法的 JSON", nil)
		e.Err = err
		return e
	}
}

func bodyTooLarge(limit int64, err error) error {
	return &model.Error{
		Code:    model.CodeInvalid,
		Message: "请求体超过上限",
		Details: map[string]any{"reason": "body_too_large", "max_bytes": limit},
		Err:     err,
	}
}

// invalid 构造带 details.reason 的 CodeInvalid 错误。
func invalid(reason, message string, extra map[string]any) *model.Error {
	d := make(map[string]any, len(extra)+1)
	for k, v := range extra {
		d[k] = v
	}
	d["reason"] = reason
	return &model.Error{Code: model.CodeInvalid, Message: message, Details: d}
}

func missingField(field string) error {
	return invalid("missing_field", "缺少必填字段 "+field, map[string]any{"field": field})
}

func badValue(field string, value any) error {
	return invalid("invalid_value", "字段 "+field+" 的取值不受支持", map[string]any{"field": field, "value": value})
}

// parsePage 解析 limit / offset：limit 缺省 100、超过 500 按 500；非数字或负数返回 CodeInvalid。
func parsePage(r *http.Request) (model.Page, error) {
	q := r.URL.Query()
	p := model.Page{Limit: defaultPageLimit}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return p, badValue("limit", v)
		}
		if n > 0 {
			p.Limit = min(n, maxPageLimit)
		}
	}
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return p, badValue("offset", v)
		}
		p.Offset = n
	}
	return p, nil
}

// optional 区分 JSON 字段的三种状态：缺席（Set=false）、显式 null（Null=true）与给出值。
// 用于 PATCH 中"不修改 / 重置 / 设为某值"语义不同的字段（例如智能体的 tools）。
type optional[T any] struct {
	Set   bool
	Null  bool
	Value T
}

// UnmarshalJSON 实现 json.Unmarshaler；字段缺席时不会被调用，因此 Set 保持 false。
func (o *optional[T]) UnmarshalJSON(b []byte) error {
	o.Set = true
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		o.Null = true
		var zero T
		o.Value = zero
		return nil
	}
	o.Null = false
	return json.Unmarshal(b, &o.Value)
}

// localizeRunError 把写入 Run.Error 的机器常量（session.Error*、engine.Error*，例如 "daemon restarted"、
// "worker crashed"）换成请求语言的 run.error.* 文案；映射规则只有 i18n.RunErrorText 一份
// （常量的空格换成下划线即为键名）。其他文本（工作进程或模型服务给出的原因）原样返回。
func localizeRunError(locale, msg string) string {
	if text, ok := i18n.RunErrorText(locale, msg); ok {
		return text
	}
	return msg
}

// runView 返回用于响应的运行记录副本，Error 已按请求语言本地化。
func (s *Server) runView(r *http.Request, run *model.Run) *model.Run {
	if run == nil {
		return nil
	}
	cp := *run
	cp.Error = localizeRunError(s.localeOf(r), cp.Error)
	return &cp
}
